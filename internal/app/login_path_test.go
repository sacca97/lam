package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractMarkedPath(t *testing.T) {
	out := "motd noise\n" + loginPathMarker + "/a:/b" + loginPathMarker + "\n"
	if got := extractMarkedPath(out); got != "/a:/b" {
		t.Fatalf("extractMarkedPath() = %q", got)
	}
	if got := extractMarkedPath("no marker"); got != "" {
		t.Fatalf("extractMarkedPath(no marker) = %q", got)
	}
}

func TestMergePATHKeepsOrderAndSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	got := mergePATH("/usr/bin::/bin", []string{"/usr/bin", dir, dir, "/does/not/exist", ""})
	if want := "/usr/bin:/bin:" + dir; got != want {
		t.Fatalf("mergePATH() = %q, want %q", got, want)
	}
}

func TestLoginShellPATHIsCached(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// A shell that prints a marked PATH and counts how often it was started.
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	shell := filepath.Join(dir, "fakeshell")
	script := "#!/bin/sh\necho x >> " + counter + "\nprintf '%s%s%s' " + loginPathMarker + " /fake/bin:/usr/bin " + loginPathMarker + "\n"
	if err := os.WriteFile(shell, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	for i := 0; i < 3; i++ {
		if got := loginShellPATH(); got != "/fake/bin:/usr/bin" {
			t.Fatalf("loginShellPATH() = %q", got)
		}
	}
	if data, _ := os.ReadFile(counter); len(data) != 2 { // "x\n" once
		t.Fatalf("the shell was started %d times, want 1", len(data)/2)
	}

	// An expired entry is refreshed.
	stale := loginPathCache{Shell: shell, Path: "/stale", Fetched: time.Now().Add(-2 * loginPathCacheTTL)}
	data, _ := json.Marshal(stale)
	os.WriteFile(loginPathCacheFile(), data, 0600)
	if got := loginShellPATH(); got != "/fake/bin:/usr/bin" {
		t.Fatalf("stale cache used: %q", got)
	}
}
