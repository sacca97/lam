package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

func newTestSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, priv
}

type testSSHServer struct {
	addr    string
	hostKey ssh.Signer
	closed  atomic.Bool
}

// startTestSSHServer runs an SSH server that accepts only authorized and runs
// serveRemote in-process for every exec request.
func startTestSSHServer(t *testing.T, hostKey ssh.Signer, authorized ...ssh.PublicKey) *testSSHServer {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, a := range authorized {
				if string(a.Marshal()) == string(key.Marshal()) {
					return nil, nil
				}
			}
			return nil, os.ErrPermission
		},
	}
	return startTestSSHServerConfig(t, hostKey, cfg)
}

func startTestSSHServerConfig(t *testing.T, hostKey ssh.Signer, cfg *ssh.ServerConfig) *testSSHServer {
	t.Helper()
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &testSSHServer{addr: ln.Addr().String(), hostKey: hostKey}
	t.Cleanup(func() { srv.closed.Store(true); ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn, cfg)
		}
	}()
	return srv
}

func (s *testSSHServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go func() {
		for r := range reqs {
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			for r := range chReqs {
				if r.Type != "exec" {
					_ = r.Reply(false, nil)
					continue
				}
				_ = r.Reply(true, nil)
				go func() {
					_ = serveRemote(ch, ch)
					ch.Close()
				}()
			}
		}()
	}
	_ = sconn.Close()
}

func useTestBackend(t *testing.T) *remoteTestBackend {
	t.Helper()
	previous := selectedBackend
	backend := &remoteTestBackend{created: make(chan sessionInfo, 4)}
	setBackend(backend)
	t.Cleanup(func() { setBackend(previous) })
	return backend
}

// sshTestEnv is a temp HOME with a client key, ssh_config and known_hosts.
type sshTestEnv struct {
	home    string
	paths   SSHPaths
	signer  ssh.Signer
	known   string
	keyPath string
}

func newSSHTestEnv(t *testing.T) *sshTestEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	line, err := GenerateSSHKey(keyPath, "test")
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	data, _ := os.ReadFile(keyPath)
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	return &sshTestEnv{home: home, signer: signer, known: filepath.Join(home, ".ssh", "known_hosts"), keyPath: keyPath}
}

func acceptAll(string, ssh.PublicKey) (bool, error) { return true, nil }

func TestBuiltinSSHRemoteOperations(t *testing.T) {
	backend := useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())

	// User, port and key come from the defaults and the spec.
	host := "tester@" + srv.addr
	var asked atomic.Int32
	opts := SSHOptions{DisableAgent: true, ConfirmHost: func(h string, k ssh.PublicKey) (bool, error) {
		asked.Add(1)
		if ssh.FingerprintSHA256(k) != ssh.FingerprintSHA256(hostKey.PublicKey()) {
			t.Errorf("wrong key offered")
		}
		return true, nil
	}}
	client, err := startBuiltinRemoteRPC(host, false, opts)
	if err != nil {
		t.Fatal(err)
	}
	rb := &remoteSessionBackend{name: "ssh:test", client: client}
	if err := rb.Create("p", "/x", "claude"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-backend.created:
		if got.Name != "p" || got.CWD != "/x" {
			t.Fatalf("created %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("create did not arrive")
	}
	if _, err := rb.List(); err != nil {
		t.Fatal(err)
	}
	client.close()
	if asked.Load() != 1 {
		t.Fatalf("host asked %d times", asked.Load())
	}

	// The accepted key is in known_hosts: a second connection asks nothing.
	data, err := os.ReadFile(env.known)
	if err != nil || !strings.Contains(string(data), "ssh-ed25519") {
		t.Fatalf("known_hosts = %q, %v", data, err)
	}
	client, err = startBuiltinRemoteRPC(host, true, SSHOptions{DisableAgent: true})
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	client.close()
}

func TestBuiltinSSHUnknownHostRejected(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())
	for name, confirm := range map[string]func(string, ssh.PublicKey) (bool, error){
		"nil callback": nil,
		"declined":     func(string, ssh.PublicKey) (bool, error) { return false, nil },
	} {
		_, err := DialSSH("u@"+srv.addr, SSHOptions{DisableAgent: true, ConfirmHost: confirm})
		if err == nil {
			t.Fatalf("%s: connected to an unknown host", name)
		}
	}
	if data, _ := os.ReadFile(env.known); len(data) != 0 {
		t.Fatalf("known_hosts written without consent: %q", data)
	}
}

