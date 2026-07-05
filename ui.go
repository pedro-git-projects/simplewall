package main

import (
	"image"
	"image/color"
	"path/filepath"
	"strings"
	"sync"

	"gioui.org/app"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Gruvbox dark palette
var (
	clrBg     = color.NRGBA{R: 0x28, G: 0x28, B: 0x28, A: 0xff}
	clrBg1    = color.NRGBA{R: 0x3c, G: 0x38, B: 0x36, A: 0xff}
	clrBg2    = color.NRGBA{R: 0x50, G: 0x49, B: 0x45, A: 0xff}
	clrBg3    = color.NRGBA{R: 0x66, G: 0x5c, B: 0x54, A: 0xff}
	clrFg     = color.NRGBA{R: 0xeb, G: 0xdb, B: 0xb2, A: 0xff}
	clrYellow = color.NRGBA{R: 0xfa, G: 0xbd, B: 0x2f, A: 0xff}
	clrAqua   = color.NRGBA{R: 0x83, G: 0xa5, B: 0x98, A: 0xff}
	clrRed    = color.NRGBA{R: 0xfb, G: 0x49, B: 0x34, A: 0xff}
)

const (
	thumbDp = unit.Dp(160) // display size
	thumbPx = 240          // stored pixel size (thumb width)
)

type imageEntry struct {
	path  string
	thumb image.Image
	click widget.Clickable
}

type wallpaperApp struct {
	window *app.Window
	th     *material.Theme
	ops    op.Ops

	mu       sync.Mutex
	folders  []string
	images   []*imageEntry
	selected int

	monitors      []string
	currentMode   string
	currentScreen string

	addFolderBtn     widget.Clickable
	applyBtn         widget.Clickable
	folderRemoveBtns []widget.Clickable
	modeClicks       [5]widget.Clickable
	screenClicks     []widget.Clickable

	folderList layout.List
	imageList  layout.List
	gridScroll gesture.Scroll // owns wheel events over the grid for custom-speed scrolling
	focusTag   int            // keyboard-focus target for arrow-key navigation
	cols       int            // current number of grid columns (for up/down navigation)

	thumbCh chan thumbResult
	status  string
	errMsg  bool // true = status is an error
}

func newApp(w *app.Window) *wallpaperApp {
	th := material.NewTheme()
	th.Palette.Bg = clrBg
	th.Palette.Fg = clrFg
	th.Palette.ContrastBg = clrYellow
	th.Palette.ContrastFg = clrBg

	monitors := getMonitors()
	a := &wallpaperApp{
		window:        w,
		th:            th,
		selected:      -1,
		currentMode:   "fill",
		currentScreen: monitors[0],
		monitors:      monitors,
		screenClicks:  make([]widget.Clickable, len(monitors)),
		thumbCh:       make(chan thumbResult, 128),
	}

	go func() {
		for r := range a.thumbCh {
			a.mu.Lock()
			for _, img := range a.images {
				if img.path == r.path {
					img.thumb = r.thumb
					break
				}
			}
			a.mu.Unlock()
			w.Invalidate()
		}
	}()

	return a
}

func (a *wallpaperApp) run() error {
	for {
		switch e := a.window.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&a.ops, e)
			a.update(gtx)
			a.draw(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (a *wallpaperApp) update(gtx layout.Context) {
	if a.addFolderBtn.Clicked(gtx) {
		go func() {
			folder, err := pickFolder()
			if err != nil {
				a.status = err.Error()
				a.errMsg = true
				a.window.Invalidate()
				return
			}
			if folder != "" {
				a.addFolder(folder)
			}
		}()
	}

	if a.applyBtn.Clicked(gtx) {
		a.mu.Lock()
		idx := a.selected
		images := a.images
		a.mu.Unlock()
		if idx >= 0 && idx < len(images) {
			path := images[idx].path
			mode := a.currentMode
			screen := a.currentScreen
			go func() {
				if err := setWallpaper(path, mode, screen, a.monitors); err != nil {
					a.status = "Error: " + err.Error()
					a.errMsg = true
				} else {
					a.status = "Set: " + filepath.Base(path)
					a.errMsg = false
				}
				a.window.Invalidate()
			}()
		} else {
			a.status = "No image selected"
			a.errMsg = true
		}
	}

	for i, mode := range fehModes {
		if a.modeClicks[i].Clicked(gtx) {
			a.currentMode = mode
		}
	}

	for i, screen := range a.monitors {
		if i < len(a.screenClicks) && a.screenClicks[i].Clicked(gtx) {
			a.currentScreen = screen
		}
	}

	a.mu.Lock()
	for i := 0; i < len(a.folderRemoveBtns) && i < len(a.folders); i++ {
		if a.folderRemoveBtns[i].Clicked(gtx) {
			a.mu.Unlock()
			a.removeFolder(i)
			a.mu.Lock()
			break
		}
	}
	a.mu.Unlock()
}

func (a *wallpaperApp) draw(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, clrBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(a.drawSidebar),
		layout.Flexed(1, a.drawGrid),
	)
}

// ── Sidebar ──────────────────────────────────────────────────────────────────

func (a *wallpaperApp) drawSidebar(gtx layout.Context) layout.Dimensions {
	const sideW = unit.Dp(256)
	w := gtx.Dp(sideW)
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))

	paint.FillShape(gtx.Ops, clrBg1, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Inset{Top: unit.Dp(16), Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(12)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(a.drawTitle),
				layout.Rigid(spacer(8)),
				layout.Rigid(a.drawSectionLabel("FOLDERS")),
				layout.Rigid(spacer(4)),
				layout.Rigid(a.drawFolderList),
				layout.Rigid(spacer(6)),
				layout.Rigid(a.drawAddFolderBtn),
				layout.Rigid(spacer(14)),
				layout.Rigid(a.drawDivider),
				layout.Rigid(spacer(12)),
				layout.Rigid(a.drawSectionLabel("MODE")),
				layout.Rigid(spacer(6)),
				layout.Rigid(a.drawModeSelector),
				layout.Rigid(spacer(12)),
				layout.Rigid(a.drawSectionLabel("SCREEN")),
				layout.Rigid(spacer(6)),
				layout.Rigid(a.drawScreenSelector),
				layout.Flexed(1, fill),
				layout.Rigid(a.drawApplyBtn),
				layout.Rigid(spacer(8)),
				layout.Rigid(a.drawStatus),
			)
		},
	)
}

