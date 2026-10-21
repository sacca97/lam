package app

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func sessionActivity(s sessionInfo) (active, known bool) {
	switch s.Activity {
	case "quiet":
		return false, true
	case "active":
		return true, true
	default:
		return false, false
	}
}

func activityMarker(s sessionInfo) string {
	active, known := sessionActivity(s)
	if !known {
		return "\033[38;5;240m?\033[0m"
	}
	if active {
		return "\033[38;5;108m●\033[0m"
	}
	return "\033[38;5;240m○\033[0m"
}

// splitPaneHeights keeps an optional integrated terminal near 30% of the
// available content height while leaving at least two rows for each pane.
// One additional row is reserved for the divider.
func splitPaneHeights(contentRows int, terminalVisible bool) (mainRows, terminalRows int) {
	return splitPaneHeightsPct(contentRows, terminalVisible, 30)
}

func splitPaneHeightsPct(contentRows int, terminalVisible bool, percent int) (mainRows, terminalRows int) {
	if contentRows <= 0 {
		return 0, 0
	}
	if !terminalVisible || contentRows < 5 {
		return contentRows, 0
	}
	terminalRows = max(2, contentRows*percent/100)
	terminalRows = min(terminalRows, contentRows-3)
	mainRows = contentRows - terminalRows - 1
	return mainRows, terminalRows
}

// paneGeometry is the size of the session pane and, when visible, the
// integrated terminal. With right set the terminal is a column beside the
// session pane (one column is the divider); otherwise it is a block below it
// (one row is the divider). termW and termH are zero while it is hidden.
type paneGeometry struct {
	right        bool
	mainW, mainH int
	termW, termH int
}

const (
	minSideBySideMainWidth = 40
	minSideBySideTermWidth = 20
)

func paneGeometryFor(totalW, contentRows int, terminalVisible bool, placement string, percent int) paneGeometry {
	g := paneGeometry{mainW: max(1, totalW), mainH: max(1, contentRows)}
	if !terminalVisible || contentRows <= 0 {
		return g
	}
	if placement == terminalRight {
		termW := max(minSideBySideTermWidth, totalW*percent/100)
		// Too narrow for two usable panes: fall back to the block below.
		if mainW := totalW - termW - 1; mainW >= minSideBySideMainWidth {
			return paneGeometry{right: true, mainW: mainW, mainH: contentRows, termW: termW, termH: contentRows}
		}
	}
	mainH, termH := splitPaneHeightsPct(contentRows, true, percent)
	g.mainH, g.termH, g.termW = max(1, mainH), termH, g.mainW
	return g
}

// fitCell truncates terminal text by display cells, preserving complete ANSI
// sequences and grapheme clusters, then pads it to exactly width cells.
func fitCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	if n := width - ansi.StringWidth(s); n > 0 {
		s += spaces(n)
	}
	return s
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// sessionIdentity includes a process identity when available, so a session
// removed and recreated under the same name does not inherit an old selection.
func sessionIdentity(s sessionInfo) string {
	if s.Identity != "" {
		return s.Identity
	}
	return s.Name + "\x00" + s.PanePID
}

func filteredSessions(sessions []sessionInfo, query string) []sessionInfo {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return sessions
	}
	var out []sessionInfo
	for _, s := range sessions {
		if strings.Contains(strings.ToLower(s.Name), query) || strings.Contains(strings.ToLower(s.CWD), query) {
			out = append(out, s)
		}
	}
	return out
}

// preserveSessionSelection keeps the same session selected across sorted list
// refreshes. If that session vanished or was replaced, it clears selection.
func preserveSessionSelection(previous []sessionInfo, selected int, next []sessionInfo) int {
	if len(next) == 0 {
		return -1
	}
	if selected >= 0 && selected < len(previous) {
		want := sessionIdentity(previous[selected])
		for i := range next {
			if sessionIdentity(next[i]) == want {
				return i
			}
		}
		// The selected process disappeared or was replaced under the same name.
		// Leave selection empty until the user chooses another row.
		return -1
	}
	if len(previous) == 0 {
		return 0
	}
	return -1
}

func preferredSessionIndex(sessions []sessionInfo, name string) int {
	if len(sessions) == 0 {
		return -1
	}
	for i := range sessions {
		if sessions[i].Name == name {
			return i
		}
	}
	return 0
}

func sidebarWidthFor(cols int) int {
	if cols <= 0 {
		return sidebarWidth
	}
	w := sidebarWidth
	if cols < 54 {
		w = cols / 3
		if cols >= 18 && w < 12 {
			w = 12
		}
	}
	if w > cols-3 {
		w = cols - 3
	}
	if w < 1 {
		w = 1
	}
	return w
}

func compactSidebarWidthFor(cols int) int {
	const compactWidth = 10
	if cols <= 0 {
		return compactWidth
	}
	w := compactWidth
	if w > cols-3 {
		w = cols - 3
	}
	if w < 1 {
		w = 1
	}
	return w
}

