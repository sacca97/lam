package app

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHostRenameMovesSocketPathMetadataAndMarkers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LAM_SOCKET_DIR", dir)
	oldName := fmt.Sprintf("rename-test-%d", time.Now().UnixNano())
	newName := oldName + "-renamed"

	if err := os.WriteFile(sessionSocket(oldName), []byte("socket path"), 0600); err != nil {
		t.Fatal(err)
	}
	meta := sessionMeta{Name: oldName, PID: 10, HostPID: 11, CWD: dir, Command: "sh", Created: time.Now()}
	if err := writeSessionMeta(meta); err != nil {
		t.Fatal(err)
	}
	host := &hostSession{name: oldName, meta: meta}
	for _, prefix := range []string{"claude-unread", "claude-status"} {
		oldPath := fmt.Sprintf("/tmp/%s-%s", prefix, oldName)
		newPath := fmt.Sprintf("/tmp/%s-%s", prefix, newName)
		if err := os.WriteFile(oldPath, []byte("marker"), 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(oldPath)
		defer os.Remove(newPath)
	}

	if err := host.rename(newName); err != nil {
		t.Fatal(err)
	}
	if got := host.currentName(); got != newName {
		t.Fatalf("host name = %q, want %s", got, newName)
	}
	if _, err := os.Stat(sessionSocket(oldName)); !os.IsNotExist(err) {
		t.Fatalf("old socket still exists: %v", err)
	}
	if _, err := os.Stat(sessionMetaFile(oldName)); !os.IsNotExist(err) {
		t.Fatalf("old metadata still exists: %v", err)
	}
	data, err := os.ReadFile(sessionMetaFile(newName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"name":"`+newName+`"`) {
		t.Fatalf("renamed metadata does not contain the new name: %s", data)
	}
	for _, prefix := range []string{"claude-unread", "claude-status"} {
		oldPath := fmt.Sprintf("/tmp/%s-%s", prefix, oldName)
		newPath := fmt.Sprintf("/tmp/%s-%s", prefix, newName)
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Fatalf("old %s marker still exists: %v", prefix, err)
		}
		if _, err := os.Stat(newPath); err != nil {
			t.Fatalf("new %s marker is missing: %v", prefix, err)
		}
	}

	occupiedName := newName + "-occupied"
	if err := os.WriteFile(sessionSocket(occupiedName), []byte("other socket"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := host.rename(occupiedName); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("rename to occupied name error = %v, want already exists", err)
	}
	if got := host.currentName(); got != newName {
		t.Fatalf("failed rename changed host name to %q", got)
	}
}

func TestHostTerminalAnswersQueriesOnceWithDefaultColors(t *testing.T) {
	var replies []byte
	term, err := newHostTerminal(80, 24, func(data []byte) { replies = append(replies, data...) })
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	term.VTWrite([]byte("\x1b[c\x1b]10;?\x07\x1b]11;?\x07"))
	got := string(replies)
	for _, want := range []string{"\x1b[?62;22c", "\x1b]10;rgb:e5e5/e5e5/e5e5", "\x1b]11;rgb:0000/0000/0000"} {
		if !strings.Contains(got, want) {
			t.Errorf("replies %q lack %q", got, want)
		}
	}
	if strings.Count(got, "\x1b[?62;22c") != 1 {
		t.Errorf("device attributes answered %d times: %q", strings.Count(got, "\x1b[?62;22c"), got)
	}

	// Emoji sequences take two cells, as in the terminal showing them.
	term.VTWrite([]byte("\r\n⚠️|"))
	if x, _ := term.CursorX(); x != 3 {
		t.Errorf("cursor after a variation-selector emoji and a marker = column %d, want 3", x)
	}
}

func TestHostTerminalKeepsAThousandRowsOfScrollback(t *testing.T) {
	line := strings.Repeat("\x1b[38;5;108mstreaming output with some colour and enough text to fill the row ", 4) + "\r\n"
	for _, cols := range []int{60, 80, 100, 120, 160, 200, 250, 300, 400} {
		term, err := newHostTerminal(cols, 40, func([]byte) {})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5000; i++ {
			term.VTWrite([]byte(line))
		}
		if rows, _ := term.ScrollbackRows(); rows < 1000 {
			t.Errorf("%d columns: only %d rows of scrollback kept, want at least 1000", cols, rows)
		}
		term.Close()
	}
}

func TestEventLogTrimsAndNamesCallers(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for i := 0; i < eventLogKeepLines*3; i++ {
		logEvent("padding line %d %s", i, strings.Repeat("x", 600))
	}
	logEvent("the newest line")
	data, err := os.ReadFile(eventLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > eventLogMaxBytes+1024 {
		t.Errorf("event log grew to %d bytes", len(data))
	}
	if !strings.HasSuffix(strings.TrimSpace(string(data)), "the newest line") {
		t.Errorf("newest line missing from the log tail")
	}
	if got := callerNames(0, 3); !strings.Contains(got, "TestEventLogTrimsAndNamesCallers") {
		t.Errorf("callerNames = %q", got)
	}
}

// Pruning scrollback by line count is several times slower in libghostty than
// pruning by bytes, so a terminal that scrolls fast must never be limited by
// lines. This guards the throughput of a screen that is flooded with output.
func TestScrollbackIsBoundedByBytesNotLines(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("throughput is a wall-clock measure and shared CI runners are too noisy for it")
	}
	chunk := []byte(strings.Repeat("\x1b[38;5;108msome streaming output line that keeps scrolling the screen\r\n", 500))
	speed := func(term interface {
		VTWrite([]byte)
	}) float64 {
		for i := 0; i < 80; i++ {
			term.VTWrite(chunk) // fill the scrollback first
		}
		start := time.Now()
		const rounds = 300
		for i := 0; i < rounds; i++ {
			term.VTWrite(chunk)
		}
		return float64(len(chunk)*rounds) / time.Since(start).Seconds() / 1e6
	}
	host, err := newHostTerminal(200, 48, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ui, err := newTermEmu(200, 48, termCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Close()
	// Line-limited terminals measure about 120 MB/s here, byte-limited ones
	// 500 MB/s or more. The bar is set well below that to stay stable on slow
	// machines while still telling the two apart.
	const floor = 250.0
	if mbps := speed(host); mbps < floor {
		t.Errorf("host terminal parses a scrolling flood at %.0f MB/s, want at least %.0f: is scrollback limited by lines again?", mbps, floor)
	}
	if mbps := speed(uiWriter{ui}); mbps < floor {
		t.Errorf("interface terminal parses a scrolling flood at %.0f MB/s, want at least %.0f: is scrollback limited by lines again?", mbps, floor)
	}
}

type uiWriter struct{ e *termEmu }

func (w uiWriter) VTWrite(p []byte) { w.e.Write(p) }

func TestHostSkipsAheadForAClientThatFellBehind(t *testing.T) {
	term, err := newHostTerminal(80, 24, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	h := &hostSession{term: term, cols: 80, rows: 24, clients: map[*hostClient]struct{}{}}
	newClient := func() *hostClient {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		return &hostClient{conn: a, out: make(chan hostFrame, hostOutQueue), done: make(chan struct{})}
	}
	slow, fast := newClient(), newClient()
	h.clients[slow], h.clients[fast] = struct{}{}, struct{}{}

	// The slow client has a backlog nobody is writing out.
	for i := 0; i < 5; i++ {
		slow.send(hostFrame{typ: frameOutput, payload: make([]byte, 20<<10)})
	}
	if slow.pending.Load() <= hostLagBytes {
		t.Fatalf("test setup: backlog is only %d bytes", slow.pending.Load())
	}

	term.VTWrite([]byte("fresh output"))
	h.broadcastLocked([]byte("fresh output"))

	if len(slow.out) != 1 {
		t.Fatalf("the lagging client has %d frames queued, want just the snapshot", len(slow.out))
	}
	f := <-slow.out
	if f.typ != frameSnapshot || !strings.Contains(string(f.payload), "fresh output") {
		t.Errorf("the lagging client got (%d, %q), want a snapshot that includes the new output", f.typ, f.payload)
	}
	if got := <-fast.out; got.typ != frameOutput || string(got.payload) != "fresh output" {
		t.Errorf("the client that kept up got (%d, %q), want the plain output", got.typ, got.payload)
	}
	// The accounting follows the queue.
	slow.pending.Store(0)
	if slow.send(hostFrame{typ: frameOutput, payload: make([]byte, 100)}); slow.pending.Load() != 105 {
		t.Errorf("pending = %d after queueing a 100-byte frame, want 105", slow.pending.Load())
	}
	slow.drain()
	if slow.pending.Load() != 0 || len(slow.out) != 0 {
		t.Errorf("drain left %d bytes and %d frames", slow.pending.Load(), len(slow.out))
	}
}

func newFloodTestSession(t *testing.T) (*hostSession, *hostClient) {
	t.Helper()
	term, err := newHostTerminal(80, 24, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	h := &hostSession{term: term, cols: 80, rows: 24, clients: map[*hostClient]struct{}{}}
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	cl := &hostClient{conn: a, out: make(chan hostFrame, hostOutQueue), done: make(chan struct{})}
	h.clients[cl] = struct{}{}
	return h, cl
}

// Ordinary output, even a busy build log, is forwarded as it is.
func TestHostForwardsOrdinaryOutputUntouched(t *testing.T) {
	h, cl := newFloodTestSession(t)
	chunk := make([]byte, 32<<10)
	for i := 0; i < 8; i++ { // 256 KB, well under the flood threshold
		h.mu.Lock()
		h.term.VTWrite(chunk)
		h.broadcastLocked(chunk)
		h.mu.Unlock()
		<-cl.out
		cl.pending.Store(0)
	}
	h.mu.Lock()
	flooded := cl.stale
	h.mu.Unlock()
	if flooded {
		t.Fatal("a client receiving ordinary output was treated as flooded")
	}
}

// A program printing as fast as it can is not replayed to the client: the
// client gets the latest screen, at a bounded rate, and nothing is left stale.
func TestHostCoalescesAFloodIntoSnapshots(t *testing.T) {
	h, cl := newFloodTestSession(t)
	chunk := []byte(strings.Repeat("flooding the screen with a long log line\r\n", 800)) // ~33 KB
	var forwarded int
	for i := 0; i < 80; i++ { // ~2.6 MB offered at once
		h.mu.Lock()
		h.term.VTWrite(chunk)
		h.broadcastLocked(chunk)
		h.mu.Unlock()
		for len(cl.out) > 0 {
			f := <-cl.out
			cl.pending.Add(-frameSize(f))
			if f.typ == frameOutput {
				forwarded += len(f.payload)
			}
		}
	}
	if forwarded > hostFloodBytes+len(chunk) {
		t.Errorf("%d bytes of a flood were forwarded, want about %d at most", forwarded, hostFloodBytes)
	}
	h.mu.Lock()
	stale := cl.stale
	h.term.VTWrite([]byte("the very last line"))
	h.broadcastLocked([]byte("the very last line")) // withheld too: it must not overtake the snapshot
	h.mu.Unlock()
	if !stale {
		t.Fatal("the client was not marked as needing a snapshot")
	}
	if len(cl.out) != 0 {
		t.Fatalf("output was forwarded while a snapshot was owed (%d frames queued)", len(cl.out))
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(cl.out) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(cl.out) != 1 {
		t.Fatalf("expected exactly one snapshot after the flood, got %d frames", len(cl.out))
	}
	f := <-cl.out
	if f.typ != frameSnapshot || !strings.Contains(string(f.payload), "the very last line") {
		t.Errorf("snapshot after the flood = (%d, %d bytes) without the last line", f.typ, len(f.payload))
	}
	h.mu.Lock()
	stillStale := cl.stale
	h.mu.Unlock()
	if stillStale {
		t.Error("the client is still marked stale after its snapshot")
	}
	// Normal output resumes afterwards.
	time.Sleep(hostFloodInterval + 20*time.Millisecond)
	h.mu.Lock()
	h.broadcastLocked([]byte("ok"))
	h.mu.Unlock()
	if len(cl.out) != 1 || (<-cl.out).typ != frameOutput {
		t.Error("output was not forwarded normally once the flood ended")
	}
}

// The echo of a large paste arrives as many tiny frames. They fill the frame
// queue long before the byte limits, and that must not drop the client.
func TestHostKeepsAClientWhoseFrameQueueFillsWithSmallFrames(t *testing.T) {
	h, cl := newFloodTestSession(t)
	t.Cleanup(func() { // the snapshot timer must not fire after the terminal is closed
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.floodTimer != nil {
			h.floodTimer.Stop()
		}
	})
	line := []byte("line 0000001 xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\r\n")
	for i := 0; i < hostOutQueue*2; i++ { // nothing reads the queue
		h.mu.Lock()
		h.term.VTWrite(line)
		h.broadcastLocked(line)
		h.mu.Unlock()
	}
	h.mu.Lock()
	_, attached := h.clients[cl]
	stale := cl.stale
	h.mu.Unlock()
	if !attached {
		t.Fatal("the client was dropped when its frame queue filled")
	}
	if !stale {
		t.Fatal("the client was not marked for a fresh snapshot")
	}
}
