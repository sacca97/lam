package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, entries []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range entries {
		h := h
		h.Size = int64(len(bodies[h.Name]))
		if h.Typeflag == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write([]byte(bodies[h.Name]))
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func appTarball(t *testing.T, body string) []byte {
	return makeTarGz(t, []tar.Header{
		{Name: "lam.app/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "lam.app/Contents/MacOS/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "lam.app/Contents/MacOS/lam", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string]string{"lam.app/Contents/MacOS/lam": body})
}

func TestReplaceAppBundleSwapsTheApp(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "lam.app")
	os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", "lam"), []byte("old"), 0o755)
	if err := replaceAppBundle(bundle, appTarball(t, "\xcf\xfa\xed\xfenew")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "lam"))
	if string(got) != "\xcf\xfa\xed\xfenew" {
		t.Fatalf("executable = %q, want the new one", got)
	}
	if st, _ := os.Stat(filepath.Join(bundle, "Contents", "MacOS", "lam")); st.Mode()&0o111 == 0 {
		t.Fatal("the new executable lost its execute bit")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".lam-update-*")); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

func TestReplaceAppBundleKeepsTheOldAppOnABadDownload(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "lam.app")
	os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", "lam"), []byte("old"), 0o755)
	empty := makeTarGz(t, []tar.Header{{Name: "lam.app/", Typeflag: tar.TypeDir, Mode: 0o755}}, nil)
	if err := replaceAppBundle(bundle, empty); err == nil {
		t.Fatal("an app without an executable was installed")
	}
	if err := replaceAppBundle(bundle, []byte("not a tarball")); err == nil {
		t.Fatal("garbage was installed")
	}
	got, _ := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "lam"))
	if string(got) != "old" {
		t.Fatalf("the old app changed: %q", got)
	}
}

func TestExtractTarGzRefusesUnsafeEntries(t *testing.T) {
	for _, h := range []tar.Header{
		{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "lam.app/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		dest := t.TempDir()
		err := extractTarGz(makeTarGz(t, []tar.Header{h}, map[string]string{h.Name: "x"}), dest)
		if err == nil {
			t.Fatalf("%q was extracted", h.Name)
		}
		if _, statErr := os.Lstat(filepath.Join(filepath.Dir(dest), "evil")); statErr == nil {
			t.Fatal("a file escaped the destination")
		}
	}
}

func TestAppBundleOf(t *testing.T) {
	if got := appBundleOf("/Users/a/Applications/lam.app/Contents/MacOS/lam"); got != "/Users/a/Applications/lam.app" {
		t.Fatalf("got %q", got)
	}
	if got := appBundleOf("/usr/local/bin/lam"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestDesktopAssetNames(t *testing.T) {
	if got := desktopAssetName(); !strings.HasPrefix(got, "lam-") {
		t.Fatalf("got %q", got)
	}
}

func TestExtractTarGzIgnoresPaxGlobalHeaders(t *testing.T) {
	data := makeTarGz(t, []tar.Header{
		{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "abc"}},
		{Name: "lam.app/Contents/MacOS/lam", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string]string{"lam.app/Contents/MacOS/lam": "x"})
	dest := t.TempDir()
	if err := extractTarGz(data, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "lam.app", "Contents", "MacOS", "lam")); err != nil {
		t.Fatal(err)
	}
}
