package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHost stands in for session hosts: dialing a name hands the test the host's
// end of the connection.
type fakeHost struct {
	mu       sync.Mutex
	sessions map[string]chan net.Conn
}

func newFakeHost(names ...string) *fakeHost {
	h := &fakeHost{sessions: map[string]chan net.Conn{}}
	for _, n := range names {
		h.sessions[n] = make(chan net.Conn, 4)
	}
	return h
}

func (h *fakeHost) dial(name string, _ time.Duration) (net.Conn, error) {
	h.mu.Lock()
	ch, ok := h.sessions[name]
	h.mu.Unlock()
	if !ok {
		return nil, errors.New("no such session")
	}
	client, host := net.Pipe()
	ch <- host
	return client, nil
}

func (h *fakeHost) accepted(t *testing.T, name string) net.Conn {
	t.Helper()
	select {
	case c := <-h.sessions[name]:
		return c
	case <-time.After(2 * time.Second):
		t.Fatalf("the interface never attached to %q", name)
		return nil
	}
}

// muxPair connects a client to a server running over an in-memory connection.
func muxPair(t *testing.T, host *fakeHost) *muxClient {
	t.Helper()
	cc, sc := net.Pipe()
	go func() {
		defer sc.Close()
		_ = serveMux(sc, sc, host.dial, sc)
	}()
	client := newMuxClient(cc, cc, nil)
	t.Cleanup(client.close)
	return client
}

func TestMuxFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	for i, payload := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte("ab"), 40000)} {
		if err := writeMuxFrame(&buf, &mu, uint32(i+7), muxFrameData, payload); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte("ab"), 40000)} {
		ch, kind, got, err := readMuxFrame(&buf)
		if err != nil || ch != uint32(i+7) || kind != muxFrameData || !bytes.Equal(got, want) {
			t.Fatalf("frame %d = (%d, %d, %d bytes, %v)", i, ch, kind, len(got), err)
		}
	}
	// A header that promises more than the limit is refused, not allocated.
	bad := []byte{0, 0, 0, 1, muxFrameData, 0xff, 0xff, 0xff, 0xff}
	if _, _, _, err := readMuxFrame(bytes.NewReader(bad)); err == nil {
		t.Fatal("an oversized frame was accepted")
	}
}

