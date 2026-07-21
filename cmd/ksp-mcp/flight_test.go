package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// deadFlightServer is a kspServer pointed at a dead kRPC port, so every live call
// degrades gracefully — good for exercising the arm/execute GATE logic without a
// game and without ever firing anything.
func deadFlightServer() *kspServer {
	return newKSPServer(krpc.DialConfig{Host: "127.0.0.1", RPCPort: 59323, Timeout: 200 * time.Millisecond})
}

func TestIsGo(t *testing.T) {
	for _, s := range []string{"go", "GO", " Go ", "affirmative", "we are go"} {
		if !isGo(s) {
			t.Errorf("isGo(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "no", "nogo", "no-go", "stop", "maybe", "g"} {
		if isGo(s) {
			t.Errorf("isGo(%q) = true, want false", s)
		}
	}
}

func TestFlightArmValidates(t *testing.T) {
	srv := deadFlightServer()
	defer srv.Close()

	// No target → not armed, no error.
	out := srv.flightArm(ascentInput{})
	if out.Armed {
		t.Error("arming with no target should not arm")
	}

	// Valid target → armed with a read-back.
	out = srv.flightArm(ascentInput{TargetApoapsisM: 80000})
	if !out.Armed {
		t.Fatalf("valid ascent should arm; note=%q errors=%v", out.Note, out.Errors)
	}
	if !strings.Contains(out.Readback, "liftoff") || !strings.Contains(out.Readback, "80 km") {
		t.Errorf("read-back missing expected content:\n%s", out.Readback)
	}
}

func TestFlightExecuteRefusesWithoutGo(t *testing.T) {
	srv := deadFlightServer()
	defer srv.Close()

	srv.flightArm(ascentInput{TargetApoapsisM: 80000}) // arm a valid program

	// No confirmation → NO-GO, fires nothing (and never touches the dead kRPC port).
	out := srv.flightExecute(flightExecuteInput{Confirm: ""})
	if out.Executing {
		t.Fatal("execute without confirm must NOT execute")
	}
	if !strings.Contains(strings.ToUpper(out.Note), "NO-GO") {
		t.Errorf("expected a NO-GO note, got %q", out.Note)
	}

	// Wrong confirmation → still refuses.
	if srv.flightExecute(flightExecuteInput{Confirm: "maybe"}).Executing {
		t.Error("execute with a non-go confirm must NOT execute")
	}
}

func TestFlightExecuteRefusesWithNothingArmed(t *testing.T) {
	srv := deadFlightServer()
	defer srv.Close()
	// "go" but nothing armed → refuses (before any game contact).
	out := srv.flightExecute(flightExecuteInput{Confirm: "go"})
	if out.Executing {
		t.Fatal("execute with nothing armed must NOT execute")
	}
	if !strings.Contains(strings.ToLower(out.Note), "armed") {
		t.Errorf("note should mention nothing armed, got %q", out.Note)
	}
}

func TestFlightExecuteGoButNoGameDegrades(t *testing.T) {
	srv := deadFlightServer()
	defer srv.Close()
	srv.flightArm(ascentInput{TargetApoapsisM: 80000})
	// Armed + "go", but kRPC is dead → it must fail to resolve the vessel and NOT
	// execute (graceful, no crash, nothing fired).
	out := srv.flightExecute(flightExecuteInput{Confirm: "go"})
	if out.Executing {
		t.Error("execute must not claim to fly when the vessel can't be resolved")
	}
}

func TestFlightAbortWhenIdle(t *testing.T) {
	srv := deadFlightServer()
	defer srv.Close()
	if srv.flightAbort().Aborted {
		t.Error("abort with nothing running should report not-aborted")
	}
}

func TestFlightStatusReportsArmed(t *testing.T) {
	srv := deadFlightServer()
	defer srv.Close()
	if srv.flightStatus().Armed {
		t.Error("fresh server should report not armed")
	}
	srv.flightArm(ascentInput{TargetApoapsisM: 80000})
	st := srv.flightStatus()
	if !st.Armed || st.Running {
		t.Errorf("after arm: Armed=%v Running=%v, want armed & not running", st.Armed, st.Running)
	}
}

// TestFlightToolsGatedByFlag — the flight-control tools appear ONLY when
// registered with enableFlight=true.
func TestFlightToolsGatedByFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	flightNames := []string{"flight_arm", "flight_execute", "flight_abort", "flight_status"}

	// enableFlight=false → none present.
	if got := listToolNames(ctx, t, false); hasAny(got, flightNames) {
		t.Error("flight-control tools must be ABSENT when -enable-flight is off")
	}
	// enableFlight=true → all present.
	got := listToolNames(ctx, t, true)
	for _, n := range flightNames {
		if !got[n] {
			t.Errorf("flight tool %q missing when -enable-flight is on", n)
		}
	}
}

func listToolNames(ctx context.Context, t *testing.T, enableFlight bool) map[string]bool {
	t.Helper()
	srv := deadFlightServer()
	t.Cleanup(srv.Close)
	s := mcp.NewServer(&mcp.Implementation{Name: "ksp-mcp", Version: version}, nil)
	registerTools(s, srv, enableFlight)

	serverT, clientT := mcp.NewInMemoryTransports()
	go func() { _ = s.Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range lt.Tools {
		names[tl.Name] = true
	}
	return names
}

func hasAny(names map[string]bool, want []string) bool {
	for _, n := range want {
		if names[n] {
			return true
		}
	}
	return false
}
