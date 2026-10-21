package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

type focusChordMode uint8

const (
	focusChordBoth focusChordMode = iota
	focusChordSpace
	focusChordBackslash
	focusChordOff
)

// focusChordFromEnv makes the pane escape configurable with LAM_FOCUS_CHORD.
func focusChordFromEnv() focusChordMode {
	value := os.Getenv("LAM_FOCUS_CHORD")
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "none", "disabled":
		return focusChordOff
	case "ctrl-space", "ctrl+space", "space":
		return focusChordSpace
	case "ctrl-backslash", "ctrl+backslash", "backslash":
		return focusChordBackslash
	default:
		return focusChordBoth
	}
}

type inputEvent struct {
	data           []byte
	focus          bool
	sidebarToggle  bool
	terminalToggle bool
	action         string // detach, quit-all, prev or next: work from anywhere in the UI
	mouse          *terminalMouseEvent
}

type terminalMouseEvent struct {
	button  int
	x       int // 1-based terminal cell column
	y       int // 1-based terminal cell row
	release bool
}

type terminalInputDecoder struct {
	mode       focusChordMode
	pending    []byte
	paste      bool
	lastUpdate time.Time
}

func newTerminalInputDecoder(mode focusChordMode) *terminalInputDecoder {
	return &terminalInputDecoder{mode: mode}
}

func (d *terminalInputDecoder) Feed(data []byte) []inputEvent {
	if len(data) == 0 {
		return nil
	}
	d.pending = append(d.pending, data...)
	d.lastUpdate = time.Now()
	return d.process()
}

// Flush releases an incomplete escape sequence after its short inter-byte
// window. Inside bracketed paste, only bytes that cannot begin the paste end
// marker are released, so paste contents are never interpreted as key chords.
func (d *terminalInputDecoder) Flush() []inputEvent {
	if len(d.pending) == 0 {
		return nil
	}
	if d.paste {
		return d.process()
	}
	events := []inputEvent{{data: append([]byte(nil), d.pending...)}}
	d.pending = nil
	return events
}

func (d *terminalInputDecoder) process() []inputEvent {
	var events []inputEvent
	var plain []byte
	emitPlain := func() {
		if len(plain) > 0 {
			events = append(events, inputEvent{data: append([]byte(nil), plain...)})
			plain = plain[:0]
		}
	}
	consume := func(n int) {
		copy(d.pending, d.pending[n:])
		d.pending = d.pending[:len(d.pending)-n]
	}

	for len(d.pending) > 0 {
		if d.paste {
			if end := bytes.Index(d.pending, []byte(pasteEnd)); end >= 0 {
				emitPlain()
				n := end + len(pasteEnd)
				events = append(events, inputEvent{data: append([]byte(nil), d.pending[:n]...)})
				consume(n)
				d.paste = false
				continue
			}
			keep := suffixPrefixLength(d.pending, []byte(pasteEnd))
			safe := len(d.pending) - keep
			if safe > 0 {
				events = append(events, inputEvent{data: append([]byte(nil), d.pending[:safe]...)})
				consume(safe)
			}
			return events
		}

		b := d.pending[0]
		if b == 0x1b {
			emitPlain()
			if len(d.pending) == 1 {
				return events
			}
			if d.pending[1] == '[' || d.pending[1] == 'O' {
				end := sequenceEnd(d.pending)
				if end == 0 {
					return events
				}
				seq := append([]byte(nil), d.pending[:end]...)
				consume(end)
				if string(seq) == pasteStart {
					events = append(events, inputEvent{data: seq})
					d.paste = true
					continue
				}
				if mouse, ok := parseSGRMouse(seq); ok {
					events = append(events, inputEvent{mouse: &mouse})
					continue
				}
				if action := globalKeyAction(string(seq)); action != "" {
					events = append(events, inputEvent{action: action})
				} else if d.isFocusSequence(seq) {
					events = append(events, inputEvent{focus: true})
				} else if d.isSidebarToggleSequence(seq) {
					events = append(events, inputEvent{sidebarToggle: true})
				} else {
					events = append(events, inputEvent{data: seq})
				}
				continue
			}
			// Alt-prefixed keys are complete two-byte units for our purposes.
			seq := append([]byte(nil), d.pending[:2]...)
			consume(2)
			if string(seq) == "\x1bt" {
				events = append(events, inputEvent{terminalToggle: true})
				continue
			}
			events = append(events, inputEvent{data: seq})
			continue
		}

		if b == 0x04 || b == 0x11 { // Ctrl+D detaches, Ctrl+Q quits and stops everything
			emitPlain()
			consume(1)
			action := "detach"
			if b == 0x11 {
				action = "quit-all"
			}
			events = append(events, inputEvent{action: action})
			continue
		}
		if d.isFocusByte(b) {
			emitPlain()
			consume(1)
			events = append(events, inputEvent{focus: true})
			continue
		}
		plain = append(plain, b)
		consume(1)
	}
	emitPlain()
	return events
}

