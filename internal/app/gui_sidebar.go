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
		return "unread", g.th.Unread, age
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

	// No heading: the strip above the tabs drags the window where lam draws its
	// own title bar.
	top := gtx.Dp(10)
	moveArea(gtx, image.Rect(0, 0, size.X, top))

	// The machines, as tabs.
	tabH := gtx.Dp(34)
	g.layoutHostTabs(gtx, image.Rect(0, top, size.X, top+tabH), pad)

	// Search, and the new session button.
	fh := gtx.Dp(34)
	fy := top + tabH + gtx.Dp(8)
	nb := image.Rect(size.X-pad-fh, fy, size.X-pad, fy+fh)
	g.layoutSearch(gtx, image.Rect(pad, fy, nb.Min.X-gtx.Dp(6), fy+fh))
	if g.ghostButton(gtx, "new", nb, "New session", true, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconPlus(gtx, c, float32(gtx.Dp(6)), float32(max(1, gtx.Dp(1.6))), col)
	}) {
		g.openNewSessionOn(g.tabHost())
	}

	// How the list is grouped.
	gy := fy + fh + gtx.Dp(8)
	gh := gtx.Dp(24)
	g.layoutGroupSwitch(gtx, image.Rect(pad, gy, size.X-pad, gy+gh))

	// The list.
	footer := gtx.Dp(40)
	listArea := image.Rect(0, gy+gh+gtx.Dp(6), size.X, size.Y-footer)
	rows := g.cachedRows()
	now := time.Now()
	func() {
		defer op.Offset(listArea.Min).Push(gtx.Ops).Pop()
		lg := gtx
		lg.Constraints = layout.Exact(listArea.Size())
		defer clip.Rect{Max: listArea.Size()}.Push(gtx.Ops).Pop()
		if len(rows) == 0 {
			g.layoutEmptyList(lg, listErr, pad)
			return
		}
		if g.revealSel {
			g.revealSel = false
			g.mu.Lock()
			sel := g.selected
			g.mu.Unlock()
			if i := slices.IndexFunc(rows, func(r guiRow) bool { return !r.header && !r.stateHead && r.s.Name == sel }); i >= 0 {
				if p := g.list.Position; i < p.First {
					g.list.ScrollTo(i)
				} else if i >= p.First+p.Count-1 && p.Count > 1 {
					g.list.ScrollTo(i - p.Count + 2)
				}
			}
		}
		animate := false
		g.nextAge = time.Time{}
		g.list.Layout(lg, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			if !rows[i].header && !rows[i].stateHead && rows[i].s.Activity == "active" {
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

	// Footer: how many sessions are at work on every machine, settings and shortcuts.
	paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(0, size.Y-footer), Max: image.Pt(size.X, size.Y-footer+max(1, gtx.Dp(1)))}.Op())
	b := gtx.Dp(30)
	fcy := size.Y - footer/2
	hr := image.Rect(size.X-pad-b, fcy-b/2, size.X-pad, fcy-b/2+b)
	sr := hr.Sub(image.Pt(b+gtx.Dp(2), 0))
	if g.ghostButton(gtx, "settings", sr, "Settings", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconSliders(gtx, c, float32(gtx.Dp(7)), col)
	}) {
		g.dlg = dialog{kind: dlgSettings}
	}
	if g.ghostButton(gtx, "help", hr, "Keyboard shortcuts", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconKeyboard(gtx, c, float32(gtx.Dp(8)), col)
	}) {
		g.dlg = dialog{kind: dlgHelp}
	}
	tr := sr.Sub(image.Pt(b+gtx.Dp(2), 0))
	if g.ghostButton(gtx, "side-toggle", tr, "Hide sidebar  "+g.sideToggleKey(), false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconSidebar(gtx, c, float32(gtx.Dp(8)), col)
	}) {
		g.hideSide = true
	}
	active, idle := 0, 0
	for _, s := range g.allSessions() {
		switch {
		case s.Shell:
		case s.Activity == "active":
			active++
		default:
			idle++
		}
	}
	x := pad + gtx.Dp(4)
	dot := image.Pt(x+gtx.Dp(3), fcy)
	paint.FillShape(gtx.Ops, g.th.Accent, clip.Ellipse{Min: dot.Sub(image.Pt(gtx.Dp(3), gtx.Dp(3))), Max: dot.Add(image.Pt(gtx.Dp(3), gtx.Dp(3)))}.Op(gtx.Ops))
	x += gtx.Dp(12)
	x += g.drawTextMid(gtx, image.Pt(x, fcy), g.th.TinySize, font.Normal, g.th.TextDim, fmt.Sprintf("%d active", active), 1<<20)
	g.drawTextMid(gtx, image.Pt(x, fcy), g.th.TinySize, font.Normal, g.th.TextFaint, fmt.Sprintf("  ·  %d idle", idle), 1<<20)
	// A newer release: a quiet button before the icons.
	if tag, ok := g.upd.available(); ok {
		chip := image.Pt(tr.Min.X-gtx.Dp(8), fcy)
		g.buttonStyled(gtx, "update-chip", "↑ "+tag, chip, true, false, false)
		if g.clicked(gtx, "update-chip") {
			g.openUpdate()
		}
	}
}

