package app

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// IDE mode: LAM owns the terminal, draws a session sidebar on the left and
// embeds the selected session or an empty-state shell on the right, rendered
// through an in-process terminal emulator. An idle fallback shell is removed
// on exit; a shell with running work remains available to reattach.

// paneRestartHold is how long a pane that ended is kept before attaching again.
const paneRestartHold = 3 * time.Second

const (
	sidebarWidth      = 26
	uiFrameRate       = 60
	doubleClickWindow = 400 * time.Millisecond

	// statusMessageTimeout is how long a bottom-bar message stays up.
	statusMessageTimeout = 5 * time.Second

	// fullRepaintEvery is how often the whole screen is sent again, in case
	// something outside LAM disturbed it. Frames in between only carry the
	// rows that changed.
	fullRepaintEvery = 30 * time.Second
)

type embedded struct {
	name           string
	identity       string
	shell          bool
	managed        bool
	cmd            *exec.Cmd          // local sessions: the process attached to the session
	ptmx           *os.File           // local sessions: its terminal
	stream         io.ReadWriteCloser // remote sessions: the channel on the remote connection
	out            chan []byte        // remote sessions: frames for the writer goroutine
	send           func([]byte)       // how the interface's own reports (mouse) reach the session
	em             *termEmu
	terminalMu     sync.RWMutex
	scrollback     int
	mouseModes     mouseModeTracker
	bracketedPaste atomic.Bool
	cwdMu          sync.RWMutex
	cwd            string
	done           atomic.Bool
	linkGen        int // which remote connection this pane attached over
	started        time.Time
	quit           chan struct{} // local sessions: ends the input writer
	quitOnce       sync.Once
	restartHeld    bool // the session list was already refreshed for this ended pane
}

// currentLinkStatus is the state of the remote connection; a local backend is
// always up.
func currentLinkStatus() linkStatus {
	if m, ok := backend().(linkMonitor); ok {
		return m.LinkStatus()
	}
	return linkStatus{Up: true}
}

// linkStatusText says what is wrong with the connection and when the next
// attempt happens, for the status line.
func linkStatusText(st linkStatus, now time.Time) string {
	text := "Disconnected, reconnecting"
	if wait := st.RetryAt.Sub(now); wait > 0 {
		text += fmt.Sprintf(" in %ds", int(wait.Seconds())+1)
	} else {
		text += "…"
	}
	if st.Attempt > 1 {
		text += fmt.Sprintf(" (attempt %d)", st.Attempt)
	}
	if reason := linkFailureReason(st.Err); reason != "" {
		text += " · " + reason
	}
	return text
}

// linkFailureReason picks the part of an error worth showing: what ssh itself
// said when there is something, otherwise the error.
func linkFailureReason(err string) string {
	if i := strings.LastIndex(err, " ("); i >= 0 && strings.HasSuffix(err, ")") {
		return err[i+2 : len(err)-1]
	}
	return err
}

type sessionListResult struct {
	sessions []sessionInfo
	err      error
}

func startEmbedded(name string, cols, rows int, dirty *dirtyFlag) (*embedded, error) {
	// Remote sessions are attached over the connection the interface already has.
	if attacher, ok := backend().(streamAttacher); ok {
		gen := currentLinkStatus().Gen // before attaching: the link may be replaced meanwhile
		stream, err := attacher.OpenAttachStream(name, cols, rows)
		if err != nil {
			return nil, err
		}
		p, err := startStreamPane(name, stream, cols, rows, dirty)
		if p != nil {
			p.linkGen = gen
		}
		return p, err
	}
	attacher, ok := backend().(processAttacher)
	if !ok {
		return nil, fmt.Errorf("the %s backend cannot attach sessions", backend().Name())
	}
	cmd := attacher.AttachCmd(name)
	cmd.Env = append(envWithout("TMUX", "TERM"), "TERM=xterm-256color")
	return startEmbeddedCommand(name, cmd, false, "", cols, rows, dirty)
}

func startShellEmbedded(name, cwd string, cols, rows int, dirty *dirtyFlag) (*embedded, error) {
	if err := newSession(name, cwd, fallbackShellCommand); err != nil {
		return nil, err
	}
	p, err := startEmbedded(name, cols, rows, dirty)
	if err != nil {
		_ = killSession(name)
		return nil, err
	}
	p.shell = true
	p.managed = true
	p.setWorkingDirectory(cwd)
	return p, nil
}

// newEmbedded builds a pane and its emulator. What the terminal sends back
// (mouse reports) is queued and written by its own goroutine through e.send:
// the emulator calls this while parsing, and a program that is not reading its
// input must not be able to stall it. The caller sets e.send and then starts
// feeding the emulator; stop ends the sending goroutine.
func newEmbedded(name string, shell bool, cwd string, cols, rows int, dirty *dirtyFlag) (e *embedded, stop func(), err error) {
	e = &embedded{name: name, shell: shell, cwd: cwd, started: time.Now()}
	replies := make(chan []byte, 256)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case data := <-replies:
				e.send(data)
			case <-quit:
				return
			}
		}
	}()
	em, err := newTermEmu(cols, rows, termCallbacks{
		WritePty: func(data []byte) {
			select {
			case replies <- append([]byte(nil), data...):
			default: // queue full: drop the report rather than block
			}
		},
		Pwd: func(uri string) {
			if cwd := terminalWorkingDirectory(uri); cwd != "" {
				e.setWorkingDirectory(cwd)
			}
		},
		// The emulator swallows clipboard writes; hand them to the machine
		// running the UI, which is where the user is.
		ClipboardWrite: func(text string) { go copyToSystemClipboard(text) },
		ModeChanged: func(mode ansi.Mode, enabled bool) {
			if mode == ansi.ModeBracketedPaste {
				e.bracketedPaste.Store(enabled)
			}
			if e.mouseModes.Update(mode, enabled) {
				dirty.Store(true)
			}
		},
	})
	if err != nil {
		close(quit)
		return nil, nil, err
	}
	e.em = em
	return e, func() { close(quit) }, nil
}

// startEmbeddedCommand attaches a pane to a local session by running a command
// on a terminal.
func startEmbeddedCommand(name string, cmd *exec.Cmd, shell bool, cwd string, cols, rows int, dirty *dirtyFlag) (*embedded, error) {
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, err
	}
	e, stop, err := newEmbedded(name, shell, cwd, cols, rows, dirty)
	if err != nil {
		ptmx.Close()
		_ = cmd.Process.Kill()
		go cmd.Wait()
		return nil, err
	}
	e.cmd, e.ptmx = cmd, ptmx
	e.send = func(data []byte) { ptmx.Write(data) }
	e.out = make(chan []byte, 4096)
	e.quit = make(chan struct{})
	go func() {
		for {
			select {
			case data := <-e.out:
				if writePTY(ptmx, data) != nil {
					return
				}
			case <-e.quit:
				return
			}
		}
	}()
	go func() {
		defer stop()
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				e.terminalMu.Lock()
				e.em.Write(buf[:n])
				e.terminalMu.Unlock()
				dirty.Store(true)
			}
			if err != nil {
				e.done.Store(true)
				dirty.Store(true)
				return
			}
		}
	}()
	return e, nil
}

// startStreamPane attaches a pane to a remote session. The stream carries the
// session host's frames: a snapshot of the screen, then its output.
func startStreamPane(name string, stream io.ReadWriteCloser, cols, rows int, dirty *dirtyFlag) (*embedded, error) {
	e, stop, err := newEmbedded(name, false, "", cols, rows, dirty)
	if err != nil {
		stream.Close()
		return nil, err
	}
	e.stream = stream
	readerDone := make(chan struct{})
	e.send = func(data []byte) { _ = e.writeInput(data) }
	// Writes to a stalled link block, so the interface only queues frames; this
	// goroutine ends when the stream is closed, which fails its write.
	e.out = make(chan []byte, 1024)
	go func() {
		for {
			select {
			case frame := <-e.out:
				if writeAll(stream, frame) != nil {
					return
				}
			case <-readerDone:
				return
			}
		}
	}()
	go func() {
		defer stop()
		defer close(readerDone)
		reader := bufio.NewReaderSize(stream, 64<<10)
		for {
			typ, payload, err := readFrame(reader)
			if err != nil {
				e.done.Store(true)
				dirty.Store(true)
				return
			}
			if typ == frameSnapshot || typ == frameOutput {
				e.terminalMu.Lock()
				e.em.Write(payload)
				e.terminalMu.Unlock()
				dirty.Store(true)
			}
		}
	}()
	return e, nil
}

// writeInput sends bytes to the session as if typed.
func (e *embedded) writeInput(data []byte) error {
	if e.stream != nil {
		return e.queueFrame(frameInput, data)
	}
	if e.out == nil {
		return writePTY(e.ptmx, data)
	}
	// A program that is not reading, or a big paste, must not stall the interface.
	for len(data) > 0 {
		n := min(len(data), 4096)
		select {
		case e.out <- append([]byte(nil), data[:n]...):
		default:
			return errors.New("the terminal is not accepting input")
		}
		data = data[n:]
	}
	return nil
}

// queueFrame hands a frame to the writer goroutine without ever waiting on the
// link. A full queue means the link is stalled: the frame is refused.
func (e *embedded) queueFrame(typ byte, payload []byte) error {
	frame := make([]byte, 5+len(payload))
	frame[0] = typ
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	select {
	case e.out <- frame:
		return nil
	default:
		return errors.New("connection is not accepting input")
	}
}

// ownedClients is how many attached clients the pane itself accounts for.
func (e *embedded) ownedClients() int {
	if e.done.Load() {
		return 0
	}
	if e.stream != nil || (e.cmd != nil && e.cmd.Process != nil) {
		return 1
	}
	return 0
}

func (e *embedded) workingDirectory() string {
	e.cwdMu.RLock()
	defer e.cwdMu.RUnlock()
	return e.cwd
}

func (e *embedded) setWorkingDirectory(cwd string) {
	e.cwdMu.Lock()
	e.cwd = cwd
	e.cwdMu.Unlock()
}

