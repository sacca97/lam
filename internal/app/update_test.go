package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareRelease(t *testing.T) {
	cases := []struct {
		a, b  string
		order int
		ok    bool
	}{
		{"v1.2.3", "v1.2.4", -1, true},
		{"v1.10.0", "v1.9.9", 1, true},
		{"1.0.0", "v1.0.0", 0, true},
		{"dev", "v1.0.0", 0, false},
		{"v1.0.0-3-gabc-dirty", "v1.0.0", 0, false},
	}
	for _, c := range cases {
		if order, ok := compareRelease(c.a, c.b); order != c.order || ok != c.ok {
			t.Errorf("compareRelease(%q, %q) = %d, %v", c.a, c.b, order, ok)
		}
	}
}

func TestRemotePlatform(t *testing.T) {
	for in, want := range map[string]string{
		"Linux x86_64\n": "linux/amd64", "Darwin arm64": "darwin/arm64", "Linux aarch64": "linux/arm64", "FreeBSD amd64": "", "junk": "",
	} {
		if got := remotePlatform(in); got != want {
			t.Errorf("remotePlatform(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallReleaseVerifiesAndReplaces(t *testing.T) {
	binary := append([]byte{0x7f, 'E', 'L', 'F'}, "new-binary"...)
	sum := sha256.Sum256(binary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin":
			w.Write(binary)
		case "/page":
			fmt.Fprint(w, "<html>not found</html>")
		}
	}))
	defer srv.Close()
	asset := func(path, digest string) release {
		return release{Tag: "v2.0.0", Assets: []releaseAsset{{Name: releaseAssetName(), URL: srv.URL + path, Digest: digest}}}
	}
	target := filepath.Join(t.TempDir(), "lam")
	os.WriteFile(target, []byte("old-binary"), 0o755)
	old := updateTarget
	updateTarget = func() (string, error) { return target, nil }
	defer func() { updateTarget = old }()

	for name, rel := range map[string]release{
		"bad digest":     asset("/bin", "sha256:0000"),
		"not executable": asset("/page", ""),
	} {
		if err := installRelease(rel); err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if got, _ := os.ReadFile(target); string(got) != "old-binary" {
			t.Fatalf("executable changed despite %s: %q", name, got)
		}
	}
	if err := installRelease(asset("/bin", "sha256:"+hex.EncodeToString(sum[:]))); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != string(binary) {
		t.Fatalf("executable = %q, want the new build", got)
	}
}

func TestInstallReleaseNeedsABuildForThisPlatform(t *testing.T) {
	if err := installRelease(release{Tag: "v2.0.0"}); err == nil {
		t.Fatal("a release with no assets was accepted")
	}
}
