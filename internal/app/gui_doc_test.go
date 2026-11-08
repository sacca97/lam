//go:build gui

package app

import (
	"fmt"
	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/unit"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleMarkdown = "# lam document viewer\n\nA paragraph with *emphasis*, **strong text**, `inline code` and a [link](https://example.com). " +
	"It is long enough to wrap onto several lines so that the wrapping can be checked against the width of the page.\n\n" +
	"## Lists\n\n- first item\n- second item with a [relative link](other.md)\n  - nested item\n- [x] a finished task\n- [ ] an open task\n\n1. one\n2. two\n\n" +
	"> A quotation that\n> goes over two lines.\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\n" +
	"| Name | Role | Notes |\n|------|------|-------|\n| lam | sessions | keeps agents running |\n| viewer | documents | markdown and PDF |\n\n---\n\n### Done\n\nThe end.\n"

func waitDoc(t *testing.T, g *guiApp) {
	t.Helper()
	for i := 0; i < 200; i++ {
		g.mu.Lock()
		loading := g.doc != nil && g.doc.loading
		g.mu.Unlock()
		if !loading {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the document never loaded")
}

func shootDoc(t *testing.T, g *guiApp, name string) {
	img := renderGUI(t, g, 1240, 720, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(filepath.Join(dir, name))
	png.Encode(f, img)
	f.Close()
}

func TestGUIDocumentMarkdown(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.md")
	os.WriteFile(file, []byte(sampleMarkdown), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", file)
	waitDoc(t, g)
	shootDoc(t, g, "gui-doc-markdown.png")
	if g.doc == nil || g.doc.err != "" || g.doc.md == nil || len(g.doc.md.Items) == 0 {
		t.Fatalf("not laid out: %+v", g.doc)
	}
	if len(g.doc.links) == 0 {
		t.Fatal("no links were found to click")
	}
	g.closeDoc()
}

func TestGUIDocumentPDF(t *testing.T) {
	file := filepath.Join(t.TempDir(), "paper.pdf")
	os.WriteFile(file, []byte(tinyPDF), 0o644)
	if p := os.Getenv("LAM_TEST_PDF"); p != "" {
		file = p
	}
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", file)
	waitDoc(t, g)
	if g.doc.err != "" {
		t.Fatal(g.doc.err)
	}
	renderGUI(t, g, 1240, 720, 1) // asks for the visible pages
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		g.doc.mu.Lock()
		ready := g.doc.pages[0] != nil && g.doc.pages[0].ready
		g.doc.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	shootDoc(t, g, "gui-doc-pdf.png")
	g.doc.mu.Lock()
	ready := g.doc.pages[0] != nil && g.doc.pages[0].ready
	g.doc.mu.Unlock()
	if !ready {
		t.Fatal("page 1 was never rendered")
	}
	g.closeDoc()
}

func TestGUIDocumentErrors(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", filepath.Join(t.TempDir(), "missing.md"))
	waitDoc(t, g)
	if g.doc.err == "" {
		t.Fatal("a missing file opened")
	}
	shootDoc(t, g, "gui-doc-error.png")
	g.openDocument("nowhere", "/x/y.md")
	waitDoc(t, g)
	if g.doc.err == "" {
		t.Fatal("a host that is not connected opened a file")
	}
}

func TestAbsoluteDocPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"~/a/b.md", "/home/u/a/b.md"},
		{"b.md", "/work/b.md"},
		{"../c/d.pdf", "/c/d.pdf"},
		{"/abs/x.md", "/abs/x.md"},
		{" ~ ", "/home/u"},
	} {
		if got := absoluteDocPath(c.in, "/work/proj", "/home/u"); c.in == "../c/d.pdf" {
			if got != "/work/c/d.pdf" {
				t.Errorf("%q -> %q", c.in, got)
			}
		} else if c.in == "b.md" {
			if got != "/work/proj/b.md" {
				t.Errorf("%q -> %q", c.in, got)
			}
		} else if got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}

// The open dialog lists the session's folder with its folders and documents,
// and clicking a file opens it.
func TestGUIOpenDocumentDialogListsAndOpens(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "paper.pdf"), []byte(tinyPDF), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	// The dialog starts in the folder of the session being looked at.
	g.mu.Lock()
	for i := range g.sessions {
		if g.sessions[i].Name == g.selected {
			g.sessions[i].CWD = dir
		}
	}
	g.mu.Unlock()
	g.openDocumentDialog()
	for i := 0; i < 100; i++ {
		g.mu.Lock()
		done := !g.dlg.pick.loading
		g.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var labels []string
	for _, r := range g.pickRows() {
		labels = append(labels, r.label)
	}
	want := []string{"..", "sub", "paper.pdf", "readme.md"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %q, want %q", labels, want)
	}
	shootDoc(t, g, "gui-doc-picker.png")
	// Typing narrows the list; the arrow keys and Enter open a row.
	g.dialogText("rea")
	if rows := g.pickRows(); len(rows) != 1 || rows[0].label != "readme.md" {
		t.Fatalf("after typing: %+v", rows)
	}
	g.pickKey(key.Event{Name: key.NameDownArrow})
	g.pickKey(key.Event{Name: key.NameReturn})
	waitDoc(t, g)
	if g.doc == nil || filepath.Base(g.doc.path) != "readme.md" || g.doc.err != "" {
		t.Fatalf("doc = %+v", g.doc)
	}
}

// Folders can be walked in and out of: into one from the list, and out and back
// with up, back and forward, or by a step of the path.
func TestGUIOpenDocumentDialogNavigation(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "inner.md"), []byte("# in"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mu.Lock()
	for i := range g.sessions {
		if g.sessions[i].Name == g.selected {
			g.sessions[i].CWD = dir
		}
	}
	g.mu.Unlock()
	g.openDocumentDialog()
	settle := func() {
		for i := 0; i < 100; i++ {
			g.mu.Lock()
			done := !g.dlg.pick.loading
			g.mu.Unlock()
			if done {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the folder never listed")
	}
	names := func() string {
		var out []string
		for _, r := range g.pickRows() {
			out = append(out, r.label)
		}
		return strings.Join(out, ",")
	}
	settle()
	start := g.dlg.field.String()
	if names() != "..,sub" {
		t.Fatalf("start: %s", names())
	}
	g.pickActivate(g.pickRows()[1]) // into sub
	settle()
	if !strings.HasSuffix(g.dlg.field.String(), "/sub/") || names() != "..,deep,inner.md" {
		t.Fatalf("in sub: %q %s", g.dlg.field.String(), names())
	}
	g.pickActivate(g.pickRows()[1]) // into deep
	settle()
	crumbs := g.pickCrumbs()
	if last := crumbs[len(crumbs)-1]; last.label != "deep" || !strings.HasSuffix(last.target, "/sub/deep/") {
		t.Fatalf("crumbs = %+v", crumbs)
	}
	g.pickGo(crumbs[len(crumbs)-2].target, true) // a step of the path: back to sub
	settle()
	if !strings.HasSuffix(g.dlg.field.String(), "/sub/") {
		t.Fatalf("crumb went to %q", g.dlg.field.String())
	}
	g.pickUp()
	settle()
	if g.dlg.field.String() != start {
		t.Fatalf("up went to %q, want %q", g.dlg.field.String(), start)
	}
	g.pickBack() // history: start, sub, deep, sub, start -> back is sub
	settle()
	if !strings.HasSuffix(g.dlg.field.String(), "/sub/") {
		t.Fatalf("back went to %q", g.dlg.field.String())
	}
	g.pickForward()
	settle()
	if g.dlg.field.String() != start {
		t.Fatalf("forward went to %q", g.dlg.field.String())
	}
	// With a row picked, right goes in and left goes up.
	g.pickKey(key.Event{Name: key.NameDownArrow}) // ..
	g.pickKey(key.Event{Name: key.NameDownArrow}) // sub
	g.pickKey(key.Event{Name: key.NameRightArrow})
	settle()
	if !strings.HasSuffix(g.dlg.field.String(), "/sub/") {
		t.Fatalf("right went to %q", g.dlg.field.String())
	}
	shootDoc(t, g, "gui-doc-picker-sub.png")
}

func TestGUIOpenDocumentDialogWheelScrolls(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("doc%02d.md", i)), []byte("# x"), 0o644)
	}
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mu.Lock()
	for i := range g.sessions {
		if g.sessions[i].Name == g.selected {
			g.sessions[i].CWD = dir
		}
	}
	g.mu.Unlock()
	d := &guiDriver{t: t, g: g}
	d.frame()
	g.openDocumentDialog()
	for i := 0; i < 100; i++ {
		g.mu.Lock()
		done := !g.dlg.pick.loading
		g.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.frame()
	d.frame()
	at := time.Duration(0)
	scroll := func(x, y, dy float32) {
		at += time.Millisecond
		d.router.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(x, y), Scroll: f32.Pt(0, dy), Time: at})
		d.frame()
	}
	move := func(x, y float32) {
		at += time.Millisecond
		d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: at})
		d.frame()
	}
	pk := g.dlg.pick
	// The wheel works on a pointer that has not moved since the dialog opened.
	before := pk.offset
	scroll(620, 400, 40)
	if pk.offset <= before {
		t.Fatalf("a wheel turn over a list the pointer never moved over did nothing")
	}
	// The pointer arrives over the list: the first move places it, the second enters.
	move(620, 400)
	move(621, 401)
	// Then it is over the list, still or moving a little.
	for i := 0; i < 10; i++ {
		before := pk.offset
		scroll(620, 400, 40)
		if pk.offset <= before {
			t.Fatalf("scroll %d: offset %v did not grow from %v", i, pk.offset, before)
		}
		move(620+float32(i), 380+float32(i))
	}
	for i := 0; i < 10; i++ {
		before := pk.offset
		scroll(620, 400, -40)
		if pk.offset >= before && before > 0 {
			t.Fatalf("scroll back %d: offset %v did not shrink from %v", i, pk.offset, before)
		}
	}
}

