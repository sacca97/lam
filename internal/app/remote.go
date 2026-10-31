package app

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	remoteProtocolVersion = 3
	defaultRemoteTCPPort  = "42000"
	remoteTicketTTL       = 30 * time.Second
	remoteTicketDomain    = "LAM-REMOTE-TCP-1"
)

func normalizeRemoteTCPAddress(address string) (string, error) {
	return normalizeRemoteAddress(address, false)
}

func normalizeRemoteListenAddress(address string) (string, error) {
	if strings.TrimSpace(address) == "" {
		address = ":" + defaultRemoteTCPPort
	}
	return normalizeRemoteAddress(address, true)
}

func remoteListenPort(address string) (string, bool) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", false
	}
	if host == "" {
		return port, true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return port, true
	}
	return port, false
}

func normalizeRemoteAddress(address string, allowUnspecified bool) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("remote TCP address is required")
	}
	host, port, err := net.SplitHostPort(address)
	if err == nil {
		if host == "" {
			if !allowUnspecified {
				return "", fmt.Errorf("remote TCP address must name a host or VPN address")
			}
		}
		if port == "" {
			port = defaultRemoteTCPPort
		}
		return joinRemoteTCPHostPort(host, port, allowUnspecified)
	}

	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		host = address[1 : len(address)-1]
		if !isRemoteTCPHostIP(host) {
			return "", fmt.Errorf("invalid remote TCP address %q", address)
		}
		return joinRemoteTCPHostPort(host, defaultRemoteTCPPort, allowUnspecified)
	}
	if isRemoteTCPHostIP(address) {
		return joinRemoteTCPHostPort(address, defaultRemoteTCPPort, allowUnspecified)
	}
	if strings.Contains(address, ":") {
		return "", fmt.Errorf("invalid remote TCP address %q; use host or host:port", address)
	}
	return joinRemoteTCPHostPort(address, defaultRemoteTCPPort, allowUnspecified)
}

func isRemoteTCPHostIP(host string) bool {
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return net.ParseIP(host) != nil
}

func joinRemoteTCPHostPort(host, port string, allowUnspecified bool) (string, error) {
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid port %q: use a number from 1 to 65535", port)
	}
	ipHost := host
	if zone := strings.LastIndexByte(ipHost, '%'); zone >= 0 {
		ipHost = ipHost[:zone]
	}
	if ip := net.ParseIP(ipHost); ip != nil && ip.IsUnspecified() && !allowUnspecified {
		return "", fmt.Errorf("remote TCP address must name a reachable host or VPN interface, not %q", host)
	}
	return net.JoinHostPort(host, port), nil
}

func parseRemoteTCPTarget(target string) (sshHost, address string, err error) {
	address, err = normalizeRemoteTCPAddress(target)
	if err != nil {
		return "", "", err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("invalid remote TCP address %q", target)
	}
	sshHost = host
	tcpHost := host
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		if strings.Count(host, "@") != 1 {
			return "", "", fmt.Errorf("invalid SSH target %q", host)
		}
		user := host[:at]
		tcpHost = host[at+1:]
		if user == "" || tcpHost == "" {
			return "", "", fmt.Errorf("invalid SSH target %q", host)
		}
	}
	if strings.HasPrefix(sshHost, "-") || strings.ContainsAny(sshHost, " \t\r\n") || strings.HasPrefix(tcpHost, "-") {
		return "", "", fmt.Errorf("invalid SSH target %q", sshHost)
	}
	return sshHost, net.JoinHostPort(tcpHost, port), nil
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

