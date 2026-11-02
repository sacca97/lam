//go:build gui

package app

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	gh "go.mitchellh.com/libghostty"
)

func TestKeyBytes(t *testing.T) {
	cases := []struct {
		name key.Name
		mods key.Modifiers
		want string
		ok   bool
	}{
		{key.NameReturn, 0, "\r", true},
		{key.NameDeleteBackward, 0, "\x7f", true},
		{key.NameTab, 0, "\t", true},
		{key.NameTab, key.ModShift, "\x1b[Z", true},
		{key.NameEscape, 0, "\x1b", true},
		{key.NameUpArrow, 0, "\x1b[A", true},
		{key.NameLeftArrow, key.ModCtrl, "\x1b[1;5D", true},
		{key.NameDeleteForward, 0, "\x1b[3~", true},
		{key.NamePageDown, 0, "\x1b[6~", true},
		{"C", key.ModCtrl, "\x03", true},
		{"D", key.ModCtrl, "\x04", true},
		{"[", key.ModCtrl, "\x1b", true},
		{key.NameSpace, key.ModCtrl, "\x00", true},
		{"B", key.ModAlt, "\x1bb", true},
		{"F1", 0, "\x1bOP", true},
		{"F5", 0, "\x1b[15~", true},
		{"A", 0, "", false}, // plain text arrives as an edit event
		{"A", key.ModShift, "", false},
		{key.NameSpace, 0, "", false},
	}
	for _, c := range cases {
		got, ok := keyBytes(c.name, c.mods)
		if ok != c.ok || string(got) != c.want {
			t.Errorf("keyBytes(%q,%v) = %q,%v; want %q,%v", c.name, c.mods, got, ok, c.want, c.ok)
		}
	}
}

func testPane(t testing.TB, cols, rows int) *embedded {
	t.Helper()
	e, stop, err := newEmbedded("t", false, "", cols, rows, newDirtyFlag())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	e.send = func([]byte) {}
	return e
}

const demoScreen = "\x1b[1mBold\x1b[0m \x1b[3mItalic\x1b[0m \x1b[1;3mBoldItalic\x1b[0m \x1b[4mUnder\x1b[0m \x1b[4:3;58;5;203mcurly\x1b[0m \x1b[21mdouble\x1b[0m \x1b[7mInverse\x1b[0m \x1b[9mStrike\x1b[0m \x1b[2mFaint\x1b[0m\r\n" +
	"\x1b[31mred\x1b[32m green\x1b[33m yellow\x1b[34m blue\x1b[35m magenta\x1b[36m cyan\x1b[0m \x1b[91mbright-red\x1b[0m \x1b[41;97m on red \x1b[0m\r\n" +
	"256: \x1b[38;5;108mcolor108\x1b[0m \x1b[48;5;236;38;5;214m 214 on 236 \x1b[0m  true: \x1b[38;2;255;120;50mrgb(255,120,50)\x1b[0m \x1b[48;2;40;60;120m bg \x1b[0m\r\n" +
	"wide: \u4f60\u597d\u4e16\u754c \u3053\u3093\u306b\u3061\u306f ok  braille: \u28ff\u2847\u28b8\u2809  blocks: \u2581\u2582\u2583\u2584\u2585\u2586\u2587\u2588 \u2591\u2592\u2593\r\n" +
	"\u256d\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u256e  \u250c\u2500\u252c\u2500\u2510 \u2554\u2550\u2566\u2550\u2557 \u250f\u2501\u2533\u2501\u2513\r\n" +
	"\u2502 \x1b[1m\u273b Claude Code\x1b[0m   \u2502  \u251c\u2500\u253c\u2500\u2524 \u2560\u2550\u256c\u2550\u2563 \u2523\u2501\u254b\u2501\u252b\r\n" +
	"\u2570\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u256f  \u2514\u2500\u2534\u2500\u2518 \u255a\u2550\u2569\u2550\u255d \u2517\u2501\u253b\u2501\u251b\r\n" +
	"\x1b[48;5;108;30m user \x1b[38;5;108;48;5;61m\ue0b0\x1b[97m ~/src/lam \x1b[0;38;5;61m\ue0b0\x1b[0m ls --color\r\n" +
	"Makefile  \x1b[1;34mcmd\x1b[0m  go.mod  \x1b[1;34minternal\x1b[0m  README.md  \x1b[32mlam*\x1b[0m\r\n" +
	"\x1b[38;5;108muser@host\x1b[0m:\x1b[34m~/src/lam\x1b[0m$ "

