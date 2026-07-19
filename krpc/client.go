// Package krpc is a minimal, honest Go client for kRPC's protobuf-over-TCP wire
// protocol (https://krpc.github.io/krpc/). It implements the RPC and stream
// connection handshakes, the length-prefixed Request/Response exchange, argument
// and return-value serialization, KRPC.GetServices self-discovery, and stream
// subscriptions — enough to drive a curated, read-only copilot tool surface
// (see cmd/ksp-mcp), not a full generated-services client.
//
// Design: a dynamic call layer — Call(service, procedure, args...) invokes any
// procedure by name with pre-encoded arguments and returns the raw result bytes
// — plus a thin set of typed helpers (spacecenter.go) for exactly what the MCP
// tools need. The command wave adds mutating calls with the same Call layer and
// the same Encode* argument helpers; nothing here needs reshaping for it.
package krpc

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc/pb"
	"google.golang.org/protobuf/proto"
)

// Default connection parameters (kRPC's in-game server defaults).
const (
	DefaultHost       = "127.0.0.1"
	DefaultRPCPort    = 50000
	DefaultStreamPort = 50001
	DefaultClientName = "ksp-mcp"
)

// DialConfig configures a connection. The zero value is usable via its defaults;
// prefer DialDefault for the common case.
type DialConfig struct {
	Host       string        // default 127.0.0.1
	RPCPort    int           // default 50000
	StreamPort int           // default 50001; set to 0 to skip the stream connection
	ClientName string        // default "ksp-mcp"; shown in kRPC's in-game client list
	Timeout    time.Duration // dial + handshake deadline; default 10s
}

