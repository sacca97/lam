package app

import (
	"strings"
	"testing"
)

func TestKeyboardHelpOverlayShowsShortcutsInPopup(t *testing.T) {
	overlay := keyboardHelpOverlay(80, 24)
	for _, want := range []string{"LAM shortcuts", "Ctrl+Space", "Shift+drag", "Select/copy text", "New session", "Toggle terminal", "Quit LAM"} {
		if !strings.Contains(overlay, want) {
			t.Errorf("keyboard help popup is missing %q", want)
		}
	}
}

func TestKeyboardHelpOmitsRemovedScheduleControls(t *testing.T) {
	overlay := keyboardHelpOverlay(80, 24)
	for _, removed := range []string{"Switch area", "New schedule", "Recent Runs"} {
		if strings.Contains(overlay, removed) {
			t.Fatalf("keyboard help contains removed control %q: %q", removed, overlay)
		}
	}
}
