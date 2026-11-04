//go:build gui

package app

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	giofont "gioui.org/font/opentype"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"golang.org/x/image/math/fixed"
)

// The GUI offers what the TUI does, the way a desktop application does it:
// a click picks the pane that gets the keyboard, actions are buttons, menus
// and dialogs, and lam's own shortcuts use Ctrl+Shift so that every key a
// program understands (Ctrl+D, Ctrl+Q, Ctrl+arrows) reaches the program.

const (
	defaultGUIFontSize = 14
	minGUIFontSize     = 8
	maxGUIFontSize     = 32
	defaultSidebarDp   = 268
	minSidebarDp       = 160
)

// What receives the keyboard.
type guiFocus int

const (
	focusMain guiFocus = iota
	focusTerminal
	focusFilter
)

type guiApp struct {
	copyMu              sync.Mutex
	copyQueue           []string // text programs copied with OSC 52, for the next frame
	copyHooked          bool
	th                  guiTheme
	win                 *app.Window
	dirty               *dirtyFlag
	shaper              *text.Shaper
	fb                  *fallbackFonts
	settings            uiSettings
	launchDir           string
	localHost           string // cached name of this machine
	autoConnect         string // a host to connect to once the window is up (lam ssh HOST)
	moreShown, moreNext string // the session whose three dots are shown, last frame and this one
	plusShown, plusNext string // the same for the + on a project heading
	pathSource          sessionDirectorySource
	remote              bool
	doc                 *docView   // the document in front, or nil when a session is
	docs                []*docView // the open documents, as tabs
	docText             *text.Shaper
	docSans             font.Typeface
	docShapes           map[docShapeKey]docShaped
	mb                  *multiBackend // this machine's sessions and the connected host's
	restorer            sessionRestorer
	upd                 guiUpdate // the newer release, if any, and the install

	// Shared with the list refresher and notification callbacks.
	mu            sync.Mutex
	sessions      []sessionInfo
	rowsVer       atomic.Uint64 // bumped when sessions change in place, for the row cache
	listed        bool          // the list was read at least once
	listErr       string
	openReq       string
	windowFocused bool
	mute          bool
	nsView        uintptr              // the macOS window's view, to style its title bar
	statusSince   map[string]time.Time // when each session's status last changed
	lastStatus    map[string]string
	askReq        *sshAsk        // a question from the SSH client, to show
	connRes       *connectResult // a finished connection attempt

	// Everything below belongs to the UI goroutine.
	connID    int          // the connection attempt that counts
	gen       atomic.Int64 // counts backend switches
	askPrev   dialog       // the connect dialog, while a question covers it
	selected  string
	filter    string
	collapsed map[string]bool
	focus     guiFocus
	rowCache  rowCache
	nextAge   time.Time // the soonest a visible age label changes
	shapes    map[shapeKey]shapedLine

	main       *embedded
	mainView   *termView
	attachFail map[string]time.Time
	emptyShell *embedded
	emptyTried bool

	terminals   map[string]*embedded // owner session → its terminal
	termVisible bool
	termView    *termView

	dlg      dialog
	message  string
	msgUntil time.Time

	rowClicks map[string]*widget.Clickable
	rowTags   map[string]*bool
	headers   map[string]*widget.Clickable
	buttons   map[string]*widget.Clickable
	hits      map[string]bool // buttons clicked, read before they are laid out
	list      layout.List
	keyTag    bool
	lastMods  key.Modifiers
	skipEdits bool // drop typed text until the next key press
	hideSide  bool
	sideTag   bool // sidebar edge drag
	splitTag  bool // terminal divider drag
	dragging  int  // 0 none, 1 sidebar edge, 2 divider
	lastEdge  time.Time
	split     struct {
		body  image.Rectangle // the main area's panes, in its own coordinates
		right bool
	}
	menu      *rowMenu
	pressTag  bool
	lastPress image.Point // window position of the last pointer press
	finishes  *finishWatcher
	maximized bool
}

// fontSize is the terminal text size in sp: the zoomed size, or 14. The
// desktop picks the font, not its size: desktop sizes are set for menus.
func (g *guiApp) fontSize() float32 {
	if g.settings.GUIFontSize > 0 {
		return g.settings.GUIFontSize
	}
	return defaultGUIFontSize
}

// guiBuilt says whether this binary can open the window.
const guiBuilt = true

