//go:build gui

package app

import (
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"
)

func TestFuzzyScore(t *testing.T) {
	if fuzzyScore("set", "Settings") < 0 || fuzzyScore("stg", "Settings") < 0 || fuzzyScore("xyz", "Settings") >= 0 {
		t.Fatal("matching")
	}
	if fuzzyScore("set", "Settings") >= fuzzyScore("stg", "Settings") {
		t.Fatal("a substring should beat scattered letters")
	}
	if fuzzyScore("", "anything") != 0 {
		t.Fatal("an empty query matches everything")
	}
}

func TestPaletteRunsAnAction(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	g.openPalette()
	d.frame()
	d.frame()
	if len(g.paletteMatches()) < 8 {
		t.Fatalf("palette offers %d items", len(g.paletteMatches()))
	}
	g.dialogText("shortcuts")
	if got := g.paletteMatches(); len(got) == 0 || got[0].label != "Keyboard shortcuts" {
		t.Fatalf("matches for shortcuts = %v", got)
	}
	g.dialogKey(d.gtx(), key.Event{Name: key.NameReturn, State: key.Press})
	if g.dlg.kind != dlgHelp {
		t.Fatalf("dialog = %v after Enter", g.dlg.kind)
	}
}

func TestPaletteGoesToASession(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	g.openPalette()
	d.frame()
	g.dialogText("opencode")
	m := g.paletteMatches()
	if len(m) == 0 || m[0].label != "Go to opencode-notes" {
		t.Fatalf("matches = %v", m)
	}
	g.dialogKey(d.gtx(), key.Event{Name: key.NameReturn, State: key.Press})
	if g.selected != "opencode-notes" {
		t.Fatalf("selected %q", g.selected)
	}
}

func (d *guiDriver) gtx() layout.Context { return layout.Context{} }

// A second session can be shown beside the one in front, and put away.
func TestGUIShowsASecondSessionBeside(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := nativeBackend{}
	for _, n := range []string{"gui-one", "gui-two"} {
		if err := b.Create(n, t.TempDir(), "cat"); err != nil {
			t.Fatal(err)
		}
		defer b.Kill(n)
	}
	g := newGUIState()
	g.settings = defaultSettings()
	g.settings.GUIFontSize = defaultGUIFontSize
	g.applySessions(listSessionsWithError())
	g.selected = "gui-one"
	g.windowFocused = true
	d := &guiDriver{t: t, g: g}
	defer g.shutdown()
	d.frame()
	d.frame()
	found := false
	for _, it := range g.paletteAll() {
		found = found || it.label == "Show gui-two beside"
	}
	if !found {
		t.Fatal("the palette does not offer the other session")
	}
	g.openBeside("gui-two")
	d.frame()
	d.frame()
	if g.besidePane == nil || g.besidePane.name != "gui-two" || g.termView.pane != g.besidePane {
		t.Fatalf("second pane = %+v", g.besidePane)
	}
	if g.main == nil || g.main.name != "gui-one" {
		t.Fatal("the first session left the main pane")
	}
	g.toggleTerminal() // closes the second pane, as it does the terminal
	d.frame()
	if g.beside != "" || g.besidePane != nil || g.termVisible {
		t.Fatal("the second pane stayed")
	}
}
