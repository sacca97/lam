package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
)

const remoteProtocolVersion = 3

// readRemoteLine reads one newline-terminated line of at most limit bytes.
func readRemoteLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, fmt.Errorf("remote request exceeds %d bytes", limit)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

type remoteRequest struct {
	ID      uint64   `json:"id"`
	Op      string   `json:"op"`
	Name    string   `json:"name,omitempty"`
	NewName string   `json:"newName,omitempty"`
	CWD     string   `json:"cwd,omitempty"`
	Command string   `json:"command,omitempty"`
	Text    string   `json:"text,omitempty"`
	Path    string   `json:"path,omitempty"`
	Base    string   `json:"base,omitempty"`
	Names   []string `json:"names,omitempty"`
	Offset  int64    `json:"offset,omitempty"`
	Length  int      `json:"length,omitempty"`
}

type remoteResponse struct {
	ID          uint64          `json:"id"`
	Error       string          `json:"error,omitempty"`
	Protocol    int             `json:"protocol,omitempty"`
	Version     string          `json:"version,omitempty"` // the build, from ping
	Sessions    []sessionInfo   `json:"sessions,omitempty"`
	Directory   string          `json:"directory,omitempty"`
	Directories []string        `json:"directories,omitempty"`
	Launchers   []string        `json:"launchers,omitempty"`
	History     string          `json:"history,omitempty"`
	Busy        bool            `json:"busy,omitempty"`
	Clients     int             `json:"clients,omitempty"`
	Restorable  []manifestEntry `json:"restorable,omitempty"`
	Restored    int             `json:"restored,omitempty"`
	Failures    []string        `json:"failures,omitempty"`
	Data        []byte          `json:"data,omitempty"`     // read-file: a piece of the file
	Size        int64           `json:"size,omitempty"`     // read-file: the whole file
	Modified    int64           `json:"modified,omitempty"` // read-file: its modification time, unix nanoseconds
}

type remoteRPCResult struct {
	response remoteResponse
	err      error
}

// pipeRWC joins the two ends of an ssh process's stdio into one connection.
type pipeRWC struct {
	io.Reader
	io.WriteCloser
}

// startRemoteRPC runs `lam remote` on the host over ssh and talks to it. The
// ssh process is the only ssh connection: requests and every attached session
// share it.
// validateSSHHost rejects what ssh would read as an option or several words.
func validateSSHHost(host string) error {
	if strings.TrimSpace(host) == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("invalid SSH host %q", host)
	}
	return nil
}

// builtinSSH selects the in-process SSH client instead of the ssh binary
// (`lam ssh --builtin`, or LAM_SSH=builtin).
var builtinSSH = os.Getenv("LAM_SSH") == "builtin"

// guiSSHOptions, when set, supplies the client's options in place of the
// terminal's: its questions for a connection a person is waiting on, and for
// a reconnect only what needs no one (a remembered password).
var guiSSHOptions func(unattended bool) SSHOptions

// sshGlobalsMu guards the two settings above: the GUI changes them while
// connections of earlier hosts may still reconnect.
var sshGlobalsMu sync.RWMutex

func startRemoteRPC(host string, unattended bool) (*muxClient, error) {
	sshGlobalsMu.RLock()
	builtin := builtinSSH
	sshGlobalsMu.RUnlock()
	return startRemoteRPCVia(host, unattended, builtin)
}

// startRemoteRPCVia connects with the ssh binary or lam's own client.
func startRemoteRPCVia(host string, unattended, builtin bool) (*muxClient, error) {
	if err := validateSSHHost(host); err != nil {
		return nil, err
	}
	sshGlobalsMu.RLock()
	hook := guiSSHOptions
	sshGlobalsMu.RUnlock()
	if builtin {
		opts := SSHOptionsForCLI(unattended)
		if hook != nil {
			opts = hook(unattended)
		}
		return startBuiltinRemoteRPC(host, unattended, opts)
	}
	cmd, tail, err := newSSHCommand(unattended, host, remoteToolCommand("remote"))
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open SSH input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("open SSH output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start SSH: %w", err)
	}
	reader := bufio.NewReader(stdout)
	client, err := muxHandshake(pipeRWC{Reader: reader, WriteCloser: stdin}, reader, func() {
		// The host exits when its input closes; give it a moment, then stop ssh.
		exited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-exited
		}
	})
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, tail.suffix())
	}
	return client, nil
}

