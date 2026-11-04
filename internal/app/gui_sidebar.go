//go:build gui

package app

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// The sidebar: where the sessions run, a search field with the new session
// button, the sessions grouped by project, and settings at the bottom. A
// session row says what it is, what it is doing and since when; color is
// spent only on an agent at work and one that waits for you.

// providerName is what a session runs, as people call it.
func providerName(s sessionInfo) string {
	kind := strings.ToLower(commandName(s.Command))
	if kind == "" {
		kind = strings.ToLower(strings.SplitN(shortName(s.Name), "-", 2)[0])
	}
	switch {
	case s.Shell, kind == "shell", kind == "terminal", kind == "bash", kind == "zsh", kind == "fish", kind == "sh":
		return "Shell"
	case strings.HasPrefix(kind, "claude"):
		return "Claude Code"
	case strings.HasPrefix(kind, "codex"):
		return "Codex"
	case strings.HasPrefix(kind, "opencode"):
		return "opencode"
	case kind == "agy":
		return "Antigravity"
	case kind == "pi":
		return "pi"
	case kind == "omp":
		return "Oh My Pi"
	case strings.HasPrefix(kind, "gemini"):
		return "Gemini"
	}
	return kind
}

// statusOf is a session's state in words, its color, and how long it has
// been in it ("" when unknown).
func (g *guiApp) statusOf(s sessionInfo, now time.Time) (status string, col color.NRGBA, age string) {
	g.mu.Lock()
	since, ok := g.statusSince[s.Name]
	g.mu.Unlock()
	if ok {
		d := now.Sub(since)
		age = shortDuration(d)
		if next := since.Add(nextAgeStep(d)); g.nextAge.IsZero() || next.Before(g.nextAge) {
			g.nextAge = next // when this label changes, to redraw then and not before
		}
	}
	switch {
	case s.Unread:
		return "needs you", g.th.Unread, age
	case s.Activity == "active":
		return "working", g.th.Accent, age
	}
	return "", g.th.TextFaint, age
}

