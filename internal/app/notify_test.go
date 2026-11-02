package app

import (
	"strings"
	"testing"
	"time"
)

func TestFinishWatcherReportsLongWorkOnly(t *testing.T) {
	w := newFinishWatcher()
	start := time.Unix(1000, 0)
	list := func(activity string) []sessionInfo {
		return []sessionInfo{
			{Name: "claude-api", Activity: activity},
			{Name: "shell", Activity: activity, Shell: true},
		}
	}
	if got := w.update(list("active"), start); len(got) != 0 {
		t.Fatalf("starting work reported %v", got)
	}
	// An unknown state (a host that did not answer) keeps the session working.
	if got := w.update(list(""), start.Add(5*time.Second)); len(got) != 0 {
		t.Fatalf("unknown state reported %v", got)
	}
	// A pause between two steps is not the end.
	if got := w.update(list("quiet"), start.Add(20*time.Second)); len(got) != 0 {
		t.Fatalf("a short pause reported %v", got)
	}
	w.update(list("active"), start.Add(24*time.Second))
	if got := w.update(list("quiet"), start.Add(30*time.Second)); len(got) != 0 {
		t.Fatalf("pause after resuming reported %v", got)
	}
	got := w.update(list("quiet"), start.Add(30*time.Second+notifyQuietFor))
	if len(got) != 1 || got[0].Name != "claude-api" {
		t.Fatalf("finish after long work = %v, want claude-api only", got)
	}
	if got := w.update(list("quiet"), start.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("staying quiet reported again: %v", got)
	}

	// A redraw on attach is a short burst: no notification.
	w.update(list("active"), start.Add(2*time.Minute))
	w.update(list("quiet"), start.Add(2*time.Minute+5*time.Second))
	if got := w.update(list("quiet"), start.Add(2*time.Minute+30*time.Second)); len(got) != 0 {
		t.Fatalf("short burst reported %v", got)
	}

	// A session that goes away is forgotten.
	w.update(list("active"), start.Add(3*time.Minute))
	w.update(nil, start.Add(4*time.Minute))
	if len(w.activeSince) != 0 {
		t.Fatalf("stopped sessions still tracked: %v", w.activeSince)
	}
}

func TestWindowFocusReportsAreActions(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	events := d.Feed([]byte("\x1b[Oa\x1b[I"))
	if len(events) != 3 || events[0].action != "window-blur" || string(events[1].data) != "a" || events[2].action != "window-focus" {
		t.Fatalf("events = %+v", events)
	}
}

func TestNotificationTextIsQuoted(t *testing.T) {
	if got := appleScriptString(`a "b" \c`); got != `"a \"b\" \\c"` {
		t.Errorf("appleScriptString = %s", got)
	}
	if got := powershellString("it's"); got != "'it''s'" {
		t.Errorf("powershellString = %s", got)
	}
	if got := notifyText("x\x1b]0;evil\ay"); strings.ContainsAny(got, "\x1b\a") {
		t.Errorf("notifyText kept control characters: %q", got)
	}
}

func TestFinishNotificationNamesSessionFolderAndHost(t *testing.T) {
	s := sessionInfo{Name: "claude-api"}
	for _, c := range []struct{ dir, backend, body string }{
		{"~/Projects/api", "native", "~/Projects/api"},
		{"~/Projects/api", "ssh:fedora", "~/Projects/api on fedora"},
		{"", "ssh:fedora", "on fedora"},
		{"", "native", "Waiting for you"},
	} {
		title, body := finishNotification(s, c.dir, c.backend)
		if title != "claude-api finished" || body != c.body {
			t.Errorf("finishNotification(%q, %q) = %q, %q; want body %q", c.dir, c.backend, title, body, c.body)
		}
	}
}

// After the machine slept, what was recorded before says nothing about now.
func TestFinishWatcherForgetsAcrossALongGap(t *testing.T) {
	w := newFinishWatcher()
	start := time.Unix(1000, 0)
	list := func(activity string) []sessionInfo { return []sessionInfo{{Name: "claude-api", Activity: activity}} }
	w.update(list("active"), start)
	w.update(list("quiet"), start.Add(10*time.Hour))
	if got := w.update(list("quiet"), start.Add(10*time.Hour+notifyQuietFor+time.Second)); len(got) != 0 {
		t.Fatalf("woke from sleep and reported %v", got)
	}
}
