package main

// flight.go — Tier 4: the SPOKEN GO/NO-GO flight-control tools. This is the only
// surface that actually flies the vessel (throttle, staging, autopilot). It is
// registered ONLY when ksp-mcp is started with -enable-flight, and even then it
// fires NOTHING until the pilot has (1) armed a program and (2) given an explicit
// "go" confirmation to flight_execute. The design is Michael's radio metaphor:
// arm → CAPCOM reads the plan back → the crew calls the go → it flies.
//
// Safety layers, outermost first:
//   1. -enable-flight gate: absent → these tools don't exist at all.
//   2. arm/execute split: execute refuses unless a program is armed AND the
//      confirmation is exactly a "go".
//   3. Validate(): the armed program must pass the autopilot safety invariants
//      (throttle bounds, a mandatory dead-man, a terminal condition per phase).
//   4. The runner's guaranteed Stop(): throttle is cut + autopilot disengaged on
//      completion, abort, error, or cancel (see autopilot/runner.go).
//   5. flight_abort: cancels the run at any time.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cpuchip/ksp-hmi/autopilot"
	"github.com/cpuchip/ksp-hmi/krpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerFlightTools wires the Tier-4 spoken go/no-go flight-control tools. It is
// called ONLY when the server is started with -enable-flight; without that flag
// these tools do not exist, and the whole live-control surface is dormant.
func registerFlightTools(s *mcp.Server, srv *kspServer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "flight_arm",
		Description: "ARM a launch-to-orbit ascent to fly (does NOT fire yet). Builds + validates the program and " +
			"returns a spoken read-back for the crew. Set target_apoapsis_m (e.g. 80000). After arming, read the " +
			"plan back, then call flight_execute with confirm=\"go\". Arming fires nothing and needs no craft in " +
			"flight. This tool exists only when the server runs with -enable-flight.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ascentInput) (*mcp.CallToolResult, flightArmOut, error) {
		return nil, srv.flightArm(in), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "flight_execute",
		Description: "FLY the armed program — this is the live-fire trigger; it controls throttle, staging, and the " +
			"autopilot. It REFUSES unless (1) a program is armed and (2) confirm is exactly \"go\" (the crew's " +
			"spoken clearance). Use only after flight_arm and after the crew calls the go. The flight auto-cuts " +
			"throttle on completion or abort. This tool exists only when the server runs with -enable-flight.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flightExecuteInput) (*mcp.CallToolResult, flightExecuteOut, error) {
		return nil, srv.flightExecute(in), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "flight_abort",
		Description: "ABORT the running flight immediately: cut throttle and disengage the autopilot. Use the moment " +
			"the crew calls an abort, or anything looks wrong. Safe to call when nothing is running (it just says " +
			"so). This tool exists only when the server runs with -enable-flight.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, flightAbortOut, error) {
		return nil, srv.flightAbort(), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "flight_status",
		Description: "Report the flight-control state: whether a program is armed, whether a flight is running, the " +
			"current phase and throttle, and the outcome of the last flight (completed or aborted, with the reason). " +
			"Use to follow an executing flight or check what happened. This tool exists only when the server runs " +
			"with -enable-flight.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, flightStatusOut, error) {
		return nil, srv.flightStatus(), nil
	})
}

// flightTickRate is how often the runner reads telemetry and applies control.
// 5 Hz is plenty for an ascent and easy on kRPC; the pure Step logic is unchanged
// at any rate.
const flightTickRate = 200 * time.Millisecond

// flightState is the armed-program + running-flight lifecycle. Guarded by mu.
type flightState struct {
	mu       sync.Mutex
	armed    *autopilot.Program
	readback string

	running   bool
	cancel    context.CancelFunc
	phase     string
	lastCtrl  autopilot.Control
	lastState autopilot.State
	result    *autopilot.RunResult
	resultErr string
	startedAt time.Time
}

// ---- the kRPC-backed ControlSink (the live write surface) ----

type kspControlSink struct {
	c            *krpc.Conn
	control      uint64
	autopilotID  uint64
	surfaceFrame uint64
	engaged      bool
}

func (s *kspControlSink) Apply(ctrl autopilot.Control) error {
	if err := s.c.SetThrottle(s.control, ctrl.Throttle); err != nil {
		return fmt.Errorf("throttle: %w", err)
	}
	switch ctrl.Steer {
	case autopilot.SteerPitchHeading:
		if !s.engaged {
			// SAS and the kRPC autopilot fight each other — hand control to the
			// autopilot, in the surface frame so pitch/heading are horizon-relative.
			_ = s.c.SetSAS(s.control, false)
			_ = s.c.AutopilotSetReferenceFrame(s.autopilotID, s.surfaceFrame)
			if err := s.c.AutopilotEngage(s.autopilotID); err != nil {
				return fmt.Errorf("autopilot engage: %w", err)
			}
			s.engaged = true
		}
		if err := s.c.AutopilotTargetPitchAndHeading(s.autopilotID, ctrl.TargetPitch, ctrl.TargetHead); err != nil {
			return fmt.Errorf("target attitude: %w", err)
		}
	case autopilot.SteerPrograde:
		// Best-effort: hold with SAS. (A dedicated prograde SAS mode is a later
		// refinement; the ascent program uses pitch/heading, not prograde.)
		_ = s.c.SetSAS(s.control, true)
	case autopilot.SteerHold:
		// leave attitude as commanded last
	}
	if ctrl.Stage {
		if err := s.c.ActivateNextStage(s.control); err != nil {
			return fmt.Errorf("stage: %w", err)
		}
	}
	return nil
}