func (a *wallpaperApp) drawTitle(gtx layout.Context) layout.Dimensions {
	lbl := material.H6(a.th, "Simple Wallpaper")
	lbl.Color = clrYellow
	return lbl.Layout(gtx)
}

func (a *wallpaperApp) drawSectionLabel(text string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		lbl := material.Caption(a.th, text)
		lbl.Color = clrBg3
		return lbl.Layout(gtx)
	}
}

func (a *wallpaperApp) drawFolderList(gtx layout.Context) layout.Dimensions {
	a.mu.Lock()
	n := len(a.folders)
	for len(a.folderRemoveBtns) < n {
		a.folderRemoveBtns = append(a.folderRemoveBtns, widget.Clickable{})
	}
	a.mu.Unlock()

	if n == 0 {
		lbl := material.Body2(a.th, "No folders added")
		lbl.Color = clrBg3
		return lbl.Layout(gtx)
	}

	maxH := gtx.Dp(unit.Dp(160))
	gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, maxH)
	a.folderList.Axis = layout.Vertical

	return a.folderList.Layout(gtx, n, func(gtx layout.Context, i int) layout.Dimensions {
		a.mu.Lock()
		if i >= len(a.folders) {
			a.mu.Unlock()
			return layout.Dimensions{}
		}
		name := filepath.Base(a.folders[i])
		a.mu.Unlock()

		return layout.Inset{Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(a.th, name)
					lbl.Color = clrFg
					return lbl.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if i >= len(a.folderRemoveBtns) {
						return layout.Dimensions{}
					}
					btn := material.Button(a.th, &a.folderRemoveBtns[i], "×")
					btn.Background = color.NRGBA{}
					btn.Color = clrBg3
					btn.Inset = layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}
					return btn.Layout(gtx)
				}),
			)
		})
	})
}