func guiCmd(args []string) {
	// Started from the Dock or Finder, the window has no shell's PATH, and the
	// agents installed in ~/.local/bin or ~/.bun/bin would not be found.
	go useLoginShellPATH()
	g := newGUIApp(nil)
	if len(args) >= 2 && args[0] == "ssh" {
		// Connected from inside the window, so a host key, a password or a
		// failure is shown there: this process has no terminal of its own.
		g.autoConnect = args[1]
	}
	go func() {
		err := g.run()
		if c, ok := backend().(interface{ Close() }); ok {
			c.Close()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "lam gui:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

// newGUIState builds the application without touching any session.
func newGUIState() *guiApp {
	g := &guiApp{
		th:          defaultGUITheme(),
		dirty:       newDirtyFlag(),
		settings:    loadSettings(),
		finishes:    newFinishWatcher(),
		list:        layout.List{Axis: layout.Vertical},
		collapsed:   map[string]bool{},
		attachFail:  map[string]time.Time{},
		terminals:   map[string]*embedded{},
		rowClicks:   map[string]*widget.Clickable{},
		rowTags:     map[string]*bool{},
		headers:     map[string]*widget.Clickable{},
		buttons:     map[string]*widget.Clickable{},
		hits:        map[string]bool{},
		statusSince: map[string]time.Time{},
		lastStatus:  map[string]string{},
		pathSource:  localDirectorySource{},
		mb:          newMultiBackend(nativeBackend{}),
	}
	if g.settings.GUIFontSize < minGUIFontSize || g.settings.GUIFontSize > maxGUIFontSize {
		g.settings.GUIFontSize = 0 // follow the desktop
	}
	g.mute = g.settings.MuteNotifications
	name := g.settings.GUITheme
	if env := os.Getenv("LAM_GUI_THEME"); env != "" {
		name = env
	}
	g.th = themeNamed(name)
	for _, cwd := range g.settings.CollapsedProjects {
		g.collapsed[cwd] = true
	}
	g.shaper = newUIShaper()
	g.fb = newFallbackFonts(func() {
		if g.win != nil {
			g.win.Invalidate()
		}
	})
	g.mainView = newTermView(&g.th, g.fb)
	g.termView = newTermView(&g.th, g.fb)
	g.mainView.onPress = func() { g.setFocus(focusMain) }
	g.termView.onPress = func() { g.setFocus(focusTerminal) }
	return g
}

// newGUIApp builds the application on this machine's sessions, plus a remote
// host's when one is given.
func newGUIApp(remote SessionBackend) *guiApp {
	g := newGUIState()
	if remote != nil {
		g.mb.SetRemote(remote)
		g.remote = true
	}
	setBackend(g.mb)
	if src, ok := g.mb.local.(sessionDirectorySource); ok {
		g.pathSource = src
	}
	if dir, err := g.pathSource.WorkingDirectory(); err == nil {
		g.launchDir = dir
	}
	g.selected = lastSelectedSession()
	g.applySessions(listSessionsWithError())
	if r, ok := g.mb.local.(sessionRestorer); ok {
		g.restorer = r
		if entries, err := r.RestorableSessions(); err == nil && len(entries) > 0 {
			g.dlg = dialog{kind: dlgRestore, restore: entries}
		}
	}
	return g
}

// applySessions installs a freshly read list and reports whether anything the
// window shows changed.
func (g *guiApp) applySessions(list []sessionInfo, err error) (changed bool) {
	if err == nil {
		list = hideOwnedTerminals(list)
		slices.SortStableFunc(list, func(a, b sessionInfo) int {
			switch {
			case sessionLess(a, b):
				return -1
			case sessionLess(b, a):
				return 1
			}
			return 0
		})
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		changed = g.listErr != err.Error()
		g.listErr = err.Error()
		return changed
	}
	changed = g.listErr != "" || !g.listed || !slices.Equal(g.sessions, list)
	g.listErr, g.listed = "", true
	if changed {
		g.sessions = list
		g.rowsVer.Add(1)
	}
	// Note when each session's status changes, for "working · 4m". A status
	// seen for the first time has no known start.
	now := time.Now()
	seen := map[string]bool{}
	for _, s := range list {
		seen[s.Name] = true
		status := s.Activity
		if s.Unread {
			status = "unread"
		}
		if prev, ok := g.lastStatus[s.Name]; ok && prev != status {
			g.statusSince[s.Name] = now
		}
		g.lastStatus[s.Name] = status
	}
	for name := range g.lastStatus {
		if !seen[name] {
			delete(g.lastStatus, name)
			delete(g.statusSince, name)
		}
	}
	return changed
}

// refreshSessions re-reads the session list and reports whether the window
// needs a redraw because of it.
func (g *guiApp) refreshSessions() (changed bool) {
	gen := g.gen.Load()
	list, err := listSessionsWithError()
	if g.gen.Load() != gen {
		return false // the window moved to another host meanwhile
	}
	changed = g.applySessions(list, err)
	if err != nil {
		return changed
	}
	g.mu.Lock()
	selected, focused, mute := g.selected, g.windowFocused, g.mute
	g.mu.Unlock()
	if g.remote {
		// The agents run elsewhere; this machine must stay up to keep the link.
		for _, s := range list {
			if s.Host != "" && s.Activity == "active" {
				guiAwake.Touch()
				break
			}
		}
	}
	for _, s := range g.finishes.update(hideOwnedTerminals(list), time.Now()) {
		if mute || (focused && s.Name == selected) {
			continue
		}
		title, body := finishNotification(s, shortenHomePath(s.CWD, g.sourceFor(s.Host)), hostOrNative(s.Host))
		name := s.Name
		notify(title, body, func() {
			g.mu.Lock()
			g.openReq = name
			g.mu.Unlock()
			g.win.Perform(system.ActionRaise)
			g.win.Invalidate()
		})
	}
	return changed
}

// pollInterval is how long to wait between session list reads: a window nobody
// is looking at polls less often, and so does a remote host, where each read
// costs a round trip.
func (g *guiApp) pollInterval() time.Duration {
	d := time.Second
	g.mu.Lock()
	if !g.windowFocused {
		d = 3 * time.Second
	}
	g.mu.Unlock()
	if g.remote {
		d *= 2
	}
	return d
}

var guiAwake = newKeepAwake()

// setTheme switches between the dark and the light look while running.
func (g *guiApp) setTheme(name string) {
	g.th = themeNamed(name)
	g.mainView.applyColors()
	g.termView.applyColors()
	g.styleWindow()
	g.win.Invalidate()
}

func (g *guiApp) run() error {
	g.win = new(app.Window)
	g.win.Option(app.Title("lam"), app.Size(1240, 780), app.MinSize(560, 340))
	if guiOwnTitleBar {
		g.win.Option(app.Decorated(false))
	}
	go func() {
		for range g.dirty.wake {
			g.win.Invalidate()
		}
	}()
	go func() {
		for {
			time.Sleep(g.pollInterval())
			if g.refreshSessions() {
				g.win.Invalidate()
			}
		}
	}()
	g.startUpdateChecks()
	// Killed rather than closed: close the window the normal way, so the
	// temporary shell is still cleaned up.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(stop)
	go func() {
		for range stop {
			g.win.Perform(system.ActionClose)
		}
	}()
	var ops op.Ops
	for {
		ev := g.win.Event()
		g.platformEvent(ev)
		switch e := ev.(type) {
		case app.DestroyEvent:
			g.shutdown()
			return e.Err
		case app.ConfigEvent:
			g.mu.Lock()
			changed := g.windowFocused != e.Config.Focused
			g.windowFocused = e.Config.Focused
			g.mu.Unlock()
			g.maximized = e.Config.Mode == app.Maximized || e.Config.Mode == app.Fullscreen
			if changed {
				if v := g.focusedView(); v != nil { // only the pane that has the keyboard
					v.windowFocus(e.Config.Focused)
				}
			}
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			g.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// shutdown leaves every session and terminal running, as detaching from the
// TUI does, except the temporary shell shown when there were no sessions.
func (g *guiApp) shutdown() {
	g.mainView.detach()
	g.termView.detach()
	if g.main != nil {
		g.main.close()
	}
	for _, p := range g.terminals {
		p.close()
	}
	if g.emptyShell != nil {
		_, _ = disposeFallbackShell(g.emptyShell)
	}
	guiAwake.Release()
}

func (g *guiApp) allSessions() []sessionInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions
}

func (g *guiApp) visible() []sessionInfo { return filteredSessions(g.allSessions(), g.filter) }

func (g *guiApp) selectedInfo() (sessionInfo, bool) {
	for _, s := range g.visible() {
		if s.Name == g.selected {
			return s, true
		}
	}
	return sessionInfo{}, false
}

func (g *guiApp) say(format string, args ...any) {
	g.message = fmt.Sprintf(format, args...)
	g.msgUntil = time.Now().Add(6 * time.Second)
}

func (g *guiApp) selectSession(name string) {
	if name == "" {
		return
	}
	g.doc = nil // the session comes to the front; the documents stay as tabs
	g.mu.Lock()
	g.selected = name
	for i := range g.sessions {
		if g.sessions[i].Name == name && g.sessions[i].Unread {
			g.sessions[i].Unread = false
			g.rowsVer.Add(1)
		}
	}
	g.mu.Unlock()
	if g.focus == focusTerminal {
		g.focus = focusMain
	}
	rememberSelectedSession(name)
	go clearUnread(name)
}

func (g *guiApp) stepSession(step int) {
	list := g.visible()
	if len(list) == 0 {
		return
	}
	i := slices.IndexFunc(list, func(s sessionInfo) bool { return s.Name == g.selected })
	if i < 0 {
		g.selectSession(list[0].Name)
		return
	}
	g.selectSession(list[nextSession(list, i, step, g.collapsed)].Name)
}

func (g *guiApp) toggleProject(cwd string) {
	if g.collapsed[cwd] {
		delete(g.collapsed, cwd)
	} else {
		g.collapsed[cwd] = true
	}
	g.rowCache.collapsedVer++
	g.settings.CollapsedProjects = g.settings.CollapsedProjects[:0]
	for folder := range g.collapsed {
		g.settings.CollapsedProjects = append(g.settings.CollapsedProjects, folder)
	}
	slices.Sort(g.settings.CollapsedProjects)
	saveSettings(g.settings)
}

// mainPane is the pane of the selected session, attached on demand. With no
// sessions at all it is a shell in the starting directory, which goes away
// again (unless something still runs in it) once a session exists.
func (g *guiApp) mainPane(cols, rows int) *embedded {
	g.mu.Lock()
	listed, total := g.listed, len(g.sessions)
	g.mu.Unlock()
	if listed && total == 0 {
		g.closeMain()
		if g.emptyShell != nil && g.emptyShell.done.Load() {
			// The shell was stopped or exited: stay open with nothing in it
			// rather than showing a dead pane or starting another.
			p := g.emptyShell
			g.emptyShell = nil
			g.mainView.attach(nil)
			go disposeFallbackShell(p)
		}
		if g.emptyShell == nil && !g.emptyTried {
			g.emptyTried = true
			name := launcherSessionName("terminal", g.launchDir, nil)
			p, err := startShellEmbedded(name, g.launchDir, cols, rows, g.dirty)
			if err != nil {
				g.say("Start shell: %v", err)
			} else {
				g.emptyShell = p
			}
		}
		g.mainView.attach(g.emptyShell)
		return g.emptyShell
	}
	if g.emptyShell != nil {
		p := g.emptyShell
		g.emptyShell, g.emptyTried = nil, false
		if g.mainView.pane == p {
			g.mainView.attach(nil)
		}
		go disposeFallbackShell(p)
	}
	s, ok := g.selectedInfo()
	if !ok {
		if list := g.visible(); len(list) > 0 {
			g.selectSession(list[0].Name)
			s, ok = list[0], true
		}
	}
	if !ok {
		g.closeMain()
		return nil
	}
	if p := g.main; p != nil && p.name == s.Name && p.identity == sessionIdentity(s) {
		if !p.done.Load() || time.Since(p.started) < paneRestartHold || (hostOfName(p.name) != "" && !currentLinkStatus().Up) {
			g.mainView.attach(p)
			return p
		}
	}
	if t, failed := g.attachFail[s.Name]; failed && time.Since(t) < 2*time.Second {
		return nil
	}
	g.closeMain()
	p, err := startEmbedded(s.Name, cols, rows, g.dirty)
	if err != nil {
		g.attachFail[s.Name] = time.Now()
		g.say("Attach %s: %v", s.Name, err)
		return nil
	}
	delete(g.attachFail, s.Name)
	p.identity = sessionIdentity(s)
	if s.Shell {
		p.shell = true
		p.setWorkingDirectory(s.CWD)
	}
	g.main = p
	g.mainView.attach(p)
	if g.win != nil {
		g.win.Option(app.Title("lam · " + s.Name))
	}
	return p
}

func (g *guiApp) closeMain() {
	if g.main == nil {
		return
	}
	g.mainView.attach(nil)
	g.main.close()
	g.main = nil
}

// terminalPane is the selected session's own shell, started in the
// session's current directory, or a terminal left running by an earlier run.
func (g *guiApp) terminalPane(cols, rows int) *embedded {
	s, ok := g.selectedInfo()
	if !ok || !g.termVisible {
		g.termView.attach(nil)
		return nil
	}
	owner := s.Name
	if p, ok := g.terminals[owner]; ok {
		if p.done.Load() && !(hostOfName(owner) != "" && !currentLinkStatus().Up) {
			g.disposeTerminal(owner)
			g.termVisible = false
			g.focus = focusMain
			return nil
		}
		g.termView.attach(p)
		return p
	}
	cwd := s.CWD
	if live, err := sessionProcessWorkingDirectory(s.Name); err == nil && live != "" {
		cwd = live
	}
	if cwd == "" {
		cwd = g.launchDir
	}
	label := terminalLabelFor(owner)
	if listed, err := backend().List(); err == nil && !terminalLabelAmbiguous(listed, owner) {
		inUse := map[string]bool{}
		for _, p := range g.terminals {
			inUse[p.name] = true
		}
		for _, cand := range listed {
			if l, ok := terminalSessionLabel(cand.Name); !ok || l != label || cand.Host != s.Host || inUse[cand.Name] {
				continue
			}
			if p, err := startEmbedded(cand.Name, cols, rows, g.dirty); err == nil {
				p.shell, p.managed = true, true
				p.setWorkingDirectory(cand.CWD)
				g.terminals[owner] = p
				g.termView.attach(p)
				return p
			}
		}
	}
	name := hostPrefix(s.Host) + "term-" + label + "-" + newUUID()[:8]
	p, err := startShellEmbedded(name, cwd, cols, rows, g.dirty)
	if err != nil {
		g.say("Start terminal: %v", err)
		g.termVisible = false
		return nil
	}
	g.terminals[owner] = p
	g.termView.attach(p)
	return p
}

// disposeTerminal closes an owner's terminal and ends its shell: closing the
// terminal is an explicit request.
func (g *guiApp) disposeTerminal(owner string) {
	p, ok := g.terminals[owner]
	if !ok {
		return
	}
	delete(g.terminals, owner)
	if g.termView.pane == p {
		g.termView.attach(nil)
	}
	p.close()
	name := p.name
	go func() {
		if listed, err := backend().List(); err == nil && !hasSession(listed, name) {
			return
		}
		_ = killSession(name)
	}()
}

func (g *guiApp) toggleTerminal() {
	if g.termVisible {
		if s, ok := g.selectedInfo(); ok {
			g.disposeTerminal(s.Name)
		}
		g.termVisible = false
		g.focus = focusMain
		return
	}
	if _, ok := g.selectedInfo(); ok {
		g.termVisible = true
		g.focus = focusTerminal
	}
}

// setFocus gives the keyboard to a pane and tells the programs (mode 1004).
func (g *guiApp) setFocus(f guiFocus) {
	old := g.focusedView()
	g.focus = f
	if nw := g.focusedView(); nw != old {
		g.mu.Lock()
		focused := g.windowFocused
		g.mu.Unlock()
		if focused {
			if old != nil {
				old.windowFocus(false)
			}
			if nw != nil {
				nw.windowFocus(true)
			}
		}
	}
}

func (g *guiApp) focusedView() *termView {
	switch g.focus {
	case focusTerminal:
		if g.termVisible && g.termView.pane != nil {
			return g.termView
		}
	case focusFilter:
		return nil
	}
	return g.mainView
}

func (g *guiApp) layout(gtx layout.Context) layout.Dimensions {
	g.flushProgramCopies(gtx)
	g.pollConnect()
	if g.autoConnect != "" && g.dlg.kind == dlgNone {
		host := g.autoConnect
		g.autoConnect = ""
		g.openConnect()
		g.dlg.field.set(host)
		g.startConnect(host)
	}
	g.pruneGone()
	// Cleared before the terminals are read: output that arrives while this
	// frame is drawn sets it again and wakes the next one.
	g.dirty.Store(false)
	g.mu.Lock()
	req := g.openReq
	g.openReq = ""
	g.mu.Unlock()
	if req != "" {
		g.filter = ""
		g.selectSession(req)
	}
	px := float64(gtx.Metric.PxPerSp * g.fontSize())
	g.mainView.setFontPx(px)
	g.termView.setFontPx(px)
	size := gtx.Constraints.Max

	// Every press anywhere: where context menus open, and outside clicks close them.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &g.pressTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			g.lastPress = image.Pt(int(pe.Position.X), int(pe.Position.Y))
			if g.menu != nil && !g.lastPress.In(g.menu.rect.Add(g.menu.at)) {
				g.menu = nil // a press outside the menu closes it
			}
		}
	}

	paint.Fill(gtx.Ops, g.th.Bg)
	g.handleKeys(gtx)
	sideW := 0
	if !g.hideSide {
		sideW = g.sidebarWidth(gtx, size.X)
		func() {
			sg := gtx
			sg.Constraints = layout.Exact(image.Pt(sideW, size.Y))
			g.layoutSidebar(sg)
		}()
		paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(sideW, 0), Max: image.Pt(sideW+max(1, gtx.Dp(1)), size.Y)}.Op())
		g.edgeHandle(gtx, image.Rect(sideW-gtx.Dp(3), 0, sideW+gtx.Dp(4), size.Y), &g.sideTag, pointer.CursorColResize)
	}
	x := 0
	if !g.hideSide {
		x = sideW + max(1, gtx.Dp(1))
	}
	func() {
		defer op.Offset(image.Pt(x, 0)).Push(gtx.Ops).Pop()
		mg := gtx
		mg.Constraints = layout.Exact(image.Pt(size.X-x, size.Y))
		g.layoutMain(mg)
	}()
	if g.dlg.kind != dlgNone {
		g.menu = nil // a dialog opened over it: nothing is to be picked from the menu
	}
	if g.menu != nil {
		g.layoutMenu(gtx)
	}
	if g.dlg.kind != dlgNone {
		g.layoutDialog(gtx)
	}
	g.handleDrags(gtx)
	// Watch presses without taking them from what is under the pointer.
	// Pass-through must be set before the area exists, or the area blocks.
	pt := pointer.PassOp{}.Push(gtx.Ops)
	st := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &g.pressTag)
	st.Pop()
	pt.Pop()
	return layout.Dimensions{Size: size}
}