// A click on a row of the list goes into the folder or opens the file, with the
// wheel area above the rows.
func TestGUIOpenDocumentDialogClicksRows(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# a"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.mu.Lock()
	for i := range g.sessions {
		if g.sessions[i].Name == g.selected {
			g.sessions[i].CWD = dir
		}
	}
	g.mu.Unlock()
	d := &guiDriver{t: t, g: g}
	d.frame()
	g.openDocumentDialog()
	for i := 0; i < 100 && func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.dlg.pick.loading }(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	d.frame()
	d.frame()
	at := time.Duration(0)
	move := func(x, y float32) {
		for i := 0; i < 2; i++ {
			at += time.Millisecond
			d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: at})
			d.frame()
		}
	}
	click := func(x, y float32) {
		move(x, y)
		p := f32.Pt(x, y)
		d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Time: at + time.Millisecond},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: at + 2*time.Millisecond})
		d.frame()
		d.frame()
	}
	// Rows: "Parent folder", then sub, then a.md. Find the list by clicking down it.
	var subY float32
	for y := float32(250); y < 560; y += 6 {
		move(500, y)
		if g.clickable("pick-1").Hovered() {
			subY = y
			break
		}
	}
	if subY == 0 {
		t.Fatal("the second row never got the pointer")
	}
	click(500, subY)
	if !strings.HasSuffix(g.dlg.field.String(), "/sub/") {
		t.Fatalf("a click on the folder row left the field at %q", g.dlg.field.String())
	}
}

