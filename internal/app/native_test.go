package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain builds the lam binary once; hosts are started by re-executing it
// (see selfBinary / LAM_BIN), which a test binary cannot do itself.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lam-test-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "lam")
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "locating application package for test binary")
		os.RemoveAll(dir)
		os.Exit(1)
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	build := exec.Command("go", "build", "-o", bin, "./cmd/lam")
	build.Dir = repoRoot
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building lam for tests:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	os.Setenv("LAM_BIN", bin)
	// Keep tests away from the real state and sockets: they create and stop
	// sessions, which would otherwise show up in the user's own event log and
	// restore list.
	state, err := os.MkdirTemp("", "lam-test-state")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_STATE_HOME", state)
	code := m.Run()
	os.RemoveAll(state)
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"sh -c 'claude --permission-mode auto'": "claude",
		"sh -c claude":                          "claude",
		"/usr/bin/vim file":                     "vim",
		"":                                      "",
	}
	for in, want := range cases {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q, want %q", in, got, want)
		}
	}
}

// findSession polls List until the named session appears.
func findSession(t *testing.T, b SessionBackend, name string) sessionInfo {
	t.Helper()
	for i := 0; i < 30; i++ {
		sessions, err := b.List()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sessions {
			if s.Name == name {
				return s
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("session %q not listed", name)
	return sessionInfo{}
}

// attachSnapshot connects like `lam attach` does and returns the snapshot
// frame the host sends first.
func attachSnapshot(t *testing.T, name string) string {
	t.Helper()
	conn, err := connectSession(name, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeFrame(conn, frameResize, resizePayload(80, 24)); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	if typ != frameSnapshot {
		t.Fatalf("first frame type = %d, want snapshot", typ)
	}
	return string(payload)
}

func TestRunAttachConnectionReportsStdoutWriteError(t *testing.T) {
	stdout, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("/dev/full is unavailable: %v", err)
	}
	defer stdout.Close()
	oldStdout := os.Stdout
	os.Stdout = stdout
	defer func() { os.Stdout = oldStdout }()

	stdin, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdinWriter.Close()
	oldStdin := os.Stdin
	os.Stdin = stdin
	defer func() {
		os.Stdin = oldStdin
		stdin.Close()
	}()

	client, server := net.Pipe()
	defer server.Close()
	go func() {
		if _, _, err := readFrame(server); err != nil {
			return
		}
		_ = writeFrame(server, frameOutput, []byte("screen output"))
	}()

	err = runAttachConnection("test", client)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("runAttachConnection() error = %v, want ENOSPC", err)
	}
}

func TestNativeLifecycle(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	b := nativeBackend{}
	cwd := t.TempDir()
	if err := b.Create("lam-test", cwd, "echo NATIVE-OK; sleep 60"); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("lam-test")

	s := findSession(t, b, "lam-test")
	if s.CWD != cwd {
		t.Errorf("cwd = %q, want %q", s.CWD, cwd)
	}
	if s.Command != commandName("echo NATIVE-OK; sleep 60") {
		t.Errorf("command = %q", s.Command)
	}

	hist := ""
	for i := 0; i < 30 && !strings.Contains(hist, "NATIVE-OK"); i++ {
		hist, _ = b.History("lam-test")
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(hist, "NATIVE-OK") {
		t.Errorf("history missing output: %q", hist)
	}

	if err := b.Kill("lam-test"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if sessions, _ := b.List(); !hasSession(sessions, "lam-test") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("session still listed after kill")
}

func TestNativeReattachRestoresScreen(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	b := nativeBackend{}
	if err := b.Create("lam-cat", t.TempDir(), "cat"); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("lam-cat")

	if err := b.Send("lam-cat", "hello-native"); err != nil {
		t.Fatal(err)
	}
	// The echo reaches the emulator asynchronously, so poll for it.
	snap := ""
	for i := 0; i < 30 && !strings.Contains(snap, "hello-native"); i++ {
		snap = attachSnapshot(t, "lam-cat")
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(snap, "hello-native") {
		t.Errorf("snapshot missing typed text: %q", snap)
	}
}

func TestNativeStaleCleanup(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("LAM_SOCKET_DIR", dir)

	// A socket file nobody listens on: listen, then close without unlinking.
	ln, err := net.Listen("unix", filepath.Join(dir, "lam-stale.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	data, _ := json.Marshal(sessionMeta{Name: "lam-stale", PID: 1, CWD: dir, Command: "sh -c claude"})
	if err := os.WriteFile(filepath.Join(dir, "lam-stale.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	sessions, err := nativeBackend{}.List()
	if err != nil {
		t.Fatal(err)
	}
	if hasSession(sessions, "lam-stale") {
		t.Error("stale session listed")
	}
	for _, f := range []string{"lam-stale.sock", "lam-stale.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s not removed", f)
		}
	}
}

func TestNativeListIncludesSessionsFromDifferentWorkingDirectories(t *testing.T) {
	runtimeDir := shortTempDir(t)
	t.Setenv("LAM_SOCKET_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := os.MkdirAll(socketDir(), 0700); err != nil {
		t.Fatal(err)
	}

	for name, cwd := range map[string]string{"project-a": projectA, "project-b": projectB} {
		listener, err := net.Listen("unix", sessionSocket(name))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		meta, err := json.Marshal(sessionMeta{Name: name, PID: os.Getpid(), HostPID: os.Getpid(), CWD: cwd, Command: "bash", Created: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sessionMetaFile(name), meta, 0600); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(projectB)
	sessions, err := nativeBackend{}.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("listed %d sessions from %q, want sessions from both working directories: %#v", len(sessions), projectB, sessions)
	}
	seen := map[string]string{}
	for _, session := range sessions {
		seen[session.Name] = session.CWD
	}
	if seen["project-a"] != projectA || seen["project-b"] != projectB {
		t.Fatalf("listed session working directories = %#v, want project-a=%q and project-b=%q", seen, projectA, projectB)
	}
}

func TestSocketDirIsSharedAcrossWorkingDirectories(t *testing.T) {
	runtimeDir := shortTempDir(t)
	projectA := t.TempDir()
	projectB := t.TempDir()
	t.Setenv("LAM_SOCKET_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	t.Chdir(projectA)
	fromA := socketDir()
	t.Chdir(projectB)
	fromB := socketDir()
	if fromA != fromB {
		t.Fatalf("socket directories differ by working directory: %q != %q", fromA, fromB)
	}
	if want := filepath.Join(runtimeDir, "lam"); fromA != want {
		t.Fatalf("socket directory = %q, want shared runtime directory %q", fromA, want)
	}
}

func TestNativeSessionEndsOnItsOwn(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	b := nativeBackend{}
	if err := b.Create("lam-short", t.TempDir(), "sleep 1"); err != nil {
		t.Fatal(err)
	}
	findSession(t, b, "lam-short")
	for i := 0; i < 50; i++ {
		if sessions, _ := b.List(); !hasSession(sessions, "lam-short") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("session still listed after its command exited")
}

func TestNativeReattachRestoresModes(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	b := nativeBackend{}
	// A program that enables mouse reporting, bracketed paste and the alt screen.
	cmd := `printf '\033[?1049h\033[?1000h\033[?2004hMODES-OK'; sleep 60`
	if err := b.Create("lam-modes", t.TempDir(), cmd); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("lam-modes")

	snap := ""
	for i := 0; i < 30 && !strings.Contains(snap, "MODES-OK"); i++ {
		snap = attachSnapshot(t, "lam-modes")
		time.Sleep(100 * time.Millisecond)
	}
	for _, want := range []string{"MODES-OK", "\x1b[?1049h", "\x1b[?1000h", "\x1b[?2004h"} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot missing %q: %q", want, snap)
		}
	}
}

func TestNativeSnapshotIsViewportOnly(t *testing.T) {
	t.Setenv("LAM_SOCKET_DIR", shortTempDir(t))
	b := nativeBackend{}
	// 200 lines overflow the 24-row screen into scrollback; the snapshot must not replay them all.
	if err := b.Create("lam-scroll", t.TempDir(), `i=0; while [ $i -lt 200 ]; do echo SCROLL-LINE-$i; i=$((i+1)); done; sleep 60`); err != nil {
		t.Fatal(err)
	}
	defer b.Kill("lam-scroll")

	snap := ""
	for i := 0; i < 30 && !strings.Contains(snap, "SCROLL-LINE-199"); i++ {
		snap = attachSnapshot(t, "lam-scroll")
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(snap, "SCROLL-LINE-199") {
		t.Fatalf("snapshot missing last line: %q", snap)
	}
	if strings.Contains(snap, "SCROLL-LINE-10\r") || strings.Contains(snap, "SCROLL-LINE-10\n") {
		t.Errorf("snapshot contains scrollback, want viewport only")
	}
	if hist, _ := b.History("lam-scroll"); !strings.Contains(hist, "SCROLL-LINE-10\n") {
		t.Errorf("history lost scrollback")
	}
}

// shortTempDir is a temporary directory with a path short enough for a unix
// socket: t.TempDir() on macOS is long enough to exceed the 104-byte limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lam")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
