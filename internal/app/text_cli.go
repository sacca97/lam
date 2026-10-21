package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Plain-text verbs for non-terminal clients (e.g. a phone over SSH).

type lsEntry struct {
	Name     string `json:"name"`
	CWD      string `json:"cwd"`
	Command  string `json:"command"`
	Activity string `json:"activity,omitempty"`
	Unread   bool   `json:"unread"`
	Shell    bool   `json:"shell"`
	PID      string `json:"pid,omitempty"`
	Clients  int    `json:"clients,omitempty"`
}

func lsCmd(args []string) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return fmt.Errorf("usage: lam ls [--json]")
		}
		asJSON = true
	}
	sessions, err := listSessionsWithError()
	if err != nil {
		return err
	}
	return writeLs(os.Stdout, sessions, asJSON)
}

func writeLs(w io.Writer, sessions []sessionInfo, asJSON bool) error {
	if asJSON {
		out := make([]lsEntry, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, lsEntry{s.Name, s.CWD, s.Command, s.Activity, s.Unread, s.Shell, s.PanePID, s.Clients})
		}
		return json.NewEncoder(w).Encode(out)
	}
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\n", s.Name, s.CWD)
	}
	return nil
}

func psCmd(args []string) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return fmt.Errorf("usage: lam ps [--json]")
		}
		asJSON = true
	}
	sessions, err := listSessionsWithError()
	if err != nil {
		return err
	}
	return writePs(os.Stdout, sessions, asJSON)
}

func writePs(w io.Writer, sessions []sessionInfo, asJSON bool) error {
	if asJSON {
		return writeLs(w, sessions, true)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tPID\tAGE\tCWD")
	for _, s := range sessions {
		name := s.Name
		if s.Unread {
			name += " \u25cf"
		}
		state := "detached"
		switch {
		case s.Clients == 1:
			state = "attached"
		case s.Clients > 1:
			state = fmt.Sprintf("attached (%d)", s.Clients)
		}
		pid := s.PanePID
		if pid == "" || pid == "0" {
			pid = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, state, pid, sessionAge(s.Created), psCWD(s))
	}
	return tw.Flush()
}

func sessionAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// psCWD prefers the session's live working directory where /proc allows it,
// else the directory it started in, with $HOME shortened to ~.
func psCWD(s sessionInfo) string {
	dir := s.CWD
	if pid, err := strconv.Atoi(s.PanePID); err == nil && pid > 0 {
		if live := processWorkingDirectory(pid); live != "" {
			dir = live
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		if dir == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(dir, home+"/"); ok {
			return "~/" + rest
		}
	}
	return dir
}

func sameSessionDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// tailCmd prints a session's scrollback and screen as plain text. With -f it
// keeps polling: appended text is streamed as-is; when the content was
// repainted instead (full-screen TUIs) a form feed ("\f") is written before
// the fresh snapshot so clients know to replace what they showed.
func tailCmd(args []string) error {
	follow := false
	var name string
	for _, a := range args {
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case strings.HasPrefix(a, "-") || name != "":
			return fmt.Errorf("usage: lam tail <session> [-f]")
		default:
			name = a
		}
	}
	if name == "" {
		return fmt.Errorf("usage: lam tail <session> [-f]")
	}
	prev, err := backend().History(name)
	if err != nil {
		return err
	}
	fmt.Print(prev)
	if !follow {
		return nil
	}
	for {
		time.Sleep(time.Second)
		cur, err := backend().History(name)
		if err != nil {
			return err
		}
		if cur == prev {
			continue
		}
		if strings.HasPrefix(cur, prev) {
			fmt.Print(cur[len(prev):])
		} else {
			fmt.Print("\f" + cur)
		}
		prev = cur
	}
}

func sendCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: lam send <session> <text>")
	}
	return backend().Send(args[0], strings.Join(args[1:], " "))
}
