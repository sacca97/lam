package app

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// stdinPrompt is shared by every question so buffered input is not lost
// between two prompts.
var stdinPrompt = bufio.NewReader(os.Stdin)

// askYesNo asks on the terminal; anything but y/yes is a no.
func askYesNo(question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false // nobody to ask: a script or wrapper must not hang
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, _ := stdinPrompt.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// offerUpdate is shown before the interface opens when the two ends run
// different builds. remoteBehind says which one is older. It never updates
// without an answer, and reports whether the remote host was updated so the
// caller can connect again.
func offerUpdate(host, remoteVersion string, remoteBehind bool) (remoteUpdated, localUpdated bool) {
	if remoteVersion == "" {
		remoteVersion = "an unknown build"
	}
	if !remoteBehind {
		fmt.Fprintf(os.Stderr, "  This lam (%s) is older than the remote one (%s).\n", version, remoteVersion)
		if askYesNo("  Update this machine now?") {
			if err := updateCmd(nil); err != nil {
				fmt.Fprintf(os.Stderr, "  Update failed: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "  Run lam again to use the new version.")
				return false, true
			}
		}
		return false, false
	}
	fmt.Fprintf(os.Stderr, "  The remote lam (%s) is older than this one (%s).\n", remoteVersion, version)
	if !askYesNo("  Update it now?") {
		return false, false
	}
	cmd, _, err := newSSHCommand(false, host, remoteToolCommand("update"))
	if err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
		err = cmd.Run()
	}
	if err == nil {
		return true, false
	}
	// An older lam has no update command: send this build over ssh instead.
	fmt.Fprintf(os.Stderr, "  Remote update failed (%v); trying to send this build instead.\n", err)
	if err := pushBinary(host); err != nil {
		fmt.Fprintf(os.Stderr, "  Could not send it: %v\n", err)
		return false, false
	}
	return true, false
}

// remotePlatform maps `uname -sm` to Go's GOOS/GOARCH names.
func remotePlatform(uname string) string {
	f := strings.Fields(strings.ToLower(uname))
	if len(f) != 2 {
		return ""
	}
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[f[1]]
	if arch == "" || (f[0] != "linux" && f[0] != "darwin") {
		return ""
	}
	return f[0] + "/" + arch
}

// pushBinary copies the running executable to ~/.local/bin/lam on the host. It
// only does so between machines of the same OS and architecture.
func pushBinary(host string) error {
	if err := validateSSHHost(host); err != nil {
		return err
	}
	out, err := exec.Command("ssh", "-T", "--", host, "uname -sm").Output()
	if err != nil {
		return fmt.Errorf("ask the remote host for its platform: %w", err)
	}
	if _, release := parseRelease(version); !release {
		return fmt.Errorf("this is a development build (%s); only release builds are sent", version)
	}
	if got, want := remotePlatform(string(out)), localPlatform(); got != want {
		return fmt.Errorf("the remote host is %q and this machine is %q; build lam there instead (make install)", strings.TrimSpace(string(out)), want)
	}
	exe, err := updateTarget()
	if err != nil {
		return err
	}
	f, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer f.Close()
	script := `mkdir -p "$HOME/.local/bin" && cat > "$HOME/.local/bin/lam.new" && chmod 755 "$HOME/.local/bin/lam.new" && mv "$HOME/.local/bin/lam.new" "$HOME/.local/bin/lam"` +
		// A running daemon, and the TCP listener in it, is still the old build.
		`; if [ -f "$HOME/.config/systemd/user/lam-daemon.service" ] || [ -f "$HOME/Library/LaunchAgents/com.lam.daemon.plist" ]; then "$HOME/.local/bin/lam" install-service || true; fi`
	cmd := exec.Command("ssh", "-T", "--", host, script)
	cmd.Stdin, cmd.Stderr = f, os.Stderr
	return cmd.Run()
}
