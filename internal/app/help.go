package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type keyHelpBinding struct {
	key    string
	action string
}

func keyboardHelpBindings() []keyHelpBinding {
	var bindings []keyHelpBinding
	bindings = append(bindings,
		keyHelpBinding{"↑/↓", "Move selection"},
		keyHelpBinding{"Enter", "Open or focus"},
		keyHelpBinding{"1–9", "Select session"},
		keyHelpBinding{"/", "Filter list"},
		keyHelpBinding{"n", "New session"},
		keyHelpBinding{"t / Alt+T", "Toggle terminal"},
		keyHelpBinding{"s", "Settings (terminal layout)"},
		keyHelpBinding{"r", "Rename session"},
		keyHelpBinding{"k", "Stop session"},
		keyHelpBinding{"Ctrl+↑/↓", "Previous/next session"},
		keyHelpBinding{"Ctrl+D", "Detach (quit, sessions keep running)"},
		keyHelpBinding{"Ctrl+Q", "Quit and stop all sessions"},
		keyHelpBinding{"Click", "Focus pane"},
		keyHelpBinding{"Ctrl+Space", "Cycle focus"},
		keyHelpBinding{"Ctrl+\\", "Cycle focus"},
		keyHelpBinding{"Wheel", "Scroll history"},
		keyHelpBinding{"Shift+Wheel", "Send wheel to app"},
		keyHelpBinding{"Shift+drag", "Select/copy text"},
	)
	bindings = append(bindings,
		keyHelpBinding{"Ctrl+L", "Redraw screen (sidebar)"},
		keyHelpBinding{"F2", "Toggle sidebar"},
		keyHelpBinding{"? / Esc", "Close help"},
		keyHelpBinding{"q", "Quit LAM (sessions keep running)"},
	)
	return bindings
}

func keyboardHelpOverlay(cols, rows int) string {
	if cols < 12 || rows < 6 {
		return ""
	}
	bindings := keyboardHelpBindings()
	boxWidth := min(64, cols-4)
	if boxWidth < 12 {
		boxWidth = min(cols, 12)
	}
	innerWidth := boxWidth - 2
	twoColumns := boxWidth >= 64
	columnWidth := innerWidth
	if twoColumns {
		columnWidth = (innerWidth - 2) / 2
	}
	keyWidth := 0
	for _, binding := range bindings {
		keyWidth = max(keyWidth, ansi.StringWidth(binding.key))
	}
	keyWidth = min(keyWidth, min(12, columnWidth-2))
	if keyWidth < 1 {
		keyWidth = 1
	}
	formatBinding := func(binding keyHelpBinding, width int) string {
		if width < 1 {
			return ""
		}
		descriptionWidth := max(0, width-keyWidth-1)
		key := "\033[38;5;151m" + fitCell(binding.key, keyWidth) + "\033[38;5;250m"
		return key + " " + fitCell(binding.action, descriptionWidth)
	}

	var lines []string
	if twoColumns {
		for i := 0; i < len(bindings); i += 2 {
			line := formatBinding(bindings[i], columnWidth)
			if i+1 < len(bindings) {
				line += "  " + formatBinding(bindings[i+1], columnWidth)
			} else {
				line += strings.Repeat(" ", 2+columnWidth)
			}
			lines = append(lines, line)
		}
	} else {
		for _, binding := range bindings {
			lines = append(lines, formatBinding(binding, innerWidth))
		}
	}

	maxContentRows := rows - 5
	if maxContentRows < 1 {
		maxContentRows = 1
	}
	if len(lines) > maxContentRows {
		if maxContentRows == 1 {
			lines = []string{"Resize terminal to see shortcuts"}
		} else {
			lines = append(lines[:maxContentRows-1], "… resize terminal for more shortcuts")
		}
	}
	boxHeight := len(lines) + 4
	startRow := max(1, (rows-boxHeight)/2+1)
	startCol := max(1, (cols-boxWidth)/2+1)

	var out strings.Builder
	border := "\033[48;5;235m\033[38;5;108m"
	text := "\033[38;5;250m"
	writeRow := func(row int, content string) {
		out.WriteString(fmt.Sprintf("\033[%d;%dH%s│%s%s%s│\033[0m", row, startCol, border, text, fitCell(content, innerWidth), border))
	}
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s┌%s┐\033[0m", startRow, startCol, border, strings.Repeat("─", innerWidth)))

	title := " LAM shortcuts"
	closeHint := "? / Esc close "
	titleContent := title + strings.Repeat(" ", max(0, innerWidth-ansi.StringWidth(title)-ansi.StringWidth(closeHint))) + closeHint
	writeRow(startRow+1, titleContent)
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s├%s┤\033[0m", startRow+2, startCol, border, strings.Repeat("─", innerWidth)))
	for i, line := range lines {
		writeRow(startRow+3+i, line)
	}
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s└%s┘\033[0m", startRow+boxHeight-1, startCol, border, strings.Repeat("─", innerWidth)))
	return out.String()
}