// sshArgs is the ssh command line for running a command on host. Keepalives
// make ssh itself give up on a dead link in about 15 seconds. An unattended
// connection (a reconnect while the interface is drawing) must never wait for
// a password or a host-key question, so it fails instead.
func sshArgs(unattended bool, host, command string) []string {
	// Without a tty ssh marks its packets as bulk traffic (DSCP CS1), which
	// Wi-Fi puts in the background queue; this connection carries keystrokes.
	args := []string{"-T", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-o", "IPQoS=af21"}
	if unattended {
		args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	}
	return append(args, "--", host, command)
}

// newSSHCommand prepares ssh to run command on host. An attended command shows
// ssh's messages; an unattended one keeps them in the returned tail so a failure
// can say why.
func newSSHCommand(unattended bool, host, command string) (*exec.Cmd, *stderrTail, error) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return nil, nil, fmt.Errorf("find ssh: %w", err)
	}
	cmd := exec.Command(ssh, sshArgs(unattended, host, command)...)
	tail := &stderrTail{}
	if unattended {
		cmd.Stderr = tail
	} else {
		cmd.Stderr = os.Stderr
	}
	return cmd, tail, nil
}

// stderrTail keeps the end of what ssh printed, to explain why it failed.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 512 {
		t.buf = t.buf[len(t.buf)-512:]
	}
	return len(p), nil
}

func (t *stderrTail) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := strings.TrimSpace(string(t.buf)); s != "" {
		return " (" + strings.ReplaceAll(s, "\n", "; ") + ")"
	}
	return ""
}

// muxHandshake agrees on the protocol with the host and switches the
// connection to frames. A host running an older lam is refused with a message
// saying so; there is no slower fallback.
func muxHandshake(rw io.ReadWriteCloser, reader *bufio.Reader, cleanup func()) (*muxClient, error) {
	var cleanupOnce sync.Once
	teardown := func() {
		_ = rw.Close()
		if cleanup != nil {
			cleanupOnce.Do(cleanup)
		}
	}
	type outcome struct {
		client *muxClient
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		// Both requests go out at once: the host answers them in order, so the
		// handshake costs one round trip instead of two.
		var batch []byte
		for _, request := range []remoteRequest{{ID: 1, Op: "ping"}, {ID: 2, Op: "mux"}} {
			data, _ := json.Marshal(request)
			batch = append(append(batch, data...), '\n')
		}
		if err := writeAll(rw, batch); err != nil {
			result <- outcome{err: err}
			return
		}
		answer := func(request remoteRequest) (remoteResponse, error) {
			line, err := readRemoteLine(reader, maxFramePayload)
			if err != nil {
				return remoteResponse{}, fmt.Errorf("the remote lam closed the connection: %w", err)
			}
			var response remoteResponse
			if err := json.Unmarshal(line, &response); err != nil {
				return remoteResponse{}, fmt.Errorf("unexpected reply from the remote lam: %w", err)
			}
			if response.Error != "" {
				return remoteResponse{}, fmt.Errorf("remote %s: %s", request.Op, response.Error)
			}
			return response, nil
		}
		response, err := answer(remoteRequest{Op: "ping"})
		if err == nil && response.Protocol != remoteProtocolVersion {
			err = &protocolMismatchError{remote: response.Protocol, remoteVersion: response.Version}
		}
		if err == nil {
			_, err = answer(remoteRequest{Op: "mux"})
		}
		if err != nil {
			result <- outcome{err: err}
			return
		}
		client := newMuxClient(rw, reader, func() {
			if cleanup != nil {
				cleanupOnce.Do(cleanup)
			}
		})
		client.version = response.Version
		result <- outcome{client: client}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			teardown()
			return nil, got.err
		}
		return got.client, nil
	case <-time.After(muxHandshakeWait):
		teardown()
		return nil, errors.New("timed out waiting for the remote lam to answer: is it installed and up to date?")
	}
}

