package app

import (
	"bytes"
	"image/color"
	"strconv"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	gh "go.mitchellh.com/libghostty"
)

// termEmu is the terminal emulator behind each pane the UI shows. It wraps
// libghostty, the same engine the session hosts use, and exposes the handful
// of operations the interface needs. Scrolling the screen is orders of
// magnitude cheaper than in the pure-Go emulator it replaces, which let a
// program that floods the screen pin a CPU core.
//
// All methods are safe for concurrent use.
type termEmu struct {
	mu   sync.Mutex
	term *gh.Terminal
	enc  *gh.MouseEncoder

	cols, rows int
	modes      map[ansi.Mode]bool
	cb         termCallbacks
	tail       []byte // end of the previous write, for sequences split across reads
}

type termCallbacks struct {
	// WritePty receives what the interface itself sends to the program:
	// encoded mouse events. Terminal queries (cursor position, device
	// attributes, colors) are deliberately not answered here: this emulator
	// only mirrors a stream the session host already answers, so a second
	// answer would reach the program as stray input.
	WritePty func([]byte)
	// Pwd is called with the working directory the program reported (OSC 7).
	Pwd func(uri string)
	// ClipboardWrite is called when the program sets the clipboard (OSC 52).
	ClipboardWrite func(text string)
	// ModeChanged is called when one of the tracked modes changes.
	ModeChanged func(mode ansi.Mode, enabled bool)
}

const (
	// Scrollback is bounded by bytes, never by a line count: libghostty prunes
	// by lines about five times slower than by bytes, which made screens that
	// scroll fast (build logs, cat) cost a core. 4 MiB holds about 2000 rows of
	// a 200-column terminal.
	uiScrollbackBytes = 4 << 20
	cellPixelsX       = 8
	cellPixelsY       = 16
)

// trackedModes are the modes the UI needs to know about.
var trackedModes = []ansi.Mode{
	ansi.ModeMouseX10,
	ansi.ModeMouseNormal,
	ansi.ModeMouseHighlight,
	ansi.ModeMouseButtonEvent,
	ansi.ModeMouseAnyEvent,
	ansi.ModeBracketedPaste,
}

func newTermEmu(cols, rows int, cb termCallbacks) (*termEmu, error) {
	e := &termEmu{cols: cols, rows: rows, cb: cb, modes: make(map[ansi.Mode]bool)}
	opts := []gh.TerminalOption{
		gh.WithSize(uint16(cols), uint16(rows)),
		gh.WithMaxScrollbackBytes(uiScrollbackBytes),
		// Measure emoji sequences and variation selectors as one wide glyph, as
		// the terminal that displays them does. Without it cell widths disagree
		// with the layout and text lands in the wrong column.
		gh.WithModeDefault(gh.ModeGraphemeCluster, true),
		gh.WithPwdChanged(func(t *gh.Terminal) {
			if cb.Pwd == nil {
				return
			}
			if uri, err := t.Pwd(); err == nil && uri != "" {
				cb.Pwd(uri)
			}
		}),
		gh.WithClipboardWrite(func(_ *gh.Terminal, w gh.ClipboardWrite) gh.ClipboardWriteReply {
			if cb.ClipboardWrite == nil {
				return gh.ClipboardWriteReply{Result: gh.ClipboardWriteUnsupported}
			}
			for _, content := range w.Contents {
				if content.MIME == "text/plain" || content.MIME == "text/plain;charset=utf-8" {
					cb.ClipboardWrite(string(content.Data))
					return gh.ClipboardWriteReply{Result: gh.ClipboardWriteSuccess}
				}
			}
			return gh.ClipboardWriteReply{Result: gh.ClipboardWriteUnsupported}
		}),
	}
	term, err := gh.NewTerminal(opts...)
	if err != nil {
		return nil, err
	}
	enc, err := gh.NewMouseEncoder()
	if err != nil {
		term.Close()
		return nil, err
	}
	e.term, e.enc = term, enc
	e.setEncoderSize()
	return e, nil
}

func (e *termEmu) setEncoderSize() {
	e.enc.SetOptSize(gh.MouseEncoderSize{
		ScreenWidth:  uint32(e.cols * cellPixelsX),
		ScreenHeight: uint32(e.rows * cellPixelsY),
		CellWidth:    cellPixelsX,
		CellHeight:   cellPixelsY,
	})
}

// Write feeds program output to the terminal.
func (e *termEmu) Write(p []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return
	}
	e.term.VTWrite(p)
	if e.mayChangeModes(p) {
		e.syncModesLocked()
	}
}