func (g *guiApp) sidebarWidth(gtx layout.Context, total int) int {
	w := g.settings.GUISidebarWidth
	if w <= 0 {
		w = defaultSidebarDp
	}
	px := gtx.Dp(unit.Dp(w))
	return min(max(px, gtx.Dp(minSidebarDp)), total/2)
}

// edgeHandle is a draggable strip with a resize cursor.
func (g *guiApp) edgeHandle(gtx layout.Context, r image.Rectangle, tag *bool, cursor pointer.Cursor) {
	st := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	cursor.Add(gtx.Ops)
	st.Pop()
}

// handleDrags moves the sidebar edge (window coordinates) and the terminal
// divider (main area coordinates: its handle sits inside that offset).
func (g *guiApp) handleDrags(gtx layout.Context) {
	for _, h := range []struct {
		tag  *bool
		kind int
	}{{&g.sideTag, 1}, {&g.splitTag, 2}} {
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: h.tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
			if !ok {
				break
			}
			pe, ok := ev.(pointer.Event)
			if !ok {
				continue
			}
			switch pe.Kind {
			case pointer.Press:
				g.dragging = h.kind
				if h.kind == 1 {
					if time.Since(g.lastEdge) < multiClick {
						g.settings.GUISidebarWidth = 0 // double click: back to the default width
						saveSettings(g.settings)
						g.dragging = 0
					}
					g.lastEdge = time.Now()
				}
			case pointer.Drag:
				switch g.dragging {
				case 1:
					g.settings.GUISidebarWidth = max(minSidebarDp, pe.Position.X/gtx.Metric.PxPerDp)
				case 2:
					b := g.split.body
					pct := g.settings.TerminalPercent
					if g.split.right && b.Dx() > 0 {
						pct = (b.Max.X - int(pe.Position.X)) * 100 / b.Dx()
					} else if b.Dy() > 0 {
						pct = (b.Max.Y - int(pe.Position.Y)) * 100 / b.Dy()
					}
					g.settings.TerminalPercent = min(max(pct, minTerminalPercent), maxTerminalPercent)
				}
				gtx.Execute(op.InvalidateCmd{})
			case pointer.Release, pointer.Cancel:
				if g.dragging != 0 {
					saveSettings(g.settings)
				}
				g.dragging = 0
			}
		}
	}
}