// sideToggleKey is the shortcut that shows or hides the sidebar, for tooltips.
func (g *guiApp) sideToggleKey() string {
	if guiMac {
		return "⌘B"
	}
	return "Ctrl+Shift+B"
}

// sideTab is one machine in the tab strip.
type sideTab struct {
	host, label string
	connected   bool // this machine, or the host connected now
	unread      int
}

// sideTabs are this machine, the host connected now, and the hosts connected
// to before.
func (g *guiApp) sideTabs() []sideTab {
	_, remote := g.mb.Remote()
	unread := map[string]int{}
	for _, s := range g.allSessions() {
		if s.Unread {
			unread[s.Host]++
		}
	}
	tabs := []sideTab{{label: g.hostLabel(), connected: true, unread: unread[""]}}
	if remote != "" {
		tabs = append(tabs, sideTab{host: remote, label: remote, connected: true, unread: unread[remote]})
	}
	for _, h := range g.settings.GUIHosts {
		if h != remote {
			tabs = append(tabs, sideTab{host: h, label: h})
		}
	}
	return tabs
}

// layoutHostTabs draws the machines as tabs: click one to list its sessions,
// a host not connected connects. The × disconnects, or forgets a host not
// connected; the + connects to another.
func (g *guiApp) layoutHostTabs(gtx layout.Context, r image.Rectangle, pad int) {
	front := g.tabHost()
	x := pad
	room := r.Max.X - pad - gtx.Dp(28)
	circle := func(c image.Point, rad int, col color.NRGBA) {
		paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: c.Sub(image.Pt(rad, rad)), Max: c.Add(image.Pt(rad, rad))}.Op(gtx.Ops))
	}
	// Tabs take the width their names need; one that does not fit shrinks a
	// little, and those after it are left out (the + still connects anywhere).
	// The connected machines come first, so they are the ones that stay.
	var tabs []sideTab
	var labelW []int
	budget, used := room-pad, 0
	for _, t := range g.sideTabs() {
		chrome := gtx.Dp(20) + gtx.Dp(8) + gtx.Dp(1)
		if t.unread > 0 {
			chrome += gtx.Dp(22)
		}
		if t.host != "" {
			chrome += gtx.Dp(16)
		}
		_, nat, _ := g.shape(gtx, g.th.SmallSize, font.Medium, t.label, gtx.Dp(96))
		if left := budget - used - chrome; nat > left {
			if left < gtx.Dp(48) {
				break
			}
			nat = left
		}
		tabs, labelW = append(tabs, t), append(labelW, nat)
		used += chrome + nat
	}
	for i, t := range tabs {
		gl, tw, _ := g.shape(gtx, g.th.SmallSize, font.Medium, t.label, labelW[i]+1)
		w := gtx.Dp(20) + tw + gtx.Dp(8)
		if t.unread > 0 {
			w += gtx.Dp(22)
		}
		if t.host != "" {
			w += gtx.Dp(16)
		}
		tr := image.Rect(x, r.Min.Y, x+w, r.Max.Y)
		x += w + gtx.Dp(1)
		id := "tab-" + t.host
		c := g.clickable(id)
		if c.Clicked(gtx) {
			if t.connected {
				g.sideHost = t.host
			} else if g.dlg.kind == dlgNone {
				g.openConnect()
				g.dlg.field.set(t.host)
				g.startConnect(t.host)
			}
		}
		active := t.connected && t.host == front
		func() {
			st := op.Offset(tr.Min).Push(gtx.Ops)
			defer st.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(tr.Size())
			c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				rr := clip.UniformRRect(image.Rectangle{Max: tr.Size()}, gtx.Dp(8))
				switch {
				case active:
					paint.FillShape(gtx.Ops, g.th.Surface, rr.Op(gtx.Ops))
					paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
				case c.Hovered():
					paint.FillShape(gtx.Ops, g.th.Hover, rr.Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: tr.Size()}
			})
		}()
		cy := (tr.Min.Y + tr.Max.Y) / 2
		dotCol := g.th.TextFaint
		if t.connected {
			dotCol = g.th.Accent
		}
		circle(image.Pt(tr.Min.X+gtx.Dp(9), cy), gtx.Dp(3), dotCol)
		col := g.th.TextFaint
		switch {
		case active:
			col = g.th.Text
		case t.connected:
			col = g.th.TextDim
		}
		if len(gl) > 0 {
			asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
			g.paintGlyphs(gtx, gl, image.Pt(tr.Min.X+gtx.Dp(18), cy+(asc-desc)/2), col)
		}
		bx := tr.Min.X + gtx.Dp(18) + tw + gtx.Dp(5)
		if t.unread > 0 {
			br := image.Rect(bx, cy-gtx.Dp(8), bx+gtx.Dp(17), cy+gtx.Dp(8))
			paint.FillShape(gtx.Ops, g.th.Unread, clip.UniformRRect(br, gtx.Dp(8)).Op(gtx.Ops))
			g.drawTextCentered(gtx, br, g.th.TinySize, font.Bold, color.NRGBA{R: 0x1c, G: 0x0f, B: 0x0b, A: 0xff}, fmt.Sprint(t.unread))
		}
		if !t.connected {
			if c.Hovered() {
				g.tooltip(gtx, tr, "Connect to "+t.label)
			}
		}
		if t.host != "" {
			xr := image.Rect(tr.Max.X-gtx.Dp(17), cy-gtx.Dp(9), tr.Max.X-gtx.Dp(1), cy+gtx.Dp(9))
			tip := "Disconnect from " + t.label + " (its sessions keep running)"
			if !t.connected {
				tip = "Forget " + t.label
			}
			if g.ghostButton(gtx, "tabx-"+t.host, xr, tip, false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
				iconCross(gtx, c, float32(gtx.Dp(3)), float32(max(1, gtx.Dp(1.2))), col)
			}) {
				if t.connected {
					g.connID++
					g.switchBackend(nil)
				} else {
					g.forgetHost(t.host)
				}
			}
		}
	}
	plus := image.Rect(r.Max.X-pad-gtx.Dp(28), r.Min.Y+gtx.Dp(3), r.Max.X-pad, r.Max.Y-gtx.Dp(3))
	if g.ghostButton(gtx, "tab-add", plus, "Connect to a host  "+map[bool]string{true: "⌘O", false: "Ctrl+Shift+O"}[guiMac], false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconPlus(gtx, c, float32(gtx.Dp(5)), float32(max(1, gtx.Dp(1.4))), col)
	}) {
		g.openConnect()
	}
}

