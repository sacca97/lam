package app

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// The built-in SSH client: an in-process alternative to the system ssh binary
// (which Android does not have). It gives the mux layer the same thing the ssh
// process gave it, the stdio of `lam remote` on the host.

// SSHPaths says where the client finds its files. Empty fields default to the
// usual places under ~/.ssh; an app with its own key and known_hosts (Android)
// sets them to files in its private directory.
type SSHPaths struct {
	ConfigPath     string // ssh_config; default ~/.ssh/config
	KnownHostsPath string // default ~/.ssh/known_hosts
	// IdentityFiles are the private keys to try after the ssh-agent. Nil means
	// ~/.ssh/id_ed25519, id_ecdsa and id_rsa; an empty non-nil slice means none.
	IdentityFiles []string
	DisableConfig bool // do not read an ssh_config at all
}

func sshHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func (p SSHPaths) config() string {
	if p.DisableConfig {
		return ""
	}
	if p.ConfigPath != "" {
		return p.ConfigPath
	}
	if home := sshHomeDir(); home != "" {
		return filepath.Join(home, ".ssh", "config")
	}
	return ""
}

func (p SSHPaths) knownHosts() (string, error) {
	if p.KnownHostsPath != "" {
		return p.KnownHostsPath, nil
	}
	if home := sshHomeDir(); home != "" {
		return filepath.Join(home, ".ssh", "known_hosts"), nil
	}
	return "", errors.New("no known_hosts path: HOME is not set")
}

func (p SSHPaths) identities() []string {
	if p.IdentityFiles != nil {
		return p.IdentityFiles
	}
	home := sshHomeDir()
	if home == "" {
		return nil
	}
	var out []string
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		out = append(out, filepath.Join(home, ".ssh", name))
	}
	return out
}

