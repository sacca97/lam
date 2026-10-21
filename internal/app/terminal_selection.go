package app

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

type terminalSelectionPoint struct {
	X int
	Y int
}

// terminalSelectionPoint rows count from the oldest line of scrollback, so a
// selection keeps its place while the view scrolls under a drag.
type terminalTextSelection struct {
	pane     *embedded
	terminal bool // the integrated terminal rather than the session pane
	start    terminalSelectionPoint
	end      terminalSelectionPoint
	dragging bool
	active   bool
}

var terminalSelectionBackground = color.RGBA{R: 77, G: 105, B: 122, A: 255}

func shiftLeftMousePress(event terminalMouseEvent) bool {
	return !event.release && event.button&4 != 0 && event.button&64 == 0 && event.button&32 == 0 && event.button&3 == 0
}

// plainLeftPress is an unmodified left-button press, which selects text in
// panes whose program is not tracking the mouse.
func plainLeftPress(event terminalMouseEvent) bool {
	return !event.release && event.button&(3|4|8|16|32|64) == 0
}

func (e *embedded) renderViewportWithSelection(selection *terminalTextSelection) string {
	e.terminalMu.RLock()
	defer e.terminalMu.RUnlock()

	selected := selection != nil && selection.active && selection.pane == e
	alt := e.em.IsAltScreen()
	if !selected && (e.scrollback <= 0 || alt) {
		return e.em.Render()
	}

	width, height := e.em.Width(), e.em.Height()
	scrollbackLines := e.em.ScrollbackLen()
	offset := 0
	if !alt {
		offset = min(e.scrollback, scrollbackLines)
	}
	start := scrollbackLines - offset

	// Draw the whole viewport the fast way. Reading cells one by one is far
	// slower, so only the rows a selection touches are redrawn from cells.
	var lines []string
	if offset == 0 {
		lines = make([]string, height)
		copy(lines, e.em.RenderLines())
	} else {
		lines = e.em.RenderRows(start, height)
	}
	if selected {
		first, last := normalizedTerminalSelection(selection.start, selection.end)
		for y := max(0, first.Y-start); y <= min(height-1, last.Y-start); y++ {
			lines[y] = e.renderRowFromCells(y, start, width, scrollbackLines, selection)
		}
	}
	return strings.Join(lines, "\n")
}

// renderRowFromCells draws one viewport row cell by cell, highlighting the
// selected cells.
func (e *embedded) renderRowFromCells(y, start, width, scrollbackLines int, selection *terminalTextSelection) string {
	screen := uv.NewScreenBuffer(width, 1)
	sourceY := start + y
	for x := 0; x < width; x++ {
		var cell *uv.Cell
		if sourceY < scrollbackLines {
			cell = e.em.ScrollbackCellAt(x, sourceY)
		} else {
			cell = e.em.CellAt(x, sourceY-scrollbackLines)
		}
		inSelection := selection != nil && terminalSelectionContains(selection, x, sourceY)
		if cell == nil {
			if inSelection {
				cell = uv.EmptyCell.Clone()
				cell.Style.Bg = terminalSelectionBackground
				screen.SetCell(x, 0, cell)
			}
			continue
		}
		if cell.Width == 0 {
			if inSelection && x > 0 {
				lead := screen.CellAt(x-1, 0)
				if lead != nil && lead.Width > 1 {
					colored := lead.Clone()
					colored.Style.Bg = terminalSelectionBackground
					screen.SetCell(x-1, 0, colored)
				}
			}
			continue
		}
		if inSelection {
			cell = cell.Clone()
			if cell.IsZero() {
				cell = uv.EmptyCell.Clone()
			}
			cell.Style.Bg = terminalSelectionBackground
		}
		screen.SetCell(x, 0, cell)
	}
	return screen.Render()
}

func terminalSelectionContains(selection *terminalTextSelection, x, y int) bool {
	start, end := normalizedTerminalSelection(selection.start, selection.end)
	return (y > start.Y || y == start.Y && x >= start.X) &&
		(y < end.Y || y == end.Y && x <= end.X)
}

func normalizedTerminalSelection(start, end terminalSelectionPoint) (terminalSelectionPoint, terminalSelectionPoint) {
	if end.Y < start.Y || end.Y == start.Y && end.X < start.X {
		return end, start
	}
	return start, end
}

func terminalSelectionText(width, height int, start, end terminalSelectionPoint, cellAt func(int, int) *uv.Cell) string {
	if width < 1 || height < 1 || cellAt == nil {
		return ""
	}
	start.X = max(0, min(width-1, start.X))
	start.Y = max(0, min(height-1, start.Y))
	end.X = max(0, min(width-1, end.X))
	end.Y = max(0, min(height-1, end.Y))
	start, end = normalizedTerminalSelection(start, end)
	if cell := cellAt(start.X, start.Y); cell != nil && cell.Width == 0 && start.X > 0 {
		start.X--
	}

	lines := make([]string, 0, end.Y-start.Y+1)
	for y := start.Y; y <= end.Y; y++ {
		left, right := 0, width-1
		if y == start.Y {
			left = start.X
		}
		if y == end.Y {
			right = end.X
		}
		var line strings.Builder
		for x := left; x <= right; x++ {
			cell := cellAt(x, y)
			if cell == nil {
				line.WriteByte(' ')
			} else if cell.Width == 0 {
				continue
			} else if cell.IsZero() {
				line.WriteByte(' ')
			} else {
				line.WriteString(cell.Content)
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return strings.Join(lines, "\n")
}

func (e *embedded) selectedText(start, end terminalSelectionPoint) string {
	e.terminalMu.RLock()
	defer e.terminalMu.RUnlock()
	width, height := e.em.Width(), e.em.ScrollbackLen()+e.em.Height()
	return terminalSelectionText(width, height, start, end, e.absoluteCellAtLocked)
}

// selectionRow converts a viewport row to the row numbering selections use.
func (e *embedded) selectionRow(y int) int {
	e.terminalMu.RLock()
	defer e.terminalMu.RUnlock()
	lines := e.em.ScrollbackLen()
	return lines - min(e.scrollback, lines) + y
}

func (e *embedded) absoluteCellAtLocked(x, y int) *uv.Cell {
	lines := e.em.ScrollbackLen()
	if y < lines {
		return e.em.ScrollbackCellAt(x, y)
	}
	return e.em.CellAt(x, y-lines)
}