// layoutGroupSwitch is the choice between listing the sessions by state and by
// folder.
func (g *guiApp) layoutGroupSwitch(gtx layout.Context, r image.Rectangle) {
	cy := (r.Min.Y + r.Max.Y) / 2
	x := r.Min.X + gtx.Dp(2)
	x += g.drawTextMid(gtx, image.Pt(x, cy), g.th.TinySize, font.Normal, g.th.TextFaint, "Group by", r.Dx()) + gtx.Dp(8)
	type seg struct{ id, label, value string }
	segs := []seg{{"grp-state", "State", "state"}, {"grp-folder", "Folder", "folder"}}
	current := g.settings.GUIGroup
	if current == "" {
		current = "state"
	}
	widths := make([]int, len(segs))
	total := 0
	for i, sg := range segs {
		_, tw, _ := g.shape(gtx, g.th.TinySize, font.Medium, sg.label, 1<<20)
		widths[i] = tw + gtx.Dp(18)
		total += widths[i]
	}
	box := image.Rect(x, r.Min.Y+gtx.Dp(1), x+total, r.Max.Y-gtx.Dp(1))
	rr := clip.UniformRRect(box, gtx.Dp(7))
	sx := box.Min.X
	for i, sg := range segs {
		sr := image.Rect(sx, box.Min.Y, sx+widths[i], box.Max.Y)
		sx += widths[i]
		c := g.clickable(sg.id)
		if c.Clicked(gtx) && current != sg.value {
			g.settings.GUIGroup = sg.value
			saveSettings(g.settings)
			g.rowsVer.Add(1)
			current = sg.value
		}
		on := current == sg.value
		func() {
			st := op.Offset(sr.Min).Push(gtx.Ops)
			defer st.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(sr.Size())
			c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				switch {
				case on:
					paint.FillShape(gtx.Ops, g.th.Selected, clip.UniformRRect(image.Rectangle{Max: sr.Size()}, gtx.Dp(6)).Op(gtx.Ops))
				case c.Hovered():
					paint.FillShape(gtx.Ops, g.th.Hover, clip.UniformRRect(image.Rectangle{Max: sr.Size()}, gtx.Dp(6)).Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: sr.Size()}
			})
		}()
		col := g.th.TextFaint
		if on {
			col = g.th.Text
		}
		g.drawTextCentered(gtx, sr, g.th.TinySize, font.Medium, col, sg.label)
	}
	paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
}

