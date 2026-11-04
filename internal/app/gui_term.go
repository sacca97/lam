//go:build gui

package app

import (
	"image"
	"image/color"
	"io"
	"math"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	uv "github.com/charmbracelet/ultraviolet"
	gh "go.mitchellh.com/libghostty"
)

// termView draws one pane's terminal and turns pointer, keyboard and
// clipboard input into what the program expects.
//
// Drawing goes through libghostty's render state, the API Ghostty's own
// renderers use: after an update it says which rows changed, and only those
// are rasterized again. Each row is one image, so a frame costs one texture
// per row however many glyphs the screen holds, and a screen that does not
// change costs nothing but the draw of the cached images.
type termView struct {
	th *guiTheme
	fb *fallbackFonts

	font   *termFont
	fontPx float64
	fbGen  uint64

	pane *embedded
	rs   *gh.RenderState
	ri   *gh.RenderStateRowIterator
	rc   *gh.RenderStateRowCells
	enc  *gh.KeyEncoder
	kev  *gh.KeyEvent

	cols, rows int
	lines      []termLine
	full       bool
	colors     gh.RenderStateColors
	cursor     gh.RenderStateCursor
	scrollbar  gh.Scrollbar
	modes      termModes

	// input
	tag        bool // pointer and clipboard events
	scrollAcc  float32
	lastScroll time.Time
	sel        termSelection
	sbDrag     bool
	hoverBar   bool
	blinkStart time.Time
	curOp      paint.ImageOp // the block cursor's image, kept while its key holds
	curKey     cursorKey
	gbuf       []byte
	onPress    func() // a click in the pane: it takes the keyboard
	pressed    pointer.Buttons
	reported   bool // the press in progress went to the program, not to selection
	// Smooth scrolling: sub is how many pixels of the row above the viewport
	// show at the top, and pending is scrolling still to animate, in pixels
	// (positive towards the newest output).
	sub     float32
	pending float32
}

type termModes struct {
	mouse, buttonMouse, anyMouse  bool
	altScreen, altScroll, cursorK bool
	bracketed, focus              bool
}

// termLine is one screen row: its cells as last read, and the image they
// were drawn into. Two buffers alternate so the image the GPU may still be
// reading is never the one being drawn.
type termLine struct {
	buf   [2]*image.RGBA
	cur   int
	op    paint.ImageOp
	cells []termCell
	valid bool
}

type termCell struct {
	text    string
	style   uint8
	span    uint8
	skip    bool // the second half of a wide character
	fg, bg  color.NRGBA
	ul      gh.SGRUnderline
	ulColor color.NRGBA
	strike  bool
}

// termSelection is a selection being made with the pointer. Points are in
// screen coordinates (scrollback included), so scrolling while dragging keeps
// the anchor where it was.
type termSelection struct {
	active, dragging bool
	anchor, head     screenPoint
	unit             int // 1 characters, 2 words, 3 lines
	lastClick        time.Time
	lastCell         image.Point
	clicks           int
}

type screenPoint struct {
	x int
	y uint64
}

func newTermView(th *guiTheme, fb *fallbackFonts) *termView {
	return &termView{th: th, fb: fb}
}

// attach shows p, or nothing when p is nil.
func (v *termView) attach(p *embedded) {
	if v.pane == p {
		return
	}
	v.detach()
	v.pane = p
	if p == nil {
		return
	}
	v.rs, _ = gh.NewRenderState()
	if v.rs != nil {
		// One row above the viewport, for the partly scrolled row at the top.
		_ = v.rs.SetOverscan(gh.RenderStateOverscan{Above: 1})
	}
	v.ri, _ = gh.NewRenderStateRowIterator()
	v.rc, _ = gh.NewRenderStateRowCells()
	v.enc, _ = gh.NewKeyEncoder()
	v.kev, _ = gh.NewKeyEvent()
	v.applyColors()
	v.lines, v.full, v.sel, v.sub, v.pending = nil, true, termSelection{}, 0, 0
	v.blinkStart = time.Now()
}

// applyColors makes the theme's colors the terminal's defaults; a program can
// still change them (OSC 4, 10, 11).
func (v *termView) applyColors() {
	if v.pane == nil {
		return
	}
	pal := gh.DefaultPalette()
	for i, c := range v.th.Palette16 {
		pal[i] = gh.ColorRGB{R: c.R, G: c.G, B: c.B}
	}
	fg, bg, cur := v.th.TermFg, v.th.TermBg, v.th.Cursor
	v.pane.em.withTerminal(func(t *gh.Terminal) {
		_ = t.SetColorPalette(&pal)
		_ = t.SetColorForeground(&gh.ColorRGB{R: fg.R, G: fg.G, B: fg.B})
		_ = t.SetColorBackground(&gh.ColorRGB{R: bg.R, G: bg.G, B: bg.B})
		_ = t.SetColorCursor(&gh.ColorRGB{R: cur.R, G: cur.G, B: cur.B})
	})
	v.full = true
}

