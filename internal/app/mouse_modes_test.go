package app

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOuterMouseCaptureNeeded(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{name: "shell-only session view supports pane clicks", want: true},
		{name: "session view supports pane clicks", want: true},
		{name: "modal screens leave mouse to terminal", mode: "help", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outerMouseCaptureNeeded(test.mode); got != test.want {
				t.Fatalf("outerMouseCaptureNeeded() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestIsShiftMouseButtonEvent(t *testing.T) {
	tests := []struct {
		name  string
		event terminalMouseEvent
		want  bool
	}{
		{name: "shift click", event: terminalMouseEvent{button: 4}, want: true},
		{name: "shift drag", event: terminalMouseEvent{button: 36}, want: true},
		{name: "ordinary click", event: terminalMouseEvent{button: 0}, want: false},
		{name: "shift wheel remains app scroll", event: terminalMouseEvent{button: 68}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isShiftMouseButtonEvent(test.event); got != test.want {
				t.Fatalf("isShiftMouseButtonEvent() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestMouseModeTracker(t *testing.T) {
	var tracker mouseModeTracker
	if !tracker.Update(ansi.ModeMouseNormal, true) || !tracker.Active() {
		t.Fatal("enabling mouse tracking should activate the tracker")
	}
	if !tracker.Update(ansi.ModeMouseAnyEvent, true) {
		t.Fatal("enabling a second mouse mode should change the tracker")
	}
	if !tracker.Update(ansi.ModeMouseNormal, false) || !tracker.Active() {
		t.Fatal("disabling one mode should retain the other active mode")
	}
	if !tracker.Update(ansi.ModeMouseAnyEvent, false) || tracker.Active() {
		t.Fatal("disabling the final mouse mode should clear the tracker")
	}
	if tracker.Update(ansi.ModeMouseExtSgr, true) || tracker.Active() {
		t.Fatal("the SGR encoding mode alone must not request mouse capture")
	}
}