// nextAgeStep is the age at which shortDuration's text changes after d.
func nextAgeStep(d time.Duration) time.Duration {
	switch {
	case d < time.Minute:
		return time.Minute
	case d < time.Hour:
		return (d/time.Minute + 1) * time.Minute
	case d < 24*time.Hour:
		return (d/time.Hour + 1) * time.Hour
	}
	return (d/(24*time.Hour) + 1) * 24 * time.Hour
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// hostLabel is this machine's name.
func (g *guiApp) hostLabel() string {
	if g.localHost == "" {
		g.localHost = "this machine"
		if h, err := os.Hostname(); err == nil && h != "" {
			g.localHost = strings.SplitN(h, ".", 2)[0]
		}
	}
	return g.localHost
}

// moveArea makes r drag the window, for lam's own title bar.
func moveArea(gtx layout.Context, r image.Rectangle) {
	st := clip.Rect(r).Push(gtx.Ops)
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
	st.Pop()
}

func (g *guiApp) layoutSidebar(gtx layout.Context) {
	g.moreShown, g.moreNext = g.moreNext, "" // the row whose dots were shown last frame
	g.plusShown, g.plusNext = g.plusNext, "" // likewise the project heading whose + was shown
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, g.th.Sidebar, clip.Rect{Max: size}.Op())
	pad := gtx.Dp(g.th.Pad)
	g.mu.Lock()
	listErr := g.listErr
	g.mu.Unlock()

	// No heading: the sidebar is all search and sessions, from the top edge. The
	// strip above the search still drags the window where lam draws its own title bar.
	top := gtx.Dp(10)
	moveArea(gtx, image.Rect(0, 0, size.X, top))

	// Search, and the new session button.
	fh := gtx.Dp(34)
	fy := top
	nb := image.Rect(size.X-pad-fh, fy, size.X-pad, fy+fh)
	g.layoutSearch(gtx, image.Rect(pad, fy, nb.Min.X-gtx.Dp(6), fy+fh))
	if g.ghostButton(gtx, "new", nb, "New session", true, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconPlus(gtx, c, float32(gtx.Dp(6)), float32(max(1, gtx.Dp(1.6))), col)
	}) {
		g.openNewSession()
	}

	// The list.
	footer := gtx.Dp(46)
	listArea := image.Rect(0, fy+fh+gtx.Dp(10), size.X, size.Y-footer)
	rows := g.cachedRows()
	now := time.Now()
	func() {
		defer op.Offset(listArea.Min).Push(gtx.Ops).Pop()
		lg := gtx
		lg.Constraints = layout.Exact(listArea.Size())
		defer clip.Rect{Max: listArea.Size()}.Push(gtx.Ops).Pop()
		if len(rows) <= 1 {
			g.layoutEmptyList(lg, listErr, pad)
			return
		}
		animate := false
		g.nextAge = time.Time{}
		g.list.Layout(lg, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			if !rows[i].section && !rows[i].header && rows[i].s.Activity == "active" {
				animate = true
			}
			return g.layoutRow(gtx, rows[i], pad, now)
		})
		g.mu.Lock()
		looked := g.windowFocused
		g.mu.Unlock()
		if animate && looked { // an unfocused window does not redraw for the halo
			gtx.Execute(op.InvalidateCmd{At: now.Add(60 * time.Millisecond)})
		} else if !g.nextAge.IsZero() {
			gtx.Execute(op.InvalidateCmd{At: g.nextAge})
		}
	}()

	// Footer: settings and shortcuts.
	paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(0, size.Y-footer), Max: image.Pt(size.X, size.Y-footer+max(1, gtx.Dp(1)))}.Op())
	b := gtx.Dp(30)
	fcy := size.Y - footer/2
	sr := image.Rect(pad, fcy-b/2, pad+b, fcy-b/2+b)
	if g.ghostButton(gtx, "settings", sr, "Settings", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconSliders(gtx, c, float32(gtx.Dp(7)), col)
	}) {
		g.dlg = dialog{kind: dlgSettings}
	}
	hr := sr.Add(image.Pt(b+gtx.Dp(2), 0))
	if g.ghostButton(gtx, "help", hr, "Keyboard shortcuts", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconKeyboard(gtx, c, float32(gtx.Dp(8)), col)
	}) {
		g.dlg = dialog{kind: dlgHelp}
	}
	// A newer release: a quiet button at the right end of the footer.
	if tag, ok := g.upd.available(); ok {
		chip := image.Pt(size.X-pad, fcy)
		g.buttonStyled(gtx, "update-chip", "↑ "+tag, chip, true, false, false)
		if g.clicked(gtx, "update-chip") {
			g.openUpdate()
		}
	}
	// Connecting to a host: the icon with its words, and the host's name once
	// connected, so it is clear what this button is and where the sessions run.
	cr := hr.Add(image.Pt(b+gtx.Dp(2), 0))
	label, tip := "Connect", "Connect to a host  "+map[bool]string{true: "⌘O", false: "Ctrl+Shift+O"}[guiMac]
	if g.remote {
		_, host := g.mb.Remote()
		label, tip = "Disconnect", "Disconnect from "+host+" (its sessions keep running)"
	}
	reserve := gtx.Dp(8)
	if _, ok := g.upd.available(); ok {
		reserve += gtx.Dp(72)
	}
	gl, tw, _ := g.shape(gtx, g.th.TinySize, font.Medium, label, max(gtx.Dp(30), size.X-cr.Min.X-pad-b-reserve))
	cr.Max.X = cr.Min.X + b + tw + gtx.Dp(6)
	if g.ghostButton(gtx, "connect", cr, tip, g.remote, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		if g.remote {
			col = g.th.Accent
		}
		iconServer(gtx, image.Pt(cr.Min.X+b/2, c.Y), float32(gtx.Dp(7)), col)
		if len(gl) > 0 {
			asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
			g.paintGlyphs(gtx, gl, image.Pt(cr.Min.X+b-gtx.Dp(2), c.Y+(asc-desc)/2), col)
		}
	}) {
		if g.remote {
			g.connID++
			g.switchBackend(nil)
		} else {
			g.openConnect()
		}
	}
}