func newTestApp(t testing.TB, e *embedded) *guiApp {
	g := newGUIState()
	g.settings = defaultSettings()
	g.settings.GUIFontSize = defaultGUIFontSize
	g.sessions = []sessionInfo{
		{Name: "claude-api", CWD: "/home/user/src/api", Command: "claude", Activity: "active"},
		{Name: "codex-api", CWD: "/home/user/src/api", Command: "codex", Activity: "quiet", Unread: true},
		{Name: "claude-lam", CWD: "/home/user/src/lam", Command: "claude", Activity: "active"},
		{Name: "shell-lam", CWD: "/home/user/src/lam", Shell: true, Activity: "quiet"},
		{Name: "opencode-notes", CWD: "/tmp/notes", Command: "opencode", Activity: "quiet"},
	}
	now := time.Now()
	for name, ago := range map[string]time.Duration{"claude-api": 4 * time.Minute, "codex-api": 12 * time.Minute, "claude-lam": 30 * time.Second, "shell-lam": 2 * time.Hour, "opencode-notes": 26 * time.Hour} {
		g.statusSince[name] = now.Add(-ago)
	}
	g.listed, g.selected = true, "claude-lam"
	e.name = "claude-lam"
	e.identity = sessionIdentity(g.sessions[2])
	g.main = e
	g.mainView.attach(e)
	return g
}

// renderGUI lays out one frame of the whole window and returns it as an image.
func renderGUI(t *testing.T, g *guiApp, w, h int, scale float32) *image.RGBA {
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skipf("no headless GPU: %v", err)
	}
	defer win.Release()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Constraints: layout.Exact(image.Pt(w, h)), Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}}
	for range 2 { // the first frame sizes the grid, the second draws it
		ops.Reset()
		g.layout(gtx)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img
}

