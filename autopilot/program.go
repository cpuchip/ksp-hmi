// Package autopilot is the deterministic flight-program executor — the "fast
// loop" pilot that CAPCOM (the slow, heavy LLM loop) writes programs for. It is
// PURE: it takes a Program plus a Telemetry snapshot and returns Control outputs
// (throttle / steering / stage / abort), with no dependency on kRPC or the game.
// That purity is the whole safety and testability story: the flight logic can be
// ground against synthetic telemetry and a toy sim until it provably sequences a
// launch, before a single byte is ever written to a live vessel.
//
// The vocabulary is deliberately BOUNDED — a Program is a validated list of
// phases with typed conditions and a small guidance grammar, not arbitrary code.
// CAPCOM composes known-safe primitives; it cannot express anything outside them.
// Package cmd/ksp-mcp turns Control outputs into real kRPC control writes (that is
// the gated Tier-4 wave); nothing in THIS package fires anything.
package autopilot

import (
	"fmt"
	"math"
	"strings"
)

// Condition field names — the only telemetry fields a condition may test.
const (
	FieldAltitude      = "altitude"
	FieldApoapsis      = "apoapsis"
	FieldPeriapsis     = "periapsis"
	FieldSurfaceSpeed  = "surface_speed"
	FieldOrbitalSpeed  = "orbital_speed"
	FieldVerticalSpeed = "vertical_speed"
	FieldMET           = "met"
	FieldTimeInPhase   = "time_in_phase"
	FieldPitch         = "pitch"
)

var validFields = map[string]bool{
	FieldAltitude: true, FieldApoapsis: true, FieldPeriapsis: true,
	FieldSurfaceSpeed: true, FieldOrbitalSpeed: true, FieldVerticalSpeed: true,
	FieldMET: true, FieldTimeInPhase: true, FieldPitch: true,
}

// Condition is a single typed threshold test on telemetry, e.g. {apoapsis gt
// 80000}. It is the only conditional primitive — no expressions, no code.
type Condition struct {
	Field string  `json:"field"`
	Op    string  `json:"op"` // "gt" | "lt"
	Value float64 `json:"value"`
}

// value pulls the tested field out of a telemetry snapshot (time_in_phase comes
// from the executor's per-phase elapsed clock, not telemetry).
func (c Condition) value(t Telemetry, phaseElapsed float64) float64 {
	switch c.Field {
	case FieldAltitude:
		return t.AltitudeM
	case FieldApoapsis:
		return t.ApoapsisM
	case FieldPeriapsis:
		return t.PeriapsisM
	case FieldSurfaceSpeed:
		return t.SurfaceSpeedMS
	case FieldOrbitalSpeed:
		return t.OrbitalSpeedMS
	case FieldVerticalSpeed:
		return t.VerticalSpeedMS
	case FieldMET:
		return t.MET
	case FieldTimeInPhase:
		return phaseElapsed
	case FieldPitch:
		return t.PitchDeg
	default:
		return math.NaN()
	}
}

func (c Condition) eval(t Telemetry, phaseElapsed float64) bool {
	v := c.value(t, phaseElapsed)
	if math.IsNaN(v) {
		return false
	}
	switch c.Op {
	case "gt":
		return v > c.Value
	case "lt":
		return v < c.Value
	default:
		return false
	}
}

func (c Condition) describe() string {
	op := c.Op
	switch c.Op {
	case "gt":
		op = "≥"
	case "lt":
		op = "≤"
	}
	unit := ""
	switch c.Field {
	case FieldAltitude, FieldApoapsis, FieldPeriapsis:
		if c.Value >= 1000 {
			return fmt.Sprintf("%s %s %.0f km", c.Field, op, c.Value/1000)
		}
		unit = " m"
	case FieldSurfaceSpeed, FieldOrbitalSpeed, FieldVerticalSpeed:
		unit = " m/s"
	case FieldMET, FieldTimeInPhase:
		unit = " s"
	case FieldPitch:
		unit = "°"
	}
	return fmt.Sprintf("%s %s %.0f%s", c.Field, op, c.Value, unit)
}

func (c Condition) valid() bool {
	return validFields[c.Field] && (c.Op == "gt" || c.Op == "lt")
}

// PitchProgram is a gravity-turn schedule: pitch interpolated linearly with
// altitude between two anchor points (e.g. 90° up at 500 m → 0° at 45 km).
type PitchProgram struct {
	StartAltitudeM float64 `json:"start_altitude_m"`
	EndAltitudeM   float64 `json:"end_altitude_m"`
	StartPitchDeg  float64 `json:"start_pitch_deg"`
	EndPitchDeg    float64 `json:"end_pitch_deg"`
}

