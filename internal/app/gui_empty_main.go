//go:build gui

package app

import (
	"image"

	"gioui.org/font"
	"gioui.org/layout"
)

// layoutEmptyMain is the main pane with no session: what lam is for, the
// things to start with as buttons, and the keys that matter most.
func (g *guiApp) layoutEmptyMain(gtx layout.Context, r image.Rectangle) {
	cx := (r.Min.X + r.Max.X) / 2
	y := r.Min.Y + max(gtx.Dp(24), (r.Dy()-gtx.Dp(330))/3)
	centre := func(weight font.Weight, text string, dim bool, lineH int) {
		col := g.th.Text
		if dim {
			col = g.th.TextFaint
		}
		_, w, _ := g.shape(gtx, g.th.UISize, weight, text, 1<<20)
		g.drawText(gtx, image.Pt(cx-min(w, r.Dx()-gtx.Dp(32))/2, y), g.th.UISize, weight, col, text, r.Dx()-gtx.Dp(32))
		y += lineH
	}
	centre(font.SemiBold, "No sessions yet", false, gtx.Dp(30))
	centre(font.Normal, "Start an agent or a shell, or connect to another machine.", true, gtx.Dp(44))

	type starter struct{ id, label string }
	starters := []starter{{"main-new", "New session"}, {"main-doc", "Open a document"}, {"main-connect", "Connect to a host"}}
	total, gap := 0, gtx.Dp(10)
	widths := make([]int, len(starters))
	for i, s := range starters {
		_, w, _ := g.shape(gtx, g.th.SmallSize, font.Medium, s.label, 1<<20)
		widths[i] = w + gtx.Dp(28)
		total += widths[i] + gap
	}
	x := cx - (total-gap)/2
	for i, s := range starters {
		g.buttonStyled(gtx, s.id, s.label, image.Pt(x, y+gtx.Dp(16)), false, false, i == 0)
		x += widths[i] + gap
	}
	y += gtx.Dp(64)
	if g.clicked(gtx, "main-new") {
		g.openNewSession()
	}
	if g.clicked(gtx, "main-doc") {
		g.openDocumentDialog()
	}
	if g.clicked(gtx, "main-connect") {
		g.openConnect()
	}

	mod := "Ctrl+Shift+"
	if guiMac {
		mod = "⌘"
	}
	col := cx - gtx.Dp(150)
	for _, h := range [][2]string{{mod + "N", "New session"}, {mod + "K", "Command palette"}, {mod + "D", "Open a document"}, {mod + "O", "Connect to a host"}, {"F1", "All shortcuts"}} {
		g.drawText(gtx, image.Pt(col, y), g.th.SmallSize, font.Medium, g.th.TextDim, h[0], gtx.Dp(130))
		g.drawText(gtx, image.Pt(col+gtx.Dp(140), y), g.th.SmallSize, font.Normal, g.th.TextFaint, h[1], gtx.Dp(200))
		y += gtx.Dp(24)
	}
}