// Documents are tabs next to the session: several can be open, Ctrl+W (Cmd+W on
// a Mac) closes the one in front, and Ctrl+Tab goes round them.
func TestGUIDocumentTabs(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "alpha.md"), filepath.Join(dir, "beta.pdf")
	os.WriteFile(a, []byte("# Alpha\n\ntext"), 0o644)
	os.WriteFile(b, []byte(tinyPDF), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", a)
	waitDoc(t, g)
	first := g.doc
	g.openDocument("", b)
	waitDoc(t, g)
	second := g.doc
	if len(g.docs) != 2 || g.doc != second {
		t.Fatalf("docs=%d front=%v", len(g.docs), g.doc == second)
	}
	g.openDocument("", a) // the same file: its tab comes forward, no third tab
	if len(g.docs) != 2 || g.doc != first {
		t.Fatalf("reopening made %d tabs", len(g.docs))
	}
	shootDoc(t, g, "gui-doc-tabs.png")
	g.cycleTab(1)
	if g.doc != second {
		t.Fatal("Ctrl+Tab did not go to the next tab")
	}
	g.cycleTab(1)
	if g.doc != nil {
		t.Fatal("after the last document the session should come to the front")
	}
	g.cycleTab(-1)
	if g.doc != second {
		t.Fatal("Ctrl+Shift+Tab did not go back")
	}
	// Ctrl+W closes the tab in front, and the one beside it comes forward.
	if !g.docKey(key.Event{Name: "W", Modifiers: key.ModCtrl}) || len(g.docs) != 1 || g.doc != first {
		t.Fatalf("ctrl+W: docs=%d", len(g.docs))
	}
	if !g.docKey(key.Event{Name: "W", Modifiers: key.ModCommand}) || len(g.docs) != 0 || g.doc != nil {
		t.Fatalf("cmd+W: docs=%d", len(g.docs))
	}
	// With a session in front, Ctrl+W is not taken: it belongs to the program.
	if g.docKey(key.Event{Name: "W", Modifiers: key.ModCtrl}) {
		t.Fatal("Ctrl+W was taken from the session")
	}
}