func (e *embedded) resize(cols, rows int) {
	e.terminalMu.Lock()
	defer e.terminalMu.Unlock()
	e.em.Resize(cols, rows)
	if e.stream != nil {
		_ = e.queueFrame(frameResize, resizePayload(cols, rows))
		return
	}
	pty.Setsize(e.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// renderViewport returns the live terminal screen or a window into its local
// scrollback. terminalMu keeps each cell snapshot stable while the PTY reader
// updates the emulator.
func (e *embedded) renderViewport() string {
	return e.renderViewportWithSelection(nil)
}

// scrollbackBy moves the local viewport by terminal rows. Positive values
// move toward older output; negative values return toward the live screen.
func (e *embedded) scrollbackBy(rows int) bool {
	e.terminalMu.Lock()
	defer e.terminalMu.Unlock()
	if e.em.IsAltScreen() {
		return false
	}
	maxOffset := e.em.ScrollbackLen()
	next := max(0, min(maxOffset, e.scrollback+rows))
	if next == e.scrollback {
		return false
	}
	e.scrollback = next
	return true
}

// snapToLive leaves scrollback and returns to the live screen, as typing into
// a scrolled-back terminal should. It reports whether the view changed.
func (e *embedded) snapToLive() bool {
	e.terminalMu.Lock()
	defer e.terminalMu.Unlock()
	if e.scrollback == 0 {
		return false
	}
	e.scrollback = 0
	return true
}

func (e *embedded) sendMouse(mouse uv.MouseEvent) {
	e.terminalMu.Lock()
	defer e.terminalMu.Unlock()
	e.em.SendMouse(mouse)
}

func (e *embedded) close() {
	if e.stream != nil {
		e.stream.Close()
		e.terminalMu.Lock()
		e.em.Close()
		e.terminalMu.Unlock()
		return
	}
	if e.quit != nil {
		e.quitOnce.Do(func() { close(e.quit) })
	}
	e.ptmx.Close()
	e.terminalMu.Lock()
	e.em.Close()
	e.terminalMu.Unlock()
	if e.cmd.Process != nil {
		if e.shell && e.cmd.Process.Pid > 0 {
			_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
		} else {
			_ = e.cmd.Process.Kill()
		}
		go e.cmd.Wait()
	}
}

func disposeFallbackShell(p *embedded) (preserved bool, err error) {
	if p == nil {
		return false, nil
	}
	if !p.managed {
		p.close()
		return false, nil
	}
	ownedClients := p.ownedClients()
	preserved, err = cleanupFallbackSession(p.name, ownedClients)
	p.close()
	return preserved, err
}

func cleanupFallbackSession(name string, ownedClients int) (preserved bool, err error) {
	busy, attachedClients, err := sessionRuntimeStatus(name)
	if err != nil {
		return true, err
	}
	if busy || attachedClients > ownedClients {
		return true, nil
	}
	if err := killSession(name); err != nil {
		return true, err
	}
	return false, nil
}

func runIDE(promptMe bool, launchDir string) {
	if err := runIDEWithInitialSession(promptMe, launchDir, ""); err != nil {
		fmt.Fprintf(os.Stderr, "Error opening LAM: %v\n", err)
	}
}

func checkInteractiveTerminal() error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("LAM requires an interactive terminal")
	}
	return nil
}

