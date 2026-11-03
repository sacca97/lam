package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentKind(t *testing.T) {
	for name, want := range map[string]docKind{"a.md": docMarkdown, "A.MARKDOWN": docMarkdown, "b.pdf": docPDF, "c.txt": docNone, "d": docNone} {
		if got := documentKind(name); got != want {
			t.Errorf("documentKind(%q) = %v", name, got)
		}
	}
}

func TestReadDocumentInPieces(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789"), docChunk/10*2+7) // more than two chunks
	path := filepath.Join(dir, "big.pdf")
	os.WriteFile(path, big, 0o644)
	d, err := readDocument(localDocumentSource{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Data, big) {
		t.Fatalf("read %d bytes, want %d", len(d.Data), len(big))
	}
	if _, err := readDocument(localDocumentSource{}, filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("a text file was accepted")
	}
	if _, err := readDocument(localDocumentSource{}, filepath.Join(dir, "missing.md")); err == nil {
		t.Fatal("a missing file was accepted")
	}
	huge := filepath.Join(dir, "huge.md")
	f, _ := os.Create(huge)
	f.Truncate(maxMarkdownLen + 1)
	f.Close()
	if _, err := readDocument(localDocumentSource{}, huge); err == nil || !strings.Contains(err.Error(), "up to") {
		t.Fatalf("an oversized file: %v", err)
	}
}

func TestReadFileChunkRefusesWhatIsNotAFile(t *testing.T) {
	if _, _, _, err := readFileChunk("relative.md", 0, 10); err == nil {
		t.Fatal("a relative path was accepted")
	}
	if _, _, _, err := readFileChunk(t.TempDir(), 0, 10); err == nil {
		t.Fatal("a directory was accepted")
	}
	path := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(path, []byte("hello"), 0o644)
	if _, _, _, err := readFileChunk(path, 99, 10); err == nil {
		t.Fatal("an offset past the end was accepted")
	}
}

// The host answers read-file and complete-path as the viewer asks.
func TestRemoteReadFileAndCompletePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "notebook"), 0o755)
	got := dispatchRemoteRequest(remoteRequest{Op: "read-file", Path: filepath.Join(dir, "notes.md"), Length: 100})
	if got.Error != "" || string(got.Data) != "# hi" || got.Size != 4 || got.Modified == 0 {
		t.Fatalf("read-file = %+v", got)
	}
	got = dispatchRemoteRequest(remoteRequest{Op: "complete-path", Path: dir + "/no", Base: dir})
	if got.Error != "" || len(got.Directories) != 2 {
		t.Fatalf("complete-path = %+v", got)
	}
	got = dispatchRemoteRequest(remoteRequest{Op: "read-file", Path: "nope.md"})
	if got.Error == "" {
		t.Fatal("a relative path was read")
	}
}

func TestReadFileChunkReadsOnlyDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(path, []byte("x"), 0o600)
	if _, _, _, err := readFileChunk(path, 0, 10); err == nil {
		t.Fatal("a text file was served")
	}
}
