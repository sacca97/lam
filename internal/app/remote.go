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

func startRemoteRPC(host string, unattended bool) (*muxClient, error) {
	if err := validateSSHHost(host); err != nil {
		return nil, err
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
	args := []string{"-T", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
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
		ask := func(request remoteRequest) (remoteResponse, error) {
			data, _ := json.Marshal(request)
			if err := writeAll(rw, append(data, '\n')); err != nil {
				return remoteResponse{}, err
			}
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
		response, err := ask(remoteRequest{ID: 1, Op: "ping"})
		if err == nil && response.Protocol != remoteProtocolVersion {
			err = &protocolMismatchError{remote: response.Protocol, remoteVersion: response.Version}
		}
		if err == nil {
			_, err = ask(remoteRequest{ID: 2, Op: "mux"})
		}
		if err != nil {
			result <- outcome{err: err}
			return
		}
		result <- outcome{client: newMuxClient(rw, reader, func() {
			if cleanup != nil {
				cleanupOnce.Do(cleanup)
			}
		})}
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
	client, err := startRemoteRPC(host, false)
	if err != nil {
		return nil, err
	}
	b := &sshSessionBackend{host: host, remoteSessionBackend: remoteSessionBackend{name: "ssh:" + host, client: client}}
	b.supervise(func() (*muxClient, error) { return startRemoteRPC(host, true) })
	return b, nil
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

func (b *remoteSessionBackend) cur() *muxClient {
	b.linkMu.Lock()
	defer b.linkMu.Unlock()
	return b.client
}

func (b *remoteSessionBackend) LinkStatus() linkStatus {
	b.linkMu.Lock()
	defer b.linkMu.Unlock()
	return linkStatus{Up: b.attempt == 0, Gen: b.gen, Attempt: b.attempt, RetryAt: b.retryAt, Err: b.lastErr}
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
	if resp, pingErr := remote(backend).callWithTimeout(remoteRequest{Op: "ping"}, 5*time.Second); pingErr == nil {
		if order, ok := compareRelease(version, resp.Version); ok && order != 0 {
			updated, local := offerUpdate(host, resp.Version, order > 0)
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
	return response.Sessions, err
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
		response, err := b.cur().call(request)
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
		cwd, err := os.Getwd()
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
