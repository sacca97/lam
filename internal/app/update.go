package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// releasesURL is where `lam update` looks for builds. scripts/release.sh builds
// each platform and the release carries them as bare executables named
// lam-<os>-<arch>; GitHub records a SHA-256 digest for every asset.
var releasesURL = "https://api.github.com/repos/sacca97/lam/releases"

const maxReleaseDownload = 200 << 20

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"` // "sha256:<hex>", computed by GitHub
}

type release struct {
	Tag    string         `json:"tag_name"`
	Assets []releaseAsset `json:"assets"`
}

// releaseAssetName is the executable of this platform in a release.
func releaseAssetName() string {
	return fmt.Sprintf("lam-%s-%s", runtime.GOOS, runtime.GOARCH)
}

func httpGet(url string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lam-update")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return data, nil
}

// fetchRelease reads the latest release, or the named one.
func fetchRelease(tag string) (release, error) {
	url := releasesURL + "/latest"
	if tag != "" {
		url = releasesURL + "/tags/" + neturl.PathEscape(tag)
	}
	data, err := httpGet(url, 4<<20)
	if err != nil {
		return release{}, fmt.Errorf("look up the release: %w", err)
	}
	var r release
	if err := json.Unmarshal(data, &r); err != nil || r.Tag == "" {
		return release{}, errors.New("unexpected reply from the release server")
	}
	return r, nil
}

// looksExecutable guards against installing an error page or a truncated
// download: it must start like an ELF or Mach-O file.
func looksExecutable(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch {
	case b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F':
		return true
	case b[0] == 0xcf && b[1] == 0xfa && b[2] == 0xed && b[3] == 0xfe: // 64-bit Mach-O
		return true
	}
	return false
}

// replaceExecutable swaps the file at path for binary. The new file is written
// beside the old one and renamed over it, so a running lam is not disturbed.
func replaceExecutable(path string, binary []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lam-update-*")
	if err != nil {
		return fmt.Errorf("write next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// updateTarget is the executable `lam update` replaces; a variable for tests.
var updateTarget = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// installRelease downloads and installs the executable of a release for this
// platform. It is checked against the digest GitHub recorded for the asset.
func installRelease(r release) error {
	name := releaseAssetName()
	var asset *releaseAsset
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			asset = &r.Assets[i]
		}
	}
	if asset == nil {
		return fmt.Errorf("release %s has no build for %s/%s (%s)", r.Tag, runtime.GOOS, runtime.GOARCH, name)
	}
	binary, err := httpGet(asset.URL, maxReleaseDownload)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if want, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok {
		sum := sha256.Sum256(binary)
		if hex.EncodeToString(sum[:]) != strings.ToLower(want) {
			return fmt.Errorf("checksum mismatch for %s: not installing it", name)
		}
	}
	if !looksExecutable(binary) {
		return fmt.Errorf("%s is not an executable: not installing it", name)
	}
	target, err := updateTarget()
	if err != nil {
		return fmt.Errorf("find the lam executable: %w", err)
	}
	return replaceExecutable(target, binary)
}

// updateCmd is `lam update [--check] [vX.Y.Z]`.
func updateCmd(args []string) error {
	check, tag := false, ""
	for _, arg := range args {
		switch {
		case arg == "--check":
			check = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown option %q", arg)
		case tag == "":
			tag = arg
		default:
			return errors.New("usage: lam update [--check] [version]")
		}
	}
	r, err := fetchRelease(tag)
	if err != nil {
		return err
	}
	order, comparable := compareRelease(version, r.Tag)
	switch {
	case comparable && order == 0:
		fmt.Printf("lam %s is up to date.\n", version)
		return nil
	case comparable && order > 0 && tag == "":
		fmt.Printf("lam %s is newer than the latest release (%s).\n", version, r.Tag)
		return nil
	case check && !comparable:
		fmt.Printf("lam %s is a development build; the latest release is %s.\n", version, r.Tag)
		return nil
	case check:
		fmt.Printf("lam %s is installed; %s is available. Run `lam update`.\n", version, r.Tag)
		return nil
	}
	if !comparable && !check && !askYesNo(fmt.Sprintf("This is a development build (%s). Replace it with %s?", version, r.Tag)) {
		return nil
	}
	if comparable && order > 0 && !askYesNo(fmt.Sprintf("lam %s is newer than %s. Install the older release?", version, r.Tag)) {
		return nil
	}
	fmt.Printf("Updating lam %s -> %s ...\n", version, r.Tag)
	if err := installRelease(r); err != nil {
		return err
	}
	fmt.Printf("Updated to %s.\n", r.Tag)
	if daemonServiceInstalled() {
		err := installService()
		if err == nil {
			err = restartDaemonService()
		}
		if err != nil {
			fmt.Printf("Could not restart the background daemon: %v\n", err)
		} else {
			fmt.Println("Restarted the background daemon.")
		}
	}
	fmt.Println("Sessions keep running; restart open lam windows to use the new version.")
	return nil
}