// SSHOptions configures DialSSH. The zero value is usable.
type SSHOptions struct {
	Paths SSHPaths
	// ConfirmHost is asked about a host that is not in known_hosts. Returning
	// true accepts the key and appends it to known_hosts. Nil rejects unknown
	// hosts. A key that differs from the recorded one is never offered here.
	ConfirmHost func(host string, key ssh.PublicKey) (bool, error)
	// Passphrase is asked for the passphrase of an encrypted private key. Nil
	// means encrypted keys are skipped with an explanatory error.
	Passphrase func(keyPath string) ([]byte, error)
	// Password is asked for the account password, after the agent and keys
	// failed, for both password and keyboard-interactive login. It is called at
	// most once per connection and the answer is not kept. Nil means no
	// password login.
	Password func(user, host string) (string, error)

	AgentSocket  string // default $SSH_AUTH_SOCK
	DisableAgent bool

	ConnectTimeout    time.Duration // default 15s
	KeepaliveInterval time.Duration // default 15s
	KeepaliveMisses   int           // default 3

	// Dial replaces the TCP dial (tests, proxies).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// SSHStream is the stdio of the remote command and the connection under it.
type SSHStream struct {
	io.Reader             // remote stdout
	Stderr    *stderrTail // the end of remote stderr
	stdin     io.WriteCloser
	client    *ssh.Client
	session   *ssh.Session
	once      sync.Once
	stopKeep  chan struct{}
}

func (s *SSHStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Close ends the command: it closes its input, which makes `lam remote` exit,
// and drops the connection after a short grace period.
func (s *SSHStream) Close() error {
	s.once.Do(func() {
		close(s.stopKeep)
		_ = s.stdin.Close()
		go func() {
			done := make(chan struct{})
			go func() { _ = s.session.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
			_ = s.session.Close()
			_ = s.client.Close()
		}()
	})
	return nil
}

// sshTarget is a host with everything resolved.
type sshTarget struct {
	user, hostname, port string
	identities           []string
}

func (t sshTarget) addr() string { return net.JoinHostPort(t.hostname, t.port) }

// parseSSHHost splits user@host, host:port, user@host:port and [v6]:port.
func parseSSHHost(spec string) (user, host, port string, err error) {
	if err := validateSSHHost(spec); err != nil {
		return "", "", "", err
	}
	host = spec
	if i := strings.LastIndex(host, "@"); i >= 0 {
		user, host = host[:i], host[i+1:]
	}
	if strings.HasPrefix(host, "[") {
		h, p, splitErr := net.SplitHostPort(host)
		if splitErr != nil {
			h = strings.Trim(host, "[]")
		} else {
			port = p
		}
		host = h
	} else if strings.Count(host, ":") == 1 {
		h, p, _ := strings.Cut(host, ":")
		host, port = h, p
	}
	if host == "" {
		return "", "", "", fmt.Errorf("invalid SSH host %q", spec)
	}
	return user, host, port, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home := sshHomeDir(); home != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// resolveSSHTarget applies ~/.ssh/config to spec. What the spec states
// (user@, :port) wins over the file, as with ssh.
func resolveSSHTarget(spec string, paths SSHPaths) (sshTarget, error) {
	u, host, port, err := parseSSHHost(spec)
	if err != nil {
		return sshTarget{}, err
	}
	t := sshTarget{user: u, hostname: host, port: port}
	if path := paths.config(); path != "" {
		if f, err := os.Open(path); err == nil {
			cfg, decodeErr := ssh_config.Decode(f)
			f.Close()
			if decodeErr == nil {
				if v, _ := cfg.Get(host, "HostName"); v != "" {
					t.hostname = v
				}
				if t.user == "" {
					t.user, _ = cfg.Get(host, "User")
				}
				if t.port == "" {
					t.port, _ = cfg.Get(host, "Port")
				}
				for _, opt := range []string{"ProxyJump", "ProxyCommand"} {
					if v, _ := cfg.Get(host, opt); v != "" && !strings.EqualFold(v, "none") {
						return sshTarget{}, fmt.Errorf("%s in ~/.ssh/config for %s is not supported by lam's own SSH client: use `lam ssh %s` in a terminal", opt, host, host)
					}
				}
				ids, _ := cfg.GetAll(host, "IdentityFile")
				for _, id := range ids {
					t.identities = append(t.identities, expandHome(id))
				}
			}
		}
	}
	if t.port == "" {
		t.port = "22"
	}
	if t.user == "" {
		if cur, err := user.Current(); err == nil && cur.Username != "" {
			t.user = cur.Username
		} else if env := os.Getenv("USER"); env != "" {
			t.user = env
		} else {
			return sshTarget{}, fmt.Errorf("no user for %q: write user@%s", spec, host)
		}
	}
	seen := map[string]bool{}
	var all []string
	for _, id := range append(t.identities, paths.identities()...) {
		if id != "" && !seen[id] {
			seen[id] = true
			all = append(all, id)
		}
	}
	t.identities = all
	return t, nil
}

var sshKnownHostsMu sync.Mutex

// hostKeyChecker verifies against known_hosts, asks about unknown hosts and
// refuses changed keys.
func hostKeyChecker(path string, confirm func(string, ssh.PublicKey) (bool, error)) (ssh.HostKeyCallback, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	check, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	callback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err // revoked, malformed...
		}
		if len(keyErr.Want) > 0 {
			// The host is known by a key of another type: ask for that type
			// instead (see dialSSHCommand) rather than calling it a change.
			var algs []string
			same := false
			for _, w := range keyErr.Want {
				same = same || w.Key.Type() == key.Type()
				algs = append(algs, hostKeyAlgorithms(w.Key.Type())...)
			}
			if !same {
				return &hostKeyTypeError{algs: algs}
			}
			return fmt.Errorf("REMOTE HOST IDENTIFICATION HAS CHANGED for %s: the key (%s) does not match %s line %d; refusing to connect",
				hostname, ssh.FingerprintSHA256(key), keyErr.Want[0].Filename, keyErr.Want[0].Line)
		}
		if confirm == nil {
			return fmt.Errorf("host %s is not in known_hosts (%s %s) and cannot be confirmed here", hostname, key.Type(), ssh.FingerprintSHA256(key))
		}
		ok, confirmErr := confirm(hostname, key)
		if confirmErr != nil {
			return confirmErr
		}
		if !ok {
			return fmt.Errorf("host key for %s was not accepted", hostname)
		}
		sshKnownHostsMu.Lock()
		defer sshKnownHostsMu.Unlock()
		out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		defer out.Close()
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		if info, serr := out.Stat(); serr == nil && info.Size() > 0 {
			if last, rerr := os.ReadFile(path); rerr == nil && len(last) > 0 && last[len(last)-1] != '\n' {
				line = "\n" + line // the file did not end with a newline
			}
		}
		_, err = fmt.Fprintln(out, line)
		return err
	}
	return callback, nil
}

// hostKeyTypeError means the server offered a host key type known_hosts has
// nothing for, while it has another type for the host.
type hostKeyTypeError struct{ algs []string }

func (e *hostKeyTypeError) Error() string {
	return "host key type differs from the one in known_hosts"
}

func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// authMethods builds the agent method first, then key files.
func authMethods(t sshTarget, opts SSHOptions, skipped *[]string) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	closeAgent := func() {}
	if !opts.DisableAgent {
		sock := opts.AgentSocket
		if sock == "" {
			sock = os.Getenv("SSH_AUTH_SOCK")
		}
		if sock != "" {
			if conn, err := net.DialTimeout("unix", sock, 2*time.Second); err == nil {
				closeAgent = func() { _ = conn.Close() }
				methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
			}
		}
	}
	// Key files are opened only when authentication reaches them, so a key
	// with a passphrase is asked about only if the agent could not log in.
	var readable []string
	for _, path := range t.identities {
		if _, err := os.Stat(path); err == nil {
			readable = append(readable, path)
		}
	}
	if len(readable) > 0 {
		methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			var signers []ssh.Signer
			for _, path := range readable {
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				signer, err := ssh.ParsePrivateKey(data)
				var missing *ssh.PassphraseMissingError
				if errors.As(err, &missing) {
					if opts.Passphrase == nil {
						*skipped = append(*skipped, path)
						continue // an encrypted key and nobody to ask
					}
					pass, perr := opts.Passphrase(path)
					if perr != nil {
						return nil, perr
					}
					signer, err = ssh.ParsePrivateKeyWithPassphrase(data, pass)
					if err != nil {
						// A wrong passphrase must not end the login: the
						// agent, another key or the password may still work.
						*skipped = append(*skipped, path+" (wrong passphrase)")
						continue
					}
				} else if err != nil {
					*skipped = append(*skipped, path+" ("+err.Error()+")")
					continue // not a key we can read
				}
				signers = append(signers, signer)
			}
			return signers, nil
		}))
	}
	if opts.Password != nil {
		// Password and keyboard-interactive share one question.
		var once sync.Once
		var pass string
		var passErr error
		ask := func() (string, error) {
			once.Do(func() { pass, passErr = opts.Password(t.user, t.hostname) })
			return pass, passErr
		}
		methods = append(methods,
			ssh.PasswordCallback(ask),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					if i < len(echos) && echos[i] {
						continue // not a password prompt
					}
					if !strings.Contains(strings.ToLower(questions[i]), "password") {
						return nil, fmt.Errorf("the server asks %q, which lam cannot answer (a one-time code?)", strings.TrimSpace(questions[i]))
					}
					p, err := ask()
					if err != nil {
						return nil, err
					}
					answers[i] = p
				}
				return answers, nil
			}))
	}
	if len(methods) == 0 {
		return nil, closeAgent, errors.New("no SSH credentials: no ssh-agent and no private key in ~/.ssh")
	}
	return methods, closeAgent, nil
}