// Stop is the fail-safe: cut throttle and disengage steering. Best-effort on every
// call — it tries all of them and returns the first error, so one failing call
// doesn't skip the others.
func (s *kspControlSink) Stop() error {
	err := s.c.SetThrottle(s.control, 0)
	if s.engaged {
		if derr := s.c.AutopilotDisengage(s.autopilotID); derr != nil && err == nil {
			err = derr
		}
		s.engaged = false
	}
	return err
}

// ---- the kRPC-backed TelemetrySource ----

type kspTelemetrySource struct {
	c      *krpc.Conn
	vessel uint64
}

func (s *kspTelemetrySource) Read() (autopilot.Telemetry, error) {
	ft, err := s.c.FlightTelemetry(s.vessel)
	if err != nil {
		return autopilot.Telemetry{}, err
	}
	o, err := s.c.Orbit(s.vessel)
	if err != nil {
		return autopilot.Telemetry{}, err
	}
	met, err := s.c.VesselMET(s.vessel)
	if err != nil {
		return autopilot.Telemetry{}, err
	}
	avail, err := s.c.VesselAvailableThrust(s.vessel)
	if err != nil {
		return autopilot.Telemetry{}, err
	}
	return autopilot.Telemetry{
		MET:                 met,
		AltitudeM:           ft.MeanAltitudeMeter,
		ApoapsisM:           o.ApoapsisAltitude,
		PeriapsisM:          o.PeriapsisAltitude,
		SurfaceSpeedMS:      ft.HorizontalSpeed,
		VerticalSpeedMS:     ft.VerticalSpeed,
		PitchDeg:            ft.PitchDeg,
		ActiveEngineHasFuel: avail > 0,
	}, nil
}

// ---- tool: flight_arm ----