func TestBuiltinSSHChangedHostKeyFails(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())
	stream, err := DialSSH("u@"+srv.addr, SSHOptions{DisableAgent: true, ConfirmHost: acceptAll})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()

	// A different server key on the same address.
	other, _ := newTestSigner(t)
	srv2 := startTestSSHServer(t, other, env.signer.PublicKey())
	// known_hosts remembers the old key for the new server's address.
	changed := knownhosts.Line([]string{knownhosts.Normalize(srv2.addr)}, hostKey.PublicKey()) + "\n"
	if err := os.WriteFile(env.known, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = DialSSH("u@"+srv2.addr, SSHOptions{DisableAgent: true, ConfirmHost: func(string, ssh.PublicKey) (bool, error) {
		called = true
		return true, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "HAS CHANGED") {
		t.Fatalf("err = %v, want host key change failure", err)
	}
	if called {
		t.Fatal("a changed key was offered to the callback")
	}
}

func TestBuiltinSSHAuthWithAgent(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	// Remove the key file so only the agent can authenticate.
	os.Remove(env.keyPath)
	agentSigner, agentPriv := newTestSigner(t)
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: agentPriv}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(shortTempDir(t), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, c); c.Close() }()
		}
	}()
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, agentSigner.PublicKey())
	stream, err := DialSSH("u@"+srv.addr, SSHOptions{AgentSocket: sock, ConfirmHost: acceptAll})
	if err != nil {
		t.Fatalf("agent auth: %v", err)
	}
	stream.Close()

	// Without the agent there is nothing to authenticate with.
	if _, err := DialSSH("u@"+srv.addr, SSHOptions{DisableAgent: true, ConfirmHost: acceptAll}); err == nil {
		t.Fatal("connected with no credentials")
	}
}

func TestBuiltinSSHAuthWithKeyFiles(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())

	// A custom key location, as an Android app would use.
	appKey := filepath.Join(t.TempDir(), "app_key")
	line, err := GenerateSSHKey(appKey, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") || !strings.HasSuffix(line, " app") {
		t.Fatalf("authorized_keys line = %q", line)
	}
	if info, err := os.Stat(appKey); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode: %v %v", info, err)
	}
	if _, err := GenerateSSHKey(appKey, "app"); err == nil {
		t.Fatal("overwrote an existing key")
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	srv2 := startTestSSHServer(t, hostKey, authorized)
	known := filepath.Join(t.TempDir(), "kh")
	paths := SSHPaths{KnownHostsPath: known, IdentityFiles: []string{appKey}, DisableConfig: true}
	stream, err := DialSSH("u@"+srv2.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll})
	if err != nil {
		t.Fatalf("custom key: %v", err)
	}
	stream.Close()
	// The home key is not tried when identities are given, so srv rejects it.
	if _, err := DialSSH("u@"+srv.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll}); err == nil {
		t.Fatal("authenticated with an unauthorized key")
	}
	// The default key in the temp HOME works.
	stream, err = DialSSH("u@"+srv.addr, SSHOptions{DisableAgent: true, ConfirmHost: acceptAll})
	if err != nil {
		t.Fatalf("default key: %v", err)
	}
	stream.Close()
}

func TestBuiltinSSHEncryptedKey(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	_, priv := newTestSigner(t)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "enc", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	enc := filepath.Join(t.TempDir(), "enc")
	if err := os.WriteFile(enc, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(priv)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, signer.PublicKey())
	paths := SSHPaths{IdentityFiles: []string{enc}, DisableConfig: true}
	_ = env
	_, err = DialSSH("u@"+srv.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("err = %v, want a passphrase explanation", err)
	}
	stream, err := DialSSH("u@"+srv.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll,
		Passphrase: func(string) ([]byte, error) { return []byte("secret"), nil }})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
}

func TestParseSSHHost(t *testing.T) {
	cases := []struct{ in, user, host, port string }{
		{"host", "", "host", ""},
		{"me@host", "me", "host", ""},
		{"host:2222", "", "host", "2222"},
		{"me@host:2222", "me", "host", "2222"},
		{"me@[::1]:2200", "me", "::1", "2200"},
		{"::1", "", "::1", ""},
	}
	for _, c := range cases {
		u, h, p, err := parseSSHHost(c.in)
		if err != nil || u != c.user || h != c.host || p != c.port {
			t.Errorf("parseSSHHost(%q) = %q %q %q %v", c.in, u, h, p, err)
		}
	}
	if _, _, _, err := parseSSHHost("-oFoo=bar"); err == nil {
		t.Error("accepted an option as host")
	}
}

func TestResolveSSHTargetFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `Host work
  HostName work.example.com
  User alice
  Port 2022
  IdentityFile ~/.ssh/work_key

Host *
  User fallback
`
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveSSHTarget("work", SSHPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if got.hostname != "work.example.com" || got.user != "alice" || got.port != "2022" {
		t.Fatalf("resolved %+v", got)
	}
	if len(got.identities) == 0 || got.identities[0] != filepath.Join(home, ".ssh", "work_key") {
		t.Fatalf("identities = %v", got.identities)
	}
	if !strings.HasSuffix(got.identities[len(got.identities)-1], "id_rsa") {
		t.Fatalf("default identities missing: %v", got.identities)
	}
	// The spec wins over the file.
	got, _ = resolveSSHTarget("bob@work:99", SSHPaths{})
	if got.user != "bob" || got.port != "99" || got.hostname != "work.example.com" {
		t.Fatalf("resolved %+v", got)
	}
	// A host the file does not mention gets the wildcard user and port 22.
	got, _ = resolveSSHTarget("other", SSHPaths{})
	if got.user != "fallback" || got.port != "22" || got.hostname != "other" {
		t.Fatalf("resolved %+v", got)
	}
	// DisableConfig ignores the file.
	got, _ = resolveSSHTarget("work", SSHPaths{DisableConfig: true, IdentityFiles: []string{}})
	if got.hostname != "work" || len(got.identities) != 0 {
		t.Fatalf("resolved %+v", got)
	}
}