func (g *guiApp) layoutEmptyList(gtx layout.Context, listErr string, pad int) {
	title, body := "No sessions yet", "Start one, or connect to a host."
	switch {
	case listErr != "":
		title, body = "Sessions unavailable", listErr
	case g.filter != "":
		title, body = "No matches", "Nothing is called “"+g.filter+"”."
	}
	w := gtx.Constraints.Max.X - 2*pad
	g.drawText(gtx, image.Pt(pad+gtx.Dp(6), gtx.Dp(14)), g.th.SmallSize, font.Medium, g.th.TextDim, title, w)
	g.drawText(gtx, image.Pt(pad+gtx.Dp(6), gtx.Dp(36)), g.th.TinySize, font.Normal, g.th.TextFaint, body, w)
	if g.filter != "" {
		return
	}
	// With nothing to show, the two things to do are buttons, not a hint.
	x, y := pad+gtx.Dp(6), gtx.Dp(92)
	if listErr == "" {
		g.buttonStyled(gtx, "empty-new", "New session", image.Pt(x, y), false, false, true)
		if g.clicked(gtx, "empty-new") {
			g.openNewSession()
		}
		y += gtx.Dp(40)
	}
	label := "Connect to a host"
	if g.remote {
		label = "Change host"
	}
	g.buttonStyled(gtx, "empty-connect", label, image.Pt(x, y), false, false, false)
	if g.clicked(gtx, "empty-connect") {
		g.openConnect()
	}
}

// layoutSearch draws the filter field; clicking it gives it the keyboard.
func (g *guiApp) layoutSearch(gtx layout.Context, r image.Rectangle) {
	fc := g.clickable("filter")
	if fc.Clicked(gtx) {
		g.focus = focusFilter
	}
	focused := g.focus == focusFilter
	st := op.Offset(r.Min).Push(gtx.Ops)
	defer st.Pop()
	cg := gtx
	cg.Constraints = layout.Exact(r.Size())
	fc.Layout(cg, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(8))
		bg, border := g.th.Surface, g.th.Border
		switch {
		case focused:
			border = g.th.Accent
		case fc.Hovered():
			bg = g.th.Hover
		}
		paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
		pointer.CursorText.Add(gtx.Ops)
		return layout.Dimensions{Size: r.Size()}
	})
	h := r.Dy()
	iconSearch(gtx, image.Pt(gtx.Dp(16), h/2), float32(gtx.Dp(4.5)), g.th.TextFaint)
	tx := gtx.Dp(30)
	text, col := g.filter, g.th.Text
	if text == "" {
		text, col = "Search", g.th.TextFaint
	}
	w := g.drawTextMid(gtx, image.Pt(tx, h/2), g.th.TinySize, font.Normal, col, text, r.Dx()-tx-gtx.Dp(28))
	if focused {
		x := tx + 1
		if g.filter != "" {
			x += w
		}
		paint.FillShape(gtx.Ops, g.th.Accent, clip.Rect{Min: image.Pt(x, gtx.Dp(9)), Max: image.Pt(x+max(1, gtx.Dp(1.5)), h-gtx.Dp(9))}.Op())
	}
	if g.filter != "" {
		cr := image.Rect(r.Dx()-gtx.Dp(28), gtx.Dp(3), r.Dx()-gtx.Dp(3), h-gtx.Dp(3))
		if g.ghostButton(gtx, "filter-clear", cr, "", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconCross(gtx, c, float32(gtx.Dp(3.5)), float32(max(1, gtx.Dp(1.4))), col)
		}) {
			g.filter = ""
		}
	}
}

// ghostButton draws a square icon button, transparent until hovered (or
// outlined when bordered), with a tooltip, and reports whether it was clicked.
func (g *guiApp) ghostButton(gtx layout.Context, id string, r image.Rectangle, tip string, bordered bool, icon func(layout.Context, image.Point, color.NRGBA)) bool {
	return g.ghostButtonTinted(gtx, id, r, tip, bordered, color.NRGBA{}, icon)
}

// ghostButtonTinted is ghostButton with a hover color (the close button's red).
func (g *guiApp) ghostButtonTinted(gtx layout.Context, id string, r image.Rectangle, tip string, bordered bool, hoverBg color.NRGBA, icon func(layout.Context, image.Point, color.NRGBA)) bool {
	c := g.clickable(id)
	clicked := c.Clicked(gtx)
	hovered := c.Hovered()
	fg := g.th.TextDim
	st := op.Offset(r.Min).Push(gtx.Ops)
	cg := gtx
	cg.Constraints = layout.Exact(r.Size())
	c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(7))
		switch {
		case hovered && hoverBg.A > 0:
			paint.FillShape(gtx.Ops, hoverBg, rr.Op(gtx.Ops))
		case hovered:
			paint.FillShape(gtx.Ops, g.th.Selected, rr.Op(gtx.Ops))
		case bordered:
			paint.FillShape(gtx.Ops, g.th.Surface, rr.Op(gtx.Ops))
		}
		if bordered {
			paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: r.Size()}
	})
	st.Pop()
	if hovered {
		fg = g.th.Text
		if tip != "" {
			g.tooltip(gtx, r, tip)
		}
	}
	icon(gtx, image.Pt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2), fg)
	return clicked
}