func TestRenderScreenshot(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	e.em.Write([]byte(demoScreen))
	renderGUI(t, g, 1240, 720, 1) // starts the fallback font lookups for CJK
	time.Sleep(time.Second)
	img := renderGUI(t, g, 1240, 720, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(dir + "/gui.png")
	png.Encode(f, img)
	f.Close()
	t.Logf("screenshot: %s/gui.png cell %v", dir, g.mainView.cellSize())
	// The dialogs and the session menu, for a look.
	for name, open := range map[string]func(){
		"new":      g.openNewSession,
		"stop":     func() { g.openStop(g.sessions[2]) },
		"settings": func() { g.dlg = dialog{kind: dlgSettings} },
		"connect":  func() { g.openConnect() },
		"ask": func() {
			g.dlg = dialog{kind: dlgAsk, ask: &sshAsk{title: "Password", text: "Password for me@build-box", canRemember: true}}
		},
		"menu": func() { g.dlg = dialog{}; g.menu = &rowMenu{s: g.sessions[2], at: image.Pt(120, 230)} },
	} {
		g.dlg, g.menu = dialog{}, nil
		open()
		img := renderGUI(t, g, 1240, 720, 1)
		f, _ := os.Create(dir + "/gui-" + name + ".png")
		png.Encode(f, img)
		f.Close()
	}
}

func TestEncodeKey(t *testing.T) {
	term, err := gh.NewTerminal(gh.WithSize(80, 24))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	enc, _ := gh.NewKeyEncoder()
	defer enc.Close()
	ev, _ := gh.NewKeyEvent()
	defer ev.Close()
	cases := []struct {
		name key.Name
		mods key.Modifiers
		want string
	}{
		{key.NameReturn, 0, "\r"},
		{key.NameDeleteBackward, 0, "\x7f"},
		{key.NameTab, 0, "\t"},
		{key.NameTab, key.ModShift, "\x1b[Z"},
		{key.NameEscape, 0, "\x1b"},
		{key.NameUpArrow, 0, "\x1b[A"},
		{key.NameLeftArrow, key.ModCtrl, "\x1b[1;5D"},
		{key.NameDeleteForward, 0, "\x1b[3~"},
		{key.NamePageDown, 0, "\x1b[6~"},
		{"C", key.ModCtrl, "\x03"},
		{"D", key.ModCtrl, "\x04"},
		{"[", key.ModCtrl, "\x1b"},
		{key.NameSpace, key.ModCtrl, "\x00"},
		{"B", key.ModAlt, "\x1bb"},
		{"F1", 0, "\x1bOP"},
		{"F5", 0, "\x1b[15~"},
	}
	for _, c := range cases {
		got, ok := encodeKey(enc, ev, term, c.name, c.mods)
		if !ok || string(got) != c.want {
			t.Errorf("encodeKey(%q,%v) = %q,%v; want %q", c.name, c.mods, got, ok, c.want)
		}
	}
	// Application cursor mode (DECCKM) switches the arrows to SS3.
	term.VTWrite([]byte("\x1b[?1h"))
	if got, _ := encodeKey(enc, ev, term, key.NameUpArrow, 0); string(got) != "\x1bOA" {
		t.Errorf("up arrow in application cursor mode = %q", got)
	}
}

// TestScrollbackAndSelection scrolls the view into scrollback and selects a
// word there, as the pointer would.
func TestScrollbackAndSelection(t *testing.T) {
	e := testPane(t, 40, 5)
	g := newTestApp(t, e)
	g.mainView.setFontPx(14)
	for i := 0; i < 30; i++ {
		e.em.Write([]byte(fmt.Sprintf("line-%02d alpha beta\r\n", i)))
	}
	v := g.mainView
	v.update()
	if v.scrollbar.Total <= v.scrollbar.Len {
		t.Fatalf("no scrollback: %+v", v.scrollbar)
	}
	v.scrollLines(-10)
	v.update()
	first := ""
	for _, c := range v.lines[1].cells[:7] {
		first += c.text
	}
	if !strings.HasPrefix(first, "line-1") {
		t.Fatalf("top row after scrolling back = %q", first)
	}
	// Double click on the first word of the top row selects it.
	v.sel = termSelection{}
	v.sel.anchor = v.toScreen(image.Pt(2, 0))
	v.sel.head = v.sel.anchor
	v.sel.unit, v.sel.active = 2, true
	v.applySelection()
	if got := v.selectionText(); got != first {
		t.Fatalf("selected %q, want %q", got, first)
	}
	v.update()
	if v.lines[1].cells[0].bg != v.th.Selection {
		t.Error("selected cells are not highlighted")
	}
	v.typed()
	v.update()
	if v.scrollbar.Offset+v.scrollbar.Len != v.scrollbar.Total || v.sel.active {
		t.Errorf("typing did not return to the bottom and clear the selection: %+v", v.scrollbar)
	}
}

func TestBoxGlyphsCoverTheirCell(t *testing.T) {
	// Lines must reach the cell edges so that they join across cells.
	for _, r := range []rune{0x2500, 0x2502, 0x253c, 0x2550, 0x2551, 0x256c, 0x2501, 0x254b} {
		m := boxGlyph(r, 9, 19, 1)
		if m == nil {
			t.Fatalf("%U not drawn", r)
		}
		arms, _ := boxArms[r]
		edge := func(x, y int) bool { return m.AlphaAt(x, y).A > 0 }
		rowHas := func(y int) bool {
			for x := 0; x < 9; x++ {
				if edge(x, y) {
					return true
				}
			}
			return false
		}
		colHas := func(x int) bool {
			for y := 0; y < 19; y++ {
				if edge(x, y) {
					return true
				}
			}
			return false
		}
		if (arms[0] > 0) != rowHas(0) || (arms[2] > 0) != rowHas(18) || (arms[3] > 0) != colHas(0) || (arms[1] > 0) != colHas(8) {
			t.Errorf("%U arms %v do not match the edges it reaches", r, arms)
		}
	}
}

// TestGUIAttachLive attaches to a throwaway session (isolated socket dir),
// types into it, and resizes it.
func TestGUIAttachLive(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	b := nativeBackend{}
	if err := b.Create("gui-cat", t.TempDir(), "cat"); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("gui-cat")
	dirty := newDirtyFlag()
	p, err := startEmbedded("gui-cat", 60, 20, dirty)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if err := p.writeInput([]byte("hello-gui\r")); err != nil {
		t.Fatal(err)
	}
	read := func(y int) string {
		p.terminalMu.RLock()
		defer p.terminalMu.RUnlock()
		var sb strings.Builder
		for x := 0; x < p.em.Width(); x++ {
			if c := p.em.CellAt(x, y); c != nil {
				sb.WriteString(c.Content)
			}
		}
		return sb.String()
	}
	ok := false
	for i := 0; i < 50 && !ok; i++ {
		ok = strings.Contains(read(0), "hello-gui")
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("typed text never echoed; row0 = %q", read(0))
	}
	if !dirty.Load() {
		t.Error("dirty flag not set by output")
	}
	p.resize(90, 30)
	if w := p.em.Width(); w != 90 {
		t.Errorf("emulator width = %d, want 90", w)
	}
}

// TestRenderCost measures the CPU cost per frame on a 200x50 screen: every
// row changing, one row changing, and nothing changing.
func TestRenderCost(t *testing.T) {
	cols, rows := 200, 50
	e := testPane(t, cols, rows)
	g := newTestApp(t, e)
	g.mainView.setFontPx(14)
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	cw, ch := g.mainView.font.cellW, g.mainView.font.cellH
	gtx.Constraints = layout.Exact(image.Pt(cols*cw, rows*ch))
	frame := func() {
		ops.Reset()
		g.mainView.Layout(gtx, true)
	}
	fill := func(seed int) {
		var sb strings.Builder
		sb.WriteString("\x1b[H")
		for y := 0; y < rows; y++ {
			fmt.Fprintf(&sb, "\x1b[%dm", 31+(y+seed)%6)
			for x := 0; x < cols/10; x++ {
				fmt.Fprintf(&sb, "%09d ", (seed*7919+y*31+x)%1000000007)
			}
			sb.WriteString("\x1b[0m\r\n")
		}
		e.em.Write([]byte(sb.String()))
	}
	fill(0)
	frame()
	const n = 30
	start := time.Now()
	for i := 1; i <= n; i++ {
		fill(i)
		frame()
	}
	full := time.Since(start) / n
	start = time.Now()
	for i := 0; i < n; i++ {
		e.em.Write([]byte(fmt.Sprintf("\x1b[10;1Hline %d", i)))
		frame()
	}
	one := time.Since(start) / n
	start = time.Now()
	for i := 0; i < n; i++ {
		frame()
	}
	steady := time.Since(start) / n
	t.Logf("200x50 per frame (CPU, no GPU): all rows changed %v, one row changed %v, nothing changed %v", full, one, steady)
}

// guiDriver runs frames of the whole window through Gio's input router, so
// tests deliver the same events a real window does.
type guiDriver struct {
	t      *testing.T
	g      *guiApp
	router input.Router
	ops    op.Ops
}

func (d *guiDriver) frame() {
	d.ops.Reset()
	gtx := layout.Context{Ops: &d.ops, Now: time.Now(), Source: d.router.Source(),
		Constraints: layout.Exact(image.Pt(1100, 700)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	d.g.layout(gtx)
	d.router.Frame(&d.ops)
}

// TestGUITypingReachesTheSession types into a throwaway session (isolated
// socket directory) the way a window delivers keys: text as EditEvents,
// Enter as a key press.
func TestGUITypingReachesTheSession(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := nativeBackend{}
	if err := b.Create("gui-cat", t.TempDir(), "cat"); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("gui-cat")
	g := newGUIState()
	g.settings = defaultSettings()
	g.settings.GUIFontSize = defaultGUIFontSize
	g.applySessions(listSessionsWithError())
	g.selected = "gui-cat"
	g.windowFocused = true
	d := &guiDriver{t: t, g: g}
	defer g.shutdown()
	d.frame()
	d.frame()
	if g.main == nil {
		t.Fatalf("session not attached: %q", g.message)
	}
	d.router.Queue(key.EditEvent{Text: "hello-gui"})
	d.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	d.frame()
	screen := func() string {
		var sb strings.Builder
		for y := 0; y < g.main.em.Height(); y++ {
			for x := 0; x < g.main.em.Width(); x++ {
				if c := g.main.em.CellAt(x, y); c != nil {
					sb.WriteString(c.Content)
				}
			}
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	for i := 0; i < 50 && strings.Count(screen(), "hello-gui") < 2; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if n := strings.Count(screen(), "hello-gui"); n < 2 {
		t.Fatalf("typed text not echoed by cat (seen %d times):\n%s", n, screen())
	}
	// Ctrl+C reaches the program: lam's shortcuts need Ctrl+Shift.
	d.router.Queue(key.Event{Name: "C", Modifiers: key.ModCtrl, State: key.Press})
	d.frame()
	for i := 0; i < 50 && !g.main.done.Load(); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if !g.main.done.Load() {
		t.Error("Ctrl+C did not end cat")
	}
}

func TestGUIShortcutsOpenDialogs(t *testing.T) {
	e := testPane(t, 80, 24)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	d.frame()
	for _, c := range []struct {
		name key.Name
		want dialogKind
	}{{"N", dlgNewSession}, {"R", dlgRename}, {"W", dlgStop}, {"Q", dlgQuitAll}, {",", dlgSettings}} {
		d.router.Queue(key.Event{Name: c.name, Modifiers: key.ModCtrl | key.ModShift, State: key.Press})
		d.frame()
		if g.dlg.kind != c.want {
			t.Errorf("Ctrl+Shift+%s opened dialog %d, want %d", c.name, g.dlg.kind, c.want)
		}
		d.router.Queue(key.Event{Name: key.NameEscape, State: key.Press})
		d.frame()
		if g.dlg.kind != dlgNone {
			t.Errorf("Esc left dialog %d open", g.dlg.kind)
		}
	}
	// Typing into the rename field edits it.
	d.router.Queue(key.Event{Name: "R", Modifiers: key.ModCtrl | key.ModShift, State: key.Press})
	d.frame()
	d.router.Queue(key.Event{Name: key.NameDeleteBackward, State: key.Press}, key.EditEvent{Text: "X"})
	d.frame()
	if got := g.dlg.field.String(); got != "claude-laX" {
		t.Errorf("rename field = %q", got)
	}
}

// TestGUIMouseReachesProgram clicks in a pane whose program asked for mouse
// reports (SGR mode) and checks the press and the release it receives.
func TestGUIMouseReachesProgram(t *testing.T) {
	e := testPane(t, 80, 24)
	sent := make(chan []byte, 16)
	e.send = func(b []byte) { sent <- append([]byte(nil), b...) }
	g := newTestApp(t, e)
	e.em.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	cw, ch := g.mainView.font.cellW, g.mainView.font.cellH
	// The terminal starts below the header; aim at cell (5, 3) of the pane.
	origin := f32.Pt(float32(g.sidebarWidth(layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}, 1100)+1+gtx14()), float32(int(g.th.HeaderH)+int(g.th.TermPad)/2))
	at := origin.Add(f32.Pt(float32(5*cw+cw/2), float32(3*ch+ch/2)))
	d.router.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
	)
	d.frame()
	d.router.Queue(
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at},
	)
	d.frame()
	var got []string
	timeout := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case b := <-sent:
			got = append(got, string(b))
		case <-timeout:
			t.Fatalf("program received %q, want a press and a release", got)
		}
	}
	if got[0] != "\x1b[<0;6;4M" || got[1] != "\x1b[<0;6;4m" {
		t.Errorf("mouse reports = %q, want press and release at column 6, row 4", got)
	}
}