// blackholeConn can be told to swallow everything the peer sends.
type blackholeConn struct {
	net.Conn
	mu   sync.Mutex
	dead bool
}

func (c *blackholeConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		c.mu.Lock()
		dead := c.dead
		c.mu.Unlock()
		if err != nil || !dead {
			return n, err
		}
	}
}

func TestBuiltinSSHKeepaliveDetectsDeadLink(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())
	var bh *blackholeConn
	opts := SSHOptions{
		DisableAgent: true, ConfirmHost: acceptAll,
		KeepaliveInterval: 50 * time.Millisecond, KeepaliveMisses: 2,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			bh = &blackholeConn{Conn: c}
			return bh, err
		},
	}
	client, err := startBuiltinRemoteRPC("u@"+srv.addr, false, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	bh.mu.Lock()
	bh.dead = true
	bh.mu.Unlock()
	select {
	case <-client.done:
	case <-time.After(5 * time.Second):
		t.Fatal("dead link not detected")
	}
}

func TestBuiltinSSHConnectTimeout(t *testing.T) {
	env := newSSHTestEnv(t)
	_ = env
	// A listener that accepts and never speaks SSH.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	start := time.Now()
	_, err = DialSSH("u@"+ln.Addr().String(), SSHOptions{DisableAgent: true, ConfirmHost: acceptAll, ConnectTimeout: 300 * time.Millisecond})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestBuiltinSSHPasswordAuth(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		name := "password"
		if interactive {
			name = "keyboard-interactive"
		}
		t.Run(name, func(t *testing.T) {
			useTestBackend(t)
			newSSHTestEnv(t)
			cfg := &ssh.ServerConfig{}
			if interactive {
				cfg.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
					answers, err := challenge("", "", []string{"Password: "}, []bool{false})
					if err != nil {
						return nil, err
					}
					if len(answers) == 1 && answers[0] == "hunter2" {
						return nil, nil
					}
					return nil, os.ErrPermission
				}
			} else {
				cfg.PasswordCallback = func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
					if string(pass) == "hunter2" {
						return nil, nil
					}
					return nil, os.ErrPermission
				}
			}
			hostKey, _ := newTestSigner(t)
			srv := startTestSSHServerConfig(t, hostKey, cfg)
			paths := SSHPaths{IdentityFiles: []string{}, DisableConfig: true}

			asked := 0
			stream, err := DialSSH("u@"+srv.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll,
				Password: func(user, host string) (string, error) {
					asked++
					if user != "u" || host != "127.0.0.1" {
						t.Errorf("asked for %s@%s", user, host)
					}
					return "hunter2", nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			stream.Close()
			if asked != 1 {
				t.Fatalf("password asked %d times, want 1", asked)
			}

			asked = 0
			_, err = DialSSH("u@"+srv.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll,
				Password: func(string, string) (string, error) { asked++; return "wrong", nil }})
			if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
				t.Fatalf("wrong password: err = %v", err)
			}
			if asked != 1 {
				t.Fatalf("wrong password asked %d times, want 1", asked)
			}

			// Without a callback there is no password login at all.
			_, err = DialSSH("u@"+srv.addr, SSHOptions{Paths: paths, DisableAgent: true, ConfirmHost: acceptAll})
			if err == nil {
				t.Fatal("login without credentials succeeded")
			}
		})
	}
}

// Answering a question slowly is not a timeout.
func TestBuiltinSSHSlowAnswerIsNotATimeout(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())
	opts := SSHOptions{DisableAgent: true, ConnectTimeout: 300 * time.Millisecond,
		ConfirmHost: func(string, ssh.PublicKey) (bool, error) {
			time.Sleep(900 * time.Millisecond)
			return true, nil
		}}
	stream, err := DialSSH("tester@"+srv.addr, opts)
	if err != nil {
		t.Fatalf("a slow answer timed out: %v", err)
	}
	stream.Close()
}

func TestPermanentSSHFailureIsNotRetried(t *testing.T) {
	for msg, want := range map[string]bool{
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]": true,
		"REMOTE HOST IDENTIFICATION HAS CHANGED for x":                                          true,
		"connect 10.0.0.1:22: i/o timeout":                                                      false,
		"connection refused":                                                                    false,
	} {
		if got := permanentSSHFailure(msg); got != want {
			t.Errorf("permanentSSHFailure(%q) = %v, want %v", msg, got, want)
		}
	}
}
