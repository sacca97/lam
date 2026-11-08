package app

import (
	"encoding/json"
	"fmt"
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
	MuteNotifications bool     `json:"muteNotifications,omitempty"` // no desktop notification when an agent finishes
	GUIFontSize       float32  `json:"guiFontSize,omitempty"`       // terminal text size in lam-gui, in sp
	GUILastHost       string   `json:"guiLastHost,omitempty"`       // the host lam-gui last connected to
	GUIGroup          string   `json:"guiGroup,omitempty"`          // the sidebar groups sessions by "state" (default) or "folder"
	GUIHosts          []string `json:"guiHosts,omitempty"`          // hosts lam-gui connected to, latest first: shown in the sidebar to connect again
	NoUpdateCheck     bool     `json:"noUpdateCheck,omitempty"`     // do not look for a newer release in the window
	UI                string   `json:"ui,omitempty"`                // "tui" makes lam ssh use the terminal interface instead of the window
	TailscaleAutoUp   bool     `json:"tailscaleAutoUp,omitempty"`   // turn Tailscale on without asking before reaching a tailnet host
	TailnetHosts      []string `json:"tailnetHosts,omitempty"`      // hosts seen as machines of the tailnet
	GUITheme          string   `json:"guiTheme,omitempty"`          // "dark" (default) or "light"
	GUISidebarWidth   float32  `json:"guiSidebarWidth,omitempty"`   // lam-gui sidebar width in dp; 0 keeps the default
}

// settingsCount is the number of rows in the settings box.
const settingsCount = 3

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
	_ = writeFileAtomic(settingsPath(), data, 0644)
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

// writeFileAtomic replaces path with data through a temporary file and a
// rename, so a crash or a second lam writing at the same time never leaves a
// half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