func (v *termView) detach() {
	if v.kev != nil {
		v.kev.Close()
	}
	if v.enc != nil {
		v.enc.Close()
	}
	if v.rc != nil {
		v.rc.Close()
	}
	if v.ri != nil {
		v.ri.Close()
	}
	if v.rs != nil {
		v.rs.Close()
	}
	v.rs, v.ri, v.rc, v.enc, v.kev = nil, nil, nil, nil, nil
	v.pane = nil
}

// key sends a key press to the program. It returns false when the key has
// no meaning for a terminal.
func (v *termView) key(name key.Name, mods key.Modifiers) bool {
	if v.pane == nil {
		return false
	}
	var out []byte
	var ok bool
	v.pane.em.withTerminal(func(t *gh.Terminal) { out, ok = encodeKey(v.enc, v.kev, t, name, mods) })
	if !ok {
		out, ok = keyBytes(name, mods)
	}
	if !ok {
		return false
	}
	v.typed()
	_ = v.pane.writeInput(out)
	return true
}

// text sends typed text (from the keyboard or an input method).
func (v *termView) text(s string) {
	if v.pane == nil || s == "" {
		return
	}
	v.typed()
	_ = v.pane.writeInput([]byte(s))
}

// setFontPx switches the terminal font size; px is in device pixels.
func (v *termView) setFontPx(px float64) {
	if v.font != nil && v.fontPx == px {
		return
	}
	if v.font != nil {
		v.font.close()
	}
	v.font, v.fontPx = newTermFont(px, v.fb), px
	v.full = true
}

// gridSize is how many cells fit in an area of the given pixel size.
func (v *termView) gridSize(area image.Point) (cols, rows int) {
	return max(2, area.X/v.font.cellW), max(1, area.Y/v.font.cellH)
}

func (v *termView) cellSize() image.Point { return image.Pt(v.font.cellW, v.font.cellH) }

var asciiText = func() (t [128]string) {
	for i := range t {
		t[i] = string(rune(i))
	}
	return
}()

// update reads the screen from libghostty and redraws the rows that changed.
func (v *termView) update() {
	if v.pane == nil || v.rs == nil {
		return
	}
	v.pane.em.withTerminal(func(t *gh.Terminal) {
		_ = v.rs.Update(t)
		v.scrollbar, _ = t.Scrollbar()
		mode := func(m gh.Mode) bool { on, _ := t.Mode(m); return on }
		v.modes = termModes{
			buttonMouse: mode(gh.ModeButtonMouse),
			anyMouse:    mode(gh.ModeAnyMouse),
			altScroll:   mode(gh.ModeAltScroll),
			cursorK:     mode(gh.ModeDECCKM),
			bracketed:   mode(gh.ModeBracketedPaste),
			focus:       mode(gh.ModeFocusEvent),
		}
		v.modes.mouse = v.modes.buttonMouse || v.modes.anyMouse || mode(gh.ModeNormalMouse) || mode(gh.ModeX10Mouse)
		screen, _ := t.ActiveScreen()
		v.modes.altScreen = screen == gh.ScreenAlternate
	})
	dirty, _ := v.rs.Dirty()
	if c, err := v.rs.Colors(); err == nil {
		if c.Background != v.colors.Background || c.Foreground != v.colors.Foreground {
			v.full = true
		}
		v.colors = *c
	}
	if c, err := v.rs.Cursor(); err == nil {
		v.cursor = *c
	}
	if g := v.fb.gen.Load(); g != v.fbGen {
		v.fbGen, v.full = g, true
		clear(v.font.glyphs) // missing glyphs may have a font now
	}
	cols, _ := v.rs.Cols()
	rows, _ := v.rs.Rows()
	if int(cols) != v.cols || int(rows) != v.rows || len(v.lines) != int(rows)+1 {
		v.cols, v.rows = int(cols), int(rows)
		v.lines = make([]termLine, v.rows+1) // [0] is the row above the viewport
		v.full = true
	}
	if dirty == gh.RenderStateDirtyFalse && !v.full {
		return
	}
	if dirty == gh.RenderStateDirtyFull {
		v.full = true
	}
	if err := v.rs.RowIterator(v.ri); err != nil {
		return
	}
	v.lines[0].valid = false // the row above exists only while scrolled back
	for v.ri.Next() {
		vy, err := v.ri.ViewportY()
		if err != nil || vy < -1 || int(vy) >= v.rows {
			continue
		}
		line := &v.lines[vy+1]
		rowDirty, _ := v.ri.Dirty()
		if v.full || rowDirty || !line.valid {
			v.readRow(line)
			v.drawRow(line)
			_ = v.ri.SetDirty(false)
		}
	}
	_ = v.rs.SetDirty(gh.RenderStateDirtyFalse)
	v.full = false
}