// pitchAt returns the scheduled pitch for an altitude, clamped to the endpoints.
func (p PitchProgram) pitchAt(altitude float64) float64 {
	if altitude <= p.StartAltitudeM {
		return p.StartPitchDeg
	}
	if altitude >= p.EndAltitudeM || p.EndAltitudeM <= p.StartAltitudeM {
		return p.EndPitchDeg
	}
	frac := (altitude - p.StartAltitudeM) / (p.EndAltitudeM - p.StartAltitudeM)
	return p.StartPitchDeg + frac*(p.EndPitchDeg-p.StartPitchDeg)
}

// Guidance is what a phase commands: throttle, how to steer, and whether to fire
// staging on entry. Exactly one steering mode applies per phase.
type Guidance struct {
	Throttle      float64       `json:"throttle"`                 // 0..1
	PitchProgram  *PitchProgram `json:"pitch_program,omitempty"`  // steer to a scheduled pitch...
	FixedPitchDeg *float64      `json:"fixed_pitch_deg,omitempty"` // ...or hold a fixed pitch...
	Prograde      bool          `json:"prograde,omitempty"`        // ...or hold prograde...
	// (none set → hold current attitude)
	StageOnEntry bool `json:"stage_on_entry,omitempty"` // fire staging once when the phase begins
}

// Phase is one leg of a flight program: a guidance command held until a terminal
// condition is met, at which point the executor advances to the next phase.
type Phase struct {
	Name     string    `json:"name"`
	Guidance Guidance  `json:"guidance"`
	Until    Condition `json:"until"`
}

// Program is a complete, bounded flight plan. AutoStage fires staging whenever the
// active engines run dry; MaxDurationS is a mandatory dead-man that aborts the
// whole program if it overruns; Abort conditions abort on any hard limit.
type Program struct {
	Name          string      `json:"name"`
	TargetHeading float64     `json:"target_heading_deg"`
	Phases        []Phase     `json:"phases"`
	AutoStage     bool        `json:"auto_stage"`
	Abort         []Condition `json:"abort,omitempty"`
	MaxDurationS  float64     `json:"max_duration_s"`
}

// Validate returns every safety/shape problem with a program. A program that
// returns no errors is safe to ARM (it still needs the crew's spoken go to run).
// The invariants are the safety floor: bounded throttle, a mandatory dead-man,
// and a terminal condition on every phase so nothing can run forever.
func Validate(p Program) []error {
	var errs []error
	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, fmt.Errorf("program has no name"))
	}
	if len(p.Phases) == 0 {
		errs = append(errs, fmt.Errorf("program has no phases"))
	}
	if p.MaxDurationS <= 0 {
		errs = append(errs, fmt.Errorf("max_duration_s must be > 0 (the mandatory dead-man timeout)"))
	}
	if p.TargetHeading < 0 || p.TargetHeading > 360 {
		errs = append(errs, fmt.Errorf("target_heading_deg %.0f out of range [0,360]", p.TargetHeading))
	}
	for i, ph := range p.Phases {
		where := fmt.Sprintf("phase %d (%q)", i, ph.Name)
		if strings.TrimSpace(ph.Name) == "" {
			errs = append(errs, fmt.Errorf("phase %d has no name", i))
		}
		if ph.Guidance.Throttle < 0 || ph.Guidance.Throttle > 1 {
			errs = append(errs, fmt.Errorf("%s: throttle %.2f out of range [0,1]", where, ph.Guidance.Throttle))
		}
		if !ph.Until.valid() {
			errs = append(errs, fmt.Errorf("%s: invalid terminal condition {field=%q op=%q} — every phase must end on a valid condition",
				where, ph.Until.Field, ph.Until.Op))
		}
		steerModes := 0
		if ph.Guidance.PitchProgram != nil {
			steerModes++
		}
		if ph.Guidance.FixedPitchDeg != nil {
			steerModes++
		}
		if ph.Guidance.Prograde {
			steerModes++
		}
		if steerModes > 1 {
			errs = append(errs, fmt.Errorf("%s: more than one steering mode set (pick pitch_program, fixed_pitch, or prograde)", where))
		}
	}
	for i, a := range p.Abort {
		if !a.valid() {
			errs = append(errs, fmt.Errorf("abort condition %d invalid {field=%q op=%q}", i, a.Field, a.Op))
		}
	}
	return errs
}

