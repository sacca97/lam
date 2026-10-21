package app

// Native session backend. Every session is owned by a small host process
// (host.go) that runs the command on a pty and listens on a unix socket. LAM
// talks to the host with the length-prefixed frame protocol below, so sessions
// keep running after the UI exits and can be reattached later (`lam attach`).

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Frame types. A frame is: 1 byte type, 4 byte big-endian payload length, payload.
const (
	frameInput        byte = 1  // client -> host: raw bytes written to the pty
	frameOutput       byte = 2  // host -> client: raw pty output, streamed to attached clients
	frameResize       byte = 3  // client -> host: uint16 cols, uint16 rows (big endian)
	frameSnapshot     byte = 4  // host -> client: redraw of the current screen, first frame on attach
	frameKill         byte = 5  // client -> host: terminate the session
	frameHistory      byte = 6  // client -> host: request scrollback as plain text
	frameHistoryResp  byte = 7  // host -> client: plain-text scrollback followed by the screen
	framePing         byte = 8  // liveness check, answered with a ping frame
	frameActivity     byte = 9  // client -> host: request recent output time
	frameActivityResp byte = 10 // host -> client: JSON sessionActivity
	frameRename       byte = 11 // client -> host: replace the session name
	frameRenameResp   byte = 12 // host -> client: empty on success, error text on failure
)

// maxFramePayload bounds what a peer may make us allocate.
const maxFramePayload = 16 << 20

// detachKeys are the bytes that detach `lam attach` (Ctrl+Space, Ctrl+\).
const detachKeys = "\x00\x1c"

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return writeAll(w, buf)
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFramePayload {
		return 0, nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func resizePayload(cols, rows int) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint16(p[0:2], uint16(cols))
	binary.BigEndian.PutUint16(p[2:4], uint16(rows))
	return p
}

// parseResize decodes a Resize payload; zero sizes are rejected.
func parseResize(p []byte) (cols, rows int, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	cols = int(binary.BigEndian.Uint16(p[0:2]))
	rows = int(binary.BigEndian.Uint16(p[2:4]))
	return cols, rows, cols > 0 && rows > 0
}

type sessionActivitySnapshot struct {
	LastOutputAt    time.Time `json:"lastOutputAt,omitempty"`
	AttachedClients int       `json:"attachedClients,omitempty"`
	State           string    `json:"state,omitempty"` // "active" or "quiet", judged by the host
}

const activeOutputWindow = 20 * time.Second

// socketDir returns the per-user runtime directory for session sockets.
func socketDir() string {
	if d := os.Getenv("LAM_SOCKET_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "lam")
	}
	return fmt.Sprintf("/tmp/lam-%d", os.Getuid())
}

func sessionSocket(name string) string   { return filepath.Join(socketDir(), name+".sock") }
func sessionMetaFile(name string) string { return filepath.Join(socketDir(), name+".json") }

// sessionMeta is written by the host once it is listening.
type sessionMeta struct {
	Name    string    `json:"name"`
	PID     int       `json:"pid"`     // session process (sh -c ...), also its process group
	HostPID int       `json:"hostPid"` // the host daemon
	CWD     string    `json:"cwd"`
	Command string    `json:"command"`
	Created time.Time `json:"created"`
}

// validSessionName rejects names that cannot be used as a socket file name.
func validSessionName(name string) error {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\x00\n\t") {
		return fmt.Errorf("invalid session name %q", name)
	}
	return nil
}

// selfBinary is the lam executable used to start hosts and attach clients.
// LAM_BIN overrides it for development and packaging.
func selfBinary() (string, error) {
	if p := os.Getenv("LAM_BIN"); p != "" {
		return p, nil
	}
	return os.Executable()
}

// connectSession dials a session's host socket.
func connectSession(name string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", sessionSocket(name), timeout)
}

// sockDead reports whether a dial error means nobody is listening on the socket.
func sockDead(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

func removeSessionFiles(name string) {
	os.Remove(sessionSocket(name))
	os.Remove(sessionMetaFile(name))
}

// nativeBackend runs sessions under per-session host processes.
type nativeBackend struct{}

func (nativeBackend) Name() string { return "native" }

// Create starts a host process for the session and waits until it accepts
// connections. The host is detached (setsid), so it outlives lam.
func (nativeBackend) Create(name, cwd, command string) error {
	if err := validSessionName(name); err != nil {
		return err
	}
	if conn, err := connectSession(name, 300*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("session %q already exists", name)
	}
	if cwd != "" {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			return fmt.Errorf("directory %q does not exist", cwd)
		}
	}
	self, err := selfBinary()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "host", name, cwd, command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap the host when it exits; it does not depend on us
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := connectSession(name, 200*time.Millisecond); err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("session %q did not start", name)
}

// AttachCmd runs `lam attach <name>`, which needs a terminal on stdio.
func (nativeBackend) AttachCmd(name string) *exec.Cmd {
	self, err := selfBinary()
	if err != nil {
		self = "lam"
	}
	return exec.Command(self, "attach", name)
}

