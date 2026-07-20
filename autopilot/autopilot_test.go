package autopilot

import (
	"math"
	"testing"
)

func TestConditionEval(t *testing.T) {
	tel := Telemetry{AltitudeM: 1200, ApoapsisM: 70000, MET: 30}
	cases := []struct {
		c    Condition
		want bool
	}{
		{Condition{FieldAltitude, "gt", 1000}, true},
		{Condition{FieldAltitude, "lt", 1000}, false},
		{Condition{FieldApoapsis, "gt", 80000}, false},
		{Condition{FieldApoapsis, "gt", 60000}, true},
		{Condition{FieldMET, "gt", 25}, true},
		{Condition{FieldTimeInPhase, "gt", 5}, true}, // phaseElapsed=10 below
		{Condition{"bogus", "gt", 0}, false},
		{Condition{FieldAltitude, "bogus", 0}, false},
	}
	for i, tc := range cases {
		if got := tc.c.eval(tel, 10); got != tc.want {
			t.Errorf("case %d %+v: eval = %v, want %v", i, tc.c, got, tc.want)
		}
	}
}

func TestPitchProgram(t *testing.T) {
	pp := PitchProgram{StartAltitudeM: 500, EndAltitudeM: 45000, StartPitchDeg: 90, EndPitchDeg: 0}
	cases := []struct {
		alt, want float64
	}{
		{0, 90},          // below start → clamped to start pitch (vertical)
		{500, 90},        // at start
		{22750, 45},      // halfway → 45°
		{45000, 0},       // at end → horizon
		{100000, 0},      // above end → clamped to horizon
	}
	for _, tc := range cases {
		if got := pp.pitchAt(tc.alt); math.Abs(got-tc.want) > 0.01 {
			t.Errorf("pitchAt(%.0f) = %.2f, want %.2f", tc.alt, got, tc.want)
		}
	}
}

func TestValidate_CatchesUnsafeShapes(t *testing.T) {
	good := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	if errs := Validate(good); len(errs) != 0 {
		t.Fatalf("BuildAscent should validate clean, got %v", errs)
	}

	// No dead-man timeout.
	p := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	p.MaxDurationS = 0
	if errs := Validate(p); len(errs) == 0 {
		t.Error("missing max_duration_s must be rejected (the dead-man is mandatory)")
	}

	// Throttle out of range.
	p = BuildAscent(AscentParams{TargetApoapsisM: 80000})
	p.Phases[0].Guidance.Throttle = 1.5
	if errs := Validate(p); len(errs) == 0 {
		t.Error("throttle > 1 must be rejected")
	}

	// No phases.
	if errs := Validate(Program{Name: "empty", MaxDurationS: 60}); len(errs) == 0 {
		t.Error("a program with no phases must be rejected")
	}

	// Invalid terminal condition (a phase that could run forever).
	p = BuildAscent(AscentParams{TargetApoapsisM: 80000})
	p.Phases[1].Until = Condition{Field: "bogus", Op: "gt", Value: 1}
	if errs := Validate(p); len(errs) == 0 {
		t.Error("an invalid/absent terminal condition must be rejected")
	}

	// Two steering modes at once.
	p = BuildAscent(AscentParams{TargetApoapsisM: 80000})
	up := 90.0
	p.Phases[1].Guidance.FixedPitchDeg = &up // gravity-turn phase already has a pitch program
	if errs := Validate(p); len(errs) == 0 {
		t.Error("more than one steering mode must be rejected")
	}
}

func TestStep_DeadmanAborts(t *testing.T) {
	p := BuildAscent(AscentParams{TargetApoapsisM: 80000, MaxDurationS: 10})
	st := State{TotalElapsed: 9.95}
	ctrl, ns := Step(p, Telemetry{}, st, 0.1)
	if !ctrl.Abort {
		t.Fatalf("past the dead-man, Step must abort; got %+v", ctrl)
	}
	if ctrl.Throttle != 0 {
		t.Errorf("an abort must cut throttle, got %.2f", ctrl.Throttle)
	}
	if !ns.Aborted {
		t.Error("state should record the abort")
	}
}

func TestStep_AutoStageOnDryEngine(t *testing.T) {
	p := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	// Enter the gravity-turn phase (index 1) so StageOnEntry isn't the trigger,
	// with the active stage dry while we're commanding thrust. TotalElapsed is
	// well past the 1 s stage cooldown (a real booster runs dry tens of seconds
	// in), so the cooldown — which correctly blocks double-staging right after
	// ignition — doesn't apply here.
	st := State{PhaseIndex: 1, phaseEntered: true, TotalElapsed: 30}
	tel := Telemetry{AltitudeM: 5000, ApoapsisM: 20000, ActiveEngineHasFuel: false}
	ctrl, ns := Step(p, tel, st, 0.1)
	if !ctrl.Stage {
		t.Fatalf("dry active engine under thrust must auto-stage; got %+v", ctrl)
	}
	// Cooldown: a second immediate tick must NOT stage again.
	ctrl2, _ := Step(p, tel, ns, 0.1)
	if ctrl2.Stage {
		t.Error("auto-stage must be debounced, not fire every tick")
	}
}

