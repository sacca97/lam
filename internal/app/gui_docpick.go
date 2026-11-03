//go:build gui

package app

import (
	"fmt"
	"image"
	"image/color"
	"path"
	"sort"
	"strings"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// The open-document dialog is a small file browser: the folder the field names
// is listed with its subfolders and its markdown and PDF files, and a click,
// or the arrow keys and Enter, opens one. It starts in the session's folder.

const docPickRowDp = 30

type docEntry struct {
	name string
	dir  bool
	kind docKind
}

type docPickRow struct {
	label string
	up    bool // the parent folder
	entry docEntry
}

type docPicker struct {
	host, base string // the machine, and the folder relative input is taken from
	dir        string // the folder (as typed, ending in /) that all lists
	all        []docEntry
	loading    bool
	err        string
	sel        int     // the row picked with the keys, or -1
	offset     float32 // how far the list is scrolled, in pixels
	rowH, view int     // row and list heights in pixels, as last drawn
	gen        int
	tag        bool
	hist       []string // the folders visited, as typed, and where in them we are
	pos        int
}

// pickSplit divides the field into the folder part, with its slash, and the
// name being typed.
func pickSplit(field string) (dir, partial string) {
	i := strings.LastIndex(field, "/")
	return field[:i+1], field[i+1:]
}

// pickRefresh lists the folder the field names, unless that is the one already
// listed. The answer arrives later, as the host may be far away.
func (g *guiApp) pickRefresh() {
	d := &g.dlg
	pk := d.pick
	if pk == nil {
		return
	}
	dir, _ := pickSplit(d.field.String())
	if dir == pk.dir && (pk.loading || pk.all != nil || pk.err != "") {
		return
	}
	pk.dir, pk.loading, pk.err, pk.all = dir, true, "", nil
	pk.gen++
	gen := pk.gen
	completer, _ := g.documentSourceFor(pk.host).(documentCompleter)
	go func() {
		var entries []docEntry
		var err error
		if completer == nil {
			err = errNotConnected
		} else {
			var matches []string
			if matches, err = completer.CompleteDocumentPath(dir, pk.base); err == nil {
				for _, m := range matches {
					name := strings.TrimPrefix(m, dir)
					isDir := strings.HasSuffix(name, "/")
					name = strings.TrimSuffix(name, "/")
					entries = append(entries, docEntry{name: name, dir: isDir, kind: documentKind(name)})
				}
			}
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].dir != entries[j].dir {
				return entries[i].dir
			}
			return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
		})
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.dlg.pick != pk || pk.gen != gen {
			return
		}
		pk.loading = false
		if err != nil {
			pk.err = err.Error()
		}
		pk.all = append([]docEntry{}, entries...)
		g.dirty.Store(true)
		g.wake()
	}()
}

