package app

// What a session is doing is judged in two places and shown in many. The host
// judges its Activity from output, CPU and what the agent reported; the
// interface adds whether the user has looked since it finished (Unread). Every
// place that shows, counts or notifies about it asks sessionInfo.Phase, so the
// sidebar, the window title, the terminal list, `lam ls --json` and the
// notifications cannot disagree.
//
//	          output / CPU / agent says "working"
//	   ┌─────────────────────────────────────────────┐
//	   ▼                                             │
//	Working ──(finishes, nobody looking)──▶ Unread ──(selected / attached)──▶ Idle
//	   │                                                                       │
//	   └───────────────(finishes, somebody looking)────────────────────────────┘
//	                                                                          Idle ──(works again)──▶ Working
//
// Working and Idle come from Activity; Unread is a flag a finished agent sets.

// The values Activity takes on the wire (the host's judgment, native.go
// sessionActivitySnapshot.State and the remote protocol); "" is unknown.
const (
	activityActive = "active"
	activityQuiet  = "quiet"
)

// sessionPhase is what a person sees of a session.
type sessionPhase int

// In the order the sidebar lists them.
const (
	stateUnread  sessionPhase = iota // finished, and not looked at yet
	stateWorking                     // producing output or computing
	stateIdle                        // quiet, or not known to be working
)

var sessionPhaseNames = [...]string{"unread", "working", "idle"}

// String is the machine-readable name: what `lam ls --json` and the window
// title use, in lower case.
func (st sessionPhase) String() string {
	if st < 0 || int(st) >= len(sessionPhaseNames) {
		return "idle"
	}
	return sessionPhaseNames[st]
}

// Label is the name for a person: a sidebar group heading.
func (st sessionPhase) Label() string {
	switch st {
	case stateUnread:
		return "Unread"
	case stateWorking:
		return "Working"
	}
	return "Idle"
}

// Phase is the one reading of Activity and Unread. Unread wins: an agent that
// finished is waiting for you whatever it does next.
func (s sessionInfo) Phase() sessionPhase {
	switch {
	case s.Unread:
		return stateUnread
	case s.Activity == activityActive:
		return stateWorking
	}
	return stateIdle
}

// Working reports whether the session is producing output or computing, which
// is Activity alone: an unread session can be working again too.
func (s sessionInfo) Working() bool { return s.Activity == activityActive }