// serveRemoteHandshake is the host's side of muxHandshake: it answers ping,
// then on "mux" hands the connection to serveMux. Nothing else is accepted
// before that.
func serveRemoteHandshake(reader *bufio.Reader, w io.Writer, closer io.Closer) error {
	for {
		line, err := readRemoteLine(reader, 4096)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var request remoteRequest
		if err := json.Unmarshal(line, &request); err != nil {
			return fmt.Errorf("decode remote request: %w", err)
		}
		reply := func(response remoteResponse) error {
			response.ID = request.ID
			data, _ := json.Marshal(response)
			return writeAll(w, append(data, '\n'))
		}
		switch request.Op {
		case "ping":
			if err := reply(remoteResponse{Protocol: remoteProtocolVersion, Version: version}); err != nil {
				return err
			}
		case "mux":
			if err := reply(remoteResponse{}); err != nil {
				return err
			}
			return serveMux(w, reader, connectSession, closer)
		default:
			if err := reply(remoteResponse{Error: "update lam: this host needs the multiplexed protocol"}); err != nil {
				return err
			}
		}
	}
}

// widenPATH is a variable so a test can see that serveRemote calls it.
var widenPATH = useLoginShellPATH

// serveRemote speaks to one client on stdio (the ssh case). A client that goes
// silent is dropped: closing stdin cannot interrupt a blocked read, so the
// caller is told to stop waiting instead. The process then exits and the
// sessions, which are separate processes, keep running.
func serveRemote(in io.Reader, out io.Writer) error {
	// ssh runs this without a login shell, so the provider CLIs the profile puts
	// on PATH would not be found: the launcher list would hold only the shell.
	widenPATH()
	dropped := &stdioDrop{in: in, out: out, done: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- serveRemoteHandshake(bufio.NewReader(in), out, dropped) }()
	select {
	case err := <-result:
		return err
	case <-dropped.done:
		return nil
	}
}

// stdioDrop is the io.Closer given to serveMux for the stdio case.
type stdioDrop struct {
	in   io.Reader
	out  io.Writer
	once sync.Once
	done chan struct{}
}

func (d *stdioDrop) Close() error {
	d.once.Do(func() {
		for _, x := range []any{d.in, d.out} {
			if closer, ok := x.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		close(d.done)
	})
	return nil
}

// streamAttacher is implemented by backends that attach sessions over their
// connection instead of running a process.
type streamAttacher interface {
	OpenAttachStream(name string, cols, rows int) (io.ReadWriteCloser, error)
}

func (b *remoteSessionBackend) OpenAttachStream(name string, cols, rows int) (io.ReadWriteCloser, error) {
	return b.cur().OpenStream(name, cols, rows)
}

func newSSHSessionBackend(host string) (*sshSessionBackend, error) {
	sshGlobalsMu.RLock()
	builtin := builtinSSH
	sshGlobalsMu.RUnlock()
	return newSSHSessionBackendVia(host, builtin)
}

func newSSHSessionBackendVia(host string, builtin bool) (*sshSessionBackend, error) {
	client, err := startRemoteRPCVia(host, false, builtin)
	if err != nil {
		return nil, err
	}
	b := &sshSessionBackend{host: host, remoteSessionBackend: remoteSessionBackend{name: "ssh:" + host, client: client}}
	b.supervise(func() (*muxClient, error) { return startRemoteRPCVia(host, true, builtin) })
	return b, nil
}

// newSSHSessionBackendKeysFirst connects the way people expect: with the ssh
// binary and no questions, which uses their keys, agent, certificates and
// ~/.ssh/config exactly as in a terminal. Only when that is refused for a
// reason a person can fix (a password is needed, the host is new) does it
// fall back to lam's own client, which can ask in the window.
func newSSHSessionBackendKeysFirst(host string) (*sshSessionBackend, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return newSSHSessionBackendVia(host, true)
	}
	// host:port is lam's spelling; the ssh binary would take it as a name.
	if _, _, port, err := parseSSHHost(host); err == nil && port != "" {
		return newSSHSessionBackendVia(host, true)
	}
	client, err := startRemoteRPCVia(host, true, false) // BatchMode: never asks
	if err == nil {
		b := &sshSessionBackend{host: host, remoteSessionBackend: remoteSessionBackend{name: "ssh:" + host, client: client}}
		b.supervise(func() (*muxClient, error) { return startRemoteRPCVia(host, true, false) })
		return b, nil
	}
	// A host that cannot be reached will not be reachable the second time.
	for _, down := range []string{"timed out", "Connection refused", "No route to host", "Network is unreachable"} {
		if strings.Contains(err.Error(), down) {
			return nil, err
		}
	}
	return newSSHSessionBackendVia(host, true)
}