// pickRows are the rows to show: the parent, then the entries that start with
// what has been typed.
func (g *guiApp) pickRows() []docPickRow {
	d := &g.dlg
	pk := d.pick
	if pk == nil {
		return nil
	}
	_, partial := pickSplit(d.field.String())
	var rows []docPickRow
	if partial == "" {
		rows = append(rows, docPickRow{label: "..", up: true})
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range pk.all {
		if partial == "" || strings.HasPrefix(strings.ToLower(e.name), strings.ToLower(partial)) {
			rows = append(rows, docPickRow{label: e.name, entry: e})
		}
	}
	return rows
}

// pickEdited is called after the field changed.
func (g *guiApp) pickEdited() {
	if pk := g.dlg.pick; pk != nil {
		pk.sel, pk.offset = -1, 0
		g.pickRefresh()
	}
}

// pickParent is the folder above dir, written the way the field shows paths.
func (g *guiApp) pickParent(dir string) string {
	pk := g.dlg.pick
	src := g.sourceFor(pk.host)
	home, _ := src.HomeDirectory()
	parent := path.Dir(absoluteDocPath(dir, pk.base, home))
	if parent == "/" {
		return "/"
	}
	return shortenHomePath(parent, src) + "/"
}

// pickGo shows a folder. A visit is remembered, for the back and forward
// buttons, unless it is itself a step through that history.
func (g *guiApp) pickGo(dir string, remember bool) {
	d := &g.dlg
	pk := d.pick
	if remember && (len(pk.hist) == 0 || pk.hist[pk.pos] != dir) {
		pk.hist = append(pk.hist[:min(pk.pos+1, len(pk.hist))], dir)
		pk.pos = len(pk.hist) - 1
	}
	d.field.set(dir)
	d.err = ""
	g.pickEdited()
}

func (g *guiApp) pickBack() {
	if pk := g.dlg.pick; pk.pos > 0 {
		pk.pos--
		g.pickGo(pk.hist[pk.pos], false)
	}
}

func (g *guiApp) pickForward() {
	if pk := g.dlg.pick; pk.pos+1 < len(pk.hist) {
		pk.pos++
		g.pickGo(pk.hist[pk.pos], false)
	}
}

func (g *guiApp) pickUp() {
	dir, _ := pickSplit(g.dlg.field.String())
	g.pickGo(g.pickParent(dir), true)
}

// pickActivate goes into a folder or opens a file.
func (g *guiApp) pickActivate(r docPickRow) {
	d := &g.dlg
	pk := d.pick
	dir, _ := pickSplit(d.field.String())
	switch {
	case r.up:
		g.pickGo(g.pickParent(dir), true)
	case r.entry.dir:
		g.pickGo(dir+r.entry.name+"/", true)
	default:
		src := g.sourceFor(pk.host)
		home, _ := src.HomeDirectory()
		host := pk.host
		file := absoluteDocPath(dir+r.entry.name, pk.base, home)
		g.closeDialog()
		g.openDocument(host, file)
	}
}

// pickCrumb is one step of the path shown above the list.
type pickCrumb struct{ label, target string }

// pickCrumbs splits the folder being shown into steps, each of which goes there.
func (g *guiApp) pickCrumbs() []pickCrumb {
	pk := g.dlg.pick
	src := g.sourceFor(pk.host)
	home, _ := src.HomeDirectory()
	dir, _ := pickSplit(g.dlg.field.String())
	abs := absoluteDocPath(dir, pk.base, home)
	var crumbs []pickCrumb
	rest, prefix := abs, "/"
	if home != "" && (abs == home || strings.HasPrefix(abs, home+"/")) {
		crumbs = append(crumbs, pickCrumb{"~", "~/"})
		rest, prefix = strings.TrimPrefix(abs, home), "~/"
	} else {
		crumbs = append(crumbs, pickCrumb{"/", "/"})
	}
	acc := ""
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part == "" {
			continue
		}
		acc += part + "/"
		crumbs = append(crumbs, pickCrumb{part, prefix + acc})
	}
	return crumbs
}

// pickKey handles the keys that move through the list and between folders; it
// reports whether it used the key.
func (g *guiApp) pickKey(ev key.Event) bool {
	d := &g.dlg
	pk := d.pick
	if pk == nil {
		return false
	}
	alt := ev.Modifiers.Contain(key.ModAlt) || (guiMac && ev.Modifiers.Contain(key.ModCommand))
	switch {
	case alt && ev.Name == key.NameLeftArrow, guiMac && ev.Modifiers.Contain(key.ModCommand) && ev.Name == "[":
		g.pickBack()
		return true
	case alt && ev.Name == key.NameRightArrow, guiMac && ev.Modifiers.Contain(key.ModCommand) && ev.Name == "]":
		g.pickForward()
		return true
	case alt && ev.Name == key.NameUpArrow:
		g.pickUp()
		return true
	}
	rows := g.pickRows()
	show := func(i int) {
		pk.sel = min(max(i, 0), len(rows)-1)
		top := float32(pk.sel * pk.rowH)
		if top < pk.offset {
			pk.offset = top
		} else if bottom := top + float32(pk.rowH); bottom > pk.offset+float32(pk.view) {
			pk.offset = bottom - float32(pk.view)
		}
	}
	switch ev.Name {
	case key.NameDownArrow:
		show(pk.sel + 1)
	case key.NameUpArrow:
		if pk.sel > 0 {
			show(pk.sel - 1)
		}
	case key.NamePageDown:
		show(pk.sel + docPickVisible - 1)
	case key.NamePageUp:
		show(max(pk.sel-docPickVisible+1, 0))
	case key.NameHome:
		if pk.sel < 0 {
			return false
		}
		show(0)
	case key.NameEnd:
		if pk.sel < 0 {
			return false
		}
		show(len(rows) - 1)
	case key.NameLeftArrow:
		// With a row picked, the arrows walk the folders: left goes up, right goes in.
		if pk.sel < 0 {
			return false
		}
		g.pickUp()
	case key.NameRightArrow:
		if pk.sel < 0 || pk.sel >= len(rows) || (!rows[pk.sel].up && !rows[pk.sel].entry.dir) {
			return false
		}
		g.pickActivate(rows[pk.sel])
	case key.NameReturn, key.NameEnter:
		if pk.sel >= 0 && pk.sel < len(rows) {
			g.pickActivate(rows[pk.sel])
			return true
		}
		return false
	case key.NameTab:
		if pk.sel >= 0 && pk.sel < len(rows) && !rows[pk.sel].up {
			dir, _ := pickSplit(d.field.String())
			if e := rows[pk.sel].entry; e.dir {
				g.pickGo(dir+e.name+"/", true)
			} else {
				d.field.set(dir + e.name)
				g.pickEdited()
			}
			return true
		}
		return false
	default:
		return false
	}
	return true
}

