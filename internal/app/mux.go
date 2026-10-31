package app

// A remote connection is one authenticated byte stream (the stdio of an ssh
// command) shared by everything the interface needs from
// the remote host: requests, and any number of attached sessions. Sharing it
// means the network connection and the authentication happen once, and
// switching to another session costs one round trip instead of a new
// connection and a new login.
//
// After a short line-based handshake (see serveRemoteHandshake) both sides
// exchange frames: channel (4 bytes) | kind (1) | length (4) | payload.
// Channel 0 carries requests and responses as JSON. Every other channel is an
// attached session and carries the session host's own frame stream unchanged,
// so the interface decodes it exactly as `lam attach` does.

import (
	"bufio"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	muxFrameRPC   byte = 1 // channel 0: a JSON request (to the host) or response (from it)
	muxFrameOpen  byte = 2 // to the host: attach a new channel to a session (muxOpen)
	muxFrameData  byte = 3 // either way: bytes of the session's frame stream
	muxFrameClose byte = 4 // either way: the channel ended; the payload may say why
	muxFrameAck   byte = 5 // to the host: the interface has consumed this many more bytes (4, big endian)

	muxHeaderSize  = 9
	muxMaxData     = 32 << 10        // data frames are split to this size
	muxInputStall  = 5 * time.Second // how long a full input queue may hold up the connection
	muxStreamQueue = 512             // frames buffered per channel before the connection waits
	// Flow control. The host sends at most muxWindow bytes of a session's output
	// that the interface has not yet consumed, and the interface says how much it
	// has consumed every muxAckEvery bytes. Without it, output piles up in the
	// network's buffers where nothing can skip it, and a program that floods the
	// screen leaves keystrokes and scrolling waiting behind seconds of output.
	muxWindow        = 128 << 10
	muxAckEvery      = 32 << 10
	muxOpenTimeout   = 2 * time.Second
	muxHandshakeWait = 15 * time.Second
)

// Liveness. An idle client pings the host this often; a link that has said
// nothing for muxDeadAfter is dropped, so a connection that vanished without a
// goodbye (a sleeping laptop, a NAT that forgot it) is noticed in seconds
// rather than after TCP gives up. The host drops clients that stay silent for
// muxServerIdle, which is several pings. Adjustable so tests can shorten them.
var (
	muxPingInterval = newLiveness(5 * time.Second)
	muxDeadAfter    = newLiveness(15 * time.Second)
	muxServerIdle   = newLiveness(30 * time.Second)
)

// liveness is a duration that tests can change while connections still run.
type liveness struct{ ns atomic.Int64 }

func newLiveness(d time.Duration) *liveness {
	l := &liveness{}
	l.ns.Store(int64(d))
	return l
}

func (l *liveness) Load() time.Duration   { return time.Duration(l.ns.Load()) }
func (l *liveness) Store(d time.Duration) { l.ns.Store(int64(d)) }

