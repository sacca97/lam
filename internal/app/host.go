package app

// Session host. `lam host <name> <cwd> <command>` runs one session: it starts
// the command on a pty, keeps a terminal emulator in sync with the pty output
// (so a reattaching client can be given the current screen), and serves any
// number of clients on a unix socket. It is started detached (setsid), so it
// keeps running after LAM exits. It exits when the command exits or is killed.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	gh "go.mitchellh.com/libghostty"
)

const (
	hostDefaultCols = 80
	hostDefaultRows = 24
	// Scrollback kept per session, bounded by bytes and not by a line count:
	// libghostty prunes by lines about five times slower, which made a session
	// that prints a lot cost a core on the host. 4 MiB keeps well over 1000 rows
	// at every terminal width from 60 to 300 columns (see the test) and about
	// 2000 at 200; the library's own default (about 1 MB) would keep only ~220
	// rows of a 200-column terminal.
	hostScrollbackBytes = 4 << 20
	hostOutQueue        = 512 // frames queued per client before it is dropped
	// A client that has this many bytes waiting has fallen behind (a slow link,
	// a flood of output). It is not made to replay the backlog: what it has not
	// received is dropped and it gets the current screen, so what it shows is
	// never far out of date and its own keystrokes are not stuck behind output
	// it will never need to see.
	hostLagBytes = 64 << 10
	// The socket buffer toward a client is kept small: bytes sitting in it
	// cannot be skipped, so a large one hides the backlog the limit above
	// is meant to bound.
	hostSendBuffer = 32 << 10
	// A client that receives more than hostFloodBytes within hostFloodInterval
	// (over 10 MB/s: a program printing as fast as it can, far beyond what any
	// build log does) is not sent that output. A screen can only show the latest
	// of it, and parsing every byte would cost the interface a whole core, so it
	// gets a fresh snapshot at most hostFloodRefresh apart until the flood ends.
	hostFloodBytes    = 1 << 20
	hostFloodInterval = 100 * time.Millisecond
	hostFloodRefresh  = time.Second / 60
)

// hostFrame is a frame queued for a client's writer goroutine.
type hostFrame struct {
	typ     byte
	payload []byte
}

// hostClient is one attached lam attach process.
type hostClient struct {
	conn    net.Conn
	out     chan hostFrame
	pending atomic.Int64 // bytes queued in out and not yet taken by the writer
	done    chan struct{}
	once    sync.Once

	// Guarded by the session's mu.
	rateStart time.Time // start of the current flood-measuring interval
	rateBytes int       // output offered to the client in it
	stale     bool      // output was withheld; the client needs a snapshot
}

func frameSize(f hostFrame) int64 { return int64(5 + len(f.payload)) }

// send queues a frame without blocking. It reports false if the queue is full.
func (c *hostClient) send(f hostFrame) bool {
	select {
	case c.out <- f:
		c.pending.Add(frameSize(f))
		return true
	default:
		return false
	}
}

// drain drops everything queued and not yet being written.
func (c *hostClient) drain() {
	for {
		select {
		case f := <-c.out:
			c.pending.Add(-frameSize(f))
		default:
			return
		}
	}
}

func (c *hostClient) close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