// layoutMain draws the session pane, and the terminal pane next to or below
// it, under a header with the session's actions.
func (g *guiApp) layoutMain(gtx layout.Context) {
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, g.th.TermBg, clip.Rect{Max: size}.Op())
	headerH := gtx.Dp(g.th.HeaderH)
	g.layoutHeader(gtx, size.X, headerH)

	body := image.Rect(0, headerH, size.X, size.Y)
	mainR, termR, divR := body, image.Rectangle{}, image.Rectangle{}
	// On a narrow window the terminal goes below, as in the TUI.
	right := g.settings.TerminalPlacement == terminalRight && body.Dx() > gtx.Dp(720)
	line := max(1, gtx.Dp(1))
	if g.termVisible {
		pct := g.settings.TerminalPercent
		if right {
			divX := body.Max.X - body.Dx()*pct/100
			mainR = image.Rect(body.Min.X, body.Min.Y, divX, body.Max.Y)
			divR = image.Rect(divX, body.Min.Y, divX+line, body.Max.Y)
			termR = image.Rect(divX+line, body.Min.Y, body.Max.X, body.Max.Y)
		} else {
			divY := body.Max.Y - body.Dy()*pct/100
			mainR = image.Rect(body.Min.X, body.Min.Y, body.Max.X, divY)
			divR = image.Rect(body.Min.X, divY, body.Max.X, divY+line)
			termR = image.Rect(body.Min.X, divY+line, body.Max.X, body.Max.Y)
		}
		g.split.body, g.split.right = body, right
	}
	pad := gtx.Dp(g.th.TermPad)
	inner := func(r image.Rectangle) image.Rectangle {
		return image.Rect(r.Min.X+pad, r.Min.Y+pad/2, r.Max.X-pad/2, r.Max.Y-pad/2)
	}

	if g.doc != nil {
		g.termView.attach(nil)
		g.layoutDoc(gtx, body)
		return
	}
	mr := inner(mainR)
	if mr.Dx() > 10 && mr.Dy() > 10 {
		cols, rows := g.mainView.gridSize(mr.Size())
		if p := g.mainPane(cols, rows); p != nil {
			if p.em.Width() != cols || p.em.Height() != rows {
				p.resize(cols, rows)
			}
			g.drawView(gtx, g.mainView, mr, g.focusedView() == g.mainView)
		} else {
			msg := "No session selected"
			if g.listed && len(g.allSessions()) == 0 {
				msg = "No sessions"
			}
			if g.listErr != "" {
				msg = g.listErr
			} else if g.message != "" && time.Now().Before(g.msgUntil) {
				msg = g.message
			}
			g.drawText(gtx, image.Pt(mr.Min.X+pad, mr.Min.Y+pad), g.th.UISize, font.Normal, g.th.TextDim, msg, mr.Dx()-2*pad)
		}
	}
	if g.termVisible {
		paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect(divR).Op())
		cursor := pointer.CursorRowResize
		if right {
			cursor = pointer.CursorColResize
		}
		g.edgeHandle(gtx, divR.Inset(-gtx.Dp(3)), &g.splitTag, cursor)
		strip := gtx.Dp(24)
		label := "Terminal"
		if p := g.terminals[g.selected]; p != nil {
			if cwd := p.workingDirectory(); cwd != "" {
				label += " · " + shortenHomePath(cwd, g.sourceFor(hostOfName(g.selected)))
			}
		}
		col := g.th.TextFaint
		if g.focus == focusTerminal {
			col = g.th.TextDim
		}
		g.drawTextMid(gtx, image.Pt(termR.Min.X+pad, termR.Min.Y+strip/2+gtx.Dp(3)), g.th.SmallSize, font.Medium, col, label, termR.Dx()-2*pad-gtx.Dp(30))
		cb := gtx.Dp(24)
		ccy := termR.Min.Y + strip/2 + gtx.Dp(3)
		if g.ghostButton(gtx, "term-close", image.Rect(termR.Max.X-pad/2-cb, ccy-cb/2, termR.Max.X-pad/2, ccy-cb/2+cb), "Close terminal", false, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconCross(gtx, c, float32(gtx.Dp(3.5)), float32(max(1, gtx.Dp(1.3))), col)
		}) {
			g.toggleTerminal()
		}
		tr := inner(image.Rect(termR.Min.X, termR.Min.Y+strip, termR.Max.X, termR.Max.Y))
		if tr.Dx() > 10 && tr.Dy() > 10 {
			cols, rows := g.termView.gridSize(tr.Size())
			if p := g.terminalPane(cols, rows); p != nil {
				if p.em.Width() != cols || p.em.Height() != rows {
					p.resize(cols, rows)
				}
				g.drawView(gtx, g.termView, tr, g.focusedView() == g.termView)
			}
		}
	} else {
		g.termView.attach(nil)
	}
	if g.message != "" && time.Now().Before(g.msgUntil) && g.mainView.pane != nil {
		g.layoutToast(gtx, size)
	}
}

