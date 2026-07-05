package main

import (
	"crypto/sha1"
	"encoding/hex"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	xdraw "golang.org/x/image/draw"
)

var imageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".bmp":  true,
	".webp": true,
}

type thumbResult struct {
	path  string
	thumb image.Image
}

func isImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func scanFolderPaths(dir string) []string {
	var paths []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if isImageFile(d.Name()) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}

func loadThumbnail(path string, thumbW, thumbH int) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	// Center-crop the source to the thumbnail's aspect ratio before scaling so
	// the image fills the box without being stretched. Scaling the full source
	// into the fixed dst rectangle would squash it to 3:2 regardless of the
	// original proportions.
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	crop := sb
	if sw*thumbH > sh*thumbW {
		// Source is wider than the target: trim the sides.
		cw := sh * thumbW / thumbH
		x0 := sb.Min.X + (sw-cw)/2
		crop = image.Rect(x0, sb.Min.Y, x0+cw, sb.Max.Y)
	} else {
		// Source is taller than the target: trim top and bottom.
		ch := sw * thumbH / thumbW
		y0 := sb.Min.Y + (sh-ch)/2
		crop = image.Rect(sb.Min.X, y0, sb.Max.X, y0+ch)
	}

	dst := image.NewRGBA(image.Rect(0, 0, thumbW, thumbH))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), src, crop, xdraw.Src, nil)
	return dst, nil
}

// mirrorImage decodes the image at srcPath, flips it horizontally, and writes
// the result as a PNG into the user cache dir. It returns the path to the
// mirrored copy so feh can point a monitor at it. The output name is derived
// from srcPath, so repeated calls overwrite the same file instead of piling up
// on disk; the file is regenerated on every call to stay in sync with the
// source.
func mirrorImage(srcPath string) (string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	dir := filepath.Join(cacheDir, "simple-wallpaper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	sum := sha1.Sum([]byte(srcPath))
	out := filepath.Join(dir, "mirror-"+hex.EncodeToString(sum[:])+".png")

	of, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer of.Close()

	if err := png.Encode(of, dst); err != nil {
		return "", err
	}
	return out, nil
}

// decodeImage opens and decodes the image at path into an in-memory image.
func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	return src, err
}

