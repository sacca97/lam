package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// confirmDialog is a centered two-button prompt drawn like the help box. The
// first button is the default.
type confirmDialog struct {
	title   string
	body    []string
	buttons [2]string
	hint    string
}

func (d confirmDialog) overlay(cols, rows, selected int) string {
	if cols < 20 || rows < 8 {
		return ""
	}
	buttonRow := func(width int) string {
		render := func(label string, on bool) string {
			label = " " + label + " "
			if on {
				return "\033[48;5;108m\033[38;5;235m" + label + "\033[48;5;235m"
			}
			return "\033[48;5;238m\033[38;5;250m" + label + "\033[48;5;235m"
		}
		first, second := render(d.buttons[0], selected == 0), render(d.buttons[1], selected == 1)
		used := ansi.StringWidth(d.buttons[0]) + ansi.StringWidth(d.buttons[1]) + 4 + 2
		pad := max(0, (width-used)/2)
		return spaces(pad) + first + "  " + second
	}

	want := ansi.StringWidth(d.title) + ansi.StringWidth(d.hint) + 4
	for _, line := range d.body {
		want = max(want, ansi.StringWidth(line)+2)
	}
	want = max(want, ansi.StringWidth(d.buttons[0])+ansi.StringWidth(d.buttons[1])+8)
	boxWidth := min(max(want+2, 36), cols-4)
	innerWidth := boxWidth - 2
	boxHeight := len(d.body) + 7
	startRow := max(1, (rows-boxHeight)/2+1)
	startCol := max(1, (cols-boxWidth)/2+1)

	border := "\033[48;5;235m\033[38;5;108m"
	text := "\033[38;5;250m"
	var out strings.Builder
	writeRow := func(row int, content string) {
		// The content may switch background colors, so reset to the box's
		// own before padding to the border.
		pad := max(0, innerWidth-ansi.StringWidth(content))
		out.WriteString(fmt.Sprintf("\033[%d;%dH%s│%s%s%s%s│\033[0m", row, startCol, border, text, content, spaces(pad), border))
	}
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s┌%s┐\033[0m", startRow, startCol, border, strings.Repeat("─", innerWidth)))
	title := " " + d.title
	hint := d.hint + " "
	gap := max(1, innerWidth-ansi.StringWidth(title)-ansi.StringWidth(hint))
	writeRow(startRow+1, fitCell(title+spaces(gap)+hint, innerWidth))
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s├%s┤\033[0m", startRow+2, startCol, border, strings.Repeat("─", innerWidth)))
	writeRow(startRow+3, "")
	for i, line := range d.body {
		writeRow(startRow+4+i, fitCell(" "+line, innerWidth))
	}
	writeRow(startRow+4+len(d.body), "")
	writeRow(startRow+5+len(d.body), buttonRow(innerWidth))
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s└%s┘\033[0m", startRow+6+len(d.body), startCol, border, strings.Repeat("─", innerWidth)))
	return out.String()
}

func restoreDialogBody(entries []manifestEntry) []string {
	body := []string{fmt.Sprintf("%d previous session(s) ended without being stopped:", len(entries))}
	const shown = 6
	for i, e := range entries {
		if i == shown {
			body = append(body, fmt.Sprintf("  … and %d more", len(entries)-shown))
			break
		}
		body = append(body, "  "+e.Name)
	}
	return append(body, "Reopen them and resume their agents?")
}

// settingsOverlay draws the settings box: one row per setting, the chosen
// row's value highlighted.
func settingsOverlay(cols, rows int, s uiSettings, row int) string {
	if cols < 40 || rows < 9 {
		return ""
	}
	placement := "Below"
	if s.TerminalPlacement == terminalRight {
		placement = "Right"
	}
	items := [][2]string{
		{"Terminal position", placement},
		{"Terminal size", fmt.Sprintf("%d%%", s.TerminalPercent)},
	}
	const title, hint = " Settings", "↑/↓ choose · ←/→ change · Esc close "
	boxWidth := min(cols-4, max(ansi.StringWidth(title)+ansi.StringWidth(hint)+6, 50))
	innerWidth := boxWidth - 2
	boxHeight := len(items) + 5
	startRow := max(1, (rows-boxHeight)/2+1)
	startCol := max(1, (cols-boxWidth)/2+1)

	border := "\033[48;5;235m\033[38;5;108m"
	text := "\033[38;5;250m"
	var out strings.Builder
	writeRow := func(r int, content string) {
		pad := max(0, innerWidth-ansi.StringWidth(content))
		out.WriteString(fmt.Sprintf("\033[%d;%dH%s│%s%s%s%s│\033[0m", r, startCol, border, text, content, spaces(pad), border))
	}
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s┌%s┐\033[0m", startRow, startCol, border, strings.Repeat("─", innerWidth)))
	gap := max(1, innerWidth-ansi.StringWidth(title)-ansi.StringWidth(hint))
	writeRow(startRow+1, fitCell(title+spaces(gap)+hint, innerWidth))
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s├%s┤\033[0m", startRow+2, startCol, border, strings.Repeat("─", innerWidth)))
	for i, item := range items {
		value := " ‹ " + item[1] + " › "
		if i == row {
			value = "\033[48;5;108m\033[38;5;235m" + value + "\033[48;5;235m" + text
		}
		label := fitCell(" "+item[0], 24)
		writeRow(startRow+3+i, label+value)
	}
	out.WriteString(fmt.Sprintf("\033[%d;%dH%s└%s┘\033[0m", startRow+3+len(items), startCol, border, strings.Repeat("─", innerWidth)))
	return out.String()
}

// quitAllBody lists what quitting with Ctrl+Q will terminate.
func quitAllBody(sessions []sessionInfo, remote bool) []string {
	where := ""
	if remote {
		where = " on the remote host"
	}
	body := []string{fmt.Sprintf("Stop %d session(s)%s and quit?", len(sessions), where)}
	for i, s := range sessions {
		if i == 4 && len(sessions) > 5 {
			body = append(body, fmt.Sprintf("… and %d more", len(sessions)-4))
			break
		}
		body = append(body, "  "+s.Name)
	}
	return append(body, "Their running programs will be terminated.")
}
