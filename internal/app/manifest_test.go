package app

import (
	"reflect"
	"testing"
)

func TestResumeCommand(t *testing.T) {
	for command, want := range map[string]string{
		"claude":                        "claude --continue",
		"claude --permission-mode auto": "claude --permission-mode auto --continue",
		"claude --permission-mode auto --continue":    "claude --permission-mode auto --continue",
		"claude --permission-mode auto 'fix the bug'": "claude --permission-mode auto --continue",
		"codex":                                "codex resume --last",
		"codex --approve-for-me":               "codex resume --last --approve-for-me",
		"codex resume --last --approve-for-me": "codex resume --last --approve-for-me",
		"pi":                                   "pi --continue",
		"pi --continue":                        "pi --continue",
		"pi -- 'start here'":                   "pi --continue",
		"opencode":                             "opencode --continue",
		`exec "${SHELL:-/bin/sh}" -l -i`:       `exec "${SHELL:-/bin/sh}" -l -i`,
		"htop":                                 "htop",
	} {
		if got := resumeCommand(command); got != want {
			t.Errorf("resumeCommand(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestManifestLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	manifestRecord(manifestEntry{Name: "a", CWD: "/x", Command: "claude"})
	manifestRecord(manifestEntry{Name: "b", CWD: "/y", Command: "pi"})
	manifestRecord(manifestEntry{Name: "term-a-1", CWD: "/x", Command: "sh"}) // scratch terminal: ignored
	manifestRename("a", "c")
	manifestForget("b")
	got := readManifest()
	if len(got) != 1 || got[0].Name != "c" || got[0].CWD != "/x" {
		t.Fatalf("manifest = %#v", got)
	}
	created := got[0].Created
	manifestRecord(manifestEntry{Name: "c", CWD: "/x", Command: "claude --continue"})
	got = readManifest()
	if len(got) != 1 || got[0].Command != "claude --continue" || !reflect.DeepEqual(got[0].Created, created) {
		t.Fatalf("upsert changed order or lost the command: %#v", got)
	}
}

func TestRemoteRestoreOps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("LAM_SOCKET_DIR", t.TempDir())
	manifestRecord(manifestEntry{Name: "keep", CWD: "/gone", Command: "pi"})
	manifestRecord(manifestEntry{Name: "drop", CWD: "/tmp", Command: "pi"})

	got := dispatchRemoteRequest(remoteRequest{ID: 1, Op: "restorable"})
	if len(got.Restorable) != 2 {
		t.Fatalf("restorable = %#v", got.Restorable)
	}
	if r := dispatchRemoteRequest(remoteRequest{ID: 2, Op: "discard-restorable", Names: []string{"drop"}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	// A directory that no longer exists is reported and forgotten, and names
	// outside the request are left alone.
	got = dispatchRemoteRequest(remoteRequest{ID: 3, Op: "restore", Names: []string{"keep", "unknown"}})
	if got.Restored != 0 || len(got.Failures) != 1 {
		t.Fatalf("restore = %#v", got)
	}
	if left := readManifest(); len(left) != 0 {
		t.Fatalf("manifest = %#v", left)
	}
}
