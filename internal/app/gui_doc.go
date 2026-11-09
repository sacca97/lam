//go:build gui

package app

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	giofont "gioui.org/font/opentype"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"
)

// The document viewer shows a markdown or PDF file in the main pane, in place
// of a session, until it is closed or another session is picked. The file is
// read here or on the connected host, then drawn by the window itself: the
// markdown on a page that follows the theme, the PDF as rendered pages.

const (
	docBodyDp   = 16  // markdown text size, as on GitHub
	docPageGap  = 14  // dp between PDF pages
	docMaxText  = 880 // dp, the widest the markdown column gets
	docMinZoom  = 0.5
	docMaxZoom  = 3.0
	docKeepNear = 3 // PDF pages kept rendered either side of the view
)

var docDeskBg = color.NRGBA{R: 225, G: 228, B: 232, A: 255}

// transition moves the document to a state, reporting whether that was
// allowed (see doc_state.go). guiApp.mu must be held.
func (dv *docView) transition(to docState) bool {
	if !docMayMove(dv.state, to) {
		return false
	}
	dv.state = to
	return true
}

type docView struct {
	host, path string
	kind       docKind
	state      docState // see doc_state.go; guarded by guiApp.mu
	err        string   // why it is docFailed
	gen        int      // bumped by every load, so a late answer is dropped

	mu       sync.Mutex // guards what the render goroutines fill in
	data     []byte
	modified time.Time

	md    *mdLayout
	mdKey struct {
		w, px float32
	}
	pdf   *pdfDoc
	pages map[int]*pdfPage

	scrollY, scrollX float32
	zoom             float32
	tag              bool
	tabTag           bool // the header tab takes the middle button
	viewH, contentH  float32
	links            []docLink // where the links are, in window coordinates, this frame
	hover            image.Point

	// What the pointer is doing: dragging the scroll bar, or selecting text.
	dragBar    bool
	barGrab    float32 // where on the thumb it was taken
	selecting  bool
	selA, selB docPos
	hasSel     bool
	stale      atomic.Bool // the load goroutine replaced the data: the UI drops md at the next frame
	barH       float32     // the scroll thumb's height, from the last frame
	x0, margin float32     // where the page starts, set each frame, for hit tests
}

// docPos is a place in the text: an item of the layout and a byte in it.
type docPos struct{ item, off int }

func (a docPos) less(b docPos) bool { return a.item < b.item || a.item == b.item && a.off < b.off }

type pdfPage struct {
	asked   int // the width wanted; the render may be smaller (see pdfDoc.render)
	width   int
	op      paint.ImageOp
	ready   bool
	loading bool
}

type docLink struct {
	r    image.Rectangle
	dest string
}

var errNotConnected = errors.New("not connected to that host")

// documentSourceFor is where the files of host are read.
func (g *guiApp) documentSourceFor(host string) documentSource {
	if host != "" {
		if r, h := g.mb.Remote(); r != nil && h == host {
			if src, ok := r.(documentSource); ok {
				return src
			}
		}
		return nil
	}
	return localDocumentSource{}
}

// absoluteDocPath makes what was typed an absolute path: ~ is the home, and a
// relative path starts in base.
func absoluteDocPath(input, base, home string) string {
	input = strings.TrimSpace(input)
	switch {
	case input == "~":
		input = home
	case strings.HasPrefix(input, "~/"):
		input = path.Join(home, input[2:])
	case !strings.HasPrefix(input, "/"):
		input = path.Join(base, input)
	}
	return path.Clean(input)
}

// openDocument shows the file at an absolute path on host ("" for this machine).
func (g *guiApp) openDocument(host, p string) {
	for _, dv := range g.docs {
		if dv.host == host && dv.path == p {
			g.doc = dv // already open: its tab comes to the front
			g.mu.Lock()
			failed := dv.state == docFailed
			g.mu.Unlock()
			if failed {
				g.loadDocument(dv)
			}
			return
		}
	}
	dv := &docView{host: host, path: p, kind: documentKind(p), zoom: 1, state: docLoading}
	g.docs = append(g.docs, dv)
	g.doc = dv
	g.loadDocument(dv)
}

