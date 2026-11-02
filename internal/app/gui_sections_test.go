//go:build gui

package app

import (
	"image/png"
	"os"
	"testing"
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
