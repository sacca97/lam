package app

import (
	"bytes"
	"testing"
	"time"
)

func TestTerminalInputDecoderPreservesBytesAroundSplitFocusChord(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	var got []inputEvent
	got = append(got, d.Feed([]byte("before\x1b[32;"))...)
	got = append(got, d.Feed([]byte("5uafter"))...)

	var payload []byte
	focusCount := 0
	for _, event := range got {
		if event.focus {
			focusCount++
		} else {
			payload = append(payload, event.data...)
		}
	}
	if !bytes.Equal(payload, []byte("beforeafter")) {
		t.Fatalf("forwarded bytes = %q, want %q", payload, "beforeafter")
	}
	if focusCount != 1 {
		t.Fatalf("focus events = %d, want 1", focusCount)
	}
}

func TestTerminalInputDecoderHandlesMultipleEventsInOneRead(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	events := d.Feed([]byte("a\x00b\x1c\x1b[92;5uc"))
	var payload []byte
	focusCount := 0
	for _, event := range events {
		if event.focus {
			focusCount++
		} else {
			payload = append(payload, event.data...)
		}
	}
	if string(payload) != "abc" || focusCount != 3 {
		t.Fatalf("payload=%q focus events=%d, want payload abc and 3 focus events", payload, focusCount)
	}
}

func TestTerminalInputDecoderDoesNotInspectBracketedPaste(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	var got []byte
	focusCount := 0
	for _, event := range d.Feed([]byte(pasteStart)) {
		if event.focus {
			focusCount++
		} else {
			got = append(got, event.data...)
		}
	}
	for _, event := range d.Feed([]byte("paste\x00\x1b[92;5u")) {
		if event.focus {
			focusCount++
		} else {
			got = append(got, event.data...)
		}
	}
	for _, event := range d.Feed([]byte(pasteEnd + "after\x00")) {
		if event.focus {
			focusCount++
		} else {
			got = append(got, event.data...)
		}
	}
	if focusCount != 1 {
		t.Fatalf("focus events = %d, want only the chord after paste", focusCount)
	}
	want := pasteStart + "paste\x00\x1b[92;5u" + pasteEnd + "after"
	if string(got) != want {
		t.Fatalf("forwarded paste bytes = %q, want %q", got, want)
	}
}

func TestTerminalInputDecoderCanPassReservedChordThrough(t *testing.T) {
	d := newTerminalInputDecoder(focusChordOff)
	want := []byte("x\x00\x1b[32;5uy")
	events := d.Feed(want)
	var got []byte
	focusCount := 0
	for _, event := range events {
		if event.focus {
			focusCount++
		} else {
			got = append(got, event.data...)
		}
	}
	if focusCount != 0 || !bytes.Equal(got, want) {
		t.Fatalf("focus=%d forwarded=%q, want no focus and unchanged bytes", focusCount, got)
	}
}

func TestTerminalInputDecoderFlushesIncompleteEscapeWithoutDroppingIt(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	if events := d.Feed([]byte("\x1b[32;")); len(events) != 0 {
		t.Fatalf("incomplete sequence emitted early: %#v", events)
	}
	events := d.flushAfter(time.Now().Add(time.Second), 40*time.Millisecond)
	if len(events) != 1 || string(events[0].data) != "\x1b[32;" {
		t.Fatalf("flush events = %#v, want original partial sequence", events)
	}
}

func TestTerminalInputDecoderParsesSGRMousePressMotionAndRelease(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	events := d.Feed([]byte("\x1b[<0;4;7M\x1b[<32;5;8M\x1b[<0;5;8m"))
	if len(events) != 3 {
		t.Fatalf("events = %#v, want press, motion, and release", events)
	}
	for i, want := range []struct {
		button  int
		x       int
		y       int
		release bool
	}{{button: 0, x: 4, y: 7}, {button: 32, x: 5, y: 8}, {button: 0, x: 5, y: 8, release: true}} {
		event := events[i]
		if event.mouse == nil {
			t.Fatalf("event %d = %#v, want mouse event", i, event)
		}
		if event.mouse.button != want.button || event.mouse.x != want.x || event.mouse.y != want.y || event.mouse.release != want.release {
			t.Fatalf("event %d mouse = %#v, want %#v", i, event.mouse, want)
		}
	}
}

func TestPopUTF8RemovesOneRune(t *testing.T) {
	if got := string(popUTF8([]byte("a界"))); got != "a" {
		t.Fatalf("popUTF8 = %q, want a", got)
	}
}

func TestTerminalInputDecoderRecognizesAltT(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	events := d.Feed([]byte("a\x1btb"))
	if len(events) != 3 || !events[1].terminalToggle || string(events[0].data) != "a" || string(events[2].data) != "b" {
		t.Fatalf("events = %#v, want a, terminal toggle, b", events)
	}
}

func TestGlobalKeysBecomeActions(t *testing.T) {
	d := newTerminalInputDecoder(focusChordBoth)
	for input, want := range map[string]string{
		"\x04": "detach", "\x11": "quit-all", "\x1b[1;5A": "prev", "\x1b[1;5B": "next",
	} {
		events := d.Feed([]byte(input))
		if len(events) != 1 || events[0].action != want {
			t.Errorf("%q = %+v, want action %q", input, events, want)
		}
	}
	// Inside a bracketed paste they are text.
	events := d.Feed([]byte(pasteStart + "a\x04b" + pasteEnd))
	for _, e := range events {
		if e.action != "" {
			t.Errorf("paste produced action %q", e.action)
		}
	}
}
