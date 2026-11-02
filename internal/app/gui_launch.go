package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// preferGUI says whether commands that show the interface (lam ssh) open the
// window rather than taking over the terminal. LAM_UI=tui or "ui": "tui" in
// the settings file turn it off, as does --tui for one run.
func preferGUI() bool {
	switch strings.ToLower(os.Getenv("LAM_UI")) {
	case "tui":
		return false
	case "gui":
		return true
	}
	return !strings.EqualFold(loadSettings().UI, "tui")
}

// guiExecutable finds a binary that can show the window: this one when it has
// the GUI built in, otherwise lam-gui installed next to it or on PATH.
func guiExecutable() string {
	exe, err := os.Executable()
	if err == nil {
		if guiBuilt {
			return exe
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		sibling := filepath.Join(filepath.Dir(exe), "lam-gui")
		if st, err := os.Stat(sibling); err == nil && !st.IsDir() {
			return sibling
		}
	}
	if p, err := exec.LookPath("lam-gui"); err == nil {
		return p
	}
	return ""
}

// haveDisplay says whether a window could open here: not in a plain SSH login
// or on a server without a desktop.
func haveDisplay() bool {
	switch runtime.GOOS {
	case "darwin":
		return os.Getenv("SSH_CONNECTION") == ""
	case "linux", "freebsd", "openbsd", "netbsd":
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return false
}

// launchGUI starts the window detached, the way `code .` does: the terminal is
// given back at once and closing it does not close the window. It reports
// false, having started nothing, when no window can be shown here.
func launchGUI(args ...string) bool {
	exe := guiExecutable()
	if exe == "" || !haveDisplay() {
		return false
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	_ = cmd.Process.Release()
	return true
}