// docPickVisible is how many rows the list shows; set when drawn.
var docPickVisible = 8

// layoutDocPick draws the whole browser at y: the back, forward and up buttons
// with the path field, the clickable path, and the list. It returns its height.
func (g *guiApp) layoutDocPick(gtx layout.Context, pad, y, inner int) int {
	d := &g.dlg
	pk := d.pick
	y0 := y

	// Back, forward, up, and the field the path is typed in.
	b, gapB := gtx.Dp(36), gtx.Dp(4)
	x := pad
	nav := func(id, tip string, enabled bool, icon func(layout.Context, image.Point, color.NRGBA, float32)) bool {
		r := image.Rect(x, y, x+b, y+b)
		x += b + gapB
		ir := float32(gtx.Dp(5))
		if !enabled {
			rr := clip.UniformRRect(r, gtx.Dp(8))
			paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
			icon(gtx, image.Pt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2), color.NRGBA{R: g.th.TextFaint.R, G: g.th.TextFaint.G, B: g.th.TextFaint.B, A: 90}, ir)
			return false
		}
		return g.ghostButton(gtx, id, r, tip, true, func(gtx layout.Context, c image.Point, col color.NRGBA) { icon(gtx, c, col, ir) })
	}
	mod := "Alt+"
	if guiMac {
		mod = "⌘"
	}
	if nav("pick-back", "Back  "+mod+"←", pk.pos > 0, iconChevronLeft) {
		g.pickBack()
	}
	if nav("pick-fwd", "Forward  "+mod+"→", pk.pos+1 < len(pk.hist), iconChevronRight) {
		g.pickForward()
	}
	if nav("pick-up", "Parent folder  Alt+↑", true, iconArrowUp) {
		g.pickUp()
	}
	g.drawField(gtx, image.Rect(x, y, pad+inner, y+b), &d.field, "")
	y += b + gtx.Dp(6)

	// The path as steps: a click goes to that folder.
	crumbs := g.pickCrumbs()
	size := g.th.SmallSize
	sepW := gtx.Dp(18)
	avail := inner - gtx.Dp(8)
	widths := make([]int, len(crumbs))
	for i, c := range crumbs {
		_, widths[i], _ = g.shape(gtx, size, font.Medium, c.label, 1<<20)
		widths[i] += gtx.Dp(14)
	}
	first, used := len(crumbs), 0
	for first > 0 {
		w := widths[first-1] + sepW
		if used+w > avail && first < len(crumbs) {
			break
		}
		used += w
		first--
	}
	cy := y + gtx.Dp(13)
	cx := pad + gtx.Dp(2)
	if first > 0 {
		g.drawTextMid(gtx, image.Pt(cx, cy), size, font.Normal, g.th.TextFaint, "…", gtx.Dp(14))
		cx += sepW
	}
	for i := first; i < len(crumbs); i++ {
		c := crumbs[i]
		r := image.Rect(cx, y, cx+widths[i], y+gtx.Dp(26))
		cl := g.clickable(fmt.Sprintf("crumb-%d", i))
		if cl.Clicked(gtx) {
			g.pickGo(c.target, true)
			return gtx.Dp(120)
		}
		func() {
			st := op.Offset(r.Min).Push(gtx.Ops)
			defer st.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(r.Size())
			cl.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				if cl.Hovered() {
					paint.FillShape(gtx.Ops, g.th.Hover, clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(6)).Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: r.Size()}
			})
		}()
		col := g.th.TextDim
		if i == len(crumbs)-1 {
			col = g.th.Text // where we are
		}
		g.drawTextMid(gtx, image.Pt(r.Min.X+gtx.Dp(7), cy), size, font.Medium, col, c.label, widths[i])
		cx += widths[i]
		if i < len(crumbs)-1 {
			g.drawTextMid(gtx, image.Pt(cx+gtx.Dp(4), cy), size, font.Normal, g.th.TextFaint, "›", sepW)
			cx += sepW
		}
	}
	y += gtx.Dp(30)

	// The list.
	rowH := gtx.Dp(docPickRowDp)
	rows := g.pickRows()
	visible := docPickVisible
	area := image.Rect(pad, y, pad+inner, y+visible*rowH)
	pk.rowH, pk.view = rowH, area.Dy()
	rr := clip.UniformRRect(area, gtx.Dp(8))
	paint.FillShape(gtx.Ops, g.th.Surface, rr.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())

	// The wheel scrolls it by what the wheel says, pixel by pixel. The events
	// are read here, and the area that takes them is registered after the rows
	// below, on top of them: a handler under the rows' buttons never saw them.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &pk.tag, Kinds: pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Scroll {
			pk.offset += min(max(pe.Scroll.Y, -float32(3*rowH)), float32(3*rowH))
		}
	}
	maxOff := float32(max(0, len(rows)*rowH-area.Dy()))
	pk.offset = min(max(pk.offset, 0), maxOff)

	g.mu.Lock()
	loading, errText, listed := pk.loading, pk.err, pk.all != nil
	g.mu.Unlock()
	if len(rows) == 0 || (len(rows) == 1 && rows[0].up && (loading || errText != "" || listed)) {
		msg := "No markdown or PDF files here"
		col := g.th.TextFaint
		switch {
		case loading:
			msg = "Looking…"
		case errText != "":
			msg, col = errText, g.th.Unread
		case !listed:
			msg = ""
		}
		g.drawText(gtx, image.Pt(pad+gtx.Dp(14), y+rowH+gtx.Dp(8)), g.th.SmallSize, font.Normal, col, msg, inner-gtx.Dp(28))
	}
	clipList := clip.Rect(area).Push(gtx.Ops)
	firstRow := int(pk.offset) / rowH
	shift := int(pk.offset) - firstRow*rowH
	for i := 0; i <= visible; i++ {
		idx := firstRow + i
		if idx >= len(rows) {
			break
		}
		r := rows[idx]
		top := y + i*rowH - shift
		rect := image.Rect(pad+gtx.Dp(4), top+gtx.Dp(2), pad+inner-gtx.Dp(10), top+rowH-gtx.Dp(2))
		c := g.clickable(fmt.Sprintf("pick-%d", i))
		if c.Clicked(gtx) {
			clipList.Pop()
			g.pickActivate(rows[idx])
			return gtx.Dp(120)
		}
		func() {
			stc := op.Offset(rect.Min).Push(gtx.Ops)
			defer stc.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(rect.Size())
			c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				bg := color.NRGBA{}
				switch {
				case idx == pk.sel:
					bg = g.th.Selected
				case c.Hovered():
					bg = g.th.Hover
				}
				if bg.A > 0 {
					paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: rect.Size()}, gtx.Dp(6)).Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: rect.Size()}
			})
		}()
		cy := rect.Min.Y + rect.Dy()/2
		ix := rect.Min.X + gtx.Dp(10)
		switch {
		case r.up:
			iconArrowUp(gtx, image.Pt(ix+gtx.Dp(7), cy), g.th.TextDim, float32(gtx.Dp(5)))
		case r.entry.dir:
			docIconFolder(gtx, image.Pt(ix+gtx.Dp(7), cy), float32(gtx.Dp(7)), g.th.Accent)
		default:
			docIconFile(gtx, image.Pt(ix+gtx.Dp(7), cy), float32(gtx.Dp(7)), g.th.TextDim)
		}
		label := r.label
		if r.up {
			label = "Parent folder"
		}
		g.drawTextMid(gtx, image.Pt(ix+gtx.Dp(24), cy), g.th.UISize, font.Normal, g.th.Text, label, rect.Dx()-gtx.Dp(90))
		switch {
		case !r.up && !r.entry.dir:
			tag := "MD"
			if r.entry.kind == docPDF {
				tag = "PDF"
			}
			g.drawTextRightMid(gtx, image.Pt(rect.Max.X-gtx.Dp(10), cy), g.th.TinySize, g.th.TextFaint, tag)
		case r.entry.dir:
			iconChevronRight(gtx, image.Pt(rect.Max.X-gtx.Dp(14), cy), g.th.TextFaint, float32(gtx.Dp(3.5)))
		}
	}
	clipList.Pop()
	// Passing, so the rows below still see the pointer and the clicks.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	wheel := clip.Rect(area).Push(gtx.Ops)
	event.Op(gtx.Ops, &pk.tag)
	wheel.Pop()
	pass.Pop()
	// The scroll position, when the list is longer than its window.
	if maxOff > 0 {
		h := max(gtx.Dp(22), int(float32(area.Dy())*float32(area.Dy())/float32(len(rows)*rowH)))
		ty := area.Min.Y + int(float32(area.Dy()-h)*pk.offset/maxOff)
		paint.FillShape(gtx.Ops, g.th.TextFaint, clip.UniformRRect(image.Rect(area.Max.X-gtx.Dp(7), ty+gtx.Dp(2), area.Max.X-gtx.Dp(4), ty+h-gtx.Dp(2)), gtx.Dp(2)).Op(gtx.Ops))
	}
	return y + area.Dy() - y0
}

