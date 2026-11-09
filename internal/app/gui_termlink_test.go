//go:build gui

package app

import (
	"gioui.org/f32"
	"gioui.org/io/pointer"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPathTokenAt(t *testing.T) {
	cols := func(s string) []string { return strings.Split(s, "") }
	for _, c := range []struct {
		row  string
		x    int
		want string
	}{
		{"wrote docs/plan.md.", 8, "docs/plan.md"},
		{"see `notes/a.md` now", 8, "notes/a.md"},
		{"see (/tmp/paper.pdf)", 8, "/tmp/paper.pdf"},
		{"at src/x.md:42:7 here", 5, "src/x.md"},
		{"read @README.md please", 7, "README.md"},
		{"in ~/doc/a.PDF", 6, "~/doc/a.PDF"},
		{"main.go is code", 2, ""},
		{"wrote docs/plan.md", 2, ""}, // on the word before it
		{"a  b", 2, ""},
		{"see https://en.wikipedia.org/wiki/Foo_(bar). ok", 20, "https://en.wikipedia.org/wiki/Foo_(bar)"},
		{"(see https://a.io/x)", 10, "https://a.io/x"},
		{"日本/a.md", 4, "日本/a.md"},
		{"open https://example.com/a?b=1. now", 10, "https://example.com/a?b=1"},
		{"open https://example.com/a. now", 30, ""},
		{"mail mailto:me@x.org", 8, "mailto:me@x.org"},
	} {
		if got, _ := linkAt(cols(c.row), c.x); got.tok != c.want {
			t.Errorf("%q at %d = %q, want %q", c.row, c.x, got.tok, c.want)
		}
	}
}

// A path seen in the session's terminal opens next to the session's folder.
func TestOpenTerminalLinkResolvesInTheSessionFolder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# plan"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mu.Lock()
	for i := range g.sessions {
		if g.sessions[i].Name == g.selected {
			g.sessions[i].CWD = dir
		}
	}
	g.mu.Unlock()
	g.rowsVer.Add(1)
	if !g.openTerminalLink("plan.md") {
		t.Fatal("not opened")
	}
	d := &guiDriver{t: t, g: g}
	for i := 0; i < 200 && g.doc == nil; i++ { // the search runs in the background
		d.frame()
		time.Sleep(10 * time.Millisecond)
	}
	waitDoc(t, g)
	if g.doc == nil || g.doc.path != filepath.Join(dir, "plan.md") || g.doc.err != "" {
		t.Fatalf("opened %+v", g.doc)
	}
}

// The underline covers the link without the full stop after it.
func TestLinkAtColumns(t *testing.T) {
	row := strings.Split("go to https://a.io/x. ok", "")
	l, ok := linkAt(row, 8)
	if !ok || l.lo != 6 || l.hi != 19 {
		t.Fatalf("link = %+v %v", l, ok)
	}
	if isWebLink("https://") || !isWebLink("HTTP://a") || isWebLink("ftp://a") {
		t.Fatal("isWebLink")
	}
}

// The agent's pane underlines a link under the pointer, and Ctrl+click opens a path.
func TestAgentPaneHighlightsLinks(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	e.em.Write([]byte("\x1b[2J\x1b[Hsee https://example.com/page and docs/plan.md here\r\n"))
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	d.frame()
	at := time.Duration(0)
	found := false
	for y := float32(50); y < 120 && !found; y += 6 {
		for x := float32(300); x < 700 && !found; x += 6 {
			at += time.Millisecond
			d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: at})
			d.frame()
			found = g.mainView.linkOn
		}
	}
	if !found {
		t.Fatalf("no link was highlighted in the agent pane; rows: %v", len(g.mainView.lines))
	}
}

// A file an agent names without its folder is found below the session's folder.
func TestOpenTerminalLinkFindsFileInSubfolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "paper", "build"), 0o755)
	os.WriteFile(filepath.Join(dir, "paper", "build", "main.md"), []byte("# deep"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mu.Lock()
	for i := range g.sessions {
		if g.sessions[i].Name == g.selected {
			g.sessions[i].CWD = dir
		}
	}
	g.mu.Unlock()
	g.rowsVer.Add(1)
	g.openTerminalLink("main.md")
	d := &guiDriver{t: t, g: g}
	for i := 0; i < 300 && g.doc == nil; i++ {
		d.frame()
		time.Sleep(10 * time.Millisecond)
	}
	if g.doc == nil || g.doc.path != filepath.Join(dir, "paper", "build", "main.md") {
		t.Fatalf("opened %+v", g.doc)
	}
}