func TestMuxRequestsAndSessionsShareOneConnection(t *testing.T) {
	previous := selectedBackend
	setBackend(&tcpRemoteTestBackend{created: make(chan sessionInfo, 1)})
	t.Cleanup(func() { setBackend(previous) })

	host := newFakeHost("alpha", "beta")
	client := muxPair(t, host)

	// Open two sessions; each tells the host the terminal size first.
	a, err := client.OpenStream("alpha", 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.OpenStream("beta", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hostA, hostB := host.accepted(t, "alpha"), host.accepted(t, "beta")
	for _, c := range []struct {
		conn       net.Conn
		cols, rows int
	}{{hostA, 100, 30}, {hostB, 80, 24}} {
		typ, payload, err := readFrame(c.conn)
		if err != nil || typ != frameResize {
			t.Fatalf("first frame to the host = (%d, %v), want a resize", typ, err)
		}
		if cols, rows, ok := parseResize(payload); !ok || cols != c.cols || rows != c.rows {
			t.Fatalf("size sent to the host = %dx%d, want %dx%d", cols, rows, c.cols, c.rows)
		}
	}

	// Output goes to the right session, while requests keep working in between.
	go writeFrame(hostA, frameSnapshot, []byte("screen of alpha"))
	go writeFrame(hostB, frameOutput, []byte("output of beta"))
	if typ, payload, err := readFrame(a); err != nil || typ != frameSnapshot || string(payload) != "screen of alpha" {
		t.Fatalf("alpha got (%d, %q, %v)", typ, payload, err)
	}
	if typ, payload, err := readFrame(b); err != nil || typ != frameOutput || string(payload) != "output of beta" {
		t.Fatalf("beta got (%d, %q, %v)", typ, payload, err)
	}
	if response, err := client.call(remoteRequest{Op: "ping"}); err != nil || response.Protocol != remoteProtocolVersion {
		t.Fatalf("ping while sessions are open = (%#v, %v)", response, err)
	}

	// Input travels the other way, without crossing between sessions.
	go writeFrame(a, frameInput, []byte("typed in alpha"))
	if typ, payload, err := readFrame(hostA); err != nil || typ != frameInput || string(payload) != "typed in alpha" {
		t.Fatalf("host alpha got (%d, %q, %v)", typ, payload, err)
	}
	hostB.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := readFrame(hostB); err == nil {
		t.Fatal("input typed in alpha reached beta")
	}
}

func TestMuxLargeOutputArrivesIntact(t *testing.T) {
	host := newFakeHost("big")
	client := muxPair(t, host)
	stream, err := client.OpenStream("big", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hostConn := host.accepted(t, "big")
	readFrame(hostConn) // the size
	payload := bytes.Repeat([]byte("0123456789"), 30000)
	go writeFrame(hostConn, frameOutput, payload)
	typ, got, err := readFrame(stream)
	if err != nil || typ != frameOutput || !bytes.Equal(got, payload) {
		t.Fatalf("large frame = (%d, %d bytes, %v), want %d bytes intact", typ, len(got), err, len(payload))
	}
}

func TestMuxClosingEitherEndEndsTheSession(t *testing.T) {
	host := newFakeHost("one", "two")
	client := muxPair(t, host)

	// The interface detaches: the host connection is closed, the session lives on.
	one, _ := client.OpenStream("one", 80, 24)
	hostOne := host.accepted(t, "one")
	readFrame(hostOne)
	one.Close()
	hostOne.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := readFrame(hostOne); err == nil {
		t.Fatal("the host connection stayed open after the interface detached")
	}

	// The session ends: the interface sees the end of the stream.
	two, _ := client.OpenStream("two", 80, 24)
	hostTwo := host.accepted(t, "two")
	readFrame(hostTwo)
	hostTwo.Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(two)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a session that ended normally read as an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not end when the session did")
	}
}

func TestMuxUnknownSessionExplainsItself(t *testing.T) {
	client := muxPair(t, newFakeHost())
	stream, err := client.OpenStream("missing", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(stream)
	if err == nil || !strings.Contains(err.Error(), `no such session "missing"`) {
		t.Fatalf("attaching to an unknown session gave %v", err)
	}
}

func TestMuxConnectionLossEndsEverything(t *testing.T) {
	host := newFakeHost("s")
	cc, sc := net.Pipe()
	go func() { _ = serveMux(sc, sc, host.dial, sc) }()
	client := newMuxClient(cc, cc, nil)
	stream, _ := client.OpenStream("s", 80, 24)
	host.accepted(t, "s")

	sc.Close() // the network goes away

	if _, err := io.ReadAll(stream); err == nil {
		t.Fatal("a stream on a lost connection ended without an error")
	}
	if _, err := client.call(remoteRequest{Op: "ping"}); err == nil {
		t.Fatal("a request on a lost connection succeeded")
	}
	if _, err := client.OpenStream("s", 80, 24); err == nil {
		t.Fatal("a session was attached on a lost connection")
	}
}

func TestMuxHandshakeRefusesOlderHosts(t *testing.T) {
	// An older host answers ping with its own protocol version.
	cc, sc := net.Pipe()
	go func() {
		defer sc.Close()
		reader := bufio.NewReader(sc)
		line, _ := reader.ReadBytes('\n')
		var request remoteRequest
		_ = json.Unmarshal(line, &request)
		data, _ := json.Marshal(remoteResponse{ID: request.ID, Protocol: remoteProtocolVersion - 1})
		_, _ = sc.Write(append(data, '\n'))
	}()
	_, err := muxHandshake(cc, bufio.NewReader(cc), nil)
	if err == nil || !strings.Contains(err.Error(), "update lam on the remote host") {
		t.Fatalf("handshake with an older host = %v, want an instruction to update it", err)
	}

}

// countingReadConn counts the bytes that actually cross the connection.
type countingReadConn struct {
	net.Conn
	n atomic.Int64
}

func (c *countingReadConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// A program that redraws the whole screen for every wheel notch sends nearly
// the same text again and again; on a slow link that is what makes scrolling lag.
func TestMuxCompressesRepeatedScreenRedraws(t *testing.T) {
	host := newFakeHost("app")
	rawClient, server := net.Pipe()
	go func() {
		defer server.Close()
		_ = serveMux(server, server, host.dial, server)
	}()
	wire := &countingReadConn{Conn: rawClient}
	client := newMuxClient(wire, wire, nil)
	t.Cleanup(client.close)

	stream, err := client.OpenStream("app", 200, 50)
	if err != nil {
		t.Fatal(err)
	}
	hostConn := host.accepted(t, "app")
	readFrame(hostConn) // the size

	redraw := func(offset int) []byte {
		var b bytes.Buffer
		for row := 0; row < 48; row++ {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K\x1b[38;5;%dmline %06d the quick brown fox jumps over the lazy dog the quick brown fox\x1b[0m", row+1, 16+(offset+row)%200, offset+row)
		}
		return b.Bytes()
	}
	var sent int
	go func() {
		for i := 0; i < 40; i++ {
			payload := redraw(i * 3)
			sent += len(payload)
			writeFrame(hostConn, frameOutput, payload)
		}
	}()
	for i := 0; i < 40; i++ {
		if _, _, err := readFrame(stream); err != nil {
			t.Fatal(err)
		}
	}
	if got := wire.n.Load(); got*3 > int64(sent) {
		t.Errorf("%d bytes of screen redraws took %d bytes on the connection; want at most a third", sent, got)
	}
	t.Logf("%d bytes of redraws crossed the connection as %d bytes (%.0f%%)", sent, wire.n.Load(), 100*float64(wire.n.Load())/float64(sent))
}

// Without flow control the output of a program that floods the screen piles up
// in the network's buffers, where nothing can skip it. The host must stop
// reading from the session while the interface has too much unconsumed.
func TestMuxFlowControlBoundsWhatIsInFlight(t *testing.T) {
	host := newFakeHost("flood")
	client := muxPair(t, host)
	stream, err := client.OpenStream("flood", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hostConn := host.accepted(t, "flood")
	readFrame(hostConn) // the size

	var written atomic.Int64
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		chunk := make([]byte, 16<<10)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := hostConn.Write(chunk); err != nil {
				return
			}
			written.Add(int64(len(chunk)))
		}
	}()

	// The interface reads nothing yet: the host may only get a window ahead.
	time.Sleep(300 * time.Millisecond)
	if got, limit := written.Load(), int64(muxWindow+3*muxMaxData); got > limit {
		t.Fatalf("the host got %d bytes ahead of an interface that read nothing, want at most %d", got, limit)
	} else if got < muxWindow/2 {
		t.Fatalf("the host only got %d bytes out, want about a window (%d)", got, muxWindow)
	}

	// Reading lets it continue, and the interface can see all that was sent.
	buf := make([]byte, 64<<10)
	total := 0
	deadline := time.Now().Add(5 * time.Second)
	for total < 2<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d bytes arrived while reading: flow control did not resume", total)
		}
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
}

func shortenMuxLiveness(t *testing.T, ping, dead, serverIdle time.Duration) {
	t.Helper()
	p, d, s := muxPingInterval.Load(), muxDeadAfter.Load(), muxServerIdle.Load()
	muxPingInterval.Store(ping)
	muxDeadAfter.Store(dead)
	muxServerIdle.Store(serverIdle)
	t.Cleanup(func() { muxPingInterval.Store(p); muxDeadAfter.Store(d); muxServerIdle.Store(s) })
}

func TestMuxIdleLinkStaysAlive(t *testing.T) {
	shortenMuxLiveness(t, 20*time.Millisecond, 120*time.Millisecond, 120*time.Millisecond)
	client := muxPair(t, newFakeHost())
	time.Sleep(500 * time.Millisecond) // several dead-after periods with nothing to say
	if _, err := client.call(remoteRequest{Op: "ping"}); err != nil {
		t.Fatalf("an idle but healthy link was dropped: %v", err)
	}
}

func TestMuxSilentLinkIsDroppedAndTornDown(t *testing.T) {
	shortenMuxLiveness(t, 20*time.Millisecond, 120*time.Millisecond, time.Minute)
	cc, sc := net.Pipe()
	go io.Copy(io.Discard, sc) // the far end reads but never answers
	defer sc.Close()
	released := make(chan struct{})
	client := newMuxClient(cc, cc, func() { close(released) })
	stream, _ := client.OpenStream("s", 80, 24)

	select {
	case <-client.done:
	case <-time.After(2 * time.Second):
		t.Fatal("a silent link was not dropped")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("the dropped link was not torn down")
	}
	if _, err := io.ReadAll(stream); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("stream ended with %v, want a timeout", err)
	}
}

func TestMuxServerDropsASilentClient(t *testing.T) {
	shortenMuxLiveness(t, 20*time.Millisecond, time.Minute, 120*time.Millisecond)
	cc, sc := net.Pipe()
	defer cc.Close()
	done := make(chan error, 1)
	go func() { done <- serveMux(sc, sc, newFakeHost().dial, sc) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the host kept a client that never spoke")
	}
}

func TestRemoteBackendReconnectsAfterTheLinkDrops(t *testing.T) {
	previous := remoteRetryDelays
	remoteRetryDelays = []time.Duration{0, 10 * time.Millisecond}
	t.Cleanup(func() { remoteRetryDelays = previous })

	host := newFakeHost("s")
	cc, sc := net.Pipe()
	go func() { _ = serveMux(sc, sc, host.dial, sc) }()
	backend := &remoteSessionBackend{name: "test", client: newMuxClient(cc, cc, nil)}
	var attempts atomic.Int32
	backend.supervise(func() (*muxClient, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("host unreachable")
		}
		return muxPair(t, host), nil
	})
	defer backend.Close()

	if st := backend.LinkStatus(); !st.Up || st.Gen != 0 {
		t.Fatalf("fresh link = %+v", st)
	}
	stream, _ := backend.OpenAttachStream("s", 80, 24)
	host.accepted(t, "s")

	sc.Close() // the connection is lost

	if _, err := io.ReadAll(stream); err == nil {
		t.Fatal("the stream on the lost connection did not end")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st := backend.LinkStatus(); st.Up && st.Gen == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reconnected: %+v after %d attempts", backend.LinkStatus(), attempts.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3 (two failures, then success)", attempts.Load())
	}
	if _, err := backend.cur().call(remoteRequest{Op: "ping"}); err != nil {
		t.Fatalf("the new connection does not work: %v", err)
	}
	again, err := backend.OpenAttachStream("s", 80, 24)
	if err != nil {
		t.Fatalf("attaching again after reconnecting: %v", err)
	}
	again.Close()
}

func TestRemoteBackendReportsTheOutageWhileDown(t *testing.T) {
	previous := remoteRetryDelays
	remoteRetryDelays = []time.Duration{time.Hour}
	t.Cleanup(func() { remoteRetryDelays = previous })

	cc, sc := net.Pipe()
	go func() { _ = serveMux(sc, sc, newFakeHost().dial, sc) }()
	backend := &remoteSessionBackend{name: "test", client: newMuxClient(cc, cc, nil)}
	backend.supervise(func() (*muxClient, error) { return nil, errors.New("unreachable") })
	defer backend.Close()

	sc.Close()
	deadline := time.Now().Add(2 * time.Second)
	for backend.LinkStatus().Up {
		if time.Now().After(deadline) {
			t.Fatal("the outage was never reported")
		}
		time.Sleep(5 * time.Millisecond)
	}
	st := backend.LinkStatus()
	if st.Attempt != 1 || st.Err == "" || !st.RetryAt.After(time.Now()) {
		t.Fatalf("status while down = %+v", st)
	}
	// Requests fail at once instead of waiting on a dead connection.
	start := time.Now()
	if _, err := backend.List(); err == nil || time.Since(start) > time.Second {
		t.Fatalf("List while down = %v after %v", err, time.Since(start))
	}
}

func TestServeRemoteReturnsWhenTheClientFallsSilent(t *testing.T) {
	shortenMuxLiveness(t, 20*time.Millisecond, time.Minute, 120*time.Millisecond)
	cc, sc := net.Pipe()
	defer cc.Close()
	done := make(chan error, 1)
	go func() { done <- serveRemote(sc, sc) }()
	go func() { // a client that finishes the handshake and then says nothing
		reader := bufio.NewReader(cc)
		cc.Write([]byte(`{"id":1,"op":"mux"}` + "\n"))
		reader.ReadBytes('\n')
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serveRemote kept waiting on a silent client")
	}
}

// A pane that stops reading blocks the read loop, which must not make a
// working connection look dead.
func TestMuxStalledPaneDoesNotTimeOutTheLink(t *testing.T) {
	shortenMuxLiveness(t, 20*time.Millisecond, 120*time.Millisecond, time.Minute)
	host := newFakeHost("busy")
	client := muxPair(t, host)
	stream, err := client.OpenStream("busy", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hostConn := host.accepted(t, "busy")
	readFrame(hostConn)
	go func() {
		for i := 0; i < muxStreamQueue+50; i++ { // more than the pane's queue holds
			writeFrame(hostConn, frameOutput, []byte("x"))
		}
	}()
	time.Sleep(600 * time.Millisecond) // several dead-after periods, nobody reading
	select {
	case <-client.done:
		t.Fatalf("a stalled pane took the connection down: %v", client.cause())
	default:
	}
	stream.Close()
}

func TestRemoteBackendClosedDuringOutageNeverDials(t *testing.T) {
	for i := 0; i < 30; i++ {
		cc, sc := net.Pipe()
		go func() { _ = serveMux(sc, sc, newFakeHost().dial, sc) }()
		backend := &remoteSessionBackend{name: "test", client: newMuxClient(cc, cc, nil)}
		var dials atomic.Int32
		backend.supervise(func() (*muxClient, error) { dials.Add(1); return nil, errors.New("unreachable") })
		backend.Close()
		time.Sleep(20 * time.Millisecond)
		if dials.Load() != 0 {
			t.Fatalf("iteration %d: dialed %d times after Close", i, dials.Load())
		}
	}
}

// Started by ssh without a login shell, the remote host must widen its PATH
// before answering, or the provider CLIs are not found and only a shell is offered.
func TestServeRemoteWidensPATHFirst(t *testing.T) {
	var widened atomic.Bool
	previous := widenPATH
	widenPATH = func() { widened.Store(true) }
	t.Cleanup(func() { widenPATH = previous })

	cc, sc := net.Pipe()
	done := make(chan struct{})
	go func() { _ = serveRemote(sc, sc); close(done) }()
	cc.Close() // the client leaves at once; serveRemote must already have widened PATH
	<-done
	if !widened.Load() {
		t.Fatal("serveRemote did not widen PATH")
	}
}

// A pane that never reads is detached rather than freezing replies for the others.
func TestMuxStalledPaneIsDetachedAndRPCsResume(t *testing.T) {
	shortenMuxLiveness(t, 20*time.Millisecond, time.Minute, time.Minute)
	previous := muxStalledGrace
	muxStalledGrace = 200 * time.Millisecond
	t.Cleanup(func() { muxStalledGrace = previous })
	host := newFakeHost("busy")
	client := muxPair(t, host)
	stream, err := client.OpenStream("busy", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hostConn := host.accepted(t, "busy")
	readFrame(hostConn)
	go func() {
		for i := 0; i < muxStreamQueue+50; i++ {
			writeFrame(hostConn, frameOutput, []byte("x"))
		}
	}()
	select {
	case <-stream.done:
	case <-time.After(3 * time.Second):
		t.Fatal("a pane that never reads was not detached")
	}
	if _, err := client.call(remoteRequest{Op: "list"}); err != nil {
		t.Fatalf("RPC after the stall: %v", err)
	}
}