// layoutStateHead is the heading over the sessions in one state.
func (g *guiApp) layoutStateHead(gtx layout.Context, r guiRow, pad int) layout.Dimensions {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(8)
	if r.first {
		gap = 0
	}
	h := gtx.Dp(26)
	cy := gap + h/2 + gtx.Dp(2)
	col := g.th.TextFaint
	switch r.state {
	case stateUnread:
		col = g.th.Unread
	case stateWorking:
		col = g.th.Accent
	}
	x := pad + gtx.Dp(6)
	dot := image.Pt(x+gtx.Dp(3), cy)
	paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: dot.Sub(image.Pt(gtx.Dp(3), gtx.Dp(3))), Max: dot.Add(image.Pt(gtx.Dp(3), gtx.Dp(3)))}.Op(gtx.Ops))
	x += gtx.Dp(14)
	x += g.drawTextMid(gtx, image.Pt(x, cy), g.th.TinySize, font.Bold, col, strings.ToUpper(r.label), w) + gtx.Dp(8)
	count := fmt.Sprint(r.count)
	_, cw, _ := g.shape(gtx, g.th.TinySize, font.Normal, count, 1<<20)
	right := w - pad - gtx.Dp(6)
	g.drawTextRightMid(gtx, image.Pt(right, cy), g.th.TinySize, g.th.TextFaint, count)
	if end := right - cw - gtx.Dp(8); end > x {
		paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(x, cy), Max: image.Pt(end, cy+max(1, gtx.Dp(1)))}.Op())
	}
	return layout.Dimensions{Size: image.Pt(w, gap+h)}
}