func (a *wallpaperApp) drawAddFolderBtn(gtx layout.Context) layout.Dimensions {
	btn := material.Button(a.th, &a.addFolderBtn, "+ Add Folder")
	btn.Background = clrBg2
	btn.Color = clrFg
	return btn.Layout(gtx)
}

func (a *wallpaperApp) drawDivider(gtx layout.Context) layout.Dimensions {
	sz := image.Pt(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(1)))
	paint.FillShape(gtx.Ops, clrBg2, clip.Rect{Max: sz}.Op())
	return layout.Dimensions{Size: sz}
}

func (a *wallpaperApp) drawModeSelector(gtx layout.Context) layout.Dimensions {
	return wrapFlow(gtx, len(fehModes), func(gtx layout.Context, i int) layout.Dimensions {
		return a.drawToggle(gtx, &a.modeClicks[i], capitalize(fehModes[i]), a.currentMode == fehModes[i])
	})
}

func (a *wallpaperApp) drawScreenSelector(gtx layout.Context) layout.Dimensions {
	return wrapFlow(gtx, len(a.monitors), func(gtx layout.Context, i int) layout.Dimensions {
		if i >= len(a.screenClicks) {
			return layout.Dimensions{}
		}
		return a.drawToggle(gtx, &a.screenClicks[i], a.monitors[i], a.currentScreen == a.monitors[i])
	})
}

// wrapFlow lays out n items left-to-right, wrapping to a new row whenever the
// next item would overflow the available width. Items size to their content,
// so this keeps every button at its natural width instead of squeezing the
// last one until its label wraps vertically. Inter-item spacing is expected to
// come from each item's own trailing inset.
func wrapFlow(gtx layout.Context, n int, child func(gtx layout.Context, i int) layout.Dimensions) layout.Dimensions {
	type cell struct {
		call op.CallOp
		size image.Point
	}
	maxW := gtx.Constraints.Max.X
	// Size each item to its content, not the full sidebar width, so the
	// buttons stay compact and several fit per row.
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}

	cells := make([]cell, n)
	for i := 0; i < n; i++ {
		macro := op.Record(gtx.Ops)
		dims := child(cgtx, i)
		cells[i] = cell{macro.Stop(), dims.Size}
	}
	x, y, rowH := 0, 0, 0
	for _, c := range cells {
		if x > 0 && x+c.size.X > maxW {
			x = 0
			y += rowH
			rowH = 0
		}
		off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		c.call.Add(gtx.Ops)
		off.Pop()
		x += c.size.X
		if c.size.Y > rowH {
			rowH = c.size.Y
		}
	}
	return layout.Dimensions{Size: image.Pt(maxW, y+rowH)}
}

func (a *wallpaperApp) drawToggle(gtx layout.Context, click *widget.Clickable, label string, active bool) layout.Dimensions {
	bg, fg := clrBg2, clrFg
	if active {
		bg, fg = clrAqua, clrBg
	}
	return layout.Inset{Right: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		btn := material.Button(a.th, click, label)
		btn.Background = bg
		btn.Color = fg
		btn.Inset = layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(8), Right: unit.Dp(8)}
		return btn.Layout(gtx)
	})
}

func (a *wallpaperApp) drawApplyBtn(gtx layout.Context) layout.Dimensions {
	btn := material.Button(a.th, &a.applyBtn, "Set Wallpaper")
	btn.Background = clrYellow
	btn.Color = clrBg
	return btn.Layout(gtx)
}