// writeLoop is the only writer on the client connection, so a slow client
// stalls only its own queue.
func (c *hostClient) writeLoop() {
	for {
		select {
		case f := <-c.out:
			c.pending.Add(-frameSize(f))
			if err := writeFrame(c.conn, f.typ, f.payload); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

type hostSession struct {
	awake    *keepAwake
	meter    outputMeter
	snapMu   sync.Mutex // guards snap and snapAt
	snap     sessionActivitySnapshot
	snapAt   time.Time
	cpu      cpuSampler
	name     string
	meta     sessionMeta
	renameMu sync.Mutex
	closing  bool
	ptmx     *os.File
	inMu     sync.Mutex // guards inBuf and inWake
	inBuf    []byte
	inWake   bool
	cmd      *exec.Cmd
	ln       net.Listener

	// mu guards the emulator, the pty size and the client set. Pty output is
	// written to the emulator and broadcast under it, so a client's snapshot
	// is always followed by exactly the output produced after it.
	mu           sync.Mutex
	term         *gh.Terminal // libghostty; not thread-safe, only touched under mu
	cols         int
	rows         int
	clients      map[*hostClient]struct{}
	lastOutputAt atomic.Int64

	floodTimer *time.Timer // pending snapshot for clients withheld output; guarded by mu

	exited   chan struct{} // closed when the session command has exited
	ptyDone  chan struct{} // closed when the pty output reaches EOF
	killOnce sync.Once
	signaled atomic.Bool  // the host was told to stop by a signal, not by a kill request or the command exiting
	reason   atomic.Value // string: why the session is ending, for the event log
}

// setReason records why the session is ending; the first reason wins.
func (h *hostSession) setReason(reason string) {
	h.reason.CompareAndSwap(nil, reason)
}

// hostEnv is the environment for the session command. TMUX is dropped because
// it would point at a server that is not the session's.
func hostEnv() []string {
	env := envWithout("TMUX", "TMUX_PANE", "TERM", "COLORTERM")
	return append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
}

// Default colors reported to programs that ask what the terminal looks like
// (OSC 10 and 11). Programs use them to pick a light or dark theme; the host
// cannot see the real terminal, so it reports a dark one, which is what the
// interface's own emulator did before.
var (
	hostDefaultForeground = gh.ColorRGB{R: 0xe5, G: 0xe5, B: 0xe5}
	hostDefaultBackground = gh.ColorRGB{R: 0x00, G: 0x00, B: 0x00}
)

// newHostTerminal creates the emulator that holds a session's screen.
// writePty receives the terminal's replies to the program's queries.
func newHostTerminal(cols, rows int, writePty func([]byte)) (*gh.Terminal, error) {
	term, err := gh.NewTerminal(gh.WithSize(uint16(cols), uint16(rows)),
		gh.WithMaxScrollbackBytes(hostScrollbackBytes),
		// Measure emoji sequences as one wide glyph, like the terminals that
		// display them, so the screen's cell widths match the layout.
		gh.WithModeDefault(gh.ModeGraphemeCluster, true),
		gh.WithWritePty(func(_ *gh.Terminal, data []byte) { writePty(data) }))
	if err != nil {
		return nil, err
	}
	fg, bg := hostDefaultForeground, hostDefaultBackground
	_ = term.SetColorForeground(&fg)
	_ = term.SetColorBackground(&bg)
	return term, nil
}

// runHost is the body of `lam host`. It blocks until the session ends.
func runHost(name, cwd, command string) error {
	if err := validSessionName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(socketDir(), 0700); err != nil {
		return err
	}
	if conn, err := connectSession(name, 300*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("session %q already exists", name)
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = cwd
	cmd.Env = hostEnv()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: hostDefaultRows, Cols: hostDefaultCols})
	if err != nil {
		return err
	}

	sock := sessionSocket(name)
	os.Remove(sock) // stale socket from a host that died without cleaning up
	ln, err := net.Listen("unix", sock)
	if err != nil {
		cmd.Process.Kill()
		ptmx.Close()
		return err
	}

	h := &hostSession{
		awake:   newKeepAwake(),
		name:    name,
		ptmx:    ptmx,
		cmd:     cmd,
		ln:      ln,
		cols:    hostDefaultCols,
		rows:    hostDefaultRows,
		clients: make(map[*hostClient]struct{}),
		exited:  make(chan struct{}),
		ptyDone: make(chan struct{}),
	}
	h.awake.still = func() bool { return h.activitySnapshot().State == "active" }
	// The terminal answers the program's queries (cursor position, device
	// attributes, colors, ...) through this callback; it runs inside VTWrite,
	// under mu. Every attached client sees the same stream but must not answer
	// it again, so this is the one place queries are answered.
	term, err := newHostTerminal(hostDefaultCols, hostDefaultRows, func(data []byte) { h.ptmx.Write(data) })
	if err != nil {
		cmd.Process.Kill()
		ptmx.Close()
		ln.Close()
		os.Remove(sock)
		return err
	}
	h.term = term
	defer term.Close()

	// Metadata goes last: List only trusts a session once it is listening.
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	meta := sessionMeta{
		Name:    name,
		PID:     cmd.Process.Pid,
		HostPID: os.Getpid(),
		CWD:     cwd,
		Command: command,
		Created: time.Now(),
	}
	if err := writeSessionMeta(meta); err != nil {
		ln.Close()
		cmd.Process.Kill()
		ptmx.Close()
		os.Remove(sock)
		return err
	}
	h.meta = meta
	h.cpu.pid = meta.PID
	manifestRecord(manifestEntry{Name: name, CWD: cwd, Command: command, Created: meta.Created})

	// A plain SIGTERM or SIGINT to the host takes the same path as a Kill
	// request. A hangup does not: the host has no terminal, so a SIGHUP means a
	// login session or terminal somewhere went away, which must not end a
	// session that is meant to outlive them.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGHUP {
				logEvent("host %s ignored SIGHUP", h.currentName())
				continue
			}
			h.setReason("signal " + sig.String())
			h.signaled.Store(true)
			h.kill()
			return
		}
	}()

	go h.readPty()
	go func() {
		waitErr := cmd.Wait()
		h.setReason(fmt.Sprintf("command exited (%v)", describeExit(cmd, waitErr)))
		close(h.exited)
	}()
	go h.acceptLoop()

	<-h.exited
	select { // let the output reader drain what the command wrote before exiting
	case <-h.ptyDone:
	case <-time.After(time.Second):
	}
	h.renameMu.Lock()
	h.closing = true
	_ = ln.Close()
	removeSessionFiles(h.name)
	reason, _ := h.reason.Load().(string)
	logEvent("host %s ended: %s", h.name, reason)
	if !h.signaled.Load() {
		manifestForget(h.name)
	}
	h.renameMu.Unlock()
	h.mu.Lock()
	for cl := range h.clients {
		cl.close()
	}
	h.mu.Unlock()
	ptmx.Close()
	return nil
}

