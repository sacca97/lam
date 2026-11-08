package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	err := vaultStoreIn(account, password)
	if err == nil {
		vaultIndexAdd(account)
	}
	return err
}

func vaultStoreIn(account, password string) error {
	switch runtime.GOOS {
	case "darwin":
		// The password goes in on standard input, through security's own command
		// mode: as an argument it would show in the process list while it runs.
		cmd := "add-generic-password -U -a " + securityQuote(account) + " -s " + vaultService + " -w " + securityQuote(password) + "\n"
		if _, err := vaultRun(cmd, "security", "-i"); err != nil {
			return err
		}
		// That mode does not always fail loudly: check the Keychain has it.
		if got, ok := vaultLookup(account); !ok || got != password {
			return errors.New("the Keychain did not keep the password")
		}
		return nil
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
	vaultIndexRemove(account)
	switch runtime.GOOS {
	case "darwin":
		_, _ = vaultRun("", "security", "delete-generic-password", "-a", account, "-s", vaultService)
	case "linux":
		_, _ = vaultRun("", "secret-tool", "clear", "service", vaultService, "account", account)
	}
}

// securityQuote quotes a word for the security tool's command mode.
func securityQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s) + `"`
}

// The keychains cannot be listed without reading their secrets, so lam keeps the
// names (user@host, never a password) of what it stored in a file of its own,
// to show them and let the person forget them.
func vaultIndexPath() string { return filepath.Join(stateDir(), "vault-accounts") }

func vaultAccounts() []string {
	data, err := os.ReadFile(vaultIndexPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

func vaultIndexWrite(list []string) {
	_ = os.MkdirAll(stateDir(), 0o700)
	_ = writeFileAtomic(vaultIndexPath(), []byte(strings.Join(list, "\n")+"\n"), 0o600)
}

func vaultIndexAdd(account string) {
	list := vaultAccounts()
	for _, a := range list {
		if a == account {
			return
		}
	}
	vaultIndexWrite(append(list, account))
}

func vaultIndexRemove(account string) {
	var keep []string
	for _, a := range vaultAccounts() {
		if a != account {
			keep = append(keep, a)
		}
	}
	vaultIndexWrite(keep)
}

// vaultForgetAll removes every password lam stored, and says how many.
func vaultForgetAll() int {
	list := vaultAccounts()
	for _, a := range list {
		vaultDelete(a)
	}
	return len(list)
}

// forgetPasswordCmd is `lam forget-password [user@host | --all]`.
func forgetPasswordCmd(args []string) error {
	list := vaultAccounts()
	if len(args) == 0 {
		if len(list) == 0 {
			fmt.Println("No saved SSH passwords.")
			return nil
		}
		fmt.Println("Saved SSH passwords (forget one with: lam forget-password user@host, or --all):")
		for _, a := range list {
			fmt.Println("  " + a)
		}
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: lam forget-password [user@host | --all]")
	}
	if args[0] == "--all" {
		fmt.Printf("Forgot %d saved password(s).\n", vaultForgetAll())
		return nil
	}
	vaultDelete(args[0])
	fmt.Printf("Forgot the saved password for %s.\n", args[0])
	return nil
}