func (g *guiApp) drawView(gtx layout.Context, v *termView, r image.Rectangle, focused bool) {
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	vg := gtx
	vg.Constraints = layout.Exact(r.Size())
	g.mu.Lock()
	windowFocused := g.windowFocused
	g.mu.Unlock()
	v.Layout(vg, focused && windowFocused && g.dlg.kind == dlgNone)
}

func (g *guiApp) layoutToast(gtx layout.Context, size image.Point) {
	gtx.Execute(op.InvalidateCmd{At: g.msgUntil})
	gl, w, _ := g.shape(gtx, g.th.SmallSize, font.Normal, g.message, size.X*2/3)
	padX, h := gtx.Dp(14), gtx.Dp(32)
	r := image.Rect(size.X/2-w/2-padX, size.Y-h-gtx.Dp(18), size.X/2+w/2+padX, size.Y-gtx.Dp(18))
	paint.FillShape(gtx.Ops, g.th.Surface, clip.UniformRRect(r, h/2).Op(gtx.Ops))
	if len(gl) > 0 {
		asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
		g.paintGlyphs(gtx, gl, image.Pt(r.Min.X+padX, r.Min.Y+h/2+(asc-desc)/2), g.th.Text)
	}
}

// guiOwnTitleBar is whether lam draws the window's title bar itself. GNOME
// and other Wayland desktops leave that to the application, and Gio's
// fallback bar does not belong in a designed window. macOS keeps its own.
var guiOwnTitleBar = runtime.GOOS == "linux"

// layoutHeader is the title bar over the panes: the session, its state and
// its actions, and the window controls. The empty part drags the window.
func (g *guiApp) layoutHeader(gtx layout.Context, width, h int) {
	pad := gtx.Dp(g.th.TermPad)
	cy := h / 2
	moveArea(gtx, image.Rect(0, 0, width, h))
	paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(0, h-max(1, gtx.Dp(1))), Max: image.Pt(width, h)}.Op())

	x := width - gtx.Dp(8)
	b := gtx.Dp(30)
	btn := func(id, tip string, hover color.NRGBA, icon func(layout.Context, image.Point, color.NRGBA)) bool {
		r := image.Rect(x-b, cy-b/2, x, cy-b/2+b)
		x -= b + gtx.Dp(2)
		return g.ghostButtonTinted(gtx, id, r, tip, false, hover, icon)
	}
	if guiOwnTitleBar {
		ir := float32(gtx.Dp(5))
		if btn("win-close", "", g.th.Danger, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconCross(gtx, c, ir*0.9, float32(max(1, gtx.Dp(1.3))), col)
		}) {
			g.win.Perform(system.ActionClose)
		}
		maximized := g.maximized
		if btn("win-max", "", color.NRGBA{}, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconMaximize(gtx, c, ir, maximized, col)
		}) {
			if maximized {
				g.win.Perform(system.ActionUnmaximize)
			} else {
				g.win.Perform(system.ActionMaximize)
			}
		}
		if btn("win-min", "", color.NRGBA{}, func(gtx layout.Context, c image.Point, col color.NRGBA) {
			iconMinimize(gtx, c, ir, col)
		}) {
			g.win.Perform(system.ActionMinimize)
		}
		x -= gtx.Dp(6)
		paint.FillShape(gtx.Ops, g.th.Divider, clip.Rect{Min: image.Pt(x, cy-gtx.Dp(9)), Max: image.Pt(x+max(1, gtx.Dp(1)), cy+gtx.Dp(9))}.Op())
		x -= gtx.Dp(8)
	}

	if g.doc != nil {
		// A document is in front: its tab, with Reload at the right.
		xr := x - gtx.Dp(8)
		if !g.doc.loading {
			xr -= g.button(gtx, "doc-reload", "Reload", image.Pt(xr, cy), true, false) + gtx.Dp(8)
			if g.clicked(gtx, "doc-reload") {
				g.loadDocument(g.doc)
			}
		}
		g.layoutTabs(gtx, pad, xr, cy)
		return
	}
	s, ok := g.selectedInfo()
	if !ok {
		if len(g.docs) > 0 {
			g.layoutTabs(gtx, pad, x, cy)
			return
		}
		if g.emptyShell != nil {
			g.drawTextMid(gtx, image.Pt(pad, cy), g.th.UISize, font.SemiBold, g.th.Text, "Shell", x-pad)
		}
		return
	}
	mod := "Ctrl+Shift+"
	if guiMac {
		mod = "⌘"
	}
	termTip := "Open terminal  " + mod + "T"
	if g.termVisible {
		termTip = "Close terminal  " + mod + "T"
	}
	ir := float32(gtx.Dp(8))
	if btn("stop", "Stop session  "+mod+"W", color.NRGBA{}, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconStop(gtx, c, ir, col)
	}) {
		g.openStop(s)
	}
	if btn("rename", "Rename  "+mod+"R", color.NRGBA{}, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		iconPencil(gtx, c, ir*0.9, col)
	}) {
		g.openRename(s)
	}
	termOn := g.termVisible
	if btn("terminal", termTip, color.NRGBA{}, func(gtx layout.Context, c image.Point, col color.NRGBA) {
		if termOn {
			col = g.th.Accent
		}
		iconTerminal(gtx, c, ir, col)
	}) {
		g.toggleTerminal()
	}

	if len(g.docs) > 0 {
		// The session is one tab among the open documents'.
		g.layoutTabs(gtx, pad, x, cy)
		return
	}
	// Title: the session, then its folder and state.
	tx := pad
	w := g.drawTextMid(gtx, image.Pt(tx, cy), g.th.UISize, font.SemiBold, g.th.Text, s.Name, x-tx-gtx.Dp(20))
	tx += w + gtx.Dp(12)
	status, col, age := g.statusOf(s, time.Now())
	if g.main != nil && g.main.done.Load() {
		status, col, age = "ended", g.th.Unread, ""
	}
	if s.CWD != "" && tx < x-gtx.Dp(120) {
		w = g.drawTextMid(gtx, image.Pt(tx, cy), g.th.TinySize, font.Normal, g.th.TextFaint, shortenHomePath(s.CWD, g.sourceFor(s.Host)), x-tx-gtx.Dp(110))
		tx += w + gtx.Dp(12)
	}
	if status != "" && tx < x-gtx.Dp(70) {
		// A small chip: the state, and since when.
		label := status
		if age != "" {
			label += " · " + age
		}
		gl, lw, _ := g.shape(gtx, g.th.TinySize, font.Medium, label, x-tx)
		ch := gtx.Dp(22)
		cr := image.Rect(tx, cy-ch/2, tx+lw+gtx.Dp(26), cy-ch/2+ch)
		bg := col
		bg.A = 28
		paint.FillShape(gtx.Ops, bg, clip.UniformRRect(cr, ch/2).Op(gtx.Ops))
		d := gtx.Dp(6)
		paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: image.Pt(cr.Min.X+gtx.Dp(9), cy-d/2), Max: image.Pt(cr.Min.X+gtx.Dp(9)+d, cy-d/2+d)}.Op(gtx.Ops))
		if len(gl) > 0 {
			asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
			g.paintGlyphs(gtx, gl, image.Pt(cr.Min.X+gtx.Dp(20), cy+(asc-desc)/2), col)
		}
	}
}

