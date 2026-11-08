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

// The sidebar lists one machine's sessions at a time, the one whose tab is in
// front, even when the same folder is open on both.
func TestGUISidebarShowsOneMachineAtATime(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mb.SetRemote(&fakeBackend{name: "ssh:build-box"})
	g.remote.Store(true)
	g.sessions = append(g.sessions,
		sessionInfo{Name: "build-box/claude-api", Host: "build-box", CWD: "/home/user/src/api", Command: "claude", Activity: "active"},
		sessionInfo{Name: "build-box/shell-data", Host: "build-box", CWD: "/srv/data", Shell: true, Activity: "quiet"},
	)
	g.sessions = sortedCopy(g.sessions)
	sessionsIn := func(rows []guiRow) (names []string) {
		for _, r := range rows {
			if !r.header && !r.stateHead {
				names = append(names, r.s.Name)
			}
		}
		return
	}
	g.sideHost = ""
	if got := sessionsIn(g.sidebarRows(g.sessions)); len(got) != 5 {
		t.Fatalf("this machine's sessions = %v", got)
	}
	g.sideHost = "build-box"
	if got := sessionsIn(g.sidebarRows(g.sessions)); len(got) != 2 || got[0] != "build-box/claude-api" && got[1] != "build-box/claude-api" {
		t.Fatalf("the host's sessions = %v", got)
	}
	// By folder there are headings for the folders on that machine only.
	g.settings.GUIGroup = "folder"
	headers := 0
	for _, r := range g.sidebarRows(g.sessions) {
		if r.header {
			headers++
		}
	}
	if headers != 2 { // api, data
		t.Fatalf("project headings = %d", headers)
	}
	// A host that is no longer connected falls back to this machine.
	g.mb.SetRemote(nil)
	if g.tabHost() != "" {
		t.Fatalf("tab host = %q after disconnecting", g.tabHost())
	}

	g.mb.SetRemote(&fakeBackend{name: "ssh:build-box"})
	g.sideHost = "build-box"
	img := renderGUI(t, g, 1240, 720, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(dir + "/gui-sections.png")
	png.Encode(f, img)
	f.Close()
}

// By state the sessions are in three groups, unread first, and the newest
// change first within each.
func TestGUISidebarGroupsByState(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.settings.GUIGroup = "state"
	var heads []string
	for _, r := range g.sidebarRows(g.sessions) {
		if r.stateHead {
			heads = append(heads, r.label)
		}
	}
	if len(heads) != 3 || heads[0] != "Unread" || heads[1] != "Working" || heads[2] != "Idle" {
		t.Fatalf("state headings = %v", heads)
	}
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
	// By state: the selected session leads the working ones.
	move(100, 235) // over the selected row: the dots appear
	move(236, 235) // onto the dots
	move(237, 236) // and a little more, as a hand does
	dots := g.clickable("more-claude-lam")
	if !dots.Hovered() {
		t.Fatal("the dots did not get the pointer")
	}
	p := f32.Pt(237, 236)
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
	g.settings.GUIGroup = "folder"
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
	// Over the heading of a project its + appears: find the heading.
	var id string
	var y float32
	for y = 130; y < 420 && id == ""; y += 2 {
		move(100, y)
		for k := range g.buttons {
			if strings.HasPrefix(k, "new-in-") {
				id = k
			}
		}
	}
	if id == "" {
		t.Fatal("the + did not appear on the heading")
	}
	y -= 2
	move(226, y+2) // onto the +
	move(227, y+3) // and a little more
	if !g.clickable(id).Hovered() {
		t.Fatal("the + did not get the pointer")
	}
	p := f32.Pt(227, y+3)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Time: at + time.Millisecond},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: at + 2*time.Millisecond})
	d.frame()
	d.frame()
	if g.dlg.kind != dlgNewSession {
		t.Fatalf("clicking the + opened dialog %v, not a new session", g.dlg.kind)
	}
}

