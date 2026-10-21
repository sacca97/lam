package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstalledLaunchersIncludesOnlySupportedCommandsOnPath(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"claude", "opencode", "pig"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)

	if got, want := installedLaunchers(), []string{"claude", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("installedLaunchers() = %#v, want %#v", got, want)
	}
}

func TestPreferredLauncherUsesLastUsedWhenAvailable(t *testing.T) {
	available := []string{"claude", "codex", "pi"}
	if got := preferredLauncher(available, "codex"); got != "codex" {
		t.Fatalf("preferredLauncher() = %q, want codex", got)
	}
	if got := preferredLauncher(available, "opencode"); got != "claude" {
		t.Fatalf("preferredLauncher() fallback = %q, want claude", got)
	}
	if got := preferredLauncher(nil, "claude"); got != "" {
		t.Fatalf("preferredLauncher() with no providers = %q, want empty", got)
	}
}

func TestRemoteLauncherListOperation(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"pi", "codex", "pig"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)

	response := dispatchRemoteRequest(remoteRequest{Op: "launchers"})
	if response.Error != "" {
		t.Fatalf("launchers returned error: %s", response.Error)
	}
	if want := []string{"codex", "pi"}; !reflect.DeepEqual(response.Launchers, want) {
		t.Fatalf("remote launchers = %#v, want %#v", response.Launchers, want)
	}
}
