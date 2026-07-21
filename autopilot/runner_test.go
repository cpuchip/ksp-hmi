package autopilot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// simRig is a tiny closed-loop test double: it is BOTH the ControlSink (records
// throttle/stage commands) and the TelemetrySource (advances a coarse kinematic
// model from the last commanded throttle). Physics magnitudes are tuned so a full
// ascent completes in a few dozen ticks (fast wall-clock) with one auto-stage
// event after the first stage's fuel runs dry.
type simRig struct {
	mu         sync.Mutex
	alt        float64
	litStage   int   // -1 before ignition; Stage commands advance it
	fuel       []int // ticks of fuel remaining per stage
	throttle   float64
	stageCount int
	applied    []Control
	stops      int
	minPitch   float64 // lowest TargetPitch the sink was ever commanded (proves the turn)
	failRead   bool    // when true, Read returns an error (abort path test)
}

func newSimRig() *simRig {
	return &simRig{litStage: -1, fuel: []int{22, 60}, minPitch: 90}
}

func (r *simRig) Read() (Telemetry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failRead {
		return Telemetry{}, errors.New("simulated telemetry loss")
	}
	hasFuel := r.litStage >= 0 && r.litStage < len(r.fuel) && r.fuel[r.litStage] > 0
	if r.throttle > 0 && hasFuel {
		r.fuel[r.litStage]--
		r.alt += 2500 * r.throttle
		hasFuel = r.fuel[r.litStage] > 0
	}
	return Telemetry{AltitudeM: r.alt, ApoapsisM: r.alt, ActiveEngineHasFuel: hasFuel}, nil
}

func (r *simRig) Apply(c Control) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.throttle = c.Throttle
	if c.Steer == SteerPitchHeading && c.TargetPitch < r.minPitch {
		r.minPitch = c.TargetPitch
	}
	if c.Stage {
		r.stageCount++
		r.litStage++
	}
	r.applied = append(r.applied, c)
	return nil
}

func (r *simRig) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops++
	r.throttle = 0
	return nil
}

func (r *simRig) snapshot() (stops, stageCount int, minPitch, throttle float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stops, r.stageCount, r.minPitch, r.throttle
}

// TestRunFliesAscent — the runner flies the ascent program end to end against the
// sim: liftoff, gravity turn (pitching over), an auto-stage on burnout, cutoff at
// the target apoapsis, and Stop() called EXACTLY once.
func TestRunFliesAscent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	p := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	rig := newSimRig()

	res, err := Run(ctx, p, rig, rig, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v (reason %q)", err, res.AbortReason)
	}
	if !res.Completed || res.Aborted {
		t.Fatalf("expected clean completion, got Completed=%v Aborted=%v reason=%q", res.Completed, res.Aborted, res.AbortReason)
	}
	stops, stageCount, minPitch, throttle := rig.snapshot()
	if stops != 1 {
		t.Errorf("Stop() called %d times, want exactly 1 (the safety invariant)", stops)
	}
	if throttle != 0 {
		t.Errorf("throttle left at %.2f after completion, want 0 (cut on done)", throttle)
	}
	if stageCount < 2 {
		t.Errorf("stage events = %d, want >=2 (ignition + at least one auto-stage)", stageCount)
	}
	if minPitch >= 90 {
		t.Errorf("gravity turn never pitched below 90 (min pitch %.0f) — steering not exercised", minPitch)
	}
}

// TestRunTelemetryLossAborts — a telemetry read failure aborts the flight AND
// still brings the vessel to idle (Stop). The fail-safe under sensor loss.
func TestRunTelemetryLossAborts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	rig := newSimRig()
	rig.failRead = true

	res, err := Run(ctx, p, rig, rig, 20*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected an error on telemetry loss")
	}
	if !res.Aborted {
		t.Error("telemetry loss should abort")
	}
	if stops, _, _, _ := rig.snapshot(); stops != 1 {
		t.Errorf("Stop() called %d times on abort, want exactly 1", stops)
	}
}

// TestRunContextCancelAborts — cancelling the context stops the flight and idles
// the vessel (this is what flight_abort does under the hood).
func TestRunContextCancelAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	rig := newSimRig()

	done := make(chan RunResult, 1)
	go func() {
		res, _ := Run(ctx, p, rig, rig, 50*time.Millisecond, nil)
		done <- res
	}()
	// Let a few ticks fly, then abort.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		if !res.Aborted {
			t.Error("context cancel should abort")
		}
		if stops, _, _, throttle := rig.snapshot(); stops != 1 || throttle != 0 {
			t.Errorf("after cancel: stops=%d throttle=%.2f, want stops=1 throttle=0", stops, throttle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
