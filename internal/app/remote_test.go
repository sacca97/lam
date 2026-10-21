package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type tcpRemoteTestBackend struct {
	created chan sessionInfo
}

func (*tcpRemoteTestBackend) Name() string { return "tcp-test" }
func (b *tcpRemoteTestBackend) Create(name, cwd, command string) error {
	b.created <- sessionInfo{Name: name, CWD: cwd, Command: command}
	return nil
}
func (*tcpRemoteTestBackend) AttachCmd(string) *exec.Cmd     { return exec.Command("true") }
func (*tcpRemoteTestBackend) List() ([]sessionInfo, error)   { return nil, nil }
func (*tcpRemoteTestBackend) Kill(string) error              { return nil }
func (*tcpRemoteTestBackend) History(string) (string, error) { return "", nil }
func (*tcpRemoteTestBackend) Send(string, string) error      { return nil }

func TestNormalizeRemoteTCPAddressDefaultsPortAndKeepsOverrides(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "fedora", want: "fedora:7420"},
		{input: "10.8.0.2", want: "10.8.0.2:7420"},
		{input: "10.8.0.2:8123", want: "10.8.0.2:8123"},
		{input: "fd00::2", want: "[fd00::2]:7420"},
		{input: "[fd00::2]", want: "[fd00::2]:7420"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := normalizeRemoteTCPAddress(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("normalizeRemoteTCPAddress(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeRemoteTCPAddressRejectsWildcardBind(t *testing.T) {
	for _, address := range []string{"0.0.0.0", "[::]", ":7420"} {
		if _, err := normalizeRemoteTCPAddress(address); err == nil {
			t.Errorf("normalizeRemoteTCPAddress(%q) succeeded; want an error", address)
		}
	}
}

func TestNormalizeRemoteListenAddressDefaultsToAllInterfaces(t *testing.T) {
	got, err := normalizeRemoteListenAddress("")
	if err != nil {
		t.Fatal(err)
	}
	if want := ":7420"; got != want {
		t.Fatalf("normalizeRemoteListenAddress(\"\") = %q, want %q", got, want)
	}
	for _, address := range []string{":7420", "0.0.0.0:7420", "[::]:7420"} {
		if _, err := normalizeRemoteListenAddress(address); err != nil {
			t.Errorf("normalizeRemoteListenAddress(%q): %v", address, err)
		}
	}
	if port, all := remoteListenPort("0.0.0.0:8123"); port != "8123" || !all {
		t.Fatalf("remoteListenPort(0.0.0.0:8123) = (%q, %v), want (8123, true)", port, all)
	}
	if _, all := remoteListenPort("10.0.0.2:7420"); all {
		t.Fatal("specific VPN address was reported as all interfaces")
	}
}

func TestParseRemoteTCPTargetUsesSSHHostAndDialHost(t *testing.T) {
	tests := []struct {
		target  string
		sshHost string
		address string
	}{
		{target: "fedora", sshHost: "fedora", address: "fedora:7420"},
		{target: "sacca@fedora:8123", sshHost: "sacca@fedora", address: "fedora:8123"},
		{target: "10.8.0.2", sshHost: "10.8.0.2", address: "10.8.0.2:7420"},
	}
	for _, test := range tests {
		sshHost, address, err := parseRemoteTCPTarget(test.target)
		if err != nil {
			t.Fatalf("parseRemoteTCPTarget(%q): %v", test.target, err)
		}
		if sshHost != test.sshHost || address != test.address {
			t.Errorf("parseRemoteTCPTarget(%q) = (%q, %q), want (%q, %q)", test.target, sshHost, address, test.sshHost, test.address)
		}
	}
}

func TestRemoteTCPAuthTicketExpiresAndCanOnlyBeConsumedOnce(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, remoteTicketKeyBytes)
	now := time.Now().Truncate(time.Second)
	ticket, err := issueRemoteTCPAuthTicket(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce, expires, err := validateRemoteTCPAuthTicket(ticket, key, now)
	if err != nil {
		t.Fatal(err)
	}
	cache := newRemoteTicketReplayCache()
	if !cache.consume(nonce, expires, now) {
		t.Fatal("first ticket use was rejected")
	}
	if cache.consume(nonce, expires, now) {
		t.Fatal("replayed ticket was accepted")
	}
	if _, _, err := validateRemoteTCPAuthTicket(ticket, key, expires.Add(time.Second)); err == nil {
		t.Fatal("expired ticket was accepted")
	}
	if _, _, err := validateRemoteTCPAuthTicket(ticket+"x", key, now); err == nil {
		t.Fatal("tampered ticket was accepted")
	}
}

func TestRemoteListenerConfigStoresPrivateSigningKey(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	address, err := saveRemoteListenConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if address != ":7420" {
		t.Fatalf("saved listener address = %q, want :7420", address)
	}
	configured, err := loadRemoteListenConfig()
	if err != nil || configured != address {
		t.Fatalf("loadRemoteListenConfig() = %q, %v; want %q", configured, err, address)
	}

	key, err := ensureRemoteTicketKey()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadRemoteTicketKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, loaded) {
		t.Fatal("loaded signing key does not match the generated key")
	}
	info, err := os.Stat(remoteTicketKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("signing key mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestServeRemoteTCPConnAuthenticatesTicketBeforeRPC(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, remoteTicketKeyBytes)
	ticket, err := issueRemoteTCPAuthTicket(key)
	if err != nil {
		t.Fatal(err)
	}
	replays := newRemoteTicketReplayCache()
	serveOnce := func(ticket string) (*remoteResponse, error) {
		client, server := net.Pipe()
		serverDone := make(chan error, 1)
		go func() {
			defer server.Close()
			serverDone <- serveRemoteTCPConn(server, key, replays)
		}()
		_ = client.SetDeadline(time.Now().Add(time.Second))
		if err := writeAll(client, []byte(ticket+"\n")); err != nil {
			client.Close()
			return nil, err
		}
		if err := json.NewEncoder(client).Encode(remoteRequest{ID: 7, Op: "ping"}); err != nil {
			client.Close()
			return nil, err
		}
		var response remoteResponse
		decodeErr := json.NewDecoder(client).Decode(&response)
		client.Close()
		serveErr := <-serverDone
		if decodeErr != nil {
			return nil, decodeErr
		}
		if serveErr != nil {
			return nil, serveErr
		}
		return &response, nil
	}
	response, err := serveOnce(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != 7 || response.Protocol != remoteProtocolVersion {
		t.Fatalf("authenticated ping response = %#v", response)
	}
	if _, err := serveOnce(ticket); err == nil {
		t.Fatal("replayed authentication ticket was accepted")
	}
}

func TestRemoteToolCommandPrefersDefaultInstall(t *testing.T) {
	home := t.TempDir()
	installDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lam := filepath.Join(installDir, "lam")
	if err := os.WriteFile(lam, []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	staleLam := filepath.Join(pathDir, "lam")
	if err := os.WriteFile(staleLam, []byte("#!/bin/sh\nprintf '<stale PATH copy>\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", "-c", remoteToolCommand("remote-ticket", "name with 'quote"))
	cmd.Env = []string{"HOME=" + home, "PATH=" + pathDir}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remote command failed: %v\n%s", err, output)
	}
	want := "<remote-ticket>\n<name with 'quote>\n"
	if string(output) != want {
		t.Fatalf("remote command output = %q, want %q", output, want)
	}
}

func TestRemoteToolCommandExplainsMissingInstall(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", remoteToolCommand("remote"))
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !strings.Contains(string(output), "LAM is not installed on this SSH host") ||
		!strings.Contains(string(output), "make install") ||
		!strings.Contains(string(output), "PATH") ||
		!errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		t.Fatalf("remote command returned (%v), output %q; want an install hint and exit status 127", err, output)
	}
}

func TestTCPRemoteCreateUsesRemoteSessionBackend(t *testing.T) {
	previous := selectedBackend
	serverBackend := &tcpRemoteTestBackend{created: make(chan sessionInfo, 1)}
	setBackend(serverBackend)
	t.Cleanup(func() { setBackend(previous) })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	key := bytes.Repeat([]byte{0x42}, remoteTicketKeyBytes)
	ticket, err := issueRemoteTCPAuthTicket(key)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = serveRemoteTCPConn(conn, key, newRemoteTicketReplayCache())
	}()

	client, err := newTCPSessionBackendWithTicket(listener.Addr().String(), "127.0.0.1", ticket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	const cwd = "/remote/work/project"
	if err := client.Create("remote-project", cwd, "codex --approve-for-me"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-serverBackend.created:
		if want := (sessionInfo{Name: "remote-project", CWD: cwd, Command: "codex --approve-for-me"}); got != want {
			t.Fatalf("remote create request = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("remote create did not reach the server session backend")
	}
}

// requestCountingConn counts the requests that arrive on a connection.
type requestCountingConn struct {
	net.Conn
	mu     sync.Mutex
	counts map[string]int
}

func (c *requestCountingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, op := range []string{"home", "cwd"} {
		c.counts[op] += bytes.Count(p[:n], []byte(`"op":"`+op+`"`))
	}
	return n, err
}

// Drawing asks for the remote home directory once per session row on every
// frame. Each ask used to be a network round trip, which made typing and
// scrolling crawl on a slow link.
func TestRemoteDirectoriesAreFetchedOnce(t *testing.T) {
	previous := selectedBackend
	setBackend(&tcpRemoteTestBackend{created: make(chan sessionInfo, 1)})
	t.Cleanup(func() { setBackend(previous) })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	key := bytes.Repeat([]byte{0x42}, remoteTicketKeyBytes)
	ticket, err := issueRemoteTCPAuthTicket(key)
	if err != nil {
		t.Fatal(err)
	}
	counting := &requestCountingConn{counts: map[string]int{}}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		counting.Conn = conn
		_ = serveRemoteTCPConn(counting, key, newRemoteTicketReplayCache())
	}()

	client, err := newTCPSessionBackendWithTicket(listener.Addr().String(), "127.0.0.1", ticket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var home string
	for i := 0; i < 50; i++ {
		home, err = client.HomeDirectory()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.WorkingDirectory(); err != nil {
			t.Fatal(err)
		}
		_ = shortenHomePath(filepath.Join(home, "project"), client)
	}
	counting.mu.Lock()
	defer counting.mu.Unlock()
	if counting.counts["home"] != 1 || counting.counts["cwd"] != 1 {
		t.Fatalf("requests sent for 50 lookups: %v, want one of each", counting.counts)
	}
	if got := shortenHomePath(filepath.Join(home, "project"), client); got != "~/project" {
		t.Fatalf("shortenHomePath = %q", got)
	}
}

func TestSSHRemoteProtocolCanCreateSession(t *testing.T) {
	previous := selectedBackend
	serverBackend := &tcpRemoteTestBackend{created: make(chan sessionInfo, 1)}
	setBackend(serverBackend)
	t.Cleanup(func() { setBackend(previous) })

	// The ssh case: the client and the host talk over stdio.
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		_ = serveRemote(serverConn, serverConn)
	}()
	client, err := muxHandshake(clientConn, bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()

	backend := &remoteSessionBackend{name: "ssh:test", client: client}
	if err := backend.Create("ssh-project", "/remote/project", "claude"); err != nil {
		t.Fatal(err)
	}
	select {
	case session := <-serverBackend.created:
		if want := (sessionInfo{Name: "ssh-project", CWD: "/remote/project", Command: "claude"}); session != want {
			t.Fatalf("SSH remote create = %#v, want %#v", session, want)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH remote create did not reach the session backend")
	}
}