func rgbOf(c gh.ColorRGB) color.NRGBA { return color.NRGBA{R: c.R, G: c.G, B: c.B, A: 255} }

func mix(a, b color.NRGBA, t float32) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(float32(x)*(1-t) + float32(y)*t + 0.5) }
	return color.NRGBA{R: m(a.R, b.R), G: m(a.G, b.G), B: m(a.B, b.B), A: 255}
}

func (v *termView) readRow(line *termLine) {
	line.cells = line.cells[:0]
	if v.ri.Cells(v.rc) != nil {
		return
	}
	defFg, defBg := rgbOf(v.colors.Foreground), rgbOf(v.colors.Background)
	var st gh.RenderCellStyle
	wideTail := false
	for v.rc.Next() {
		c := termCell{span: 1, fg: defFg, bg: defBg}
		v.gbuf, _ = v.rc.AppendGraphemes(v.gbuf[:0])
		switch {
		case len(v.gbuf) == 1 && v.gbuf[0] < 0x80:
			c.text = asciiText[v.gbuf[0]]
		case len(v.gbuf) > 0:
			c.text = string(v.gbuf)
			if raw, err := v.rc.Raw(); err == nil && raw != nil {
				if wide, _ := raw.Wide(); wide == gh.CellWideWide {
					c.span = 2
				}
			}
		}
		if wideTail && c.text == "" {
			c.skip = true
		}
		wideTail = c.span == 2
		styled := v.rc.StyleInto(&st) == nil
		// An erased cell can carry only a background (\e[41m\e[K): that
		// is not "styling", but it still has to be drawn.
		if styled && st.HasForeground {
			c.fg = rgbOf(st.Foreground)
		}
		if styled && st.HasBackground {
			c.bg = rgbOf(st.Background)
		}
		if styled && st.HasStyling {
			if st.Bold {
				c.style |= styleBold
			}
			if st.Italic {
				c.style |= styleItalic
			}
			if st.Inverse {
				c.fg, c.bg = c.bg, c.fg
			}
			if st.Faint {
				c.fg = mix(c.fg, c.bg, 0.45)
			}
			c.strike = st.Strikethrough
			if st.Underline {
				c.ulColor = c.fg
				if s, err := v.rc.Style(); err == nil && s != nil {
					c.ul = s.Underline()
					if uc := s.UnderlineColor(); uc.Tag == gh.StyleColorRGB {
						c.ulColor = rgbOf(uc.RGB)
					} else if uc.Tag == gh.StyleColorPalette {
						c.ulColor = rgbOf(v.colors.Palette[uc.Palette])
					}
				}
			}
		}
		if v.sel.active {
			if selected, _ := v.rc.Selected(); selected {
				c.bg = v.th.Selection
				if c.fg == c.bg {
					c.fg = defFg
				}
			}
		}
		line.cells = append(line.cells, c)
	}
}

// drawRow rasterizes a row into the image its next frame shows.
func (v *termView) drawRow(line *termLine) {
	f := v.font
	cw, ch := f.cellW, f.cellH
	w, h := max(1, v.cols*cw), ch
	line.cur ^= 1
	img := line.buf[line.cur]
	if img == nil || img.Rect.Dx() != w || img.Rect.Dy() != h {
		img = image.NewRGBA(image.Rect(0, 0, w, h))
		line.buf[line.cur] = img
	}
	bg := rgbOf(v.colors.Background)
	fillRGBA(img, img.Rect, bg)
	for x, c := range line.cells {
		if c.bg != bg && !c.skip {
			fillRGBA(img, image.Rect(x*cw, 0, (x+int(c.span))*cw, h), c.bg)
		}
	}
	for x, c := range line.cells {
		if c.skip || c.text == "" {
			continue
		}
		if g := f.glyph(c.text, c.style, int(c.span)); g != nil {
			stamp(img, g, x*cw, c.fg)
		}
	}
	for x, c := range line.cells {
		x0, x1 := x*cw, (x+int(c.span))*cw
		if c.ul != gh.UnderlineNone {
			v.underline(img, x0, x1, c.ul, c.ulColor)
		}
		if c.strike {
			y := f.baseline - f.cellH*3/10
			fillRGBA(img, image.Rect(x0, y, x1, y+f.thin), c.fg)
		}
	}
	line.op = paint.NewImageOp(img)
	line.op.Filter = paint.FilterNearest
	line.valid = true
}

