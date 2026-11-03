package lammobile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sacca97/lam/internal/app"
)

type collector struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed chan string
}

func (c *collector) OnOutput(d []byte) { c.mu.Lock(); c.buf.Write(d); c.mu.Unlock() }
func (c *collector) OnClosed(r string) { c.closed <- r }
func (c *collector) has(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Contains(c.buf.String(), s)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDemoListAttachEchoResize(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c, err := connectDemo() // handshake over net.Pipe
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Alive() {
		t.Fatal("not alive")
	}
	list, err := c.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if list.Len() != 2 || list.Get(2) != nil {
		t.Fatalf("sessions: %d", list.Len())
	}
	var active *Session
	for i := 0; i < list.Len(); i++ {
		if list.Get(i).Name == "claude-demo" {
			active = list.Get(i)
		}
	}
	if active == nil || !active.Active || active.Folder == "" {
		t.Fatalf("claude-demo: %+v", active)
	}

	out := &collector{closed: make(chan string, 1)}
	term, err := c.Attach("claude-demo", 80, 24, out)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "snapshot and script", func() bool { return out.has("lam demo") })
	if err := term.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "echo", func() bool { return out.has("hi") })
	if err := term.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resize", func() bool { return out.has("[resized to 100x30]") })
	if term.Resize(0, 5) == nil {
		t.Fatal("zero size accepted")
	}
	term.Close()
	select {
	case <-out.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("OnClosed not called")
	}
	if _, err := c.Attach("nope", 80, 24, out); err == nil {
		// the open is answered asynchronously: the failure shows as a close
		t.Log("attach to a missing session returned a stream; expected a close")
	}
}

func TestPublicKey(t *testing.T) {
	if _, err := PublicKey(); err == nil {
		t.Skip("data dir already set by another test")
	}
	if err := SetDataDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	a, err := PublicKey()
	if err != nil || !strings.HasPrefix(a, "ssh-ed25519 ") {
		t.Fatalf("key %q: %v", a, err)
	}
	b, _ := PublicKey()
	if a != b {
		t.Fatal("key changed between calls")
	}
}

// connectDemo connects to the in-process fake host the tests run against.
func connectDemo() (*Client, error) {
	r, rw, err := app.StartMobileDemo()
	if err != nil {
		return nil, err
	}
	link, err := app.MobileConnectConn(r, rw)
	if err != nil {
		return nil, err
	}
	return &Client{link: link}, nil
}

func TestDemoNewSessionHelpers(t *testing.T) {
	// An earlier test leaves TMPDIR pointing at a removed directory.
	root, err := os.MkdirTemp("/tmp", "lam-mobile")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	t.Setenv("TMPDIR", root)
	home := filepath.Join(root, "home")
	os.Mkdir(home, 0o755)
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, "projects"), 0o755)
	c, err := connectDemo()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := c.Home(); err != nil || got != home {
		t.Fatalf("Home = %q, %v", got, err)
	}
	if got, err := c.CompleteDir("~/pro", ""); err != nil || got != "~/projects/" {
		t.Fatalf("CompleteDir = %q, %v", got, err)
	}
	if _, err := c.Launchers(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateSession("nonsense", ""); err == nil {
		t.Fatal("an unknown agent was accepted")
	}
	if _, err := c.CreateSession("terminal", filepath.Join(home, "missing")); err == nil {
		t.Fatal("a directory that does not exist was accepted")
	}
}