type muxOpen struct {
	Name string `json:"name"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func encodeMuxFrame(channel uint32, kind byte, payload []byte) []byte {
	buf := make([]byte, muxHeaderSize+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], channel)
	buf[4] = kind
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(payload)))
	copy(buf[muxHeaderSize:], payload)
	return buf
}

func writeMuxFrame(w io.Writer, mu *sync.Mutex, channel uint32, kind byte, payload []byte) error {
	buf := encodeMuxFrame(channel, kind, payload)
	mu.Lock()
	defer mu.Unlock()
	return writeAll(w, buf)
}

// muxWriter sends frames on a connection through one compressor that lives as
// long as the connection. Screen redraws repeat themselves heavily from one
// frame to the next, so what goes over the network is a fraction of the text,
// which matters most when scrolling makes a program redraw the screen over and
// over on a slow link. The compressor is flushed after every frame, so a lone
// keystroke is not held back waiting for more data.
type muxWriter struct {
	mu  sync.Mutex
	out *bufio.Writer
	fw  *flate.Writer
}

func newMuxWriter(w io.Writer) *muxWriter {
	out := bufio.NewWriterSize(w, 32<<10)
	fw, _ := flate.NewWriter(out, flate.BestSpeed)
	return &muxWriter{out: out, fw: fw}
}

func (m *muxWriter) send(channel uint32, kind byte, payload []byte) error {
	frame := encodeMuxFrame(channel, kind, payload)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.fw.Write(frame); err != nil {
		return err
	}
	if err := m.fw.Flush(); err != nil {
		return err
	}
	return m.out.Flush()
}

// newMuxReader is the receiving end of muxWriter.
func newMuxReader(r io.Reader) io.Reader { return flate.NewReader(r) }

func readMuxFrame(r io.Reader) (channel uint32, kind byte, payload []byte, err error) {
	var hdr [muxHeaderSize]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[5:9])
	if n > maxFramePayload {
		return 0, 0, nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, 0, nil, err
	}
	return binary.BigEndian.Uint32(hdr[0:4]), hdr[4], payload, nil
}

// ---- host side ----

type muxServer struct {
	w    *muxWriter
	dial func(name string, timeout time.Duration) (net.Conn, error)

	mu      sync.Mutex
	streams map[uint32]*muxServerStream
	closed  bool
}

type muxServerStream struct {
	srv     *muxServer
	channel uint32
	in      chan []byte // client bytes waiting to be written to the session
	done    chan struct{}
	once    sync.Once
	connMu  sync.Mutex
	conn    net.Conn

	flowMu   sync.Mutex
	flowCond *sync.Cond
	inflight int // bytes sent to the interface and not yet acknowledged
}

// waitWindow blocks while the interface has too much unconsumed output. It
// reports false if the channel ended meanwhile.
func (st *muxServerStream) waitWindow() bool {
	st.flowMu.Lock()
	defer st.flowMu.Unlock()
	for st.inflight >= muxWindow {
		select {
		case <-st.done:
			return false
		default:
		}
		st.flowCond.Wait()
	}
	return true
}

func (st *muxServerStream) sent(n int) {
	st.flowMu.Lock()
	st.inflight += n
	st.flowMu.Unlock()
}

func (st *muxServerStream) acked(n int) {
	st.flowMu.Lock()
	st.inflight = max(0, st.inflight-n)
	st.flowMu.Unlock()
	st.flowCond.Broadcast()
}

// serveMux serves requests and session channels on one connection until the
// client goes away. dial connects to a session's host. closer, when set, is
// closed if the client falls silent, which ends the read below.
func serveMux(w io.Writer, r io.Reader, dial func(string, time.Duration) (net.Conn, error), closer io.Closer) error {
	s := &muxServer{w: newMuxWriter(w), dial: dial, streams: make(map[uint32]*muxServerStream)}
	r = newMuxReader(r)
	defer s.closeAll()
	var workers sync.WaitGroup
	defer workers.Wait()
	var lastRecv atomic.Int64
	lastRecv.Store(time.Now().UnixNano())
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	if closer != nil {
		interval, idleLimit := muxPingInterval.Load(), muxServerIdle.Load()
		go func() {
			tick := time.NewTicker(interval)
			defer tick.Stop()
			for {
				select {
				case <-tick.C:
					if time.Since(time.Unix(0, lastRecv.Load())) > idleLimit {
						_ = closer.Close() // the client is gone; end the read below
						return
					}
				case <-stopWatch:
					return
				}
			}
		}()
	}
	for {
		channel, kind, payload, err := readMuxFrame(r)
		lastRecv.Store(time.Now().UnixNano())
		if err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		switch kind {
		case muxFrameRPC:
			var request remoteRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				return fmt.Errorf("decode remote request: %w", err)
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				data, _ := json.Marshal(dispatchRemoteRequest(request))
				_ = s.send(0, muxFrameRPC, data)
			}()
		case muxFrameOpen:
			var open muxOpen
			if err := json.Unmarshal(payload, &open); err != nil {
				_ = s.send(channel, muxFrameClose, []byte("bad open request"))
				continue
			}
			s.open(channel, open)
		case muxFrameData:
			s.mu.Lock()
			st := s.streams[channel]
			s.mu.Unlock()
			if st != nil {
				// A session that is not taking input must not stop the whole
				// connection from being read: give it a while, then end it.
				timer := time.NewTimer(muxInputStall)
				select {
				case st.in <- payload:
				case <-st.done:
				case <-timer.C:
					st.close()
				}
				timer.Stop()
			}
		case muxFrameClose:
			s.mu.Lock()
			st := s.streams[channel]
			s.mu.Unlock()
			if st != nil {
				st.close()
			}
		case muxFrameAck:
			s.mu.Lock()
			st := s.streams[channel]
			s.mu.Unlock()
			if st != nil && len(payload) == 4 {
				st.acked(int(binary.BigEndian.Uint32(payload)))
			}
		}
	}
}

func (s *muxServer) send(channel uint32, kind byte, payload []byte) error {
	return s.w.send(channel, kind, payload)
}

func (s *muxServer) closeAll() {
	s.mu.Lock()
	s.closed = true
	streams := make([]*muxServerStream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	for _, st := range streams {
		st.close()
	}
}

// open attaches a channel to a session. The channel is registered before the
// session is dialed, so bytes the client sends right after opening are kept.
func (s *muxServer) open(channel uint32, open muxOpen) {
	if channel == 0 {
		return
	}
	if err := validSessionName(open.Name); err != nil {
		_ = s.send(channel, muxFrameClose, []byte(err.Error()))
		return
	}
	st := &muxServerStream{srv: s, channel: channel, in: make(chan []byte, muxStreamQueue), done: make(chan struct{})}
	st.flowCond = sync.NewCond(&st.flowMu)
	s.mu.Lock()
	if _, dup := s.streams[channel]; dup || s.closed {
		s.mu.Unlock()
		_ = s.send(channel, muxFrameClose, []byte("channel already in use"))
		return
	}
	s.streams[channel] = st
	s.mu.Unlock()
	go st.run(open)
}

func (st *muxServerStream) run(open muxOpen) {
	defer st.close()
	conn, err := st.srv.dial(open.Name, muxOpenTimeout)
	if err != nil {
		_ = st.srv.send(st.channel, muxFrameClose, []byte(fmt.Sprintf("no such session %q", open.Name)))
		return
	}
	st.connMu.Lock()
	st.conn = conn
	st.connMu.Unlock()
	select {
	case <-st.done: // closed while dialing
		conn.Close()
		return
	default:
	}
	// The host takes the first frame of a connection as the attach request, and
	// answers it with a snapshot of the screen.
	if err := writeFrame(conn, frameResize, resizePayload(max(1, open.Cols), max(1, open.Rows))); err != nil {
		_ = st.srv.send(st.channel, muxFrameClose, []byte(err.Error()))
		return
	}
	go func() { // session -> client
		defer st.close()
		buf := make([]byte, muxMaxData)
		for {
			if !st.waitWindow() {
				return
			}
			n, err := conn.Read(buf)
			if n > 0 {
				if st.srv.send(st.channel, muxFrameData, buf[:n]) != nil {
					return
				}
				st.sent(n)
			}
			if err != nil {
				return
			}
		}
	}()
	for { // client -> session
		select {
		case data := <-st.in:
			if err := writeAll(conn, data); err != nil {
				return
			}
		case <-st.done:
			return
		}
	}
}

// close ends the channel on both sides. It is safe to call more than once.
func (st *muxServerStream) close() {
	st.once.Do(func() {
		close(st.done)
		// Taking the lock makes a waiter that has checked done reach Wait first.
		st.flowMu.Lock()
		st.flowMu.Unlock()
		st.flowCond.Broadcast()
		st.connMu.Lock()
		if st.conn != nil {
			st.conn.Close()
		}
		st.connMu.Unlock()
		st.srv.mu.Lock()
		delete(st.srv.streams, st.channel)
		closed := st.srv.closed
		st.srv.mu.Unlock()
		if !closed {
			_ = st.srv.send(st.channel, muxFrameClose, nil)
		}
	})
}

// ---- interface side ----

// muxClient is the interface's end of a remote connection. It sends requests
// (call) and opens session channels (OpenStream).
type muxClient struct {
	rw      io.ReadWriteCloser
	w       *muxWriter
	r       io.Reader
	cleanup func() // extra teardown, such as stopping the ssh process

	mu        sync.Mutex
	nextID    uint64
	nextChan  uint32
	pending   map[uint64]chan remoteRPCResult
	streams   map[uint32]*muxStream
	err       error
	done      chan struct{}
	closeOnce sync.Once

	lastRecv    atomic.Int64 // when anything last arrived, unix nanoseconds
	releaseOnce sync.Once
}

func newMuxClient(rw io.ReadWriteCloser, r io.Reader, cleanup func()) *muxClient {
	c := &muxClient{
		rw: rw, w: newMuxWriter(rw), r: newMuxReader(r), cleanup: cleanup,
		pending: make(map[uint64]chan remoteRPCResult),
		streams: make(map[uint32]*muxStream),
		done:    make(chan struct{}),
	}
	c.lastRecv.Store(time.Now().UnixNano())
	go c.readLoop()
	go c.heartbeat(muxPingInterval.Load(), muxDeadAfter.Load())
	return c
}

// heartbeat keeps an idle link talking and drops one that has gone silent.
func (c *muxClient) heartbeat(interval, deadAfter time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			quiet := time.Since(time.Unix(0, c.lastRecv.Load()))
			switch {
			case quiet > deadAfter:
				c.fail(errors.New("connection timed out"))
				return
			case quiet >= interval:
				c.mu.Lock()
				c.nextID++
				id := c.nextID
				c.mu.Unlock()
				data, _ := json.Marshal(remoteRequest{ID: id, Op: "ping"})
				// The answer finds no waiting request and only proves the link works.
				_ = c.w.send(0, muxFrameRPC, data)
			}
		case <-c.done:
			return
		}
	}
}

// release closes the byte stream and stops what carried it. Closing is what
// unblocks a write stuck on a dead link.
func (c *muxClient) release() {
	c.releaseOnce.Do(func() {
		_ = c.rw.Close()
		if c.cleanup != nil {
			c.cleanup()
		}
	})
}

func (c *muxClient) readLoop() {
	for {
		channel, kind, payload, err := readMuxFrame(c.r)
		if err == nil {
			c.lastRecv.Store(time.Now().UnixNano())
		}
		if err != nil {
			if err == io.EOF {
				err = errors.New("remote helper disconnected")
			}
			c.fail(err)
			return
		}
		switch kind {
		case muxFrameRPC:
			var response remoteResponse
			if err := json.Unmarshal(payload, &response); err != nil {
				c.fail(fmt.Errorf("decode remote response: %w", err))
				return
			}
			c.mu.Lock()
			ch := c.pending[response.ID]
			delete(c.pending, response.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- remoteRPCResult{response: response}
			}
		case muxFrameData:
			c.mu.Lock()
			st := c.streams[channel]
			c.mu.Unlock()
			if st != nil {
				c.deliver(st, payload)
			}
		case muxFrameClose:
			c.mu.Lock()
			st := c.streams[channel]
			c.mu.Unlock()
			if st != nil {
				st.finish(string(payload))
			}
		}
	}
}

// muxStalledGrace is how long the read loop waits for a pane that is not
// reading. Nothing else on the connection is read meanwhile, so the wait counts
// as proof of life only briefly (it must stay under muxDeadAfter); a pane still
// not reading after it is detached, and the interface attaches again.
var muxStalledGrace = 10 * time.Second

// deliver hands a frame to a pane, waiting briefly if its queue is full.
func (c *muxClient) deliver(st *muxStream, payload []byte) {
	select {
	case st.in <- payload:
		return
	default:
	}
	tick := time.NewTicker(muxPingInterval.Load())
	defer tick.Stop()
	giveUp := time.Now().Add(muxStalledGrace)
	for {
		select {
		case st.in <- payload:
			return
		case <-st.done:
			return
		case now := <-tick.C:
			if !now.Before(giveUp) {
				st.finish("pane is not reading")
				return
			}
			c.lastRecv.Store(now.UnixNano())
		}
	}
}

// fail ends the connection: pending requests get err and every channel ends.
func (c *muxClient) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	pending := c.pending
	c.pending = make(map[uint64]chan remoteRPCResult)
	streams := make([]*muxStream, 0, len(c.streams))
	for _, st := range c.streams {
		streams = append(streams, st)
	}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- remoteRPCResult{err: err}
	}
	for _, st := range streams {
		st.finish(err.Error())
	}
	c.closeOnce.Do(func() { close(c.done) })
	go c.release()
}

// cause is why the connection ended, or empty while it is up.
func (c *muxClient) cause() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return ""
	}
	return c.err.Error()
}

// call sends a request and waits for its response.
func (c *muxClient) call(request remoteRequest) (remoteResponse, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return remoteResponse{}, err
	}
	c.nextID++
	request.ID = c.nextID
	result := make(chan remoteRPCResult, 1)
	c.pending[request.ID] = result
	c.mu.Unlock()

	data, err := json.Marshal(request)
	if err == nil {
		err = c.w.send(0, muxFrameRPC, data)
	}
	if err != nil {
		c.fail(fmt.Errorf("write remote request: %w", err))
	}
	select {
	case got := <-result:
		if got.err != nil {
			return remoteResponse{}, got.err
		}
		if got.response.Error != "" {
			return remoteResponse{}, fmt.Errorf("remote %s: %s", request.Op, got.response.Error)
		}
		return got.response, nil
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("remote helper disconnected")
		}
		return remoteResponse{}, err
	}
}

func (c *muxClient) close() {
	c.fail(errors.New("connection closed"))
	c.release()
}

// OpenStream attaches a new channel to a session. The host sends a snapshot of
// the screen right away, then its output.
func (c *muxClient) OpenStream(name string, cols, rows int) (*muxStream, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.nextChan++
	st := &muxStream{c: c, channel: c.nextChan, in: make(chan []byte, muxStreamQueue), done: make(chan struct{})}
	c.streams[st.channel] = st
	c.mu.Unlock()

	data, _ := json.Marshal(muxOpen{Name: name, Cols: cols, Rows: rows})
	if err := c.w.send(st.channel, muxFrameOpen, data); err != nil {
		c.fail(fmt.Errorf("open session channel: %w", err))
		return nil, err
	}
	return st, nil
}

// muxStream is one attached session on a remote connection. Reading yields the
// host's frame stream and writing sends frames to the host.
type muxStream struct {
	c       *muxClient
	channel uint32
	in      chan []byte
	done    chan struct{}
	once    sync.Once
	rbuf    []byte
	unacked int // bytes read and not yet acknowledged to the host

	mu     sync.Mutex
	reason string
}

func (s *muxStream) Read(p []byte) (int, error) {
	if len(s.rbuf) == 0 {
		select {
		case data := <-s.in:
			s.rbuf = data
		case <-s.done:
			select { // deliver what arrived before the end
			case data := <-s.in:
				s.rbuf = data
			default:
				return 0, s.endError()
			}
		}
	}
	n := copy(p, s.rbuf)
	s.rbuf = s.rbuf[n:]
	if s.unacked += n; s.unacked >= muxAckEvery {
		var ack [4]byte
		binary.BigEndian.PutUint32(ack[:], uint32(s.unacked))
		s.unacked = 0
		_ = s.c.w.send(s.channel, muxFrameAck, ack[:])
	}
	return n, nil
}

func (s *muxStream) endError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reason != "" {
		return errors.New(s.reason)
	}
	return io.EOF
}

func (s *muxStream) Write(p []byte) (int, error) {
	select {
	case <-s.done:
		return 0, s.endError()
	default:
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > muxMaxData {
			chunk = chunk[:muxMaxData]
		}
		if err := s.c.w.send(s.channel, muxFrameData, chunk); err != nil {
			s.c.fail(fmt.Errorf("write to remote session: %w", err))
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// finish ends the channel from the host's side or because the connection
// failed. reason is shown to whoever reads next; empty means a normal end.
func (s *muxStream) finish(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.reason = reason
		s.mu.Unlock()
		close(s.done)
		s.c.mu.Lock()
		delete(s.c.streams, s.channel)
		s.c.mu.Unlock()
	})
}

// Close detaches from the session; the session keeps running.
func (s *muxStream) Close() error {
	first := false
	s.once.Do(func() {
		first = true
		close(s.done)
		s.c.mu.Lock()
		delete(s.c.streams, s.channel)
		s.c.mu.Unlock()
	})
	if first {
		_ = s.c.w.send(s.channel, muxFrameClose, nil)
	}
	return nil
}