// stamp draws a glyph mask in color c with its cell's left edge at x. It
// gives the same pixels as draw.DrawMask over an opaque destination with a
// uniform source, without the per-pixel generality or boxing c into an
// interface for every glyph.
func stamp(img *image.RGBA, g *glyphMask, x int, c color.NRGBA) {
	at := image.Pt(x+g.off.X, g.off.Y)
	r := g.mask.Rect.Add(at).Intersect(img.Rect)
	if r.Empty() {
		return
	}
	const m = 1<<16 - 1
	mp := r.Min.Sub(at)
	sr, sg, sb := uint32(c.R)*0x101, uint32(c.G)*0x101, uint32(c.B)*0x101
	i0 := img.PixOffset(r.Min.X, r.Min.Y)
	mi0 := g.mask.PixOffset(mp.X, mp.Y)
	w := r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[i0 : i0+w*4]
		mrow := g.mask.Pix[mi0 : mi0+w]
		for j, ma8 := range mrow {
			switch ma8 {
			case 0:
				continue
			case 255:
				d := row[j*4 : j*4+4 : j*4+4]
				d[0], d[1], d[2], d[3] = c.R, c.G, c.B, 255
				continue
			}
			ma := uint32(ma8) * 0x101
			a := (m - ma) * 0x101
			d := row[j*4 : j*4+4 : j*4+4]
			d[0] = uint8((uint32(d[0])*a + sr*ma) / m >> 8)
			d[1] = uint8((uint32(d[1])*a + sg*ma) / m >> 8)
			d[2] = uint8((uint32(d[2])*a + sb*ma) / m >> 8)
			d[3] = 255
		}
		i0 += img.Stride
		mi0 += g.mask.Stride
	}
}

func (v *termView) underline(img *image.RGBA, x0, x1 int, style gh.SGRUnderline, c color.NRGBA) {
	f := v.font
	t := f.thin
	y := min(f.baseline+max(1, (f.cellH-f.baseline)/3), f.cellH-t)
	switch style {
	case gh.UnderlineDouble:
		y = min(y, f.cellH-3*t)
		fillRGBA(img, image.Rect(x0, y, x1, y+t), c)
		fillRGBA(img, image.Rect(x0, y+2*t, x1, y+3*t), c)
	case gh.UnderlineCurly:
		amp := float64(max(1, t))
		y = min(y, f.cellH-int(2*amp)-t)
		for x := x0; x < x1; x++ {
			wy := float64(y) + amp + amp*math.Sin(float64(x)/float64(f.cellW)*2*math.Pi)
			yy := int(math.Round(wy))
			fillRGBA(img, image.Rect(x, yy, x+1, yy+t), c)
		}
	case gh.UnderlineDotted:
		for x := x0; x < x1; x += 2 * t {
			fillRGBA(img, image.Rect(x, y, min(x+t, x1), y+t), c)
		}
	case gh.UnderlineDashed:
		dash := max(2, f.cellW/3)
		for x := x0; x < x1; x += 2 * dash {
			fillRGBA(img, image.Rect(x, y, min(x+dash, x1), y+t), c)
		}
	default:
		fillRGBA(img, image.Rect(x0, y, x1, y+t), c)
	}
}

func fillRGBA(img *image.RGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	px := [4]uint8{c.R, c.G, c.B, c.A}
	first := img.Pix[img.PixOffset(r.Min.X, r.Min.Y):]
	for i := 0; i < r.Dx(); i++ {
		copy(first[i*4:], px[:])
	}
	n := r.Dx() * 4
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(img.Pix[img.PixOffset(r.Min.X, y):], first[:n])
	}
}

// Layout draws the terminal in an area of gtx.Constraints.Max and handles
// its pointer input. focused is whether keyboard input goes to it.
func (v *termView) Layout(gtx layout.Context, focused bool) layout.Dimensions {
	size := gtx.Constraints.Max
	v.handlePointer(gtx, size)
	if v.pending != 0 && v.font != nil {
		step := v.pending * 0.35
		if abs32(v.pending) < 1.5 {
			step = v.pending
		}
		v.pending -= step
		v.scrollPixels(step)
		gtx.Execute(op.InvalidateCmd{})
	}
	v.update()
	paint.FillShape(gtx.Ops, rgbOf(v.colors.Background), clip.Rect{Max: size}.Op())
	if v.pane == nil || v.font == nil {
		return layout.Dimensions{Size: size}
	}
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()

	// The pointer area: text selection cursor over the grid.
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &v.tag)
	if !v.modes.mouse {
		pointer.CursorText.Add(gtx.Ops)
	}
	area.Pop()

	cw, ch := v.font.cellW, v.font.cellH
	sub := v.subPx()
	for i := range v.lines {
		line := &v.lines[i]
		if !line.valid || (i == 0 && sub == 0) {
			continue
		}
		st := op.Offset(image.Pt(0, (i-1)*ch+sub)).Push(gtx.Ops)
		cl := clip.Rect{Max: line.op.Size()}.Push(gtx.Ops)
		line.op.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		cl.Pop()
		st.Pop()
	}
	v.drawCursor(gtx, focused, cw, ch)
	v.drawScrollbar(gtx, size)
	return layout.Dimensions{Size: size}
}

