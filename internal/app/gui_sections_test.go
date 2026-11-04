//go:build gui

package app

import (
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
)

// The window lists this machine's sessions and the connected host's under
// their own headings, even when the same folder is open on both.
func TestGUISidebarHasLocalAndRemoteSections(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mb.SetRemote(&fakeBackend{name: "ssh:build-box"})
	g.remote = true
	g.sessions = append(g.sessions,
		sessionInfo{Name: "build-box/claude-api", Host: "build-box", CWD: "/home/user/src/api", Command: "claude", Activity: "active"},
		sessionInfo{Name: "build-box/shell-data", Host: "build-box", CWD: "/srv/data", Shell: true, Activity: "quiet"},
	)
	g.sessions = sortedCopy(g.sessions)

	var sections []string
	headers := 0
	for _, r := range g.sidebarRows(g.sessions) {
		switch {
		case r.section:
			sections = append(sections, r.host)
		case r.header:
			headers++
		}
	}
	if len(sections) != 2 || sections[0] != "" || sections[1] != "build-box" {
		t.Fatalf("sections = %q", sections)
	}
	if headers != 5 { // api, lam, notes here; api, data there
		t.Fatalf("project headings = %d", headers)
	}

	img := renderGUI(t, g, 1240, 720, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(dir + "/gui-sections.png")
	png.Encode(f, img)
	f.Close()
}

func sortedCopy(list []sessionInfo) []sessionInfo {
	out := append([]sessionInfo(nil), list...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && sessionLess(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// The three dots on a session row show while the pointer is over the row and
// must stay when the pointer moves onto them, where they take the hover from the
// row: a click on them opens the session's menu.
func TestGUIRowMenuButtonStaysUnderThePointer(t *testing.T) {
	e := testPane(t, 80, 24)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	at := time.Duration(0)
	// A pointer arriving: the first move places it, the second enters what is there.
	move := func(x, y float32) {
		for i := 0; i < 2; i++ {
			at += time.Millisecond
			d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: at})
			d.frame()
		}
		d.frame()
	}
	move(100, 256) // over the selected row: the dots appear
	move(236, 256) // onto the dots
	move(237, 257) // and a little more, as a hand does
	dots := g.clickable("more-claude-lam")
	if !dots.Hovered() {
		t.Fatal("the dots did not get the pointer")
	}
	p := f32.Pt(237, 257)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Time: at + time.Millisecond},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: at + 2*time.Millisecond})
	d.frame()
	d.frame()
	if g.menu == nil {
		t.Fatal("clicking the three dots did not open the menu")
	}
}

// The + on a project heading shows while the pointer is over the heading and
// must stay when the pointer moves onto it: a click on it asks for a new
// session in that folder instead of folding the project.
func TestGUIProjectPlusStaysUnderThePointer(t *testing.T) {
	e := testPane(t, 80, 24)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	at := time.Duration(0)
	move := func(x, y float32) {
		for i := 0; i < 2; i++ {
			at += time.Millisecond
			d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: at})
			d.frame()
		}
		d.frame()
	}
	move(100, 223) // over the heading of the second project: its + appears
	var id string
	for k := range g.buttons {
		if strings.HasPrefix(k, "new-in-") {
			id = k
		}
	}
	if id == "" {
		t.Fatal("the + did not appear on the heading")
	}
	move(226, 225) // onto the +
	move(227, 226) // and a little more
	if !g.clickable(id).Hovered() {
		t.Fatal("the + did not get the pointer")
	}
	p := f32.Pt(227, 226)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Time: at + time.Millisecond},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: at + 2*time.Millisecond})
	d.frame()
	d.frame()
	if g.dlg.kind != dlgNewSession {
		t.Fatalf("clicking the + opened dialog %v, not a new session", g.dlg.kind)
	}
}

// Hosts connected to before stay in the sidebar, not connected, with a way
// back, until they are forgotten.
func TestSidebarKeepsSavedHosts(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.settings.GUIHosts = []string{"fedora", "lab-box"}
	saved := func() (names []string) {
		for _, r := range g.sidebarRows(filteredSessions(g.allSessions(), "")) {
			if r.section && r.saved {
				names = append(names, r.label)
			}
		}
		return
	}
	if got := saved(); len(got) != 2 || got[0] != "fedora" || got[1] != "lab-box" {
		t.Fatalf("saved hosts shown: %v", got)
	}
	g.forgetHost("fedora")
	if got := saved(); len(got) != 1 || got[0] != "lab-box" {
		t.Fatalf("after forgetting: %v", got)
	}
	if _, err := g.launchersOn("lab-box"); err == nil {
		t.Fatal("agents of a host that is not connected were listed")
	}
}