func iconChevronLeft(gtx layout.Context, c image.Point, col color.NRGBA, r float32) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(max(1, gtx.Dp(1.6)))
	strokeLine(gtx, f32.Pt(x+r*0.45, y-r), f32.Pt(x-r*0.55, y), w, col)
	strokeLine(gtx, f32.Pt(x-r*0.55, y), f32.Pt(x+r*0.45, y+r), w, col)
}

func iconChevronRight(gtx layout.Context, c image.Point, col color.NRGBA, r float32) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(max(1, gtx.Dp(1.6)))
	strokeLine(gtx, f32.Pt(x-r*0.45, y-r), f32.Pt(x+r*0.55, y), w, col)
	strokeLine(gtx, f32.Pt(x+r*0.55, y), f32.Pt(x-r*0.45, y+r), w, col)
}

func iconArrowUp(gtx layout.Context, c image.Point, col color.NRGBA, r float32) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(max(1, gtx.Dp(1.6)))
	strokeLine(gtx, f32.Pt(x, y+r), f32.Pt(x, y-r), w, col)
	strokeLine(gtx, f32.Pt(x-r*0.8, y-r*0.2), f32.Pt(x, y-r), w, col)
	strokeLine(gtx, f32.Pt(x+r*0.8, y-r*0.2), f32.Pt(x, y-r), w, col)
}

