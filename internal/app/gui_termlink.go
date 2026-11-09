//go:build gui

package app

import (
	"path"
	"regexp"
	"strings"
)

// Ctrl+click (⌘+click on a Mac) on a web address in a terminal opens the
// browser, and on a markdown or PDF path the document viewer, from the machine
// the session runs on. With the key held, the link under the pointer is
// underlined.

var pathLineSuffix = regexp.MustCompile(`:\d+(:\d+)?$`)

// termLink is a link found in a row: what it names and the columns it covers.
type termLink struct {
	tok    string
	lo, hi int
}

// isWebLink is whether tok is an address for the browser.
func isWebLink(tok string) bool {
	for _, p := range []string{"http://", "https://", "mailto:"} {
		if len(tok) > len(p) && strings.EqualFold(tok[:len(p)], p) {
			return true
		}
	}
	return false
}

// linkAt is the link under column x of a terminal row, one string per column
// (empty for blank cells): a web address, or a markdown or PDF path with the
// punctuation a sentence puts around it taken off.
func linkAt(cols []string, x int) (termLink, bool) {
	if x < 0 || x >= len(cols) {
		return termLink{}, false
	}
	// The word around x, up to a blank and, in the strict form, brackets and
	// quotes too. An address may hold brackets, a path in a sentence is
	// usually wrapped in them.
	word := func(strict bool) (lo, hi int, ok bool) {
		blank := func(i int) bool {
			c := cols[i]
			if c == "\x00" {
				return false
			}
			if c == "" || strings.TrimSpace(c) == "" || strings.ContainsAny(c, "\"'<>|`") {
				return true
			}
			return strict && strings.ContainsAny(c, "()[]{}")
		}
		if blank(x) {
			return 0, 0, false
		}
		lo, hi = x, x
		for lo > 0 && !blank(lo-1) {
			lo--
		}
		for hi+1 < len(cols) && !blank(hi+1) {
			hi++
		}
		return lo, hi, true
	}
	text := func(lo, hi int) string {
		return strings.ReplaceAll(strings.Join(cols[lo:hi+1], ""), "\x00", "")
	}
	if lo, hi, ok := word(false); ok && isWebLink(text(lo, hi)) {
		for hi > lo && (strings.ContainsAny(cols[hi], ".,;:!?") || cols[hi] == ")" && strings.Count(text(lo, hi), ")") > strings.Count(text(lo, hi), "(") ||
			cols[hi] == "]" && strings.Count(text(lo, hi), "]") > strings.Count(text(lo, hi), "[")) {
			hi--
		}
		if x <= hi {
			return termLink{text(lo, hi), lo, hi}, true
		}
		return termLink{}, false
	}
	lo, hi, ok := word(true)
	if !ok {
		return termLink{}, false
	}
	for hi > lo && strings.ContainsAny(cols[hi], ".,;:!?") {
		hi--
	}
	if x > hi {
		return termLink{}, false
	}
	tok := text(lo, hi)
	tok = strings.TrimPrefix(tok, "@") // Claude Code's @file mentions
	tok = strings.TrimPrefix(tok, "file://")
	tok = pathLineSuffix.ReplaceAllString(tok, "")
	if tok == "" || documentKind(tok) == docNone {
		return termLink{}, false
	}
	return termLink{tok, lo, hi}, true
}

// rowColumns is the text of a laid-out row, one entry per column.
func rowColumns(line *termLine) []string {
	cols := make([]string, len(line.cells))
	for i, c := range line.cells {
		if c.skip {
			cols[i] = "\x00" // the second half of a wide character
		} else {
			cols[i] = c.text
		}
	}
	return cols
}

// linkAtCell is the link under a cell of the view, if there is one.
func (v *termView) linkAtCell(cell, row int) (termLink, bool) {
	if row+1 < 0 || row+1 >= len(v.lines) {
		return termLink{}, false
	}
	return linkAt(rowColumns(&v.lines[row+1]), cell)
}

// openTerminalLink opens what was clicked in a terminal: a web address in the
// browser, a path in the viewer. Relative paths start in the session's folder
// and the file is read where the session runs.
func (g *guiApp) openTerminalLink(tok string) bool {
	if isWebLink(tok) {
		openInBrowser(tok)
		return true
	}
	s, ok := g.selectedInfo()
	if !ok {
		return false
	}
	home := ""
	if tok == "~" || strings.HasPrefix(tok, "~/") {
		src := g.sourceFor(s.Host)
		if src == nil {
			return false
		}
		var err error
		if home, err = src.HomeDirectory(); err != nil || home == "" {
			g.say("Cannot find the home folder on that machine")
			return true
		}
	}
	if home != "" || strings.HasPrefix(tok, "/") {
		g.openDocument(s.Host, absoluteDocPath(tok, s.CWD, home))
		return true
	}
	// A relative path is not always relative to the folder the session started
	// in (an agent names a file in a subfolder, or from where it has moved to):
	// look for it, off the UI goroutine, as the machine may be far away.
	src := g.documentSourceFor(s.Host)
	if src == nil {
		g.openDocument(s.Host, absoluteDocPath(tok, s.CWD, ""))
		return true
	}
	host, recorded, name := s.Host, s.CWD, s.Name
	g.say("Looking for %s…", path.Base(tok))
	go func() {
		var bases []string
		if live, err := sessionProcessWorkingDirectory(name); err == nil && live != "" {
			bases = append(bases, live)
		}
		if recorded != "" && (len(bases) == 0 || bases[0] != recorded) {
			bases = append(bases, recorded)
		}
		found, ok := findDocument(src, bases, tok)
		g.onUI(func() {
			if !ok {
				g.say("Cannot find %s in %s or below it", tok, shortenHomePath(recorded, g.sourceFor(host)))
				return
			}
			g.openDocument(host, found)
		})
	}()
	return true
}