func (a *wallpaperApp) drawStatus(gtx layout.Context) layout.Dimensions {
	if a.status == "" {
		return layout.Dimensions{}
	}
	lbl := material.Caption(a.th, a.status)
	if a.errMsg {
		lbl.Color = clrRed
	} else {
		lbl.Color = clrFg
	}
	return lbl.Layout(gtx)
}

// ── Image grid ───────────────────────────────────────────────────────────────

func (a *wallpaperApp) drawGrid(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, clrBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	const (
		padDp = unit.Dp(12)
		gapDp = unit.Dp(8)
	)

	a.mu.Lock()
	images := make([]*imageEntry, len(a.images))
	copy(images, a.images)
	selected := a.selected
	a.mu.Unlock()

	if len(images) == 0 {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body1(a.th, "Add a folder to browse wallpapers")
			lbl.Color = clrBg3
			return lbl.Layout(gtx)
		})
	}

	return layout.Inset{Top: padDp, Bottom: padDp, Left: padDp, Right: padDp}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		thumbW := gtx.Dp(thumbDp)
		thumbH := thumbW * 2 / 3
		gap := gtx.Dp(gapDp)

		cols := (gtx.Constraints.Max.X + gap) / (thumbW + gap)
		if cols < 1 {
			cols = 1
		}
		a.cols = cols
		numRows := (len(images) + cols - 1) / cols

		// Arrow keys move the selection; do this before laying out so the new
		// highlight and any auto-scroll take effect in this frame.
		selected = a.handleArrowKeys(gtx, len(images), cols, selected)

		// Custom-speed wheel scrolling. a.gridScroll is registered as a sibling
		// on top of the list (below), so it receives wheel events first and
		// consumes them; the list's own scroll handler therefore gets nothing
		// and stays put while we drive its offset here. Since the list clamps
		// Offset to the content bounds during layout, scrolling past the top or
		// bottom simply stops instead of jittering.
		const scrollSpeed = 3
		if d := a.gridScroll.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Vertical,
			pointer.ScrollRange{}, pointer.ScrollRange{Min: -1e6, Max: 1e6}); d != 0 {
			a.imageList.Position.Offset += d * scrollSpeed
			a.imageList.Position.BeforeEnd = true
		}

		a.imageList.Axis = layout.Vertical
		result := a.imageList.Layout(gtx, numRows, func(gtx layout.Context, row int) layout.Dimensions {
			start := row * cols
			end := start + cols
			if end > len(images) {
				end = len(images)
			}

			children := make([]layout.FlexChild, end-start)
			for j := start; j < end; j++ {
				j := j
				entry := images[j]
				isSelected := j == selected
				children[j-start] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Right: gapDp, Bottom: gapDp}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return a.drawThumb(gtx, entry, isSelected, j, thumbW, thumbH)
					})
				})
			}
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
		})

		// Place the wheel handler and keyboard-focus target as a sibling on top
		// of the list so the handler wins the scroll events. PassOp keeps this
		// overlay pass-through for pointer hit-testing, so presses still reach
		// the thumbnail buttons underneath it (otherwise clicks to select an
		// image would be swallowed here).
		area := clip.Rect{Max: result.Size}.Push(gtx.Ops)
		pass := pointer.PassOp{}.Push(gtx.Ops)
		event.Op(gtx.Ops, &a.focusTag)
		a.gridScroll.Add(gtx.Ops)
		pass.Pop()
		area.Pop()

		return result
	})
}