// Clicking a tab brings it forward, and its cross closes it.
func TestGUIDocumentTabsClick(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "alpha.md"), filepath.Join(dir, "beta.md")
	os.WriteFile(a, []byte("# A"), 0o644)
	os.WriteFile(b, []byte("# B"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	g.openDocument("", a)
	waitDoc(t, g)
	g.openDocument("", b)
	waitDoc(t, g)
	d.frame()
	d.frame()
	at := time.Duration(0)
	click := func(x, y float32) {
		for i := 0; i < 2; i++ {
			at += time.Millisecond
			d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: at})
			d.frame()
		}
		p := f32.Pt(x, y)
		d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Time: at + time.Millisecond},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: at + 2*time.Millisecond})
		d.frame()
		d.frame()
	}
	click(330, 23) // the session's tab
	if g.doc != nil {
		t.Fatal("clicking the session tab did not bring the session forward")
	}
	click(430, 23) // alpha's tab
	if g.doc == nil || filepath.Base(g.doc.path) != "alpha.md" {
		t.Fatalf("clicking a tab did not bring it forward: %+v", g.doc)
	}
	click(508, 23) // alpha's cross
	if len(g.docs) != 1 || filepath.Base(g.docs[0].path) != "beta.md" {
		t.Fatalf("the cross left %d tabs", len(g.docs))
	}
}