// loadDocument reads the file in the background and prepares it.
func (g *guiApp) loadDocument(dv *docView) {
	g.mu.Lock()
	dv.gen++
	gen := dv.gen
	dv.transition(docLoading) // refused for a closed tab
	dv.err = ""
	g.mu.Unlock()
	src := g.documentSourceFor(dv.host)
	go func() {
		var d *document
		var pdf *pdfDoc
		var err error
		if src == nil {
			err = errNotConnected
		} else if d, err = readDocument(src, dv.path); err == nil && d.Kind == docPDF {
			pdf, err = openPDF(d.Data)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if dv.state == docClosed || dv.gen != gen {
			if pdf != nil {
				pdf.Close()
			}
			return
		}
		if err != nil {
			dv.transition(docFailed)
			dv.err = err.Error()
		} else {
			dv.transition(docReady)
			dv.mu.Lock()
			old := dv.pdf
			dv.data, dv.modified, dv.pdf = d.Data, d.Modified, pdf
			dv.pages = map[int]*pdfPage{}
			dv.stale.Store(true)
			dv.mu.Unlock()
			if old != nil {
				old.Close()
			}
		}
		g.dirty.Store(true)
		g.wake()
	}()
}

// closeDoc closes the document in front.
func (g *guiApp) closeDoc() {
	if g.doc != nil {
		g.closeDocTab(g.doc)
	}
}

// closeDocTab closes one document's tab. If it was in front, the tab beside it
// is, or the session when there are no more.
func (g *guiApp) closeDocTab(dv *docView) {
	i := slices.Index(g.docs, dv)
	if i < 0 {
		return
	}
	g.docs = slices.Delete(g.docs, i, i+1)
	g.mu.Lock() // the load goroutine reads these under it
	dv.transition(docClosed)
	dv.gen++
	g.mu.Unlock()
	if g.doc == dv {
		g.doc = nil
		if len(g.docs) > 0 {
			g.doc = g.docs[min(i, len(g.docs)-1)]
		}
	}
	dv.mu.Lock()
	pdf := dv.pdf
	dv.pdf = nil
	dv.mu.Unlock()
	if pdf != nil {
		go pdf.Close()
	}
}

// cycleTab moves to the next (dir 1) or previous tab: the session, then the
// documents in the order they were opened.
func (g *guiApp) cycleTab(dir int) {
	n := len(g.docs)
	if n == 0 {
		return
	}
	// The session is slot 0, but only when its tab is shown.
	_, hasSession := g.selectedInfo()
	hasSession = hasSession || g.emptyShell != nil
	first := 1
	if hasSession {
		first = 0
	}
	at := 0
	if g.doc != nil {
		at = slices.Index(g.docs, g.doc) + 1
	}
	span := n + 1 - first
	at = first + ((at-first+dir)%span+span)%span
	g.doc = nil
	if at > 0 {
		g.doc = g.docs[at-1]
	}
}

// docKey handles a key while a document is open, and reports whether it did.
func (g *guiApp) docKey(ev key.Event) bool {
	dv := g.doc
	if dv == nil {
		return false
	}
	m := ev.Modifiers
	ctrl := m.Contain(key.ModCtrl) || m.Contain(key.ModCommand)
	page := dv.viewH * 0.9
	switch {
	case ctrl && ev.Name == "W":
		// Ctrl+W, or Cmd+W on a Mac, closes the tab, as in a browser. It is only
		// taken while a document is in front: in a session Ctrl+W is the program's.
		g.closeDoc()
	case ctrl && (ev.Name == "=" || ev.Name == "+"):
		dv.zoomBy(1.15)
	case ctrl && ev.Name == "-":
		dv.zoomBy(1 / 1.15)
	case ctrl && ev.Name == "0":
		dv.zoom = 1
		dv.invalidateLayout()
	case ctrl:
		return false // the window's own shortcuts
	case ev.Name == key.NameDownArrow:
		dv.scrollY += 60
	case ev.Name == key.NameUpArrow:
		dv.scrollY -= 60
	case ev.Name == key.NamePageDown, ev.Name == key.NameSpace && !m.Contain(key.ModShift):
		dv.scrollY += page
	case ev.Name == key.NamePageUp, ev.Name == key.NameSpace:
		dv.scrollY -= page
	case ev.Name == key.NameHome:
		dv.scrollY = 0
	case ev.Name == key.NameEnd:
		dv.scrollY = dv.contentH
	case ev.Name == key.NameRightArrow:
		dv.scrollX += 60
	case ev.Name == key.NameLeftArrow:
		dv.scrollX -= 60
	case ev.Name == "R":
		g.loadDocument(dv)
	default:
		return false
	}
	return true
}

func (dv *docView) zoomBy(f float32) {
	dv.zoom = min(max(dv.zoom*f, docMinZoom), docMaxZoom)
	dv.invalidateLayout()
}

func (dv *docView) invalidateLayout() { dv.md, dv.hasSel = nil, false }

// docShaper draws the document's text in Go's fonts, which come with the
// program, so a document looks the same wherever it is opened.
type docShapeKey struct {
	s                  string
	px                 float32
	bold, italic, mono bool
}

type docShaped struct {
	gl []text.Glyph
	w  float32
}

func (g *guiApp) docShape(s string, px float32, bold, italic, mono bool) docShaped {
	key := docShapeKey{s, px, bold, italic, mono}
	if v, ok := g.docShapes[key]; ok {
		return v
	}
	if v, ok := g.docShapesOld[key]; ok {
		if g.docShapes == nil {
			g.docShapes = map[docShapeKey]docShaped{}
		}
		g.docShapes[key] = v
		return v
	}
	if g.docText == nil {
		faces, sans := docFaces()
		g.docSans = sans
		g.docText = text.NewShaper(text.NoSystemFonts(), text.WithCollection(faces))
	}
	f := font.Font{Typeface: g.docSans}
	if mono {
		f.Typeface = "Go Mono"
	}
	if bold {
		f.Weight = font.SemiBold
	}
	if italic {
		f.Style = font.Italic
	}
	g.docText.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.Int26_6(px * 64), MaxLines: 1, MaxWidth: 1 << 20}, s)
	var v docShaped
	for gg, ok := g.docText.NextGlyph(); ok; gg, ok = g.docText.NextGlyph() {
		v.gl = append(v.gl, gg)
		v.w = max(v.w, float32((gg.X + gg.Advance).Ceil()))
	}
	if g.docShapes == nil || len(g.docShapes) > 8192 {
		g.docShapesOld = g.docShapes
		g.docShapes = map[docShapeKey]docShaped{}
	}
	g.docShapes[key] = v
	return v
}

