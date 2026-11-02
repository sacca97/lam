//go:build gui

package app

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

type benchTerm struct {
	e    *embedded
	g    *guiApp
	ops  op.Ops
	gtx  layout.Context
	cols int
	rows int
}

func newBenchTerm(b *testing.B, cols, rows int) *benchTerm {
	e := testPane(b, cols, rows)
	g := newTestApp(b, e)
	g.mainView.setFontPx(14)
	bt := &benchTerm{e: e, g: g, cols: cols, rows: rows}
	bt.gtx = layout.Context{Ops: &bt.ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	cw, ch := g.mainView.font.cellW, g.mainView.font.cellH
	bt.gtx.Constraints = layout.Exact(image.Pt(cols*cw, rows*ch))
	return bt
}

func (bt *benchTerm) frame() {
	bt.ops.Reset()
	bt.g.mainView.Layout(bt.gtx, true)
}

// fill writes a screen of styled text: colors, bold, italic, underline,
// a wide character and a background run per row.
func (bt *benchTerm) fill(seed int) {
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	for y := 0; y < bt.rows; y++ {
		fmt.Fprintf(&sb, "\x1b[%dm", 31+(y+seed)%6)
		for x := 0; x < bt.cols/10; x++ {
			switch x % 5 {
			case 1:
				sb.WriteString("\x1b[1m")
			case 2:
				sb.WriteString("\x1b[3;38;5;108m")
			case 3:
				sb.WriteString("\x1b[4;48;5;236m")
			case 4:
				sb.WriteString("\x1b[0;7m")
			}
			fmt.Fprintf(&sb, "%09d ", (seed*7919+y*31+x)%1000000007)
		}
		sb.WriteString("\x1b[0m\r\n")
	}
	bt.e.em.Write([]byte(sb.String()))
}

func BenchmarkTermFullRedraw(b *testing.B) {
	bt := newBenchTerm(b, 200, 60)
	bt.fill(0)
	bt.frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.fill(i + 1)
		bt.frame()
	}
}

func BenchmarkTermOneRow(b *testing.B) {
	bt := newBenchTerm(b, 200, 60)
	bt.fill(0)
	bt.frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.e.em.Write([]byte(fmt.Sprintf("\x1b[10;1Hline %d", i)))
		bt.frame()
	}
}

func BenchmarkTermIdle(b *testing.B) {
	bt := newBenchTerm(b, 200, 60)
	bt.fill(0)
	bt.frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.frame()
	}
}

// BenchmarkTermScroll scrolls the screen a line at a time, as a program
// printing output does.
func BenchmarkTermScroll(b *testing.B) {
	bt := newBenchTerm(b, 200, 60)
	bt.fill(0)
	bt.frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.e.em.Write([]byte(fmt.Sprintf("\x1b[%dmscrolling line %09d with some styled text\x1b[0m\r\n", 31+i%6, i)))
		bt.frame()
	}
}

// BenchmarkTermScrollback scrolls back and forth through history, so every
// row changes each frame.
func BenchmarkTermScrollback(b *testing.B) {
	bt := newBenchTerm(b, 200, 60)
	for i := 0; i < 200; i++ {
		bt.fill(i)
	}
	bt.frame()
	v := bt.g.mainView
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.scrollLines(1 - 2*(i&1))
		bt.frame()
	}
}