// button draws a pill button with its right (alignRight) or left edge at
// pt.X, centred on pt.Y, and returns its width.
func (g *guiApp) button(gtx layout.Context, id, label string, pt image.Point, alignRight, danger bool) int {
	return g.buttonStyled(gtx, id, label, pt, alignRight, danger, false)
}

func (g *guiApp) buttonStyled(gtx layout.Context, id, label string, pt image.Point, alignRight, danger, primary bool) int {
	gl, tw, _ := g.shape(gtx, g.th.SmallSize, font.Medium, label, 1<<20)
	padX, h := gtx.Dp(14), gtx.Dp(32)
	w := tw + 2*padX
	x0 := pt.X
	if alignRight {
		x0 = pt.X - w
	}
	r := image.Rect(x0, pt.Y-h/2, x0+w, pt.Y-h/2+h)
	c := g.clickable(id)
	// Layout consumes the click events, so they are read first.
	if c.Clicked(gtx) {
		g.hits[id] = true
	}
	// Primary buttons are filled (red when they destroy something);
	// secondary ones are outlined.
	hov := c.Hovered()
	bg, fg, border := color.NRGBA{}, g.th.TextDim, g.th.Border
	switch {
	case primary && danger:
		bg, fg, border = g.th.Danger, g.th.Text, color.NRGBA{}
		if hov {
			bg = mix(g.th.Danger, g.th.Text, 0.15)
		}
	case primary:
		bg, fg, border = g.th.Accent, g.th.AccentText, color.NRGBA{}
		if hov {
			bg = mix(g.th.Accent, g.th.Text, 0.2)
		}
	case hov:
		bg, fg = g.th.Hover, g.th.Text
	}
	st := op.Offset(r.Min).Push(gtx.Ops)
	cg := gtx
	cg.Constraints = layout.Exact(r.Size())
	c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(7))
		if bg.A > 0 {
			paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
		}
		if border.A > 0 {
			paint.FillShape(gtx.Ops, border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: r.Size()}
	})
	st.Pop()
	if len(gl) > 0 {
		asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
		g.paintGlyphs(gtx, gl, image.Pt(r.Min.X+padX, pt.Y+(asc-desc)/2), fg)
	}
	return w
}

func (g *guiApp) clickable(id string) *widget.Clickable {
	c := g.buttons[id]
	if c == nil {
		c = new(widget.Clickable)
		g.buttons[id] = c
	}
	return c
}

// clicked reports whether the button drawn as id was clicked.
func (g *guiApp) clicked(gtx layout.Context, id string) bool {
	hit := g.hits[id]
	delete(g.hits, id)
	return hit
}

// handleKeys routes the keyboard: an open dialog or the filter field first,
// then lam's own shortcuts, then the pane clicked last.
func (g *guiApp) handleKeys(gtx layout.Context) {
	tag := &g.keyTag
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	key.InputHintOp{Tag: tag, Hint: key.HintText}.Add(gtx.Ops)
	area.Pop()
	gtx.Execute(key.FocusCmd{Tag: tag})

	mods := key.ModCtrl | key.ModAlt | key.ModShift | key.ModSuper | key.ModCommand
	filters := []event.Filter{
		transfer.TargetFilter{Target: tag, Type: "application/text"}, // pasted into a dialog or the filter
		key.FocusFilter{Target: tag},                                 // typed text arrives as EditEvents through this
		key.Filter{Focus: tag, Optional: mods},
		key.Filter{Focus: tag, Name: key.NameTab, Optional: mods},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		switch ev := ev.(type) {
		case transfer.DataEvent:
			r := ev.Open()
			data, _ := io.ReadAll(io.LimitReader(r, 1<<20))
			r.Close()
			text := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(string(data))
			switch {
			case g.dlg.kind != dlgNone:
				g.dialogText(text)
			case g.focus == focusFilter:
				g.filter += text
			}
		case key.Event:
			g.lastMods = ev.Modifiers
			g.skipEdits = false
			if ev.State != key.Press {
				continue
			}
			ctrlAltSuper := ev.Modifiers&keyMods() != 0
			pasteKey := ev.Name == "V" && ev.Modifiers&(key.ModCtrl|key.ModCommand) != 0
			dialogBefore := g.dlg.kind
			switch {
			case pasteKey && (g.dlg.kind != dlgNone || g.focus == focusFilter):
				gtx.Execute(clipboard.ReadCmd{Tag: tag})
			case g.dlg.kind != dlgNone:
				g.dialogKey(gtx, ev)
			case g.menu != nil && ev.Name == key.NameEscape:
				g.menu = nil
			case g.focus != focusFilter && g.docKey(ev):
				gtx.Execute(op.InvalidateCmd{})
			case g.shortcut(gtx, ev):
			case g.focus == focusFilter:
				g.filterKey(ev)
			case guiMac && ev.Modifiers.Contain(key.ModCommand):
				// Cmd combinations are the application's, never the program's.
			case printable(ev.Name) && !ctrlAltSuper:
				// The character arrives as an EditEvent.
			case g.doc != nil:
				// A document takes no input.
			default:
				if v := g.focusedView(); v != nil {
					v.key(ev.Name, ev.Modifiers)
				}
			}
			// The character of the key that closed a dialog is not for the program.
			g.skipEdits = dialogBefore != dlgNone && g.dlg.kind == dlgNone
		case key.EditEvent:
			if g.skipEdits {
				continue
			}
			if g.lastMods&(keyMods()|key.ModCommand) != 0 {
				continue // already sent as a key
			}
			switch {
			case g.dlg.kind != dlgNone:
				g.dialogText(ev.Text)
			case g.focus == focusFilter:
				g.filter += ev.Text
			case g.doc != nil:
			default:
				if v := g.focusedView(); v != nil {
					v.text(ev.Text)
				}
			}
		}
	}
}

// guiMac is whether lam runs as a Mac application: its shortcuts use Cmd,
// and Option types characters (é, ™) as in Terminal.app, unless
// LAM_MAC_OPTION_AS_ALT=1 makes it the terminal's Alt (Meta) key.
var guiMac = runtime.GOOS == "darwin"

var macOptionAsAlt = os.Getenv("LAM_MAC_OPTION_AS_ALT") == "1"

// keyMods are the modifiers that make a key press something other than
// typing: those keys go to the program as key presses, not as text.
func keyMods() key.Modifiers {
	if guiMac && !macOptionAsAlt {
		return key.ModCtrl | key.ModSuper
	}
	return key.ModCtrl | key.ModAlt | key.ModSuper
}

// printable reports whether a key types a character, which then arrives as
// an EditEvent. Gio names the other keys with symbols (⏎, ←, ⌫) or words.
func printable(n key.Name) bool {
	switch n {
	case key.NameSpace:
		return true
	case key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow,
		key.NameReturn, key.NameEnter, key.NameEscape, key.NameHome, key.NameEnd,
		key.NameDeleteBackward, key.NameDeleteForward, key.NamePageUp, key.NamePageDown,
		key.NameCommand:
		return false
	}
	r := []rune(string(n))
	return len(r) == 1 && r[0] >= ' '
}