// cursorKey is everything the block cursor's image depends on.
type cursorKey struct {
	size    image.Point
	col, bg color.NRGBA
	text    string
	style   uint8
	span    int
	font    *termFont
	gen     uint64
}

const cursorBlink = 530 * time.Millisecond

func (v *termView) drawCursor(gtx layout.Context, focused bool, cw, ch int) {
	c := v.cursor
	if !c.ViewportHasValue || !c.Visible || int(c.ViewportY)+1 >= len(v.lines) {
		return
	}
	if focused && c.Blinking {
		phase := gtx.Now.Sub(v.blinkStart) / cursorBlink
		next := v.blinkStart.Add((phase + 1) * cursorBlink)
		gtx.Execute(op.InvalidateCmd{At: next})
		if phase%2 == 1 {
			return
		}
	}
	x, y := int(c.ViewportX), int(c.ViewportY)
	if c.WideTail && x > 0 {
		x--
	}
	span := 1
	var cell termCell
	if cells := v.lines[y+1].cells; x < len(cells) {
		cell = cells[x]
		span = int(cell.span)
	}
	col := v.th.Cursor
	if v.colors.CursorHasValue {
		col = rgbOf(v.colors.Cursor)
	}
	r := image.Rect(x*cw, y*ch, (x+span)*cw, (y+1)*ch).Add(image.Pt(0, v.subPx()))
	style := c.VisualStyle
	if !focused {
		style = gh.CursorVisualStyleBlockHollow
	}
	t := max(1, gtx.Dp(1.5))
	switch style {
	case gh.CursorVisualStyleBar:
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: r.Min, Max: image.Pt(r.Min.X+t, r.Max.Y)}.Op())
	case gh.CursorVisualStyleUnderline:
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(r.Min.X, r.Max.Y-t), Max: r.Max}.Op())
	case gh.CursorVisualStyleBlockHollow:
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: clip.Rect(r).Path(), Width: float32(t)}.Op())
	default:
		// A block, with the character under it drawn in the background color.
		key := cursorKey{size: r.Size(), col: col, bg: rgbOf(v.colors.Background), text: cell.text, style: cell.style, span: span, font: v.font, gen: v.fbGen}
		if v.curOp == (paint.ImageOp{}) || key != v.curKey {
			img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
			fillRGBA(img, img.Rect, col)
			if cell.text != "" {
				if g := v.font.glyph(cell.text, cell.style, span); g != nil {
					stamp(img, g, 0, rgbOf(v.colors.Background))
				}
			}
			v.curOp = paint.NewImageOp(img)
			v.curOp.Filter = paint.FilterNearest
			v.curKey = key
		}
		st := op.Offset(r.Min).Push(gtx.Ops)
		cl := clip.Rect{Max: r.Size()}.Push(gtx.Ops)
		v.curOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		cl.Pop()
		st.Pop()
	}
}

// The scrollbar is a thin overlay that shows while scrolled back, just after
// scrolling, or with the pointer over it.
func (v *termView) scrollbarRect(gtx layout.Context, size image.Point) (track, thumb image.Rectangle, ok bool) {
	sb := v.scrollbar
	if sb.Total <= sb.Len || sb.Len == 0 {
		return
	}
	w := gtx.Dp(6)
	inset := gtx.Dp(3)
	track = image.Rect(size.X-w-inset, inset, size.X-inset, size.Y-inset)
	h := track.Dy()
	th := max(gtx.Dp(28), int(float64(h)*float64(sb.Len)/float64(sb.Total)))
	top := track.Min.Y + int(float64(h-th)*float64(sb.Offset)/float64(sb.Total-sb.Len))
	thumb = image.Rect(track.Min.X, top, track.Max.X, top+th)
	return track, thumb, true
}

func (v *termView) drawScrollbar(gtx layout.Context, size image.Point) {
	_, thumb, ok := v.scrollbarRect(gtx, size)
	if !ok {
		return
	}
	atBottom := v.scrollbar.Offset+v.scrollbar.Len >= v.scrollbar.Total
	recent := gtx.Now.Sub(v.lastScroll) < 900*time.Millisecond
	if atBottom && !recent && !v.hoverBar && !v.sbDrag {
		return
	}
	if recent {
		gtx.Execute(op.InvalidateCmd{At: v.lastScroll.Add(900 * time.Millisecond)})
	}
	col := v.th.TextDim
	col.A = 110
	if v.hoverBar || v.sbDrag {
		col.A = 200
	}
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(thumb, thumb.Dx()/2).Op(gtx.Ops))
}

// cellAt is the grid cell under a pixel position, clamped to the grid.
func (v *termView) cellAt(p f32.Point) image.Point {
	x := int(p.X) / v.font.cellW
	y := int(math.Floor(float64(p.Y-float32(v.subPx())) / float64(v.font.cellH)))
	return image.Pt(min(max(x, 0), max(v.cols-1, 0)), min(max(y, 0), max(v.rows-1, 0)))
}

