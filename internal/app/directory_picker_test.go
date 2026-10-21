package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompleteSessionDirsUsesSelectedBaseAndOnlyDirectories(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"Alpha", "Alpine", ".archive", "nested/Child"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "Alphanumeric"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, err := completeSessionDirs("Al", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Alpha/", "Alpine/"}; !reflect.DeepEqual(matches, want) {
		t.Fatalf("completeSessionDirs(Al) = %#v, want %#v", matches, want)
	}

	matches, err = completeSessionDirs("nested/Ch", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nested/Child/"}; !reflect.DeepEqual(matches, want) {
		t.Fatalf("completeSessionDirs(nested/Ch) = %#v, want %#v", matches, want)
	}

	matches, err = completeSessionDirs(".", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".archive/"}; !reflect.DeepEqual(matches, want) {
		t.Fatalf("completeSessionDirs(.) = %#v, want %#v", matches, want)
	}
}

func TestCommonDirectoryPrefix(t *testing.T) {
	if got, want := commonDirectoryPrefix([]string{"~/Projects/lam/", "~/Projects/lambda/"}), "~/Projects/lam"; got != want {
		t.Fatalf("commonDirectoryPrefix = %q, want %q", got, want)
	}
}

func TestFormatDirectoryCompletionsShowsPathsRelativeToTypedParent(t *testing.T) {
	got := formatDirectoryCompletions("~/Pro", []string{"~/Projects/", "~/Profiles/"})
	if want := "Projects/  Profiles/"; got != want {
		t.Fatalf("formatDirectoryCompletions = %q, want %q", got, want)
	}
}

func TestRemoteDirectoryCompletionOperation(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "remote-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	response := dispatchRemoteRequest(remoteRequest{Op: "complete-dir", Path: "remote", Base: base})
	if response.Error != "" {
		t.Fatalf("complete-dir returned error: %s", response.Error)
	}
	if want := []string{"remote-project/"}; !reflect.DeepEqual(response.Directories, want) {
		t.Fatalf("remote directory completions = %#v, want %#v", response.Directories, want)
	}
}