// startTCPRemoteRPC connects to the host's TCP listener and talks to it. The
// ticket authenticates this one connection, which then carries everything.
func startTCPRemoteRPC(address, ticket string) (*muxClient, error) {
	address, err := normalizeRemoteTCPAddress(address)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(ticket) == "" {
		return nil, fmt.Errorf("remote authentication ticket is empty")
	}
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to remote %s: %w", address, err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeAll(conn, []byte(ticket+"\n")); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send remote authentication ticket: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return muxHandshake(conn, bufio.NewReader(conn), nil)
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
			line, err := readRemoteTCPLine(reader, maxFramePayload)
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
		line, err := readRemoteTCPLine(reader, 4096)
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

// serveRemoteTCPConn authenticates one TCP client with its ticket, then serves it.
func serveRemoteTCPConn(conn net.Conn, key []byte, replays *remoteTicketReplayCache) error {
	reader := bufio.NewReader(conn)
	if err := authenticateRemoteTCPClient(reader, conn, key, replays); err != nil {
		return err
	}
	return serveRemoteHandshake(reader, conn, conn)
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

func newTCPSessionBackendWithTicket(address, sshHost, ticket string) (*tcpSessionBackend, error) {
	address, err := normalizeRemoteTCPAddress(address)
	if err != nil {
		return nil, err
	}
	client, err := startTCPRemoteRPC(address, ticket)
	if err != nil {
		return nil, err
	}
	b := &tcpSessionBackend{address: address, sshHost: sshHost, remoteSessionBackend: remoteSessionBackend{name: "tcp:" + address, client: client}}
	if sshHost != "" {
		// A ticket works once, so a new connection needs a new one from the host.
		b.supervise(func() (*muxClient, error) {
			ticket, err := issueRemoteTCPAuthTicketOverSSH(sshHost, true)
			if err != nil {
				return nil, err
			}
			return startTCPRemoteRPC(address, ticket)
		})
	}
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

func issueRemoteTCPAuthTicketOverSSH(sshHost string, unattended bool) (string, error) {
	if err := validateSSHHost(sshHost); err != nil {
		return "", err
	}
	cmd, tail, err := newSSHCommand(unattended, sshHost, remoteToolCommand("remote-ticket"))
	if err != nil {
		return "", err
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("request remote access ticket over SSH to %s: %w%s", sshHost, err, tail.suffix())
	}
	for _, line := range strings.Split(string(output), "\n") {
		candidate := strings.TrimSpace(line)
		parts := strings.Split(candidate, ".")
		if len(parts) != 2 {
			continue
		}
		payload, payloadErr := base64.RawURLEncoding.DecodeString(parts[0])
		signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[1])
		if payloadErr == nil && signatureErr == nil && len(payload) > 0 && len(signature) == sha256.Size {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("SSH host %s did not return a valid remote access ticket", sshHost)
}

func printRemoteTCPAuthTicket() error {
	key, err := ensureRemoteTicketKey()
	if err != nil {
		return fmt.Errorf("load remote ticket signing key: %w", err)
	}
	ticket, err := issueRemoteTCPAuthTicket(key)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, ticket)
	return err
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

type tcpSessionBackend struct {
	address string
	sshHost string
	remoteSessionBackend
}

func newTCPSessionBackend(target string) (*tcpSessionBackend, error) {
	sshHost, address, err := parseRemoteTCPTarget(target)
	if err != nil {
		return nil, err
	}
	ticket, err := issueRemoteTCPAuthTicketOverSSH(sshHost, false)
	if err != nil {
		return nil, err
	}
	return newTCPSessionBackendWithTicket(address, sshHost, ticket)
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
// says which is older and offers to update, as `lam ssh` and `lam remote` both
// do. host is the ssh host used to reach the remote machine.
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

func runTCPRemote(address string, promptMe bool) error {
	sshHost, _, err := parseRemoteTCPTarget(address)
	if err != nil {
		return err
	}
	backend, err := connectOfferingUpdate(sshHost, func() (*tcpSessionBackend, error) { return newTCPSessionBackend(address) },
		func(b *tcpSessionBackend) *remoteSessionBackend { return &b.remoteSessionBackend })
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

func serveRemoteTCPWithStop(address string, stop <-chan struct{}) error {
	address, err := normalizeRemoteListenAddress(address)
	if err != nil {
		return err
	}
	key, err := loadRemoteTicketKey()
	if err != nil {
		return fmt.Errorf("load remote ticket signing key: %w", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	defer listener.Close()
	listenerClosed := make(chan struct{})
	defer close(listenerClosed)
	if stop != nil {
		go func() {
			select {
			case <-stop:
				_ = listener.Close()
			case <-listenerClosed:
			}
		}()
	}
	fmt.Fprintf(os.Stderr, "LAM remote listening on %s (unencrypted TCP)\n", listener.Addr())
	replays := newRemoteTicketReplayCache()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if stop != nil {
				select {
				case <-stop:
					return nil
				default:
				}
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			time.Sleep(50 * time.Millisecond) // a transient failure such as EMFILE
			continue
		}
		go func(conn net.Conn) {
			defer conn.Close()
			_ = serveRemoteTCPConn(conn, key, replays)
		}(conn)
	}
}

type remoteTicketClaims struct {
	ExpiresAt int64  `json:"exp"`
	Nonce     string `json:"nonce"`
}

func issueRemoteTCPAuthTicket(key []byte) (string, error) {
	if len(key) != remoteTicketKeyBytes {
		return "", fmt.Errorf("invalid remote ticket signing key")
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate remote ticket nonce: %w", err)
	}
	claims := remoteTicketClaims{ExpiresAt: time.Now().Add(remoteTicketTTL).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce)}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode remote ticket: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(remoteTicketDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func validateRemoteTCPAuthTicket(ticket string, key []byte, now time.Time) (string, time.Time, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return "", time.Time{}, fmt.Errorf("malformed remote authentication ticket")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("malformed remote authentication ticket")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return "", time.Time{}, fmt.Errorf("malformed remote authentication ticket")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(remoteTicketDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	if subtle.ConstantTimeCompare(signature, mac.Sum(nil)) != 1 {
		return "", time.Time{}, fmt.Errorf("invalid remote authentication ticket")
	}
	var claims remoteTicketClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", time.Time{}, fmt.Errorf("malformed remote authentication ticket")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(claims.Nonce)
	if err != nil || len(nonce) != 24 {
		return "", time.Time{}, fmt.Errorf("malformed remote authentication ticket")
	}
	expires := time.Unix(claims.ExpiresAt, 0)
	if !expires.After(now) || expires.After(now.Add(time.Minute)) {
		return "", time.Time{}, fmt.Errorf("remote authentication ticket is expired or has an invalid lifetime")
	}
	return claims.Nonce, expires, nil
}

type remoteTicketReplayCache struct {
	mu   sync.Mutex
	used map[string]time.Time
}

func newRemoteTicketReplayCache() *remoteTicketReplayCache {
	return &remoteTicketReplayCache{used: make(map[string]time.Time)}
}

func (c *remoteTicketReplayCache) consume(nonce string, expires time.Time, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for usedNonce, usedUntil := range c.used {
		if !usedUntil.After(now) {
			delete(c.used, usedNonce)
		}
	}
	if _, exists := c.used[nonce]; exists {
		return false
	}
	c.used[nonce] = expires
	return true
}

func authenticateRemoteTCPClient(reader *bufio.Reader, conn net.Conn, key []byte, replays *remoteTicketReplayCache) error {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := readRemoteTCPLine(reader, 4096)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("read authentication ticket: %w", err)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return fmt.Errorf("malformed remote authentication ticket")
	}
	nonce, expires, err := validateRemoteTCPAuthTicket(strings.TrimSpace(string(line)), key, time.Now())
	if err != nil {
		return err
	}
	if !replays.consume(nonce, expires, time.Now()) {
		return fmt.Errorf("remote authentication ticket has already been used")
	}
	return nil
}

func readRemoteTCPLine(reader *bufio.Reader, limit int) ([]byte, error) {
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