// DialSSH connects to host (user@host, host:port, user@host:port or an
// ~/.ssh/config alias), authenticates and runs `lam remote` there.
func DialSSH(host string, opts SSHOptions) (*SSHStream, error) {
	return dialSSHCommand(host, remoteToolCommand("remote"), opts)
}

func dialSSHCommand(host, command string, opts SSHOptions) (*SSHStream, error) {
	target, err := resolveSSHTarget(host, opts.Paths)
	if err != nil {
		return nil, err
	}
	knownPath, err := opts.Paths.knownHosts()
	if err != nil {
		return nil, err
	}
	timeout := opts.ConnectTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	gate := &deadlineGate{timeout: timeout}
	if f := opts.ConfirmHost; f != nil {
		opts.ConfirmHost = func(h string, k ssh.PublicKey) (bool, error) {
			defer gate.pause()()
			return f(h, k)
		}
	}
	if f := opts.Passphrase; f != nil {
		opts.Passphrase = func(p string) ([]byte, error) {
			defer gate.pause()()
			return f(p)
		}
	}
	if f := opts.Password; f != nil {
		opts.Password = func(u, h string) (string, error) {
			defer gate.pause()()
			return f(u, h)
		}
	}
	callback, err := hostKeyChecker(knownPath, opts.ConfirmHost)
	if err != nil {
		return nil, err
	}
	var skipped []string
	methods, closeAgent, err := authMethods(target, opts, &skipped)
	// The agent is only needed while authenticating.
	defer closeAgent()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            target.user,
		Auth:            methods,
		HostKeyCallback: callback,
		Timeout:         timeout,
	}
	var client *ssh.Client
	for attempt := 0; ; attempt++ {
		client, err = connectSSH(target, cfg, opts, timeout, gate)
		var typeErr *hostKeyTypeError
		if errors.As(err, &typeErr) && attempt == 0 {
			// Retry once, offering only the key types known_hosts has.
			cfg.HostKeyAlgorithms = typeErr.algs
			continue
		}
		if err != nil {
			if len(skipped) > 0 {
				err = fmt.Errorf("%w; skipped passphrase-protected or unreadable keys: %s", err, strings.Join(skipped, ", "))
			}
			return nil, err
		}
		break
	}

	// The link is watched from the moment it is up, not only once the remote
	// command runs: a server that stalls after login must not hang us.
	stopKeep := make(chan struct{})
	go keepalive(client, opts.KeepaliveInterval, opts.KeepaliveMisses, stopKeep)
	fail := func(err error) (*SSHStream, error) { close(stopKeep); client.Close(); return nil, err }
	session, err := client.NewSession()
	if err != nil {
		return fail(fmt.Errorf("open SSH session: %w", err))
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return fail(err)
	}
	tail := &stderrTail{}
	go func() { _, _ = io.Copy(tail, stderr) }()
	if err := session.Start(command); err != nil {
		return fail(fmt.Errorf("start remote command: %w", err))
	}
	s := &SSHStream{Reader: stdout, Stderr: tail, stdin: stdin, client: client, session: session, stopKeep: stopKeep}
	return s, nil
}