func (c *DialConfig) withDefaults() {
	if c.Host == "" {
		c.Host = DefaultHost
	}
	if c.RPCPort == 0 {
		c.RPCPort = DefaultRPCPort
	}
	if c.ClientName == "" {
		c.ClientName = DefaultClientName
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	// StreamPort intentionally not defaulted here: 0 is a meaningful "no stream".
}

// Conn is a live kRPC connection. It is safe for concurrent use: RPC calls are
// serialized (kRPC's RPC channel is strictly one request/response at a time),
// and the stream connection is read by its own goroutine.
type Conn struct {
	cfg DialConfig

	rpc   net.Conn
	rpcR  *bufio.Reader
	rpcMu sync.Mutex // guards a single in-flight Request/Response on rpc

	clientID []byte // 16-byte identifier from the RPC handshake

	// Discovery: populated once (lazily) from KRPC.GetServices and cached.
	// enums maps "Service.EnumName" -> {value: name} so enum codes render as
	// real names version-robustly. procs maps "Service.procedure" -> Procedure
	// so a return value is decoded by its DECLARED type (no float-vs-double
	// guessing). Guarded by discMu.
	discMu sync.Mutex
	enums  map[string]map[int32]string
	procs  map[string]*pb.Procedure

	stream *streamConn // nil if StreamPort==0 or the stream handshake was skipped
}

// DialDefault connects to a kRPC server on the given host using default ports.
// A blank host means 127.0.0.1.
func DialDefault(host string) (*Conn, error) {
	return Dial(DialConfig{Host: host, StreamPort: DefaultStreamPort})
}

// Dial opens the RPC connection (and, unless StreamPort==0, the stream
// connection), performs the handshakes, and returns a ready Conn.
func Dial(cfg DialConfig) (*Conn, error) {
	cfg.withDefaults()
	deadline := time.Now().Add(cfg.Timeout)

	rpcAddr := net.JoinHostPort(cfg.Host, itoa(cfg.RPCPort))
	rpc, err := net.DialTimeout("tcp", rpcAddr, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("krpc: dial rpc %s: %w", rpcAddr, err)
	}
	c := &Conn{cfg: cfg, rpc: rpc, rpcR: bufio.NewReader(rpc)}

	if err := c.handshakeRPC(deadline); err != nil {
		rpc.Close()
		return nil, err
	}

	if cfg.StreamPort != 0 {
		if err := c.handshakeStream(deadline); err != nil {
			rpc.Close()
			return nil, err
		}
	}
	return c, nil
}

// handshakeRPC sends the RPC ConnectionRequest and validates the response,
// capturing the client identifier used by the stream handshake.
func (c *Conn) handshakeRPC(deadline time.Time) error {
	_ = c.rpc.SetDeadline(deadline)
	req := &pb.ConnectionRequest{
		Type:       pb.ConnectionRequest_RPC,
		ClientName: c.cfg.ClientName,
	}
	if err := writeMessage(c.rpc, req); err != nil {
		return fmt.Errorf("krpc: send rpc handshake: %w", err)
	}
	var resp pb.ConnectionResponse
	if err := readMessage(c.rpcR, &resp); err != nil {
		return fmt.Errorf("krpc: read rpc handshake: %w", err)
	}
	if resp.Status != pb.ConnectionResponse_OK {
		return fmt.Errorf("krpc: rpc handshake rejected: %s (%s)", resp.Status, resp.Message)
	}
	c.clientID = resp.ClientIdentifier
	_ = c.rpc.SetDeadline(time.Time{}) // clear; per-call deadlines handle timeouts
	return nil
}

// ClientID returns kRPC's 16-byte client identifier for this connection.
func (c *Conn) ClientID() []byte { return c.clientID }

// ClientGUID returns the client identifier as a canonical GUID string.
func (c *Conn) ClientGUID() string { return formatGUID(c.clientID) }

// Close tears down the stream and RPC connections.
func (c *Conn) Close() error {
	if c.stream != nil {
		c.stream.close()
	}
	if c.rpc != nil {
		return c.rpc.Close()
	}
	return nil
}

// Call invokes service.procedure with the given pre-encoded positional arguments
// (arg i is sent at position i) and returns the raw result bytes, which the
// caller decodes with the matching Decode* helper. This is the dynamic call
// layer every typed helper and every future command builds on.
func (c *Conn) Call(service, procedure string, args ...[]byte) ([]byte, error) {
	call := &pb.ProcedureCall{Service: service, Procedure: procedure}
	for i, a := range args {
		call.Arguments = append(call.Arguments, &pb.Argument{Position: uint32(i), Value: a})
	}
	req := &pb.Request{Calls: []*pb.ProcedureCall{call}}

	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()

	_ = c.rpc.SetDeadline(time.Now().Add(c.callTimeout()))
	defer c.rpc.SetDeadline(time.Time{})

	if err := writeMessage(c.rpc, req); err != nil {
		return nil, fmt.Errorf("krpc: send %s.%s: %w", service, procedure, err)
	}
	var resp pb.Response
	if err := readMessage(c.rpcR, &resp); err != nil {
		return nil, fmt.Errorf("krpc: read %s.%s: %w", service, procedure, err)
	}
	if resp.Error != nil {
		return nil, &RPCError{proc: service + "." + procedure, e: resp.Error}
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("krpc: %s.%s returned no result", service, procedure)
	}
	r := resp.Results[0]
	if r.Error != nil {
		return nil, &RPCError{proc: service + "." + procedure, e: r.Error}
	}
	return r.Value, nil
}

func (c *Conn) callTimeout() time.Duration {
	if c.cfg.Timeout > 0 {
		return c.cfg.Timeout
	}
	return 10 * time.Second
}

// ---- self-discovery ----

// Services fetches the full service/procedure/enumeration catalogue via
// KRPC.GetServices. The return is a whole Services protobuf message (not a naked
// value).
func (c *Conn) Services() (*pb.Services, error) {
	b, err := c.Call("KRPC", "GetServices")
	if err != nil {
		return nil, err
	}
	var svcs pb.Services
	if err := proto.Unmarshal(b, &svcs); err != nil {
		return nil, fmt.Errorf("krpc: decode Services: %w", err)
	}
	return &svcs, nil
}

// ensureDiscovery loads the enumeration and procedure catalogues once from a
// single KRPC.GetServices call and caches them. Best-effort: a failure leaves the
// caches nil and callers degrade (raw enum ints, length-based scalar decode).
func (c *Conn) ensureDiscovery() {
	c.discMu.Lock()
	defer c.discMu.Unlock()
	if c.enums != nil || c.procs != nil {
		return
	}
	svcs, err := c.Services()
	if err != nil {
		return
	}
	enums := map[string]map[int32]string{}
	procs := map[string]*pb.Procedure{}
	for _, s := range svcs.Services {
		for _, e := range s.Enumerations {
			m := make(map[int32]string, len(e.Values))
			for _, v := range e.Values {
				m[v.Value] = v.Name
			}
			enums[s.Name+"."+e.Name] = m
		}
		for _, p := range s.Procedures {
			procs[s.Name+"."+p.Name] = p
		}
	}
	c.enums, c.procs = enums, procs
}

// Discover forces the service catalogue to load now (rather than on first use)
// and reports how many services/procedures/enumerations were found. Handy as a
// self-check that discovery works against a live server.
func (c *Conn) Discover() (services, procedures, enumerations int, err error) {
	svcs, err := c.Services()
	if err != nil {
		return 0, 0, 0, err
	}
	c.ensureDiscovery()
	for _, s := range svcs.Services {
		procedures += len(s.Procedures)
		enumerations += len(s.Enumerations)
	}
	return len(svcs.Services), procedures, enumerations, nil
}

// enumName returns the name for an enumeration value. If it can't be resolved it
// returns the decimal value so the caller always has something honest to show.
func (c *Conn) enumName(enumKey string, value int32) string {
	c.ensureDiscovery()
	if m, ok := c.enums[enumKey]; ok {
		if name, ok := m[value]; ok {
			return name
		}
	}
	return itoa32(value)
}

// returnCode returns the declared return TypeCode for a procedure, if known.
func (c *Conn) returnCode(service, proc string) (pb.Type_TypeCode, bool) {
	c.ensureDiscovery()
	p := c.procs[service+"."+proc]
	if p == nil || p.ReturnType == nil {
		return pb.Type_NONE, false
	}
	return p.ReturnType.Code, true
}

// ---- discovery-driven typed call helpers ----
//
// These sit on top of Call and decode the result by the procedure's DECLARED
// return type where known — so a numeric field decodes correctly whether kRPC
// declares it float or double. When discovery is unavailable, callScalar falls
// back to decoding by byte length (8=double, 4=float, else varint).

// callScalar invokes a numeric-returning procedure and returns a float64.
func (c *Conn) callScalar(service, proc string, args ...[]byte) (float64, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return 0, err
	}
	code, known := c.returnCode(service, proc)
	if !known {
		return decodeScalarByLen(b)
	}
	switch code {
	case pb.Type_DOUBLE:
		return DecodeDouble(b)
	case pb.Type_FLOAT:
		v, e := DecodeFloat(b)
		return float64(v), e
	case pb.Type_SINT32, pb.Type_SINT64:
		v, e := DecodeSint64(b)
		return float64(v), e
	case pb.Type_UINT32, pb.Type_UINT64:
		v, e := DecodeUint64(b)
		return float64(v), e
	default:
		return decodeScalarByLen(b)
	}
}

