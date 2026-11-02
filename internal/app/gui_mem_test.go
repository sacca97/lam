//go:build gui

package app

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

func memNow() (heap uint64, gor int) {
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc, runtime.NumGoroutine()
}

func memGUI(t *testing.T) (*guiApp, *guiDriver) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	g := newGUIState()
	g.settings = defaultSettings()
	g.settings.GUIFontSize = defaultGUIFontSize
	g.windowFocused = true
	// The sessions these tests start are real host processes: stop every one
	// when the test ends, whatever the test did with them.
	t.Cleanup(func() {
		if list, err := (nativeBackend{}).List(); err == nil {
			for _, s := range list {
				_ = (nativeBackend{}).Kill(s.Name)
			}
		}
		time.Sleep(300 * time.Millisecond)
	})
	return g, &guiDriver{t: t, g: g}
}

// Maps keyed by session name or cwd while sessions come and go.
func TestMemMapsGrowWithChurn(t *testing.T) {
	g, d := memGUI(t)
	defer g.shutdown()
	for i := 0; i < 1500; i++ {
		var list []sessionInfo
		for j := 0; j < 3; j++ {
			list = append(list, sessionInfo{Name: fmt.Sprintf("s-%d-%d", i, j), CWD: fmt.Sprintf("/proj/%d", i)})
		}
		g.applySessions(list, nil)
		d.frame()
	}
	t.Logf("after 1500 churn rounds: rowClicks=%d rowTags=%d headers=%d buttons=%d attachFail=%d lastStatus=%d collapsed=%d",
		len(g.rowClicks), len(g.rowTags), len(g.headers), len(g.buttons), len(g.attachFail), len(g.lastStatus), len(g.collapsed))
	h, n := memNow()
	t.Logf("heap=%d KB goroutines=%d", h>>10, n)
}

// Real sessions: terminals for sessions that disappear, and goroutines.
func TestMemRealSessionChurn(t *testing.T) {
	g, d := memGUI(t)
	defer g.shutdown()
	b := nativeBackend{}
	h0, n0 := memNow()
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("mem-%d", i)
		if err := b.Create(name, t.TempDir(), "cat"); err != nil {
			t.Fatal(err)
		}
		g.applySessions(listSessionsWithError())
		g.selected = name
		g.termVisible = true
		for k := 0; k < 4; k++ {
			d.frame()
			time.Sleep(20 * time.Millisecond)
		}
		b.Kill(name) // killed from outside (CLI/other client)
		g.applySessions(listSessionsWithError())
		for k := 0; k < 3; k++ {
			d.frame()
			time.Sleep(20 * time.Millisecond)
		}
	}
	g.selected = ""
	g.termVisible = false
	for k := 0; k < 5; k++ {
		d.frame()
	}
	h1, n1 := memNow()
	t.Logf("terminals still held=%d (all sessions are dead); goroutines %d -> %d; heap %d -> %d KB", len(g.terminals), n0, n1, h0>>10, h1>>10)
}

// Thousands of frames with output scrolling: heap and goroutines.
func TestMemFrames(t *testing.T) {
	g, d := memGUI(t)
	defer g.shutdown()
	b := nativeBackend{}
	if err := b.Create("mem-yes", t.TempDir(), "sh -c 'while :; do echo $RANDOM line of output; sleep 0.002; done'"); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("mem-yes")
	g.applySessions(listSessionsWithError())
	g.selected = "mem-yes"
	g.termVisible = true
	for k := 0; k < 50; k++ {
		d.frame()
		time.Sleep(5 * time.Millisecond)
	}
	h0, n0 := memNow()
	for r := 0; r < 4; r++ {
		for k := 0; k < 1500; k++ {
			g.dirty.Store(true)
			d.frame()
			if k%10 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		h, n := memNow()
		t.Logf("round %d: heap %d KB goroutines %d (start %d KB / %d)", r, h>>10, n, h0>>10, n0)
	}
}
