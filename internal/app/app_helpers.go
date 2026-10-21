package app

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const allKeys = "qwertyuiopasdfghjlzxcvbnm"

func stateDir() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		home, _ := os.UserHomeDir()
		xdg = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(xdg, "lam")
}

func getLauncher() string {
	data, err := os.ReadFile(filepath.Join(stateDir(), "launcher"))
	if err != nil {
		return "claude"
	}
	l := strings.TrimSpace(string(data))
	if l == "claude" || l == "codex" || l == "opencode" || l == "pi" {
		return l
	}
	return "claude"
}

func setLauncher(l string) {
	dir := stateDir()
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "launcher"), []byte(l+"\n"), 0644)
}

type sessionLauncherLister interface {
	AvailableLaunchers() ([]string, error)
}

var supportedLaunchers = []string{"claude", "codex", "opencode", "pi"}

func availableSessionLaunchers() ([]string, error) {
	if remote, ok := backend().(sessionLauncherLister); ok {
		launchers, err := remote.AvailableLaunchers()
		if err != nil {
			return nil, err
		}
		return orderedSupportedLaunchers(launchers), nil
	}
	return installedLaunchers(), nil
}

func installedLaunchers() []string {
	launchers := make([]string, 0, len(supportedLaunchers))
	for _, launcher := range supportedLaunchers {
		if _, err := exec.LookPath(launcher); err == nil {
			launchers = append(launchers, launcher)
		}
	}
	return launchers
}

func orderedSupportedLaunchers(available []string) []string {
	set := make(map[string]struct{}, len(available))
	for _, launcher := range available {
		set[launcher] = struct{}{}
	}
	ordered := make([]string, 0, len(available))
	for _, launcher := range supportedLaunchers {
		if _, ok := set[launcher]; ok {
			ordered = append(ordered, launcher)
		}
	}
	return ordered
}

func preferredLauncher(available []string, lastUsed string) string {
	for _, launcher := range available {
		if launcher == lastUsed {
			return launcher
		}
	}
	if len(available) == 0 {
		return ""
	}
	return available[0]
}

func launcherLabel(launcher string) string {
	switch launcher {
	case "terminal":
		return "Terminal"
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	default:
		return launcher
	}
}

// autoApproveFlag is the launcher's automatic-approval mode (classifier /
// automatic review, still sandboxed or gated; never the "bypass everything" flags).
func autoApproveFlag(launcher string) string {
	if launcher == "codex" {
		return "--approve-for-me"
	}
	if launcher == "claude" {
		return "--permission-mode auto"
	}
	return ""
}

func launcherCmd(launcher, prompt string) string {
	cmd := launcher
	if flags := autoApproveFlag(launcher); flags != "" {
		cmd += " " + flags
	}
	if prompt != "" {
		switch launcher {
		case "claude":
			cmd += " " + shellQuote(prompt)
		case "pi":
			cmd += " -- " + shellQuote(prompt)
		}
	}
	return cmd
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func hasUnread(name string) bool {
	_, err := os.Stat(fmt.Sprintf("/tmp/claude-unread-%s", name))
	return err == nil
}

func clearUnread(name string) {
	if remote, ok := backend().(interface{ ClearUnread(string) error }); ok {
		_ = remote.ClearUnread(name)
		return
	}
	os.Remove(fmt.Sprintf("/tmp/claude-unread-%s", name))
}

// resolveDir expands ~ and returns the absolute path of an existing directory.
func resolveDir(arg string) (string, error) {
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		home, _ := os.UserHomeDir()
		arg = filepath.Join(home, arg[1:])
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", arg)
	}
	return abs, nil
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