func gtx14() int { return int(defaultGUITheme().TermPad) }

// TestSmoothScrollMovesByPixels scrolls back by less than a row and checks
// that the view shifts by pixels, then settles on whole rows.
func TestSmoothScrollMovesByPixels(t *testing.T) {
	e := testPane(t, 40, 6)
	g := newTestApp(t, e)
	v := g.mainView
	v.setFontPx(14)
	for i := 0; i < 40; i++ {
		e.em.Write([]byte(fmt.Sprintf("row %02d\r\n", i)))
	}
	v.update()
	bottom := v.scrollbar.Offset
	v.scrollPixels(-float32(v.font.cellH) / 2)
	v.update()
	if v.subPx() == 0 || v.scrollbar.Offset != bottom {
		t.Fatalf("half a row up: sub %d, offset %d (bottom %d)", v.subPx(), v.scrollbar.Offset, bottom)
	}
	if !v.lines[0].valid {
		t.Error("the row above the viewport is not drawn while it partly shows")
	}
	v.scrollPixels(-float32(v.font.cellH))
	v.update()
	if v.scrollbar.Offset != bottom-1 {
		t.Errorf("a row and a half up: offset %d, want %d", v.scrollbar.Offset, bottom-1)
	}
	v.scrollPixels(1e6)
	v.update()
	if v.subPx() != 0 || v.scrollbar.Offset != bottom {
		t.Errorf("scrolling far down did not stop at the bottom: sub %d offset %d", v.subPx(), v.scrollbar.Offset)
	}
}