// tooltip shows a label under r (above it at the bottom edge), drawn over
// everything else.
func (g *guiApp) tooltip(gtx layout.Context, r image.Rectangle, text string) {
	m := op.Record(gtx.Ops)
	gl, w, _ := g.shape(gtx, g.th.TinySize, font.Medium, text, 1<<20)
	ph, pw := gtx.Dp(26), w+gtx.Dp(18)
	x := (r.Min.X+r.Max.X)/2 - pw/2
	if edge := gtx.Constraints.Max.X - gtx.Dp(4); x+pw > edge {
		x = edge - pw
	}
	if x < gtx.Dp(4) {
		x = gtx.Dp(4)
	}
	// Under the control, or above it when there is no room below: the footer
	// buttons sit at the bottom edge of the window.
	y := r.Max.Y + gtx.Dp(6)
	if y+ph > gtx.Constraints.Max.Y-gtx.Dp(4) {
		y = r.Min.Y - gtx.Dp(6) - ph
	}
	tr := image.Rect(x, y, x+pw, y+ph)
	rr := clip.UniformRRect(tr, gtx.Dp(6))
	paint.FillShape(gtx.Ops, g.th.Selected, rr.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
	if len(gl) > 0 {
		asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
		g.paintGlyphs(gtx, gl, image.Pt(tr.Min.X+gtx.Dp(9), tr.Min.Y+ph/2+(asc-desc)/2), g.th.Text)
	}
	op.Defer(gtx.Ops, m.Stop())
}

func (g *guiApp) layoutRow(gtx layout.Context, r guiRow, pad int, now time.Time) layout.Dimensions {
	w := gtx.Constraints.Max.X
	if r.section {
		return g.layoutSection(gtx, r, pad)
	}
	if r.header {
		return g.layoutGroupHeader(gtx, r, pad)
	}
	s := r.s
	click := g.rowClicks[s.Name]
	if click == nil {
		click = new(widget.Clickable)
		g.rowClicks[s.Name] = click
		g.rowTags[s.Name] = new(bool)
	}
	for {
		ev, ok := click.Update(gtx)
		if !ok {
			break
		}
		if ev.NumClicks > 0 {
			g.selectSession(s.Name)
			g.focus = focusMain
			if ev.NumClicks == 2 {
				g.openRename(s)
			}
		}
	}
	// Right click opens the session's menu where the pointer is.
	tag := g.rowTags[s.Name]
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Buttons.Contain(pointer.ButtonSecondary) {
			g.selectSession(s.Name)
			g.menu = &rowMenu{s: s, at: g.lastPress}
		}
	}
	itemH := gtx.Dp(g.th.ItemH)
	inner := image.Rect(pad-gtx.Dp(2), gtx.Dp(1), w-pad+gtx.Dp(2), itemH-gtx.Dp(1))
	selected := s.Name == g.selected
	hovered := click.Hovered()
	menuOpen := g.menu != nil && g.menu.s.Name == s.Name
	// The dots sit on top of the row, so the pointer over them is no longer over
	// the row. They stay shown while they were shown last frame, which is what
	// lets them take the pointer and see it enter and leave; they go once they
	// are neither over a hovered row nor hovered themselves.
	showMore := hovered || menuOpen || g.moreShown == s.Name
	dims := click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(inner, gtx.Dp(g.th.ItemRadius))
		switch {
		case selected:
			paint.FillShape(gtx.Ops, g.th.Selected, rr.Op(gtx.Ops))
		case hovered || menuOpen:
			paint.FillShape(gtx.Ops, g.th.Hover, rr.Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(w, itemH)}
	})

	cy := itemH / 2
	line1, line2 := cy-gtx.Dp(8), cy+gtx.Dp(8)
	dotX := inner.Min.X + gtx.Dp(16)
	g.statusDot(gtx, image.Pt(dotX, line1), s, now)
	x := dotX + gtx.Dp(14)
	right := inner.Max.X - gtx.Dp(10)

	status, scol, age := g.statusOf(s, now)
	if showMore {
		mr := image.Rect(right-gtx.Dp(26), cy-gtx.Dp(12), right+gtx.Dp(2), cy+gtx.Dp(12))
		if g.ghostButton(gtx, "more-"+s.Name, mr, "", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconMore(gtx, c, float32(gtx.Dp(4.5)), col)
		}) {
			g.selectSession(s.Name)
			g.menu = &rowMenu{s: s, at: g.lastPress}
		}
		if hovered || menuOpen || g.clickable("more-"+s.Name).Hovered() {
			g.moreNext = s.Name
		}
		right = mr.Min.X - gtx.Dp(4)
	} else if age != "" && status != "" {
		_, aw, _ := g.shape(gtx, g.th.TinySize, font.Normal, age, 1<<20)
		g.drawTextRightMid(gtx, image.Pt(right, line1), g.th.TinySize, g.th.TextFaint, age)
		right -= aw + gtx.Dp(8)
	}

	label := sessionLabel(s)
	if g.collapsed[groupKey(s)] {
		label = shortName(s.Name)
	}
	nameCol, weight := g.th.TextDim, font.Medium
	if selected {
		nameCol = g.th.Text
	}
	g.drawTextMid(gtx, image.Pt(x, line1), g.th.UISize, weight, nameCol, label, right-x)
	// Second line: the agent, then its state, colored only when it matters.
	sub := providerName(s)
	sx := x
	sx += g.drawTextMid(gtx, image.Pt(sx, line2), g.th.TinySize, font.Normal, g.th.TextFaint, sub, inner.Max.X-gtx.Dp(10)-sx)
	if status != "" {
		sx += g.drawTextMid(gtx, image.Pt(sx, line2), g.th.TinySize, font.Normal, g.th.TextFaint, "  ·  ", inner.Max.X-sx)
		g.drawTextMid(gtx, image.Pt(sx, line2), g.th.TinySize, font.Medium, scol, status, inner.Max.X-gtx.Dp(10)-sx)
	}

	pt := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	pt.Pop()
	return dims
}