// docFaces is the document's fonts: the desktop's sans serif in its four styles
// when fontconfig has them, as the interface uses, else Go's fonts, which come
// with the program; and Go Mono for code. The name of the sans typeface is
// returned with them.
func docFaces() ([]font.FontFace, font.Typeface) {
	faces := gofont.Collection()
	if sans := systemSansFaces(); len(sans) > 0 {
		return append(faces, sans...), "DocSans"
	}
	return faces, "Go"
}

// systemSansFaces loads the regular, bold, italic and bold italic files of the
// desktop's sans serif; nil unless all four exist and differ, so that bold
// and italic are really different faces.
func systemSansFaces() []font.FontFace {
	switch runtime.GOOS {
	case "darwin":
		dir := "/System/Library/Fonts/Supplemental/"
		return loadSansFiles([4]string{dir + "Arial.ttf", dir + "Arial Bold.ttf", dir + "Arial Italic.ttf", dir + "Arial Bold Italic.ttf"})
	case "linux", "freebsd":
	default:
		return nil
	}
	// The desktop's own font is often one variable file, which cannot give a
	// real bold or italic; the first family with a file for each style does.
	var families []string
	if f, _ := desktopFont(false); f != "" {
		families = append(families, f)
	}
	families = append(families, "Open Sans", "Roboto", "Liberation Sans", "DejaVu Sans", "Cantarell", "Noto Sans")
	patterns := [4]string{"", ":weight=bold", ":slant=italic", ":weight=bold:slant=italic"}
	for _, family := range families {
		var paths [4]string
		seen := map[string]bool{}
		ok := true
		for i, pat := range patterns {
			p, _, got := fcMatch(family + pat)
			if p == "" || seen[p] || !strings.HasPrefix(strings.ToLower(got), strings.ToLower(family)) {
				ok = false
				break
			}
			seen[p] = true
			paths[i] = p
		}
		if ok {
			if faces := loadSansFiles(paths); faces != nil {
				return faces
			}
		}
	}
	return nil
}

// loadSansFiles reads regular, bold, italic and bold italic font files.
func loadSansFiles(paths [4]string) []font.FontFace {
	styles := [4]struct {
		weight font.Weight
		style  font.Style
	}{{font.Normal, font.Regular}, {font.SemiBold, font.Regular}, {font.Normal, font.Italic}, {font.SemiBold, font.Italic}}
	var out []font.FontFace
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		face, err := giofont.Parse(data)
		if err != nil {
			return nil
		}
		out = append(out, font.FontFace{Font: font.Font{Typeface: "DocSans", Weight: styles[i].weight, Style: styles[i].style}, Face: face})
	}
	return out
}

