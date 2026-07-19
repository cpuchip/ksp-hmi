package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc"
)

func TestFmtDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{65, "1m 05s"},
		{360, "6m 00s"},
		{3661, "1h 01m 01s"},
		{90061, "1d 01h 01m 01s"},
		{-5, "-5s"},
	}
	for _, tc := range cases {
		if got := fmtDuration(tc.in); got != tc.want {
			t.Errorf("fmtDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRound2(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{3.14159, 3.14},
		{2.5, 2.5},
		{0.125, 0.13},
		{100, 100},
		{-1.005, -1.0},
	}
	for _, tc := range cases {
		if got := round2(tc.in); got != tc.want {
			t.Errorf("round2(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestGracefulDegradationWhenDown verifies the real "kRPC unreachable" path: with
// no server listening, game_state reports disconnected (never errors) and the
// vessel tools return Available:false with a spoken-friendly message rather than
// failing. Points at a closed loopback port with a short timeout.
func TestGracefulDegradationWhenDown(t *testing.T) {
	srv := newKSPServer(krpc.DialConfig{
		Host:       "127.0.0.1",
		RPCPort:    59321, // nothing listens here
		StreamPort: 0,
		Timeout:    500 * time.Millisecond,
	})
	defer srv.Close()

	gs := srv.gameState()
	if gs.Connected {
		t.Error("game_state.Connected = true with no server; want false")
	}
	if !strings.Contains(gs.Message, "Can't reach kRPC") {
		t.Errorf("game_state.Message = %q, want it to explain kRPC is unreachable", gs.Message)
	}

	vs, err := srv.vesselStatus()
	if err != nil {
		t.Fatalf("vessel_status returned a hard error instead of degrading: %v", err)
	}
	if vs.Available {
		t.Error("vessel_status.Available = true with no server; want false")
	}
	if vs.Message == "" {
		t.Error("vessel_status.Message empty; want a graceful explanation")
	}

	// Every vessel tool degrades the same way (no panics, no hard errors).
	if o, err := srv.orbit(); err != nil || o.Available {
		t.Errorf("orbit degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if f, err := srv.flightTelemetry(); err != nil || f.Available {
		t.Errorf("flight_telemetry degraded wrong: avail=%v err=%v", f.Available, err)
	}
	if r, err := srv.resources(); err != nil || r.Available {
		t.Errorf("resources degraded wrong: avail=%v err=%v", r.Available, err)
	}
	if n, err := srv.maneuverNodes(); err != nil || n.Available {
		t.Errorf("maneuver_nodes degraded wrong: avail=%v err=%v", n.Available, err)
	}
	if cw, err := srv.crew(); err != nil || cw.Available {
		t.Errorf("crew degraded wrong: avail=%v err=%v", cw.Available, err)
	}
}