// statusDot marks a session: green with a soft breathing halo while the
// agent works, warm when it finished and waits, a hollow ring when idle.
func (g *guiApp) statusDot(gtx layout.Context, c image.Point, s sessionInfo, now time.Time) {
	r := float32(gtx.Dp(3.5))
	circle := func(rad float32, col color.NRGBA) {
		paint.FillShape(gtx.Ops, col, clip.Ellipse{
			Min: image.Pt(int(float32(c.X)-rad), int(float32(c.Y)-rad)),
			Max: image.Pt(int(float32(c.X)+rad+0.5), int(float32(c.Y)+rad+0.5)),
		}.Op(gtx.Ops))
	}
	switch {
	case s.Unread:
		circle(r, g.th.Unread)
	case s.Activity == "active":
		phase := 0.5 + 0.5*math.Sin(float64(now.UnixMilli()%1600)/1600*2*math.Pi)
		halo := g.th.Accent
		halo.A = uint8(40 + 50*phase)
		circle(r+float32(gtx.Dp(2.5))+float32(phase)*float32(gtx.Dp(1.5)), halo)
		circle(r, g.th.Accent)
	default:
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(float32(c.X)+r-0.5, float32(c.Y)))
		p.ArcTo(f32.Pt(float32(c.X), float32(c.Y)), f32.Pt(float32(c.X), float32(c.Y)), 2*math.Pi)
		paint.FillShape(gtx.Ops, g.th.Idle, clip.Stroke{Path: p.End(), Width: float32(max(1, gtx.Dp(1.3)))}.Op())
	}
}

