package krpc

// wire_test.go — the value codec and framing, checked against byte-for-byte
// golden vectors taken from kRPC's own reference Python client tests
// (client/python/krpc/test/test_encoder.py & test_decoder.py, kRPC @ main,
// fetched 2026-07-19). These are the goldens the task calls for: if kRPC and this
// package ever disagree on a byte, one of these fails.

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/cpuchip/ksp-hmi/krpc/pb"
	"google.golang.org/protobuf/proto"
)

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func mustUnhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestEncodeValueGoldens(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		// From kRPC test_encoder.py:
		{"uint32=300", EncodeUint32(300), "ac02"},            // test_encode_value
		{"string=trademark", EncodeString("™"), "03e284a2"},  // test_encode_unicode_string
		{"class id=300 (uint64)", EncodeObject(300), "ac02"}, // test_encode_class
		{"class none (id 0)", EncodeObject(0), "00"},         // test_encode_class_none
		// Standard protobuf value encodings (independently verifiable):
		{"double=1.0", EncodeDouble(1.0), "000000000000f03f"},
		{"double=0.0", EncodeDouble(0.0), "0000000000000000"},
		{"float=1.0", EncodeFloat(1.0), "0000803f"},
		{"sint32=-1 (zigzag)", EncodeSint32(-1), "01"},
		{"sint32=1 (zigzag)", EncodeSint32(1), "02"},
		{"sint32=-2 (zigzag)", EncodeSint32(-2), "03"},
		{"bool=true", EncodeBool(true), "01"},
		{"bool=false", EncodeBool(false), "00"},
		{"uint64=300", EncodeUint64(300), "ac02"},
		{"enum=1 (sint32)", EncodeEnum(1), "02"},
	}
	for _, tc := range tests {
		if got := hexOf(tc.got); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestDecodeValueGoldens(t *testing.T) {
	// uint32=300 (test_decode_value)
	if v, err := DecodeUint32(mustUnhex(t, "ac02")); err != nil || v != 300 {
		t.Errorf("DecodeUint32(ac02) = %d, %v; want 300", v, err)
	}
	// string (test_decode_unicode_string)
	if v, err := DecodeString(mustUnhex(t, "03e284a2")); err != nil || v != "™" {
		t.Errorf("DecodeString(03e284a2) = %q, %v; want trademark", v, err)
	}
	// class id=300 (test_decode_class)
	if v, err := DecodeObject(mustUnhex(t, "ac02")); err != nil || v != 300 {
		t.Errorf("DecodeObject(ac02) = %d, %v; want 300", v, err)
	}
	// class none: 0x00 -> id 0 (test_decode_class_none)
	if v, err := DecodeObject(mustUnhex(t, "00")); err != nil || v != 0 {
		t.Errorf("DecodeObject(00) = %d, %v; want 0", v, err)
	}
	// double round trips
	if v, err := DecodeDouble(mustUnhex(t, "000000000000f03f")); err != nil || v != 1.0 {
		t.Errorf("DecodeDouble(1.0) = %v, %v", v, err)
	}
	// sint32 zigzag
	if v, err := DecodeSint32(mustUnhex(t, "01")); err != nil || v != -1 {
		t.Errorf("DecodeSint32(01) = %d, %v; want -1", v, err)
	}
	// empty input decodes to zero value, not an error
	if v, err := DecodeDouble(nil); err != nil || v != 0 {
		t.Errorf("DecodeDouble(nil) = %v, %v; want 0,nil", v, err)
	}
}

func TestScalarRoundTrip(t *testing.T) {
	doubles := []float64{0, 1, -1, 3.14159265358979, 1e300, -2.5e-17, 100000.5}
	for _, d := range doubles {
		got, err := DecodeDouble(EncodeDouble(d))
		if err != nil || got != d {
			t.Errorf("double round-trip %v: got %v, %v", d, got, err)
		}
	}
	floats := []float32{0, 1, -1, 3.14159, 1e30}
	for _, f := range floats {
		got, err := DecodeFloat(EncodeFloat(f))
		if err != nil || got != f {
			t.Errorf("float round-trip %v: got %v, %v", f, got, err)
		}
	}
	sints := []int32{0, 1, -1, 2147483647, -2147483648, 42, -42}
	for _, v := range sints {
		got, err := DecodeSint32(EncodeSint32(v))
		if err != nil || got != v {
			t.Errorf("sint32 round-trip %d: got %d, %v", v, got, err)
		}
	}
	strs := []string{"", "Kerbin", "™", "Jebediah Kerman"}
	for _, s := range strs {
		got, err := DecodeString(EncodeString(s))
		if err != nil || got != s {
			t.Errorf("string round-trip %q: got %q, %v", s, got, err)
		}
	}
}

func TestProcedureCallGolden(t *testing.T) {
	// test_encode_message: a ProcedureCall marshals to a known byte string.
	call := &pb.ProcedureCall{Service: "ServiceName", Procedure: "ProcedureName"}
	b, err := proto.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	const want = "0a0b536572766963654e616d65120d50726f6365647572654e616d65"
	if got := hexOf(b); got != want {
		t.Errorf("ProcedureCall marshal = %s, want %s", got, want)
	}
}

func TestFrameGolden(t *testing.T) {
	// test_encode_message_with_size: the framed message is the varint length
	// (0x1c = 28) followed by the 28-byte ProcedureCall.
	call := &pb.ProcedureCall{Service: "ServiceName", Procedure: "ProcedureName"}
	b, err := frame(call)
	if err != nil {
		t.Fatal(err)
	}
	const want = "1c0a0b536572766963654e616d65120d50726f6365647572654e616d65"
	if got := hexOf(b); got != want {
		t.Errorf("frame(ProcedureCall) = %s, want %s", got, want)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	// writeMessage then readMessage reproduces the message across the framing.
	orig := &pb.Request{Calls: []*pb.ProcedureCall{
		{Service: "SpaceCenter", Procedure: "get_UT"},
		{Service: "KRPC", Procedure: "GetStatus"},
	}}
	var buf bytes.Buffer
	if err := writeMessage(&buf, orig); err != nil {
		t.Fatal(err)
	}
	// Prepend a second framed message to prove the reader consumes exactly one.
	var buf2 bytes.Buffer
	_ = writeMessage(&buf2, &pb.Request{})
	combined := append(append([]byte{}, buf.Bytes()...), buf2.Bytes()...)

	r := newTestReader(combined)
	var got pb.Request
	if err := readMessage(r, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Calls) != 2 || got.Calls[0].Procedure != "get_UT" || got.Calls[1].Service != "KRPC" {
		t.Errorf("round-trip mismatch: %+v", got.Calls)
	}
	// The second message is still there, fully intact.
	var second pb.Request
	if err := readMessage(r, &second); err != nil {
		t.Fatalf("second message: %v", err)
	}
}

func TestCollectionDecode(t *testing.T) {
	// A List of three uint32 values, encoded as kRPC would.
	list := &pb.List{Items: [][]byte{EncodeUint32(1), EncodeUint32(2), EncodeUint32(300)}}
	b, err := proto.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	items, err := DecodeList(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("DecodeList len = %d, want 3", len(items))
	}
	want := []uint32{1, 2, 300}
	for i, it := range items {
		v, err := DecodeUint32(it)
		if err != nil || v != want[i] {
			t.Errorf("item %d = %d, %v; want %d", i, v, err, want[i])
		}
	}
	// A null collection (single 0x00) decodes to empty, not an error.
	if items, err := DecodeList([]byte{0}); err != nil || items != nil {
		t.Errorf("DecodeList(00) = %v, %v; want nil,nil", items, err)
	}
	if items, err := DecodeList(nil); err != nil || items != nil {
		t.Errorf("DecodeList(nil) = %v, %v; want nil,nil", items, err)
	}
}

func TestGUIDGolden(t *testing.T) {
	// test_guid: the mixed-endian GUID rendering of a 16-byte identifier.
	in := mustUnhex(t, "391b276fdd00e44d9732f0d3a68838df")
	const want = "6f271b39-00dd-4de4-9732-f0d3a68838df"
	if got := formatGUID(in); got != want {
		t.Errorf("formatGUID = %s, want %s", got, want)
	}
}