func TestStep_LiftoffStagesOnEntry(t *testing.T) {
	p := BuildAscent(AscentParams{TargetApoapsisM: 80000})
	ctrl, _ := Step(p, Telemetry{AltitudeM: 0}, State{}, 0.1)
	if !ctrl.Stage {
		t.Errorf("liftoff must fire staging on entry (ignition); got %+v", ctrl)
	}
	if ctrl.Throttle != 1 {
		t.Errorf("liftoff throttle = %.2f, want 1", ctrl.Throttle)
	}
	if ctrl.Steer != SteerPitchHeading || ctrl.TargetPitch != 90 {
		t.Errorf("liftoff should hold pitch 90; got steer=%s pitch=%.0f", ctrl.Steer, ctrl.TargetPitch)
	}
}

// TestSimFliesAscent is the end-to-end oracle: a crude kinematic rocket (vertical
// only — it exercises throttle/stage/phase SEQUENCING and the apoapsis cutoff, not
// aerodynamics) is flown entirely by Step. If the state machine is wired right it
// lifts off, auto-stages when the booster runs dry, and cuts the engines the tick
// apoapsis reaches the target. This is the proof the executor sequences a launch
// before it is ever connected to a live throttle.
func TestSimFliesAscent(t *testing.T) {
	const g = 9.81
	const dt = 0.1
	target := 80000.0

	// Two stages: full-throttle acceleration (m/s^2) and burn seconds of fuel.
	stages := []struct{ accel, fuel float64 }{
		{accel: 25, fuel: 40},
		{accel: 18, fuel: 90},
	}
	lit := -1 // no stage ignited yet; liftoff's StageOnEntry lights the first
	stageCount := 0
	var alt, vSpd float64
	apoapsis := func() float64 {
		if vSpd > 0 {
			return alt + vSpd*vSpd/(2*g)
		}
		return alt
	}

	p := BuildAscent(AscentParams{TargetApoapsisM: target})
	st := State{}
	sawTurn := false
	done := false

	for i := 0; i < 20000; i++ {
		hasFuel := lit >= 0 && lit < len(stages) && stages[lit].fuel > 0
		tel := Telemetry{
			MET:                 st.TotalElapsed,
			AltitudeM:           alt,
			ApoapsisM:           apoapsis(),
			VerticalSpeedMS:     vSpd,
			ActiveEngineHasFuel: hasFuel,
		}
		ctrl, ns := Step(p, tel, st, dt)
		st = ns
		if ctrl.Abort {
			t.Fatalf("unexpected abort %q at t=%.1f alt=%.0f apo=%.0f", ctrl.AbortReason, st.TotalElapsed, alt, apoapsis())
		}
		if ctrl.Steer == SteerPitchHeading && ctrl.TargetPitch < 90 {
			sawTurn = true // the gravity turn actually pitched over
		}
		if ctrl.Stage && lit < len(stages)-1 {
			lit++
			stageCount++
		}
		if ctrl.Done {
			done = true
			break
		}
		// Kinematics: vertical thrust when commanded and fueled, minus gravity.
		thrustAccel := 0.0
		if ctrl.Throttle > 0 && lit >= 0 && lit < len(stages) && stages[lit].fuel > 0 {
			thrustAccel = ctrl.Throttle * stages[lit].accel
			stages[lit].fuel -= dt
		}
		vSpd += (thrustAccel - g) * dt
		alt += vSpd * dt
		if alt < 0 {
			alt = 0
			if vSpd < 0 {
				vSpd = 0
			}
		}
	}

	if !done {
		t.Fatalf("program never completed (alt=%.0f apo=%.0f t=%.1f)", alt, apoapsis(), st.TotalElapsed)
	}
	if apoapsis() < target {
		t.Errorf("cut too early: apoapsis %.0f < target %.0f", apoapsis(), target)
	}
	if stageCount < 2 {
		t.Errorf("expected ignition + at least one auto-stage (>=2 stage events), got %d", stageCount)
	}
	if !sawTurn {
		t.Error("the gravity turn never pitched below 90° — steering schedule not exercised")
	}
	if st.TotalElapsed >= p.MaxDurationS {
		t.Errorf("ascent took %.0fs, at/over the dead-man %.0fs", st.TotalElapsed, p.MaxDurationS)
	}
	t.Logf("ascent complete: apoapsis %.0f m, %d stage events, %.1fs", apoapsis(), stageCount, st.TotalElapsed)
}

func TestDescribe_ReadsBackAndPromisesNoFire(t *testing.T) {
	d := Describe(BuildAscent(AscentParams{TargetApoapsisM: 80000}))
	for _, want := range []string{"liftoff", "gravity turn", "full throttle", "80 km"} {
		if !contains(d, want) {
			t.Errorf("read-back missing %q:\n%s", want, d)
		}
	}
	if !contains(d, "go") {
		t.Errorf("read-back must state nothing fires until the crew's go:\n%s", d)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
