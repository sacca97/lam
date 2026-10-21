package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestEmbeddedScrollbackViewportMovesAndReturnsLive(t *testing.T) {
	em, err := newTermEmu(12, 2, termCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	terminal := &embedded{em: em}
	defer terminal.em.Close()

	terminal.em.Write([]byte("one\r\ntwo\r\nthree\r\nfour"))
	live := terminal.renderViewport()
	if !strings.Contains(live, "three") || !strings.Contains(live, "four") {
		t.Fatalf("live viewport = %q, want the latest lines", live)
	}

	if !terminal.scrollbackBy(1) {
		t.Fatal("scrolling up did not move the viewport")
	}
	history := terminal.renderViewport()
	if !strings.Contains(history, "two") || !strings.Contains(history, "three") {
		t.Fatalf("scrolled viewport = %q, want previous and current lines", history)
	}

	if !terminal.scrollbackBy(-100) {
		t.Fatal("scrolling down did not return to the live viewport")
	}
	if got := terminal.renderViewport(); got != live {
		t.Fatalf("viewport after returning live = %q, want %q", got, live)
	}
}

func scrolledTestPane(t *testing.T, scrollback int) *embedded {
	t.Helper()
	em, err := newTermEmu(24, 6, termCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(em.Close)
	for i := 0; i < 60; i++ {
		em.Write([]byte(fmt.Sprintf("\x1b[3%dmrow %02d 日本 end\x1b[0m\r\n", i%7, i)))
	}
	return &embedded{em: em, scrollback: scrollback}
}

func TestScrolledViewportFastPathMatchesCellPath(t *testing.T) {
	p := scrolledTestPane(t, 7)
	fast := strings.Split(p.renderViewportWithSelection(nil), "\n")
	scrollbackLines := p.em.ScrollbackLen()
	start := scrollbackLines - 7
	if len(fast) != 6 {
		t.Fatalf("rows = %d, want 6", len(fast))
	}
	for y := range fast {
		want := strings.TrimRight(ansi.Strip(p.renderRowFromCells(y, start, 24, scrollbackLines, nil)), " ")
		if got := strings.TrimRight(ansi.Strip(fast[y]), " "); got != want {
			t.Errorf("row %d: fast %q, cells %q", y, got, want)
		}
	}
	if !strings.Contains(ansi.Strip(fast[0]), "row ") {
		t.Errorf("scrolled view does not show history: %q", fast[0])
	}
}

func TestSelectionHighlightsOnlySelectedRows(t *testing.T) {
	p := scrolledTestPane(t, 0)
	sel := &terminalTextSelection{pane: p, active: true,
		start: terminalSelectionPoint{X: 2, Y: p.selectionRow(1)}, end: terminalSelectionPoint{X: 5, Y: p.selectionRow(2)}}
	lines := strings.Split(p.renderViewportWithSelection(sel), "\n")
	highlight := "48;2;77;105;122"
	for y, line := range lines {
		if got, want := strings.Contains(line, highlight), y == 1 || y == 2; got != want {
			t.Errorf("row %d highlighted = %v, want %v: %q", y, got, want, line)
		}
	}
	if got := p.selectedText(sel.start, sel.end); !strings.Contains(got, "\n") {
		t.Errorf("selected text = %q, want two rows", got)
	}
}

func TestTypingReturnsToLiveScreen(t *testing.T) {
	p := scrolledTestPane(t, 0)
	if p.snapToLive() {
		t.Fatal("snapToLive reported a change on the live screen")
	}
	if !p.scrollbackBy(5) {
		t.Fatal("could not scroll back")
	}
	if !p.snapToLive() || p.scrollback != 0 {
		t.Fatalf("snapToLive did not return to live (scrollback %d)", p.scrollback)
	}
}
