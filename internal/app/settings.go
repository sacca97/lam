package app

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	terminalBelow = "below"
	terminalRight = "right"
)

var terminalPercents = []int{30, 40, 50}

// Dragging the divider can leave any size between these.
const (
	minTerminalPercent = 15
	maxTerminalPercent = 80
)

// uiSettings are the interface preferences kept between runs.
type uiSettings struct {
	TerminalPlacement string   `json:"terminalPlacement"`
	TerminalPercent   int      `json:"terminalPercent"`
	SidebarWidth      int      `json:"sidebarWidth,omitempty"`      // 0 keeps the default
	CollapsedProjects []string `json:"collapsedProjects,omitempty"` // folders folded in the sidebar
}

func settingsPath() string { return filepath.Join(stateDir(), "settings.json") }

func defaultSettings() uiSettings {
	return uiSettings{TerminalPlacement: terminalRight, TerminalPercent: 40}
}

func loadSettings() uiSettings {
	s := defaultSettings()
	if data, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.TerminalPlacement != terminalBelow && s.TerminalPlacement != terminalRight {
		s.TerminalPlacement = defaultSettings().TerminalPlacement
	}
	if s.TerminalPercent < minTerminalPercent || s.TerminalPercent > maxTerminalPercent {
		s.TerminalPercent = defaultSettings().TerminalPercent
	}
	return s
}

func saveSettings(s uiSettings) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil || os.MkdirAll(stateDir(), 0755) != nil {
		return
	}
	_ = os.WriteFile(settingsPath(), data, 0644)
}

// cyclePercent moves to the next (or previous) allowed terminal size.
func cyclePercent(current, step int) int {
	i := 0 // the preset nearest to a size set by dragging
	for j, p := range terminalPercents {
		if abs(p-current) < abs(terminalPercents[i]-current) {
			i = j
		}
	}
	return terminalPercents[(i+step+len(terminalPercents))%len(terminalPercents)]
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Dragging the sidebar's edge can leave any width between these.
const minSidebarWidth = 16

// clampSidebarWidth keeps the sidebar usable and leaves room for the panes.
func clampSidebarWidth(width, cols int) int {
	return max(1, min(max(minSidebarWidth, min(width, cols/2)), cols-3))
}