// agentBadge draws the small square that says which program a session runs.
func (g *guiApp) agentBadge(gtx layout.Context, r image.Rectangle, s sessionInfo) {
	letter, tint := "?", g.th.TextDim
	switch providerName(s) {
	case "Claude Code":
		letter, tint = "C", color.NRGBA{R: 0xd9, G: 0x77, B: 0x57, A: 0xff}
	case "Codex":
		letter, tint = "X", color.NRGBA{R: 0x7a, G: 0xa2, B: 0xf7, A: 0xff}
	case "opencode":
		letter, tint = "O", color.NRGBA{R: 0x7d, G: 0xd3, B: 0xc0, A: 0xff}
	case "pi":
		letter, tint = "π", color.NRGBA{R: 0xc3, G: 0xa6, B: 0xff, A: 0xff}
	case "Oh My Pi":
		letter, tint = "Ω", color.NRGBA{R: 0xe0, G: 0xa3, B: 0xff, A: 0xff}
	case "Antigravity":
		letter, tint = "A", color.NRGBA{R: 0xf2, G: 0xc4, B: 0x6d, A: 0xff}
	case "Gemini":
		letter, tint = "G", color.NRGBA{R: 0x8a, G: 0xb4, B: 0xf8, A: 0xff}
	case "Shell":
		letter = "$"
	default:
		if name := providerName(s); name != "" {
			letter = strings.ToUpper(string([]rune(name)[:1]))
		}
	}
	bg := tint
	bg.A = 0x2a
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(6)).Op(gtx.Ops))
	g.drawTextCentered(gtx, r, g.th.TinySize, font.Bold, tint, letter)
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
	if g.remote.Load() {
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
	g.filterEd.sync(g.filter)
	if g.fieldText(gtx, &g.filterEd, image.Rect(tx, 0, r.Dx()-gtx.Dp(28), h), h/2, g.th.TinySize, g.th.Text, "Search", focused) {
		g.focus = focusFilter
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
	if r.stateHead {
		return g.layoutStateHead(gtx, r, pad)
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
		case s.Unread: // waiting for you: a faint wash of the unread colour
			wash := g.th.Unread
			wash.A = 22
			paint.FillShape(gtx.Ops, wash, rr.Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(w, itemH)}
	})

	// One line: who it is, its name and where it is, then how long and its dot.
	cy := itemH / 2
	badge := image.Rect(inner.Min.X+gtx.Dp(8), cy-gtx.Dp(10), inner.Min.X+gtx.Dp(28), cy+gtx.Dp(10))
	g.agentBadge(gtx, badge, s)
	x := badge.Max.X + gtx.Dp(8)
	right := inner.Max.X - gtx.Dp(10)
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
	} else {
		g.statusDot(gtx, image.Pt(right-gtx.Dp(3), cy), s, now)
		right -= gtx.Dp(16)
		if _, _, age := g.statusOf(s, now); age != "" {
			_, aw, _ := g.shape(gtx, g.th.TinySize, font.Normal, age, 1<<20)
			g.drawTextRightMid(gtx, image.Pt(right, cy), g.th.TinySize, g.th.TextFaint, age)
			right -= aw + gtx.Dp(8)
		}
	}

	byFolder := g.settings.GUIGroup == "folder"
	label := shortName(s.Name)
	if byFolder && !g.collapsed[groupKey(s)] {
		label = sessionLabel(s)
	}
	nameCol, weight := g.th.TextDim, font.Medium
	if selected || s.Unread || s.Activity == "active" {
		nameCol = g.th.Text
	}
	nameW := g.drawTextMid(gtx, image.Pt(x, cy), g.th.UISize, weight, nameCol, label, right-x)
	// Under a state heading the folder tells the sessions apart; under a folder
	// heading it is the program.
	rest := right - x - nameW - gtx.Dp(8)
	note := providerName(s)
	if !byFolder {
		note = g.fitPath(gtx, s.CWD, s.Host, rest)
	}
	if rest > gtx.Dp(28) {
		g.drawTextMid(gtx, image.Pt(x+nameW+gtx.Dp(8), cy), g.th.TinySize, font.Normal, g.th.TextFaint, note, rest)
	}

	pt := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	pt.Pop()
	if hovered && !menuOpen {
		// What the row cannot show on one line: its program, state and folder.
		state := stateNames[sidebarState(s)]
		head := providerName(s) + " · " + state + " · "
		_, hw, _ := g.shape(gtx, g.th.TinySize, font.Medium, head, 1<<20)
		g.tooltip(gtx, image.Rect(inner.Min.X, inner.Min.Y, inner.Max.X, inner.Max.Y), head+g.fitPath(gtx, s.CWD, s.Host, w-hw-gtx.Dp(40)))
	}
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

