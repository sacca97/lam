package app

import (
	"runtime"
	"testing"
)

func TestPreferGUIHonoursTheSwitches(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("LAM_UI", "")
	if !preferGUI() {
		t.Error("the window is the default")
	}
	t.Setenv("LAM_UI", "tui")
	if preferGUI() {
		t.Error("LAM_UI=tui must keep the terminal interface")
	}
	t.Setenv("LAM_UI", "")
	s := loadSettings()
	s.UI = "tui"
	saveSettings(s)
	if preferGUI() {
		t.Error(`"ui": "tui" in the settings must keep the terminal interface`)
	}
}

func TestLaunchGUIDoesNothingWithoutADisplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display check differs per platform")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if launchGUI("gui", "ssh", "somewhere") {
		t.Fatal("a window must not be started on a machine with no display")
	}
}

func TestMacOpenArgsStartsAnInstanceOfTheApp(t *testing.T) {
	got := macOpenArgs("/Users/x/Applications/lam.app", []string{"gui", "ssh", "box"})
	want := []string{"-n", "-a", "/Users/x/Applications/lam.app", "--args", "ssh", "box"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
