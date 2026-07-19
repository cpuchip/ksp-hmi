package krpc

// stream.go — kRPC stream subscriptions. A stream turns a procedure call into a
// server-pushed feed: KRPC.AddStream(call, start) registers it and returns a
// Stream id; the server then pushes StreamUpdate messages (batched StreamResults
// keyed by id) down the separate stream TCP connection until KRPC.RemoveStream.
//
// The read-only MCP tools poll via direct RPC calls and do not need streams; the
// stream layer exists because it is core to the protocol and is what the future
// continuous-telemetry CAPCOM will ride on. It is fully unit-tested via dispatch
// (stream_test.go) without a live server.

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc/pb"
	"google.golang.org/protobuf/proto"
)

// ErrNoStream is returned by stream operations when the connection was opened
// without a stream channel (DialConfig.StreamPort == 0).
var ErrNoStream = errors.New("krpc: no stream connection (StreamPort was 0)")

// streamConn owns the stream TCP socket and the registry of active streams.
type streamConn struct {
	conn   net.Conn
	r      *bufio.Reader
	mu     sync.Mutex
	byID   map[uint64]*Stream
	closed chan struct{}
	once   sync.Once
}

// Stream is a single server-pushed telemetry feed. Read the latest value with
// Value; block for the first push with WaitReady. Decode the bytes with the
// Decode* helper matching the streamed procedure's return type.
type Stream struct {
	id    uint64
	mu    sync.Mutex
	value []byte
	err   error
	got   bool
	ready chan struct{}
	once  sync.Once
}

// ID returns the kRPC stream id.
func (s *Stream) ID() uint64 { return s.id }

// Value returns the latest pushed value, whether any update has arrived, and any
// error reported for the streamed procedure.
func (s *Stream) Value() (data []byte, updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.got, s.err
}

// WaitReady blocks until the first update arrives or timeout elapses.
func (s *Stream) WaitReady(timeout time.Duration) error {
	select {
	case <-s.ready:
		_, _, err := s.Value()
		return err
	case <-time.After(timeout):
		return fmt.Errorf("krpc: stream %d: no update within %s", s.id, timeout)
	}
}

func (s *Stream) set(res *pb.ProcedureResult) {
	s.mu.Lock()
	if res.Error != nil {
		s.err = &RPCError{proc: "stream", e: res.Error}
	} else {
		s.value = res.Value
		s.err = nil
	}
	s.got = true
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
}

// handshakeStream opens the stream socket and performs the STREAM handshake,
// which echoes back the 16-byte client identifier from the RPC handshake so the
// server pairs the two connections.
func (c *Conn) handshakeStream(deadline time.Time) error {
	addr := net.JoinHostPort(c.cfg.Host, itoa(c.cfg.StreamPort))
	conn, err := net.DialTimeout("tcp", addr, c.cfg.Timeout)
	if err != nil {
		return fmt.Errorf("krpc: dial stream %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	req := &pb.ConnectionRequest{
		Type:             pb.ConnectionRequest_STREAM,
		ClientIdentifier: c.clientID,
	}
	if err := writeMessage(conn, req); err != nil {
		conn.Close()
		return fmt.Errorf("krpc: send stream handshake: %w", err)
	}
	r := bufio.NewReader(conn)
	var resp pb.ConnectionResponse
	if err := readMessage(r, &resp); err != nil {
		conn.Close()
		return fmt.Errorf("krpc: read stream handshake: %w", err)
	}
	if resp.Status != pb.ConnectionResponse_OK {
		conn.Close()
		return fmt.Errorf("krpc: stream handshake rejected: %s (%s)", resp.Status, resp.Message)
	}
	_ = conn.SetDeadline(time.Time{})

	sc := &streamConn{conn: conn, r: r, byID: map[uint64]*Stream{}, closed: make(chan struct{})}
	c.stream = sc
	go sc.readLoop()
	return nil
}

// readLoop consumes StreamUpdate messages until the connection closes.
func (sc *streamConn) readLoop() {
	for {
		var upd pb.StreamUpdate
		if err := readMessage(sc.r, &upd); err != nil {
			select {
			case <-sc.closed: // expected on Close
			default:
			}
			return
		}
		sc.dispatch(&upd)
	}
}

// dispatch fans a StreamUpdate out to the registered streams. Separated from the
// socket so it is unit-testable without a live server.
func (sc *streamConn) dispatch(upd *pb.StreamUpdate) {
	sc.mu.Lock()
	targets := make([]struct {
		s   *Stream
		res *pb.ProcedureResult
	}, 0, len(upd.Results))
	for _, res := range upd.Results {
		if res == nil || res.Result == nil {
			continue
		}
		if s := sc.byID[res.Id]; s != nil {
			targets = append(targets, struct {
				s   *Stream
				res *pb.ProcedureResult
			}{s, res.Result})
		}
	}
	sc.mu.Unlock()
	for _, t := range targets {
		t.s.set(t.res)
	}
}

func (sc *streamConn) close() {
	sc.once.Do(func() {
		close(sc.closed)
		sc.conn.Close()
	})
}

// AddStream subscribes to service.procedure with the given pre-encoded arguments
// and returns a Stream that receives the server's pushed updates. Decode each
// value with the Decode* helper for the procedure's return type.
func (c *Conn) AddStream(service, procedure string, args ...[]byte) (*Stream, error) {
	if c.stream == nil {
		return nil, ErrNoStream
	}
	inner := &pb.ProcedureCall{Service: service, Procedure: procedure}
	for i, a := range args {
		inner.Arguments = append(inner.Arguments, &pb.Argument{Position: uint32(i), Value: a})
	}
	// A PROCEDURE_CALL-typed argument is encoded as the raw marshaled message
	// (message types serialize directly, with no naked-value wrapping).
	callBytes, err := proto.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("krpc: marshal stream call: %w", err)
	}
	b, err := c.Call("KRPC", "AddStream", callBytes, EncodeBool(true))
	if err != nil {
		return nil, err
	}
	var sm pb.Stream
	if err := proto.Unmarshal(b, &sm); err != nil {
		return nil, fmt.Errorf("krpc: decode Stream: %w", err)
	}
	s := &Stream{id: sm.Id, ready: make(chan struct{})}
	sc := c.stream
	sc.mu.Lock()
	// If the id already exists (server can reuse an id for an identical call),
	// reuse the existing Stream so both callers see updates.
	if existing := sc.byID[sm.Id]; existing != nil {
		sc.mu.Unlock()
		return existing, nil
	}
	sc.byID[sm.Id] = s
	sc.mu.Unlock()
	return s, nil
}

// RemoveStream unsubscribes a stream.
func (c *Conn) RemoveStream(s *Stream) error {
	if c.stream == nil {
		return ErrNoStream
	}
	c.stream.mu.Lock()
	delete(c.stream.byID, s.id)
	c.stream.mu.Unlock()
	_, err := c.Call("KRPC", "RemoveStream", EncodeUint64(s.id))
	return err
}