// layoutGroupHeader draws a project heading: a click folds it, + starts a
// session in that folder.
// layoutSection is the heading over one machine's sessions: whether it is
// this one or a remote host, which, and for a remote one the state of the link.
func (g *guiApp) layoutSection(gtx layout.Context, r guiRow, pad int) layout.Dimensions {
	w := gtx.Constraints.Max.X
	gap := 0
	if r.host != "" {
		// The break between two machines is heavier than the one between two
		// projects: more air, and a thicker rule across the whole sidebar in the
		// border colour.
		gap = gtx.Dp(20)
		rule := max(2, gtx.Dp(2))
		paint.FillShape(gtx.Ops, g.th.Border, clip.Rect{Min: image.Pt(0, gap/2-rule/2), Max: image.Pt(w, gap/2-rule/2+rule)}.Op())
	}
	h := gtx.Dp(30)
	band := image.Rect(pad-gtx.Dp(2), gap, w-pad+gtx.Dp(2), gap+h)
	paint.FillShape(gtx.Ops, g.th.Hover, clip.UniformRRect(band, gtx.Dp(g.th.ItemRadius)).Op(gtx.Ops))
	cy := gap + h/2

	kind, detail, detailCol := "LOCAL", r.label, g.th.TextFaint
	iconCol := g.th.TextDim
	if r.saved {
		kind, detailCol, iconCol = "REMOTE", g.th.TextDim, g.th.TextFaint
	} else if r.host != "" {
		kind = "REMOTE"
		if st := currentLinkStatus(); !st.Up {
			detail, detailCol, iconCol = linkStatusText(st, time.Now()), g.th.Unread, g.th.Unread
			gtx.Execute(op.InvalidateCmd{At: time.Now().Add(time.Second)})
		} else {
			iconCol = g.th.Accent
			if v := g.mb.remoteVersion(); v != "" {
				detail += " · " + v
			}
		}
	}
	x := band.Min.X + gtx.Dp(14)
	if r.host != "" {
		iconServer(gtx, image.Pt(x, cy), float32(gtx.Dp(6)), iconCol)
	} else {
		iconTerminal(gtx, image.Pt(x, cy), float32(gtx.Dp(6)), iconCol)
	}
	x += gtx.Dp(14)
	br := image.Rect(band.Max.X-gtx.Dp(28), cy-gtx.Dp(12), band.Max.X-gtx.Dp(4), cy+gtx.Dp(12))
	if r.saved {
		// A host connected to before: kept here, not connected, with a way back.
		forget := br
		if g.ghostButton(gtx, "forget-"+r.host, forget, "Forget "+r.label, false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconCross(gtx, c, float32(gtx.Dp(3.5)), float32(max(1, gtx.Dp(1.2))), col)
		}) {
			g.forgetHost(r.host)
		}
		gl, tw, _ := g.shape(gtx, g.th.TinySize, font.Medium, "Connect", w)
		cr := image.Rect(forget.Min.X-gtx.Dp(6)-tw-gtx.Dp(16), cy-gtx.Dp(12), forget.Min.X-gtx.Dp(2), cy+gtx.Dp(12))
		if g.ghostButtonTinted(gtx, "connect-"+r.host, cr, "Connect to "+r.label, true, color.NRGBA{}, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			if len(gl) > 0 {
				asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
				g.paintGlyphs(gtx, gl, image.Pt(c.X-tw/2, c.Y+(asc-desc)/2), g.th.Accent)
			}
		}) {
			g.openConnect()
			g.dlg.field.set(r.host)
			g.startConnect(r.host)
		}
		br = cr
	} else if g.ghostButton(gtx, "new-on-"+r.host, br, "New session on "+r.label, false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconPlus(gtx, c, float32(gtx.Dp(4.5)), float32(max(1, gtx.Dp(1.4))), col)
	}) {
		g.openNewSessionOn(r.host)
	}
	x += g.drawTextMid(gtx, image.Pt(x, cy), g.th.TinySize, font.Bold, g.th.Text, kind, w) + gtx.Dp(8)
	g.drawTextMid(gtx, image.Pt(x, cy), g.th.TinySize, font.Normal, detailCol, detail, br.Min.X-x-gtx.Dp(4))
	return layout.Dimensions{Size: image.Pt(w, gap+h+gtx.Dp(2))}
}

// forgetHost drops a saved host from the sidebar.
func (g *guiApp) forgetHost(host string) {
	g.settings.GUIHosts = slices.DeleteFunc(slices.Clone(g.settings.GUIHosts), func(h string) bool { return h == host })
	saveSettings(g.settings)
}

