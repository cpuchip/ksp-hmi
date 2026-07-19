package krpc

// wire.go — kRPC's length-prefixed protobuf framing and its "naked value"
// serialization. These are the two facts that are NOT the standard protobuf
// message encoding and so cannot be delegated to proto.Marshal:
//
//  1. Framing: every message on the TCP stream is prefixed with its byte length,
//     the length itself encoded as a Protocol Buffers varint (base-128). Verified
//     against kRPC doc communication-protocols/tcpip.rst (fetched 2026-07-19).
//
//  2. Values: the bytes inside Argument.value and ProcedureResult.value are a
//     SINGLE value encoded in protobuf's on-the-wire form WITHOUT a field tag —
//     e.g. a double is 8 raw little-endian bytes, a uint64 is a bare varint, a
//     string is a varint length followed by UTF-8. Enumerations are encoded as
//     sint32 (zig-zag), and a class/object reference is its uint64 object id
//     (0 == null). Verified byte-for-byte against the reference Python client
//     (krpc/encoder.py, krpc/decoder.py) and its test vectors — see wire_test.go.
//
// The primitives here are the whole "argument encoding" surface; the future
// command wave encodes its inputs with exactly these helpers.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/cpuchip/ksp-hmi/krpc/pb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// ---- framing ----

// frame encodes m and prepends its length as a protobuf varint, returning the
// ready-to-write bytes (length prefix followed by the message).
func frame(m proto.Message) ([]byte, error) {
	body, err := proto.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal %T: %w", m, err)
	}
	out := protowire.AppendVarint(nil, uint64(len(body)))
	return append(out, body...), nil
}

// writeMessage frames and writes m in a single Write.
func writeMessage(w io.Writer, m proto.Message) error {
	b, err := frame(m)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// readMessage reads one length-prefixed message and unmarshals it into m. The
// protobuf varint length prefix is exactly an unsigned LEB128, so the stdlib
// binary.ReadUvarint decodes it directly from the buffered reader.
func readMessage(r *bufio.Reader, m proto.Message) error {
	size, err := binary.ReadUvarint(r)
	if err != nil {
		return err
	}
	if size > maxMessageBytes {
		return fmt.Errorf("krpc: framed message too large (%d bytes)", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, m)
}

// maxMessageBytes caps a single framed message to guard against a desynced
// stream reading a bogus length. GetServices on a heavily-modded install is a
// few hundred KB; 64 MiB is comfortably beyond any real message.
const maxMessageBytes = 64 << 20

// ---- naked value encoders (no field tag) ----

// EncodeDouble encodes a float64 as kRPC does: 8 raw little-endian IEEE-754 bytes.
func EncodeDouble(v float64) []byte { return protowire.AppendFixed64(nil, math.Float64bits(v)) }

// EncodeFloat encodes a float32 as 4 raw little-endian IEEE-754 bytes.
func EncodeFloat(v float32) []byte { return protowire.AppendFixed32(nil, math.Float32bits(v)) }

// EncodeUint32 encodes a uint32 as a bare varint.
func EncodeUint32(v uint32) []byte { return protowire.AppendVarint(nil, uint64(v)) }

// EncodeUint64 encodes a uint64 as a bare varint. Also the encoding of a
// class/object reference (the object id); see EncodeObject.
func EncodeUint64(v uint64) []byte { return protowire.AppendVarint(nil, v) }

// EncodeSint32 encodes an int32 as a zig-zag varint. Also the encoding of an
// enumeration value; see EncodeEnum.
func EncodeSint32(v int32) []byte {
	return protowire.AppendVarint(nil, protowire.EncodeZigZag(int64(v)))
}

// EncodeSint64 encodes an int64 as a zig-zag varint.
func EncodeSint64(v int64) []byte {
	return protowire.AppendVarint(nil, protowire.EncodeZigZag(v))
}

// EncodeBool encodes a bool as a single varint byte (0 or 1).
func EncodeBool(v bool) []byte {
	if v {
		return []byte{1}
	}
	return []byte{0}
}

// EncodeString encodes a string as a varint length prefix followed by its UTF-8
// bytes.
func EncodeString(s string) []byte { return protowire.AppendString(nil, s) }

// EncodeBytes encodes a byte slice as a varint length prefix followed by the bytes.
func EncodeBytes(b []byte) []byte { return protowire.AppendBytes(nil, b) }

// EncodeObject encodes a remote object reference as its uint64 object id. A nil
// object is id 0.
func EncodeObject(objectID uint64) []byte { return EncodeUint64(objectID) }

// EncodeEnum encodes an enumeration value as sint32.
func EncodeEnum(v int32) []byte { return EncodeSint32(v) }

// ---- naked value decoders ----
//
// Every decoder treats an empty input as the zero value: kRPC always sends the
// encoded value for a value-returning procedure, but zero-length is the safe,
// honest reading of "nothing came back."

// DecodeDouble decodes 8 little-endian bytes into a float64.
func DecodeDouble(b []byte) (float64, error) {
	if len(b) == 0 {
		return 0, nil
	}
	v, n := protowire.ConsumeFixed64(b)
	if n < 0 {
		return 0, wireErr("double", n)
	}
	return math.Float64frombits(v), nil
}

// DecodeFloat decodes 4 little-endian bytes into a float32.
func DecodeFloat(b []byte) (float32, error) {
	if len(b) == 0 {
		return 0, nil
	}
	v, n := protowire.ConsumeFixed32(b)
	if n < 0 {
		return 0, wireErr("float", n)
	}
	return math.Float32frombits(v), nil
}

// DecodeUint32 decodes a bare varint into a uint32.
func DecodeUint32(b []byte) (uint32, error) {
	v, err := DecodeUint64(b)
	return uint32(v), err
}

// DecodeUint64 decodes a bare varint into a uint64. Also decodes a class/object
// reference (the object id).
func DecodeUint64(b []byte) (uint64, error) {
	if len(b) == 0 {
		return 0, nil
	}
	v, n := protowire.ConsumeVarint(b)
	if n < 0 {
		return 0, wireErr("uint64", n)
	}
	return v, nil
}

// DecodeSint32 decodes a zig-zag varint into an int32. Also decodes an
// enumeration value.
func DecodeSint32(b []byte) (int32, error) {
	v, err := DecodeSint64(b)
	return int32(v), err
}

// DecodeSint64 decodes a zig-zag varint into an int64.
func DecodeSint64(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, nil
	}
	v, n := protowire.ConsumeVarint(b)
	if n < 0 {
		return 0, wireErr("sint64", n)
	}
	return protowire.DecodeZigZag(v), nil
}

// DecodeBool decodes a varint into a bool.
func DecodeBool(b []byte) (bool, error) {
	v, err := DecodeUint64(b)
	return v != 0, err
}

// DecodeString decodes a varint-length-prefixed UTF-8 string.
func DecodeString(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	s, n := protowire.ConsumeString(b)
	if n < 0 {
		return "", wireErr("string", n)
	}
	return s, nil
}

// DecodeBytes decodes a varint-length-prefixed byte slice.
func DecodeBytes(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	v, n := protowire.ConsumeBytes(b)
	if n < 0 {
		return nil, wireErr("bytes", n)
	}
	return v, nil
}

// DecodeObject decodes a remote object reference into its object id. An id of 0
// means the reference was null.
func DecodeObject(b []byte) (uint64, error) { return DecodeUint64(b) }

// DecodeEnum decodes an enumeration value (sint32).
func DecodeEnum(b []byte) (int32, error) { return DecodeSint32(b) }

// ---- collection decoders ----
//
// A LIST/SET/TUPLE value is a protobuf message (List/Set/Tuple) whose `items`
// are each an independently-encoded value; the caller decodes each item with the
// Decode* helper for the element type. kRPC represents a null collection as a
// single 0x00 byte, which these treat as empty.

func isNullCollection(b []byte) bool {
	return len(b) == 0 || (len(b) == 1 && b[0] == 0)
}

// DecodeList decodes a LIST value into its raw item byte-slices.
func DecodeList(b []byte) ([][]byte, error) {
	if isNullCollection(b) {
		return nil, nil
	}
	var l pb.List
	if err := proto.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("krpc: decode list: %w", err)
	}
	return l.Items, nil
}

