package krpc

// client_test.go — a hermetic in-process fake kRPC server that speaks the real
// framing + handshake, so the RPC handshake, the dynamic Call layer, discovery,
// and (in stream_test.go) stream subscriptions are exercised end-to-end without
// the game. The live game is the other oracle; this one runs in CI anywhere.

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc/pb"
	"google.golang.org/protobuf/proto"
)

func newTestReader(b []byte) *bufio.Reader { return bufio.NewReader(bytes.NewReader(b)) }

// ---- fake kRPC server ----

type fakeKRPC struct {
	rpcLn, streamLn net.Listener
	handle          func(*pb.Request) *pb.Response
	clientID        []byte

	mu          sync.Mutex
	streamConns []net.Conn
}

func startFakeKRPC(t *testing.T, handle func(*pb.Request) *pb.Response) *fakeKRPC {
	t.Helper()
	rpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("rpc listen: %v", err)
	}
	streamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("stream listen: %v", err)
	}
	f := &fakeKRPC{
		rpcLn:    rpcLn,
		streamLn: streamLn,
		handle:   handle,
		clientID: bytes.Repeat([]byte{0xAB}, 16),
	}
	go f.serve(rpcLn, f.handleRPCConn)
	go f.serve(streamLn, f.handleStreamConn)
	t.Cleanup(func() { rpcLn.Close(); streamLn.Close() })
	return f
}

func (f *fakeKRPC) host() string    { return "127.0.0.1" }
func (f *fakeKRPC) rpcPort() int    { return f.rpcLn.Addr().(*net.TCPAddr).Port }
func (f *fakeKRPC) streamPort() int { return f.streamLn.Addr().(*net.TCPAddr).Port }

func (f *fakeKRPC) serve(ln net.Listener, h func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go h(conn)
	}
}

func (f *fakeKRPC) handleRPCConn(conn net.Conn) {
	r := bufio.NewReader(conn)
	var req pb.ConnectionRequest
	if err := readMessage(r, &req); err != nil {
		return
	}
	if req.Type != pb.ConnectionRequest_RPC {
		_ = writeMessage(conn, &pb.ConnectionResponse{Status: pb.ConnectionResponse_WRONG_TYPE})
		return
	}
	_ = writeMessage(conn, &pb.ConnectionResponse{Status: pb.ConnectionResponse_OK, ClientIdentifier: f.clientID})
	for {
		var rq pb.Request
		if err := readMessage(r, &rq); err != nil {
			return
		}
		_ = writeMessage(conn, f.handle(&rq))
	}
}

func (f *fakeKRPC) handleStreamConn(conn net.Conn) {
	r := bufio.NewReader(conn)
	var req pb.ConnectionRequest
	if err := readMessage(r, &req); err != nil {
		return
	}
	if req.Type != pb.ConnectionRequest_STREAM || !bytes.Equal(req.ClientIdentifier, f.clientID) {
		_ = writeMessage(conn, &pb.ConnectionResponse{Status: pb.ConnectionResponse_WRONG_TYPE})
		return
	}
	_ = writeMessage(conn, &pb.ConnectionResponse{Status: pb.ConnectionResponse_OK})
	f.mu.Lock()
	f.streamConns = append(f.streamConns, conn)
	f.mu.Unlock()
	io.Copy(io.Discard, r) // block until the client closes the socket
}

func (f *fakeKRPC) pushStream(id uint64, value []byte) {
	upd := &pb.StreamUpdate{Results: []*pb.StreamResult{{Id: id, Result: &pb.ProcedureResult{Value: value}}}}
	f.mu.Lock()
	conns := append([]net.Conn(nil), f.streamConns...)
	f.mu.Unlock()
	for _, c := range conns {
		_ = writeMessage(c, upd)
	}
}

func (f *fakeKRPC) waitStreamConn(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		f.mu.Lock()
		n := len(f.streamConns)
		f.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fake server: no stream connection registered")
}

// response builders
func valueResponse(v []byte) *pb.Response {
	return &pb.Response{Results: []*pb.ProcedureResult{{Value: v}}}
}
func errorResponse(name, desc string) *pb.Response {
	return &pb.Response{Results: []*pb.ProcedureResult{{Error: &pb.Error{Name: name, Description: desc}}}}
}
func msgResponse(t *testing.T, m proto.Message) *pb.Response {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return valueResponse(b)
}