// A page that cannot be drawn is not tried again on every frame, and a render
// that finishes after a reload does not fill the new document's pages.
func TestGUIPDFPagesNotRetriedAndNotStale(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.pdf")
	os.WriteFile(file, []byte(tinyPDF), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", file)
	waitDoc(t, g)
	dv := g.doc
	old := dv.pdf
	// Simulate a render failing: the width is recorded, so need is false.
	dv.mu.Lock()
	dv.pages = map[int]*pdfPage{0: {width: 600}}
	pg := dv.pages[0]
	dv.mu.Unlock()
	if need := pg == nil || (pg.width != 600 && !pg.loading); need {
		t.Fatal("a failed width would be retried")
	}
	// A reload replaces the PDF; a render of the old one must not store its page.
	g.loadDocument(dv)
	waitDoc(t, g)
	dv.mu.Lock()
	dv.pages = map[int]*pdfPage{0: {loading: true}}
	dv.mu.Unlock()
	g.renderPage(dv, old, 0, 300)
	time.Sleep(500 * time.Millisecond)
	dv.mu.Lock()
	ready := dv.pages[0].ready
	dv.mu.Unlock()
	if ready {
		t.Fatal("a stale render filled the reloaded document")
	}
}

// Tab cycling skips the session tab when there is no session to show.
func TestGUICycleTabWithoutSession(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	dir := t.TempDir()
	for _, n := range []string{"a.md", "b.md"} {
		os.WriteFile(filepath.Join(dir, n), []byte("# x"), 0o644)
		g.openDocument("", filepath.Join(dir, n))
		waitDoc(t, g)
	}
	g.selected = ""
	g.emptyShell = nil
	g.mu.Lock()
	g.sessions = nil
	g.mu.Unlock()
	g.doc = g.docs[0]
	g.cycleTab(1)
	g.cycleTab(1)
	if g.doc == nil {
		t.Fatal("cycling landed on a session that has no tab")
	}
}

func TestFollowLinkDecodesAndKeepsAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "my notes.md"), []byte("# n"), 0o644)
	start := filepath.Join(dir, "start.md")
	os.WriteFile(start, []byte("# s"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", start)
	waitDoc(t, g)
	g.followLink("my%20notes.md")
	waitDoc(t, g)
	if g.doc.err != "" || filepath.Base(g.doc.path) != "my notes.md" {
		t.Fatalf("doc = %+v", g.doc)
	}
	g.followLink(filepath.Join(dir, "start.md"))
	waitDoc(t, g)
	if g.doc.path != start {
		t.Fatalf("an absolute link opened %q", g.doc.path)
	}
}

// Links with a scheme go to the browser only for the schemes a document can be
// trusted with; the rest are refused.
func TestFollowLinkRefusesOddSchemes(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	file := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(file, []byte("# a"), 0o644)
	g.openDocument("", file)
	waitDoc(t, g)
	n := len(g.docs)
	for _, dest := range []string{"file:///etc/passwd", "smb://evil/share", "--flag://x", "javascript:alert(1)"} {
		g.message = ""
		g.followLink(dest)
		if len(g.docs) != n {
			t.Fatalf("%s opened a tab", dest)
		}
		if g.message == "" {
			t.Fatalf("%s was not refused out loud", dest)
		}
	}
}

func TestDirFieldRoot(t *testing.T) {
	if got := dirField("/", localDirectorySource{}); got != "/" {
		t.Fatalf("root = %q", got)
	}
}

// A heading named in a #link is where the page scrolls to.
func TestGUIDocumentAnchorLinksScroll(t *testing.T) {
	var src strings.Builder
	src.WriteString("# Top\n\n[jump](#the-end) and [again](#top-1)\n\n")
	for i := 0; i < 60; i++ {
		src.WriteString("A paragraph of filler text that takes up room on the page.\n\n")
	}
	src.WriteString("## The end!\n\nLast words.\n\n# Top\n")
	file := filepath.Join(t.TempDir(), "long.md")
	os.WriteFile(file, []byte(src.String()), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", file)
	waitDoc(t, g)
	renderGUI(t, g, 1240, 720, 1)
	if _, ok := g.doc.md.Anchors["the-end"]; !ok {
		t.Fatalf("anchors = %v", g.doc.md.Anchors)
	}
	if _, ok := g.doc.md.Anchors["top-1"]; !ok {
		t.Fatalf("a repeated heading has no -1 anchor: %v", g.doc.md.Anchors)
	}
	g.followLink("#the-end")
	if g.doc.scrollY < 500 {
		t.Fatalf("scrollY = %v after the link", g.doc.scrollY)
	}
}

// Dragging across text selects it and Ctrl+C copies it; the scroll bar can be
// dragged.
func TestGUIDocumentSelectionAndScrollBar(t *testing.T) {
	var src strings.Builder
	src.WriteString("# Heading\n\nfirst paragraph here\n\n")
	for i := 0; i < 80; i++ {
		src.WriteString("filler filler filler filler\n\n")
	}
	file := filepath.Join(t.TempDir(), "sel.md")
	os.WriteFile(file, []byte(src.String()), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	g.openDocument("", file)
	waitDoc(t, g)
	d := &guiDriver{t: t, g: g}
	d.frame()
	d.frame()
	d.frame()
	dv := g.doc
	var first, last docPos
	for i, it := range dv.md.Items {
		if it.Kind == mdText_ && it.Text == "first paragraph here" {
			first = docPos{i, 0}
			last = docPos{i, len(it.Text)}
		}
	}
	if last.off == 0 {
		t.Fatal("paragraph not found")
	}
	dv.selA, dv.selB, dv.hasSel = first, last, true
	if got := dv.selectionText(func(it mdItem) float32 { return float32(len(it.Text)) * it.Size * 0.5 }); got != "first paragraph here" {
		t.Fatalf("selected %q", got)
	}
	dv.selB.off = 5
	if got := dv.selectionText(func(it mdItem) float32 { return float32(len(it.Text)) * it.Size * 0.5 }); got != "first" {
		t.Fatalf("selected %q", got)
	}

	// Drag the thumb to the bottom.
	_, _, ok := dv.thumb(layoutCtxFor(d))
	if !ok {
		t.Fatal("no scroll bar on a long page")
	}
	at := time.Duration(0)
	send := func(kind pointer.Kind, x, y float32) {
		at += time.Millisecond
		// A drag is a move with the button down; Gio makes the Drag event.
		var buttons pointer.Buttons
		if kind == pointer.Press || kind == pointer.Drag {
			buttons = pointer.ButtonPrimary
		}
		if kind == pointer.Drag {
			kind = pointer.Move
		}
		d.router.Queue(pointer.Event{Kind: kind, Source: pointer.Mouse, Buttons: buttons, Position: f32.Pt(x, y), Time: at})
		d.frame()
	}
	// The pane starts right of the sidebar and below the header; the bar is at
	// the right edge. Find the pane from the thumb's position by probing.
	g.doc.scrollY = 0
	d.frame()
	hit := false
	for x := float32(1095); x > 1000 && !hit; x -= 4 {
		for y := float32(60); y < 400 && !hit; y += 8 {
			send(pointer.Move, x, y)
			send(pointer.Press, x, y)
			hit = dv.dragBar
			if hit {
				send(pointer.Drag, x, 690)
				send(pointer.Release, x, 690)
			} else {
				send(pointer.Release, x, y)
			}
		}
	}
	if !hit || dv.scrollY <= 0 {
		t.Fatalf("the scroll bar did not drag (hit %v, scrollY %v)", hit, dv.scrollY)
	}
}

func layoutCtxFor(*guiDriver) layout.Context {
	return layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
}

// A press of the wheel on a document's tab closes it; on the session's tab it
// does nothing.
func TestGUIDocumentTabClosesOnMiddleClick(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "alpha.md"), filepath.Join(dir, "beta.md")
	os.WriteFile(a, []byte("# A"), 0o644)
	os.WriteFile(b, []byte("# B"), 0o644)
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	d := &guiDriver{t: t, g: g}
	g.openDocument("", a)
	waitDoc(t, g)
	g.openDocument("", b)
	waitDoc(t, g)
	d.frame()
	d.frame()
	at := time.Duration(0)
	middle := func(x, y float32) {
		p := f32.Pt(x, y)
		for i := 0; i < 2; i++ {
			at += time.Millisecond
			d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p, Time: at})
			d.frame()
		}
		d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonTertiary, Position: p, Time: at + time.Millisecond},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p, Time: at + 2*time.Millisecond})
		d.frame()
		d.frame()
	}
	middle(330, 23) // the session's tab
	if len(g.docs) != 2 {
		t.Fatalf("the wheel closed something on the session's tab: %d documents", len(g.docs))
	}
	middle(430, 23) // alpha's tab
	if len(g.docs) != 1 || filepath.Base(g.docs[0].path) != "beta.md" {
		t.Fatalf("the wheel left %d tabs", len(g.docs))
	}
}
