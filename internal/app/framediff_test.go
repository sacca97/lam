package app

import (
	"strings"
	"testing"
)

func TestFrameDiffSendsOnlyChangedRows(t *testing.T) {
	var f frameDiff
	frame := func(a, b, c string) string {
		return "\x1b[1;1H" + a + "\x1b[2;1H\x1b[2K" + b + "\x1b[2;30H|" + "\x1b[3;1H" + c
	}
	first := f.diff(frame("top", "mid", "bottom"))
	if first != frame("top", "mid", "bottom") {
		t.Fatalf("first frame must be sent whole: %q", first)
	}
	if got := f.diff(frame("top", "mid", "bottom")); got != "" {
		t.Fatalf("identical frame sent %q", got)
	}
	got := f.diff(frame("top", "MID", "bottom"))
	if !strings.Contains(got, "MID") || strings.Contains(got, "top") || strings.Contains(got, "bottom") {
		t.Fatalf("only the middle row should be sent: %q", got)
	}
	// Both chunks of the changed row travel together.
	if !strings.Contains(got, "\x1b[2;30H|") {
		t.Fatalf("row 2 lost a chunk: %q", got)
	}
	f.reset()
	if got := f.diff(frame("top", "MID", "bottom")); got != frame("top", "MID", "bottom") {
		t.Fatalf("after reset the whole frame must be sent: %q", got)
	}
	if got := f.diff("no cursor moves"); got != "no cursor moves" {
		t.Fatalf("body without chunks changed: %q", got)
	}
}

func TestFrameDiffIgnoresLookalikesAndHandlesMultiDigitRows(t *testing.T) {
	var f frameDiff
	body := "\x1b[12;40Hrow twelve \x1b[31mred\x1b[0m \x1b[5Cnot an address \x1b[99;Hbroken\x1b[3;1Hthree"
	if got := f.diff(body); got != body {
		t.Fatalf("first frame changed: %q", got)
	}
	if got := f.diff(body); got != "" {
		t.Fatalf("identical frame sent %q", got)
	}
	changed := strings.Replace(body, "three", "THREE", 1)
	if got := f.diff(changed); got != "\x1b[3;1HTHREE" {
		t.Fatalf("only row 3 should be sent: %q", got)
	}
}

func BenchmarkFrameDiff(b *testing.B) { // a 200x50 frame, one row changing
	frame := func(tick int) string {
		var sb strings.Builder
		for row := 1; row <= 50; row++ {
			sb.WriteString("\x1b[")
			sb.WriteString(string(rune('0' + row/10)))
			sb.WriteString(string(rune('0' + row%10)))
			sb.WriteString(";1H\x1b[2K\x1b[38;5;108m")
			sb.WriteString(strings.Repeat("x", 190))
			if row == 20 {
				sb.WriteString(string(rune('a' + tick%26)))
			}
			sb.WriteString("\x1b[0m")
		}
		return sb.String()
	}
	var f frameDiff
	frames := []string{frame(0), frame(1)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.diff(frames[i%2])
	}
}