func writeSessionMeta(meta sessionMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	path := sessionMetaFile(meta.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readPty feeds pty output to the emulator and to every attached client.
func (h *hostSession) readPty() {
	defer close(h.ptyDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := h.ptmx.Read(buf)
		if n > 0 {
			h.output(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (h *hostSession) output(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastOutputAt.Store(time.Now().UnixNano())
	if h.meter.add(len(data), time.Now()) >= activityOutputBytes {
		h.awake.Touch()
	}
	h.term.VTWrite(data)
	if len(h.clients) == 0 {
		return
	}
	h.broadcastLocked(data)
}

// armSnapshotLocked makes sure clients that were sent no output because of a
// flood get a snapshot soon.
func (h *hostSession) armSnapshotLocked() {
	if h.floodTimer == nil {
		h.floodTimer = time.AfterFunc(hostFloodRefresh, h.flushStale)
	}
}

// flushStale brings every client that was withheld output up to date.
func (h *hostSession) flushStale() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.floodTimer = nil
	var snapshot *hostFrame
	for cl := range h.clients {
		if !cl.stale {
			continue
		}
		if snapshot == nil {
			snapshot = &hostFrame{typ: frameSnapshot, payload: []byte(h.snapshotLocked())}
		}
		cl.drain()
		cl.stale = false
		if !cl.send(*snapshot) {
			cl.close()
			delete(h.clients, cl)
		}
	}
}

// broadcastLocked sends output to every attached client. A client that has
// fallen too far behind gets the current screen instead of the output: the
// terminal has already processed this output, so the snapshot includes it.
//
// The PTY reader reuses data's buffer, so it is copied only if some client is
// actually sent it: a detached session, or a client being withheld a flood,
// allocates nothing per chunk beyond the terminal emulator's own work.
func (h *hostSession) broadcastLocked(data []byte) {
	var snapshot, output *hostFrame
	frame := func() hostFrame {
		if output == nil {
			output = &hostFrame{typ: frameOutput, payload: append([]byte(nil), data...)}
		}
		return *output
	}
	now := time.Now()
	for cl := range h.clients {
		if cl.pending.Load() > hostLagBytes {
			if snapshot == nil {
				snapshot = &hostFrame{typ: frameSnapshot, payload: []byte(h.snapshotLocked())}
			}
			cl.drain()
			cl.stale = false
			if !cl.send(*snapshot) {
				cl.close()
				delete(h.clients, cl)
			}
			continue
		}
		// A flood is not forwarded; once one starts, nothing is until the
		// snapshot goes out, because output sent after the withheld part would
		// land on a screen that never saw it.
		if now.Sub(cl.rateStart) > hostFloodInterval {
			cl.rateStart, cl.rateBytes = now, 0
		}
		cl.rateBytes += len(data)
		if cl.stale || cl.rateBytes > hostFloodBytes {
			cl.stale = true
			h.armSnapshotLocked()
			continue
		}
		if !cl.send(frame()) {
			// Many small frames (the echo of a large paste) can fill the queue
			// while few bytes are pending. Treat it as lag, not as a dead client:
			// the snapshot that follows includes this output.
			cl.stale = true
			h.armSnapshotLocked()
		}
	}
}

func (h *hostSession) acceptLoop() {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return
		}
		go h.handleConn(conn)
	}
}

// handleConn reads the first frame to learn what the peer wants. A Resize
// frame means an attach; the other frames are one-shot requests.
func (h *hostSession) handleConn(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	typ, payload, err := readFrame(conn)
	if err != nil {
		conn.Close()
		return
	}
	switch typ {
	case frameResize:
		h.attach(conn, payload)
	case frameInput:
		h.writeInput(payload)
	case frameKill:
		h.setReason("kill request")
		h.kill()
	case frameHistory:
		writeFrame(conn, frameHistoryResp, []byte(h.history()))
	case framePing:
		writeFrame(conn, framePing, nil)
	case frameActivity:
		h.mu.Lock()
		attachedClients := len(h.clients)
		h.mu.Unlock()
		snapshot := h.activitySnapshot()
		if snapshot.State == "active" {
			h.awake.Touch() // work that prints nothing still keeps the machine up
		}
		snapshot.AttachedClients = attachedClients
		activity, _ := json.Marshal(snapshot)
		writeFrame(conn, frameActivityResp, activity)
	case frameRename:
		if err := h.rename(string(payload)); err != nil {
			writeFrame(conn, frameRenameResp, []byte(err.Error()))
		} else {
			writeFrame(conn, frameRenameResp, nil)
		}
	}
	conn.Close()
}

func (h *hostSession) currentName() string {
	h.renameMu.Lock()
	defer h.renameMu.Unlock()
	return h.name
}

func (h *hostSession) rename(newName string) error {
	if err := validSessionName(newName); err != nil {
		return err
	}
	h.renameMu.Lock()
	defer h.renameMu.Unlock()
	if h.closing {
		return fmt.Errorf("session is shutting down")
	}
	oldName := h.name
	if newName == oldName {
		return nil
	}
	for _, path := range []string{sessionSocket(newName), sessionMetaFile(newName)} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("session %q already exists", newName)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check session name %q: %w", newName, err)
		}
	}
	oldMeta := h.meta
	newMeta := oldMeta
	newMeta.Name = newName
	if err := os.Rename(sessionSocket(oldName), sessionSocket(newName)); err != nil {
		return fmt.Errorf("rename session socket: %w", err)
	}
	h.name = newName
	h.meta = newMeta
	if err := writeSessionMeta(newMeta); err != nil {
		h.name = oldName
		h.meta = oldMeta
		rollbackErr := os.Rename(sessionSocket(newName), sessionSocket(oldName))
		metaErr := writeSessionMeta(oldMeta)
		if rollbackErr != nil {
			return fmt.Errorf("write renamed session metadata: %v; restore old socket: %w", err, rollbackErr)
		}
		if metaErr != nil {
			return fmt.Errorf("write renamed session metadata: %v; restore old metadata: %w", err, metaErr)
		}
		return fmt.Errorf("write renamed session metadata: %w", err)
	}
	_ = os.Remove(sessionMetaFile(oldName))
	manifestRename(oldName, newName)
	moveSessionMarkers(oldName, newName)
	return nil
}

func moveSessionMarkers(oldName, newName string) {
	for _, prefix := range []string{"claude-unread", "claude-status"} {
		oldPath := fmt.Sprintf("/tmp/%s-%s", prefix, oldName)
		newPath := fmt.Sprintf("/tmp/%s-%s", prefix, newName)
		if _, err := os.Lstat(oldPath); err != nil {
			continue
		}
		if _, err := os.Lstat(newPath); err == nil {
			continue
		}
		_ = os.Rename(oldPath, newPath)
	}
}

// attach registers a client and streams the screen to it. It returns when the
// client disconnects or the session ends.
func (h *hostSession) attach(conn net.Conn, resize []byte) {
	conn.SetDeadline(time.Time{})
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.SetWriteBuffer(hostSendBuffer)
	}
	cl := &hostClient{conn: conn, out: make(chan hostFrame, hostOutQueue), done: make(chan struct{})}

	h.mu.Lock()
	if cols, rows, ok := parseResize(resize); ok {
		h.resizeLocked(cols, rows)
	}
	cl.send(hostFrame{typ: frameSnapshot, payload: []byte(h.snapshotLocked())})
	h.clients[cl] = struct{}{}
	h.mu.Unlock()

	go cl.writeLoop()
	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			break
		}
		switch typ {
		case frameInput:
			h.writeInput(payload)
		case frameResize:
			if cols, rows, ok := parseResize(payload); ok {
				h.mu.Lock()
				h.resizeLocked(cols, rows)
				h.mu.Unlock()
			}
		case frameKill:
			h.setReason("kill request from an attached client")
			h.kill()
		}
	}
	h.mu.Lock()
	delete(h.clients, cl)
	h.mu.Unlock()
	cl.close()
}

