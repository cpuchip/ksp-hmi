package main

// ascent.go — the SAFE half of the autopilot surface: `plan_ascent` lets CAPCOM
// author a launch-to-orbit flight program, validates it against the safety
// invariants, and returns a spoken read-back. It needs no game connection and
// writes NOTHING — it plans and describes. Flying the program (arming the
// executor and wiring it to real throttle/stage/steer) is the gated Tier-4 wave
// and is deliberately not built here; there is no execute/arm/abort tool yet.

import (
	"github.com/cpuchip/ksp-hmi/autopilot"
)

type ascentInput struct {
	TargetApoapsisM    float64  `json:"target_apoapsis_m" jsonschema:"target apoapsis ALTITUDE in meters to fly the ascent to (e.g. 80000 for an 80 km orbit around Kerbin); required"`
	HeadingDeg         *float64 `json:"heading_deg,omitempty" jsonschema:"compass heading to fly in degrees (90 = due east, the usual prograde launch); default 90"`
	TurnStartAltitudeM *float64 `json:"turn_start_altitude_m,omitempty" jsonschema:"altitude in meters to begin the gravity turn; default 500"`
	TurnEndAltitudeM   *float64 `json:"turn_end_altitude_m,omitempty" jsonschema:"altitude in meters by which to be fully pitched to the horizon; default 45000"`
	MaxDurationS       *float64 `json:"max_duration_s,omitempty" jsonschema:"dead-man timeout in seconds — the program aborts if it overruns; default 600"`
}

type ascentOut struct {
	base
	ProgramName string             `json:"program_name,omitempty"`
	Valid       bool               `json:"valid"`
	Readback    string             `json:"readback,omitempty"`
	Program     *autopilot.Program `json:"program,omitempty"`
	Errors      []string           `json:"errors,omitempty"`
	Note        string             `json:"note"`
}

// planAscent builds + validates + describes a launch-to-orbit program. It never
// touches the game (Available is always true — the crew can plan on the pad or in
// the VAB) and it never flies anything.
func (s *kspServer) planAscent(in ascentInput) ascentOut {
	if in.TargetApoapsisM <= 0 {
		return ascentOut{
			base:  base{Available: true},
			Valid: false,
			Note:  "Give a target apoapsis altitude in meters (e.g. 80000 for an 80 km orbit) and I'll draft the ascent.",
		}
	}
	params := autopilot.AscentParams{TargetApoapsisM: in.TargetApoapsisM}
	if in.HeadingDeg != nil {
		params.HeadingDeg = *in.HeadingDeg
	}
	if in.TurnStartAltitudeM != nil {
		params.TurnStartAltitudeM = *in.TurnStartAltitudeM
	}
	if in.TurnEndAltitudeM != nil {
		params.TurnEndAltitudeM = *in.TurnEndAltitudeM
	}
	if in.MaxDurationS != nil {
		params.MaxDurationS = *in.MaxDurationS
	}

	prog := autopilot.BuildAscent(params)
	errs := autopilot.Validate(prog)

	out := ascentOut{
		base:        base{Available: true},
		ProgramName: prog.Name,
		Program:     &prog,
		Readback:    autopilot.Describe(prog),
		Valid:       len(errs) == 0,
		Note: "This PLANS and reads back the ascent — it does not arm or fly it. Flying a program is the " +
			"gated go/no-go wave (not built yet); nothing here touches the vessel.",
	}
	for _, e := range errs {
		out.Errors = append(out.Errors, e.Error())
	}
	return out
}