// layoutTabs draws the tabs of the header between left and right: the session
// being looked at, then one tab for each open document, each with a cross.
func (g *guiApp) layoutTabs(gtx layout.Context, left, right, cy int) {
	type tab struct {
		id, label string
		dv        *docView
		w         int
	}
	var tabs []tab
	if s, ok := g.selectedInfo(); ok {
		tabs = append(tabs, tab{id: "tab-session", label: s.Name})
	} else if g.emptyShell != nil {
		tabs = append(tabs, tab{id: "tab-session", label: "Shell"})
	}
	for _, dv := range g.docs {
		tabs = append(tabs, tab{id: fmt.Sprintf("tab-%p", dv), label: path.Base(dv.path), dv: dv})
	}
	h := gtx.Dp(32)
	padX, icon, cross := gtx.Dp(12), gtx.Dp(22), gtx.Dp(26)
	size := g.th.UISize
	total, docs := 0, 0
	for i := range tabs {
		_, tw, _ := g.shape(gtx, size, font.Medium, tabs[i].label, 1<<20)
		tabs[i].w = padX*2 + tw
		if tabs[i].dv != nil {
			tabs[i].w = padX + icon + tw + cross
			docs++
		}
		tabs[i].w = min(tabs[i].w, gtx.Dp(220))
		total += tabs[i].w + gtx.Dp(2)
	}
	if over := total - (right - left); over > 0 && docs > 0 {
		// Tight: the document tabs share the room, down to a stub.
		for i := range tabs {
			if tabs[i].dv != nil {
				tabs[i].w = max(tabs[i].w-over/docs, gtx.Dp(96))
			}
		}
	}
	x := left
	var closing *docView
	for _, t := range tabs {
		if x+t.w > right {
			break
		}
		r := image.Rect(x, cy-h/2, x+t.w, cy+h/2)
		x += t.w + gtx.Dp(2)
		active := (t.dv == nil && g.doc == nil) || (t.dv != nil && t.dv == g.doc)
		c := g.clickable(t.id)
		if c.Clicked(gtx) {
			g.doc = t.dv
		}
		hovered := c.Hovered()
		func() {
			st := op.Offset(r.Min).Push(gtx.Ops)
			defer st.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(r.Size())
			c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(8))
				switch {
				case active:
					paint.FillShape(gtx.Ops, g.th.Selected, rr.Op(gtx.Ops))
				case hovered:
					paint.FillShape(gtx.Ops, g.th.Hover, rr.Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: r.Size()}
			})
		}()
		if t.dv != nil {
			// A press of the wheel closes the tab, as in a browser.
			pass := pointer.PassOp{}.Push(gtx.Ops) // the tab's own click is still the button's
			st := clip.Rect(r).Push(gtx.Ops)
			event.Op(gtx.Ops, &t.dv.tabTag)
			st.Pop()
			pass.Pop()
			for {
				ev, ok := gtx.Event(pointer.Filter{Target: &t.dv.tabTag, Kinds: pointer.Press})
				if !ok {
					break
				}
				if pe, ok := ev.(pointer.Event); ok && pe.Buttons.Contain(pointer.ButtonTertiary) {
					closing = t.dv
				}
			}
		}
		if active {
			paint.FillShape(gtx.Ops, g.th.Accent, clip.UniformRRect(image.Rect(r.Min.X+gtx.Dp(10), r.Max.Y-gtx.Dp(2), r.Max.X-gtx.Dp(10), r.Max.Y), gtx.Dp(1)).Op(gtx.Ops))
		}
		col := g.th.TextDim
		if active {
			col = g.th.Text
		}
		tx := r.Min.X + padX
		maxText := r.Dx() - 2*padX
		if t.dv != nil {
			docIconFile(gtx, image.Pt(r.Min.X+padX+gtx.Dp(6), cy), float32(gtx.Dp(6)), col)
			tx = r.Min.X + padX + icon
			maxText = r.Dx() - padX - icon - cross
		}
		g.drawTextMid(gtx, image.Pt(tx, cy), size, font.Medium, col, t.label, maxText)
		if t.dv == nil {
			continue
		}
		if hovered {
			where := shortenHomePath(t.dv.path, g.sourceFor(t.dv.host))
			if t.dv.host != "" {
				where += " on " + t.dv.host
			}
			g.tooltip(gtx, r, where)
		}
		xr := image.Rect(r.Max.X-cross+gtx.Dp(2), cy-gtx.Dp(10), r.Max.X-gtx.Dp(6), cy+gtx.Dp(10))
		mod := "Ctrl+W"
		if guiMac {
			mod = "⌘W"
		}
		if g.ghostButton(gtx, "x-"+t.id, xr, "Close  "+mod, false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconCross(gtx, c, float32(gtx.Dp(3.5)), float32(max(1, gtx.Dp(1.3))), col)
		}) {
			closing = t.dv
		}
	}
	if closing != nil {
		g.closeDocTab(closing)
	}
}

// layoutDoc draws the document in r, which is in window coordinates.
func (g *guiApp) layoutDoc(gtx layout.Context, r image.Rectangle) {
	dv := g.doc
	dv.viewH = float32(r.Dy())
	if dv.stale.Swap(false) {
		dv.md, dv.hasSel, dv.selecting = nil, false, false
	}
	g.mu.Lock()
	state, errText := dv.state, dv.err
	g.mu.Unlock()
	msg := func(s string, c color.NRGBA) {
		paint.FillShape(gtx.Ops, g.th.TermBg, clip.Rect(r).Op())
		g.drawText(gtx, image.Pt(r.Min.X+gtx.Dp(24), r.Min.Y+gtx.Dp(24)), g.th.UISize, font.Normal, c, s, r.Dx()-gtx.Dp(48))
	}
	switch {
	case state == docLoading:
		msg("Opening "+path.Base(dv.path)+"…", g.th.TextDim)
		return
	case state == docFailed:
		msg(errText, g.th.Unread)
		return
	}

	defer clip.Rect(r).Push(gtx.Ops).Pop()
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	local := image.Rectangle{Max: r.Size()}
	g.docInput(gtx, local, r.Min)

	if dv.kind == docPDF {
		g.drawPDF(gtx, local)
	} else {
		g.drawMarkdown(gtx, local, r.Min)
	}
	dv.scrollY = min(max(dv.scrollY, 0), max(0, dv.contentH-dv.viewH))
	// The scroll bar: it can be dragged, and a press on its track jumps.
	if h, y, ok := dv.thumb(gtx); ok {
		col := color.NRGBA{A: 70}
		if g.th.Name == "dark" {
			col = color.NRGBA{R: 255, G: 255, B: 255, A: 70}
		}
		if dv.dragBar {
			col.A = 130
		}
		bar := image.Rect(r.Dx()-gtx.Dp(9), int(y), r.Dx()-gtx.Dp(3), int(y+h))
		paint.FillShape(gtx.Ops, col, clip.UniformRRect(bar, gtx.Dp(3)).Op(gtx.Ops))
	}
}