func (g *guiApp) layoutGroupHeader(gtx layout.Context, r guiRow, pad int) layout.Dimensions {
	w := gtx.Constraints.Max.X
	// Each project after the first in its section is set off by a rule and
	// some air, so a folder reads as a block.
	gap := 0
	if !r.first {
		gap = gtx.Dp(12)
		paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(pad, gap/2), Max: image.Pt(w-pad, gap/2+max(1, gtx.Dp(1)))}.Op())
	}
	h := gap + gtx.Dp(28)
	defer op.Offset(image.Pt(0, gap)).Push(gtx.Ops).Pop()
	h -= gap
	key := groupKey(sessionInfo{Host: r.host, CWD: r.cwd})
	c := g.headers[key]
	if c == nil {
		c = new(widget.Clickable)
		g.headers[key] = c
	}
	if c.Clicked(gtx) {
		g.toggleProject(key)
	}
	// The + sits on top of the heading, so the pointer over it is no longer over
	// the heading: it stays shown while it was shown last frame, or the click
	// would land on the heading and only fold the project.
	hovered := c.Hovered() || g.plusShown == key
	dims := c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	dims.Size.Y += gap
	cy := h/2 + gtx.Dp(3)
	col := g.th.TextFaint
	if hovered {
		col = g.th.TextDim
	}
	right := w - pad - gtx.Dp(4)
	if r.count > 0 {
		// The count sits at the right edge; the + that shows on hover goes left of it.
		g.drawTextRightMid(gtx, image.Pt(right, cy), g.th.TinySize, g.th.TextFaint, fmt.Sprint(r.count))
		_, cw, _ := g.shape(gtx, g.th.TinySize, font.Medium, fmt.Sprint(r.count), 1<<20)
		right -= cw + gtx.Dp(6)
	}
	if hovered {
		br := image.Rect(right-gtx.Dp(24), cy-gtx.Dp(12), right, cy+gtx.Dp(12))
		if g.ghostButton(gtx, "new-in-"+key, br, "New session here", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconPlus(gtx, c, float32(gtx.Dp(4.5)), float32(max(1, gtx.Dp(1.4))), col)
		}) {
			g.openNewSessionIn(r.cwd, r.host)
		}
		if c.Hovered() || g.clickable("new-in-"+key).Hovered() {
			g.plusNext = key
		}
		right = br.Min.X - gtx.Dp(6)
	}
	x := pad + gtx.Dp(4)
	chevron(gtx, image.Pt(x+gtx.Dp(4), cy), float32(gtx.Dp(3.2)), r.folded, col)
	x += gtx.Dp(14)
	g.drawTextMid(gtx, image.Pt(x, cy), g.th.TinySize, font.SemiBold, col, r.label, right-x-gtx.Dp(6))
	return dims
}

// drawTextCentered draws one line centred in r.
func (g *guiApp) drawTextCentered(gtx layout.Context, r image.Rectangle, size unit.Sp, weight font.Weight, c color.NRGBA, s string) {
	gl, w, _ := g.shape(gtx, size, weight, s, 1<<20)
	if len(gl) == 0 {
		return
	}
	asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
	g.paintGlyphs(gtx, gl, image.Pt((r.Min.X+r.Max.X-w)/2, (r.Min.Y+r.Max.Y)/2+(asc-desc)/2), c)
}

// Icons, drawn as paths so they are sharp at any size.

func strokeLine(gtx layout.Context, a, b f32.Point, width float32, col color.NRGBA) {
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(a)
	p.LineTo(b)
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func iconPlus(gtx layout.Context, c image.Point, r, width float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	strokeLine(gtx, f32.Pt(x-r, y), f32.Pt(x+r, y), width, col)
	strokeLine(gtx, f32.Pt(x, y-r), f32.Pt(x, y+r), width, col)
}

func iconCross(gtx layout.Context, c image.Point, r, width float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	strokeLine(gtx, f32.Pt(x-r, y-r), f32.Pt(x+r, y+r), width, col)
	strokeLine(gtx, f32.Pt(x-r, y+r), f32.Pt(x+r, y-r), width, col)
}

func iconSearch(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X)-r*0.25, float32(c.Y)-r*0.25
	w := float32(math.Max(1.5, float64(r)/3))
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x+r, y))
	p.ArcTo(f32.Pt(x, y), f32.Pt(x, y), 2*math.Pi)
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	d := r * 0.72
	strokeLine(gtx, f32.Pt(x+d, y+d), f32.Pt(x+r*1.7, y+r*1.7), w, col)
}

// iconSliders is the settings icon: three lines with a knob on each.
func iconSliders(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.5, float64(r)/5))
	knob := r * 0.32
	for i, at := range []float32{-0.45, 0.4, -0.1} {
		ly := y + (float32(i)-1)*r*0.75
		strokeLine(gtx, f32.Pt(x-r, ly), f32.Pt(x+r, ly), w, col)
		kx := x + at*r*1.4
		paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: image.Pt(int(kx-knob), int(ly-knob)), Max: image.Pt(int(kx+knob), int(ly+knob))}.Op(gtx.Ops))
	}
}

