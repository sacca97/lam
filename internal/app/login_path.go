package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const loginPathMarker = "__LAM_PATH__"

// useLoginShellPATH widens PATH for processes started without a login shell
// (`ssh host lam remote`, the systemd/launchd daemon). Provider CLIs usually
// live in directories the user's shell profile adds, so without this the
// remote provider list is empty and spawned sessions cannot find them.
// Entries are only appended; the inherited PATH keeps precedence.
func useLoginShellPATH() {
	current := os.Getenv("PATH")
	extra := strings.Split(loginShellPATH(), ":")
	if home, err := os.UserHomeDir(); err == nil {
		for _, dir := range []string{".local/bin", ".npm-global/bin", ".bun/bin", ".opencode/bin", ".cargo/bin"} {
			extra = append(extra, filepath.Join(home, dir))
		}
	}
	extra = append(extra, "/opt/homebrew/bin", "/usr/local/bin")
	os.Setenv("PATH", mergePATH(current, extra))
}

// loginPathCacheTTL is how long a shell's PATH is reused. Starting an
// interactive login shell can take seconds with a heavy profile (nvm, conda),
// and this runs for every remote connection.
const loginPathCacheTTL = time.Hour

type loginPathCache struct {
	Shell   string    `json:"shell"`
	Path    string    `json:"path"`
	Fetched time.Time `json:"fetched"`
}

func loginPathCacheFile() string { return filepath.Join(stateDir(), "login-path.json") }

// loginShellPATH returns the PATH the user's shell builds as a login,
// interactive shell, from a cache while it is fresh. It returns "" if the
// shell cannot be run.
func loginShellPATH() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return ""
	}
	if data, err := os.ReadFile(loginPathCacheFile()); err == nil {
		var cached loginPathCache
		if json.Unmarshal(data, &cached) == nil && cached.Shell == shell && cached.Path != "" &&
			time.Since(cached.Fetched) < loginPathCacheTTL {
			return cached.Path
		}
	}
	path := askShellForPATH(shell)
	if path != "" {
		if data, err := json.Marshal(loginPathCache{Shell: shell, Path: path, Fetched: time.Now()}); err == nil {
			if os.MkdirAll(stateDir(), 0755) == nil {
				_ = os.WriteFile(loginPathCacheFile(), data, 0600)
			}
		}
	}
	return path
}

// askShellForPATH runs the shell. The marker lets the PATH be found among any
// noise a profile prints.
func askShellForPATH(shell string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", `printf '%s%s%s' `+loginPathMarker+` "$PATH" `+loginPathMarker)
	// A profile may leave a background process holding the output pipe open;
	// without this, Output would wait for it after the shell is killed.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return extractMarkedPath(string(out))
}

func extractMarkedPath(out string) string {
	start := strings.Index(out, loginPathMarker)
	if start < 0 {
		return ""
	}
	rest := out[start+len(loginPathMarker):]
	end := strings.Index(rest, loginPathMarker)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// mergePATH appends the existing directories in extra that current lacks.
func mergePATH(current string, extra []string) string {
	seen := make(map[string]struct{})
	var dirs []string
	for _, dir := range strings.Split(current, ":") {
		if dir == "" {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	for _, dir := range extra {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return strings.Join(dirs, ":")
}