// thumb is the scroll bar's thumb: its height and top, and whether there is
// anything to scroll.
func (dv *docView) thumb(gtx layout.Context) (h, y float32, ok bool) {
	if dv.contentH <= dv.viewH || dv.viewH <= 0 {
		return 0, 0, false
	}
	h = max(float32(gtx.Dp(24)), dv.viewH*dv.viewH/dv.contentH)
	y = (dv.viewH - h) * dv.scrollY / max(1, dv.contentH-dv.viewH)
	dv.barH = h
	return h, y, true
}

// docInput takes the wheel (Ctrl and the wheel zoom) and clicks on links.
func (g *guiApp) docInput(gtx layout.Context, local image.Rectangle, origin image.Point) {
	dv := g.doc
	area := clip.Rect(local).Push(gtx.Ops)
	event.Op(gtx.Ops, &dv.tag)
	area.Pop()
	hover := false
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  &dv.tag,
			Kinds:   pointer.Scroll | pointer.Press | pointer.Move | pointer.Drag | pointer.Release | pointer.Cancel,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20},
			ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20},
		})
		if !ok {
			break
		}
		pe, isPointer := ev.(pointer.Event)
		if !isPointer {
			continue
		}
		at := image.Pt(int(pe.Position.X), int(pe.Position.Y)).Add(origin)
		switch pe.Kind {
		case pointer.Move:
			dv.hover = at
		case pointer.Scroll:
			if pe.Modifiers.Contain(key.ModCtrl) || pe.Modifiers.Contain(key.ModCommand) {
				if pe.Scroll.Y < 0 {
					dv.zoomBy(1.1)
				} else if pe.Scroll.Y > 0 {
					dv.zoomBy(1 / 1.1)
				}
				continue
			}
			dv.scrollY += pe.Scroll.Y
			dv.scrollX += pe.Scroll.X
		case pointer.Press:
			if !pe.Buttons.Contain(pointer.ButtonPrimary) {
				continue
			}
			if h, y, ok := dv.thumb(gtx); ok && int(pe.Position.X) >= local.Dx()-gtx.Dp(16) {
				dv.dragBar = true
				dv.barGrab = h / 2 // a press on the track centres the thumb there
				if py := pe.Position.Y; py >= y && py <= y+h {
					dv.barGrab = py - y
				}
				dv.dragTo(pe.Position.Y)
				continue
			}
			followed := false
			for _, l := range dv.links {
				if at.In(l.r) {
					g.followLink(l.dest)
					followed = true
					break
				}
			}
			if !followed && dv.kind == docMarkdown {
				dv.selecting, dv.hasSel = true, false
				dv.selA = g.docHit(pe.Position)
				dv.selB = dv.selA
			}
		case pointer.Drag:
			switch {
			case dv.dragBar:
				dv.dragTo(pe.Position.Y)
			case dv.selecting:
				dv.selB = g.docHit(pe.Position)
				dv.hasSel = dv.selA != dv.selB
				// Dragging past the top or bottom edge scrolls.
				if pe.Position.Y < 0 {
					dv.scrollY += pe.Position.Y / 4
				} else if pe.Position.Y > dv.viewH {
					dv.scrollY += (pe.Position.Y - dv.viewH) / 4
				}
				gtx.Execute(op.InvalidateCmd{})
			}
		case pointer.Release, pointer.Cancel:
			dv.dragBar, dv.selecting = false, false
		}
	}
	for _, l := range dv.links {
		if dv.hover.In(l.r) {
			hover = true
		}
	}
	if hover {
		pointer.CursorPointer.Add(gtx.Ops)
	}
}

// dragTo moves the page so that the thumb, held at barGrab, follows the pointer.
func (dv *docView) dragTo(py float32) {
	span := dv.viewH - dv.barH
	if span <= 0 {
		return
	}
	dv.scrollY = min(max((py-dv.barGrab)/span*(dv.contentH-dv.viewH), 0), dv.contentH-dv.viewH)
}

