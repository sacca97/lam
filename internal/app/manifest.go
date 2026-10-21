package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The manifest remembers sessions so they can be reopened after their hosts
// are gone (reboot, crash, logout). A host removes its entry when the session
// is stopped on purpose or its command exits by itself, and keeps it when the
// host is ended by a signal.

type manifestEntry struct {
	Name    string    `json:"name"`
	CWD     string    `json:"cwd"`
	Command string    `json:"command"`
	Created time.Time `json:"created"`
}

func manifestPath() string { return filepath.Join(stateDir(), "sessions.json") }

// managedTerminalName reports whether name is an integrated-terminal session,
// which is scratch space and never restored.
func managedTerminalName(name string) bool { return strings.HasPrefix(name, "term-") }

func readManifest() []manifestEntry {
	data, err := os.ReadFile(manifestPath())
	if err != nil {
		return nil
	}
	var entries []manifestEntry
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	return entries
}

// updateManifest applies fn to the manifest under an exclusive lock, since
// every session host writes to it.
func updateManifest(fn func([]manifestEntry) []manifestEntry) error {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(manifestPath()+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	entries := fn(readManifest())
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, manifestPath())
}

func manifestRecord(entry manifestEntry) {
	if managedTerminalName(entry.Name) {
		return
	}
	_ = updateManifest(func(entries []manifestEntry) []manifestEntry {
		for i := range entries {
			if entries[i].Name == entry.Name {
				entry.Created = entries[i].Created // keep the original order
				entries[i] = entry
				return entries
			}
		}
		return append(entries, entry)
	})
}

func manifestForget(names ...string) {
	drop := make(map[string]struct{}, len(names))
	for _, name := range names {
		drop[name] = struct{}{}
	}
	_ = updateManifest(func(entries []manifestEntry) []manifestEntry {
		kept := entries[:0]
		for _, e := range entries {
			if _, ok := drop[e.Name]; !ok {
				kept = append(kept, e)
			}
		}
		return kept
	})
}

func manifestRename(oldName, newName string) {
	_ = updateManifest(func(entries []manifestEntry) []manifestEntry {
		for i := range entries {
			if entries[i].Name == oldName {
				entries[i].Name = newName
			}
		}
		return entries
	})
}

// restorableSessions are manifest entries with no live session, oldest first.
func restorableSessions() []manifestEntry {
	live, err := listSessionsWithError()
	if err != nil {
		return nil
	}
	running := make(map[string]struct{}, len(live))
	for _, s := range live {
		running[s.Name] = struct{}{}
	}
	var out []manifestEntry
	for _, e := range readManifest() {
		if _, ok := running[e.Name]; !ok && !managedTerminalName(e.Name) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// resumeCommand turns a session's original command into one that continues
// the agent's last conversation in that directory. Other commands are
// returned unchanged. The result is stable: resuming an already-resumed
// command gives the same command.
func resumeCommand(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return command
	}
	hasQuote := strings.ContainsAny(command, `'"`)
	hasFlag := func(flags ...string) bool {
		for _, f := range fields {
			for _, want := range flags {
				if f == want {
					return true
				}
			}
		}
		return false
	}
	switch commandName(command) {
	case "claude":
		if hasQuote {
			// A starting prompt must not be sent again.
			base := "claude"
			if strings.Contains(command, "--permission-mode auto") {
				base += " --permission-mode auto"
			}
			return base + " --continue"
		}
		if hasFlag("--continue", "-c", "--resume", "-r") {
			return command
		}
		return command + " --continue"
	case "codex":
		if len(fields) > 1 && fields[1] == "resume" {
			return command
		}
		base := "codex resume --last"
		if strings.Contains(command, "--approve-for-me") {
			base += " --approve-for-me"
		}
		return base
	case "pi", "opencode":
		if hasQuote {
			return fields[0] + " --continue"
		}
		if hasFlag("--continue", "-c", "--session", "-s") {
			return command
		}
		return command + " --continue"
	}
	return command
}

// restoreSessions recreates sessions from the manifest. Sessions whose
// directory no longer exists are dropped from it.
func restoreSessions(entries []manifestEntry) (restored int, failures []string) {
	for _, e := range entries {
		if e.CWD != "" {
			if st, err := os.Stat(e.CWD); err != nil || !st.IsDir() {
				failures = append(failures, e.Name+": directory is gone")
				manifestForget(e.Name)
				continue
			}
		}
		if err := newSession(e.Name, e.CWD, resumeCommand(e.Command)); err != nil {
			failures = append(failures, e.Name+": "+err.Error())
			continue
		}
		restored++
	}
	return restored, failures
}

func restoreCmd(args []string) error {
	list := false
	for _, a := range args {
		if a != "--list" {
			return fmt.Errorf("usage: lam restore [--list]")
		}
		list = true
	}
	entries := restorableSessions()
	if len(entries) == 0 {
		fmt.Println("Nothing to restore.")
		return nil
	}
	if list {
		for _, e := range entries {
			fmt.Printf("%s\t%s\t%s\n", e.Name, e.CWD, resumeCommand(e.Command))
		}
		return nil
	}
	restored, failures := restoreSessions(entries)
	fmt.Printf("Restored %d session(s).\n", restored)
	for _, f := range failures {
		fmt.Fprintln(os.Stderr, "  "+f)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d session(s) could not be restored", len(failures))
	}
	return nil
}

// sessionRestorer is implemented by backends that can reopen sessions that
// ended with a reboot or crash: the local backend and remote hosts.
type sessionRestorer interface {
	RestorableSessions() ([]manifestEntry, error)
	RestoreSessions(names []string) (restored int, failures []string, err error)
	DiscardRestorable(names []string) error
}

func (nativeBackend) RestorableSessions() ([]manifestEntry, error) {
	return restorableSessions(), nil
}

func (nativeBackend) RestoreSessions(names []string) (int, []string, error) {
	restored, failures := restoreNamed(names)
	return restored, failures, nil
}

func (nativeBackend) DiscardRestorable(names []string) error {
	manifestForget(names...)
	return nil
}

// restoreNamed restores the restorable sessions called names. The commands
// come from this machine's manifest, never from the caller.
func restoreNamed(names []string) (int, []string) {
	want := make(map[string]struct{}, len(names))
	for _, name := range names {
		want[name] = struct{}{}
	}
	var entries []manifestEntry
	for _, e := range restorableSessions() {
		if _, ok := want[e.Name]; ok {
			entries = append(entries, e)
		}
	}
	return restoreSessions(entries)
}
