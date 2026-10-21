package app

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestShiftLeftMousePressStartsSelection(t *testing.T) {
	for _, test := range []struct {
		name  string
		event terminalMouseEvent
		want  bool
	}{
		{name: "shift click", event: terminalMouseEvent{button: 4}, want: true},
		{name: "shift drag motion is not a start", event: terminalMouseEvent{button: 36}},
		{name: "release is not a start", event: terminalMouseEvent{button: 4, release: true}},
		{name: "ordinary click", event: terminalMouseEvent{button: 0}},
		{name: "shift wheel", event: terminalMouseEvent{button: 68}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shiftLeftMousePress(test.event); got != test.want {
				t.Fatalf("shiftLeftMousePress() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestTerminalSelectionTextAcrossLines(t *testing.T) {
	cells := [][]*uv.Cell{
		{
			{Content: "a", Width: 1}, {Content: "b", Width: 1}, {Content: "c", Width: 1},
			{Content: "d", Width: 1}, uv.EmptyCell.Clone(),
		},
		{
			{Content: "x", Width: 1}, {Content: "y", Width: 1}, {Content: "z", Width: 1},
			uv.EmptyCell.Clone(), uv.EmptyCell.Clone(),
		},
	}
	cellAt := func(x, y int) *uv.Cell { return cells[y][x] }

	got := terminalSelectionText(5, 2, terminalSelectionPoint{X: 2, Y: 0}, terminalSelectionPoint{X: 1, Y: 1}, cellAt)
	if got != "cd\nxy" {
		t.Fatalf("selected text = %q, want %q", got, "cd\nxy")
	}

	got = terminalSelectionText(5, 2, terminalSelectionPoint{X: 1, Y: 1}, terminalSelectionPoint{X: 2, Y: 0}, cellAt)
	if got != "cd\nxy" {
		t.Fatalf("reverse selection text = %q, want %q", got, "cd\nxy")
	}
}

func TestTerminalSelectionTextIncludesWideGraphemeWhenStartedOnPlaceholder(t *testing.T) {
	cells := [][]*uv.Cell{{
		{Content: "界", Width: 2},
		{Content: "", Width: 0},
		{Content: "x", Width: 1},
		uv.EmptyCell.Clone(),
	}}
	cellAt := func(x, y int) *uv.Cell { return cells[y][x] }

	got := terminalSelectionText(4, 1, terminalSelectionPoint{X: 1, Y: 0}, terminalSelectionPoint{X: 2, Y: 0}, cellAt)
	if got != "界x" {
		t.Fatalf("selected text = %q, want %q", got, "界x")
	}
}