// composeDesktop renders one image covering the whole multi-monitor desktop.
// Each monitor's rectangle (from xrandr) is drawn independently with its own fit
// mode taken from state, so different monitors can use different modes at once —
// something feh cannot do in a single --bg-* call. The stitched image is written
// as a PNG to the user cache dir and its path returned, ready to be handed to
// feh with --no-xinerama --bg-tile so it lands at the origin and covers the
// whole root. Monitors with no stored image are left black.
func composeDesktop(geoms []monitorGeom, state map[string]monitorState) (string, error) {
	maxX, maxY := 0, 0
	for _, g := range geoms {
		if g.x+g.w > maxX {
			maxX = g.x + g.w
		}
		if g.y+g.h > maxY {
			maxY = g.y + g.h
		}
	}
	if maxX <= 0 || maxY <= 0 {
		return "", errNoDesktopArea
	}

	canvas := image.NewRGBA(image.Rect(0, 0, maxX, maxY))
	draw.Draw(canvas, canvas.Bounds(), image.Black, image.Point{}, draw.Src)

	for _, g := range geoms {
		st, ok := state[g.name]
		if !ok || st.Image == "" {
			continue
		}
		src, err := decodeImage(st.Image)
		if err != nil {
			log.Printf("compose %s: %v", g.name, err)
			continue
		}
		rect := image.Rect(g.x, g.y, g.x+g.w, g.y+g.h)
		renderMonitor(canvas, rect, src, st.Mode)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	dir := filepath.Join(cacheDir, "simple-wallpaper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "desktop.png")

	of, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer of.Close()
	if err := png.Encode(of, canvas); err != nil {
		return "", err
	}
	return out, nil
}

// renderMonitor draws src into the sub-rectangle rect of canvas using the given
// feh-style fit mode. It mirrors what feh's --bg-* modes do, but confined to a
// single monitor's area so each screen can be rendered with its own mode. Unknown
// modes fall back to fill, matching setWallpaper.
func renderMonitor(canvas *image.RGBA, rect image.Rectangle, src image.Image, mode string) {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	w, h := rect.Dx(), rect.Dy()
	if sw == 0 || sh == 0 {
		return
	}

	switch mode {
	case "scale":
		// Stretch to exactly fill the monitor, ignoring aspect ratio.
		xdraw.CatmullRom.Scale(canvas, rect, src, sb, xdraw.Over, nil)

	case "max":
		// Fit entirely inside with black letterbox/pillarbox bars.
		dw, dh := w, h
		if sw*h > sh*w {
			dh = sh * w / sw
		} else {
			dw = sw * h / sh
		}
		ox := rect.Min.X + (w-dw)/2
		oy := rect.Min.Y + (h-dh)/2
		xdraw.CatmullRom.Scale(canvas, image.Rect(ox, oy, ox+dw, oy+dh), src, sb, xdraw.Over, nil)

	case "center":
		// Place at native size, centered; crop overflow, pad the rest with black.
		ox := rect.Min.X + (w-sw)/2
		oy := rect.Min.Y + (h-sh)/2
		dst := image.Rect(ox, oy, ox+sw, oy+sh).Intersect(rect)
		draw.Draw(canvas, dst, src, sb.Min.Add(dst.Min.Sub(image.Pt(ox, oy))), draw.Over)

	case "tile":
		// Repeat at native size from the monitor's top-left corner.
		for y := rect.Min.Y; y < rect.Max.Y; y += sh {
			for x := rect.Min.X; x < rect.Max.X; x += sw {
				dst := image.Rect(x, y, x+sw, y+sh).Intersect(rect)
				draw.Draw(canvas, dst, src, sb.Min, draw.Over)
			}
		}

	default: // "fill": cover the monitor, preserving aspect, center-cropping.
		crop := sb
		if sw*h > sh*w {
			cw := sh * w / h
			x0 := sb.Min.X + (sw-cw)/2
			crop = image.Rect(x0, sb.Min.Y, x0+cw, sb.Max.Y)
		} else {
			ch := sw * h / w
			y0 := sb.Min.Y + (sh-ch)/2
			crop = image.Rect(sb.Min.X, y0, sb.Max.X, y0+ch)
		}
		xdraw.CatmullRom.Scale(canvas, rect, src, crop, xdraw.Over, nil)
	}
}

func (a *wallpaperApp) addFolder(dir string) {
	a.mu.Lock()
	if slices.Contains(a.folders, dir) {
		a.mu.Unlock()
		return
	}
	a.folders = append(a.folders, dir)
	a.mu.Unlock()

	paths := scanFolderPaths(dir)

	a.mu.Lock()
	existing := make(map[string]bool, len(a.images))
	for _, img := range a.images {
		existing[img.path] = true
	}

	var toLoad []string
	for _, p := range paths {
		if !existing[p] {
			a.images = append(a.images, &imageEntry{path: p})
			toLoad = append(toLoad, p)
		}
	}
	a.mu.Unlock()

	a.window.Invalidate()
	a.saveConfig()

	for _, p := range toLoad {
		go func() {
			thumb, err := loadThumbnail(p, thumbPx, thumbPx*2/3)
			if err != nil {
				log.Printf("thumbnail %s: %v", p, err)
				return
			}
			a.thumbCh <- thumbResult{path: p, thumb: thumb}
			a.window.Invalidate()
		}()
	}
}

func (a *wallpaperApp) removeFolder(idx int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx < 0 || idx >= len(a.folders) {
		return
	}
	removed := a.folders[idx]
	a.folders = append(a.folders[:idx], a.folders[idx+1:]...)

	var keep []*imageEntry
	for _, img := range a.images {
		if !strings.HasPrefix(img.path, removed) {
			keep = append(keep, img)
		}
	}
	a.images = keep
	if a.selected >= len(a.images) {
		a.selected = len(a.images) - 1
	}
}
