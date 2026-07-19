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

// TestNewToolsDegradeWhenDown holds the flight-computer tools (Tier 1/2/3) to the
// same contract: with kRPC unreachable every one returns a graceful
// Available:false answer, never a hard error and never a panic. Crucially, the
// Tier 3 WRITE tools must attempt no game mutation when they can't even connect.
func TestNewToolsDegradeWhenDown(t *testing.T) {
	srv := newKSPServer(krpc.DialConfig{
		Host: "127.0.0.1", RPCPort: 59323, StreamPort: 0, Timeout: 400 * time.Millisecond,
	})
	defer srv.Close()

	if o, err := srv.targetInfo(); err != nil || o.Available {
		t.Errorf("target_info degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.listVessels(); err != nil || o.Available {
		t.Errorf("list_vessels degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.deltaVStatus(); err != nil || o.Available {
		t.Errorf("delta_v_status degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.attitude(); err != nil || o.Available {
		t.Errorf("attitude degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.bodies("Kerbin"); err != nil || o.Available {
		t.Errorf("bodies degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.calcCircularize(); err != nil || o.Available {
		t.Errorf("calc_circularize degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.calcHohmann(hohmannInput{}); err != nil || o.Available {
		t.Errorf("calc_hohmann degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.calcPlaneChange(); err != nil || o.Available {
		t.Errorf("calc_plane_change degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.calcBurnTime(burnTimeInput{DeltaVMS: 100}); err != nil || o.Available {
		t.Errorf("calc_burn_time degraded wrong: avail=%v err=%v", o.Available, err)
	}
	// Tier 3 writes: must degrade, and must not mutate (they never reach a Call).
	tfn := 60.0
	if o, err := srv.nodeCreate(nodeCreateInput{TimeFromNowSeconds: &tfn, ProgradeMS: 10}); err != nil || o.Available {
		t.Errorf("node_create degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.nodeDelete(nodeDeleteInput{}); err != nil || o.Available {
		t.Errorf("node_delete degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.nodeClear(); err != nil || o.Available {
		t.Errorf("node_clear degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planCircularize(planInput{}); err != nil || o.Available {
		t.Errorf("plan_circularize degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planHohmann(hohmannInput{}); err != nil || o.Available {
		t.Errorf("plan_hohmann degraded wrong: avail=%v err=%v", o.Available, err)
	}
}