// followLink opens a link: another document next to this one in the viewer,
// anything on the web in the browser.
func (g *guiApp) followLink(dest string) {
	dv := g.doc
	if dv == nil || dest == "" {
		return
	}
	if frag, ok := strings.CutPrefix(dest, "#"); ok {
		if un, err := url.PathUnescape(frag); err == nil {
			frag = un
		}
		if dv.md != nil {
			if y, ok := dv.md.Anchors[strings.ToLower(frag)]; ok {
				dv.scrollY = max(0, y+dv.margin*0.75-dv.margin*0.5)
			}
		}
		return
	}
	if i := strings.Index(dest, ":"); i > 0 && !strings.ContainsAny(dest[:i], "/.#?") {
		// A link with a scheme goes to the browser, but only the schemes a
		// document can be trusted to name: not file:, smb: or whatever else a
		// handler is installed for.
		switch strings.ToLower(dest[:i]) {
		case "http", "https", "mailto":
			openInBrowser(dest)
		default:
			g.say("Not opening a %s: link", dest[:i])
		}
		return
	}
	target := dest
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if un, err := url.PathUnescape(target); err == nil {
		target = un // my%20notes.md
	}
	if documentKind(target) == docNone {
		g.say("Cannot open %s here", dest)
		return
	}
	if strings.HasPrefix(target, "/") {
		g.openDocument(dv.host, path.Clean(target))
		return
	}
	g.openDocument(dv.host, path.Join(path.Dir(dv.path), target))
}