func (v *termView) toScreen(c image.Point) screenPoint {
	return screenPoint{x: c.X, y: v.scrollbar.Offset + uint64(c.Y)}
}

func mouseMods(m key.Modifiers) uv.KeyMod {
	var mod uv.KeyMod
	if m.Contain(key.ModShift) {
		mod |= uv.ModShift
	}
	if m.Contain(key.ModAlt) {
		mod |= uv.ModAlt
	}
	if m.Contain(key.ModCtrl) {
		mod |= uv.ModCtrl
	}
	return mod
}

func uvButton(b pointer.Buttons) uv.MouseButton {
	switch {
	case b.Contain(pointer.ButtonPrimary):
		return uv.MouseLeft
	case b.Contain(pointer.ButtonSecondary):
		return uv.MouseRight
	case b.Contain(pointer.ButtonTertiary):
		return uv.MouseMiddle
	}
	return uv.MouseNone
}

func (v *termView) handlePointer(gtx layout.Context, size image.Point) {
	if v.pane == nil || v.font == nil {
		return
	}
	filters := []event.Filter{
		pointer.Filter{
			Target:  &v.tag,
			Kinds:   pointer.Press | pointer.Release | pointer.Drag | pointer.Move | pointer.Scroll | pointer.Leave | pointer.Cancel,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20},
		},
		transfer.TargetFilter{Target: &v.tag, Type: "application/text"},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		switch ev := ev.(type) {
		case transfer.DataEvent:
			r := ev.Open()
			data, _ := io.ReadAll(io.LimitReader(r, 64<<20))
			r.Close()
			v.pasteBytes(data)
		case pointer.Event:
			cell := v.cellAt(ev.Position)
			track, thumb, hasBar := v.scrollbarRect(gtx, size)
			onBar := hasBar && ev.Position.X >= float32(track.Min.X-gtx.Dp(6))
			switch ev.Kind {
			case pointer.Move, pointer.Leave:
				hover := ev.Kind == pointer.Move && onBar
				if hover != v.hoverBar {
					v.hoverBar = hover
					gtx.Execute(op.InvalidateCmd{})
				}
				if ev.Kind == pointer.Move && v.modes.anyMouse && !ev.Modifiers.Contain(key.ModShift) {
					v.pane.sendMouse(uv.MouseMotionEvent{X: cell.X, Y: cell.Y, Button: uv.MouseNone, Mod: mouseMods(ev.Modifiers)})
				}
			case pointer.Scroll:
				v.scroll(ev, cell)
			case pointer.Press:
				v.pressed = ev.Buttons
				if v.onPress != nil {
					v.onPress()
				}
				if onBar && ev.Buttons.Contain(pointer.ButtonPrimary) {
					v.sbDrag = true
					v.dragScrollbar(ev.Position.Y, track, thumb)
					continue
				}
				v.reported = v.modes.mouse && !ev.Modifiers.Contain(key.ModShift)
				if v.reported {
					v.pane.sendMouse(uv.MouseClickEvent{X: cell.X, Y: cell.Y, Button: uvButton(ev.Buttons), Mod: mouseMods(ev.Modifiers)})
					continue
				}
				switch {
				case ev.Buttons.Contain(pointer.ButtonPrimary):
					v.beginSelection(gtx, cell, ev.Modifiers)
				case ev.Buttons.Contain(pointer.ButtonSecondary):
					// Right click copies a selection, or pastes without one.
					if v.sel.active {
						v.copySelection(gtx)
						v.clearSelection()
					} else {
						gtx.Execute(clipboard.ReadCmd{Tag: &v.tag})
					}
				case ev.Buttons.Contain(pointer.ButtonTertiary):
					gtx.Execute(clipboard.ReadCmd{Tag: &v.tag})
				}
			case pointer.Drag:
				if v.sbDrag {
					v.dragScrollbar(ev.Position.Y, track, thumb)
					continue
				}
				// Press, drag and release belong together: Shift pressed or
				// released half way does not move them to the other side.
				if v.reported {
					if v.modes.buttonMouse || v.modes.anyMouse {
						v.pane.sendMouse(uv.MouseMotionEvent{X: cell.X, Y: cell.Y, Button: uvButton(ev.Buttons), Mod: mouseMods(ev.Modifiers)})
					}
					continue
				}
				if v.sel.dragging {
					// Past the top or bottom edge the view scrolls along.
					switch {
					case ev.Position.Y < 0:
						v.scrollLines(-1)
					case ev.Position.Y > float32(size.Y):
						v.scrollLines(1)
					}
					v.extendSelection(cell)
					gtx.Execute(op.InvalidateCmd{})
				}
			case pointer.Release, pointer.Cancel:
				if v.sbDrag {
					v.sbDrag = false
					continue
				}
				if v.reported {
					v.reported = false
					v.pane.sendMouse(uv.MouseReleaseEvent{X: cell.X, Y: cell.Y, Button: uvButton(v.pressed), Mod: mouseMods(ev.Modifiers)})
					continue
				}
				v.sel.dragging = false
			}
		}
	}
}