// DecodeSet decodes a SET value into its raw item byte-slices.
func DecodeSet(b []byte) ([][]byte, error) {
	if isNullCollection(b) {
		return nil, nil
	}
	var s pb.Set
	if err := proto.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("krpc: decode set: %w", err)
	}
	return s.Items, nil
}

// DecodeTuple decodes a TUPLE value into its raw element byte-slices.
func DecodeTuple(b []byte) ([][]byte, error) {
	if isNullCollection(b) {
		return nil, nil
	}
	var t pb.Tuple
	if err := proto.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("krpc: decode tuple: %w", err)
	}
	return t.Items, nil
}

// DecodeVector3 decodes a TUPLE(double,double,double) — kRPC's representation of
// a 3-vector (position, velocity, direction) — into a plain [3]float64. A short
// or null tuple yields zeros for the missing components rather than an error, so
// a degenerate reading stays honest instead of failing the whole tool.
func DecodeVector3(b []byte) ([3]float64, error) {
	var v [3]float64
	items, err := DecodeTuple(b)
	if err != nil {
		return v, err
	}
	for i := 0; i < 3 && i < len(items); i++ {
		d, err := DecodeDouble(items[i])
		if err != nil {
			return v, err
		}
		v[i] = d
	}
	return v, nil
}

// DictEntry is one decoded DICTIONARY entry: the raw key and value bytes, each
// decoded further by the caller with the Decode* helper for its element type.
type DictEntry struct {
	Key   []byte
	Value []byte
}

// DecodeDictionary decodes a DICTIONARY value (e.g. SpaceCenter.get_Bodies ->
// dictionary<string, CelestialBody>) into its raw key/value pairs.
func DecodeDictionary(b []byte) ([]DictEntry, error) {
	if isNullCollection(b) {
		return nil, nil
	}
	var d pb.Dictionary
	if err := proto.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("krpc: decode dictionary: %w", err)
	}
	out := make([]DictEntry, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, DictEntry{Key: e.Key, Value: e.Value})
	}
	return out, nil
}

func wireErr(kind string, n int) error {
	return fmt.Errorf("krpc: decode %s: %w", kind, protowire.ParseError(n))
}

// ---- client identifier formatting ----

// formatGUID renders kRPC's 16-byte client_identifier as a canonical GUID
// string. The first three groups are little-endian, the last two big-endian —
// the same mixed layout the reference client uses (decoder.guid). Purely
// cosmetic (for logs / game_state); the raw bytes are what the stream handshake
// echoes back.
func formatGUID(b []byte) string {
	if len(b) != 16 {
		return fmt.Sprintf("%x", b)
	}
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[3], b[2], b[1], b[0],
		b[5], b[4],
		b[7], b[6],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15])
}