func openInBrowser(dest string) {
	name := "xdg-open"
	if guiMac {
		name = "open"
	}
	cmd := exec.Command(name, dest)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// drawMarkdown paints the page: a column on the page colour, laid out for the
// width and text size, and scrolled.
func (g *guiApp) drawMarkdown(gtx layout.Context, local image.Rectangle, origin image.Point) {
	dv := g.doc
	dv.links = dv.links[:0] // docInput already used the previous frame's
	dark := g.th.Name == "dark"
	pal := func(c color.NRGBA) color.NRGBA { return docPalette(c, dark) }
	paint.FillShape(gtx.Ops, pal(mdPage), clip.Rect(local).Op())
	px := float32(gtx.Dp(docBodyDp)) * dv.zoom
	margin := float32(gtx.Dp(32))
	col := min(float32(local.Dx())-2*margin, float32(gtx.Dp(docMaxText))*dv.zoom)
	col = max(col, float32(gtx.Dp(120)))
	if dv.md == nil || dv.mdKey.w != col || dv.mdKey.px != px {
		dv.mu.Lock()
		data := dv.data
		dv.mu.Unlock()
		dv.md = layoutMarkdown(data, col, px, func(s string, size float32, bold, italic, mono bool) float32 {
			return g.docShape(s, size, bold, italic, mono).w
		})
		dv.mdKey.w, dv.mdKey.px = col, px
		dv.hasSel, dv.selecting = false, false // the items it points at are gone
	}
	dv.contentH = dv.md.Height + margin
	dv.scrollY = min(max(dv.scrollY, 0), max(0, dv.contentH-dv.viewH))
	x0 := (float32(local.Dx()) - col) / 2
	dv.x0, dv.margin = x0, margin
	lo, hi := dv.selA, dv.selB
	if hi.less(lo) {
		lo, hi = hi, lo
	}
	top, bottom := dv.scrollY-px*3, dv.scrollY+dv.viewH+px*3
	for idx, it := range dv.md.Items {
		y := it.Y + margin*0.75
		if it.Kind == mdBox {
			if y+it.H < top || y > bottom {
				continue
			}
			rr := image.Rect(int(x0+it.X+0.5), int(y-dv.scrollY+0.5), int(x0+it.X+max(it.W, 1)+0.5), int(y-dv.scrollY+max(it.H, 1)+0.5))
			if it.Radius > 0 {
				paint.FillShape(gtx.Ops, pal(it.Color), clip.UniformRRect(rr, int(it.Radius+0.5)).Op(gtx.Ops))
			} else {
				paint.FillShape(gtx.Ops, pal(it.Color), clip.Rect(rr).Op())
			}
			continue
		}
		if y < top || y > bottom {
			continue
		}
		s := g.docShape(it.Text, it.Size, it.Bold, it.Italic, it.Mono)
		at := image.Pt(int(x0+it.X), int(y-dv.scrollY))
		if dv.hasSel && idx >= lo.item && idx <= hi.item {
			from, to := float32(0), s.w
			if idx == lo.item {
				from = g.docPrefixWidth(it, lo.off)
			}
			if idx == hi.item {
				to = g.docPrefixWidth(it, hi.off)
			}
			sel := g.th.Accent
			sel.A = 90
			paint.FillShape(gtx.Ops, sel, clip.Rect(image.Rect(at.X+int(from), at.Y-int(it.Size*0.95), at.X+int(to+0.5), at.Y+int(it.Size*0.3))).Op())
		}
		func() {
			off := op.Offset(at).Push(gtx.Ops)
			defer off.Pop()
			cl := clip.Outline{Path: g.docText.Shape(s.gl)}.Op().Push(gtx.Ops)
			paint.ColorOp{Color: pal(it.Color)}.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			cl.Pop()
		}()
		hovered := false
		if it.Link != "" {
			lr := image.Rect(at.X, at.Y-int(it.Size), at.X+int(s.w), at.Y+int(it.Size*0.3)).Add(origin)
			hovered = dv.hover.In(lr)
		}
		if hovered || it.Strike {
			ly := at.Y + gtx.Dp(2)
			if it.Strike {
				ly = at.Y - int(it.Size*0.3)
			}
			paint.FillShape(gtx.Ops, pal(it.Color), clip.Rect(image.Rect(at.X, ly, at.X+int(s.w), ly+max(1, gtx.Dp(1)))).Op())
		}
		if it.Link != "" {
			dv.links = append(dv.links, docLink{
				r:    image.Rect(at.X, at.Y-int(it.Size), at.X+int(s.w), at.Y+int(it.Size*0.3)).Add(origin),
				dest: it.Link,
			})
		}
	}
}

// drawPDF stacks the pages on a grey desk and renders the ones in view.
func (g *guiApp) drawPDF(gtx layout.Context, local image.Rectangle) {
	dv := g.doc
	desk := docDeskBg
	if g.th.Name == "dark" {
		desk = color.NRGBA{R: 38, G: 42, B: 48, A: 255}
	}
	paint.FillShape(gtx.Ops, desk, clip.Rect(local).Op())
	dv.mu.Lock()
	pdf := dv.pdf
	dv.mu.Unlock()
	if pdf == nil {
		return
	}
	gap := float32(gtx.Dp(docPageGap))
	fit := float32(local.Dx()) - 2*gap
	fit = min(fit, float32(gtx.Dp(1100)))
	width := max(int(fit*dv.zoom), gtx.Dp(80))
	// Where each page starts.
	n := pdf.Pages()
	tops := make([]float32, n+1)
	y := gap
	for i := 0; i < n; i++ {
		tops[i] = y
		s := pdf.sizes[i]
		y += float32(width)*float32(s.Y)/float32(s.X) + gap
	}
	tops[n] = y
	dv.contentH = y
	dv.scrollY = min(max(dv.scrollY, 0), max(0, dv.contentH-dv.viewH))
	wide := float32(width) + 2*gap
	dv.scrollX = min(max(dv.scrollX, 0), max(0, wide-float32(local.Dx())))
	x := (float32(local.Dx())-float32(width))/2 - dv.scrollX
	if wide > float32(local.Dx()) {
		x = gap - dv.scrollX
	}

	first, last := -1, -1
	for i := 0; i < n; i++ {
		if tops[i+1] >= dv.scrollY && tops[i] <= dv.scrollY+dv.viewH {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return
	}
	for i := first; i <= last; i++ {
		s := pdf.sizes[i]
		h := float32(width) * float32(s.Y) / float32(s.X)
		r := image.Rect(int(x), int(tops[i]-dv.scrollY), int(x)+width, int(tops[i]-dv.scrollY+h))
		paint.FillShape(gtx.Ops, color.NRGBA{A: 40}, clip.Rect(r.Add(image.Pt(0, gtx.Dp(1)))).Op())
		paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, clip.Rect(r).Op())
		dv.mu.Lock()
		pg := dv.pages[i]
		need := pg == nil || (pg.asked != width && !pg.loading)
		if need {
			if pg == nil {
				pg = &pdfPage{}
				dv.pages[i] = pg
			}
			pg.loading = true
		}
		ready, pw, imgOp := pg.ready, pg.width, pg.op
		dv.mu.Unlock()
		if need {
			g.renderPage(dv, pdf, i, width)
		}
		if ready {
			func() {
				st := op.Offset(r.Min).Push(gtx.Ops)
				defer st.Pop()
				if pw != width {
					defer op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(float32(width)/float32(pw), float32(width)/float32(pw)))).Push(gtx.Ops).Pop()
				}
				imgOp.Add(gtx.Ops)
				paint.PaintOp{}.Add(gtx.Ops)
			}()
		}
	}
	// Pages far from the view give their memory back.
	dv.mu.Lock()
	for i := range dv.pages {
		if i < first-docKeepNear || i > last+docKeepNear {
			delete(dv.pages, i)
		}
	}
	dv.mu.Unlock()
}

