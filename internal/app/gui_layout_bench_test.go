//go:build gui

package app

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

func benchSidebarApp(n int) *guiApp {
	g := newGUIState()
	g.settings = defaultSettings()
	g.settings.GUIFontSize = defaultGUIFontSize
	var list []sessionInfo
	for i := 0; i < n; i++ {
		s := sessionInfo{
			Name:     fmt.Sprintf("agent-%03d", i),
			CWD:      fmt.Sprintf("/home/u/proj%d", i%12),
			Identity: "claude",
			Command:  "claude",
			Created:  time.Now().Add(-time.Duration(i) * time.Minute),
		}
		switch i % 4 {
		case 0:
			s.Activity = "active"
		case 1:
			s.Unread = true
		}
		list = append(list, s)
	}
	g.applySessions(list, nil)
	g.selected = "agent-005"
	return g
}

func BenchmarkLayoutSidebar200(b *testing.B) {
	g := benchSidebarApp(200)
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Constraints: layout.Exact(image.Pt(300, 800)),
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	g.layoutSidebar(gtx)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ops.Reset()
		g.layoutSidebar(gtx)
	}
}

func BenchmarkSidebarRows200(b *testing.B) {
	g := benchSidebarApp(200)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.sidebarRows(g.visible())
	}
}