func (v *termView) dragScrollbar(y float32, track, thumb image.Rectangle) {
	sb := v.scrollbar
	span := float64(track.Dy() - thumb.Dy())
	if span <= 0 {
		return
	}
	frac := (float64(y) - float64(track.Min.Y) - float64(thumb.Dy())/2) / span
	row := uint(math.Round(math.Min(1, math.Max(0, frac)) * float64(sb.Total-sb.Len)))
	v.pane.em.withTerminal(func(t *gh.Terminal) { t.ScrollViewportRow(row) })
	v.sub, v.pending = 0, 0 // the thumb decides where the view is, not a glide
	v.lastScroll = time.Now()
}

// scroll handles a wheel or trackpad scroll: the program gets it when it
// tracks the mouse, a full-screen program without mouse support gets arrow
// keys (alternate scroll), and otherwise the view moves through scrollback.
func (v *termView) scroll(ev pointer.Event, cell image.Point) {
	shift := ev.Modifiers.Contain(key.ModShift)
	if !(v.modes.mouse && !shift) && !(v.modes.altScreen && v.modes.altScroll && !shift) {
		// Scrollback moves by pixels, animated, so wheel steps glide too.
		v.pending += ev.Scroll.Y
		v.lastScroll = time.Now()
		return
	}
	v.scrollAcc += ev.Scroll.Y
	lines := int(v.scrollAcc / float32(v.font.cellH))
	if lines == 0 {
		return
	}
	v.scrollAcc -= float32(lines * v.font.cellH)
	switch {
	case v.modes.mouse && !shift:
		button := uv.MouseWheelDown
		if lines < 0 {
			button = uv.MouseWheelUp
		}
		for range min(abs(lines), 10) {
			v.pane.sendMouse(uv.MouseClickEvent{X: cell.X, Y: cell.Y, Button: button, Mod: mouseMods(ev.Modifiers)})
		}
	case v.modes.altScreen && v.modes.altScroll && !shift:
		seq := "\x1b[B"
		if lines < 0 {
			seq = "\x1b[A"
		}
		if v.modes.cursorK {
			seq = "\x1bO" + seq[2:]
		}
		_ = v.pane.writeInput([]byte(strings.Repeat(seq, min(abs(lines), 10))))
	default:
		v.scrollLines(lines)
	}
}

func (v *termView) scrollLines(n int) {
	v.sub, v.pending = 0, 0
	v.pane.em.withTerminal(func(t *gh.Terminal) { t.ScrollViewportDelta(n) })
	v.lastScroll = time.Now()
}

func (v *termView) subPx() int { return int(math.Round(float64(v.sub))) }

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// scrollPixels moves the view by d pixels (positive towards the newest
// output). The position in pixels is the viewport's top row times the row
// height, minus how much of the row above shows.
func (v *termView) scrollPixels(d float32) {
	ch := float32(v.font.cellH)
	v.pane.em.withTerminal(func(t *gh.Terminal) {
		sb, err := t.Scrollbar()
		if err != nil || sb.Total <= sb.Len {
			v.sub = 0
			return
		}
		limit := float32(sb.Total-sb.Len) * ch
		pos := float32(sb.Offset)*ch - v.sub + d
		pos = min(max(pos, 0), limit)
		row := float32(math.Ceil(float64(pos / ch)))
		v.sub = row*ch - pos
		if uint64(row) != sb.Offset {
			t.ScrollViewportRow(uint(row))
		}
		if v.sub < 0.01 {
			v.sub = 0
		}
	})
	if v.sub == 0 && d > 0 && v.scrollbar.Offset+v.scrollbar.Len >= v.scrollbar.Total {
		v.pending = 0 // reached the bottom
	}
}

// scrollPage moves the view a screen up (-1) or down (1), keeping a line.
func (v *termView) scrollPage(dir int) { v.scrollLines(dir * max(1, v.rows-1)) }

func (v *termView) scrollToBottom() {
	v.sub, v.pending = 0, 0
	if v.scrollbar.Offset+v.scrollbar.Len < v.scrollbar.Total {
		v.pane.em.withTerminal(func(t *gh.Terminal) { t.ScrollViewportBottom() })
	}
}

func (v *termView) scrollToTop() {
	v.sub, v.pending = 0, 0
	v.pane.em.withTerminal(func(t *gh.Terminal) { t.ScrollViewportTop() })
	v.lastScroll = time.Now()
}

const multiClick = 400 * time.Millisecond

