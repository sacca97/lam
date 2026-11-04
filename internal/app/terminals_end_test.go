package app

import (
	"os"
	"testing"
	"time"
)

func sessionNames(t *testing.T) map[string]bool {
	t.Helper()
	list, err := nativeBackend{}.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range list {
		out[s.Name] = true
	}
	return out
}

func waitNames(t *testing.T, ok func(map[string]bool) bool) map[string]bool {
	t.Helper()
	var got map[string]bool
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if got = sessionNames(t); ok(got) {
			return got
		}
	}
	t.Fatalf("sessions: %v", got)
	return nil
}

// Closing a session, or its agent exiting, takes the session's terminal with
// it; closing the terminal alone leaves the session.
func TestEndingSessionStopsItsTerminal(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lam-term")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("LAM_SOCKET_DIR", dir)
	b := nativeBackend{}
	const term = "term-claude-demo-12345678"
	start := func(owner, command string) {
		t.Helper()
		if err := b.Create(owner, dir, command); err != nil {
			t.Fatal(err)
		}
		if err := b.Create(term, dir, "sleep 60"); err != nil {
			t.Fatal(err)
		}
	}

	// Stopping the terminal alone leaves the agent's session.
	start("claude-demo", "sleep 60")
	if err := b.Kill(term); err != nil {
		t.Fatal(err)
	}
	waitNames(t, func(n map[string]bool) bool { return !n[term] && n["claude-demo"] })

	// Stopping the session takes its terminal.
	if err := b.Create(term, dir, "sleep 60"); err != nil {
		t.Fatal(err)
	}
	if err := b.Kill("claude-demo"); err != nil {
		t.Fatal(err)
	}
	waitNames(t, func(n map[string]bool) bool { return !n[term] && !n["claude-demo"] })

	// The agent exiting by itself does the same.
	start("claude-demo", "sleep 1")
	waitNames(t, func(n map[string]bool) bool { return !n[term] && !n["claude-demo"] })
}
