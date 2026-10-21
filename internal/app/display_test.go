package app

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPreserveSessionSelectionAcrossSortedRefresh(t *testing.T) {
	previous := []sessionInfo{
		{Name: "alpha", Identity: "alpha\x001"},
		{Name: "beta", Identity: "beta\x002"},
	}
	next := []sessionInfo{
		{Name: "aardvark", Identity: "aardvark\x003"},
		previous[0],
		previous[1],
	}
	if got := preserveSessionSelection(previous, 1, next); got != 2 {
		t.Fatalf("selection index = %d, want beta at index 2", got)
	}
}

func TestPreserveSessionSelectionClearsRemovedOrReplacedTarget(t *testing.T) {
	previous := []sessionInfo{{Name: "agent", Identity: "agent\x001"}, {Name: "other", Identity: "other\x002"}}
	next := []sessionInfo{{Name: "agent", Identity: "agent\x099"}, previous[1]}
	if got := preserveSessionSelection(previous, 0, next); got != -1 {
		t.Fatalf("replaced target selection = %d, want no selection", got)
	}
	if got := preserveSessionSelection(previous, 0, []sessionInfo{previous[1]}); got != -1 {
		t.Fatalf("removed target selection = %d, want no selection", got)
	}
}

func TestPreserveSessionSelectionHandlesEmptyLists(t *testing.T) {
	if got := preserveSessionSelection(nil, 0, []sessionInfo{{Name: "new"}}); got != 0 {
		t.Fatalf("initial selection = %d, want 0", got)
	}
	if got := preserveSessionSelection([]sessionInfo{{Name: "gone"}}, 0, nil); got != -1 {
		t.Fatalf("empty selection = %d, want -1", got)
	}
}

func TestUnfilteredSessionListIncludesEveryWorkingDirectory(t *testing.T) {
	sessions := []sessionInfo{
		{Name: "project-a", CWD: "/work/project-a"},
		{Name: "project-b", CWD: "/work/project-b"},
	}
	got := filteredSessions(sessions, "")
	if len(got) != len(sessions) {
		t.Fatalf("unfiltered list has %d sessions, want %d", len(got), len(sessions))
	}
	for i := range sessions {
		if got[i].Name != sessions[i].Name || got[i].CWD != sessions[i].CWD {
			t.Fatalf("unfiltered session %d = %#v, want %#v", i, got[i], sessions[i])
		}
	}
}

func TestFitCellUsesDisplayWidthAndPreservesANSI(t *testing.T) {
	got := fitCell("\033[31m界a\033[0m", 4)
	if width := ansiWidth(got); width != 4 {
		t.Fatalf("fitted width = %d, want 4 (%q)", width, got)
	}
	if got == "" || got[0] != '\033' {
		t.Fatalf("ANSI prefix was not preserved: %q", got)
	}
}

func TestListViewportKeepsSelectionVisible(t *testing.T) {
	start, end := listViewport(20, 10, 5)
	if start != 6 || end != 11 {
		t.Fatalf("viewport = [%d,%d), want [6,11)", start, end)
	}
}

func TestSplitPaneHeightsUsesThirtyPercentTerminal(t *testing.T) {
	main, terminal := splitPaneHeights(21, true)
	if main != 14 || terminal != 6 {
		t.Fatalf("split = main %d, terminal %d; want 14 and 6 rows", main, terminal)
	}
	if main+terminal+1 != 21 {
		t.Fatalf("split uses %d rows, want 21 including divider", main+terminal+1)
	}
	if main, terminal := splitPaneHeights(21, false); main != 21 || terminal != 0 {
		t.Fatalf("unsplit = main %d, terminal %d; want 21 and 0", main, terminal)
	}
}

func ansiWidth(s string) int {
	return ansi.StringWidth(s)
}

func TestPaneGeometry(t *testing.T) {
	g := paneGeometryFor(100, 30, true, terminalRight, 40)
	if !g.right || g.termW != 40 || g.mainW != 59 || g.mainH != 30 || g.termH != 30 {
		t.Fatalf("right 40%% = %#v", g)
	}
	// Narrow windows fall back to the block below.
	if g := paneGeometryFor(60, 30, true, terminalRight, 50); g.right || g.termH == 0 || g.mainW != 60 || g.termW != 60 {
		t.Fatalf("narrow fallback = %#v", g)
	}
	below := paneGeometryFor(100, 30, true, terminalBelow, 40)
	if below.right || below.mainH+below.termH+1 != 30 || below.termW != 100 {
		t.Fatalf("below = %#v", below)
	}
	if g := paneGeometryFor(100, 30, false, terminalRight, 40); g.termH != 0 || g.mainW != 100 || g.mainH != 30 {
		t.Fatalf("hidden = %#v", g)
	}
	if got := cyclePercent(50, 1); got != 30 {
		t.Fatalf("cyclePercent wraps to %d", got)
	}
}

