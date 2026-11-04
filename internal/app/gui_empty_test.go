//go:build gui

package app

import (
	"os"
	"testing"
	"time"
)

// Stopping the shell shown when there are no sessions is quick, leaves the
// window open with nothing in it, and quitting leaves no host behind.
func TestGUIStoppedEmptyShellLeavesWindowEmpty(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lam-empty")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("LAM_SOCKET_DIR", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	g := newGUIState()
	g.settings = defaultSettings()
	g.listed = true
	g.launchDir = dir
	p := g.mainPane(100, 30)
	if p == nil {
		t.Fatal("no shell was started for an empty window")
	}
	g.refreshSessions()
	start := time.Now()
	if err := killSession(p.name); err != nil {
		t.Fatal(err)
	}
	for time.Since(start) < 5*time.Second {
		if l, _ := backend().List(); len(l) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("stopping a shell took %v", took)
	}
	g.refreshSessions()
	if g.mainPane(100, 30) != nil || g.emptyShell != nil {
		t.Fatal("a stopped shell was kept or replaced")
	}
	g.shutdown()
	time.Sleep(300 * time.Millisecond)
	if l, _ := backend().List(); len(l) != 0 {
		t.Fatalf("sessions left after quitting: %+v", l)
	}
}