// mayChangeModes reports whether the written bytes can have changed a mode:
// a private-mode sequence (CSI ?), a soft reset (CSI ! p) or a full reset
// (ESC c). Checking first keeps the common case, plain output, cheap.
func (e *termEmu) mayChangeModes(p []byte) bool {
	seqs := [][]byte{[]byte("\x1b[?"), []byte("\x1b[!p"), []byte("\x1bc")}
	joined := append(append([]byte(nil), e.tail...), p[:min(len(p), 4)]...)
	changed := false
	for _, seq := range seqs {
		if bytes.Contains(p, seq) || bytes.Contains(joined, seq) {
			changed = true
			break
		}
	}
	e.tail = append(e.tail[:0], p[max(0, len(p)-3):]...)
	return changed
}

// syncModesLocked reports tracked modes that changed during the last write.
func (e *termEmu) syncModesLocked() {
	if e.cb.ModeChanged == nil {
		return
	}
	for _, m := range trackedModes {
		on, err := e.term.Mode(gh.NewMode(uint16(m.Mode()), false))
		if err != nil || on == e.modes[m] {
			continue
		}
		e.modes[m] = on
		e.cb.ModeChanged(m, on)
	}
}

func (e *termEmu) Resize(cols, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil || cols < 1 || rows < 1 {
		return
	}
	e.cols, e.rows = cols, rows
	_ = e.term.Resize(uint16(cols), uint16(rows), cellPixelsX, cellPixelsY)
	e.setEncoderSize()
}

func (e *termEmu) Width() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cols
}

func (e *termEmu) Height() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rows
}

func (e *termEmu) IsAltScreen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return false
	}
	screen, err := e.term.ActiveScreen()
	return err == nil && screen == gh.ScreenAlternate
}

// ScrollbackLen is the number of lines that have scrolled off the top.
func (e *termEmu) ScrollbackLen() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return 0
	}
	n, err := e.term.ScrollbackRows()
	if err != nil {
		return 0
	}
	return int(n)
}

// CursorPosition is the cursor's cell on the visible screen.
func (e *termEmu) CursorPosition() uv.Position {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return uv.Position{}
	}
	x, _ := e.term.CursorX()
	y, _ := e.term.CursorY()
	return uv.Position{X: int(x), Y: int(y)}
}

func (e *termEmu) CursorHidden() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return true
	}
	visible, err := e.term.CursorVisible()
	return err == nil && !visible
}

// Render returns the visible screen as styled text, one string per row
// joined by newlines. Every row carries its own styling, so rows can be
// split and drawn independently. Trailing empty rows are omitted.
func (e *termEmu) Render() string {
	return strings.Join(e.RenderLines(), "\n")
}

// RenderLines is Render without the join: one string per row.
func (e *termEmu) RenderLines() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return nil
	}
	first, err1 := e.term.GridRef(gh.Point{Tag: gh.PointTagActive, X: 0, Y: 0})
	last, err2 := e.term.GridRef(gh.Point{Tag: gh.PointTagActive, X: uint16(e.cols - 1), Y: uint32(e.rows - 1)})
	if err1 != nil || err2 != nil {
		return nil
	}
	return e.formatRowsLocked(first, last)
}

// RenderRows returns count rows of styled text starting at row start, where
// row 0 is the oldest scrollback line, so scrolled-back views can be drawn
// without reading every cell. Rows are padded with empty strings to count.
func (e *termEmu) RenderRows(start, count int) []string {
	rows := make([]string, count)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil || count < 1 || start < 0 {
		return rows
	}
	first, err1 := e.term.GridRef(gh.Point{Tag: gh.PointTagScreen, X: 0, Y: uint32(start)})
	last, err2 := e.term.GridRef(gh.Point{Tag: gh.PointTagScreen, X: uint16(e.cols - 1), Y: uint32(start + count - 1)})
	if err1 != nil || err2 != nil {
		return rows
	}
	copy(rows, e.formatRowsLocked(first, last))
	return rows
}

// formatRowsLocked formats the rows between two points as styled text.
func (e *termEmu) formatRowsLocked(first, last *gh.GridRef) []string {
	f, err := gh.NewFormatter(e.term,
		gh.WithFormatterFormat(gh.FormatterFormatVT),
		gh.WithFormatterSelection(&gh.Selection{Start: *first, End: *last}),
		gh.WithFormatterTrim(false))
	if err != nil {
		return nil
	}
	defer f.Close()
	s, err := f.FormatString()
	if err != nil {
		return nil
	}
	rows := strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
	for i, row := range rows {
		rows[i] = expandCursorForward(row)
	}
	return rows
}

