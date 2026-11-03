package app

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// This file is the exported surface the mobile client (package lammobile) is
// built on: a connection to a remote lam, its session list, and attached
// sessions that deliver raw terminal output and take raw input.

// MobileSession is one session of the remote host.
type MobileSession struct {
	Name     string
	CWD      string
	Command  string
	Activity string // "active" while the session is producing output
	Unread   bool
	Clients  int
}

// MobileLink is a handshaken connection to a remote lam.
type MobileLink struct {
	b *remoteSessionBackend
}

// MobileConnectSSH dials host with the built-in SSH client and runs `lam
// remote` there.
func MobileConnectSSH(host string, opts SSHOptions) (*MobileLink, error) {
	stream, err := DialSSH(host, opts)
	if err != nil {
		return nil, err
	}
	link, err := MobileConnectConn(stream, stream)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, stream.Stderr.suffix())
	}
	return link, nil
}

// MobileConnectConn runs the mux handshake over rw, which must carry the
// stdio of `lam remote` (reads from its stdout, writes to its stdin).
func MobileConnectConn(r io.Reader, rw io.ReadWriteCloser) (*MobileLink, error) {
	reader := bufio.NewReader(r)
	client, err := muxHandshake(pipeRWC{Reader: reader, WriteCloser: rw}, reader, nil)
	if err != nil {
		return nil, err
	}
	return &MobileLink{b: &remoteSessionBackend{name: "mobile", client: client, stopping: make(chan struct{})}}, nil
}

// Sessions lists the host's sessions.
func (l *MobileLink) Sessions() ([]MobileSession, error) {
	list, err := l.b.List()
	if err != nil {
		return nil, err
	}
	// An agent's own shell is part of it; the list shows the agents, as the
	// desktop window does.
	list = hideOwnedTerminals(list)
	out := make([]MobileSession, 0, len(list))
	for _, s := range list {
		out = append(out, MobileSession{Name: s.Name, CWD: s.CWD, Command: s.Command, Activity: s.Activity, Unread: s.Unread, Clients: s.Clients})
	}
	return out, nil
}

// Launchers lists the agents installed on the host, in the usual order. A plain
// terminal is always possible and is not listed.
func (l *MobileLink) Launchers() ([]string, error) {
	got, err := l.b.AvailableLaunchers()
	if err != nil {
		return nil, err
	}
	return orderedSupportedLaunchers(got), nil
}

// Home is the host's home directory, where a new session starts by default.
func (l *MobileLink) Home() (string, error) { return l.b.HomeDirectory() }

// CompleteDir lists the host's directories that continue input, as the
// desktop's Tab completion does.
func (l *MobileLink) CompleteDir(input, base string) ([]string, error) {
	return l.b.CompleteSessionDir(input, base)
}

// CreateSession starts a session on the host in dir (empty means home) and
// returns its name, made like the desktop's: tool and folder, such as
// claude-api. launcher "terminal" starts a shell.
func (l *MobileLink) CreateSession(launcher, dir string) (string, error) {
	home, err := l.b.HomeDirectory()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dir) == "" {
		dir = home
	}
	cwd, err := l.b.ResolveSessionDir(strings.TrimSpace(dir), home)
	if err != nil {
		return "", err
	}
	command := fallbackShellCommand
	if launcher != "terminal" {
		if !slices.Contains(supportedLaunchers, launcher) {
			return "", fmt.Errorf("unknown agent %q", launcher)
		}
		command = launcherCmd(launcher, "")
	}
	existing, err := l.b.List()
	if err != nil {
		return "", err
	}
	name := launcherSessionName(launcher, cwd, existing)
	if err := l.b.Create(name, cwd, command); err != nil {
		return "", err
	}
	return name, nil
}

// Done is closed when the connection is lost.
func (l *MobileLink) Done() <-chan struct{} { return l.b.cur().done }

// Err says why the connection ended.
func (l *MobileLink) Err() string { return l.b.cur().cause() }

// Close ends the connection.
func (l *MobileLink) Close() { l.b.Close() }

// MobileAttach is a session attached over a link.
type MobileAttach struct {
	stream io.ReadWriteCloser
	reader *bufio.Reader
	wmu    sync.Mutex
}

// Attach attaches to a session at the given terminal size. The first output is
// a redraw of the current screen.
func (l *MobileLink) Attach(name string, cols, rows int) (*MobileAttach, error) {
	stream, err := l.b.OpenAttachStream(name, cols, rows)
	if err != nil {
		return nil, err
	}
	return &MobileAttach{stream: stream, reader: bufio.NewReaderSize(stream, 64<<10)}, nil
}

// ReadOutput blocks until the session has terminal output and returns it. It
// returns an error when the session or the connection ends.
func (a *MobileAttach) ReadOutput() ([]byte, error) {
	for {
		typ, payload, err := readFrame(a.reader)
		if err != nil {
			if err == io.EOF {
				err = errors.New("session closed")
			}
			return nil, err
		}
		if typ == frameSnapshot || typ == frameOutput {
			return payload, nil
		}
	}
}

func (a *MobileAttach) send(typ byte, payload []byte) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return writeFrame(a.stream, typ, payload)
}