// keepalive closes the connection when the peer stops answering, so readers
// see an error instead of waiting on a dead link forever.
func keepalive(client *ssh.Client, interval time.Duration, misses int, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if misses <= 0 {
		misses = 3
	}
	missed := 0
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		answered := make(chan struct{})
		go func() {
			// A reply of either kind proves the peer is alive.
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err == nil {
				close(answered)
			}
		}()
		select {
		case <-answered:
			missed = 0
		case <-time.After(interval):
			missed++
			if missed >= misses {
				_ = client.Close()
				return
			}
		case <-stop:
			return
		}
	}
}

// GenerateSSHKey writes a new ed25519 key pair in OpenSSH format (private key
// at path with mode 0600, public key at path+".pub") and returns the line to
// add to the host's authorized_keys. It refuses to overwrite an existing key.
func GenerateSSHKey(path, comment string) (string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create key: %w", err)
	}
	if _, err := f.Write(pem.EncodeToMemory(block)); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	return line, nil
}

// ConsoleSSHOptions asks on the terminal about unknown hosts and passphrases.
// Without a terminal it asks nothing, so unknown hosts fail.
func ConsoleSSHOptions() SSHOptions {
	var opts SSHOptions
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return opts
	}
	opts.ConfirmHost = func(host string, key ssh.PublicKey) (bool, error) {
		fmt.Fprintf(os.Stderr, "The authenticity of host %q cannot be established.\n%s key fingerprint is %s.\nContinue connecting (yes/no)? ",
			host, key.Type(), ssh.FingerprintSHA256(key))
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
	}
	opts.Passphrase = func(path string) ([]byte, error) {
		fmt.Fprintf(os.Stderr, "Enter passphrase for key %s: ", path)
		pass, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return pass, err
	}
	opts.Password = func(user, host string) (string, error) {
		fmt.Fprintf(os.Stderr, "%s@%s's password: ", user, host)
		pass, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(pass), err
	}
	return opts
}

// deadlineGate lets the handshake deadline stand aside while a person answers
// a question (host key, passphrase, password): thinking is not a timeout.
type deadlineGate struct {
	mu      sync.Mutex
	conn    net.Conn
	timeout time.Duration
}

func (g *deadlineGate) attach(c net.Conn) {
	g.mu.Lock()
	g.conn = c
	g.mu.Unlock()
}

// pause lifts the deadline and returns the function that puts it back.
func (g *deadlineGate) pause() func() {
	g.mu.Lock()
	if g.conn != nil {
		_ = g.conn.SetDeadline(time.Time{})
	}
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		if g.conn != nil {
			_ = g.conn.SetDeadline(time.Now().Add(g.timeout))
		}
		g.mu.Unlock()
	}
}

func connectSSH(target sshTarget, cfg *ssh.ClientConfig, opts SSHOptions, timeout time.Duration, gate *deadlineGate) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	dial := opts.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", target.addr())
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", target.addr(), err)
	}
	// ClientConfig.Timeout only covers dialing; bound the handshake too. The
	// deadline stays out of the way afterwards, keepalives watch the link.
	_ = conn.SetDeadline(time.Now().Add(timeout))
	gate.attach(conn)
	sconn, chans, reqs, err := ssh.NewClientConn(conn, target.addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh %s@%s: %w", target.user, target.addr(), err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sconn, chans, reqs), nil
}
