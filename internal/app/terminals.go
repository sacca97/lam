package app

import (
	"fmt"
	"strings"
)

// The integrated terminal of a session is a shell session named
// term-<label>-<id>, where label comes from the owning session's name. It
// outlives the UI, so a later run finds it by that label and attaches again.

// terminalLabelFor is the label a session's terminal carries in its name.
func terminalLabelFor(owner string) string {
	_, owner = splitHost(owner)
	label := sessionNameSlug(owner)
	if label == "" {
		label = "session"
	}
	if len(label) > 32 {
		label = label[:32]
	}
	return label
}

// terminalSessionLabel is the owner label in the name of a terminal session.
func terminalSessionLabel(name string) (string, bool) {
	_, name = splitHost(name)
	rest, ok := strings.CutPrefix(name, "term-")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 || len(rest)-i-1 != 8 {
		return "", false
	}
	return rest[:i], true
}

// hideOwnedTerminals drops the terminals that belong to a listed session, so
// the sidebar shows agents and not their shells. A terminal whose owner is gone
// stays listed, where it can be seen and stopped.
func hideOwnedTerminals(list []sessionInfo) []sessionInfo {
	owners := make(map[string]bool) // by machine and label
	for _, s := range list {
		if _, isTerminal := terminalSessionLabel(s.Name); !isTerminal {
			owners[s.Host+"\x00"+terminalLabelFor(s.Name)] = true
		}
	}
	visible := make([]sessionInfo, 0, len(list))
	for _, s := range list {
		if label, isTerminal := terminalSessionLabel(s.Name); isTerminal && owners[s.Host+"\x00"+label] {
			continue
		}
		visible = append(visible, s)
	}
	return visible
}

// ownedTerminals are the terminals of the named session in a raw listing. When
// another session's name gives the same label the terminals are ambiguous and
// none is returned, so one session never stops or renames another's shell.
func ownedTerminals(list []sessionInfo, owner string) []sessionInfo {
	label := terminalLabelFor(owner)
	host, _ := splitHost(owner)
	var out []sessionInfo
	if terminalLabelAmbiguous(list, owner) {
		return nil
	}
	for _, s := range list {
		if l, isTerminal := terminalSessionLabel(s.Name); isTerminal && l == label && s.Host == host {
			out = append(out, s)
		}
	}
	return out
}

func ownedTerminalsNow(owner string) []sessionInfo {
	list, err := backend().List()
	if err != nil {
		return nil
	}
	return ownedTerminals(list, owner)
}

// renamedTerminal is a terminal's name once its owner is called newOwner.
func renamedTerminal(name, newOwner string) string {
	host, _ := splitHost(name)
	return fmt.Sprintf("%sterm-%s-%s", hostPrefix(host), terminalLabelFor(newOwner), name[strings.LastIndexByte(name, '-')+1:])
}

// terminalLabelAmbiguous reports whether another listed session would give the
// same terminal label as owner.
func terminalLabelAmbiguous(list []sessionInfo, owner string) bool {
	label := terminalLabelFor(owner)
	host, _ := splitHost(owner)
	for _, s := range list {
		if _, isTerminal := terminalSessionLabel(s.Name); !isTerminal && s.Host == host && s.Name != owner && terminalLabelFor(s.Name) == label {
			return true
		}
	}
	return false
}

// stopOwnedTerminals ends the terminals of a session that has ended, so that
// closing a session, or quitting its agent, leaves no shell behind. It runs in
// the session's own host: every way a session ends (the window, the terminal
// UI, the phone, `lam kill`, the agent exiting) passes through there, and on
// the machine where the terminal lives.
func stopOwnedTerminals(owner string) {
	if _, isTerminal := terminalSessionLabel(owner); isTerminal {
		return
	}
	b := nativeBackend{}
	list, err := b.List()
	if err != nil {
		return
	}
	for _, t := range ownedTerminals(list, owner) {
		_ = b.Kill(t.Name)
	}
}
