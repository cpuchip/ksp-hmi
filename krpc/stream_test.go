package krpc

import (
	"testing"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc/pb"
)

// TestStreamDispatch exercises the fan-out logic directly, no socket involved.
func TestStreamDispatch(t *testing.T) {
	sc := &streamConn{byID: map[uint64]*Stream{}}
	s := &Stream{id: 5, ready: make(chan struct{})}
	sc.byID[5] = s

	sc.dispatch(&pb.StreamUpdate{Results: []*pb.StreamResult{
		{Id: 5, Result: &pb.ProcedureResult{Value: EncodeDouble(3.5)}},
		{Id: 99, Result: &pb.ProcedureResult{Value: EncodeDouble(1)}}, // unknown id: ignored
	}})

	val, got, err := s.Value()
	if !got || err != nil {
		t.Fatalf("stream not updated: got=%v err=%v", got, err)
	}
	if v, _ := DecodeDouble(val); v != 3.5 {
		t.Errorf("stream value = %v, want 3.5", v)
	}

	// A result carrying an error is surfaced on the stream.
	se := &Stream{id: 7, ready: make(chan struct{})}
	sc.byID[7] = se
	sc.dispatch(&pb.StreamUpdate{Results: []*pb.StreamResult{
		{Id: 7, Result: &pb.ProcedureResult{Error: &pb.Error{Name: "Boom"}}},
	}})
	if _, _, err := se.Value(); err == nil {
		t.Error("expected stream error, got nil")
	}
}

// TestStreamRoundTrip drives AddStream -> server push -> Value across the wire.
func TestStreamRoundTrip(t *testing.T) {
	const streamID = 77
	f := startFakeKRPC(t, func(req *pb.Request) *pb.Response {
		call := req.Calls[0]
		switch call.Service + "." + call.Procedure {
		case "KRPC.AddStream":
			return msgResponse(t, &pb.Stream{Id: streamID})
		case "KRPC.RemoveStream":
			return valueResponse(nil)
		default:
			return valueResponse(EncodeDouble(0))
		}
	})
	conn := dialFake(t, f, true)

	s, err := conn.AddStream("SpaceCenter", "get_UT")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != streamID {
		t.Errorf("stream id = %d, want %d", s.ID(), streamID)
	}

	f.waitStreamConn(t)
	f.pushStream(streamID, EncodeDouble(999.5))

	if err := s.WaitReady(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	val, _, _ := s.Value()
	if v, _ := DecodeDouble(val); v != 999.5 {
		t.Errorf("streamed value = %v, want 999.5", v)
	}

	if err := conn.RemoveStream(s); err != nil {
		t.Errorf("RemoveStream: %v", err)
	}
}

// TestAddStreamWithoutStreamConn: AddStream errors clearly when no stream channel.
func TestAddStreamWithoutStreamConn(t *testing.T) {
	f := startFakeKRPC(t, func(*pb.Request) *pb.Response { return valueResponse(nil) })
	conn := dialFake(t, f, false) // no stream port
	if _, err := conn.AddStream("SpaceCenter", "get_UT"); err != ErrNoStream {
		t.Errorf("AddStream err = %v, want ErrNoStream", err)
	}
}
