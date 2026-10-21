package app

import (
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
)

type mouseModeTracker struct {
	modes atomic.Uint32
}

func (tracker *mouseModeTracker) Update(mode ansi.Mode, enabled bool) bool {
	bit := mouseTrackingModeBit(mode)
	if bit == 0 {
		return false
	}
	for {
		current := tracker.modes.Load()
		next := current &^ bit
		if enabled {
			next = current | bit
		}
		if current == next {
			return false
		}
		if tracker.modes.CompareAndSwap(current, next) {
			return true
		}
	}
}

func (tracker *mouseModeTracker) Active() bool {
	return tracker.modes.Load() != 0
}

func mouseTrackingModeBit(mode ansi.Mode) uint32 {
	switch mode {
	case ansi.ModeMouseX10:
		return 1 << 0
	case ansi.ModeMouseNormal:
		return 1 << 1
	case ansi.ModeMouseHighlight:
		return 1 << 2
	case ansi.ModeMouseButtonEvent:
		return 1 << 3
	case ansi.ModeMouseAnyEvent:
		return 1 << 4
	default:
		return 0
	}
}

func outerMouseCaptureNeeded(mode string) bool {
	// The session view has interactive panes even when the shell is the only
	// pane, so it must receive clicks to switch focus. Modal screens do not
	// handle mouse events and leave selection to the outer terminal.
	return mode == ""
}

func isShiftMouseButtonEvent(event terminalMouseEvent) bool {
	return event.button&4 != 0 && event.button&64 == 0
}