type flightArmOut struct {
	base
	Armed    bool     `json:"armed"`
	Readback string   `json:"readback,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	Note     string   `json:"note"`
}

// flightArm builds + validates an ascent program and holds it armed. It fires
// NOTHING; it needs no game connection (you can arm on the pad or in the VAB).
func (s *kspServer) flightArm(in ascentInput) flightArmOut {
	if in.TargetApoapsisM <= 0 {
		return flightArmOut{base: base{Available: true}, Note: "Give a target apoapsis (meters) to arm an ascent, e.g. 80000."}
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

	out := flightArmOut{base: base{Available: true}, Readback: autopilot.Describe(prog)}
	for _, e := range errs {
		out.Errors = append(out.Errors, e.Error())
	}
	if len(errs) != 0 {
		out.Note = "Program did NOT validate — not armed. Fix the errors and arm again."
		return out
	}

	s.flight.mu.Lock()
	if s.flight.running {
		s.flight.mu.Unlock()
		out.Note = "A flight is already running — abort it before arming a new program."
		return out
	}
	s.flight.armed = &prog
	s.flight.readback = out.Readback
	s.flight.mu.Unlock()

	out.Armed = true
	out.Note = "ARMED. Read the plan back to the crew, then call flight_execute with confirm=\"go\" to fly it. Nothing has fired."
	return out
}

// ---- tool: flight_execute (the live-fire trigger) ----

type flightExecuteInput struct {
	Confirm string `json:"confirm" jsonschema:"the go/no-go confirmation — must be \"go\" (the crew's spoken clearance) to actually fly the armed program; anything else refuses"`
}

type flightExecuteOut struct {
	base
	Executing bool   `json:"executing"`
	Phase     string `json:"phase,omitempty"`
	Note      string `json:"note"`
}

func (s *kspServer) flightExecute(in flightExecuteInput) flightExecuteOut {
	// Confirmation gate first — before touching the game at all.
	if !isGo(in.Confirm) {
		return flightExecuteOut{base: base{Available: true},
			Note: "NO-GO: flight_execute requires confirm=\"go\" (the crew's spoken clearance). Nothing fired."}
	}

	s.flight.mu.Lock()
	if s.flight.running {
		s.flight.mu.Unlock()
		return flightExecuteOut{base: base{Available: true}, Note: "A flight is already running. Use flight_status, or flight_abort to stop it."}
	}
	prog := s.flight.armed
	s.flight.mu.Unlock()
	if prog == nil {
		return flightExecuteOut{base: base{Available: true}, Note: "Nothing armed. Call flight_arm first, read it back, then execute with confirm=\"go\"."}
	}

	// Resolve the live control handles. Any failure here fires nothing.
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return flightExecuteOut{base: base{Available: false, Message: fmt.Sprintf("can't resolve vessel: %v", err)}, Note: "Not executing."}
	}
	if !ok {
		return flightExecuteOut{base: b, Note: "No active vessel to fly. Not executing."}
	}
	control, err := c.VesselControl(vessel)
	if err != nil {
		return flightExecuteOut{base: base{Available: false, Message: fmt.Sprintf("control: %v", err)}, Note: "Not executing."}
	}
	ap, err := c.VesselAutoPilot(vessel)
	if err != nil {
		return flightExecuteOut{base: base{Available: false, Message: fmt.Sprintf("autopilot: %v", err)}, Note: "Not executing."}
	}
	frame, err := c.VesselSurfaceFrame(vessel)
	if err != nil {
		return flightExecuteOut{base: base{Available: false, Message: fmt.Sprintf("surface frame: %v", err)}, Note: "Not executing."}
	}

	sink := &kspControlSink{c: c, control: control, autopilotID: ap, surfaceFrame: frame}
	src := &kspTelemetrySource{c: c, vessel: vessel}

	ctx, cancel := context.WithCancel(context.Background())
	s.flight.mu.Lock()
	s.flight.running = true
	s.flight.cancel = cancel
	s.flight.result = nil
	s.flight.resultErr = ""
	s.flight.phase = "starting"
	s.flight.startedAt = time.Now()
	s.flight.mu.Unlock()

	go func() {
		res, rerr := autopilot.Run(ctx, *prog, src, sink, flightTickRate, func(ctrl autopilot.Control, st autopilot.State) {
			s.flight.mu.Lock()
			s.flight.lastCtrl = ctrl
			s.flight.lastState = st
			if ctrl.Phase != "" {
				s.flight.phase = ctrl.Phase
			}
			s.flight.mu.Unlock()
		})
		s.flight.mu.Lock()
		s.flight.running = false
		s.flight.cancel = nil
		s.flight.result = &res
		if rerr != nil {
			s.flight.resultErr = rerr.Error()
		}
		if res.Completed {
			s.flight.phase = "complete"
		} else {
			s.flight.phase = "aborted"
		}
		s.flight.mu.Unlock()
	}()

	return flightExecuteOut{base: base{Available: true}, Executing: true, Phase: "starting",
		Note: "GO — flying the armed program. Engines are live. Call flight_status to follow it, flight_abort to stop. It cuts throttle automatically on completion or abort."}
}

// ---- tool: flight_abort ----

type flightAbortOut struct {
	base
	Aborted bool   `json:"aborted"`
	Note    string `json:"note"`
}

func (s *kspServer) flightAbort() flightAbortOut {
	s.flight.mu.Lock()
	running := s.flight.running
	cancel := s.flight.cancel
	s.flight.mu.Unlock()
	if !running || cancel == nil {
		return flightAbortOut{base: base{Available: true}, Note: "No flight is running — nothing to abort."}
	}
	cancel()
	return flightAbortOut{base: base{Available: true}, Aborted: true,
		Note: "ABORT sent — the flight loop cuts throttle and disengages the autopilot. Check flight_status to confirm it stopped."}
}

// ---- tool: flight_status ----

type flightStatusOut struct {
	base
	EnabledForFlight bool    `json:"enabled_for_flight"`
	Armed            bool    `json:"armed"`
	Running          bool    `json:"running"`
	Phase            string  `json:"phase,omitempty"`
	Throttle         float64 `json:"throttle,omitempty"`
	Completed        bool    `json:"completed,omitempty"`
	Aborted          bool    `json:"aborted,omitempty"`
	AbortReason      string  `json:"abort_reason,omitempty"`
	ElapsedSeconds   float64 `json:"elapsed_seconds,omitempty"`
	Readback         string  `json:"readback,omitempty"`
	Note             string  `json:"note"`
}

func (s *kspServer) flightStatus() flightStatusOut {
	s.flight.mu.Lock()
	defer s.flight.mu.Unlock()
	out := flightStatusOut{
		base:             base{Available: true},
		EnabledForFlight: true,
		Armed:            s.flight.armed != nil,
		Running:          s.flight.running,
		Phase:            s.flight.phase,
		Readback:         s.flight.readback,
	}
	if s.flight.running {
		out.Throttle = round2(s.flight.lastCtrl.Throttle)
		out.ElapsedSeconds = round2(time.Since(s.flight.startedAt).Seconds())
		out.Note = "Flying. " + s.flight.phase
		return out
	}
	if r := s.flight.result; r != nil {
		out.Completed = r.Completed
		out.Aborted = r.Aborted
		out.AbortReason = r.AbortReason
		if s.flight.resultErr != "" {
			out.AbortReason = firstNonEmptyStr(out.AbortReason, s.flight.resultErr)
		}
		if r.Completed {
			out.Note = "Last flight completed — engines cut at target."
		} else {
			out.Note = "Last flight aborted: " + out.AbortReason
		}
		return out
	}
	if out.Armed {
		out.Note = "A program is armed and read back. Call flight_execute with confirm=\"go\" to fly it."
		return out
	}
	out.Note = "Flight control enabled, nothing armed. flight_arm to build an ascent."
	return out
}

// isGo accepts only an explicit affirmative clearance.
func isGo(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "go", "go for launch", "we are go", "affirmative":
		return true
	}
	return false
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
