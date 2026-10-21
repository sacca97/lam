package app

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const daemonPollInterval = 2 * time.Second

// sessionState tracks status for a session.
type sessionState struct {
	Status string
}

// runDaemon runs the background unread-detection loop.
// Polls sessions, tracks status transitions and client detach,
// and marks sessions as unread appropriately.
func runDaemon() {
	useLoginShellPATH()
	fmt.Println("LAM daemon: starting unread detection")

	prevStates := make(map[string]sessionState)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	remoteStop := make(chan struct{})
	var remoteDone chan struct{}
	if address, err := loadRemoteListenConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "LAM daemon: remote listener configuration: %v\n", err)
	} else if address != "" {
		remoteDone = make(chan struct{})
		go func() {
			defer close(remoteDone)
			maintainRemoteTCPListener(address, remoteStop)
		}()
	}

	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	// Run immediately on start, then on tick
	checkUnread(prevStates)

	for {
		select {
		case <-ticker.C:
			checkUnread(prevStates)
		case s := <-sig:
			fmt.Printf("LAM daemon: caught %s, exiting\n", s)
			close(remoteStop)
			if remoteDone != nil {
				<-remoteDone
			}
			return
		}
	}
}

func maintainRemoteTCPListener(address string, stop <-chan struct{}) {
	for {
		err := serveRemoteTCPWithStop(address, stop)
		if err == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "LAM daemon: remote listener failed: %v; retrying in 5s\n", err)
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func checkUnread(prevStates map[string]sessionState) {
	sessions := listSessions()

	// Track which sessions still exist to clean up stale state
	alive := make(map[string]bool)

	for _, s := range sessions {
		alive[s.Name] = true

		if s.Command != "claude" {
			continue
		}

		curStatus := ""
		if data, err := os.ReadFile(fmt.Sprintf("/tmp/claude-status-%s", s.Name)); err == nil {
			curStatus = strings.TrimSpace(string(data))
		}

		prev, hasPrev := prevStates[s.Name]
		prevStates[s.Name] = sessionState{Status: curStatus}

		if !hasPrev {
			// First time seeing this session — just record, don't trigger
			continue
		}

		prevIdle := prev.Status == "waiting" || prev.Status == ""
		nowIdle := curStatus == "waiting" || curStatus == ""
		wasWorking := !prevIdle && prev.Status != ""

		// Working → idle: mark unread (always, even if someone is attached)
		if wasWorking && nowIdle {
			os.WriteFile(fmt.Sprintf("/tmp/claude-unread-%s", s.Name), []byte{}, 0644)
		}

		// Idle → working: someone responded, clear unread
		if prevIdle && !nowIdle {
			clearUnread(s.Name)
		}
	}

	// Clean up prevStates for dead sessions
	for name := range prevStates {
		if !alive[name] {
			delete(prevStates, name)
		}
	}

}

// installService writes and enables the appropriate service file
// for the current OS (systemd on Linux, launchd on Mac).
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable path: %w", err)
	}
	// Resolve symlinks to get the real path
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	switch runtime.GOOS {
	case "linux":
		return installSystemd(exe)
	case "darwin":
		return installLaunchd(exe)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// uninstallService removes and disables the service.
func uninstallService() error {
	switch runtime.GOOS {
	case "linux":
		return uninstallSystemd()
	case "darwin":
		return uninstallLaunchd()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func daemonServiceInstalled() bool {
	path := ""
	switch runtime.GOOS {
	case "linux":
		path = systemdPath()
	case "darwin":
		path = launchdPath()
	default:
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func restartDaemonService() error {
	switch runtime.GOOS {
	case "linux":
		if err := exec.Command("systemctl", "--user", "restart", systemdUnit).Run(); err != nil {
			return fmt.Errorf("restart daemon service: %w", err)
		}
	case "darwin":
		_ = exec.Command("launchctl", "unload", launchdPath()).Run()
		if err := exec.Command("launchctl", "load", launchdPath()).Run(); err != nil {
			return fmt.Errorf("reload daemon service: %w", err)
		}
	default:
		return fmt.Errorf("service management is unsupported on %s", runtime.GOOS)
	}
	return nil
}

// --- systemd ---

const systemdUnit = "lam-daemon.service"

func systemdDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func systemdPath() string {
	return filepath.Join(systemdDir(), systemdUnit)
}

// systemdUnitText is the user service for the daemon.
//
// KillMode=process matters: the daemon starts session hosts for remote
// clients, so they live in the service's control group, and the default mode
// stops every process in the group when the service stops or restarts. That
// ended running sessions whenever LAM was reinstalled or the daemon was
// restarted. With KillMode=process only the daemon itself is stopped and the
// sessions keep running.
func systemdUnitText(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=LAM daemon - unread detection for agent sessions
After=default.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=5
KillMode=process

[Install]
WantedBy=default.target
`, exe)
}

func installSystemd(exe string) error {
	unit := systemdUnitText(exe)

	dir := systemdDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create systemd dir: %w", err)
	}
	if err := os.WriteFile(systemdPath(), []byte(unit), 0644); err != nil {
		return fmt.Errorf("cannot write unit file: %w", err)
	}

	// Reload and enable
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("daemon-reload failed: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnit).Run(); err != nil {
		return fmt.Errorf("enable failed: %w", err)
	}

	fmt.Printf("  Installed %s\n", systemdPath())
	fmt.Printf("  Service enabled and started.\n")
	fmt.Printf("  Status: systemctl --user status %s\n", systemdUnit)
	fmt.Printf("  Logs:   journalctl --user -u %s -f\n", systemdUnit)
	return nil
}

func uninstallSystemd() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnit).Run()
	if err := os.Remove(systemdPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove unit file: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("  Service stopped and removed.")
	return nil
}

// --- launchd ---

const launchdLabel = "com.lam.daemon"

func launchdPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// launchdPlistText is the launch agent for the daemon. AbandonProcessGroup
// is the launchd counterpart of KillMode=process: without it launchd kills
// every process the daemon started, session hosts included, when the agent is
// unloaded or reloaded.
func launchdPlistText(exe string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>AbandonProcessGroup</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/lam-daemon.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/lam-daemon.log</string>
</dict>
</plist>
`, launchdLabel, exe)
}

func installLaunchd(exe string) error {
	plist := launchdPlistText(exe)

	dir := filepath.Dir(launchdPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(launchdPath(), []byte(plist), 0644); err != nil {
		return fmt.Errorf("cannot write plist: %w", err)
	}

	if err := exec.Command("launchctl", "load", launchdPath()).Run(); err != nil {
		return fmt.Errorf("launchctl load failed: %w", err)
	}

	fmt.Printf("  Installed %s\n", launchdPath())
	fmt.Printf("  Service loaded and running.\n")
	fmt.Printf("  Logs: tail -f /tmp/lam-daemon.log\n")
	return nil
}

func uninstallLaunchd() error {
	_ = exec.Command("launchctl", "unload", launchdPath()).Run()
	path := launchdPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove plist: %w", err)
	}
	fmt.Println("  Service stopped and removed.")
	return nil
}