// WriteInput sends bytes to the session as if typed.
func (a *MobileAttach) WriteInput(data []byte) error {
	for len(data) > 0 {
		n := min(len(data), 4096)
		if err := a.send(frameInput, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// Resize tells the session the terminal size.
func (a *MobileAttach) Resize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 0xffff || rows > 0xffff {
		return fmt.Errorf("invalid size %dx%d", cols, rows)
	}
	return a.send(frameResize, resizePayload(cols, rows))
}

// Close detaches.
func (a *MobileAttach) Close() { _ = a.stream.Close() }

// ---- demo host ----

var demoMu sync.Mutex
var demoStarted bool

// StartMobileDemo starts an in-process `lam remote` with two fake sessions and
// returns the client's end of the connection to it. Nothing leaves the
// process: the server side runs serveRemote over a net.Pipe, and the sessions
// are unix sockets in a temporary directory speaking the host frame protocol.
func StartMobileDemo() (io.Reader, io.ReadWriteCloser, error) {
	demoMu.Lock()
	defer demoMu.Unlock()
	if !demoStarted {
		dir := filepath.Join(os.TempDir(), "lam-demo")
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, err
		}
		os.Setenv("LAM_SOCKET_DIR", dir)
		for _, s := range demoSessions {
			ln, err := net.Listen("unix", sessionSocket(s.Name))
			if err != nil {
				return nil, nil, err
			}
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					go demoSession(conn, s)
				}
			}()
		}
		setBackend(demoBackend{})
		widenPATH = func() {} // no login shell to ask on a phone
		demoStarted = true
	}
	clientEnd, serverEnd := net.Pipe()
	go func() {
		_ = serveRemote(serverEnd, serverEnd)
		serverEnd.Close()
	}()
	return clientEnd, clientEnd, nil
}

type demoSessionInfo struct {
	Name, CWD, Command, Activity string
	Banner                       string
}

var demoSessions = []demoSessionInfo{
	{Name: "claude-demo", CWD: "/home/demo/lam", Command: "claude", Activity: "active", Banner: "Claude Code"},
	{Name: "shell", CWD: "/home/demo", Command: "bash", Activity: "quiet", Banner: "bash"},
}

type demoBackend struct{}

func (demoBackend) Name() string { return "demo" }
func (demoBackend) Create(name, cwd, command string) error {
	return errors.New("the demo host cannot create sessions")
}
func (demoBackend) Kill(string) error { return errors.New("the demo host cannot kill sessions") }
func (demoBackend) History(string) (string, error) {
	return "", nil
}
func (demoBackend) Send(string, string) error { return nil }
func (demoBackend) List() ([]sessionInfo, error) {
	var out []sessionInfo
	for i, s := range demoSessions {
		out = append(out, sessionInfo{Name: s.Name, CWD: s.CWD, Command: s.Command, Activity: s.Activity,
			Created: time.Now().Add(-time.Duration(i+1) * time.Hour)})
	}
	return out, nil
}

// demoSession plays one attached client: it reads the attach request, sends a
// screen, streams scripted colored output, and echoes what is typed.
func demoSession(conn net.Conn, info demoSessionInfo) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if typ, _, err := readFrame(reader); err != nil || typ != frameResize {
		return
	}
	var wmu sync.Mutex
	send := func(typ byte, text string) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(conn, typ, []byte(text))
	}
	const prompt = "\x1b[1;32mdemo@lam\x1b[0m:\x1b[1;34m~\x1b[0m$ "
	if send(frameSnapshot, "\x1b[2J\x1b[H") != nil {
		return
	}
	script := []string{
		"\x1b[1;38;2;140;200;140m  lam demo: " + info.Banner + "\x1b[0m\r\n",
		"\x1b[2m  scripted output, nothing here is a real host\x1b[0m\r\n\r\n",
		"\x1b[31mred\x1b[0m \x1b[32mgreen\x1b[0m \x1b[33myellow\x1b[0m \x1b[34mblue\x1b[0m \x1b[35mmagenta\x1b[0m \x1b[36mcyan\x1b[0m\r\n",
		"\x1b[1mbold\x1b[0m \x1b[3mitalic\x1b[0m \x1b[4munderline\x1b[0m \x1b[7mreverse\x1b[0m\r\n",
		"\x1b[38;5;208m256-color orange\x1b[0m  \x1b[38;2;140;200;140mtruecolor green\x1b[0m\r\n\r\n",
		"\x1b[32m+\x1b[0m added a thing\r\n\x1b[31m-\x1b[0m removed a thing\r\n\r\n",
		"Type something; it is echoed back.\r\n" + prompt,
	}
	go func() {
		for _, line := range script {
			if send(frameOutput, line) != nil {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()
	line := 0
	for {
		typ, payload, err := readFrame(reader)
		if err != nil {
			return
		}
		switch typ {
		case frameInput:
			var echo []byte
			for _, b := range payload {
				switch b {
				case '\r':
					echo = append(echo, "\r\n"+prompt...)
					line = 0
				case 0x7f:
					if line > 0 {
						echo = append(echo, "\b \b"...)
						line--
					}
				default:
					echo = append(echo, b)
					line++
				}
			}
			if send(frameOutput, string(echo)) != nil {
				return
			}
		case frameResize:
			if cols, rows, ok := parseResize(payload); ok {
				if send(frameOutput, fmt.Sprintf("\x1b[2m[resized to %dx%d]\x1b[0m\r\n", cols, rows)) != nil {
					return
				}
			}
		case framePing:
			var empty [5]byte
			empty[0] = framePing
			binary.BigEndian.PutUint32(empty[1:], 0)
			wmu.Lock()
			_, _ = conn.Write(empty[:])
			wmu.Unlock()
		}
	}
}