func parseSGRMouse(seq []byte) (terminalMouseEvent, bool) {
	if len(seq) < 7 || !bytes.HasPrefix(seq, []byte("\x1b[<")) {
		return terminalMouseEvent{}, false
	}
	final := seq[len(seq)-1]
	if final != 'M' && final != 'm' {
		return terminalMouseEvent{}, false
	}
	parts := strings.Split(string(seq[3:len(seq)-1]), ";")
	if len(parts) != 3 {
		return terminalMouseEvent{}, false
	}
	button, errButton := strconv.Atoi(parts[0])
	x, errX := strconv.Atoi(parts[1])
	y, errY := strconv.Atoi(parts[2])
	if errButton != nil || errX != nil || errY != nil || x < 1 || y < 1 {
		return terminalMouseEvent{}, false
	}
	return terminalMouseEvent{button: button, x: x, y: y, release: final == 'm'}, true
}

func (d *terminalInputDecoder) isFocusByte(b byte) bool {
	return (b == 0 && (d.mode == focusChordBoth || d.mode == focusChordSpace)) ||
		(b == 0x1c && (d.mode == focusChordBoth || d.mode == focusChordBackslash))
}

func (d *terminalInputDecoder) isFocusSequence(seq []byte) bool {
	if d.mode == focusChordOff {
		return false
	}
	s := string(seq)
	space := s == "\x1b[32;5u"
	backslash := s == "\x1b[92;5u"
	return (space && (d.mode == focusChordBoth || d.mode == focusChordSpace)) ||
		(backslash && (d.mode == focusChordBoth || d.mode == focusChordBackslash))
}

func (d *terminalInputDecoder) isSidebarToggleSequence(seq []byte) bool {
	switch string(seq) {
	case "\x1bOQ", "\x1b[12~", "\x1b[12;1~", "\x1b[57365u":
		return true
	default:
		return false
	}
}

// sequenceEnd returns the byte length of a complete CSI or SS3 sequence.
// Zero means the input is still incomplete. A bounded scan prevents malformed
// terminal input from making the decoder retain an unbounded buffer.
func sequenceEnd(p []byte) int {
	for i := 2; i < len(p) && i < 256; i++ {
		if p[i] >= 0x40 && p[i] <= 0x7e {
			return i + 1
		}
	}
	if len(p) >= 256 {
		return 1
	}
	return 0
}

func suffixPrefixLength(data, marker []byte) int {
	max := len(data)
	if max >= len(marker) {
		max = len(marker) - 1
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(data[len(data)-n:], marker[:n]) {
			return n
		}
	}
	return 0
}

func popUTF8(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	i := len(data) - 1
	for i > 0 && data[i]&0xc0 == 0x80 {
		i--
	}
	return data[:i]
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (d *terminalInputDecoder) flushAfter(now time.Time, timeout time.Duration) []inputEvent {
	if len(d.pending) == 0 || now.Sub(d.lastUpdate) < timeout {
		return nil
	}
	return d.Flush()
}

// globalKeyAction names the keys that act the same wherever focus is: Ctrl+Up
// and Ctrl+Down move between sessions.
func globalKeyAction(seq string) string {
	switch seq {
	case "\x1b[1;5A":
		return "prev"
	case "\x1b[1;5B":
		return "next"
	}
	return ""
}

// writePTY writes to a pty master and rides out a full line-discipline buffer.
// A terminal in canonical mode (a plain `cat`, a shell before it starts reading)
// accepts only a few KB at a time; the kernel then reports a short write with
// nothing written instead of blocking, and a large paste would be cut off. The
// write is retried while the reader catches up, for up to ptyWriteStall.
func writePTY(w io.Writer, data []byte) error {
	deadline := time.Now().Add(ptyWriteStall)
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
			deadline = time.Now().Add(ptyWriteStall)
		}
		if err != nil && !errors.Is(err, io.ErrShortWrite) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if len(data) > 0 {
			if time.Now().After(deadline) {
				return io.ErrShortWrite
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	return nil
}

// ptyWriteStall is how long a write may make no progress before it gives up.
const ptyWriteStall = 30 * time.Second
