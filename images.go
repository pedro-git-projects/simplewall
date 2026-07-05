package main

import (
	"crypto/sha1"
	"encoding/hex"
	"image"
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