func remoteToolCommand(args ...string) string {
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		quotedArgs[i] = shellQuote(arg)
	}
	command := strings.Join(quotedArgs, " ")
	return "if [ -x \"$HOME/.local/bin/lam\" ]; then " +
		"exec \"$HOME/.local/bin/lam\" " + command +
		"; elif command -v lam >/dev/null 2>&1; then " +
		"exec lam " + command +
		"; else printf '%s\\n' 'LAM is not installed on this SSH host. Install it there with make install or add its binary to PATH.' >&2; exit 127; fi"
}

type sshSessionBackend struct {
	host string
	remoteSessionBackend
}

type remoteSessionBackend struct {
	name string

	// The connection can be replaced: when it is lost, supervise dials a new one
	// and the sessions on the remote host, which never stopped, are attached
	// again. linkMu guards what follows up to dirs.
	linkMu   sync.Mutex
	client   *muxClient
	gen      int // counts connections, so a pane can tell its own is gone
	attempt  int // reconnect attempts since the link dropped; 0 while up
	retryAt  time.Time
	lastErr  string
	stopping chan struct{}
	stopOnce sync.Once
	wake     chan struct{} // ends the wait before the next attempt

	// dirs caches the remote home and working directory. They cannot change
	// during a connection and are read while drawing, once per session row,
	// so asking the remote host each time costs a network round trip per row
	// per frame.
	dirMu sync.Mutex
	dirs  map[string]cachedRemoteDir
}

// linkStatus describes the connection for the interface.
type linkStatus struct {
	Up      bool
	Gen     int // changes every time the connection is replaced
	Attempt int
	RetryAt time.Time
	Err     string
}

// linkMonitor is implemented by backends whose connection can drop and return.
type linkMonitor interface {
	LinkStatus() linkStatus
}