// handleArrowKeys consumes arrow-key presses while the grid is focused and
// returns the updated selection index. Left/Right move by one image, Up/Down
// by a full row. The selected thumbnail is kept within the viewport.
func (a *wallpaperApp) handleArrowKeys(gtx layout.Context, n, cols, cur int) int {
	for {
		ev, ok := gtx.Source.Event(
			key.FocusFilter{Target: &a.focusTag},
			key.Filter{Focus: &a.focusTag, Name: key.NameLeftArrow},
			key.Filter{Focus: &a.focusTag, Name: key.NameRightArrow},
			key.Filter{Focus: &a.focusTag, Name: key.NameUpArrow},
			key.Filter{Focus: &a.focusTag, Name: key.NameDownArrow},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press || n == 0 {
			continue
		}
		if cur < 0 {
			cur = 0
		} else {
			switch ke.Name {
			case key.NameLeftArrow:
				cur--
			case key.NameRightArrow:
				cur++
			case key.NameUpArrow:
				cur -= cols
			case key.NameDownArrow:
				cur += cols
			}
		}
		if cur < 0 {
			cur = 0
		}
		if cur >= n {
			cur = n - 1
		}
		a.selected = cur
		a.ensureRowVisible(cur / cols)
	}
	return cur
}

// ensureRowVisible scrolls the grid the minimum amount needed to bring the
// given row into view, using the viewport metrics from the previous layout.
func (a *wallpaperApp) ensureRowVisible(row int) {
	p := a.imageList.Position
	count := max(p.Count, 1)

	switch {
	case row < p.First:
		a.imageList.ScrollTo(row)
	case row >= p.First+count:
		a.imageList.Position.First = row - count + 1
		a.imageList.Position.Offset = 0
		a.imageList.Position.BeforeEnd = true
	}
}

func (a *wallpaperApp) drawThumb(gtx layout.Context, entry *imageEntry, selected bool, idx, w, h int) layout.Dimensions {
	sz := image.Pt(w, h)
	gtx.Constraints = layout.Exact(sz)

	if entry.click.Clicked(gtx) {
		a.selected = idx
		// Move keyboard focus to the grid so the arrow keys navigate the
		// selection from here on.
		gtx.Execute(key.FocusCmd{Tag: &a.focusTag})
	}

	return entry.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		const border = 3

		borderClr := clrBg2
		if selected {
			borderClr = clrYellow
		}
		paint.FillShape(gtx.Ops, borderClr, clip.Rect{Max: sz}.Op())

		b := 0
		if selected {
			b = border
		}
		inner := image.Rect(b, b, sz.X-b, sz.Y-b)

		// Keep the thumbnail contents (image and hover bar) clipped to the
		// area inside the selection border.
		defer clip.Rect(inner).Push(gtx.Ops).Pop()

		if entry.thumb != nil {
			widget.Image{
				Src:      paint.NewImageOp(entry.thumb),
				Fit:      widget.Cover,
				Position: layout.Center,
			}.Layout(gtx)
		} else {
			paint.FillShape(gtx.Ops, clrBg1, clip.Rect(inner).Op())
		}

		// On hover, reveal the file name in a translucent bar across the
		// bottom of the thumbnail.
		if entry.click.Hovered() {
			a.drawThumbName(gtx, inner, filepath.Base(entry.path))
		}

		return layout.Dimensions{Size: sz}
	})
}

// drawThumbName paints a translucent bar with the given name across the bottom
// of the rect r, used to reveal the wallpaper's file name on hover.
func (a *wallpaperApp) drawThumbName(gtx layout.Context, r image.Rectangle, name string) {
	barH := gtx.Dp(unit.Dp(22))
	if barH > r.Dy() {
		barH = r.Dy()
	}
	pad := gtx.Dp(unit.Dp(6))
	bar := image.Rect(r.Min.X, r.Max.Y-barH, r.Max.X, r.Max.Y)

	paint.FillShape(gtx.Ops, color.NRGBA{A: 0xcc}, clip.Rect(bar).Op())

	off := op.Offset(image.Pt(bar.Min.X+pad, bar.Min.Y)).Push(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Exact(image.Pt(max(bar.Dx()-2*pad, 0), barH))
	layout.W.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Caption(a.th, name)
		lbl.Color = clrFg
		lbl.MaxLines = 1
		return lbl.Layout(gtx)
	})
	off.Pop()
}

// Helpers

func spacer(dp unit.Dp) layout.Widget {
	return layout.Spacer{Height: dp}.Layout
}

func fill(gtx layout.Context) layout.Dimensions {
	return layout.Dimensions{Size: gtx.Constraints.Min}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
