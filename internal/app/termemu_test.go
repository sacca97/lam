package app

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

type emuRecorder struct {
	mu     sync.Mutex
	pty    []byte
	pwd    string
	clip   string
	modes  map[ansi.Mode]bool
	events int
}

func newTestEmu(t *testing.T, cols, rows int) (*termEmu, *emuRecorder) {
	t.Helper()
	rec := &emuRecorder{modes: map[ansi.Mode]bool{}}
	e, err := newTermEmu(cols, rows, termCallbacks{
		WritePty:       func(b []byte) { rec.mu.Lock(); rec.pty = append(rec.pty, b...); rec.mu.Unlock() },
		Pwd:            func(u string) { rec.pwd = u },
		ClipboardWrite: func(s string) { rec.clip = s },
		ModeChanged:    func(m ansi.Mode, on bool) { rec.modes[m] = on; rec.events++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e, rec
}

func TestTermEmuRender(t *testing.T) {
	e, _ := newTestEmu(t, 20, 4)
	e.Write([]byte("plain\r\n\x1b[1;31mred bold\x1b[0m x\r\n日本語 é\r\n\x1b[41mbg"))
	rows := strings.Split(e.Render(), "\n")
	if len(rows) != 4 {
		t.Fatalf("rows = %q", rows)
	}
	for i, want := range []string{"plain", "red bold x", "日本語 e\u0301", "bg"} {
		if got := strings.TrimRight(ansi.Strip(rows[i]), " "); got != want {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}
	// A background color must not bleed into the next row once split apart.
	if strings.Contains(rows[3], "\x1b[41m") && !strings.HasSuffix(rows[3], "\x1b[0m") {
		t.Errorf("row left a style open: %q", rows[3])
	}
}

func TestTermEmuCellsAndStyles(t *testing.T) {
	e, _ := newTestEmu(t, 20, 4)
	e.Write([]byte("\x1b[1;38;2;1;2;3;48;5;9mA\x1b[0m日"))
	a := e.CellAt(0, 0)
	if a == nil || a.Content != "A" || a.Width != 1 || a.Style.Attrs&uv.AttrBold == 0 {
		t.Fatalf("A = %#v", a)
	}
	if fg, ok := a.Style.Fg.(color.RGBA); !ok || fg.R != 1 || fg.G != 2 || fg.B != 3 {
		t.Errorf("fg = %#v", a.Style.Fg)
	}
	if bg, ok := a.Style.Bg.(ansi.IndexedColor); !ok || bg != 9 {
		t.Errorf("bg = %#v", a.Style.Bg)
	}
	if w := e.CellAt(1, 0); w == nil || w.Content != "日" || w.Width != 2 {
		t.Errorf("wide = %#v", w)
	}
	if tail := e.CellAt(2, 0); tail == nil || tail.Width != 0 {
		t.Errorf("wide tail = %#v", tail)
	}
	if e.CellAt(10, 2) != nil {
		t.Error("empty cell should be nil")
	}
}

func TestTermEmuScrollback(t *testing.T) {
	e, _ := newTestEmu(t, 20, 5)
	for i := 0; i < 100; i++ {
		e.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	if n := e.ScrollbackLen(); n < 90 {
		t.Fatalf("scrollback = %d, want about 96", n)
	}
	first := e.ScrollbackCellAt(5, 0)
	if first == nil || first.Content != "0" {
		t.Fatalf("oldest line, col 5 = %#v", first)
	}
	if e.IsAltScreen() {
		t.Error("main screen reported as alternate")
	}
	e.Write([]byte("\x1b[?1049h"))
	if !e.IsAltScreen() {
		t.Error("alternate screen not detected")
	}
}

func TestTermEmuModesAndMouse(t *testing.T) {
	e, rec := newTestEmu(t, 40, 10)
	e.Write([]byte("\x1b[?1000h\x1b[?1006h\x1b[?2004h"))
	if !rec.modes[ansi.ModeMouseNormal] || !rec.modes[ansi.ModeBracketedPaste] {
		t.Fatalf("modes = %v", rec.modes)
	}
	e.SendMouse(uv.MouseClickEvent(uv.Mouse{X: 3, Y: 2, Button: uv.MouseLeft}))
	if got := string(rec.pty); got != "\x1b[<0;4;3M" {
		t.Fatalf("click report = %q", got)
	}
	rec.pty = nil
	e.SendMouse(uv.MouseReleaseEvent(uv.Mouse{X: 3, Y: 2, Button: uv.MouseLeft}))
	if got := string(rec.pty); got != "\x1b[<0;4;3m" {
		t.Fatalf("release report = %q", got)
	}
	rec.pty = nil
	e.SendMouse(uv.MouseWheelEvent(uv.Mouse{X: 0, Y: 0, Button: uv.MouseWheelUp}))
	if got := string(rec.pty); got != "\x1b[<64;1;1M" {
		t.Fatalf("wheel report = %q", got)
	}
	e.Write([]byte("\x1b[?1000l\x1b[?2004l"))
	if rec.modes[ansi.ModeMouseNormal] || rec.modes[ansi.ModeBracketedPaste] {
		t.Fatalf("modes still on: %v", rec.modes)
	}
	rec.pty = nil
	e.SendMouse(uv.MouseClickEvent(uv.Mouse{X: 1, Y: 1, Button: uv.MouseLeft}))
	if len(rec.pty) != 0 {
		t.Fatalf("mouse report sent while not tracking: %q", rec.pty)
	}
}

func TestTermEmuModeSequenceSplitAcrossWrites(t *testing.T) {
	e, rec := newTestEmu(t, 40, 10)
	e.Write([]byte("text \x1b"))
	e.Write([]byte("[?1000h more"))
	if !rec.modes[ansi.ModeMouseNormal] {
		t.Fatalf("mode set by a split sequence was missed: %v", rec.modes)
	}
	e.Write([]byte("\x1bc")) // full reset clears it again
	if rec.modes[ansi.ModeMouseNormal] {
		t.Fatalf("reset did not clear the mode: %v", rec.modes)
	}
}

func TestTermEmuRepliesAndCallbacks(t *testing.T) {
	e, rec := newTestEmu(t, 40, 10)
	// The session host already answers terminal queries; answering them here
	// too would hand the program every reply twice.
	e.Write([]byte("ab\x1b[6n\x1b[c\x1b[>0q\x1b[?2026$p\x1b]11;?\x07"))
	if len(rec.pty) != 0 {
		t.Fatalf("the interface answered terminal queries: %q", rec.pty)
	}
	e.Write([]byte("\x1b]7;file://host/home/me/work\x07"))
	if rec.pwd != "file://host/home/me/work" {
		t.Fatalf("pwd = %q", rec.pwd)
	}
	e.Write([]byte("\x1b]52;c;aGVsbG8=\x07"))
	if rec.clip != "hello" {
		t.Fatalf("clipboard = %q", rec.clip)
	}
	if pos := e.CursorPosition(); pos.X != 2 || pos.Y != 0 {
		t.Fatalf("cursor = %#v", pos)
	}
	e.Write([]byte("\x1b[?25l"))
	if !e.CursorHidden() {
		t.Error("cursor should be hidden")
	}
}

func TestTermEmuResize(t *testing.T) {
	e, _ := newTestEmu(t, 20, 5)
	e.Resize(30, 8)
	if e.Width() != 30 || e.Height() != 8 {
		t.Fatalf("size = %dx%d", e.Width(), e.Height())
	}
	e.Write([]byte(strings.Repeat("x", 30)))
	if c := e.CellAt(29, 0); c == nil || c.Content != "x" {
		t.Fatalf("last column = %#v", c)
	}
}

func BenchmarkTermEmuScroll(b *testing.B) {
	e, err := newTermEmu(200, 48, termCallbacks{})
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	chunk := []byte(strings.Repeat("\x1b[38;5;108msome streaming output line that keeps scrolling the screen\r\n", 500))
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		e.Write(chunk)
	}
}

func BenchmarkTermEmuRender(b *testing.B) {
	e, _ := newTermEmu(200, 48, termCallbacks{})
	defer e.Close()
	e.Write([]byte(strings.Repeat("\x1b[38;5;108msome streaming output line that keeps scrolling the screen\r\n", 200)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Render()
	}
}

func TestExpandCursorForward(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                            "plain",
		"\x1b[1CAccessing":                 " Accessing",
		"a\x1b[3Cb":                        "a   b",
		"\x1b[1m\x1b[33mAccess\x1b[1Cwork": "\x1b[1m\x1b[33mAccess\x1b[0m \x1b[1m\x1b[33mwork",
		"\x1b[0m\x1b[1mx\x1b[0m\x1b[2Cy":   "\x1b[0m\x1b[1mx\x1b[0m  y",
		"Capital C stays":                  "Capital C stays",
		"\x1b[0Cx":                         " x", // a 0 parameter means 1
		"\x1b[Cx":                          " x",
		"\x1b[?25hC":                       "\x1b[?25hC", // private sequence left alone
		"\x1b[1;2Hx5C":                     "\x1b[1;2Hx5C",
		"Claude Code \x1b[31mCompiling":    "Claude Code \x1b[31mCompiling",
	} {
		if got := expandCursorForward(in); got != want {
			t.Errorf("expandCursorForward(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTermEmuRenderKeepsColumnsOfSkippedCells(t *testing.T) {
	e, _ := newTestEmu(t, 30, 3)
	// Cursor movements leave cells unwritten; the row must still be 30 wide.
	e.Write([]byte("\x1b[1;1H\x1b[1;33mAccessing\x1b[1Cworkspace:\x1b[0m\x1b[2;5Hx"))
	rows := strings.Split(e.Render(), "\n")
	if got := ansi.Strip(rows[0]); got != "Accessing workspace:" {
		t.Errorf("row 0 = %q", got)
	}
	if got := ansi.Strip(rows[1]); got != "    x" {
		t.Errorf("row 1 = %q", got)
	}
	if w := ansi.StringWidth(rows[0]); w != len("Accessing workspace:") {
		t.Errorf("row 0 measures %d cells", w)
	}
}

func TestTermEmuMeasuresEmojiSequencesAsOneGlyph(t *testing.T) {
	for _, sample := range []string{"⚠️", "✔️", "❤️", "👨‍👩‍👧", "🇮🇹", "🧑🏽‍💻", "日本", "e\u0301"} {
		e, _ := newTestEmu(t, 30, 2)
		e.Write([]byte(sample + "|"))
		col := -1
		for x := 0; x < 10; x++ {
			if c := e.CellAt(x, 0); c != nil && c.Content == "|" {
				col = x
			}
		}
		if want := ansi.StringWidth(sample); col != want {
			t.Errorf("%q takes %d cells in the emulator but the layout measures %d", sample, col, want)
		}
	}
}

func BenchmarkExpandCursorForwardPlainRows(b *testing.B) {
	rows := []string{"Claude Code is compiling the Crate", "\x1b[1;32mCreated\x1b[0m files in Cache", "plain text row with nothing special"}
	for i := 0; i < b.N; i++ {
		for _, r := range rows {
			_ = expandCursorForward(r)
		}
	}
}