func dialFake(t *testing.T, f *fakeKRPC, withStream bool) *Conn {
	t.Helper()
	cfg := DialConfig{Host: f.host(), RPCPort: f.rpcPort(), Timeout: 3 * time.Second}
	if withStream {
		cfg.StreamPort = f.streamPort()
	}
	conn, err := Dial(cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// ---- tests ----

func TestHandshakeAndCall(t *testing.T) {
	f := startFakeKRPC(t, func(*pb.Request) *pb.Response {
		return valueResponse(EncodeDouble(42.0))
	})
	conn := dialFake(t, f, true)

	if len(conn.ClientID()) != 16 {
		t.Errorf("ClientID len = %d, want 16", len(conn.ClientID()))
	}
	if conn.ClientGUID() == "" {
		t.Error("ClientGUID empty")
	}
	b, err := conn.Call("SpaceCenter", "get_UT")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := DecodeDouble(b); v != 42.0 {
		t.Errorf("get_UT = %v, want 42", v)
	}
}

// servicesHandler is a small fake SpaceCenter used by several tests.
func servicesHandler(t *testing.T) func(*pb.Request) *pb.Response {
	svcs := &pb.Services{Services: []*pb.Service{{
		Name: "SpaceCenter",
		Procedures: []*pb.Procedure{
			{Name: "get_ActiveVessel", ReturnType: &pb.Type{Code: pb.Type_CLASS, Service: "SpaceCenter", Name: "Vessel"}},
			{Name: "Vessel_get_Name", ReturnType: &pb.Type{Code: pb.Type_STRING}},
			{Name: "Vessel_get_MET", ReturnType: &pb.Type{Code: pb.Type_DOUBLE}},
			{Name: "Vessel_get_Situation", ReturnType: &pb.Type{Code: pb.Type_ENUMERATION, Service: "SpaceCenter", Name: "VesselSituation"}},
		},
		Enumerations: []*pb.Enumeration{{
			Name: "VesselSituation",
			Values: []*pb.EnumerationValue{
				{Name: "Landed", Value: 3},
				{Name: "Orbiting", Value: 4},
			},
		}},
	}}}
	return func(req *pb.Request) *pb.Response {
		call := req.Calls[0]
		switch call.Service + "." + call.Procedure {
		case "KRPC.GetServices":
			return msgResponse(t, svcs)
		case "SpaceCenter.Vessel_get_MET":
			return valueResponse(EncodeDouble(360.5))
		case "SpaceCenter.Vessel_get_Situation":
			return valueResponse(EncodeEnum(4)) // Orbiting
		default:
			return errorResponse("ArgumentException", "unexpected "+call.Procedure)
		}
	}
}

func TestDiscovery(t *testing.T) {
	f := startFakeKRPC(t, servicesHandler(t))
	conn := dialFake(t, f, false)

	nSvc, nProc, nEnum, err := conn.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if nSvc != 1 || nProc != 4 || nEnum != 1 {
		t.Errorf("Discover = %d svc, %d proc, %d enum; want 1,4,1", nSvc, nProc, nEnum)
	}

	// callScalar decodes by the DECLARED type (DOUBLE here).
	met, err := conn.callScalar("SpaceCenter", "Vessel_get_MET")
	if err != nil || met != 360.5 {
		t.Errorf("Vessel_get_MET = %v, %v; want 360.5", met, err)
	}

	// callEnum resolves the value's name from the discovered enumeration.
	v, name, err := conn.callEnum("SpaceCenter", "Vessel_get_Situation", "SpaceCenter.VesselSituation")
	if err != nil || v != 4 || name != "Orbiting" {
		t.Errorf("Situation = %d/%q, %v; want 4/Orbiting", v, name, err)
	}
}

func TestActiveVesselNoVessel(t *testing.T) {
	// A procedure error (not in flight) maps to ErrNoVessel.
	f := startFakeKRPC(t, func(req *pb.Request) *pb.Response {
		return errorResponse("InvalidOperationException", "There is no active vessel")
	})
	conn := dialFake(t, f, false)
	if _, err := conn.ActiveVessel(); !errors.Is(err, ErrNoVessel) {
		t.Errorf("ActiveVessel err = %v, want ErrNoVessel", err)
	}
}

func TestActiveVesselNullObject(t *testing.T) {
	// A null object reference (id 0) also maps to ErrNoVessel.
	f := startFakeKRPC(t, func(req *pb.Request) *pb.Response {
		return valueResponse(EncodeObject(0))
	})
	conn := dialFake(t, f, false)
	if _, err := conn.ActiveVessel(); !errors.Is(err, ErrNoVessel) {
		t.Errorf("ActiveVessel(0) err = %v, want ErrNoVessel", err)
	}
}

func TestActiveVesselOK(t *testing.T) {
	f := startFakeKRPC(t, func(req *pb.Request) *pb.Response {
		return valueResponse(EncodeObject(1234))
	})
	conn := dialFake(t, f, false)
	id, err := conn.ActiveVessel()
	if err != nil || id != 1234 {
		t.Errorf("ActiveVessel = %d, %v; want 1234", id, err)
	}
}