// forgetHost drops a saved host from the sidebar.
func (g *guiApp) forgetHost(host string) {
	g.settings.GUIHosts = slices.DeleteFunc(slices.Clone(g.settings.GUIHosts), func(h string) bool { return h == host })
	saveSettings(g.settings)
}

// layoutGroupHeader draws a project heading: a click folds it, + starts a
// session in that folder.
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
	label := r.label
	if r.cwd != "" {
		label = g.fitPath(gtx, r.cwd, r.host, right-x-gtx.Dp(6))
	}
	g.drawTextMid(gtx, image.Pt(x, cy), g.th.TinySize, font.SemiBold, col, label, right-x-gtx.Dp(6))
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

// iconDocument is a page with a folded corner and two lines of text.
func iconDocument(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.3, float64(r)/6))
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x-r*0.6, y-r*0.9))
	p.LineTo(f32.Pt(x+r*0.15, y-r*0.9))
	p.LineTo(f32.Pt(x+r*0.6, y-r*0.45))
	p.LineTo(f32.Pt(x+r*0.6, y+r*0.9))
	p.LineTo(f32.Pt(x-r*0.6, y+r*0.9))
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	for _, dy := range []float32{0.1, 0.5} {
		var l clip.Path
		l.Begin(gtx.Ops)
		l.MoveTo(f32.Pt(x-r*0.3, y+r*dy))
		l.LineTo(f32.Pt(x+r*0.3, y+r*dy))
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: l.End(), Width: w}.Op())
	}
}

// iconSidebar is a window with its left strip marked off.
func iconSidebar(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.3, float64(r)/6))
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: clip.UniformRRect(image.Rect(c.X-int(r), c.Y-int(r*0.8), c.X+int(r), c.Y+int(r*0.8)), int(r*0.25)).Path(gtx.Ops), Width: w}.Op())
	var l clip.Path
	l.Begin(gtx.Ops)
	l.MoveTo(f32.Pt(x-r*0.3, y-r*0.8))
	l.LineTo(f32.Pt(x-r*0.3, y+r*0.8))
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: l.End(), Width: w}.Op())
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

// fitPath is a folder as it is on its machine, from the home folder when it
// is under it (~/Projects/lam). When that is wider than room it loses its
// leading parts (…/Projects/lam), keeping the end of the path: the last
// part alone if even the last two do not fit.
func (g *guiApp) fitPath(gtx layout.Context, dir, host string, room int) string {
	if dir == "" {
		return ""
	}
	full := shortenHomePath(dir, g.sourceFor(host))
	parts := strings.Split(full, "/")
	for drop := 0; ; drop++ {
		text := full
		if drop > 0 {
			text = "…/" + strings.Join(parts[drop:], "/")
		}
		_, w, _ := g.shape(gtx, g.th.TinySize, font.Normal, text, 1<<20)
		if w <= room || len(parts)-drop <= 1 {
			return text
		}
	}
}