// Describe renders a program as a spoken-friendly read-back for the crew — the
// text CAPCOM reads before asking for a go. Never claims it will fire; it states
// what the program WOULD do once armed and cleared.
func Describe(p Program) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Flight program %q — heading %.0f°, auto-stage %s, dead-man %.0fs.\n",
		p.Name, p.TargetHeading, onOff(p.AutoStage), p.MaxDurationS)
	for i, ph := range p.Phases {
		fmt.Fprintf(&b, "  %d. %s: %s, %s, until %s.\n",
			i+1, ph.Name, throttleWord(ph.Guidance.Throttle), steerWord(ph.Guidance), ph.Until.describe())
	}
	if len(p.Abort) > 0 {
		parts := make([]string, 0, len(p.Abort))
		for _, a := range p.Abort {
			parts = append(parts, a.describe())
		}
		fmt.Fprintf(&b, "  Abort if: %s.\n", strings.Join(parts, "; "))
	}
	b.WriteString("  Nothing fires until the crew calls the go.")
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func throttleWord(t float64) string {
	switch {
	case t <= 0:
		return "engines cut"
	case t >= 1:
		return "full throttle"
	default:
		return fmt.Sprintf("%.0f%% throttle", t*100)
	}
}

func steerWord(g Guidance) string {
	switch {
	case g.PitchProgram != nil:
		return fmt.Sprintf("pitch %.0f°→%.0f° over %.0f–%.0f m",
			g.PitchProgram.StartPitchDeg, g.PitchProgram.EndPitchDeg,
			g.PitchProgram.StartAltitudeM, g.PitchProgram.EndAltitudeM)
	case g.FixedPitchDeg != nil:
		return fmt.Sprintf("hold pitch %.0f°", *g.FixedPitchDeg)
	case g.Prograde:
		return "hold prograde"
	default:
		return "hold attitude"
	}
}

// AscentParams are the knobs CAPCOM sets to author a launch-to-orbit program.
// Zero values fall back to sane KSP/Kerbin defaults.
type AscentParams struct {
	TargetApoapsisM    float64 // required; the apoapsis to cut the ascent at
	HeadingDeg         float64 // default 90 (due east, prograde for most orbits)
	TurnStartAltitudeM float64 // default 500  (begin the gravity turn)
	TurnEndAltitudeM   float64 // default 45000 (be horizontal by here)
	MaxDurationS       float64 // default 600  (dead-man)
}

// BuildAscent constructs a validated-shape launch-to-orbit program: a full-throttle
// vertical liftoff (staging on the pad), a gravity turn that pitches from vertical
// to the horizon on an altitude schedule, and engine cutoff the instant apoapsis
// reaches the target. Circularization is a SEPARATE step (place + execute a node)
// — this program flies the ascent and cuts; it does not attempt the circular burn.
func BuildAscent(p AscentParams) Program {
	if p.HeadingDeg == 0 {
		p.HeadingDeg = 90
	}
	if p.TurnStartAltitudeM == 0 {
		p.TurnStartAltitudeM = 500
	}
	if p.TurnEndAltitudeM == 0 {
		p.TurnEndAltitudeM = 45000
	}
	if p.MaxDurationS == 0 {
		p.MaxDurationS = 600
	}
	up := 90.0
	return Program{
		Name:          fmt.Sprintf("ascent to %.0f km", p.TargetApoapsisM/1000),
		TargetHeading: p.HeadingDeg,
		AutoStage:     true,
		MaxDurationS:  p.MaxDurationS, // the dead-man is the ascent's hard limit
		Phases: []Phase{
			{
				Name:     "liftoff",
				Guidance: Guidance{Throttle: 1, FixedPitchDeg: &up, StageOnEntry: true},
				Until:    Condition{Field: FieldAltitude, Op: "gt", Value: p.TurnStartAltitudeM},
			},
			{
				Name: "gravity turn",
				Guidance: Guidance{
					Throttle: 1,
					PitchProgram: &PitchProgram{
						StartAltitudeM: p.TurnStartAltitudeM,
						EndAltitudeM:   p.TurnEndAltitudeM,
						StartPitchDeg:  90,
						EndPitchDeg:    0,
					},
				},
				Until: Condition{Field: FieldApoapsis, Op: "gt", Value: p.TargetApoapsisM},
			},
		},
	}
}
