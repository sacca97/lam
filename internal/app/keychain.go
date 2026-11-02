package app

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// The desktop's keychain keeps SSH passwords the person chose to remember:
// the macOS Keychain, or the Secret Service on Linux (secret-tool). Nothing
// is stored without being asked, and key authentication is always tried first.

const vaultService = "lam-ssh"

// vaultTimeout bounds a keychain call: a locked keyring may wait for a person.
const vaultTimeout = 8 * time.Second

func vaultAccount(user, host string) string { return user + "@" + host }

// vaultAvailable is whether this machine has a keychain lam can use.
func vaultAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("security")
		return err == nil
	case "linux":
		_, err := exec.LookPath("secret-tool")
		return err == nil
	}
	return false
}

// vaultRun runs a keychain tool and returns what it printed.
func vaultRun(stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), vaultTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	return string(out), err
}

func vaultStore(account, password string) error {
	switch runtime.GOOS {
	case "darwin":
		_, err := vaultRun("", "security", "add-generic-password", "-U", "-a", account, "-s", vaultService, "-w", password)
		return err
	case "linux":
		_, err := vaultRun(password, "secret-tool", "store", "--label=lam ssh "+account, "service", vaultService, "account", account)
		return err
	}
	return exec.ErrNotFound
}

func vaultLookup(account string) (string, bool) {
	var out string
	var err error
	switch runtime.GOOS {
	case "darwin":
		out, err = vaultRun("", "security", "find-generic-password", "-a", account, "-s", vaultService, "-w")
	case "linux":
		out, err = vaultRun("", "secret-tool", "lookup", "service", vaultService, "account", account)
	default:
		return "", false
	}
	pw := strings.TrimRight(out, "\r\n")
	return pw, err == nil && pw != ""
}

func vaultDelete(account string) {
	switch runtime.GOOS {
	case "darwin":
		_, _ = vaultRun("", "security", "delete-generic-password", "-a", account, "-s", vaultService)
	case "linux":
		_, _ = vaultRun("", "secret-tool", "clear", "service", vaultService, "account", account)
	}
}