// Hosts connected to before stay as tabs, not connected, until they are
// forgotten.
func TestSidebarKeepsSavedHosts(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.settings.GUIHosts = []string{"fedora", "lab-box"}
	saved := func() (names []string) {
		for _, tab := range g.sideTabs() {
			if !tab.connected {
				names = append(names, tab.label)
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

// Selecting an unread session reads it, and it stays under Unread until
// another is selected, so stepping with the keys does not skip a neighbour.
func TestGUISidebarStepsThroughUnreadWithoutSkipping(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.settings.GUIGroup = "state"
	g.mu.Lock()
	g.sessions = []sessionInfo{
		{Name: "a", CWD: "/x/a", Command: "claude", Unread: true},
		{Name: "b", CWD: "/x/b", Command: "claude", Unread: true},
		{Name: "c", CWD: "/x/c", Command: "claude"},
	}
	g.mu.Unlock()
	g.rowsVer.Add(1)
	order := func() (names []string) {
		for _, r := range g.cachedRows() {
			if !r.header && !r.stateHead {
				names = append(names, r.s.Name)
			}
		}
		return
	}
	g.selectSession("a")
	if got := order(); len(got) != 3 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("order after reading a: %v", got)
	}
	g.stepSession(1)
	if g.selected != "b" {
		t.Fatalf("next from a went to %q, not b", g.selected)
	}
	// Then it goes to the row under b, wherever a settled.
	before := order()
	below := ""
	for i, n := range before {
		if n == "b" {
			below = before[(i+1)%len(before)]
		}
	}
	g.stepSession(1)
	if g.selected != below {
		t.Fatalf("next from b went to %q, the row under it is %q (%v)", g.selected, below, before)
	}
}

// A new session is for the machine whose tab is in front, even when the
// selected session is on another one.
func TestGUINewSessionFollowsTheTabInFront(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mb.SetRemote(&fakeBackend{name: "ssh:build-box"})
	g.remote.Store(true)
	g.mu.Lock()
	g.sessions = append(g.sessions, sessionInfo{Name: "build-box/claude", Host: "build-box", CWD: "/srv/api", Command: "claude"})
	g.mu.Unlock()
	g.selectSession("build-box/claude") // brings the host's tab to the front
	if g.tabHost() != "build-box" {
		t.Fatalf("tab host = %q", g.tabHost())
	}
	g.sideHost = "" // the person clicks this machine's tab
	g.openNewSession()
	if g.dlg.kind != dlgNewSession || g.dlg.wizard.host != "" {
		t.Fatalf("the dialog is for host %q, want this machine", g.dlg.wizard.host)
	}
	g.closeDialog()
	g.sideHost = "build-box"
	g.openNewSession()
	if g.dlg.wizard.host != "build-box" {
		t.Fatalf("the dialog is for host %q, want build-box", g.dlg.wizard.host)
	}
}

// A card taller than the window keeps its title and buttons and scrolls its
// content: the last setting can be reached on a short window.
func TestGUISettingsScrollOnAShortWindow(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.dlg = dialog{kind: dlgSettings}
	renderGUI(t, g, 900, 420, 1)
	if g.dlg.scroll != 0 {
		t.Fatalf("scrolled before anything moved: %d", g.dlg.scroll)
	}
	g.dlg.row, g.dlg.reveal = guiSettingsRows-1, true
	for range 3 {
		renderGUI(t, g, 900, 420, 1)
	}
	if g.dlg.scroll == 0 {
		t.Fatal("the last row was not scrolled into view")
	}
	img := renderGUI(t, g, 900, 420, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(dir + "/gui-settings-short.png")
	png.Encode(f, img)
	f.Close()
}

// A field scrolls to keep its caret in view, and a press puts the caret there.
func TestGUIFieldScrollsAndTakesAClick(t *testing.T) {
	e := testPane(t, 80, 24)
	g := newTestApp(t, e)
	g.openRename(sessionInfo{Name: "x"})
	g.dlg.field.set(strings.Repeat("a long folder name/", 12))
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	if g.dlg.field.scroll == 0 {
		t.Fatal("a long text did not scroll to the caret")
	}
	// Press at the left edge of the field: the caret goes to the start of what shows.
	p := f32.Pt(300, 344)
	d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p, Time: time.Millisecond},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Time: 2 * time.Millisecond},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: 3 * time.Millisecond})
	d.frame()
	d.frame()
	if c := g.dlg.field.cursor; c == len(g.dlg.field.buf) || c == 0 {
		t.Fatalf("caret after the press = %d of %d", c, len(g.dlg.field.buf))
	}
}

// The sidebar's own button hides it, and the header's brings it back.
func TestGUISidebarToggleButtons(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	if _, ok := g.buttons["side-toggle"]; !ok {
		t.Fatal("no hide button in the sidebar")
	}
	g.hideSide = true
	d.frame()
	d.frame()
	if _, ok := g.buttons["side-show"]; !ok {
		t.Fatal("no show button in the header")
	}
	img := renderGUI(t, g, 1240, 720, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(dir + "/gui-side-hidden.png")
	png.Encode(f, img)
	f.Close()
}

// Messages stack, go by themselves, can be clicked away, and may carry an action.
func TestGUIToastsStackAndAct(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	ran := false
	g.say("first")
	g.say("first") // the same message is not stacked twice
	g.sayAction("Retry", func() { ran = true }, "second")
	if len(g.toasts) != 2 {
		t.Fatalf("toasts = %d", len(g.toasts))
	}
	d.frame()
	d.frame()
	g.hits["toast-act-1"] = true
	d.frame()
	if !ran || len(g.toasts) != 1 {
		t.Fatalf("action ran %v, toasts left %d", ran, len(g.toasts))
	}
	g.toasts[0].until = time.Now().Add(-time.Second)
	d.frame()
	if len(g.toasts) != 0 {
		t.Fatal("an expired toast stayed")
	}
}