// shortcut handles lam's own keys and reports whether it took the event.
// They need Ctrl+Shift (or Shift with the page keys), which terminals leave
// unused, so nothing a program understands is taken from it.
func (g *guiApp) shortcut(gtx layout.Context, ev key.Event) bool {
	m := ev.Modifiers
	ctrl, shift := m.Contain(key.ModCtrl), m.Contain(key.ModShift)
	// cs is the modifier of lam's own shortcuts: Ctrl+Shift, or Cmd on a Mac,
	// where Cmd never reaches a terminal program anyway. zm is the zoom key's.
	cs, zm := ctrl && shift, ctrl && shift // Ctrl+- alone is the shell's undo
	if guiMac {
		cs, zm = m.Contain(key.ModCommand), m.Contain(key.ModCommand)
		ctrl = false // Ctrl+PgUp/PgDn stay the program's
	}
	v := g.focusedView()
	zoom := func(delta float32) {
		if delta == 0 {
			g.settings.GUIFontSize = 0 // back to the default size
		} else {
			g.settings.GUIFontSize = min(max(g.fontSize()+delta, minGUIFontSize), maxGUIFontSize)
		}
		saveSettings(g.settings)
	}
	switch {
	case cs && ev.Name == "C":
		if v != nil {
			v.copySelection(gtx)
		}
	case cs && ev.Name == "V":
		if v != nil {
			v.paste(gtx)
		}
	case cs && ev.Name == "T":
		g.toggleTerminal()
	case cs && ev.Name == "N":
		g.openNewSession()
	case cs && ev.Name == "O":
		g.openConnect()
	case m.Contain(key.ModCtrl) && ev.Name == key.NameTab && len(g.docs) > 0:
		if shift {
			g.cycleTab(-1)
		} else {
			g.cycleTab(1)
		}
	case cs && ev.Name == "D":
		g.openDocumentDialog()
	case cs && ev.Name == "F":
		g.focus, g.hideSide = focusFilter, false
	case cs && ev.Name == "R":
		if s, ok := g.selectedInfo(); ok {
			g.openRename(s)
		}
	case cs && ev.Name == "W":
		if s, ok := g.selectedInfo(); ok {
			g.openStop(s)
		}
	case guiMac && cs && !shift && ev.Name == "Q":
		g.win.Perform(system.ActionClose) // quit; the sessions keep running
	case cs && ev.Name == "Q":
		g.openQuitAll()
	case cs && ev.Name == ",":
		g.dlg = dialog{kind: dlgSettings}
	case cs && ev.Name == "B", ev.Name == key.NameF2 && m == 0:
		g.hideSide = !g.hideSide
	case cs && ev.Name == "/", ev.Name == key.NameF1 && m == 0:
		g.dlg = dialog{kind: dlgHelp}
	case cs && ev.Name == key.NameUpArrow, ctrl && !shift && ev.Name == key.NamePageUp, guiMac && cs && (ev.Name == "[" || ev.Name == "{"):
		g.stepSession(-1)
	case cs && ev.Name == key.NameDownArrow, ctrl && !shift && ev.Name == key.NamePageDown, guiMac && cs && (ev.Name == "]" || ev.Name == "}"):
		g.stepSession(1)
	case shift && !cs && ev.Name == key.NamePageUp && v != nil:
		v.scrollPage(-1)
	case shift && !cs && ev.Name == key.NamePageDown && v != nil:
		v.scrollPage(1)
	case shift && !cs && ev.Name == key.NameHome && v != nil:
		v.scrollToTop()
	case shift && !cs && ev.Name == key.NameEnd && v != nil:
		v.scrollToBottom()
	case zm && (ev.Name == "=" || ev.Name == "+"):
		zoom(1)
	case zm && ev.Name == "-":
		zoom(-1)
	case zm && ev.Name == "0":
		zoom(0)
	default:
		return false
	}
	gtx.Execute(op.InvalidateCmd{})
	return true
}

func (g *guiApp) filterKey(ev key.Event) {
	switch ev.Name {
	case key.NameEscape:
		g.filter, g.focus = "", focusMain
	case key.NameReturn, key.NameEnter:
		if list := g.visible(); len(list) > 0 && !slices.ContainsFunc(list, func(s sessionInfo) bool { return s.Name == g.selected }) {
			g.selectSession(list[0].Name)
		}
		g.focus = focusMain
	case key.NameDeleteBackward:
		if r := []rune(g.filter); len(r) > 0 {
			g.filter = string(r[:len(r)-1])
		}
	case key.NameDownArrow:
		g.stepSession(1)
	case key.NameUpArrow:
		g.stepSession(-1)
	}
}

// guiRow is one line of the session list: a project heading or a session.
type guiRow struct {
	section bool // the heading over one machine's sessions
	saved   bool // a section for a host connected to before, and not connected now
	header  bool
	host    string // the machine, for a section or a project heading
	first   bool   // a project heading right under its section heading
	cwd     string
	label   string
	count   int
	folded  bool
	s       sessionInfo
}

// rowCache remembers the built sidebar rows until something they depend on
// changes, so an idle frame does not rebuild them.
type rowCache struct {
	rows         []guiRow
	first        *sessionInfo
	n            int
	filter       string
	selected     string
	host         string
	ver          uint64
	collapsedVer int
	seenCollapse int
	valid        bool
}

// cachedRows is sidebarRows(visible()), rebuilt only when its inputs change.
func (g *guiApp) cachedRows() []guiRow {
	all := g.allSessions()
	c := &g.rowCache
	var first *sessionInfo
	if len(all) > 0 {
		first = &all[0]
	}
	_, rhost := g.mb.Remote()
	host, ver := g.hostLabel()+"|"+rhost+"|"+strings.Join(g.settings.GUIHosts, ","), g.rowsVer.Load()
	if c.valid && c.first == first && c.n == len(all) && c.filter == g.filter && c.selected == g.selected &&
		c.host == host && c.ver == ver && c.seenCollapse == c.collapsedVer {
		return c.rows
	}
	c.rows = g.sidebarRows(filteredSessions(all, g.filter))
	c.first, c.n, c.filter, c.selected, c.host, c.ver, c.valid = first, len(all), g.filter, g.selected, host, ver, true
	c.seenCollapse = c.collapsedVer
	return c.rows
}

func (g *guiApp) sidebarRows(list []sessionInfo) []guiRow {
	selected := slices.IndexFunc(list, func(s sessionInfo) bool { return s.Name == g.selected })
	_, remote := g.mb.Remote()
	rows := []guiRow{{section: true, label: g.hostLabel()}}
	remoteShown, afterSection := false, true
	showRemote := func() {
		rows = append(rows, guiRow{section: true, host: remote, label: remote})
		remoteShown = true
	}
	for _, r := range buildSidebarRows(list, false, g.collapsed, selected) {
		host := r.host
		if !r.header {
			host = list[r.session].Host
		}
		if host != "" && !remoteShown {
			showRemote()
			afterSection = true
		}
		if r.header {
			rows = append(rows, guiRow{header: true, first: afterSection, cwd: r.cwd, host: r.host, label: r.label, count: r.count, folded: r.collapsed})
			afterSection = false
		} else {
			rows = append(rows, guiRow{s: list[r.session]})
		}
	}
	if remote != "" && !remoteShown {
		showRemote()
	}
	if g.filter == "" {
		for _, h := range g.settings.GUIHosts {
			if h != remote {
				rows = append(rows, guiRow{section: true, saved: true, host: h, label: h})
			}
		}
	}
	return rows
}