func listViewport(total, cursor, available int) (int, int) {
	if total < 1 || available < 1 {
		return 0, 0
	}
	start := 0
	if cursor >= available {
		start = cursor - available + 1
	}
	end := start + available
	if end > total {
		end = total
		start = end - available
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

// sidebarRow is one entry of the session list: a project heading or a session.
type sidebarRow struct {
	header    bool
	label     string // project name, for a heading
	cwd       string // the folder a heading stands for
	count     int    // sessions under a heading
	collapsed bool   // a heading whose sessions are hidden
	session   int    // index into the visible sessions, for a session
	height    int    // screen lines
}

// sessionProject names the folder a session belongs to.
func sessionProject(s sessionInfo) string {
	if s.CWD == "" {
		return "other"
	}
	if base := filepath.Base(s.CWD); base != "." && base != string(filepath.Separator) {
		return base
	}
	return s.CWD
}

// sessionLabel is what a session is called under its project heading: the
// generated name is kind-project, with -N added when there are several, so the
// project part is redundant there. A name that does not follow that pattern,
// such as a renamed session, is shown whole.
func sessionLabel(s sessionInfo) string {
	base := sessionNameSlug(filepath.Base(filepath.Clean(s.CWD)))
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
	}
	if base == "" || s.CWD == "" {
		return s.Name
	}
	at := strings.Index(s.Name, "-"+base)
	if at <= 0 {
		return s.Name
	}
	kind, rest := s.Name[:at], s.Name[at+1+len(base):]
	switch {
	case rest == "":
		return kind
	case rest[0] == '-' && len(rest) > 1 && strings.Trim(rest[1:], "0123456789") == "":
		return kind + " " + rest[1:]
	}
	return s.Name
}

// sessionLess orders sessions by project, then name, so those in one folder
// are next to each other.
func sessionLess(a, b sessionInfo) bool {
	if pa, pb := sessionProject(a), sessionProject(b); pa != pb {
		return pa < pb
	}
	if a.CWD != b.CWD {
		return a.CWD < b.CWD
	}
	return a.Name < b.Name
}

// buildSidebarRows lays the sessions out under their project headings, one
// line each. The sessions must already be sorted so that each project is
// contiguous. A collapsed project shows its heading and, if it holds it, the
// selected session. A collapsed sidebar has no room for headings and shows one
// line per session.
func buildSidebarRows(sessions []sessionInfo, narrow bool, collapsed map[string]bool, selected int) []sidebarRow {
	rows := make([]sidebarRow, 0, len(sessions)*2)
	hidden := ""
	for i := 0; i < len(sessions); i++ {
		if !narrow && (i == 0 || sessions[i-1].CWD != sessions[i].CWD) {
			count := 1
			for j := i + 1; j < len(sessions) && sessions[j].CWD == sessions[i].CWD; j++ {
				count++
			}
			folded := collapsed[sessions[i].CWD]
			hidden = ""
			if folded {
				hidden = sessions[i].CWD
			}
			rows = append(rows, sidebarRow{header: true, label: sessionProject(sessions[i]), cwd: sessions[i].CWD, count: count, collapsed: folded, height: 1})
		}
		if hidden != "" && hidden == sessions[i].CWD && i != selected {
			continue
		}
		rows = append(rows, sidebarRow{session: i, height: 1})
	}
	return rows
}

// nextSession is the session an arrow key reaches from the current one. A
// collapsed project is a single stop, so the keys do not wander through
// sessions that are not shown.
func nextSession(sessions []sessionInfo, cursor, step int, collapsed map[string]bool) int {
	if cursor < 0 || cursor >= len(sessions) {
		return cursor + step
	}
	here := sessions[cursor].CWD
	next := cursor + step
	for next >= 0 && next < len(sessions) && collapsed[here] && sessions[next].CWD == here {
		next += step
	}
	return next
}

// sidebarViewport picks the rows to show in the given number of lines so that
// the selected session, and its heading when it is the first of its project,
// are visible. It returns the half-open row range.
func sidebarViewport(rows []sidebarRow, cursor, lines int) (int, int) {
	if len(rows) == 0 || lines < 1 {
		return 0, 0
	}
	target, top := -1, -1 // the selected row, and the first row to keep with it
	for i, r := range rows {
		if !r.header && r.session == cursor {
			target, top = i, i
			if i > 0 && rows[i-1].header {
				top = i - 1
			}
			break
		}
	}
	span := func(from, to int) int {
		n := 0
		for _, r := range rows[from:to] {
			n += r.height
		}
		return n
	}
	start := 0
	if target >= 0 {
		for start < top && span(start, target+1) > lines {
			start++
		}
	}
	end := start
	for end < len(rows) && span(start, end+1) <= lines {
		end++
	}
	for start > 0 && span(start-1, end) <= lines { // no blank space below the last row
		start--
	}
	return start, end
}
