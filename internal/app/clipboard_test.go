package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyToSystemClipboardUsesAvailableClientClipboard(t *testing.T) {
	dir := t.TempDir()
	clipboard := filepath.Join(dir, "clipboard")
	command := filepath.Join(dir, "wl-copy")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n/bin/cat > \"$LAM_TEST_CLIPBOARD\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("LAM_TEST_CLIPBOARD", clipboard)

	const text = "selected from the remote session\nsecond line"
	if !copyToSystemClipboard(text) {
		t.Fatal("copyToSystemClipboard() found no usable client clipboard")
	}
	got, err := os.ReadFile(clipboard)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Fatalf("clipboard text = %q, want %q", got, text)
	}
}

func TestOSC52Text(t *testing.T) {
	if got, ok := osc52Text([]byte("52;c;aGVsbG8=")); !ok || got != "hello" {
		t.Fatalf("osc52Text() = %q, %v", got, ok)
	}
	for _, bad := range []string{"52;c;?", "52;c;%%%", "52;c", "52;c;"} {
		if _, ok := osc52Text([]byte(bad)); ok {
			t.Fatalf("osc52Text(%q) accepted", bad)
		}
	}
}