func TestSidebarRowsGroupSessionsByProject(t *testing.T) {
	sessions := []sessionInfo{
		{Name: "claude-a", CWD: "/w/alpha"},
		{Name: "opencode-a", CWD: "/w/alpha"},
		{Name: "claude-b", CWD: "/w/beta"},
	}
	sort.Slice(sessions, func(i, j int) bool { return sessionLess(sessions[i], sessions[j]) })
	rows := buildSidebarRows(sessions, false, nil, 0)
	var got []string
	for _, r := range rows {
		if r.header {
			got = append(got, fmt.Sprintf("%s(%d)", r.label, r.count))
		} else {
			got = append(got, sessions[r.session].Name)
		}
	}
	want := "alpha(2) claude-a opencode-a beta(1) claude-b"
	if strings.Join(got, " ") != want {
		t.Fatalf("rows = %q, want %q", strings.Join(got, " "), want)
	}
	if rows := buildSidebarRows(sessions, true, nil, 0); len(rows) != 3 || rows[0].header {
		t.Fatalf("collapsed sidebar should list sessions only: %+v", rows)
	}
}

func TestSidebarViewportKeepsSelectionAndItsHeadingVisible(t *testing.T) {
	var sessions []sessionInfo
	for _, p := range []string{"a", "b", "c", "d"} {
		sessions = append(sessions, sessionInfo{Name: p + "1", CWD: "/w/" + p}, sessionInfo{Name: p + "2", CWD: "/w/" + p})
	}
	rows := buildSidebarRows(sessions, false, nil, 0)
	for cursor := range sessions {
		start, end := sidebarViewport(rows, cursor, 9)
		lines, seen := 0, false
		for _, r := range rows[start:end] {
			lines += r.height
			seen = seen || (!r.header && r.session == cursor)
		}
		if lines > 9 || !seen {
			t.Fatalf("cursor %d: rows %d-%d use %d lines, selection visible=%v", cursor, start, end, lines, seen)
		}
		if cursor%2 == 0 && !rows[start:end][0].header && start > 0 && rows[start-1].header && rows[start].session == cursor {
			t.Fatalf("cursor %d: heading scrolled away", cursor)
		}
	}
}

func TestSessionLabelDropsTheProjectName(t *testing.T) {
	cases := []struct{ name, cwd, want string }{
		{"claude-api", "/home/u/api", "claude"},
		{"opencode-api-2", "/home/u/api", "opencode 2"},
		{"shell-sacca", "/home/sacca", "shell"},
		{"my-review", "/home/u/api", "my-review"},
		{"claude-x", "", "claude-x"},
	}
	for _, c := range cases {
		if got := sessionLabel(sessionInfo{Name: c.name, CWD: c.cwd}); got != c.want {
			t.Errorf("%s in %s: %q, want %q", c.name, c.cwd, got, c.want)
		}
	}
}

func TestCollapsedProjectShowsOnlyItsSelectedSession(t *testing.T) {
	sessions := []sessionInfo{
		{Name: "claude-a", CWD: "/w/a"}, {Name: "opencode-a", CWD: "/w/a"}, {Name: "claude-b", CWD: "/w/b"},
	}
	folded := map[string]bool{"/w/a": true}
	if rows := buildSidebarRows(sessions, false, folded, 2); len(rows) != 3 || !rows[0].collapsed || rows[1].header != true {
		t.Fatalf("unselected: %+v", rows)
	}
	if rows := buildSidebarRows(sessions, false, folded, 1); len(rows) != 4 || rows[1].session != 1 {
		t.Fatalf("selected inside: %+v", rows)
	}
	if got := nextSession(sessions, 0, 1, folded); got != 2 {
		t.Fatalf("down from a collapsed project = %d, want 2", got)
	}
	if got := nextSession(sessions, 2, -1, folded); got != 1 {
		t.Fatalf("up into a collapsed project = %d, want 1", got)
	}
}
