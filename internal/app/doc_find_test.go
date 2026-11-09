package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindDocument(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		return p
	}
	top := mk("notes.md")
	sub := mk("paper/main.pdf")
	deep := mk("a/b/c/deep.md")
	mk("node_modules/x/skip.md")
	mk(".hidden/h.md")
	work := filepath.Join(root, "paper")
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	src := localDocumentSource{}
	for _, c := range []struct {
		name  string
		bases []string
		rel   string
		want  string
	}{
		{"in the folder", []string{root}, "notes.md", top},
		{"as ./path", []string{root}, "./notes.md", top},
		{"in a subfolder", []string{root}, "main.pdf", sub},
		{"with its folder", []string{root}, "paper/main.pdf", sub},
		{"a few levels down", []string{root}, "deep.md", deep},
		{"from a subfolder, a path relative to the root", []string{work}, "paper/main.pdf", sub},
		{"live folder first, recorded second", []string{filepath.Join(root, "empty"), root}, "main.pdf", sub},
		{"not skipped into node_modules", []string{root}, "skip.md", ""},
		{"not hidden folders", []string{root}, "h.md", ""},
		{"missing", []string{root}, "nope.md", ""},
		{"absolute is not searched", []string{root}, top, ""},
	} {
		got, ok := findDocument(src, c.bases, c.rel)
		if c.want == "" && ok || c.want != "" && (!ok || got != c.want) {
			t.Errorf("%s: got %q %v, want %q", c.name, got, ok, c.want)
		}
	}
}