// hasCursorForward reports whether row may contain a cursor-forward
// sequence: a 'C' straight after a digit or '['. Plain capital Cs in text do
// not count, so most rows skip the scan below.
func hasCursorForward(row string) bool {
	for off := 0; ; {
		i := strings.IndexByte(row[off:], 'C')
		if i < 0 {
			return false
		}
		i += off
		if i > 0 && (row[i-1] == '[' || row[i-1] >= '0' && row[i-1] <= '9') {
			return true
		}
		off = i + 1
	}
}

// expandCursorForward replaces the cursor-forward sequences (CSI n C) the
// formatter uses to skip blank cells with real spaces. A row has to be made of
// printable cells only: its width is measured and it is padded and clipped
// from that, and a sequence that moves the cursor is invisible to the
// measurement, so the row would come out wider than the pane. If a style is
// active it is switched off for the gap, so a background or underline does not
// leak into the blank cells, and restored afterwards.
func expandCursorForward(row string) string {
	if !hasCursorForward(row) {
		return row
	}
	var out strings.Builder
	out.Grow(len(row) + 16)
	active := "" // SGR sequences in effect since the last reset
	for i := 0; i < len(row); {
		esc := strings.IndexByte(row[i:], 0x1b)
		if esc < 0 {
			out.WriteString(row[i:])
			break
		}
		out.WriteString(row[i : i+esc])
		i += esc
		if i+1 >= len(row) || row[i+1] != '[' {
			out.WriteByte(row[i])
			i++
			continue
		}
		// CSI: parameter bytes 0x30-0x3f, intermediates 0x20-0x2f, then a
		// final byte 0x40-0x7e.
		j := i + 2
		for j < len(row) && row[j] >= 0x30 && row[j] <= 0x3f {
			j++
		}
		paramEnd := j
		for j < len(row) && row[j] >= 0x20 && row[j] <= 0x2f {
			j++
		}
		if j >= len(row) || row[j] < 0x40 || row[j] > 0x7e {
			out.WriteString(row[i:])
			break
		}
		seq, params, final := row[i:j+1], row[i+2:paramEnd], row[j]
		switch {
		case final == 'm' && paramEnd == j:
			switch {
			case params == "" || params == "0":
				active = ""
			case strings.HasPrefix(params, "0;"):
				active = seq
			default:
				active += seq
			}
			out.WriteString(seq)
		case final == 'C' && paramEnd == j && isDigits(params):
			n := 1
			if v, err := strconv.Atoi(params); err == nil && v > 0 { // 0 or omitted means 1
				n = v
			}
			if active != "" {
				out.WriteString("\x1b[0m")
			}
			out.WriteString(strings.Repeat(" ", n))
			out.WriteString(active)
		default:
			out.WriteString(seq)
		}
		i = j + 1
	}
	return out.String()
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// CellAt is a cell of the visible screen; nil when out of range or empty.
func (e *termEmu) CellAt(x, y int) *uv.Cell {
	return e.cellAt(gh.PointTagActive, x, y)
}

// ScrollbackCellAt is a cell of the scrollback; y 0 is the oldest line.
func (e *termEmu) ScrollbackCellAt(x, y int) *uv.Cell {
	return e.cellAt(gh.PointTagHistory, x, y)
}

func (e *termEmu) cellAt(tag gh.PointTag, x, y int) *uv.Cell {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil || x < 0 || y < 0 || x >= e.cols {
		return nil
	}
	ref, err := e.term.GridRef(gh.Point{Tag: tag, X: uint16(x), Y: uint32(y)})
	if err != nil || ref == nil {
		return nil
	}
	return cellFromRef(ref)
}

func cellFromRef(ref *gh.GridRef) *uv.Cell {
	raw, err := ref.Cell()
	if err != nil {
		return nil
	}
	wide, _ := raw.Wide()
	switch wide {
	case gh.CellWideSpacerTail:
		return &uv.Cell{Width: 0} // second half of a wide character
	case gh.CellWideSpacerHead:
		return nil
	}
	var style uv.Style
	if styling, _ := raw.HasStyling(); styling {
		if s, err := ref.Style(); err == nil && s != nil {
			style = uvStyle(s)
		}
	}
	var content strings.Builder
	if graphemes, err := ref.Graphemes(); err == nil {
		for _, r := range graphemes {
			content.WriteRune(rune(r))
		}
	}
	if content.Len() == 0 {
		if style.IsZero() {
			return nil
		}
		return &uv.Cell{Content: " ", Width: 1, Style: style}
	}
	width := 1
	if wide == gh.CellWideWide {
		width = 2
	}
	return &uv.Cell{Content: content.String(), Width: width, Style: style}
}

func uvStyle(s *gh.Style) uv.Style {
	out := uv.Style{
		Fg:             uvColor(s.FgColor()),
		Bg:             uvColor(s.BgColor()),
		UnderlineColor: uvColor(s.UnderlineColor()),
	}
	if s.Bold() {
		out.Attrs |= uv.AttrBold
	}
	if s.Faint() {
		out.Attrs |= uv.AttrFaint
	}
	if s.Italic() {
		out.Attrs |= uv.AttrItalic
	}
	if s.Blink() {
		out.Attrs |= uv.AttrBlink
	}
	if s.Inverse() {
		out.Attrs |= uv.AttrReverse
	}
	if s.Invisible() {
		out.Attrs |= uv.AttrConceal
	}
	if s.Strikethrough() {
		out.Attrs |= uv.AttrStrikethrough
	}
	switch s.Underline() {
	case gh.UnderlineSingle:
		out.Underline = uv.UnderlineSingle
	case gh.UnderlineDouble:
		out.Underline = uv.UnderlineDouble
	case gh.UnderlineCurly:
		out.Underline = uv.UnderlineCurly
	case gh.UnderlineDotted:
		out.Underline = uv.UnderlineDotted
	case gh.UnderlineDashed:
		out.Underline = uv.UnderlineDashed
	}
	return out
}

func uvColor(c gh.StyleColor) color.Color {
	switch c.Tag {
	case gh.StyleColorPalette:
		return ansi.IndexedColor(c.Palette)
	case gh.StyleColorRGB:
		return color.RGBA{R: c.RGB.R, G: c.RGB.G, B: c.RGB.B, A: 255}
	}
	return nil
}

// SendMouse encodes a mouse event for the program, as its mouse mode asks,
// and sends it back through the pty. Nothing is sent when the program is not
// tracking the mouse.
func (e *termEmu) SendMouse(ev uv.MouseEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil || e.cb.WritePty == nil {
		return
	}
	m := ev.Mouse()
	var action gh.MouseAction
	switch ev.(type) {
	case uv.MouseReleaseEvent:
		action = gh.MouseActionRelease
	case uv.MouseMotionEvent:
		action = gh.MouseActionMotion
	default: // clicks and wheel steps
		action = gh.MouseActionPress
	}
	button, ok := ghMouseButton(m.Button)
	event, err := gh.NewMouseEvent()
	if err != nil {
		return
	}
	defer event.Close()
	event.SetAction(action)
	if ok {
		event.SetButton(button)
	} else {
		event.ClearButton()
	}
	var mods gh.Mods
	if m.Mod.Contains(uv.ModShift) {
		mods |= gh.ModShift
	}
	if m.Mod.Contains(uv.ModAlt) {
		mods |= gh.ModAlt
	}
	if m.Mod.Contains(uv.ModCtrl) {
		mods |= gh.ModCtrl
	}
	event.SetMods(mods)
	event.SetPosition(gh.MousePosition{
		X: float32(m.X*cellPixelsX + cellPixelsX/2),
		Y: float32(m.Y*cellPixelsY + cellPixelsY/2),
	})
	e.enc.SetOptFromTerminal(e.term)
	e.enc.SetOptSize(gh.MouseEncoderSize{
		ScreenWidth: uint32(e.cols * cellPixelsX), ScreenHeight: uint32(e.rows * cellPixelsY),
		CellWidth: cellPixelsX, CellHeight: cellPixelsY,
	})
	e.enc.SetOptAnyButtonPressed(action == gh.MouseActionMotion && ok)
	if out, err := e.enc.Encode(event); err == nil && len(out) > 0 {
		e.cb.WritePty(out)
	}
}

func ghMouseButton(b uv.MouseButton) (gh.MouseButton, bool) {
	switch b {
	case uv.MouseLeft:
		return gh.MouseButtonLeft, true
	case uv.MouseMiddle:
		return gh.MouseButtonMiddle, true
	case uv.MouseRight:
		return gh.MouseButtonRight, true
	case uv.MouseWheelUp:
		return gh.MouseButtonFour, true
	case uv.MouseWheelDown:
		return gh.MouseButtonFive, true
	case uv.MouseWheelLeft:
		return gh.MouseButtonSix, true
	case uv.MouseWheelRight:
		return gh.MouseButtonSeven, true
	}
	return gh.MouseButtonUnknown, false
}

func (e *termEmu) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.enc != nil {
		e.enc.Close()
		e.enc = nil
	}
	if e.term != nil {
		e.term.Close()
		e.term = nil
	}
}