// permanentSSHFailure is an error that waiting will not cure.
func permanentSSHFailure(msg string) bool {
	msg = strings.ToLower(msg)
	for _, s := range []string{"unable to authenticate", "host identification has changed", "was not accepted",
		"not in known_hosts", "no remembered password", "no password given", "no passphrase given", "no ssh credentials", "not supported by lam"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func (b *remoteSessionBackend) cur() *muxClient {
	b.linkMu.Lock()
	defer b.linkMu.Unlock()
	return b.client
}

func (b *remoteSessionBackend) LinkStatus() linkStatus {
	b.linkMu.Lock()
	defer b.linkMu.Unlock()
	// The supervisor learns of a drop a moment after the client does; until
	// it has, the link is already not up.
	dead := false
	if b.client != nil {
		select {
		case <-b.client.done:
			dead = true
		default:
		}
	}
	return linkStatus{Up: b.attempt == 0 && !dead, Gen: b.gen, Attempt: b.attempt, RetryAt: b.retryAt, Err: b.lastErr}
}

// Reconnect skips the wait before the next attempt.
func (b *remoteSessionBackend) Reconnect() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// remoteRetryDelays is the wait after each failed attempt; the last repeats.
var remoteRetryDelays = []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// supervise watches the connection and replaces it when it is lost. dial makes
// a new one without asking the user anything.
func (b *remoteSessionBackend) supervise(dial func() (*muxClient, error)) {
	b.wake = make(chan struct{}, 1)
	b.linkMu.Lock()
	if b.stopping == nil {
		b.stopping = make(chan struct{})
	}
	stopping := b.stopping
	b.linkMu.Unlock()
	delays := remoteRetryDelays
	go func() {
		for {
			current := b.cur()
			select {
			case <-current.done:
			case <-stopping:
				return
			}
			select { // Close ends the connection too, and must not start another
			case <-stopping:
				return
			default:
			}
			lost := current.cause()
			for attempt := 1; ; attempt++ {
				delay := delays[min(attempt-1, len(delays)-1)]
				b.linkMu.Lock()
				b.attempt, b.retryAt, b.lastErr = attempt, time.Now().Add(delay), lost
				b.linkMu.Unlock()
				if delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-timer.C:
					case <-b.wake:
						timer.Stop()
					case <-stopping:
						timer.Stop()
						return
					}
				}
				next, err := dial()
				if err == nil {
					// Close must not slip in between the check and the swap.
					b.linkMu.Lock()
					select {
					case <-stopping:
						b.linkMu.Unlock()
						next.close()
						return
					default:
					}
					b.client = next
					b.gen++
					b.attempt, b.lastErr = 0, ""
					b.linkMu.Unlock()
					b.dirMu.Lock()
					for op, c := range b.dirs {
						if c.err != nil {
							delete(b.dirs, op)
						}
					}
					b.dirMu.Unlock()
					break
				}
				select {
				case <-stopping:
					return
				default:
				}
				lost = err.Error()
				if permanentSSHFailure(lost) {
					// Retrying cannot help and may lock the account: wait
					// for the person (Reconnect) instead.
					b.linkMu.Lock()
					b.lastErr, b.retryAt = lost+" (not retrying; reconnect to try again)", time.Time{}
					b.linkMu.Unlock()
					select {
					case <-b.wake:
					case <-stopping:
						return
					}
				}
			}
		}
	}()
}

type cachedRemoteDir struct {
	value string
	err   error
	at    time.Time
}

// remoteDirErrorTTL is how long a failed lookup is remembered, so a
// disconnected host does not stall every frame on repeated timeouts.
const remoteDirErrorTTL = 10 * time.Second

// remoteDirTimeout bounds one directory lookup, so a dead link shows an error
// quickly instead of waiting for the heartbeat to give up.
const remoteDirTimeout = 5 * time.Second

// cachedDirectory returns the answer to a directory request, asking the
// remote host only the first time (or again after a failed lookup expires).
func (b *remoteSessionBackend) cachedDirectory(op string) (string, error) {
	b.dirMu.Lock()
	if c, ok := b.dirs[op]; ok && (c.err == nil || time.Since(c.at) < remoteDirErrorTTL) {
		b.dirMu.Unlock()
		return c.value, c.err
	}
	b.dirMu.Unlock()
	// The lock is not held across the request: a stalled link must not block
	// every other lookup behind it.
	response, err := b.callWithTimeout(remoteRequest{Op: op}, remoteDirTimeout)
	b.dirMu.Lock()
	defer b.dirMu.Unlock()
	if b.dirs == nil {
		b.dirs = make(map[string]cachedRemoteDir)
	}
	b.dirs[op] = cachedRemoteDir{value: response.Directory, err: err, at: time.Now()}
	return response.Directory, err
}