// List returns the live sessions. Metadata whose socket refuses connections
// belongs to a dead host and is removed.
func (nativeBackend) List() ([]sessionInfo, error) {
	entries, err := os.ReadDir(socketDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Hosts are asked at the same time: one that is busy or stuck costs its own
	// timeout, not the sum of everyone's.
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			names = append(names, name)
		}
	}
	probed := make([]*sessionInfo, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probed[i] = probeSession(name)
		}()
	}
	wg.Wait()
	var sessions []sessionInfo
	for _, info := range probed {
		if info != nil {
			sessions = append(sessions, *info)
		}
	}
	return sessions, nil
}

// probeSession reads one session's metadata and asks its host how it is doing.
// A nil result means there is no live session of that name.
func probeSession(name string) *sessionInfo {
	data, err := os.ReadFile(sessionMetaFile(name))
	if err != nil {
		return nil
	}
	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	conn, err := connectSession(name, 300*time.Millisecond)
	if err != nil {
		if sockDead(err) {
			removeSessionFiles(name)
		}
		return nil
	}
	activity := sessionActivitySnapshot{}
	_ = conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	if err := writeFrame(conn, frameActivity, nil); err == nil {
		if typ, payload, err := readFrame(conn); err == nil && typ == frameActivityResp {
			_ = json.Unmarshal(payload, &activity)
		}
	}
	conn.Close()
	command := commandName(meta.Command)
	activityState := activity.State
	if activityState == "" && !activity.LastOutputAt.IsZero() {
		// A host started before it judged activity itself.
		activityState = classifyActivity(activitySignals{
			Now:          time.Now(),
			LastOutputAt: activity.LastOutputAt,
			RecentBytes:  activityOutputBytes,
			AgentStatus:  agentStatus(name, meta.Command, meta.Created),
		})
	}
	return &sessionInfo{
		Name:     name,
		Identity: fmt.Sprintf("%s\x00%d\x00%d", name, meta.HostPID, meta.Created.UnixNano()),
		PanePID:  strconv.Itoa(meta.PID),
		Command:  command,
		CWD:      meta.CWD,
		Unread:   hasUnread(name),
		Activity: activityState,
		Shell:    isFallbackShellCommand(meta.Command),
		Created:  meta.Created,
		Clients:  activity.AttachedClients,
	}
}

// SessionWorkingDirectory reports the live working directory of a session
// process, falling back to the directory it started in when /proc is not
// available on the host.
func (nativeBackend) SessionWorkingDirectory(name string) (string, error) {
	meta, err := readSessionMeta(name)
	if err != nil {
		return "", err
	}
	if cwd := processWorkingDirectory(meta.PID); cwd != "" {
		return cwd, nil
	}
	return meta.CWD, nil
}

// SessionStatus reports whether the shell process has live work and how many
// clients are attached. The latter prevents one LAM window from removing an
// idle shell that another terminal is using.
func (nativeBackend) SessionStatus(name string) (bool, int, error) {
	meta, err := readSessionMeta(name)
	if err != nil {
		return false, 0, err
	}
	busy, err := processHasLiveChildren(meta.PID)
	if err != nil {
		return false, 0, err
	}
	clients, err := sessionAttachedClients(name)
	if err != nil {
		return false, 0, err
	}
	return busy, clients, nil
}

func sessionAttachedClients(name string) (int, error) {
	conn, err := connectSession(name, time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := writeFrame(conn, frameActivity, nil); err != nil {
		return 0, err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return 0, err
	}
	if typ != frameActivityResp {
		return 0, fmt.Errorf("unexpected frame %d from session %q", typ, name)
	}
	var activity sessionActivitySnapshot
	if err := json.Unmarshal(payload, &activity); err != nil {
		return 0, err
	}
	return activity.AttachedClients, nil
}

func readSessionMeta(name string) (sessionMeta, error) {
	if err := validSessionName(name); err != nil {
		return sessionMeta{}, err
	}
	data, err := os.ReadFile(sessionMetaFile(name))
	if err != nil {
		return sessionMeta{}, err
	}
	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return sessionMeta{}, err
	}
	return meta, nil
}

func (nativeBackend) Kill(name string) error {
	logEvent("stop requested for %s by %s", name, callerNames(1, 5))
	manifestForget(name) // stopping on purpose: do not offer to restore it
	conn, err := connectSession(name, time.Second)
	if err != nil {
		if sockDead(err) {
			removeSessionFiles(name)
		}
		return fmt.Errorf("no such session %q", name)
	}
	defer conn.Close()
	return writeFrame(conn, frameKill, nil)
}

