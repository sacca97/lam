package app

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const fallbackShellCommand = `exec "${SHELL:-/bin/sh}" -l -i`

func isFallbackShellCommand(command string) bool {
	return strings.TrimSpace(command) == fallbackShellCommand
}

type shellAgentLaunch struct {
	provider   string
	command    string
	cancelLine bool
}

// shellCommandTracker recognizes a directly entered provider command while
// leaving ordinary editing and shell syntax to the shell itself.
type shellCommandTracker struct {
	line      []byte
	trackable bool
}

func (tracker *shellCommandTracker) Observe(data []byte) (shellAgentLaunch, bool) {
	lineWasAlreadySent := len(tracker.line) > 0
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch {
		case b == '\r' || b == '\n':
			if b == '\r' && i+1 < len(data) && data[i+1] == '\n' {
				i++
			}
			if i != len(data)-1 || !tracker.trackable {
				tracker.reset()
				continue
			}
			launch, ok := parseShellAgentLaunch(tracker.line)
			tracker.reset()
			if ok {
				launch.cancelLine = lineWasAlreadySent
				return launch, true
			}
		case b == 0x03:
			tracker.reset()
		case b == 0x15: // Ctrl+U
			tracker.line = tracker.line[:0]
			tracker.trackable = true
		case b == 0x17: // Ctrl+W
			tracker.deletePreviousWord()
		case b == 0x7f || b == '\b':
			tracker.deletePreviousRune()
		case b == 0x1b || b < 0x20:
			tracker.trackable = false
		default:
			if tracker.trackable {
				tracker.line = append(tracker.line, b)
			}
		}
	}
	return shellAgentLaunch{}, false
}

func (tracker *shellCommandTracker) reset() {
	tracker.line = tracker.line[:0]
	tracker.trackable = true
}

func (tracker *shellCommandTracker) deletePreviousRune() {
	if !tracker.trackable || len(tracker.line) == 0 {
		return
	}
	tracker.line = tracker.line[:len(tracker.line)-1]
	for len(tracker.line) > 0 && tracker.line[len(tracker.line)-1]&0xc0 == 0x80 {
		tracker.line = tracker.line[:len(tracker.line)-1]
	}
}

func (tracker *shellCommandTracker) deletePreviousWord() {
	if !tracker.trackable {
		return
	}
	for len(tracker.line) > 0 && tracker.line[len(tracker.line)-1] == ' ' {
		tracker.line = tracker.line[:len(tracker.line)-1]
	}
	for len(tracker.line) > 0 && tracker.line[len(tracker.line)-1] != ' ' {
		tracker.deletePreviousRune()
	}
}

func parseShellAgentLaunch(line []byte) (shellAgentLaunch, bool) {
	command := strings.TrimSpace(string(line))
	if command == "" || strings.ContainsAny(command, "\r\n|&;<>()`$\\") {
		return shellAgentLaunch{}, false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return shellAgentLaunch{}, false
	}
	provider := fields[0]
	switch provider {
	case "pi", "codex", "claude":
		return shellAgentLaunch{provider: provider, command: command}, true
	default:
		return shellAgentLaunch{}, false
	}
}

func terminalWorkingDirectory(value string) string {
	if strings.HasPrefix(value, "file://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			return ""
		}
		return filepath.Clean(u.Path)
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return ""
}

func processWorkingDirectory(pid int) string {
	if pid <= 0 {
		return ""
	}
	path, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil || !filepath.IsAbs(path) {
		return ""
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ""
	}
	return path
}

// processHasLiveChildren is used for the fallback shell's exit policy. A
// command started from the shell remains its child while it runs; a zombie no
// longer represents work that should keep the shell session alive.
func processHasLiveChildren(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	children, err := procChildren(pid)
	if err != nil {
		return false, err
	}
	for _, child := range children {
		if _, alive := procStatFields(child); alive {
			return true, nil
		}
	}
	return false, nil
}
