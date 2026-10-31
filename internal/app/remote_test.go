package app

import (
	"bufio"
	"bytes"
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

type remoteTestBackend struct {
	created chan sessionInfo
}

func (*remoteTestBackend) Name() string { return "test" }
func (b *remoteTestBackend) Create(name, cwd, command string) error {
	b.created <- sessionInfo{Name: name, CWD: cwd, Command: command}
	return nil
}
func (*remoteTestBackend) AttachCmd(string) *exec.Cmd     { return exec.Command("true") }
func (*remoteTestBackend) List() ([]sessionInfo, error)   { return nil, nil }
func (*remoteTestBackend) Kill(string) error              { return nil }
func (*remoteTestBackend) History(string) (string, error) { return "", nil }
func (*remoteTestBackend) Send(string, string) error      { return nil }

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
	setBackend(&remoteTestBackend{created: make(chan sessionInfo, 1)})
	t.Cleanup(func() { setBackend(previous) })

	clientConn, serverConn := net.Pipe()
	counting := &requestCountingConn{Conn: serverConn, counts: map[string]int{}}
	go func() {
		defer serverConn.Close()
		_ = serveRemote(counting, counting)
	}()
	mux, err := muxHandshake(clientConn, bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &remoteSessionBackend{name: "ssh:test", client: mux}
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
	serverBackend := &remoteTestBackend{created: make(chan sessionInfo, 1)}
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

func TestValidateSSHHost(t *testing.T) {
	for _, bad := range []string{"", " ", "-oProxyCommand=x", "a b", "a\nb"} {
		if validateSSHHost(bad) == nil {
			t.Errorf("validateSSHHost(%q) accepted", bad)
		}
	}
	if err := validateSSHHost("user@host"); err != nil {
		t.Error(err)
	}
	if args := sshArgs(false, "host", "cmd"); args[len(args)-3] != "--" {
		t.Errorf("host is not separated from ssh options: %v", args)
	}
}