func (nativeBackend) Rename(name, newName string) error {
	if err := validSessionName(name); err != nil {
		return err
	}
	if err := validSessionName(newName); err != nil {
		return err
	}
	conn, err := connectSession(name, time.Second)
	if err != nil {
		return fmt.Errorf("no such session %q", name)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := writeFrame(conn, frameRename, []byte(newName)); err != nil {
		return err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return err
	}
	if typ != frameRenameResp {
		return fmt.Errorf("unexpected frame %d while renaming session %q", typ, name)
	}
	if len(payload) > 0 {
		return errors.New(string(payload))
	}
	return nil
}

func (nativeBackend) History(name string) (string, error) {
	conn, err := connectSession(name, time.Second)
	if err != nil {
		return "", fmt.Errorf("no such session %q", name)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeFrame(conn, frameHistory, nil); err != nil {
		return "", err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return "", err
	}
	if typ != frameHistoryResp {
		return "", fmt.Errorf("unexpected frame %d from session %q", typ, name)
	}
	return string(payload), nil
}

// Send types text into the session followed by Enter.
func (nativeBackend) Send(name, text string) error {
	conn, err := connectSession(name, time.Second)
	if err != nil {
		return fmt.Errorf("no such session %q", name)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return writeFrame(conn, frameInput, []byte(text+"\r"))
}

// attachCmd resolves `lam attach [SESSION]` and connects to it.
// With no argument it attaches to the only session, or to the last-selected
// live session when there are several. With "." it attaches to the session
// started in the current directory.
func attachCmd(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: lam attach [SESSION]")
	}
	if len(args) == 0 {
		sessions, err := listSessionsWithError()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			return fmt.Errorf("no sessions to attach to (start one with 'lam start')")
		}
		if len(sessions) == 1 {
			return runAttachAndRemember(sessions[0].Name)
		}
		if last := lastSelectedSession(); last != "" {
			for _, s := range sessions {
				if s.Name == last {
					return runAttachAndRemember(last)
				}
			}
		}
		names := make([]string, 0, len(sessions))
		for _, s := range sessions {
			names = append(names, s.Name)
		}
		return fmt.Errorf("several sessions; say which one: lam attach %s", strings.Join(names, "|"))
	}
	target := args[0]
	if target == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		sessions, err := listSessionsWithError()
		if err != nil {
			return err
		}
		var here []sessionInfo
		for _, s := range sessions {
			live := s.CWD
			if pid, err := strconv.Atoi(s.PanePID); err == nil && pid > 0 {
				if dir := processWorkingDirectory(pid); dir != "" {
					live = dir
				}
			}
			if live == "" {
				live = s.CWD
			}
			if sameSessionDir(live, cwd) || sameSessionDir(s.CWD, cwd) {
				here = append(here, s)
			}
		}
		switch len(here) {
		case 0:
			return fmt.Errorf("no session started in %s", cwd)
		case 1:
			return runAttachAndRemember(here[0].Name)
		default:
			names := make([]string, 0, len(here))
			for _, s := range here {
				names = append(names, s.Name)
			}
			return fmt.Errorf("several sessions started in %s; say which one: lam attach %s", cwd, strings.Join(names, "|"))
		}
	}
	return runAttachAndRemember(target)
}

func runAttachAndRemember(name string) error {
	rememberSelectedSession(name)
	return runAttach(name)
}

// runAttach connects the current terminal to a session until the session ends
// or the user detaches with Ctrl+Space or Ctrl+\. Detaching leaves it running.
func runAttach(name string) error {
	conn, err := connectSession(name, 2*time.Second)
	if err != nil {
		if sockDead(err) {
			removeSessionFiles(name)
		}
		return fmt.Errorf("no such session %q", name)
	}
	return runAttachConnection(name, conn)
}

func runAttachConnection(name string, conn net.Conn) error {
	defer conn.Close()

	// The goroutines below outlive this call's caller-visible state, so they use
	// the files as they were when it began.
	stdin, stdout := os.Stdin, os.Stdout
	fd := int(stdin.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, old)
	}

	// Writes come from the stdin goroutine and the resize loop.
	var wmu sync.Mutex
	send := func(typ byte, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(conn, typ, p)
	}
	sendSize := func() {
		cols, rows, err := term.GetSize(fd)
		if err != nil || cols <= 0 || rows <= 0 {
			cols, rows = 80, 24
		}
		send(frameResize, resizePayload(cols, rows))
	}
	sendSize() // the host takes the first frame as the attach request

	ended := make(chan error, 1)
	go func() {
		for {
			typ, payload, err := readFrame(conn)
			if err != nil {
				ended <- nil // a closed session is a normal end to attach
				return
			}
			if typ == frameSnapshot || typ == frameOutput {
				if err := writeAll(stdout, payload); err != nil {
					ended <- fmt.Errorf("write session output: %w", err)
					return
				}
			}
		}
	}()

	detached := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				if i := bytes.IndexAny(buf[:n], detachKeys); i >= 0 {
					if i > 0 {
						send(frameInput, buf[:i])
					}
					close(detached)
					return
				}
				if send(frameInput, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	for {
		select {
		case err := <-ended:
			return err
		case <-detached:
			fmt.Fprintf(stdout, "\r\n[detached from %s; the session keeps running]\r\n", name)
			return nil
		case <-winch:
			sendSize()
		}
	}
}