// resizeLocked resizes the pty and the emulator. h.mu must be held.
func (h *hostSession) resizeLocked(cols, rows int) {
	h.cols, h.rows = cols, rows
	h.term.Resize(uint16(cols), uint16(rows), 8, 16)
	pty.Setsize(h.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// kill terminates the session's process group: SIGTERM, then SIGKILL after 2s.
// Once the command exits, runHost cleans up and returns.
func (h *hostSession) kill() {
	h.killOnce.Do(func() {
		pgid := h.cmd.Process.Pid
		syscall.Kill(-pgid, syscall.SIGTERM)
		go func() {
			select {
			case <-h.exited:
			case <-time.After(2 * time.Second):
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}()
	})
}

// snapshotLocked renders the visible screen as VT output that redraws it on
// an attaching terminal, including modes (alt screen, mouse, bracketed paste),
// styles, tab stops and the cursor. h.mu must be held.
func (h *hostSession) snapshotLocked() string {
	var b strings.Builder
	// Reset what a previous program may have left on the client's terminal.
	b.WriteString("\x1b[0m\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[H\x1b[2J")
	start, err1 := h.term.GridRef(gh.Point{Tag: gh.PointTagActive, X: 0, Y: 0})
	end, err2 := h.term.GridRef(gh.Point{Tag: gh.PointTagActive, X: uint16(h.cols - 1), Y: uint32(h.rows - 1)})
	if err1 != nil || err2 != nil {
		return b.String()
	}
	f, err := gh.NewFormatter(h.term,
		gh.WithFormatterFormat(gh.FormatterFormatVT),
		gh.WithFormatterSelection(&gh.Selection{Start: *start, End: *end}),
		gh.WithFormatterTrim(false),
		gh.WithFormatterExtraModes(true),
		gh.WithFormatterExtraCursor(true),
		gh.WithFormatterExtraStyle(true),
		gh.WithFormatterExtraScrollingRegion(true),
		gh.WithFormatterExtraTabstops(true),
		gh.WithFormatterExtraCharsets(true))
	if err != nil {
		return b.String()
	}
	defer f.Close()
	if s, err := f.FormatString(); err == nil {
		b.WriteString(s)
	}
	if visible, err := h.term.CursorVisible(); err == nil && !visible {
		b.WriteString("\x1b[?25l")
	} else {
		b.WriteString("\x1b[?25h")
	}
	return b.String()
}

// history returns the scrollback followed by the screen, as plain text.
func (h *hostSession) history() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := gh.NewFormatter(h.term, gh.WithFormatterFormat(gh.FormatterFormatPlain))
	if err != nil {
		return ""
	}
	defer f.Close()
	s, err := f.FormatString()
	if err != nil {
		return ""
	}
	return strings.TrimRight(s, "\n ") + "\n"
}

// describeExit says how the session's command ended.
func describeExit(cmd *exec.Cmd, waitErr error) string {
	if cmd.ProcessState == nil {
		return fmt.Sprint(waitErr)
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return "killed by signal " + status.Signal().String()
	}
	return fmt.Sprintf("exit status %d", cmd.ProcessState.ExitCode())
}

// activitySnapshotTTL is how long a judged state is reused, so several clients
// polling the same session share the /proc reads.
const activitySnapshotTTL = time.Second

// activitySnapshot reports when the session last printed and, from the signals
// only the host can see, whether it is doing something right now.
func (h *hostSession) activitySnapshot() sessionActivitySnapshot {
	h.snapMu.Lock()
	defer h.snapMu.Unlock()
	if now := time.Now(); now.Sub(h.snapAt) >= activitySnapshotTTL {
		h.snap, h.snapAt = h.computeActivity(now), now
	}
	return h.snap
}

func (h *hostSession) computeActivity(now time.Time) sessionActivitySnapshot {
	var snapshot sessionActivitySnapshot
	if timestamp := h.lastOutputAt.Load(); timestamp != 0 {
		snapshot.LastOutputAt = time.Unix(0, timestamp)
	}
	h.renameMu.Lock()
	name, meta := h.name, h.meta
	h.renameMu.Unlock()
	idle := false
	if isFallbackShellCommand(meta.Command) {
		busy, err := processHasLiveChildren(meta.PID)
		idle = err == nil && !busy
	}
	snapshot.State = classifyActivity(activitySignals{
		Now:          now,
		LastOutputAt: snapshot.LastOutputAt,
		RecentBytes:  h.meter.recent(now),
		IdlePrompt:   idle,
		CPUBusy:      h.cpu.Busy(now),
		AgentStatus:  agentStatus(name, meta.Command, meta.Created),
	})
	return snapshot
}

// maxPendingInput bounds the input waiting for a program that is not reading.
const maxPendingInput = 64 << 20

// writeInput hands input to the pty without waiting for the program to read it,
// so a connection loop that also carries resize and detach is never held up.
func (h *hostSession) writeInput(data []byte) {
	h.inMu.Lock()
	if len(h.inBuf)+len(data) > maxPendingInput {
		h.inMu.Unlock()
		return
	}
	h.inBuf = append(h.inBuf, data...)
	start := !h.inWake
	h.inWake = true
	h.inMu.Unlock()
	if start {
		go h.drainInput()
	}
}

func (h *hostSession) drainInput() {
	for {
		h.inMu.Lock()
		data := h.inBuf
		h.inBuf = nil
		if len(data) == 0 {
			h.inWake = false
			h.inMu.Unlock()
			return
		}
		h.inMu.Unlock()
		if writePTY(h.ptmx, data) != nil {
			h.inMu.Lock()
			h.inBuf, h.inWake = nil, false
			h.inMu.Unlock()
			return
		}
	}
}