// docIconFolder draws a folder, r wide from its centre.
func docIconFolder(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x-r, y-r*0.55))
	p.LineTo(f32.Pt(x-r*0.2, y-r*0.55))
	p.LineTo(f32.Pt(x+r*0.1, y-r*0.25))
	p.LineTo(f32.Pt(x+r, y-r*0.25))
	p.LineTo(f32.Pt(x+r, y+r*0.65))
	p.LineTo(f32.Pt(x-r, y+r*0.65))
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: float32(max(1, gtx.Dp(1.3)))}.Op())
}

// docIconFile draws a sheet of paper.
func docIconFile(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x-r*0.7, y-r))
	p.LineTo(f32.Pt(x+r*0.2, y-r))
	p.LineTo(f32.Pt(x+r*0.7, y-r*0.45))
	p.LineTo(f32.Pt(x+r*0.7, y+r))
	p.LineTo(f32.Pt(x-r*0.7, y+r))
	p.Close()
	w := float32(max(1, gtx.Dp(1.3)))
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	strokeLine(gtx, f32.Pt(x-r*0.3, y+r*0.1), f32.Pt(x+r*0.3, y+r*0.1), w, col)
	strokeLine(gtx, f32.Pt(x-r*0.3, y+r*0.5), f32.Pt(x+r*0.3, y+r*0.5), w, col)
}