// TestGUIButtonsRespondToClicks clicks the header and sidebar buttons the
// way a mouse does.
func TestGUIButtonsRespondToClicks(t *testing.T) {
	e := testPane(t, 80, 24)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	// Where each button is, from its hover state.
	find := func(id string) (f32.Point, bool) {
		for y := float32(4); y < 120; y += 6 {
			for x := float32(0); x < 1100; x += 6 {
				d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y)})
				d.frame()
				if g.clickable(id).Hovered() {
					return f32.Pt(x+3, y), true
				}
			}
		}
		return f32.Point{}, false
	}
	click := func(p f32.Point) {
		d.router.Queue(
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p},
		)
		d.frame()
		d.frame()
	}
	for _, c := range []struct {
		id   string
		want dialogKind
	}{{"new", dlgNewSession}, {"rename", dlgRename}, {"stop", dlgStop}} {
		p, ok := find(c.id)
		if !ok {
			t.Fatalf("button %q not found", c.id)
		}
		click(p)
		if g.dlg.kind != c.want {
			t.Errorf("clicking %q opened dialog %d, want %d", c.id, g.dlg.kind, c.want)
		}
		g.closeDialog()
		d.frame()
	}
}

// TestGUIConnectAsksAboutUnknownHost connects to an SSH test server from the
// connect dialog: the unknown host key is asked about in the window, and
// answering yes completes the connection.
func TestGUIConnectAsksAboutUnknownHost(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())
	t.Cleanup(func() { builtinSSH, guiSSHOptions = false, nil })

	g := newGUIState()
	g.settings = defaultSettings()
	g.windowFocused = true
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.router.Queue(key.Event{Name: "O", Modifiers: key.ModCtrl | key.ModShift, State: key.Press})
	d.frame()
	if g.dlg.kind != dlgConnect {
		t.Fatalf("Ctrl+Shift+O opened dialog %d", g.dlg.kind)
	}
	d.router.Queue(key.Event{Name: "O", State: key.Release})
	d.router.Queue(key.EditEvent{Text: "tester@" + srv.addr})
	d.frame()
	d.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	d.frame()
	if !g.dlg.busy {
		t.Fatalf("not connecting: %q", g.dlg.err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for g.dlg.kind != dlgAsk && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		d.frame()
	}
	if g.dlg.kind != dlgAsk || !g.dlg.ask.yesNo {
		t.Fatalf("no host key question: dialog %d", g.dlg.kind)
	}
	if !strings.Contains(g.dlg.ask.text, ssh.FingerprintSHA256(hostKey.PublicKey())) {
		t.Errorf("question lacks the fingerprint: %q", g.dlg.ask.text)
	}
	d.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	d.frame()
	var res *connectResult
	for res == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		g.mu.Lock()
		res = g.connRes
		g.mu.Unlock()
	}
	if res == nil || res.err != nil {
		t.Fatalf("connection did not complete: %+v", res)
	}
	res.b.Close()
}

// A click inside the Help card stays in it; only a click outside dismisses it.
func TestGUIDialogCardIgnoresInsideClicks(t *testing.T) {
	e := testPane(t, 80, 24)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	d.frame()
	g.dlg = dialog{kind: dlgHelp}
	d.frame()
	d.frame()
	click := func(x, y float32) {
		p := f32.Pt(x, y)
		d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		d.frame()
		d.frame()
	}
	click(550, 120) // the card's title area
	if g.dlg.kind != dlgHelp {
		t.Fatalf("a click inside the card closed it (dialog %d)", g.dlg.kind)
	}
	click(700, 690) // below the card: the dimmed window
	if g.dlg.kind != dlgNone {
		t.Fatalf("a click outside left dialog %d open", g.dlg.kind)
	}
}
