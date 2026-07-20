package autopilot

// executor.go — the pure step function that IS the fast-loop pilot. Given a
// program, a telemetry snapshot, the current state, and the tick length, it
// returns the Control to apply this tick and the advanced state. No I/O, no kRPC,
// no clock of its own — the caller owns the loop and the game adapter. That makes
// the entire flight logic unit-testable against synthetic telemetry and a toy
// simulator (executor_test.go), which is the safety proof: the sequencing is
// verified before it is ever wired to a live throttle.

// stageCooldownS debounces auto-staging so a momentary thrust dropout can't chain
// several stage events in consecutive ticks.
const stageCooldownS = 1.0

// Telemetry is the snapshot the executor reasons over each tick. The kRPC adapter
// (cmd/ksp-mcp, Tier 4) fills it from the live vessel; tests fill it synthetically.
type Telemetry struct {
	MET                 float64 // mission elapsed time, s
	AltitudeM           float64 // mean-sea-level altitude
	ApoapsisM           float64 // apoapsis altitude
	PeriapsisM          float64 // periapsis altitude
	SurfaceSpeedMS      float64
	OrbitalSpeedMS      float64
	VerticalSpeedMS     float64
	PitchDeg            float64
	CurrentStage        int32
	ActiveEngineHasFuel bool // at least one active engine still has propellant
	ThrottleNow         float64
}

// Steer names the attitude command the executor is asking the game's autopilot to
// hold this tick.
type Steer string

const (
	SteerHold         Steer = "hold"          // hold current attitude (no command)
	SteerPitchHeading Steer = "pitch_heading" // hold TargetPitch/TargetHeading
	SteerPrograde     Steer = "prograde"      // hold the prograde marker
)

// Control is what the executor commands this tick. The kRPC adapter applies it:
// set throttle, request the steer, fire staging if Stage, and stop + neutralize on
// Abort or Done. This struct is the ONLY channel from the flight logic to the
// controls — small and auditable on purpose.
type Control struct {
	Throttle    float64 `json:"throttle"`
	Steer       Steer   `json:"steer"`
	TargetPitch float64 `json:"target_pitch_deg,omitempty"`
	TargetHead  float64 `json:"target_heading_deg,omitempty"`
	Stage       bool    `json:"stage,omitempty"`
	Abort       bool    `json:"abort,omitempty"`
	AbortReason string  `json:"abort_reason,omitempty"`
	Done        bool    `json:"done,omitempty"`
	Phase       string  `json:"phase,omitempty"`
	Note        string  `json:"note,omitempty"`
}

// State is the executor's memory between ticks. Start with the zero value.
type State struct {
	PhaseIndex   int
	PhaseElapsed float64
	TotalElapsed float64
	Aborted      bool
	Done         bool

	lastStageAt   float64 // total-elapsed time of the last stage command (cooldown)
	phaseEntered  bool    // whether StageOnEntry has fired for the current phase
}

// Step advances the program by one tick of length dt. It is a pure function of its
// inputs. Precedence each tick: dead-man/abort first (safety wins), then a
// completed program, then the active phase's guidance, then the phase transition.
func Step(p Program, t Telemetry, st State, dt float64) (Control, State) {
	if st.Aborted {
		return Control{Abort: true, AbortReason: "already aborted", Throttle: 0, Steer: SteerHold}, st
	}
	if st.Done {
		return Control{Done: true, Throttle: 0, Steer: SteerHold}, st
	}

	st.TotalElapsed += dt
	st.PhaseElapsed += dt

	// Dead-man: the mandatory hard timeout on the whole program.
	if p.MaxDurationS > 0 && st.TotalElapsed > p.MaxDurationS {
		st.Aborted = true
		return Control{Abort: true, AbortReason: "exceeded max duration (dead-man)", Throttle: 0, Steer: SteerHold}, st
	}
	// Hard-limit aborts.
	for _, a := range p.Abort {
		if a.eval(t, st.PhaseElapsed) {
			st.Aborted = true
			return Control{Abort: true, AbortReason: "abort condition: " + a.describe(), Throttle: 0, Steer: SteerHold}, st
		}
	}

	if st.PhaseIndex >= len(p.Phases) {
		st.Done = true
		return Control{Done: true, Throttle: 0, Steer: SteerHold, Note: "program complete"}, st
	}

	ph := p.Phases[st.PhaseIndex]
	ctrl := Control{Phase: ph.Name, Throttle: clamp01(ph.Guidance.Throttle)}

	// One-shot staging on phase entry.
	if ph.Guidance.StageOnEntry && !st.phaseEntered {
		ctrl.Stage = true
		st.lastStageAt = st.TotalElapsed
	}
	st.phaseEntered = true

	// Auto-stage: active engines are dry while we're commanding thrust — drop the
	// spent stage. Debounced so it fires once, not every tick.
	if p.AutoStage && ph.Guidance.Throttle > 0 && !t.ActiveEngineHasFuel &&
		st.TotalElapsed-st.lastStageAt > stageCooldownS {
		ctrl.Stage = true
		st.lastStageAt = st.TotalElapsed
	}

	// Steering.
	switch {
	case ph.Guidance.PitchProgram != nil:
		ctrl.Steer = SteerPitchHeading
		ctrl.TargetPitch = ph.Guidance.PitchProgram.pitchAt(t.AltitudeM)
		ctrl.TargetHead = p.TargetHeading
	case ph.Guidance.FixedPitchDeg != nil:
		ctrl.Steer = SteerPitchHeading
		ctrl.TargetPitch = *ph.Guidance.FixedPitchDeg
		ctrl.TargetHead = p.TargetHeading
	case ph.Guidance.Prograde:
		ctrl.Steer = SteerPrograde
	default:
		ctrl.Steer = SteerHold
	}

	// Phase transition: terminal condition met → advance (or complete + cut).
	if ph.Until.eval(t, st.PhaseElapsed) {
		st.PhaseIndex++
		st.PhaseElapsed = 0
		st.phaseEntered = false
		if st.PhaseIndex >= len(p.Phases) {
			st.Done = true
			ctrl.Done = true
			ctrl.Throttle = 0
			ctrl.Steer = SteerHold
			ctrl.Note = "program complete — engines cut"
		} else {
			ctrl.Note = "advancing to " + p.Phases[st.PhaseIndex].Name
		}
	}
	return ctrl, st
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