// decodeScalarByLen is the discovery-unavailable fallback: kRPC's numeric returns
// are fixed-width for float/double, so byte length disambiguates them; a varint
// is read as an unsigned integer.
func decodeScalarByLen(b []byte) (float64, error) {
	switch len(b) {
	case 8:
		return DecodeDouble(b)
	case 4:
		v, e := DecodeFloat(b)
		return float64(v), e
	default:
		v, e := DecodeUint64(b)
		return float64(v), e
	}
}

// callString invokes a string-returning procedure.
func (c *Conn) callString(service, proc string, args ...[]byte) (string, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return "", err
	}
	return DecodeString(b)
}

// callObject invokes an object-returning procedure and returns the object id
// (0 == null reference).
func (c *Conn) callObject(service, proc string, args ...[]byte) (uint64, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return 0, err
	}
	return DecodeObject(b)
}

// callEnum invokes an enum-returning procedure and returns the value plus its
// resolved name using the given enum key ("Service.EnumName").
func (c *Conn) callEnum(service, proc, enumKey string, args ...[]byte) (int32, string, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return 0, "", err
	}
	v, err := DecodeEnum(b)
	if err != nil {
		return 0, "", err
	}
	return v, c.enumName(enumKey, v), nil
}

// callInt invokes an integer-returning procedure (sint32).
func (c *Conn) callInt(service, proc string, args ...[]byte) (int32, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return 0, err
	}
	return DecodeSint32(b)
}

// callStringList invokes a list<string>-returning procedure.
func (c *Conn) callStringList(service, proc string, args ...[]byte) ([]string, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return nil, err
	}
	items, err := DecodeList(b)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, err := DecodeString(it)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// callObjectList invokes a list<object>-returning procedure, returning object ids.
func (c *Conn) callObjectList(service, proc string, args ...[]byte) ([]uint64, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return nil, err
	}
	items, err := DecodeList(b)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(items))
	for _, it := range items {
		id, err := DecodeObject(it)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// ---- KRPC service conveniences (used by game_state) ----

// Status returns kRPC server status (version, byte counters, rates).
func (c *Conn) Status() (*pb.Status, error) {
	b, err := c.Call("KRPC", "GetStatus")
	if err != nil {
		return nil, err
	}
	var st pb.Status
	if err := proto.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("krpc: decode Status: %w", err)
	}
	return &st, nil
}

// CurrentGameScene returns the active game scene and its resolved name (e.g.
// "Flight", "SpaceCenter", "TrackingStation").
func (c *Conn) CurrentGameScene() (value int32, name string, err error) {
	b, err := c.Call("KRPC", "get_CurrentGameScene")
	if err != nil {
		return 0, "", err
	}
	v, err := DecodeEnum(b)
	if err != nil {
		return 0, "", err
	}
	return v, c.enumName("KRPC.GameScene", v), nil
}

// Paused reports whether the game is paused.
func (c *Conn) Paused() (bool, error) {
	b, err := c.Call("KRPC", "get_Paused")
	if err != nil {
		return false, err
	}
	return DecodeBool(b)
}

// RPCError wraps a kRPC-reported procedure error. It is returned by Call when
// the server rejects a request or a procedure throws in-game — e.g. reading the
// active vessel when not in flight — so callers can degrade gracefully.
type RPCError struct {
	proc string
	e    *pb.Error
}

func (r *RPCError) Error() string {
	name := r.e.Name
	if name == "" {
		name = "error"
	}
	if r.e.Description != "" {
		return fmt.Sprintf("%s: %s: %s", r.proc, name, r.e.Description)
	}
	return fmt.Sprintf("%s: %s", r.proc, name)
}

// small allocation-free int formatters (avoid pulling strconv into hot paths and
// keep the package's surface tidy)
func itoa(v int) string     { return itoa64(int64(v)) }
func itoa32(v int32) string { return itoa64(int64(v)) }
func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
