package app

import "strings"

// frameDiff sends only the screen rows that changed since the previous frame.
//
// A frame body is a series of chunks, each starting with an absolute cursor
// move (CSI row;col H) and drawing part of one screen row. Chunks are grouped
// by row. A row whose text is identical to what was sent for it last time is
// already on the screen and is skipped, so typing a character costs one row
// instead of the whole screen. That matters most over a slow link.
//
// The cache describes what the terminal shows. Whenever something else is
// drawn over it, the terminal is cleared, or a write fails, call reset.
type frameDiff struct {
	prev map[int]string
}

func (f *frameDiff) reset() { f.prev = nil }

// cursorAddressAt reports whether body[i:] starts with CSI row;col H and, if
// so, the row and the length of the sequence.
func cursorAddressAt(body string, i int) (row, n int, ok bool) {
	if i+1 >= len(body) || body[i] != 0x1b || body[i+1] != '[' {
		return 0, 0, false
	}
	j := i + 2
	start := j
	for j < len(body) && body[j] >= '0' && body[j] <= '9' {
		row = row*10 + int(body[j]-'0')
		j++
	}
	if j == start || j >= len(body) || body[j] != ';' {
		return 0, 0, false
	}
	j++
	start = j
	for j < len(body) && body[j] >= '0' && body[j] <= '9' {
		j++
	}
	if j == start || j >= len(body) || body[j] != 'H' {
		return 0, 0, false
	}
	return row, j + 1 - i, true
}

func (f *frameDiff) diff(body string) string {
	type chunk struct{ row, start int }
	chunks := make([]chunk, 0, 128)
	for i := 0; ; {
		j := strings.Index(body[i:], "\x1b[")
		if j < 0 {
			break
		}
		i += j
		if row, n, ok := cursorAddressAt(body, i); ok {
			chunks = append(chunks, chunk{row, i})
			i += n
		} else {
			i += 2
		}
	}
	if len(chunks) == 0 {
		return body
	}

	var out strings.Builder
	out.Grow(len(body))
	out.WriteString(body[:chunks[0].start])
	order := make([]int, 0, len(chunks))
	next := make(map[int]string, len(chunks))
	for i, c := range chunks {
		end := len(body)
		if i+1 < len(chunks) {
			end = chunks[i+1].start
		}
		text := body[c.start:end]
		if existing, ok := next[c.row]; ok {
			next[c.row] = existing + text // a row drawn in several pieces
		} else {
			next[c.row] = text
			order = append(order, c.row)
		}
	}
	for _, row := range order {
		if f.prev != nil && f.prev[row] == next[row] {
			continue
		}
		out.WriteString(next[row])
	}
	f.prev = next
	return out.String()
}