func runIDEWithInitialSession(promptMe bool, launchDir, initialSession string) error {
	if err := checkInteractiveTerminal(); err != nil {
		return err
	}
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("setting terminal raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)
	outerMouseTracking := false
	outerBracketedPaste := false
	setOuterMouseTracking := func(enabled bool) string {
		if outerMouseTracking == enabled {
			return ""
		}
		outerMouseTracking = enabled
		if enabled {
			return "\033[?1002h\033[?1006h"
		}
		return "\033[?1006l\033[?1002l\033[?1000l"
	}
	setOuterBracketedPaste := func(enabled bool) string {
		if outerBracketedPaste == enabled {
			return ""
		}
		outerBracketedPaste = enabled
		if enabled {
			return "\033[?2004h"
		}
		return "\033[?2004l"
	}
	fmt.Print("\033[?1049h\033[2J", setOuterBracketedPaste(true))
	defer fmt.Print("\033[?7h\033[?2004l\033[?1006l\033[?1002l\033[?1000l\033[?25h\033[?1049l")

	// A hung interface cannot say why. SIGUSR1 writes every goroutine's stack to
	// the state directory, where the screen being raw and alternate cannot hide it.
	dumpSignals := make(chan os.Signal, 1)
	signal.Notify(dumpSignals, syscall.SIGUSR1)
	defer signal.Stop(dumpSignals)
	go func() {
		for range dumpSignals {
			path := filepath.Join(stateDir(), fmt.Sprintf("goroutines-%d.txt", os.Getpid()))
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
				_ = pprof.Lookup("goroutine").WriteTo(f, 2)
				f.Close()
				logEvent("goroutine dump written to %s", path)
			}
		}
	}()
	// Killed by a signal, deferred cleanup does not run and the user's terminal
	// is left raw with the alternate screen and mouse reporting on. The sessions
	// are separate processes, so they are left running.
	stopSignals := make(chan os.Signal, 1)
	signal.Notify(stopSignals, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(stopSignals)
	go func() {
		sig, ok := <-stopSignals
		if !ok {
			return
		}
		fmt.Print("\033[?7h\033[?2004l\033[?1006l\033[?1002l\033[?1000l\033[?25h\033[?1049l")
		_ = term.Restore(fd, oldState)
		code := 128 + int(syscall.SIGTERM)
		if s, isSyscall := sig.(syscall.Signal); isSyscall {
			code = 128 + int(s)
		}
		os.Exit(code)
	}()

	if launchDir == "" {
		launchDir, _ = os.Getwd()
	}
	pathSource, ok := backend().(sessionDirectorySource)
	if !ok {
		pathSource = localDirectorySource{}
	}
	shellTitle := "Shell · " + shortenHomePath(launchDir, pathSource)
	cols, rows, _ := term.GetSize(fd)
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	dirty := newDirtyFlag()
	dirty.Store(true)
	panes := map[string]*embedded{}
	managedShellNames := make(map[string]struct{})
	terminalVisible := false
	terminalFocused := false
	terminalPanes := make(map[string]*embedded)
	closeInactivePanes := func(keep string) {
		for name, p := range panes {
			if name != keep {
				p.close()
				delete(panes, name)
			}
		}
	}
	var emptyShell *embedded
	emptyShellStarted := false
	sidebarCollapsed := false
	settings := loadSettings()
	collapsedProjects := map[string]bool{}
	for _, cwd := range settings.CollapsedProjects {
		collapsedProjects[cwd] = true
	}
	sideWidth := sidebarWidthFor(cols)
	updateSidebarWidth := func() {
		if sidebarCollapsed {
			sideWidth = compactSidebarWidthFor(cols)
		} else {
			sideWidth = sidebarWidthFor(cols)
			if settings.SidebarWidth > 0 {
				sideWidth = clampSidebarWidth(settings.SidebarWidth, cols)
			}
		}
	}
	updateSidebarWidth()
	geom := func() paneGeometry {
		return paneGeometryFor(cols-sideWidth-2, rows-2, terminalVisible, settings.TerminalPlacement, settings.TerminalPercent)
	}
	paneSize := func() (int, int) {
		g := geom()
		return g.mainW, g.mainH
	}
	terminalPaneSize := func() (int, int) {
		g := geom()
		return g.termW, g.termH
	}
	// terminalOrigin is the screen cell (1-based) of the terminal's top-left.
	terminalOrigin := func() (int, int) {
		g := geom()
		if g.right {
			return sideWidth + 2 + g.mainW + 1, 2
		}
		return sideWidth + 2, g.mainH + 3
	}
	const (
		regionNone = iota
		regionMain
		regionTerminal
		regionDivider
	)
	// locate maps a screen cell to the pane under it and the cell inside it.
	locate := func(x, y int) (region, lx, ly int) {
		if x < sideWidth+2 || x >= cols || y < 2 || y >= rows {
			return regionNone, 0, 0
		}
		if g := geom(); g.termH > 0 {
			tx, ty := terminalOrigin()
			if g.right {
				if x == tx-1 {
					return regionDivider, 0, 0
				}
				if x >= tx {
					return regionTerminal, x - tx, y - ty
				}
			} else {
				if y == ty-1 {
					return regionDivider, 0, 0
				}
				if y >= ty {
					return regionTerminal, x - tx, y - ty
				}
			}
		}
		return regionMain, x - (sideWidth + 2), y - 2
	}
	resizePanes := func() {
		w, h := paneSize()
		for _, p := range panes {
			p.resize(w, h)
		}
		if emptyShell != nil {
			emptyShell.resize(w, h)
		}
		if len(terminalPanes) > 0 {
			if terminalW, terminalH := terminalPaneSize(); terminalH > 0 {
				for _, p := range terminalPanes {
					p.resize(terminalW, terminalH)
				}
			}
		}
	}

	input := make(chan []byte, 32)
	stopInput := make(chan struct{})
	defer close(stopInput)
	go func() {
		defer close(input)
		buf := make([]byte, 4096)
		for {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			ready, pollErr := unix.Poll(fds, 100)
			if pollErr != nil || ready == 0 {
				continue
			}
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case input <- append([]byte(nil), buf[:n]...):
				case <-stopInput:
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	remoteMode := backend().Name() != "native"
	var sessions []sessionInfo
	var listErr error
	if !remoteMode {
		sessions, listErr = listSessionsWithError()
		if listErr != nil {
			sessions = nil
		}
		sessions = hideOwnedTerminals(sessions)
	}
	stopTerminalsOnExit := false
	defer func() {
		for _, p := range panes {
			if p.managed {
				_, _ = disposeFallbackShell(p)
			} else {
				p.close()
			}
		}
		if emptyShell != nil {
			_, _ = disposeFallbackShell(emptyShell)
		}
		for _, p := range terminalPanes {
			if stopTerminalsOnExit {
				_, _ = disposeFallbackShell(p)
			} else {
				p.close() // detach: the shell keeps running for the next run
			}
		}
	}()
	managedShellName := ""
	lastSession := lastSelectedSession()
	sessionCursor := preferredSessionIndex(sessions, lastSession)
	sessionSelectionInitialized := !remoteMode && listErr == nil
	focusSession := false
	mode := ""
	textBuf := []byte(nil)
	wizard := sessionWizard{}
	filter := ""
	message := ""
	messageShown, messageSince := "", time.Time{}
	var killTarget *sessionInfo
	var restoreCandidates []manifestEntry
	settingsRow := 0
	dialogChoice := 0 // highlighted button of the stop/restore dialog; 0 is the default
	var renameTarget *sessionInfo
	renameCursor := 0
	renameError := ""
	lastList := time.Now()
	decoder := newTerminalInputDecoder(focusChordFromEnv())
	shellInput := shellCommandTracker{trackable: true}
	mouseCaptureSession := ""
	mouseCaptureShell := false
	mouseCaptureTerminal := false
	dividerDrag := false // the divider between the panes is being dragged
	sidebarDrag := false // the edge of the sidebar is being dragged
	var textSelection terminalTextSelection
	var lastSelectionScroll time.Time
	var dragStartWidth int
	var lastEdgePress time.Time              // for the double-click that resets the sidebar width
	var lastSelectionDrag terminalMouseEvent // repeated on ticks, so a held pointer keeps scrolling
	const escapeDelay = 40 * time.Millisecond

	visibleSessions := func() []sessionInfo { return filteredSessions(sessions, filter) }
	if initialSession != "" {
		for i, session := range visibleSessions() {
			if session.Name == initialSession {
				sessionCursor = i
				focusSession = true
				break
			}
		}
		if !hasSession(sessions, initialSession) {
			message = fmt.Sprintf("Session %s ended before LAM opened", initialSession)
		}
	}
	restorer, canRestore := backend().(sessionRestorer)
	if initialSession == "" && canRestore {
		// An older remote without these operations just has nothing to offer.
		if entries, err := restorer.RestorableSessions(); err == nil && len(entries) > 0 {
			restoreCandidates = entries
			mode = "restore"
			dialogChoice = 0
		}
	}
	restoreNames := func() []string {
		names := make([]string, len(restoreCandidates))
		for i, e := range restoreCandidates {
			names[i] = e.Name
		}
		return names
	}
	selectedSession := func() (sessionInfo, bool) {
		visible := visibleSessions()
		if sessionCursor < 0 || sessionCursor >= len(visible) {
			return sessionInfo{}, false
		}
		return visible[sessionCursor], true
	}
	awake := newKeepAwake()
	linkWasUp, linkGen, lastLinkDraw := true, 0, time.Time{}
	defer awake.Release()
	pendingNewSession := ""
	remoteListUpdates := make(chan sessionListResult, 1)
	remoteListInFlight := false
	filteredManagedShells := func(next []sessionInfo) []sessionInfo {
		next = hideOwnedTerminals(next)
		if managedShellName == "" && len(managedShellNames) == 0 {
			return next
		}
		visible := make([]sessionInfo, 0, len(next))
		for _, session := range next {
			_, managed := managedShellNames[session.Name]
			if session.Name != managedShellName && !managed {
				visible = append(visible, session)
			}
		}
		return visible
	}
	// The list is polled every second; redraw only when what is shown changed.
	lastListSignature, lastListError := "", ""
	listSignature := func(list []sessionInfo) string {
		var sig strings.Builder
		for _, s := range list {
			unread := s.Unread || (!remoteMode && hasUnread(s.Name))
			fmt.Fprintf(&sig, "%s\x00%s\x00%s\x00%s\x00%v\x00%v\n", s.Name, s.Identity, s.CWD, s.Activity, unread, s.Shell)
		}
		return sig.String()
	}
	applySessionList := func(next []sessionInfo, err error) {
		if err != nil {
			listErr = err
			if err.Error() != lastListError {
				lastListError = err.Error()
				dirty.Store(true)
			}
			return
		}
		lastListError = ""
		next = filteredManagedShells(next)
		if remoteMode {
			// The agents run elsewhere; this machine must stay up to keep the link
			// (the ticker also holds it while the link is up).
			for _, s := range next {
				if active, _ := sessionActivity(s); active {
					awake.Touch()
					break
				}
			}
		}
		if sig := listSignature(next); sig != lastListSignature {
			lastListSignature = sig
			dirty.Store(true)
		}
		oldVisible := visibleSessions()
		listErr = nil
		nextVisible := filteredSessions(next, filter)
		if !sessionSelectionInitialized {
			sessionCursor = preferredSessionIndex(nextVisible, lastSession)
			sessionSelectionInitialized = true
		} else {
			sessionCursor = preserveSessionSelection(oldVisible, sessionCursor, nextVisible)
		}
		sessions = next
		if pendingNewSession != "" {
			for i, s := range nextVisible {
				if s.Name == pendingNewSession {
					sessionCursor = i
					rememberSelectedSession(s.Name)
					focusSession = true
					pendingNewSession = ""
					break
				}
			}
		}
		if sessionCursor < 0 && len(sessions) > 0 {
			focusSession = false
		}
		for name, p := range panes {
			var found *sessionInfo
			for i := range sessions {
				if sessions[i].Name == name {
					found = &sessions[i]
					break
				}
			}
			if found == nil || p.identity != sessionIdentity(*found) {
				p.close()
				delete(panes, name)
			}
		}
		for owner, p := range terminalPanes {
			var found bool
			for i := range sessions {
				if sessions[i].Name == owner {
					found = true
					break
				}
			}
			if !found {
				delete(terminalPanes, owner)
				if textSelection.pane == p {
					textSelection = terminalTextSelection{}
				}
				delete(managedShellNames, p.name)
				// Best effort: keep a busy terminal as a standalone session,
				// otherwise drop it. The next list refresh picks up the result.
				_, _ = disposeFallbackShell(p)
			}
		}
	}
	refreshSessions := func() {
		if remoteMode {
			if remoteListInFlight {
				return
			}
			remoteListInFlight = true
			go func() {
				next, err := listSessionsWithError()
				remoteListUpdates <- sessionListResult{sessions: next, err: err}
			}()
			return
		}
		next, err := listSessionsWithError()
		applySessionList(next, err)
	}
	// refreshSessionsInBackground is for the periodic refresh: asking every host
	// takes as long as the slowest one, and the interface must not wait for it.
	refreshSessionsInBackground := func() {
		if remoteListInFlight {
			return
		}
		remoteListInFlight = true
		go func() {
			next, err := listSessionsWithError()
			remoteListUpdates <- sessionListResult{sessions: next, err: err}
		}()
	}
	if remoteMode {
		refreshSessions()
	}
	current := func() *embedded {
		if len(sessions) == 0 && sessionSelectionInitialized && (listErr == nil || emptyShell != nil) {
			if emptyShell == nil && !emptyShellStarted {
				emptyShellStarted = true
				w, h := paneSize()
				managedShellName = launcherSessionName("terminal", launchDir, sessions)
				managedShellNames[managedShellName] = struct{}{}
				p, err := startShellEmbedded(managedShellName, launchDir, w, h, dirty)
				if err != nil {
					delete(managedShellNames, managedShellName)
					managedShellName = ""
					message = fmt.Sprintf("Start shell: %v", err)
				} else {
					emptyShell = p
					focusSession = true
				}
			}
			if emptyShell != nil {
				if emptyShell.done.Load() {
					focusSession = false
				}
				return emptyShell
			}
			return nil
		}
		if emptyShell != nil {
			p := emptyShell
			emptyShell = nil
			emptyShellStarted = false
			delete(managedShellNames, managedShellName)
			managedShellName = ""
			preserved, err := disposeFallbackShell(p)
			if err != nil {
				message = fmt.Sprintf("Keep shell %s: %v", p.name, err)
			}
			if preserved {
				refreshSessions()
			}
		}
		s, ok := selectedSession()
		if !ok {
			closeInactivePanes("")
			return nil
		}
		closeInactivePanes(s.Name)
		if p, ok := panes[s.Name]; ok {
			if !p.done.Load() && p.identity == sessionIdentity(s) {
				return p
			}
			if remoteMode && p.done.Load() && p.identity == sessionIdentity(s) && !currentLinkStatus().Up {
				return p // the screen stays as it was until the connection returns
			}
			if p.done.Load() && time.Since(p.started) < paneRestartHold {
				// It ended right after attaching: the session is most likely gone.
				// Attaching again at once would loop for as long as the list
				// still names it, so show it as ended until the list catches up.
				if !p.restartHeld {
					p.restartHeld = true
					refreshSessions()
				}
				return p
			}
			p.close()
			delete(panes, s.Name)
		}
		w, h := paneSize()
		p, err := startEmbedded(s.Name, w, h, dirty)
		if err != nil {
			message = fmt.Sprintf("Attach %s: %v", s.Name, err)
			return nil
		}
		p.identity = sessionIdentity(s)
		if s.Shell {
			// Listed shell sessions were created intentionally and survive UI exit.
			// Only the empty-state shell created above is temporary/managed.
			p.shell = true
			p.setWorkingDirectory(s.CWD)
		}
		panes[s.Name] = p
		return p
	}
	disposeTerminalPane := func(owner string, p *embedded) {
		delete(terminalPanes, owner)
		if textSelection.pane == p {
			textSelection = terminalTextSelection{}
		}
		delete(managedShellNames, p.name)
		// A terminal whose shell exited may already have been removed by the
		// host. Avoid reporting that normal exit as a cleanup failure.
		if p.done.Load() {
			if listed, listErr := backend().List(); listErr == nil && !hasSession(listed, p.name) {
				p.close()
				return
			}
		}
		// Closing the pane is an explicit request: end the shell even if it is
		// busy, rather than leaving a stray "term-" entry in the session list.
		p.close()
		if err := killSession(p.name); err != nil {
			if listed, listErr := backend().List(); listErr != nil || hasSession(listed, p.name) {
				message = fmt.Sprintf("Close terminal %s: %v", p.name, err)
			}
		}
		refreshSessions()
	}
	closeIntegratedTerminal := func() {
		s, ok := selectedSession()
		if !ok {
			return
		}
		owner := s.Name
		p, ok := terminalPanes[owner]
		if !ok {
			return
		}
		terminalFocused = false
		mouseCaptureTerminal = false
		disposeTerminalPane(owner, p)
	}
	currentIntegratedTerminal := func() *embedded {
		if !terminalVisible {
			return nil
		}
		s, ok := selectedSession()
		if !ok {
			// Nothing to attach a terminal to: do not keep reserving its space.
			terminalVisible = false
			terminalFocused = false
			resizePanes()
			dirty.Store(true)
			return nil
		}
		owner := s.Name
		if p, ok := terminalPanes[owner]; ok {
			if p.done.Load() && remoteMode {
				st := currentLinkStatus()
				if !st.Up {
					return p // keep the terminal on screen until the connection returns
				}
				if p.linkGen != st.Gen {
					// The connection was replaced; the shell kept running on the
					// remote host, so attach to it again.
					w, h := terminalPaneSize()
					if np, err := startEmbedded(p.name, w, h, dirty); err == nil {
						np.shell, np.managed = p.shell, p.managed
						np.setWorkingDirectory(p.workingDirectory())
						if textSelection.pane == p {
							textSelection = terminalTextSelection{}
						}
						p.close()
						terminalPanes[owner] = np
						dirty.Store(true)
						return np
					}
				}
			}
			if p.done.Load() {
				terminalVisible = false
				terminalFocused = false
				disposeTerminalPane(owner, p)
				resizePanes()
				dirty.Store(true)
				return nil
			}
			return p
		}
		cwd := s.CWD
		if liveCWD, err := sessionProcessWorkingDirectory(s.Name); err == nil && liveCWD != "" {
			cwd = liveCWD
		}
		if cwd == "" {
			cwd = launchDir
		}
		w, h := terminalPaneSize()
		if h < 1 {
			return nil
		}
		terminalLabel := terminalLabelFor(s.Name)
		// A terminal left running by an earlier run is attached again.
		if listed, listErr := backend().List(); listErr == nil && !terminalLabelAmbiguous(listed, s.Name) {
			inUse := make(map[string]bool)
			for _, tp := range terminalPanes {
				inUse[tp.name] = true
			}
			for _, cand := range listed {
				if label, ok := terminalSessionLabel(cand.Name); !ok || label != terminalLabel || inUse[cand.Name] {
					continue
				}
				managedShellNames[cand.Name] = struct{}{}
				if np, err := startEmbedded(cand.Name, w, h, dirty); err == nil {
					np.shell, np.managed = true, true
					np.setWorkingDirectory(cand.CWD)
					terminalPanes[owner] = np
					return np
				}
				delete(managedShellNames, cand.Name)
			}
		}
		name := "term-" + terminalLabel + "-" + newUUID()[:8]
		managedShellNames[name] = struct{}{}
		p, err := startShellEmbedded(name, cwd, w, h, dirty)
		if err != nil {
			delete(managedShellNames, name)
			message = fmt.Sprintf("Start terminal: %v", err)
			return nil
		}
		terminalPanes[owner] = p
		return p
	}
	// toggleProject folds a project in the sidebar or opens it again.
	toggleProject := func(cwd string) {
		if collapsedProjects[cwd] {
			delete(collapsedProjects, cwd)
		} else {
			collapsedProjects[cwd] = true
		}
		settings.CollapsedProjects = settings.CollapsedProjects[:0]
		for folder := range collapsedProjects {
			settings.CollapsedProjects = append(settings.CollapsedProjects, folder)
		}
		sort.Strings(settings.CollapsedProjects)
		saveSettings(settings)
		dirty.Store(true)
	}
	selectSession := func(i int) bool {
		visible := visibleSessions()
		if i < 0 || i >= len(visible) {
			return false
		}
		textSelection = terminalTextSelection{}
		sessionCursor = i
		rememberSelectedSession(visible[i].Name)
		if !remoteMode {
			clearUnread(visible[i].Name)
			for j := range sessions {
				if sessions[j].Name == visible[i].Name {
					sessions[j].Unread = false
				}
			}
		}
		dirty.Store(true)
		return true
	}
	clearFocusedUnread := func() {
		selected, ok := selectedSession()
		if !ok {
			return
		}
		for i := range sessions {
			if sessions[i].Name == selected.Name {
				sessions[i].Unread = false
				break
			}
		}
		if remoteMode {
			go clearUnread(selected.Name)
		} else {
			clearUnread(selected.Name)
		}
	}
	togglePaneFocus := func() {
		if mode == "new-session" || mode == "rename" {
			return
		}
		if terminalVisible {
			switch {
			case !focusSession:
				focusSession = true
				terminalFocused = false
				if selected, ok := selectedSession(); ok {
					rememberSelectedSession(selected.Name)
					clearFocusedUnread()
				}
			case !terminalFocused:
				terminalFocused = true
				shellInput.reset()
			default:
				focusSession = false
				terminalFocused = false
				shellInput.reset()
			}
		} else if focusSession {
			focusSession = false
			shellInput.reset()
		} else {
			focusSession = true
			terminalFocused = false
			if selected, ok := selectedSession(); ok {
				rememberSelectedSession(selected.Name)
				clearFocusedUnread()
			}
		}
		dirty.Store(true)
	}
	toggleIntegratedTerminal := func() {
		if mode != "" {
			return
		}
		if terminalVisible {
			terminalVisible = false
			closeIntegratedTerminal()
			terminalFocused = false
			resizePanes()
		} else {
			if _, ok := selectedSession(); !ok {
				message = "Select a session before opening its terminal"
				dirty.Store(true)
				return
			}
			terminalVisible = true
			focusSession = true
			terminalFocused = true
			resizePanes()
			currentIntegratedTerminal()
		}
		shellInput.reset()
		dirty.Store(true)
	}
	toggleSidebar := func() {
		sidebarCollapsed = !sidebarCollapsed
		updateSidebarWidth()
		resizePanes()
		dirty.Store(true)
	}

	ticker := time.NewTicker(time.Second / uiFrameRate)
	defer ticker.Stop()
	// Frames are drawn as soon as something changes. A short burst may go out
	// back to back (a key press redraws, and its echo arrives a moment later),
	// but over time no more than uiFrameRate frames a second are drawn, so a
	// program that floods the screen costs no more than it did when frames
	// were drawn on a timer.
	const frameBurst = 3.0
	frameTokens := frameBurst
	lastFrameRefill := time.Now()
	drawTimer := time.NewTimer(time.Hour)
	drawTimer.Stop()
	drawTimerArmed := false
	var frames frameDiff
	lastOverlay := "" // kind and size of the dialog drawn on top of the last frame
	lastFullRepaint := time.Now()
	draw := func() {
		var b strings.Builder
		pw, ph := paneSize()
		contentRows := max(0, rows-2)
		g := geom()
		termBelow, termRight := 0, g.right && g.termH > 0
		if !g.right {
			termBelow = g.termH
		}
		terminalRows := g.termH
		var cur *embedded
		if mode != "new-session" && mode != "rename" {
			cur = current()
		}
		var terminalCurrent *embedded
		var terminalLines []string
		if mode != "new-session" && mode != "rename" && terminalRows > 0 {
			terminalCurrent = currentIntegratedTerminal()
			if terminalCurrent != nil {
				terminalLines = strings.Split(terminalCurrent.renderViewportWithSelection(&textSelection), "\n")
			}
		}
		mouseCaptureNeeded := outerMouseCaptureNeeded(mode)
		b.WriteString(setOuterMouseTracking(mouseCaptureNeeded))
		// Autowrap off: a row the terminal measures wider than we do must be
		// clipped at the right edge, not wrapped onto (and over) the next row.
		b.WriteString("\033[?2026h\033[?7l\033[?25l")
		bodyStart := b.Len()
		var lines []string
		wizardCursorRow, wizardCursorCol := -1, 0
		if mode == "new-session" {
			lines, wizardCursorRow, wizardCursorCol = wizard.screen(textBuf, ph)
		} else if mode == "rename" && renameTarget != nil {
			lines = []string{"Rename session", "", "Current name: " + renameTarget.Name, "New name: " + string(textBuf), "Enter rename · Esc cancel"}
			wizardCursorRow = 3
			pos := max(0, min(renameCursor, len(textBuf)))
			wizardCursorCol = ansi.StringWidth("New name: " + string(textBuf[:pos]))
			if renameError != "" {
				lines = append(lines, "Error: "+renameError)
			}
		} else if cur != nil {
			lines = strings.Split(cur.renderViewportWithSelection(&textSelection), "\n")
		}
		leftColor, rightColor := "\033[38;5;240m", "\033[38;5;240m"
		if (focusSession && !terminalFocused) || mode == "new-session" || mode == "rename" {
			rightColor = "\033[38;5;108m"
		} else if !focusSession {
			leftColor = "\033[38;5;108m"
		}
		leftInner := sideWidth - 2
		if leftInner < 1 {
			leftInner = 1
		}
		drawBorder := func(row, col, width int, title, color string) {
			if width < 2 {
				width = 2
			}
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s┌%s┐\033[0m", row, col, color, fitCell(" "+title+" ", width-2)))
		}
		drawBottom := func(row, col, width int, color, label string) {
			if width < 2 {
				width = 2
			}
			inner := width - 2
			text := ""
			if label != "" && inner > 4 {
				text = ansi.Truncate(" "+label+" ", inner, "…")
			}
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s└\033[38;5;250m%s%s%s┘\033[0m", row, col, color, text, color, strings.Repeat("─", inner-ansi.StringWidth(text))))
		}
		title := "Sessions"
		if remoteMode {
			remoteName := strings.TrimPrefix(backend().Name(), "ssh:")
			title += " · " + remoteName
		}
		if filter != "" {
			title += " /" + filter
		}
		rightTitle := "Session"
		if s, ok := selectedSession(); ok {
			state := ""
			if active, known := sessionActivity(s); known {
				state = "quiet"
				if active {
					state = "active"
				}
			} else {
				state = "?"
			}
			rightTitle = state + " · " + s.Name
		} else if emptyShell != nil || (len(sessions) == 0 && sessionSelectionInitialized && listErr == nil) {
			rightTitle = shellTitle
		} else if remoteMode && !sessionSelectionInitialized {
			rightTitle = "Connecting"
		}
		if mode == "new-session" {
			rightTitle = "New session"
		} else if mode == "rename" {
			rightTitle = "Rename session"
		}
		leftTitle := "LAM · " + title
		if sidebarCollapsed {
			leftTitle = "LAM ›"
		}
		drawBorder(1, 1, sideWidth, leftTitle, leftColor)
		drawBorder(1, sideWidth+1, cols-sideWidth, rightTitle, rightColor)

		listCursor := sessionCursor
		listRows := rows - 4 // header, bottom border, Settings and Help rows
		if listRows < 0 {
			listRows = 0
		}
		visibleNow := visibleSessions()
		sidebarRows := buildSidebarRows(visibleNow, sidebarCollapsed, collapsedProjects, listCursor)
		rowStart, rowEnd := sidebarViewport(sidebarRows, listCursor, listRows)
		// sidebarLines says what each list line shows: a row and the line of it.
		type sidebarLine struct{ row, line int }
		var sidebarLines []sidebarLine
		for ri := rowStart; ri < rowEnd; ri++ {
			for l := 0; l < sidebarRows[ri].height; l++ {
				sidebarLines = append(sidebarLines, sidebarLine{ri, l})
			}
		}
		for r := 0; r < contentRows; r++ {
			screenRow := r + 2
			// Clear the physical row before painting it. The pane contents are
			// produced by another terminal emulator and may contain wide or
			// grapheme-cluster characters whose display width differs from the
			// outer terminal's width calculation. Clearing makes a short redraw
			// unable to leave characters from the previous frame behind.
			b.WriteString(fmt.Sprintf("\033[%d;1H\033[2K%s│\033[0m", screenRow, leftColor))
			cell := ""
			if r < len(sidebarLines) && sidebarRows[sidebarLines[r].row].header {
				h := sidebarRows[sidebarLines[r].row]
				count := fmt.Sprintf("%d ", h.count)
				arrow := "▾"
				if h.collapsed {
					arrow = "▸"
				}
				name := fitCell(" "+arrow+" "+h.label, leftInner-len(count))
				cell = "\033[1;38;5;110m" + name + "\033[0m\033[38;5;240m" + count + "\033[0m"
			} else if r < len(sidebarLines) {
				i := sidebarRows[sidebarLines[r].row].session
				{
					s := visibleNow[i]
					num := " "
					if i < 9 {
						num = fmt.Sprint(i + 1)
					}
					unreadDot := " "
					if s.Unread || (!remoteMode && hasUnread(s.Name)) {
						unreadDot = "\033[38;5;203m●\033[0m"
					}
					selected := i == sessionCursor
					selectionBG := "\033[48;5;22m"
					if focusSession {
						selectionBG = "\033[48;5;236m"
					}
					if sidebarCollapsed {
						nameWidth := leftInner - 3
						if nameWidth < 0 {
							nameWidth = 0
						}
						cell = fmt.Sprintf("%s%s%s%s", num, activityMarker(s), fitCell(s.Name, nameWidth), unreadDot)
						if selected {
							cell = selectionBG + fitCell(cell, leftInner) + "\033[0m"
						}
					} else {
						nameWidth := leftInner - 7
						if nameWidth < 1 {
							nameWidth = 1
						}
						label := fitCell(sessionLabel(s), nameWidth-2)
						if selected {
							label = "\033[1;38;5;255m" + label + "\033[22m"
						} else if s.Unread || (!remoteMode && hasUnread(s.Name)) {
							label = "\033[1m" + label + "\033[22m"
						}
						edge := " "
						if selected {
							edge = "\033[38;5;110m▌\033[39m" // a bright bar marks the selection
						}
						cell = fmt.Sprintf("%s %s %s %s %s", edge, num, activityMarker(s), label, unreadDot)
						if selected {
							cell = selectionBG + fitCell(cell, leftInner) + "\033[0m"
						}
					}
				}
			} else if r == 1 && len(visibleSessions()) == 0 {
				label := "No sessions"
				if len(sessions) > 0 {
					label = "No matches"
				}
				cell = "\033[38;5;240m " + label + "\033[0m"
			}
			if r == contentRows-2 && contentRows >= 3 {
				if sidebarCollapsed {
					cell = "\033[38;5;240m s"
				} else {
					cell = "\033[38;5;240m s Settings"
				}
			}
			if r == contentRows-1 {
				switch {
				case mode == "filter":
					cell = "\033[38;5;250m /" + string(textBuf) + "_"
				case sidebarCollapsed:
					cell = "\033[38;5;240m ?"
				default:
					cell = "\033[38;5;240m ? Help"
				}
			}
			b.WriteString(fitCell(cell, leftInner))
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s│\033[0m", screenRow, sideWidth, leftColor))
			if termBelow > 0 && r == ph {
				label := " Terminal "
				if terminalCurrent != nil {
					cwd := terminalCurrent.workingDirectory()
					if cwd != "" {
						label = " Terminal · " + shortenHomePath(cwd, pathSource) + " "
					}
				}
				labelWidth := min(pw, ansi.StringWidth(label))
				divider := "├" + fitCell(label, labelWidth) + strings.Repeat("─", max(0, pw-labelWidth)) + "┤"
				dividerColor := rightColor
				if terminalFocused && focusSession {
					dividerColor = "\033[38;5;108m"
				}
				b.WriteString(fmt.Sprintf("\033[%d;%dH%s%s\033[0m", screenRow, sideWidth+1, dividerColor, divider))
				continue
			}
			paneColor := rightColor
			if termBelow > 0 && r > ph && terminalFocused && focusSession {
				paneColor = "\033[38;5;108m"
			}
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s│\033[0m", screenRow, sideWidth+1, paneColor))
			right := strings.Repeat(" ", pw)
			if termBelow > 0 && r > ph {
				terminalRow := r - ph - 1
				if terminalRow < len(terminalLines) {
					right = fitCell(terminalLines[terminalRow], pw)
				}
			} else if r < len(lines) {
				right = fitCell(lines[r], pw)
			}
			if mode != "new-session" && mode != "rename" && len(visibleSessions()) == 0 {
				if len(sessions) > 0 && r == 1 {
					right = fitCell("No sessions match this filter", pw)
				} else if len(sessions) == 0 && emptyShell == nil && r == 1 {
					if remoteMode && !sessionSelectionInitialized {
						right = fitCell("Connecting to remote…", pw)
					} else {
						right = fitCell("Shell unavailable", pw)
					}
				}
			}
			b.WriteString("\033[0m")
			b.WriteString(right)
			b.WriteString("\033[0m")
			if termRight {
				termColor := "\033[38;5;240m"
				if terminalFocused && focusSession {
					termColor = "\033[38;5;108m"
				}
				termLine := strings.Repeat(" ", g.termW)
				if r < len(terminalLines) {
					termLine = fitCell(terminalLines[r], g.termW)
				}
				dividerCol := sideWidth + 2 + g.mainW
				b.WriteString(fmt.Sprintf("\033[%d;%dH%s│\033[0m\033[%d;%dH%s", screenRow, dividerCol, termColor, screenRow, dividerCol+1, termLine))
				paneColor = termColor
			}
			// Position the right border absolutely rather than relying on the
			// cursor left by the rendered pane text.
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s│\033[0m", screenRow, cols, paneColor))
		}
		barRight := ""
		switch {
		case remoteMode && !linkWasUp:
			barRight = linkStatusText(currentLinkStatus(), time.Now())
		case message != "":
			barRight = message
		case listErr != nil:
			barRight = "List error: " + listErr.Error()
		}
		drawBottom(rows, 1, sideWidth, leftColor, "")
		drawBottom(rows, sideWidth+1, cols-sideWidth, rightColor, barRight)
		if termRight {
			termColor := "\033[38;5;240m"
			if terminalFocused && focusSession {
				termColor = "\033[38;5;108m"
			}
			dividerCol := sideWidth + 2 + g.mainW
			label := " Terminal "
			if terminalCurrent != nil {
				if cwd := terminalCurrent.workingDirectory(); cwd != "" {
					label = " Terminal · " + shortenHomePath(cwd, pathSource) + " "
				}
			}
			inner := g.termW
			label = fitCell(label, min(inner, ansi.StringWidth(label)))
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s┬%s%s┐\033[0m", 1, dividerCol, termColor, label, strings.Repeat("─", max(0, inner-ansi.StringWidth(label)))))
			b.WriteString(fmt.Sprintf("\033[%d;%dH%s┴%s┘\033[0m", rows, dividerCol, termColor, strings.Repeat("─", inner)))
		}
		modal := mode == "help" || mode == "kill" || mode == "restore" || mode == "settings" || mode == "quit-all"
		overlayText := ""
		switch {
		case mode == "help":
			overlayText = keyboardHelpOverlay(cols, rows)
		case mode == "settings":
			overlayText = settingsOverlay(cols, rows, settings, settingsRow)
		case mode == "kill" && killTarget != nil:
			overlayText = confirmDialog{
				title:   "Stop session",
				body:    []string{fmt.Sprintf("Stop %q?", killTarget.Name), "Its running program will be terminated."},
				buttons: [2]string{"OK", "Cancel"},
				hint:    "Esc cancel",
			}.overlay(cols, rows, dialogChoice)
		case mode == "quit-all":
			overlayText = confirmDialog{
				title:   "Quit and stop everything",
				body:    quitAllBody(sessions, remoteMode),
				buttons: [2]string{"Stop all", "Cancel"},
				hint:    "Esc cancel",
			}.overlay(cols, rows, dialogChoice)
		case mode == "restore":
			overlayText = confirmDialog{
				title:   "Restore sessions",
				body:    restoreDialogBody(restoreCandidates),
				buttons: [2]string{"Restore", "Discard"},
				hint:    "Esc later",
			}.overlay(cols, rows, dialogChoice)
		}
		// Send only the rows that changed. A dialog is drawn over rows that do
		// not change, so when its kind or size changes (or it goes away) what
		// the terminal shows is no longer what the cache says: send everything.
		// The cache is also dropped on a timer, as a safety net against
		// anything that disturbed the screen from outside.
		overlayKey := fmt.Sprintf("%s/%d", mode, len(overlayText))
		if !modal {
			overlayKey = ""
		}
		if overlayKey != lastOverlay || time.Since(lastFullRepaint) > fullRepaintEvery {
			frames.reset()
			lastOverlay = overlayKey
			lastFullRepaint = time.Now()
		}
		all := b.String()
		body := frames.diff(all[bodyStart:])
		var tail strings.Builder
		tail.WriteString(overlayText)
		if !modal && (mode == "new-session" || mode == "rename") && wizardCursorRow >= 0 && wizardCursorRow < ph {
			cursorX := sideWidth + 2 + max(0, min(wizardCursorCol, pw-1))
			tail.WriteString(fmt.Sprintf("\033[%d;%dH\033[?25h", wizardCursorRow+2, cursorX))
		} else if !modal && focusSession && terminalVisible && terminalFocused && terminalCurrent != nil && !terminalCurrent.done.Load() {
			pos := terminalCurrent.em.CursorPosition()
			terminalW, terminalH := terminalPaneSize()
			if !terminalCurrent.em.CursorHidden() && pos.X >= 0 && pos.X < terminalW && pos.Y >= 0 && pos.Y < terminalH {
				tx, ty := terminalOrigin()
				tail.WriteString(fmt.Sprintf("\033[%d;%dH\033[?25h", ty+pos.Y, tx+pos.X))
			}
		} else if !modal && focusSession && cur != nil && !cur.done.Load() {
			pos := cur.em.CursorPosition()
			if !cur.em.CursorHidden() && pos.X >= 0 && pos.X < pw && pos.Y >= 0 && pos.Y < ph {
				tail.WriteString(fmt.Sprintf("\033[%d;%dH\033[?25h", pos.Y+2, sideWidth+2+pos.X))
			}
		}
		tail.WriteString("\033[?7h\033[?2026l")
		if err := writeAll(os.Stdout, []byte(all[:bodyStart]+body+tail.String())); err != nil {
			// Some rows may not have reached the terminal: resend everything.
			frames.reset()
			dirty.Store(true)
		}
	}

	// tryDraw draws now if a frame is due, otherwise when it will be.
	tryDraw := func() {
		if !dirty.Load() {
			return
		}
		now := time.Now()
		frameTokens = min(frameBurst, frameTokens+now.Sub(lastFrameRefill).Seconds()*uiFrameRate)
		lastFrameRefill = now
		if frameTokens < 1 {
			if !drawTimerArmed {
				drawTimer.Reset(time.Duration((1 - frameTokens) / uiFrameRate * float64(time.Second)))
				drawTimerArmed = true
			}
			return
		}
		frameTokens--
		dirty.Swap(false)
		draw()
	}

	setWizardStage := func(stage string, value []byte) {
		wizard.stage = stage
		textBuf = append(textBuf[:0], value...)
		wizard.cursor = len(textBuf)
		wizard.error = ""
		wizard.completionMatches = nil
		wizard.lastCompletion = ""
		wizard.showCompletions = false
	}
	beginNewSession := func() {
		if terminalVisible {
			terminalVisible = false
			closeIntegratedTerminal()
			terminalFocused = false
			resizePanes()
		}
		defaultDir := normalizedSessionDir(launchDir, pathSource)
		wizard = sessionWizard{
			stage:             wizardWorkdir,
			defaultDir:        defaultDir,
			defaultDirDisplay: shortenHomePath(defaultDir, pathSource),
			promptEnabled:     promptMe,
		}
		if promptMe {
			wizard.stage = wizardPrompt
		}
		mode = "new-session"
		textBuf = nil
		message = ""
		focusSession = false
		dirty.Store(true)
	}
	completeWorkdir := func() {
		if wizard.stage != wizardWorkdir {
			return
		}
		currentInput := string(textBuf)
		if wizard.lastCompletion == currentInput && len(wizard.completionMatches) > 1 {
			wizard.showCompletions = true
			return
		}
		completer, ok := pathSource.(sessionDirectoryCompleter)
		if !ok {
			wizard.error = "Directory completion is unavailable"
			return
		}
		matches, err := completer.CompleteSessionDir(currentInput, wizard.defaultDir)
		if err != nil {
			wizard.error = err.Error()
			wizard.completionMatches = nil
			wizard.showCompletions = false
			return
		}
		if len(matches) == 0 {
			wizard.error = "No matching directories"
			wizard.completionMatches = nil
			wizard.showCompletions = false
			return
		}
		wizard.error = ""
		wizard.completionMatches = matches
		if len(matches) == 1 {
			textBuf = []byte(matches[0])
			wizard.cursor = len(textBuf)
			wizard.lastCompletion = ""
			wizard.showCompletions = false
			return
		}
		if prefix := commonCompletion(currentInput, matches); prefix != currentInput {
			textBuf = []byte(prefix)
			wizard.cursor = len(textBuf)
			wizard.lastCompletion = prefix
			wizard.showCompletions = false
			return
		}
		wizard.lastCompletion = currentInput
		wizard.showCompletions = true
	}
	backNewSession := func() {
		switch wizard.stage {
		case wizardPrompt:
			mode = ""
			textBuf = nil
			wizard = sessionWizard{}
		case wizardWorkdir:
			wizard.pathInput = append(wizard.pathInput[:0], textBuf...)
			if wizard.promptEnabled {
				setWizardStage(wizardPrompt, []byte(wizard.prompt))
			} else {
				mode = ""
				textBuf = nil
				wizard = sessionWizard{}
			}
		case wizardProvider:
			setWizardStage(wizardWorkdir, wizard.pathInput)
		}
		dirty.Store(true)
	}
	advanceNewSession := func() {
		switch wizard.stage {
		case wizardPrompt:
			wizard.prompt = string(textBuf)
			wizard.pathInput = nil
			setWizardStage(wizardWorkdir, wizard.pathInput)
		case wizardWorkdir:
			wizard.pathInput = append(wizard.pathInput[:0], textBuf...)
			dirInput := strings.TrimSpace(string(textBuf))
			if dirInput == "" {
				dirInput = wizard.defaultDir
			}
			dir, err := pathSource.ResolveSessionDir(dirInput, wizard.defaultDir)
			if err != nil {
				wizard.error = err.Error()
				return
			}
			wizard.workdir = dir
			wizard.workdirDisplay = shortenHomePath(dir, pathSource)
			available, err := availableSessionLaunchers()
			if err != nil {
				wizard.error = err.Error()
				return
			}
			wizard.providers, wizard.selectedProvider = sessionProviderChoices(available, getLauncher())
			wizard.sessions = sessions
			setWizardStage(wizardProvider, nil)
		case wizardProvider:
			if wizard.selectedProvider < 0 || wizard.selectedProvider >= len(wizard.providers) {
				wizard.error = "No provider is available"
				return
			}
			launcher := wizard.providers[wizard.selectedProvider]
			command := launcherCmd(launcher, wizard.prompt)
			if launcher == "terminal" {
				command = fallbackShellCommand
			}
			wizard.sessions = sessions
			name := wizard.nameFor(wizard.selectedProvider)
			if err := newSession(name, wizard.workdir, command); err != nil {
				wizard.error = err.Error()
				return
			}
			if launcher != "terminal" {
				setLauncher(launcher)
			}
			created := name
			mode = ""
			textBuf = nil
			wizard = sessionWizard{}
			message = ""
			if remoteMode {
				pendingNewSession = created
				refreshSessions()
			} else {
				refreshSessions()
				for i, session := range visibleSessions() {
					if session.Name == created {
						selectSession(i)
						focusSession = true
					}
				}
			}
		}
		dirty.Store(true)
	}
	handleNewSessionKey := func(key string) {
		if wizard.stage == wizardProvider {
			switch key {
			case "esc":
				backNewSession()
			case "up":
				if len(wizard.providers) > 0 {
					wizard.selectedProvider = (wizard.selectedProvider + len(wizard.providers) - 1) % len(wizard.providers)
				}
			case "down":
				if len(wizard.providers) > 0 {
					wizard.selectedProvider = (wizard.selectedProvider + 1) % len(wizard.providers)
				}
			case "enter":
				advanceNewSession()
			}
			dirty.Store(true)
			return
		}
		switch key {
		case "esc":
			backNewSession()
		case "enter":
			advanceNewSession()
		case "tab":
			completeWorkdir()
		case "left":
			wizard.cursor = len(popUTF8(textBuf[:max(0, min(wizard.cursor, len(textBuf)))]))
		case "right":
			wizard.cursor = nextUTF8(textBuf, max(0, min(wizard.cursor, len(textBuf))))
		case "home":
			wizard.cursor = 0
		case "end":
			wizard.cursor = len(textBuf)
		case "backspace":
			textBuf, wizard.cursor = backspaceWizardText(textBuf, wizard.cursor)
			wizard.error, wizard.completionMatches, wizard.lastCompletion, wizard.showCompletions = "", nil, "", false
		case "delete":
			textBuf, wizard.cursor = deleteWizardText(textBuf, wizard.cursor)
			wizard.error, wizard.completionMatches, wizard.lastCompletion, wizard.showCompletions = "", nil, "", false
		case "delete-word":
			textBuf, wizard.cursor = deleteWizardWord(textBuf, wizard.cursor)
			wizard.error, wizard.completionMatches, wizard.lastCompletion, wizard.showCompletions = "", nil, "", false
		case "clear-line":
			textBuf = nil
			wizard.cursor = 0
			wizard.error, wizard.completionMatches, wizard.lastCompletion, wizard.showCompletions = "", nil, "", false
		default:
			if key != "" && key[0] >= 0x20 {
				textBuf, wizard.cursor = insertWizardText(textBuf, wizard.cursor, key)
				wizard.error, wizard.completionMatches, wizard.lastCompletion, wizard.showCompletions = "", nil, "", false
			}
		}
		dirty.Store(true)
	}
	handleRenameKey := func(key string) {
		switch key {
		case "esc":
			mode = ""
			textBuf = nil
			renameTarget = nil
			renameError = ""
		case "enter":
			if renameTarget == nil {
				mode = ""
				return
			}
			newName := strings.TrimSpace(string(textBuf))
			if err := validSessionName(newName); err != nil {
				renameError = err.Error()
				break
			}
			oldName := renameTarget.Name
			if newName != oldName {
				if err := renameSession(oldName, newName); err != nil {
					renameError = err.Error()
					break
				}
				if tp, ok := terminalPanes[oldName]; ok {
					tp.close()
					delete(terminalPanes, oldName)
				}
				for _, t := range ownedTerminalsNow(oldName) {
					if err := renameSession(t.Name, renamedTerminal(t.Name, newName)); err != nil {
						message = fmt.Sprintf("Rename terminal of %s: %v", oldName, err)
					}
				}
				pendingNewSession = newName
				filter = ""
				refreshSessions()
			}
			mode = ""
			textBuf = nil
			renameTarget = nil
			renameError = ""
		case "left":
			renameCursor = len(popUTF8(textBuf[:max(0, min(renameCursor, len(textBuf)))]))
		case "right":
			renameCursor = nextUTF8(textBuf, max(0, min(renameCursor, len(textBuf))))
		case "home":
			renameCursor = 0
		case "end":
			renameCursor = len(textBuf)
		case "backspace":
			textBuf, renameCursor = backspaceWizardText(textBuf, renameCursor)
			renameError = ""
		case "delete":
			textBuf, renameCursor = deleteWizardText(textBuf, renameCursor)
			renameError = ""
		case "delete-word":
			textBuf, renameCursor = deleteWizardWord(textBuf, renameCursor)
			renameError = ""
		case "clear-line":
			textBuf = nil
			renameCursor = 0
			renameError = ""
		default:
			if key != "" && key[0] >= 0x20 {
				textBuf, renameCursor = insertWizardText(textBuf, renameCursor, key)
				renameError = ""
			}
		}
		dirty.Store(true)
	}

	quit := false
	var handleKey func(string)
	handleKey = func(key string) {
		if mode == "help" {
			mode = ""
			if key == "q" {
				quit = true
			}
			dirty.Store(true)
			return
		}
		if mode == "new-session" {
			handleNewSessionKey(key)
			return
		}
		if mode == "rename" {
			handleRenameKey(key)
			return
		}
		if mode == "filter" {
			switch key {
			case "esc":
				mode, textBuf = "", nil
			case "enter":
				value := strings.TrimSpace(string(textBuf))
				oldMode := mode
				mode, textBuf = "", nil
				if oldMode == "filter" {
					filter = value
					sessionCursor = 0
				}
			case "backspace":
				if len(textBuf) > 0 {
					textBuf = textBuf[:len(textBuf)-1]
					for len(textBuf) > 0 && textBuf[len(textBuf)-1]&0xc0 == 0x80 {
						textBuf = textBuf[:len(textBuf)-1]
					}
				}
			default:
				if key != "" && key[0] >= 0x20 && len(textBuf)+len(key) <= maxFieldBytes {
					textBuf = append(textBuf, key...)
				}
			}
			dirty.Store(true)
			return
		}
		if mode == "settings" {
			switch key {
			case "up", "k":
				settingsRow = 0
			case "down", "j":
				settingsRow = 1
			case "left", "right", "h", "l", "enter", " ", "tab":
				step := 1
				if key == "left" || key == "h" {
					step = -1
				}
				if settingsRow == 0 {
					if settings.TerminalPlacement == terminalRight {
						settings.TerminalPlacement = terminalBelow
					} else {
						settings.TerminalPlacement = terminalRight
					}
				} else {
					settings.TerminalPercent = cyclePercent(settings.TerminalPercent, step)
				}
				saveSettings(settings)
				resizePanes()
			case "esc", "s":
				mode = ""
			case "q":
				quit = true
				mode = ""
			}
			dirty.Store(true)
			return
		}
		if mode != "" {
			if mode == "kill" || mode == "restore" || mode == "quit-all" {
				switch key {
				case "left", "right", "up", "down", "tab", "h", "l":
					dialogChoice = 1 - dialogChoice
					dirty.Store(true)
					return
				case "enter":
					if dialogChoice == 0 {
						key = "y"
					} else {
						key = "n"
					}
				case "y", "n", "q", "esc":
				default:
					return // other keys leave the dialog open
				}
			}
			if key == "q" {
				quit = true
				mode = ""
				dirty.Store(true)
				return
			}
			if key == "esc" || key == "n" {
				if mode == "restore" && key == "n" {
					if err := restorer.DiscardRestorable(restoreNames()); err != nil {
						message = fmt.Sprintf("Discard restorable sessions: %v", err)
					}
					restoreCandidates = nil
				}
				mode = ""
				killTarget = nil
				dirty.Store(true)
				return
			}
			if key == "y" {
				switch mode {
				case "restore":
					restored, failures, err := restorer.RestoreSessions(restoreNames())
					restoreCandidates = nil
					message = fmt.Sprintf("Restored %d session(s)", restored)
					if err != nil {
						message = fmt.Sprintf("Restore sessions: %v", err)
					} else if len(failures) > 0 {
						message += " · " + strings.Join(failures, "; ")
					}
					refreshSessions()
				case "quit-all":
					var failed []string
					for _, s := range sessions {
						if err := killSessionIdentity(s); err != nil {
							failed = append(failed, fmt.Sprintf("%s: %v", s.Name, err))
						}
					}
					// Their terminals are hidden from the list; stop those too.
					if listed, listErr := backend().List(); listErr == nil {
						for _, owner := range sessions {
							for _, t := range ownedTerminals(listed, owner.Name) {
								if err := killSession(t.Name); err != nil {
									failed = append(failed, fmt.Sprintf("%s: %v", t.Name, err))
								}
							}
						}
					}
					if len(failed) > 0 {
						message = "Stop " + strings.Join(failed, "; ")
						refreshSessions()
					} else {
						stopTerminalsOnExit = true
						quit = true
					}
				case "kill":
					if killTarget != nil {
						target := *killTarget
						if err := killSessionIdentity(target); err != nil {
							message = fmt.Sprintf("Stop %s: %v", target.Name, err)
						} else {
							message = ""
							if tp, ok := terminalPanes[target.Name]; ok {
								_, _ = disposeFallbackShell(tp)
								delete(terminalPanes, target.Name)
							}
							for _, t := range ownedTerminalsNow(target.Name) {
								_ = killSession(t.Name)
							}
							if p, ok := panes[target.Name]; ok && p.identity == sessionIdentity(target) {
								p.close()
								delete(panes, target.Name)
							}
							if !remoteMode {
								clearUnread(target.Name)
							}
						}
						refreshSessions()
					}
				}
			}
			mode = ""
			killTarget = nil
			dirty.Store(true)
			return
		}
		if key == "esc" {
			if filter != "" {
				filter = ""
				sessionCursor = 0
			} else {
				quit = true
			}
			dirty.Store(true)
			return
		}
		switch key {
		case "q":
			quit = true
		case "?":
			mode = "help"
		case "up":
			if sessionCursor == -1 {
				selectSession(len(visibleSessions()) - 1)
			} else {
				selectSession(nextSession(visibleSessions(), sessionCursor, -1, collapsedProjects))
			}
		case "down":
			if sessionCursor == -1 {
				selectSession(0)
			} else {
				selectSession(nextSession(visibleSessions(), sessionCursor, 1, collapsedProjects))
			}
		case "left", " ":
			if s, ok := selectedSession(); ok {
				toggleProject(s.CWD)
			}
		case "/":
			mode = "filter"
			textBuf = []byte(filter)
		case "n":
			beginNewSession()
		case "t":
			toggleIntegratedTerminal()
		case "r":
			if target, ok := selectedSession(); ok {
				if terminalVisible {
					terminalVisible = false
					closeIntegratedTerminal()
					terminalFocused = false
					resizePanes()
				}
				copy := target
				renameTarget = &copy
				textBuf = []byte(target.Name)
				renameCursor = len(textBuf)
				renameError = ""
				message = ""
				focusSession = false
				mode = "rename"
			}
		case "\x0c": // Ctrl+L
			frames.reset()
		case "s":
			mode = "settings"
			settingsRow = 0
		case "k":
			if target, ok := selectedSession(); ok {
				copy := target
				killTarget = &copy
				mode = "kill"
				dialogChoice = 0
			}
		case "enter", "right":
			if s, ok := selectedSession(); ok {
				rememberSelectedSession(s.Name)
				focusSession = true
				clearFocusedUnread()
			}
		default:
			if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
				idx := int(key[0] - '1')
				if selectSession(idx) {
					focusSession = true
					clearFocusedUnread()
				}
			}
		}
		dirty.Store(true)
	}

	// handleAction runs the keys that work wherever focus is.
	handleAction := func(action string) {
		// Typing in a form or confirming a dialog: the key is not ours to take.
		if mode != "" && mode != "restore" && mode != "quit-all" && mode != "help" {
			return
		}
		switch action {
		case "detach":
			quit = true
			mode = ""
		case "quit-all":
			if len(sessions) == 0 {
				quit = true
				return
			}
			mode = "quit-all"
			dialogChoice = 1 // Cancel, so a stray Enter stops nothing
		case "prev", "next":
			if mode != "" {
				return
			}
			key := "up"
			if action == "next" {
				key = "down"
			}
			handleKey(key)
		}
		dirty.Store(true)
	}

	handleSidebarData := func(data []byte) {
		if string(data) == pasteStart {
			return
		}
		data = bytesWithoutPasteMarkers(data)
		s := string(data)
		switch s {
		case "\x1b[A", "\x1bOA":
			handleKey("up")
			return
		case "\x1b[B", "\x1bOB":
			handleKey("down")
			return
		case "\x1b[C", "\x1bOC":
			handleKey("right")
			return
		case "\x1b[D", "\x1bOD":
			handleKey("left")
			return
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			handleKey("home")
			return
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			handleKey("end")
			return
		case "\x1b[3~":
			handleKey("delete")
			return
		case "\x1b":
			handleKey("esc")
			return
		}
		if (mode == "new-session" || mode == "rename") && len(data) > 0 && data[0] == 0x1b {
			return
		}
		if len(data) == 0 {
			return
		}
		if mode == "new-session" || mode == "rename" {
			for _, r := range string(data) {
				switch r {
				case '\r', '\n':
					handleKey("enter")
				case '\t':
					handleKey("tab")
				case '\x7f', '\b':
					handleKey("backspace")
				case '\x01':
					handleKey("home")
				case '\x05':
					handleKey("end")
				case '\x04':
					handleKey("delete")
				case '\x15':
					handleKey("clear-line")
				case '\x17':
					handleKey("delete-word")
				default:
					if r >= 0x20 {
						handleKey(string(r))
					}
				}
			}
			return
		}
		if mode == "filter" {
			for _, c := range data {
				switch c {
				case '\r', '\n':
					handleKey("enter")
				case '\x7f', '\b':
					handleKey("backspace")
				default:
					if c >= 0x20 {
						handleKey(string([]byte{c}))
					}
				}
			}
			return
		}
		for _, r := range string(data) {
			switch r {
			case '\r', '\n':
				handleKey("enter")
			case '\t':
				handleKey("tab")
			default:
				handleKey(string(r))
			}
		}
	}

	// selectionPoint maps the pointer to a viewport cell, and reports how many
	// rows it is above (negative) or below (positive) the pane.
	selectionPoint := func(event terminalMouseEvent, inTerminal bool) (terminalSelectionPoint, int) {
		w, h := paneSize()
		left, top := sideWidth+2, 2
		if inTerminal {
			w, h = terminalPaneSize()
			left, top = terminalOrigin()
		}
		overshoot := 0
		if event.y < top {
			overshoot = event.y - top
		} else if event.y > top+h-1 {
			overshoot = event.y - (top + h - 1)
		}
		return terminalSelectionPoint{
			X: max(0, min(w-1, event.x-left)),
			Y: max(0, min(h-1, event.y-top)),
		}, overshoot
	}
	copyTextSelection := func() {
		if !textSelection.active || textSelection.pane == nil {
			return
		}
		text := textSelection.pane.selectedText(textSelection.start, textSelection.end)
		if text == "" {
			return
		}
		// Use both the local clipboard client and OSC 52: the client handles
		// desktop clipboards, while the terminal sequence covers remote and
		// sandboxed environments where no local clipboard tool is available.
		oscErr := writeAll(os.Stdout, []byte(ansi.SetSystemClipboard(text)))
		// The clipboard tool runs in the background: the interface never waits on it.
		go func() {
			if !copyToSystemClipboard(text) && oscErr != nil {
				message = fmt.Sprintf("Copy selected text: %v", oscErr)
				dirty.Store(true)
			}
		}()
	}
	// beginSelection starts a text selection in the pane under the pointer.
	// Unless force is set (Shift+drag), panes whose program tracks the mouse
	// keep receiving clicks instead.
	beginSelection := func(event terminalMouseEvent, force bool) bool {
		region, _, _ := locate(event.x, event.y)
		if region == regionNone || region == regionDivider {
			return false
		}
		inTerminal := region == regionTerminal
		var p *embedded
		if inTerminal {
			p = currentIntegratedTerminal()
		} else {
			p = current()
		}
		if p == nil || p.done.Load() || (!force && p.mouseModes.Active()) {
			return false
		}
		point, _ := selectionPoint(event, inTerminal)
		point.Y = p.selectionRow(point.Y)
		textSelection = terminalTextSelection{pane: p, terminal: inTerminal, start: point, end: point, dragging: true, active: true}
		lastSelectionDrag = event
		wasFocused := focusSession
		focusSession = true
		if !wasFocused && !inTerminal {
			if selected, ok := selectedSession(); ok {
				rememberSelectedSession(selected.Name)
				clearFocusedUnread()
			}
		}
		if terminalFocused != inTerminal {
			shellInput.reset()
		}
		terminalFocused = inTerminal
		return true
	}
	// dragDivider resizes the terminal to follow the pointer over the divider.
	dragDivider := func(event terminalMouseEvent) {
		g := geom()
		if g.termH <= 0 {
			dividerDrag = false
			return
		}
		var percent int
		if g.right {
			percent = (cols - event.x - 1) * 100 / max(1, cols-sideWidth-2)
		} else {
			percent = (rows - event.y - 1) * 100 / max(1, rows-2)
		}
		percent = max(minTerminalPercent, min(maxTerminalPercent, percent))
		if percent != settings.TerminalPercent {
			settings.TerminalPercent = percent
			resizePanes()
		}
		if event.release {
			dividerDrag = false
			saveSettings(settings)
		}
		dirty.Store(true)
	}
	// dragSidebar widens or narrows the sidebar to follow the pointer.
	dragSidebar := func(event terminalMouseEvent) {
		if width := clampSidebarWidth(event.x-1, cols); width != sideWidth {
			settings.SidebarWidth = width
			updateSidebarWidth()
			resizePanes() // the changed rows are repainted in place, as for the terminal divider
		}
		if event.release {
			sidebarDrag = false
			if sideWidth != dragStartWidth {
				lastEdgePress = time.Time{} // a drag, not the first half of a double click
			}
			saveSettings(settings)
		}
		dirty.Store(true)
	}
	// dragSelection extends the selection to the pointer, scrolling the pane
	// while the pointer is held above or below it.
	dragSelection := func(event terminalMouseEvent) {
		lastSelectionDrag = event
		point, overshoot := selectionPoint(event, textSelection.terminal)
		if overshoot != 0 && time.Since(lastSelectionScroll) >= 60*time.Millisecond {
			lastSelectionScroll = time.Now()
			textSelection.pane.scrollbackBy(-max(-3, min(3, overshoot)))
		}
		point.Y = textSelection.pane.selectionRow(point.Y)
		textSelection.end = point
		dirty.Store(true)
	}
	handleMouse := func(event terminalMouseEvent) {
		if sidebarDrag {
			dragSidebar(event)
			return
		}
		if dividerDrag {
			dragDivider(event)
			return
		}
		if textSelection.dragging {
			dragSelection(event)
			if event.release {
				textSelection.dragging = false
				if textSelection.start == textSelection.end {
					textSelection = terminalTextSelection{} // a click, not a drag
				} else {
					copyTextSelection()
				}
			}
			dirty.Store(true)
			return
		}
		if mode != "" {
			return
		}
		if shiftLeftMousePress(event) {
			beginSelection(event, true)
			return
		}
		if plainLeftPress(event) && beginSelection(event, false) {
			dirty.Store(true)
			return
		}
		if textSelection.active && !event.release && event.button&(32|64) == 0 {
			textSelection = terminalTextSelection{}
			dirty.Store(true)
		}
		if isShiftMouseButtonEvent(event) {
			return
		}
		code := event.button
		mouse := uv.Mouse{X: event.x - (sideWidth + 2), Y: event.y - 2}
		if code&4 != 0 {
			mouse.Mod |= uv.ModShift
		}
		if code&8 != 0 {
			mouse.Mod |= uv.ModAlt
		}
		if code&16 != 0 {
			mouse.Mod |= uv.ModCtrl
		}
		if code&64 != 0 {
			switch code & 3 {
			case 0:
				mouse.Button = uv.MouseWheelUp
			case 1:
				mouse.Button = uv.MouseWheelDown
			case 2:
				mouse.Button = uv.MouseWheelLeft
			case 3:
				mouse.Button = uv.MouseWheelRight
			}
		} else {
			switch code & 3 {
			case 0:
				mouse.Button = uv.MouseLeft
			case 1:
				mouse.Button = uv.MouseMiddle
			case 2:
				mouse.Button = uv.MouseRight
			}
		}

		if event.release {
			if mouseCaptureShell || mouseCaptureSession != "" || mouseCaptureTerminal {
				p := panes[mouseCaptureSession]
				if mouseCaptureShell {
					p = emptyShell
				}
				if mouseCaptureTerminal {
					p = currentIntegratedTerminal()
				}
				if p != nil {
					w, h := paneSize()
					mouse.X, mouse.Y = event.x-(sideWidth+2), event.y-2
					if mouseCaptureTerminal {
						w, h = terminalPaneSize()
						tx, ty := terminalOrigin()
						mouse.X, mouse.Y = event.x-tx, event.y-ty
					}
					mouse.X = max(0, min(w-1, mouse.X))
					mouse.Y = max(0, min(h-1, mouse.Y))
					p.sendMouse(uv.MouseReleaseEvent(mouse))
				}
				mouseCaptureSession = ""
				mouseCaptureShell = false
				mouseCaptureTerminal = false
			}
			dirty.Store(true)
			return
		}
		if plainLeftPress(event) && !sidebarCollapsed && (event.x == sideWidth || event.x == sideWidth+1) {
			// A second press on the border soon after the first puts the width back.
			if time.Since(lastEdgePress) < doubleClickWindow {
				lastEdgePress = time.Time{}
				settings.SidebarWidth = 0
				updateSidebarWidth()
				resizePanes()
				saveSettings(settings)
				dirty.Store(true)
				return
			}
			lastEdgePress = time.Now()
			dragStartWidth = sideWidth
			sidebarDrag = true // the border between the sidebar and the pane
			return
		}
		if event.x <= sideWidth+1 {
			focusSession = false
			shellInput.reset()
			if code&64 != 0 {
				delta := 1
				if code&3 == 0 {
					delta = -1
				}
				selectSession(sessionCursor + delta)
			} else if event.x <= sideWidth && code&3 == 0 && event.y == rows-1 {
				mode = "help"
			} else if event.x <= sideWidth && code&3 == 0 && event.y == rows-2 {
				mode = "settings"
				settingsRow = 0
			} else if event.x <= sideWidth && code&3 == 0 && event.y >= 2 && event.y < rows-2 {
				listRows := max(0, rows-4)
				sidebarRows := buildSidebarRows(visibleSessions(), sidebarCollapsed, collapsedProjects, sessionCursor)
				start, end := sidebarViewport(sidebarRows, sessionCursor, listRows)
				line := event.y - 2
				for ri := start; ri < end; ri++ {
					if line < sidebarRows[ri].height {
						if sidebarRows[ri].header {
							toggleProject(sidebarRows[ri].cwd)
						} else {
							selectSession(sidebarRows[ri].session)
						}
						break
					}
					line -= sidebarRows[ri].height
				}
			}
		} else if event.x < cols && event.y < rows {
			selected, hasSelected := selectedSession()
			shellAvailable := len(sessions) == 0 && sessionSelectionInitialized && listErr == nil
			wasFocused := focusSession
			focusSession = true
			if !wasFocused && hasSelected {
				rememberSelectedSession(selected.Name)
				clearFocusedUnread()
			}
			if hasSelected || shellAvailable {
				region, regionX, regionY := locate(event.x, event.y)
				wasTerminalFocused := terminalFocused
				if region == regionDivider {
					dividerDrag = plainLeftPress(event)
					terminalFocused = true
					if !wasTerminalFocused {
						shellInput.reset()
					}
					dirty.Store(true)
					return
				}
				inTerminal := region == regionTerminal
				var p *embedded
				w, h := paneSize()
				if inTerminal {
					terminalFocused = true
					p = currentIntegratedTerminal()
					w, h = terminalPaneSize()
				} else {
					terminalFocused = false
					p = current()
				}
				if wasTerminalFocused != terminalFocused {
					shellInput.reset()
				}
				if p != nil && !p.done.Load() && region != regionNone {
					mouse.X = regionX
					mouse.Y = regionY
					if mouse.X >= 0 && mouse.X < w && mouse.Y >= 0 && mouse.Y < h {
						switch {
						case code&64 != 0:
							if mouse.Button == uv.MouseWheelUp || mouse.Button == uv.MouseWheelDown {
								if code&4 != 0 || !p.mouseModes.Active() {
									rows := 3
									if mouse.Button == uv.MouseWheelDown {
										rows = -rows
									}
									p.scrollbackBy(rows)
								} else {
									p.sendMouse(uv.MouseWheelEvent(mouse))
								}
							} else {
								p.sendMouse(uv.MouseWheelEvent(mouse))
							}
						case code&32 != 0:
							p.sendMouse(uv.MouseMotionEvent(mouse))
						case code&3 != 3:
							p.sendMouse(uv.MouseClickEvent(mouse))
							if inTerminal {
								mouseCaptureTerminal = true
							} else if hasSelected {
								mouseCaptureSession = selected.Name
							} else {
								mouseCaptureShell = true
							}
						}
					}
				}
			}
		}
		dirty.Store(true)
	}

	handleInputData := func(data []byte) {
		if !focusSession || mode == "restore" || mode == "quit-all" {
			handleSidebarData(data)
			return
		}
		p := current()
		if terminalVisible && terminalFocused {
			p = currentIntegratedTerminal()
		}
		if p == nil || p.done.Load() {
			shellInput.reset()
			return
		}
		if p.shell {
			if launch, ok := shellInput.Observe(data); ok {
				busy, attachedClients, statusErr := sessionRuntimeStatus(p.name)
				if statusErr == nil && !busy && attachedClients <= 1 {
					if launch.cancelLine {
						if err := p.writeInput([]byte{0x03}); err != nil {
							message = fmt.Sprintf("Cancel shell command: %v", err)
							dirty.Store(true)
							return
						}
					}
					cwd := p.workingDirectory()
					if p.shell {
						if liveCWD, err := sessionProcessWorkingDirectory(p.name); err == nil && liveCWD != "" {
							cwd = liveCWD
						}
					}
					if cwd == "" {
						cwd = launchDir
					}
					name := launcherSessionName(launch.provider, cwd, sessions)
					if err := newSession(name, cwd, launch.command); err != nil {
						message = fmt.Sprintf("Start %s: %v", launch.provider, err)
					} else {
						message = ""
						pendingNewSession = name
						refreshSessions()
					}
					dirty.Store(true)
					return
				}
			}
		}
		if !p.bracketedPaste.Load() {
			data = bytesWithoutPasteMarkers(data)
		}
		if p.snapToLive() {
			dirty.Store(true)
		}
		if err := p.writeInput(data); err != nil {
			message = fmt.Sprintf("Send input: %v", err)
		}
	}

	for !quit {
		select {
		case data, ok := <-input:
			if !ok {
				quit = true
				continue
			}
			message = "" // any input dismisses the status message
			for _, event := range decoder.Feed(data) {
				if event.mouse != nil {
					handleMouse(*event.mouse)
					continue
				}
				if event.focus {
					togglePaneFocus()
					continue
				}
				if event.sidebarToggle {
					toggleSidebar()
					continue
				}
				if event.terminalToggle {
					toggleIntegratedTerminal()
					continue
				}
				if event.action != "" {
					handleAction(event.action)
					continue
				}
				if len(event.data) == 0 {
					continue
				}
				handleInputData(event.data)
			}
			dirty.Store(true)
		case <-winch:
			cols, rows, _ = term.GetSize(fd)
			if cols < 1 {
				cols = 1
			}
			if rows < 1 {
				rows = 1
			}
			updateSidebarWidth()
			resizePanes()
			fmt.Print("\033[2J")
			frames.reset()
			dirty.Store(true)
		case <-dirty.wake:
			tryDraw()
		case <-drawTimer.C:
			drawTimerArmed = false
			tryDraw()
		case <-ticker.C:
			for _, event := range decoder.flushAfter(time.Now(), escapeDelay) {
				if event.mouse != nil {
					handleMouse(*event.mouse)
				} else if event.focus {
					togglePaneFocus()
				} else if event.sidebarToggle {
					toggleSidebar()
				} else if event.terminalToggle {
					toggleIntegratedTerminal()
				} else if event.action != "" {
					handleAction(event.action)
				} else if len(event.data) > 0 {
					handleInputData(event.data)
				}
			}
			if textSelection.dragging && textSelection.pane != nil {
				if _, overshoot := selectionPoint(lastSelectionDrag, textSelection.terminal); overshoot != 0 {
					dragSelection(lastSelectionDrag)
				}
			}
			if message != messageShown {
				messageShown, messageSince = message, time.Now()
			}
			if message != "" && time.Since(messageSince) > statusMessageTimeout {
				message, messageShown = "", ""
				dirty.Store(true)
			}
			if remoteMode {
				st := currentLinkStatus()
				if st.Up {
					awake.Touch() // an open link keeps this machine up even when the agents are idle
				}
				if st.Up != linkWasUp || st.Gen != linkGen {
					if st.Up {
						message = "Reconnected"
						lastList = time.Time{} // fetch the list again at once
					}
					linkWasUp, linkGen = st.Up, st.Gen
					dirty.Store(true)
				} else if !st.Up && time.Since(lastLinkDraw) >= time.Second {
					lastLinkDraw = time.Now() // the countdown changes every second
					dirty.Store(true)
				}
			}
			if time.Since(lastList) > time.Second && (!remoteMode || linkWasUp) {
				refreshSessionsInBackground()
				lastList = time.Now()
			}
			if time.Since(lastFullRepaint) > fullRepaintEvery {
				dirty.Store(true) // draw repaints the whole screen
			}
			tryDraw()
		case result := <-remoteListUpdates:
			remoteListInFlight = false
			applySessionList(result.sessions, result.err)
		}
	}
	return nil
}

func bytesWithoutPasteMarkers(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte(pasteStart))
	data = bytes.TrimSuffix(data, []byte(pasteEnd))
	return data
}