// chevron draws a small filled triangle centred on c: pointing right when
// folded, down when open.
func chevron(gtx layout.Context, c image.Point, r float32, folded bool, col color.NRGBA) {
	var p clip.Path
	p.Begin(gtx.Ops)
	x, y := float32(c.X), float32(c.Y)
	if folded {
		p.MoveTo(f32.Pt(x-r*0.6, y-r))
		p.LineTo(f32.Pt(x+r*0.8, y))
		p.LineTo(f32.Pt(x-r*0.6, y+r))
	} else {
		p.MoveTo(f32.Pt(x-r, y-r*0.6))
		p.LineTo(f32.Pt(x+r, y-r*0.6))
		p.LineTo(f32.Pt(x, y+r*0.8))
	}
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Outline{Path: p.End()}.Op())
}

var uiTypeface = font.Typeface("UI")

func (g *guiApp) params(size unit.Sp, weight font.Weight, maxW int, gtx layout.Context) text.Parameters {
	return text.Parameters{
		Font:     font.Font{Typeface: uiTypeface, Weight: weight},
		PxPerEm:  fixed.I(gtx.Sp(size)),
		MaxLines: 1,
		MaxWidth: max(1, maxW),
	}
}

// shape lays out one line and returns its glyphs, width and ascent.
//
// Results are cached by text, size, weight and width; callers must not modify
// the returned glyphs.
func (g *guiApp) shape(gtx layout.Context, size unit.Sp, weight font.Weight, s string, maxW int) ([]text.Glyph, int, int) {
	p := g.params(size, weight, maxW, gtx)
	key := shapeKey{s: s, px: p.PxPerEm, weight: weight, maxW: p.MaxWidth}
	if l, ok := g.shapes[key]; ok {
		return l.gl, l.width, l.asc
	}
	g.shaper.LayoutString(p, s)
	var gl []text.Glyph
	width, asc := 0, 0
	for gg, ok := g.shaper.NextGlyph(); ok; gg, ok = g.shaper.NextGlyph() {
		gl = append(gl, gg)
		width = max(width, (gg.X + gg.Advance).Ceil())
		asc = gg.Ascent.Ceil()
	}
	if g.shapes == nil || len(g.shapes) >= 4096 {
		g.shapes = map[shapeKey]shapedLine{}
	}
	g.shapes[key] = shapedLine{gl, width, asc}
	return gl, width, asc
}

type shapeKey struct {
	s      string
	px     fixed.Int26_6
	weight font.Weight
	maxW   int
}

type shapedLine struct {
	gl         []text.Glyph
	width, asc int
}

func (g *guiApp) paintGlyphs(gtx layout.Context, gl []text.Glyph, at image.Point, c color.NRGBA) {
	if len(gl) == 0 {
		return
	}
	off := op.Offset(at).Push(gtx.Ops)
	cl := clip.Outline{Path: g.shaper.Shape(gl)}.Op().Push(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cl.Pop()
	off.Pop()
}

// drawText draws one line with its top at pt and returns its width.
func (g *guiApp) drawText(gtx layout.Context, pt image.Point, size unit.Sp, weight font.Weight, c color.NRGBA, s string, maxW int) int {
	gl, w, asc := g.shape(gtx, size, weight, s, maxW)
	g.paintGlyphs(gtx, gl, image.Pt(pt.X, pt.Y+asc), c)
	return w
}

// drawTextMid draws one line vertically centred on pt.Y.
func (g *guiApp) drawTextMid(gtx layout.Context, pt image.Point, size unit.Sp, weight font.Weight, c color.NRGBA, s string, maxW int) int {
	gl, w, _ := g.shape(gtx, size, weight, s, maxW)
	if len(gl) == 0 {
		return 0
	}
	asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
	g.paintGlyphs(gtx, gl, image.Pt(pt.X, pt.Y+(asc-desc)/2), c)
	return w
}

func (g *guiApp) drawTextRightMid(gtx layout.Context, pt image.Point, size unit.Sp, c color.NRGBA, s string) {
	gl, w, _ := g.shape(gtx, size, font.Medium, s, 1<<20)
	if len(gl) == 0 {
		return
	}
	asc, desc := gl[0].Ascent.Ceil(), gl[0].Descent.Ceil()
	g.paintGlyphs(gtx, gl, image.Pt(pt.X-w, pt.Y+(asc-desc)/2), c)
}

// newUIShaper shapes interface text in the desktop's sans serif, with Go's
// fonts behind it for anything it lacks.
func newUIShaper() *text.Shaper {
	var faces []font.FontFace
	for _, data := range uiFontFiles() {
		if face, err := giofont.Parse(data); err == nil {
			f := face.Font()
			f.Typeface = uiTypeface
			faces = append(faces, font.FontFace{Font: f, Face: face})
		}
	}
	for _, ff := range gofont.Collection() {
		if len(faces) == 0 {
			ff.Font.Typeface = uiTypeface
		}
		faces = append(faces, ff)
	}
	return text.NewShaper(text.NoSystemFonts(), text.WithCollection(faces))
}

// pruneGone drops per-session UI state for sessions that no longer exist,
// including terminals whose session was stopped from outside this window.
// It does nothing until the first listing, or while the last one failed, so a
// dropped link does not tear anything down.
func (g *guiApp) pruneGone() {
	g.mu.Lock()
	list, ok := g.sessions, g.listed && g.listErr == ""
	g.mu.Unlock()
	if !ok {
		return
	}
	live := make(map[string]bool, len(list))
	cwds := make(map[string]bool, len(list))
	for _, s := range list {
		live[s.Name], cwds[groupKey(s)] = true, true
	}
	for n := range g.rowClicks {
		if !live[n] {
			delete(g.rowClicks, n)
			delete(g.rowTags, n)
		}
	}
	for c := range g.headers {
		if !cwds[c] {
			delete(g.headers, c)
		}
	}
	for n := range g.attachFail {
		if !live[n] {
			delete(g.attachFail, n)
		}
	}
	for owner := range g.terminals {
		if !live[owner] {
			g.disposeTerminal(owner)
			if g.selected == owner {
				g.termVisible = false
			}
		}
	}
}

// sourceFor is where folders are browsed for the machine a session is on.
func (g *guiApp) sourceFor(host string) sessionDirectorySource {
	if host != "" {
		if r, h := g.mb.Remote(); r != nil && h == host {
			if src, ok := r.(sessionDirectorySource); ok {
				return src
			}
		}
	}
	return g.pathSource
}

// launchersOn are the agents installed on the machine a new session is for:
// asking this machine about a session on the remote host showed the agents
// installed here, and hid the ones installed there.
func (g *guiApp) launchersOn(host string) ([]string, error) {
	if host != "" {
		r, h := g.mb.Remote()
		if lister, ok := r.(sessionLauncherLister); ok && h == host {
			got, err := lister.AvailableLaunchers()
			if err != nil {
				return nil, err
			}
			return orderedSupportedLaunchers(got), nil
		}
		return nil, fmt.Errorf("%s is not connected", host)
	}
	return installedLaunchers(), nil
}

func hostOrNative(host string) string {
	if host == "" {
		return "native"
	}
	return "ssh:" + host
}

// flushProgramCopies writes what programs copied with OSC 52 to the clipboard.
func (g *guiApp) flushProgramCopies(gtx layout.Context) {
	if !g.copyHooked {
		g.copyHooked = true
		hook := func(text string) {
			g.copyMu.Lock()
			g.copyQueue = append(g.copyQueue, text)
			g.copyMu.Unlock()
			g.wake()
		}
		programCopyHook.Store(&hook)
	}
	g.copyMu.Lock()
	queue := g.copyQueue
	g.copyQueue = nil
	g.copyMu.Unlock()
	for _, text := range queue {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
		gtx.Execute(op.InvalidateCmd{}) // see copySelection
	}
}
