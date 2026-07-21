package autopilot

// runner.go — the loop that turns the pure Step function into a live-flown
// autopilot. It is interface-based (TelemetrySource / ControlSink) so it is
// unit-tested against a mock sink and a scripted telemetry source with NO kRPC
// dependency; the cmd/ksp-mcp layer provides the live kRPC-backed implementations.
//
// Safety invariant: sink.Stop() (cut throttle + neutralize) is called EXACTLY
// ONCE on every exit path — normal completion, abort, telemetry/apply error,
// context cancellation, or a panic in Step. The vessel must never be left under
// power because the loop returned.

import (
	"context"
	"fmt"
	"time"
)

// TelemetrySource yields a fresh Telemetry snapshot each tick (the live vessel,
// or a scripted sim in tests).
type TelemetrySource interface {
	Read() (Telemetry, error)
}

// ControlSink applies a Control to the vessel and can bring it to a safe idle.
// Apply sets throttle/steering and fires staging as commanded; Stop cuts throttle
// and disengages steering (the fail-safe).
type ControlSink interface {
	Apply(Control) error
	Stop() error
}

// RunResult is the outcome of a flown program.
type RunResult struct {
	Final       State
	Completed   bool   // reached the end of the program cleanly
	Aborted     bool   // dead-man, abort condition, cancel, or error
	AbortReason string // populated when Aborted
}

// Run flies a program to completion, abort, or context cancellation, ticking at
// dt. onTick (may be nil) is called after each Step with the applied Control and
// the new State — the MCP layer uses it to publish live status. Run always leaves
// the sink Stopped.
func Run(ctx context.Context, p Program, src TelemetrySource, sink ControlSink, dt time.Duration, onTick func(Control, State)) (res RunResult, err error) {
	// The single guaranteed Stop: whatever happens below (return, panic), the
	// vessel is brought to idle exactly once.
	stopped := false
	stop := func(reason string) {
		if stopped {
			return
		}
		stopped = true
		if serr := sink.Stop(); serr != nil && err == nil {
			err = fmt.Errorf("sink stop failed: %w", serr)
		}
		res.AbortReason = firstNonEmpty(res.AbortReason, reason)
	}
	defer func() {
		if r := recover(); r != nil {
			stop(fmt.Sprintf("panic in flight loop: %v", r))
			res.Aborted = true
			if err == nil {
				err = fmt.Errorf("flight loop panic: %v", r)
			}
		}
	}()

	if dt <= 0 {
		dt = 100 * time.Millisecond
	}
	st := State{}
	ticker := time.NewTicker(dt)
	defer ticker.Stop()
	dtSec := dt.Seconds()

	for {
		select {
		case <-ctx.Done():
			stop("context cancelled — abort")
			res.Final, res.Aborted = st, true
			return res, err
		case <-ticker.C:
			tel, rerr := src.Read()
			if rerr != nil {
				stop("telemetry read failed — abort")
				res.Final, res.Aborted = st, true
				if err == nil {
					err = fmt.Errorf("telemetry read: %w", rerr)
				}
				return res, err
			}

			ctrl, ns := Step(p, tel, st, dtSec)
			st = ns

			if aerr := sink.Apply(ctrl); aerr != nil {
				stop("control apply failed — abort")
				res.Final, res.Aborted = st, true
				if err == nil {
					err = fmt.Errorf("control apply: %w", aerr)
				}
				return res, err
			}
			if onTick != nil {
				onTick(ctrl, st)
			}

			if ctrl.Abort {
				stop(ctrl.AbortReason)
				res.Final, res.Aborted = st, true
				return res, err
			}
			if ctrl.Done {
				stop("")
				res.Final, res.Completed = st, true
				return res, err
			}
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
