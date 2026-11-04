//go:build gui

package app

import (
	"image"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// A button at the bottom edge shows its tooltip above itself: below there is
// no window left to draw it in.
func TestTooltipAtTheBottomEdgeGoesAbove(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	const w, h = 300, 200
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skipf("no headless GPU: %v", err)
	}
	defer win.Release()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Constraints: layout.Exact(image.Pt(w, h)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	button := image.Rect(40, h-38, 70, h-8)
	g.tooltip(gtx, button, "Settings")
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	painted := func(r image.Rectangle) bool {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if img.RGBAAt(x, y).A != 0 {
					return true
				}
			}
		}
		return false
	}
	if !painted(image.Rect(30, button.Min.Y-40, 90, button.Min.Y-4)) {
		t.Fatal("no tooltip above the button")
	}
}
