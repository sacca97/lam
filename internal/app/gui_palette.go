//go:build gui

package app

import (
	"image"
	"image/color"
	"sort"
	"strings"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// The command palette: one box to reach a session or anything lam can do.

type paletteItem struct {
	label, detail string
	run           func()
}

const paletteRows = 9

func (g *guiApp) openPalette() { g.dlg = dialog{kind: dlgPalette} }

// paletteAll is everything the palette offers now: the sessions on every
// machine, then the actions that make sense for what is on screen.
func (g *guiApp) paletteAll() []paletteItem {
	var items []paletteItem
	add := func(label, detail string, run func()) { items = append(items, paletteItem{label, detail, run}) }
	mod := "Ctrl+Shift+"
	if guiMac {
		mod = "⌘"
	}
	for _, s := range g.allSessions() {
		name := s.Name
		where := providerName(s)
		if s.CWD != "" {
			where += " · " + shortenHomePath(s.CWD, g.sourceFor(s.Host))
		}
		add("Go to "+name, where, func() { g.selectSession(name); g.setFocus(focusMain) })
	}
	add("New session", mod+"N", g.openNewSession)
	add("Open a document", mod+"D", g.openDocumentDialog)
	if g.remote.Load() {
		_, host := g.mb.Remote()
		add("Disconnect from "+host, "", g.openConnect)
	}
	add("Connect to a host", mod+"O", g.openConnect)
	if s, ok := g.selectedInfo(); ok {
		add("Rename "+s.Name, mod+"R", func() { g.openRename(s) })
		add("Stop "+s.Name, mod+"W", func() { g.openStop(s) })
		add("Toggle the terminal", mod+"T", g.toggleTerminal)
	}
	for _, o := range g.allSessions() {
		if name := o.Name; name != g.selected && name != g.beside && !o.Shell {
			add("Show "+name+" beside", "second pane", func() { g.openBeside(name) })
		}
	}
	if g.beside != "" {
		add("Close the second pane", "", g.closeBeside)
	}
	add("Toggle the sidebar", mod+"B", func() { g.hideSide = !g.hideSide })
	if g.settings.GUIGroup == "folder" {
		add("Group sessions by state", "", func() { g.settings.GUIGroup = "state"; saveSettings(g.settings); g.rowsVer.Add(1) })
	} else {
		add("Group sessions by folder", "", func() { g.settings.GUIGroup = "folder"; saveSettings(g.settings); g.rowsVer.Add(1) })
	}
	add("Search sessions", mod+"F", func() { g.focus, g.hideSide = focusFilter, false })
	add("Settings", mod+",", func() { g.dlg = dialog{kind: dlgSettings} })
	add("Keyboard shortcuts", "F1", func() { g.dlg = dialog{kind: dlgHelp} })
	add("Stop all sessions and quit", mod+"Q", g.openQuitAll)
	return items
}

// fuzzyScore is how well q matches s: lower is better, -1 when it does not.
// A substring beats letters spread through the text, and an earlier one beats
// a later one.
func fuzzyScore(q, s string) int {
	q, s = strings.ToLower(q), strings.ToLower(s)
	if q == "" {
		return 0
	}
	if i := strings.Index(s, q); i >= 0 {
		return i
	}
	at := 0
	for _, r := range q {
		if r == ' ' {
			continue
		}
		i := strings.IndexRune(s[at:], r)
		if i < 0 {
			return -1
		}
		at += i + len(string(r))
	}
	return 1000 + len(s)
}

// paletteMatches are the items that match the query, best first.
func (g *guiApp) paletteMatches() []paletteItem {
	q := g.dlg.field.String()
	all := g.paletteAll()
	type scored struct {
		it    paletteItem
		score int
	}
	var hits []scored
	for _, it := range all {
		if sc := fuzzyScore(q, it.label+" "+it.detail); sc >= 0 {
			if strings.HasSuffix(it.label, " beside") {
				sc += 10 // going to a session is the likelier wish
			}
			hits = append(hits, scored{it, sc})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score < hits[j].score })
	out := make([]paletteItem, len(hits))
	for i, h := range hits {
		out[i] = h.it
	}
	return out
}

func (g *guiApp) paletteKey(ev key.Event) {
	d := &g.dlg
	items := g.paletteMatches()
	switch {
	case ev.Name == key.NameEscape:
		g.closeDialog()
	case ev.Name == key.NameUpArrow, ev.Name == key.NameTab && ev.Modifiers.Contain(key.ModShift):
		d.row = max(0, d.row-1)
		d.reveal = true
	case ev.Name == key.NameDownArrow, ev.Name == key.NameTab:
		d.row = min(max(len(items)-1, 0), d.row+1)
		d.reveal = true
	case ev.Name == key.NameReturn, ev.Name == key.NameEnter:
		if d.row < len(items) {
			run := items[d.row].run
			g.closeDialog()
			run()
		}
	case d.field.key(ev):
		d.row, d.scroll = 0, 0
	}
}

func (g *guiApp) layoutPalette(gtx layout.Context) {
	size := gtx.Constraints.Max
	d := &g.dlg
	paint.FillShape(gtx.Ops, g.th.Scrim, clip.Rect{Max: size}.Op())
	st := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.scrim)
	st.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.scrim, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			g.closeDialog()
			return
		}
	}

	items := g.paletteMatches()
	d.row = min(d.row, max(len(items)-1, 0))
	rowH, pad := gtx.Dp(36), gtx.Dp(10)
	visible := min(max(len(items), 1), paletteRows)
	visible = min(visible, max(1, (size.Y-gtx.Dp(140))/rowH))
	// Keep the chosen row in view.
	if d.reveal {
		d.scroll = min(max(d.scroll, d.row-visible+1), d.row)
		d.reveal = false
	}
	d.scroll = max(0, min(d.scroll, max(len(items)-visible, 0)))
	w := min(size.X-gtx.Dp(32), gtx.Dp(600))
	h := pad + gtx.Dp(40) + pad + visible*rowH + pad
	top := min(max(gtx.Dp(16), size.Y/5), max(gtx.Dp(16), size.Y-h-gtx.Dp(16)))
	card := image.Rect((size.X-w)/2, top, (size.X+w)/2, top+h)
	cardArea := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, &d.cardTag)
	cardArea.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.cardTag, Kinds: pointer.Press | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Scroll {
			step := 1
			if pe.Scroll.Y < 0 {
				step = -1
			}
			d.scroll = max(0, min(d.scroll+step, max(len(items)-visible, 0)))
		}
	}
	for i := 6; i >= 1; i-- {
		sh := color.NRGBA{A: uint8(float32(14+4*(6-i)) * g.th.Shadow)}
		r := card.Inset(-gtx.Dp(unit.Dp(i * 2))).Add(image.Pt(0, gtx.Dp(unit.Dp(i))))
		paint.FillShape(gtx.Ops, sh, clip.UniformRRect(r, gtx.Dp(12)+gtx.Dp(unit.Dp(i*2))).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, g.th.Sidebar, clip.UniformRRect(card, gtx.Dp(12)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: clip.UniformRRect(card, gtx.Dp(12)).Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())

	defer op.Offset(card.Min).Push(gtx.Ops).Pop()
	inner := w - 2*pad
	g.drawField(gtx, image.Rect(pad, pad, pad+inner, pad+gtx.Dp(40)), &d.field, "Type a command or a session name")
	y := pad + gtx.Dp(40) + pad
	if len(items) == 0 {
		g.drawText(gtx, image.Pt(pad+gtx.Dp(8), y+gtx.Dp(8)), g.th.UISize, font.Normal, g.th.TextFaint, "Nothing matches", inner)
		return
	}
	var chosen func()
	for i := 0; i < visible && d.scroll+i < len(items); i++ {
		idx := d.scroll + i
		it := items[idx]
		r := image.Rect(pad, y+i*rowH, pad+inner, y+(i+1)*rowH)
		c := g.clickable(paletteID(i))
		if c.Clicked(gtx) {
			chosen = it.run
		}
		func() {
			st := op.Offset(r.Min).Push(gtx.Ops)
			defer st.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(r.Size())
			c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(8))
				switch {
				case idx == d.row:
					paint.FillShape(gtx.Ops, g.th.Selected, rr.Op(gtx.Ops))
				case c.Hovered():
					paint.FillShape(gtx.Ops, g.th.Hover, rr.Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: r.Size()}
			})
			col := g.th.TextDim
			if idx == d.row {
				col = g.th.Text
			}
			lw := g.drawTextMid(gtx, image.Pt(gtx.Dp(12), r.Dy()/2), g.th.UISize, font.Medium, col, it.label, r.Dx()*3/5)
			if it.detail != "" {
				g.drawTextMid(gtx, image.Pt(gtx.Dp(24)+lw, r.Dy()/2), g.th.TinySize, font.Normal, g.th.TextFaint, it.detail, r.Dx()-lw-gtx.Dp(36))
			}
		}()
	}
	if chosen != nil {
		g.closeDialog()
		chosen()
	}
}

func paletteID(i int) string { return "pal-" + string(rune('a'+i)) }
