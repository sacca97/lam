package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestConfirmDialogOverlay(t *testing.T) {
	d := confirmDialog{title: "Stop session", body: []string{`Stop "lam-new"?`}, buttons: [2]string{"OK", "Cancel"}, hint: "Esc cancel"}
	got := d.overlay(100, 30, 0)
	plain := ansi.Strip(got)
	for _, want := range []string{"Stop session", "Esc cancel", `Stop "lam-new"?`, " OK ", " Cancel "} {
		if !strings.Contains(plain, want) {
			t.Errorf("overlay missing %q", want)
		}
	}
	if d.overlay(10, 5, 0) != "" {
		t.Error("overlay should be empty when the terminal is too small")
	}
	// The default button is highlighted, the other is not.
	if !strings.Contains(got, "\033[48;5;108m\033[38;5;235m OK ") || strings.Contains(got, "\033[48;5;108m\033[38;5;235m Cancel ") {
		t.Error("OK should be the highlighted button")
	}
	if !strings.Contains(d.overlay(100, 30, 1), "\033[48;5;108m\033[38;5;235m Cancel ") {
		t.Error("Cancel should be highlighted when selected")
	}
}