// errUpdatedLocal means this machine was updated and must be started again.
var errUpdatedLocal = errors.New("lam was updated")

func runSSH(host string, promptMe bool) error {
	backend, err := connectOfferingUpdate(host, func() (*sshSessionBackend, error) { return newSSHSessionBackend(host) },
		func(b *sshSessionBackend) *remoteSessionBackend { return &b.remoteSessionBackend })
	if errors.Is(err, errUpdatedLocal) {
		return nil
	}
	if err != nil {
		return err
	}
	defer backend.Close()
	setBackend(backend)
	go backend.HomeDirectory() // cached for the first frame, fetched while the directory below is
	launchDir, err := backend.WorkingDirectory()
	if err != nil {
		return fmt.Errorf("read remote working directory: %w", err)
	}
	runIDE(promptMe, launchDir)
	return nil
}

// connectOfferingUpdate connects, and when the two ends run different builds
// says which is older and offers to update, as `lam ssh` does. host is the ssh
// host used to reach the remote machine.
func connectOfferingUpdate[B interface{ Close() }](host string, connect func() (B, error), remote func(B) *remoteSessionBackend) (B, error) {
	backend, err := connect()
	if err != nil {
		var mismatch *protocolMismatchError
		if errors.As(err, &mismatch) {
			updated, local := offerUpdate(host, mismatch.remoteVersion, mismatch.remote < remoteProtocolVersion)
			if local {
				return backend, errUpdatedLocal
			}
			if updated {
				backend, err = connect()
			}
		}
		return backend, err
	}
	// The handshake already told us the remote build.
	if remoteVersion := remote(backend).cur().version; remoteVersion != "" {
		if order, ok := compareRelease(version, remoteVersion); ok && order != 0 {
			updated, local := offerUpdate(host, remoteVersion, order > 0)
			if local {
				backend.Close()
				return backend, errUpdatedLocal
			}
			if updated {
				// The remote process that answered is the old build: start over.
				backend.Close()
				return connect()
			}
		}
	}
	return backend, nil
}

func (b *remoteSessionBackend) Name() string { return b.name }
func (b *remoteSessionBackend) Close() {
	b.stopOnce.Do(func() {
		b.linkMu.Lock()
		if b.stopping == nil {
			b.stopping = make(chan struct{})
		}
		close(b.stopping)
		b.linkMu.Unlock()
	})
	b.cur().close()
}

func (b *remoteSessionBackend) List() ([]sessionInfo, error) {
	response, err := b.cur().call(remoteRequest{Op: "list"})
	return cleanRemoteSessions(response.Sessions), err
}

// cleanRemoteSessions drops sessions whose name carries control characters and
// strips them from the rest of what a host reports, as it is drawn as text in a
// terminal: an escape sequence in a name or a folder must not reach it.
func cleanRemoteSessions(list []sessionInfo) []sessionInfo {
	isCtl := func(r rune) bool { return unicode.IsControl(r) }
	out := list[:0]
	for _, s := range list {
		if strings.IndexFunc(s.Name, isCtl) >= 0 {
			continue
		}
		s.CWD = strings.Map(func(r rune) rune {
			if isCtl(r) {
				return -1
			}
			return r
		}, s.CWD)
		out = append(out, s)
	}
	return out
}

func (b *remoteSessionBackend) Create(name, cwd, command string) error {
	_, err := b.cur().call(remoteRequest{Op: "create", Name: name, CWD: cwd, Command: command})
	return err
}

func (b *remoteSessionBackend) Kill(name string) error {
	_, err := b.cur().call(remoteRequest{Op: "kill", Name: name})
	return err
}

func (b *remoteSessionBackend) Rename(name, newName string) error {
	_, err := b.cur().call(remoteRequest{Op: "rename", Name: name, NewName: newName})
	return err
}