func iconMore(gtx layout.Context, c image.Point, gap float32, col color.NRGBA) {
	d := int(math.Max(3, float64(gap)*0.55))
	for i := -1; i <= 1; i++ {
		x := c.X + int(float32(i)*gap)
		paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: image.Pt(x-d/2, c.Y-d/2), Max: image.Pt(x-d/2+d, c.Y-d/2+d)}.Op(gtx.Ops))
	}
}

// iconKeyboard is a keyboard outline with keys, for the shortcuts list.
func iconKeyboard(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.3, float64(r)/6))
	hw, hh := r, r*0.65
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x-hw, y-hh))
	p.LineTo(f32.Pt(x+hw, y-hh))
	p.LineTo(f32.Pt(x+hw, y+hh))
	p.LineTo(f32.Pt(x-hw, y+hh))
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	k := int(math.Max(1, float64(r)/5))
	for row, n := range []int{4, 4} {
		ky := y - hh*0.35 + float32(row)*hh*0.6
		for i := 0; i < n; i++ {
			kx := x - hw*0.6 + float32(i)*hw*0.4
			paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(int(kx)-k/2, int(ky)-k/2), Max: image.Pt(int(kx)-k/2+k, int(ky)-k/2+k)}.Op())
		}
	}
	strokeLine(gtx, f32.Pt(x-hw*0.45, y+hh*0.55), f32.Pt(x+hw*0.45, y+hh*0.55), w, col)
}

// iconTerminal is a window with a prompt in it.
func iconTerminal(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.3, float64(r)/6))
	hw, hh := r, r*0.78
	rr := clip.UniformRRect(image.Rect(int(x-hw), int(y-hh), int(x+hw), int(y+hh)), int(r/4))
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: rr.Path(gtx.Ops), Width: w}.Op())
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x-hw*0.5, y-hh*0.3))
	p.LineTo(f32.Pt(x-hw*0.15, y))
	p.LineTo(f32.Pt(x-hw*0.5, y+hh*0.3))
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	strokeLine(gtx, f32.Pt(x+hw*0.05, y+hh*0.32), f32.Pt(x+hw*0.5, y+hh*0.32), w, col)
}

// iconPencil is for renaming.
func iconPencil(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.3, float64(r)/6))
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x-r*0.75, y+r*0.75))
	p.LineTo(f32.Pt(x-r*0.65, y+r*0.3))
	p.LineTo(f32.Pt(x+r*0.45, y-r*0.8))
	p.LineTo(f32.Pt(x+r*0.8, y-r*0.45))
	p.LineTo(f32.Pt(x-r*0.3, y+r*0.65))
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
}

// iconStop is the media stop symbol: a square in a circle, so it does not
// read as the window's maximize button.
func iconStop(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	w := float32(math.Max(1.3, float64(r)/6))
	x, y := float32(c.X), float32(c.Y)
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x+r*0.85, y))
	p.ArcTo(f32.Pt(x, y), f32.Pt(x, y), 2*math.Pi)
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	h := r * 0.3
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(int(x-h), int(y-h), int(x+h+0.5), int(y+h+0.5)), 1).Op(gtx.Ops))
}

// Window controls.

func iconMinimize(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	strokeLine(gtx, f32.Pt(float32(c.X)-r, float32(c.Y)), f32.Pt(float32(c.X)+r, float32(c.Y)), float32(math.Max(1.2, float64(r)/4)), col)
}

func iconMaximize(gtx layout.Context, c image.Point, r float32, maximized bool, col color.NRGBA) {
	w := float32(math.Max(1.2, float64(r)/4))
	box := func(x, y, h float32) {
		rr := clip.UniformRRect(image.Rect(int(x-h), int(y-h), int(x+h), int(y+h)), 1)
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: rr.Path(gtx.Ops), Width: w}.Op())
	}
	x, y := float32(c.X), float32(c.Y)
	if maximized {
		box(x+r*0.25, y-r*0.25, r*0.7)
		box(x-r*0.2, y+r*0.2, r*0.7)
		return
	}
	box(x, y, r*0.85)
}
