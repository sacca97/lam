package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type sessionInfo struct {
	Name     string
	Identity string
	PanePID  string
	Command  string
	CWD      string
	Unread   bool
	Activity string    `json:"activity,omitempty"`
	Shell    bool      `json:"shell,omitempty"`
	Created  time.Time `json:"created,omitempty"`
	Clients  int       `json:"clients,omitempty"`
}

// SessionBackend is the process-persistence layer LAM sits on. The native
// backend (native.go) is the only implementation; the interface stays so
// another backend can be added later.
type SessionBackend interface {
	Name() string
	// Create starts a detached session running the shell command in cwd.
	Create(name, cwd, command string) error
	List() ([]sessionInfo, error)
	Kill(name string) error
	// History returns the session's scrollback as plain text.
	History(name string) (string, error)
	// Send types text into the session followed by Enter.
	Send(name, text string) error
}

// processAttacher is implemented by backends whose sessions are attached by
// running a command on a terminal (the local one). Remote backends attach over
// their connection instead (see streamAttacher).
type processAttacher interface {
	// AttachCmd returns the (unstarted) command that attaches a terminal to the session.
	AttachCmd(name string) *exec.Cmd
}

// sessionRuntimeInspector is implemented by backends that can inspect a
// session process. Keeping it optional lets older backends preserve an
// uncertain shell rather than accidentally killing work.
type sessionRuntimeInspector interface {
	SessionWorkingDirectory(name string) (string, error)
	SessionStatus(name string) (busy bool, attachedClients int, err error)
}

func sessionProcessWorkingDirectory(name string) (string, error) {
	inspector, ok := backend().(sessionRuntimeInspector)
	if !ok {
		return "", fmt.Errorf("session working directory is unavailable")
	}
	return inspector.SessionWorkingDirectory(name)
}

func sessionRuntimeStatus(name string) (busy bool, attachedClients int, err error) {
	inspector, ok := backend().(sessionRuntimeInspector)
	if !ok {
		return true, 0, fmt.Errorf("session process state is unavailable")
	}
	return inspector.SessionStatus(name)
}

var selectedBackend SessionBackend

func backend() SessionBackend {
	if selectedBackend != nil {
		return selectedBackend
	}
	return nativeBackend{}
}

func setBackend(b SessionBackend) { selectedBackend = b }

func listSessions() []sessionInfo {
	s, _ := listSessionsWithError()
	return s
}

func listSessionsWithError() ([]sessionInfo, error) {
	s, err := backend().List()
	if err != nil {
		return nil, err
	}
	sort.Slice(s, func(i, j int) bool { return sessionLess(s[i], s[j]) })
	return s, nil
}

func newSession(name, cwd, command string) error { return backend().Create(name, cwd, command) }
func killSession(name string) error              { return backend().Kill(name) }
func sendToSession(name, text string) error      { return backend().Send(name, text) }

type sessionRenamer interface {
	Rename(name, newName string) error
}

func renameSession(name, newName string) error {
	renamer, ok := backend().(sessionRenamer)
	if !ok {
		return fmt.Errorf("session rename is unavailable")
	}
	return renamer.Rename(name, newName)
}

func lastSelectedSession() string {
	data, err := os.ReadFile(selectedSessionPath())
	if err != nil {
		return ""
	}
	name := strings.TrimSuffix(string(data), "\n")
	return strings.TrimSuffix(name, "\r")
}

func rememberSelectedSession(name string) {
	if name == "" {
		return
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".last-session-*")
	if err != nil {
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := fmt.Fprintln(f, name); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp, selectedSessionPath())
}

func selectedSessionPath() string {
	name := backend().Name()
	if name == "native" {
		return filepath.Join(stateDir(), "last-session")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	return filepath.Join(stateDir(), "last-session-"+key[:16])
}

// killSessionIdentity verifies the session selected when a confirmation began
// still exists before applying the action. A row that moved or was replaced
// during a list refresh can never redirect the confirmation to another target.
func killSessionIdentity(target sessionInfo) error {
	sessions, err := listSessionsWithError()
	if err != nil {
		return err
	}
	want := sessionIdentity(target)
	for _, s := range sessions {
		if sessionIdentity(s) == want {
			return killSession(s.Name)
		}
	}
	return fmt.Errorf("session %q is no longer active", target.Name)
}

// envWithout returns the environment minus the given variable names.
func envWithout(names ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		skip := false
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				skip = true
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

func hasSession(sessions []sessionInfo, name string) bool {
	for _, s := range sessions {
		if s.Name == name {
			return true
		}
	}
	return false
}

// commandName extracts the program name from a session command line,
// looking through the `sh -c '<command>'` wrapper LAM adds.
func commandName(cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if rest, ok := strings.CutPrefix(cmdline, "sh -c "); ok {
		cmdline = strings.Trim(strings.TrimSpace(rest), `'"`)
	}
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}