func (b *remoteSessionBackend) History(name string) (string, error) {
	response, err := b.cur().call(remoteRequest{Op: "history", Name: name})
	return response.History, err
}

func (b *remoteSessionBackend) Send(name, text string) error {
	_, err := b.cur().call(remoteRequest{Op: "send", Name: name, Text: text})
	return err
}

func (b *remoteSessionBackend) SessionWorkingDirectory(name string) (string, error) {
	response, err := b.callWithTimeout(remoteRequest{Op: "session-cwd", Name: name}, time.Second)
	return response.Directory, err
}

func (b *remoteSessionBackend) SessionStatus(name string) (bool, int, error) {
	response, err := b.callWithTimeout(remoteRequest{Op: "session-status", Name: name}, time.Second)
	return response.Busy, response.Clients, err
}

func (b *remoteSessionBackend) callWithTimeout(request remoteRequest, timeout time.Duration) (remoteResponse, error) {
	type result struct {
		response remoteResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, err := b.cur().callFor(request, timeout+time.Second)
		completed <- result{response: response, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case got := <-completed:
		return got.response, got.err
	case <-timer.C:
		return remoteResponse{}, fmt.Errorf("remote %s timed out after %s", request.Op, timeout)
	}
}

func (b *remoteSessionBackend) RestorableSessions() ([]manifestEntry, error) {
	response, err := b.callWithTimeout(remoteRequest{Op: "restorable"}, 5*time.Second)
	return response.Restorable, err
}

func (b *remoteSessionBackend) RestoreSessions(names []string) (int, []string, error) {
	response, err := b.callWithTimeout(remoteRequest{Op: "restore", Names: names}, 2*time.Minute)
	return response.Restored, response.Failures, err
}

func (b *remoteSessionBackend) DiscardRestorable(names []string) error {
	_, err := b.cur().call(remoteRequest{Op: "discard-restorable", Names: names})
	return err
}

func (b *remoteSessionBackend) ClearUnread(name string) error {
	_, err := b.cur().call(remoteRequest{Op: "clear-unread", Name: name})
	return err
}

func (b *remoteSessionBackend) WorkingDirectory() (string, error) { return b.cachedDirectory("cwd") }

func (b *remoteSessionBackend) HomeDirectory() (string, error) { return b.cachedDirectory("home") }

func (b *remoteSessionBackend) ResolveSessionDir(input, base string) (string, error) {
	response, err := b.cur().call(remoteRequest{Op: "resolve-dir", Path: input, Base: base})
	return response.Directory, err
}

func (b *remoteSessionBackend) CompleteSessionDir(input, base string) ([]string, error) {
	response, err := b.cur().call(remoteRequest{Op: "complete-dir", Path: input, Base: base})
	return response.Directories, err
}

// oldHost explains the answer of a host too old to know an operation.
func oldHost(err error) error {
	if err != nil && strings.Contains(err.Error(), "unknown operation") {
		return errors.New("the host runs an older lam that cannot open documents; update it (lam update there)")
	}
	return err
}

func (b *remoteSessionBackend) CompleteDocumentPath(input, base string) ([]string, error) {
	response, err := b.cur().call(remoteRequest{Op: "complete-path", Path: input, Base: base})
	return response.Directories, oldHost(err)
}

func (b *remoteSessionBackend) ReadFileChunk(path string, offset int64, length int) (data []byte, size, modified int64, err error) {
	response, err := b.cur().call(remoteRequest{Op: "read-file", Path: path, Offset: offset, Length: length})
	return response.Data, response.Size, response.Modified, oldHost(err)
}

func (b *remoteSessionBackend) AvailableLaunchers() ([]string, error) {
	response, err := b.cur().call(remoteRequest{Op: "launchers"})
	return response.Launchers, err
}

func dispatchRemoteRequest(request remoteRequest) remoteResponse {
	response := remoteResponse{ID: request.ID}
	fail := func(err error) remoteResponse {
		response.Error = err.Error()
		return response
	}
	switch request.Op {
	case "ping":
		response.Protocol = remoteProtocolVersion
		response.Version = version
	case "list":
		sessions, err := listSessionsWithError()
		if err != nil {
			return fail(err)
		}
		response.Sessions = sessions
	case "create":
		if err := newSession(request.Name, request.CWD, request.Command); err != nil {
			return fail(err)
		}
	case "kill":
		if err := killSession(request.Name); err != nil {
			return fail(err)
		}
	case "rename":
		if err := renameSession(request.Name, request.NewName); err != nil {
			return fail(err)
		}
	case "history":
		history, err := backend().History(request.Name)
		if err != nil {
			return fail(err)
		}
		response.History = history
	case "send":
		if err := sendToSession(request.Name, request.Text); err != nil {
			return fail(err)
		}
	case "clear-unread":
		clearUnread(request.Name)
	case "restorable":
		response.Restorable = restorableSessions()
	case "restore":
		response.Restored, response.Failures = restoreNamed(request.Names)
	case "discard-restorable":
		manifestForget(request.Names...)
	case "cwd":
		cwd, err := startDirectory()
		if err != nil {
			return fail(err)
		}
		response.Directory = cwd
	case "home":
		home, err := os.UserHomeDir()
		if err != nil {
			return fail(err)
		}
		response.Directory = home
	case "resolve-dir":
		dir, err := resolveSessionDir(request.Path, request.Base)
		if err != nil {
			return fail(err)
		}
		response.Directory = dir
	case "complete-dir":
		directories, err := completeSessionDirs(request.Path, request.Base)
		if err != nil {
			return fail(err)
		}
		response.Directories = directories
	case "complete-path":
		paths, err := completePaths(request.Path, request.Base, true)
		if err != nil {
			return fail(err)
		}
		response.Directories = paths
	case "read-file":
		data, size, modified, err := readFileChunk(request.Path, request.Offset, request.Length)
		if err != nil {
			return fail(err)
		}
		response.Data, response.Size, response.Modified = data, size, modified
	case "launchers":
		response.Launchers = installedLaunchers()
	case "session-cwd":
		cwd, err := sessionProcessWorkingDirectory(request.Name)
		if err != nil {
			return fail(err)
		}
		response.Directory = cwd
	case "session-status":
		busy, clients, err := sessionRuntimeStatus(request.Name)
		if err != nil {
			return fail(err)
		}
		response.Busy = busy
		response.Clients = clients
	default:
		return fail(fmt.Errorf("unknown operation %q", request.Op))
	}
	return response
}

// protocolMismatchError means the two ends cannot talk at all.
type protocolMismatchError struct {
	remote        int
	remoteVersion string
}

func (e *protocolMismatchError) Error() string {
	if e.remote < remoteProtocolVersion {
		return fmt.Sprintf("the remote lam (protocol %d) is older than this one (%d): update lam on the remote host with `lam update`", e.remote, remoteProtocolVersion)
	}
	return fmt.Sprintf("the remote lam (protocol %d) is newer than this one (%d): run `lam update` here", e.remote, remoteProtocolVersion)
}

// startBuiltinRemoteRPC is startRemoteRPC over the in-process SSH client. An
// unattended connection never asks anything, so options must not carry
// prompts then.
func startBuiltinRemoteRPC(host string, unattended bool, opts SSHOptions) (*muxClient, error) {
	if unattended && opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 10 * time.Second
	}
	stream, err := DialSSH(host, opts)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(stream)
	client, err := muxHandshake(pipeRWC{Reader: reader, WriteCloser: stream}, reader, nil)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, stream.Stderr.suffix())
	}
	return client, nil
}

// SSHOptionsForCLI is what `lam ssh --builtin` uses: questions on the terminal
// when attended, none otherwise.
func SSHOptionsForCLI(unattended bool) SSHOptions {
	if unattended {
		return SSHOptions{}
	}
	return ConsoleSSHOptions()
}