func (v *termView) beginSelection(gtx layout.Context, cell image.Point, mods key.Modifiers) {
	now := gtx.Now
	if now.Sub(v.sel.lastClick) < multiClick && cell == v.sel.lastCell {
		v.sel.clicks = v.sel.clicks%3 + 1
	} else {
		v.sel.clicks = 1
	}
	v.sel.lastClick, v.sel.lastCell = now, cell
	p := v.toScreen(cell)
	if mods.Contain(key.ModShift) && v.sel.active {
		v.sel.dragging = true
		v.extendSelection(cell)
		return
	}
	v.clearSelection()
	v.sel.anchor, v.sel.head, v.sel.unit, v.sel.dragging = p, p, v.sel.clicks, true
	if v.sel.unit > 1 {
		v.sel.active = true
		v.applySelection()
	}
}

func (v *termView) extendSelection(cell image.Point) {
	p := v.toScreen(cell)
	if p == v.sel.head && v.sel.active {
		return
	}
	if !v.sel.active && v.sel.unit == 1 && p == v.sel.anchor {
		return // a click that moved a pixel is still a click
	}
	v.sel.head = p
	v.sel.active = true
	v.applySelection()
}

func before(a, b screenPoint) bool { return a.y < b.y || (a.y == b.y && a.x < b.x) }

// applySelection gives libghostty the selection between anchor and head,
// widened to whole words or lines for double and triple clicks.
func (v *termView) applySelection() {
	a, b := v.sel.anchor, v.sel.head
	if before(b, a) {
		a, b = b, a
	}
	unit := v.sel.unit
	v.pane.em.withTerminal(func(t *gh.Terminal) {
		ref := func(p screenPoint) *gh.GridRef {
			r, err := t.GridRef(gh.Point{Tag: gh.PointTagScreen, X: uint16(p.x), Y: uint32(p.y)})
			if err != nil {
				return nil
			}
			return r
		}
		ra, rb := ref(a), ref(b)
		if ra == nil || rb == nil {
			return
		}
		sel := &gh.Selection{Start: *ra, End: *rb}
		switch unit {
		case 2:
			if wa, err := t.SelectWord(gh.SelectWordOptions{Ref: ra}); err == nil && wa != nil {
				sel.Start = wa.Start
			}
			if wb, err := t.SelectWord(gh.SelectWordOptions{Ref: rb}); err == nil && wb != nil {
				sel.End = wb.End
			}
		case 3:
			if la, err := t.SelectLine(gh.SelectLineOptions{Ref: ra}); err == nil && la != nil {
				sel.Start = la.Start
			}
			if lb, err := t.SelectLine(gh.SelectLineOptions{Ref: rb}); err == nil && lb != nil {
				sel.End = lb.End
			}
		}
		_ = t.SetSelection(sel)
	})
}

func (v *termView) clearSelection() {
	if v.sel.active && v.pane != nil {
		v.pane.em.withTerminal(func(t *gh.Terminal) { _ = t.SetSelection(nil) })
	}
	v.sel.active = false
}

func (v *termView) selectionText() string {
	if !v.sel.active || v.pane == nil {
		return ""
	}
	var text string
	v.pane.em.withTerminal(func(t *gh.Terminal) {
		text, _ = t.SelectionFormatString(gh.WithSelectionTrim(true), gh.WithSelectionUnwrap(true))
	})
	return text
}

func (v *termView) copySelection(gtx layout.Context) bool {
	text := v.selectionText()
	if text == "" {
		return false
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
	// The write is carried out with the next frame, and an idle window draws
	// none: the text then reached the clipboard only after something else
	// (a paste, typically) woke the window.
	gtx.Execute(op.InvalidateCmd{})
	if guiMac {
		go copyToSystemClipboard(text) // pbcopy as well, should the toolkit's write not land
	}
	return true
}

// paste asks for the clipboard; the text arrives as a transfer.DataEvent.
func (v *termView) paste(gtx layout.Context) {
	gtx.Execute(clipboard.ReadCmd{Tag: &v.tag})
}

func (v *termView) pasteBytes(data []byte) {
	if v.pane == nil || len(data) == 0 {
		return
	}
	// PasteEncode turns newlines into carriage returns and, in bracketed
	// paste mode, wraps the text and strips anything that would end the
	// bracket early.
	out, err := gh.PasteEncode(data, v.modes.bracketed)
	if err != nil {
		return
	}
	v.clearSelection()
	v.scrollToBottom()
	_ = v.pane.writeInput(out)
}

// windowFocus tells a program that asked (mode 1004) about focus changes.
func (v *termView) windowFocus(focused bool) {
	if v.pane == nil || !v.modes.focus {
		return
	}
	seq := "\x1b[O"
	if focused {
		seq = "\x1b[I"
	}
	_ = v.pane.writeInput([]byte(seq))
}

// typed is called for keyboard input that reaches the program: the view
// returns to the bottom, the selection goes, and the cursor shows at once.
func (v *termView) typed() {
	v.clearSelection()
	v.scrollToBottom()
	v.blinkStart = time.Now()
}