func (g *guiApp) renderPage(dv *docView, pdf *pdfDoc, i, width int) {
	go func() {
		img, err := pdf.render(i, width)
		dv.mu.Lock()
		defer dv.mu.Unlock()
		pg := dv.pages[i]
		if pg == nil || dv.pdf != pdf {
			return // scrolled far away, or the file was reloaded, meanwhile
		}
		pg.loading = false
		if err == nil {
			pg.op, pg.ready, pg.width = paint.NewImageOp(img), true, img.Bounds().Dx()
		}
		// Also on failure: the width is recorded, so a page that cannot be
		// drawn is not tried again on every frame, only when the width changes.
		pg.asked = width
		g.dirty.Store(true)
		g.wake()
	}()
}

// docPrefixWidth is how far into a text item its first off bytes reach.
func (g *guiApp) docPrefixWidth(it mdItem, off int) float32 {
	off = max(0, min(off, len(it.Text)))
	if off == 0 {
		return 0
	}
	return g.docShape(it.Text[:off], it.Size, it.Bold, it.Italic, it.Mono).w
}

// docHit is the place in the text nearest to a point of the view: the line
// closest to it vertically, then the character closest along the line.
func (g *guiApp) docHit(p f32.Point) docPos {
	dv := g.doc
	if dv == nil || dv.md == nil {
		return docPos{}
	}
	px, py := p.X-dv.x0, p.Y+dv.scrollY
	best, bestDY, bestDX := -1, float32(1e9), float32(1e9)
	for i, it := range dv.md.Items {
		if it.Kind != mdText_ || it.Text == "" {
			continue
		}
		base := it.Y + dv.margin*0.75
		dy := float32(0)
		if py < base-it.Size {
			dy = base - it.Size - py
		} else if py > base+it.Size*0.3 {
			dy = py - base - it.Size*0.3
		}
		if dy > bestDY+0.5 { // a farther line cannot win; do not shape it
			continue
		}
		w := g.docShape(it.Text, it.Size, it.Bold, it.Italic, it.Mono).w
		dx := float32(0)
		if px < it.X {
			dx = it.X - px
		} else if px > it.X+w {
			dx = px - it.X - w
		}
		if dy < bestDY-0.5 || dy < bestDY+0.5 && dx < bestDX {
			best, bestDY, bestDX = i, dy, dx
		}
	}
	if best < 0 {
		return docPos{}
	}
	it := dv.md.Items[best]
	// Widths grow along the text: binary search the character boundaries for
	// the first one past the point, then take the nearer of it and the one before.
	var offs []int
	for i := 0; i <= len(it.Text); {
		offs = append(offs, i)
		if i == len(it.Text) {
			break
		}
		_, n := utf8.DecodeRuneInString(it.Text[i:])
		i += n
	}
	k := sort.Search(len(offs), func(n int) bool { return it.X+g.docPrefixWidth(it, offs[n]) >= px })
	if k == len(offs) {
		k = len(offs) - 1
	} else if k > 0 && px-(it.X+g.docPrefixWidth(it, offs[k-1])) < it.X+g.docPrefixWidth(it, offs[k])-px {
		k--
	}
	pos := offs[k]
	return docPos{best, pos}
}

// docSelectionText is the selected text: the pieces of a line run together,
// the lines are separated.
func (dv *docView) selectionText(width func(mdItem) float32) string {
	if !dv.hasSel || dv.md == nil {
		return ""
	}
	lo, hi := dv.selA, dv.selB
	if hi.less(lo) {
		lo, hi = hi, lo
	}
	var b strings.Builder
	lastY, lastEnd := float32(-1e9), float32(0)
	for i := lo.item; i <= hi.item && i < len(dv.md.Items); i++ {
		it := dv.md.Items[i]
		if it.Kind != mdText_ {
			continue
		}
		t := it.Text
		if i == hi.item {
			t = t[:max(0, min(hi.off, len(t)))]
		}
		if i == lo.item {
			t = t[min(lo.off, len(t)):]
		}
		switch {
		case b.Len() > 0 && it.Y-lastY > it.Size*0.5:
			b.WriteByte('\n')
		case b.Len() > 0 && it.X-lastEnd > it.Size*1.5:
			b.WriteByte('\t') // the next table cell
		case b.Len() > 0 && it.X-lastEnd > it.Size*0.15 && !strings.HasSuffix(b.String(), " ") && !strings.HasPrefix(t, " "):
			b.WriteByte(' ') // a marker and its text, or words the layout trimmed
		}
		lastY, lastEnd = it.Y, it.X+width(it)
		b.WriteString(t)
	}
	return b.String()
}

// copyDocSelection puts the selected text on the clipboard.
func (g *guiApp) copyDocSelection(gtx layout.Context) bool {
	if g.doc == nil {
		return false
	}
	text := g.doc.selectionText(func(it mdItem) float32 {
		return g.docShape(it.Text, it.Size, it.Bold, it.Italic, it.Mono).w
	})
	if text == "" {
		return false
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
	gtx.Execute(op.InvalidateCmd{}) // see termView.copySelection
	if guiMac {
		go copyToSystemClipboard(text)
	}
	return true
}
