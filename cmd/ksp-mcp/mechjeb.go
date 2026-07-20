package main

// mechjeb.go — the MechJeb-backed planners (Tier 3, MechJeb edition): real
// intercepts, rendezvous, ejection burns, and returns computed by KRPC.MechJeb's
// ManeuverPlanner and placed as maneuver NODES. Like the native Tier 3, the only
// thing these mutate is the flight plan (reversible; node_delete / node_clear undo
// them). They NEVER touch MechJeb's NodeExecutor/autopilot — no engine fires.
//
// Three-way graceful degradation, all live-verified on the real path:
//   1. Mod absent      (MechJebAvailable false)     -> native fallback where one
//                                                       exists (plan_intercept ->
//                                                       native Hohmann), else an
//                                                       honest "install the mod".
//   2. Mod present but its planner binding is broken (KRPC.MechJeb vs MechJeb2
//      version mismatch — APIReady lies; every MakeNodes NREs) -> same fallback,
//      plus a note naming the incompatibility so it can be fixed.
//   3. Planner functional -> drive MechJeb, place the node(s), read back the
//      resulting orbit + closest approach so the CAPCOM can brief it.

import (
	"fmt"

	"github.com/cpuchip/ksp-hmi/krpc"
)

// planMJOut is the uniform readback for every MechJeb-backed planner.
type planMJOut struct {
	base
	Planner              string          `json:"planner,omitempty"` // "mechjeb" | "native" | ""
	Placed               bool            `json:"placed"`
	NodeCount            int             `json:"node_count"`
	Nodes                []nodeResultOut `json:"nodes,omitempty"`
	TotalDVMS            float64         `json:"total_dv_ms"`
	TargetName           string          `json:"target_name,omitempty"`
	ClosestApproachM     float64         `json:"closest_approach_m,omitempty"`
	TimeToClosestSeconds float64         `json:"time_to_closest_seconds,omitempty"`
	TimeToClosest        string          `json:"time_to_closest,omitempty"`
	MechJebAvailable     bool            `json:"mechjeb_available"`
	Note                 string          `json:"note,omitempty"`
}

// ---- shared preflight + degradation helpers ----

// mechjebState classifies the MechJeb capability once per call: is the mod present,
// and can its planner actually place nodes on this install. degradeNote is the
// spoken-friendly explanation to use when functional is false.
type mechjebState struct {
	available   bool
	functional  bool
	degradeNote string
}

func (s *kspServer) mechjebState(fc flightCtx) mechjebState {
	st := mechjebState{available: fc.c.MechJebAvailable()}
	if !st.available {
		st.degradeNote = "The KRPC.MechJeb mod isn't installed, so MechJeb's professional planner isn't available here. " +
			"Install KRPC.MechJeb alongside MechJeb2 for optimized intercepts/rendezvous, or use the native planners."
		return st
	}
	functional, detail := fc.c.MechJebPlannerFunctional()
	st.functional = functional
	if !functional {
		st.degradeNote = "MechJeb is installed but its maneuver planner can't run on this install — KRPC.MechJeb and the " +
			"installed MechJeb2 versions don't match, so MechJeb reports ready but every plan throws. " +
			"Fix: install a KRPC.MechJeb build matching your MechJeb2 version. [MechJeb: " + detail + "]"
	}
	return st
}

// noExistingNodesOrNote guards against clobbering a plan the pilot already has:
// MechJeb APPENDS nodes, so if any exist we refuse and ask them to clear first.
// Returns control id and ok=true when the plan is empty and it's safe to proceed.
func (s *kspServer) noExistingNodesOrNote(fc flightCtx, out *planMJOut) (control uint64, ok bool, err error) {
	control, err = fc.c.VesselControl(fc.vessel)
	if err != nil {
		return 0, false, err
	}
	existing, err := fc.c.ControlNodes(control)
	if err != nil {
		return 0, false, err
	}
	if len(existing) != 0 {
		out.base = base{Available: true}
		out.MechJebAvailable = fc.c.MechJebAvailable()
		out.Note = fmt.Sprintf("You already have %d maneuver node(s) on the plan — clear them (node_clear) before planning a new "+
			"intercept, so I don't stack nodes on top of each other.", len(existing))
		return 0, false, nil
	}
	return control, true, nil
}

// readMechJebNodes reads back the node(s) MechJeb placed: each node's burn +
// resulting orbit, the summed delta-v, and (same-primary only) the predicted closest
// approach of the final orbit to the target. targetOrbit==0 skips closest approach.
func (s *kspServer) readMechJebNodes(fc flightCtx, out *planMJOut, nodeIDs []uint64, targetOrbit, targetBodyID uint64) error {
	out.NodeCount = len(nodeIDs)
	for _, id := range nodeIDs {
		res, err := s.readNodeResult(fc, id)
		if err != nil {
			return err
		}
		out.Nodes = append(out.Nodes, res)
		out.TotalDVMS += res.DeltaVMS
	}
	out.TotalDVMS = round2(out.TotalDVMS)
	// Closest approach of the last node's resulting orbit to the target, when both
	// share the primary body (kRPC's conic solver; cross-SOI transfers don't apply).
	if len(nodeIDs) > 0 && targetOrbit != 0 && targetBodyID == fc.bodyID {
		last := nodeIDs[len(nodeIDs)-1]
		if nd, err := fc.c.NodeDetail(last); err == nil && nd.OrbitID != 0 {
			if dist, tut, err := fc.c.OrbitClosestApproach(nd.OrbitID, targetOrbit); err == nil {
				out.ClosestApproachM = round2(dist)
				out.TimeToClosestSeconds = round2(tut - fc.ut)
				out.TimeToClosest = fmtDuration(tut - fc.ut)
			}
		}
	}
	return nil
}

// requireTarget resolves the current target and fails gracefully (into out) when
// none is set — every intercept/rendezvous/velocity-match op needs one.
func (s *kspServer) requireTarget(fc flightCtx, out *planMJOut) (tr targetResolved, ok bool, err error) {
	tr, err = s.resolveTarget(fc)
	if err != nil {
		return tr, false, err
	}
	if tr.kind == "none" {
		out.base = base{Available: true}
		out.MechJebAvailable = fc.c.MechJebAvailable()
		out.Note = "No target set. Right-click a vessel/body (or pick one in the map) to target it, then ask again."
		return tr, false, nil
	}
	out.TargetName = tr.name
	return tr, true, nil
}

// ================= plan_intercept =================

type interceptInput struct {
	Optimized bool `json:"optimized,omitempty" jsonschema:"use MechJeb's optimizing transfer (handles inclination, best closest approach) instead of the fast coplanar Hohmann; default false"`
}

func (s *kspServer) planIntercept(in interceptInput) (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	tr, ok, err := s.requireTarget(fc, &out)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if !ok {
		return out, nil
	}
	// Guard up front (both the MechJeb and native paths place nodes) so we never
	// stack a new intercept on top of an existing plan.
	if _, ok, err := s.noExistingNodesOrNote(fc, &out); err != nil {
		s.drop()
		return planMJOut{}, err
	} else if !ok {
		return out, nil
	}

	st := s.mechjebState(fc)
	if !st.functional {
		// Native fallback: a Hohmann departure node to the target (live-verified).
		return s.nativeInterceptFallback(fc, st.degradeNote)
	}

	res, err := fc.c.PlanTransfer(!in.Optimized, false)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	return s.finishMechJeb(fc, out, res, tr, "MechJeb intercept transfer placed. Nothing fired — the burn is drawn on the navball. "+
		"After you're on the transfer, use plan_match_velocity for the matching burn. Undo with node_delete/node_clear.")
}

// nativeInterceptFallback places a native Hohmann departure node to the current
// target (reusing the textbook astro path) when MechJeb can't. It reports planner:
// "native" and folds in why MechJeb was unavailable.
func (s *kspServer) nativeInterceptFallback(fc flightCtx, why string) (planMJOut, error) {
	ph, err := s.planHohmann(hohmannInput{})
	if err != nil {
		return planMJOut{}, err
	}
	out := planMJOut{
		base:             base{Available: true},
		Planner:          "native",
		MechJebAvailable: fc.c.MechJebAvailable(),
		TargetName:       ph.TargetName,
	}
	if !ph.Placed {
		out.Note = "Native fallback: " + ph.Note + " (" + why + ")"
		return out, nil
	}
	out.Placed = true
	out.NodeCount = 1
	out.Nodes = []nodeResultOut{ph.Node}
	out.TotalDVMS = ph.Node.DeltaVMS
	out.TimeToClosestSeconds = ph.TimeToBurnSeconds // reuse field for time-to-burn on native
	out.Note = "Placed a native Hohmann departure node to the target (MechJeb unavailable, so this is the transparent textbook " +
		"transfer, timed for the next window). It gets you onto an intercept; you'll still need an arrival/match burn. " +
		"Undo with node_delete/node_clear. [" + why + "]"
	return out, nil
}

// finishMechJeb turns a MechJebNodes result into a planMJOut: relays MechJeb's own
// refusal honestly, else reads back the placed node(s).
func (s *kspServer) finishMechJeb(fc flightCtx, out planMJOut, res krpc.MechJebNodes, tr targetResolved, successNote string) (planMJOut, error) {
	out.Planner = "mechjeb"
	if res.Error != "" || len(res.Nodes) == 0 {
		out.Note = mechjebRefusal(res.Error)
		return out, nil
	}
	if err := s.readMechJebNodes(fc, &out, res.Nodes, tr.orbit, tr.bodyID); err != nil {
		s.drop()
		return planMJOut{}, err
	}
	out.Placed = true
	out.Note = successNote
	return out, nil
}

// mechjebRefusal renders MechJeb's own error honestly, distinguishing a clean
// "can't do that here" from the broken-binding NRE.
func mechjebRefusal(errMsg string) string {
	if errMsg == "" {
		return "MechJeb placed no nodes and gave no reason — nothing was changed."
	}
	if isBindingNRE(errMsg) {
		return "MechJeb threw an internal error placing the node (" + errMsg + ") — this is the KRPC.MechJeb/MechJeb2 version " +
			"mismatch on this install. Nothing was changed; use the native planners until the mod versions are aligned."
	}
	return "MechJeb couldn't place the node: " + errMsg + " (nothing was changed)."
}

func isBindingNRE(msg string) bool {
	return contains(msg, "Object reference not set")
}

// contains is a tiny substring check (avoid importing strings for one use).
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ================= plan_rendezvous =================

type rendezvousInput struct {
	Optimized bool `json:"optimized,omitempty" jsonschema:"use MechJeb's optimizing transfer instead of the fast coplanar one; default false"`
}

func (s *kspServer) planRendezvous(in rendezvousInput) (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	tr, ok, err := s.requireTarget(fc, &out)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if !ok {
		return out, nil
	}
	if _, ok, err := s.noExistingNodesOrNote(fc, &out); err != nil {
		s.drop()
		return planMJOut{}, err
	} else if !ok {
		return out, nil
	}

	st := s.mechjebState(fc)
	if !st.functional {
		// Partial native fallback: the transfer half only; match-velocity needs MechJeb.
		fb, err := s.nativeInterceptFallback(fc, st.degradeNote)
		if err != nil {
			return planMJOut{}, err
		}
		if fb.Placed {
			fb.Note = "Rendezvous, native fallback: placed the transfer/intercept node only. The second burn (match velocity at " +
				"closest approach) needs MechJeb, which isn't usable here — " + st.degradeNote
		}
		return fb, nil
	}

	// Two burns: transfer to intercept, then kill relative velocity at closest approach.
	transfer, err := fc.c.PlanTransfer(!in.Optimized, true)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if transfer.Error != "" || len(transfer.Nodes) == 0 {
		out.Planner = "mechjeb"
		out.Note = "Transfer burn: " + mechjebRefusal(transfer.Error)
		return out, nil
	}
	killrelvel, err := fc.c.PlanKillRelVel()
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	all := append([]uint64{}, transfer.Nodes...)
	note := "MechJeb rendezvous: transfer-to-intercept node placed"
	if killrelvel.Error != "" || len(killrelvel.Nodes) == 0 {
		note += ", but the match-velocity burn couldn't be added (" + shortReason(killrelvel.Error) + "). " +
			"Fly the transfer, then call plan_match_velocity at closest approach."
	} else {
		all = append(all, killrelvel.Nodes...)
		note += " plus a match-velocity (kill relative velocity) node at closest approach. Note: the second burn is computed from " +
			"your CURRENT orbit — re-run plan_match_velocity after the transfer for a precise match. "
	}
	note += " Nothing fired. Undo with node_clear."
	out.Planner = "mechjeb"
	if err := s.readMechJebNodes(fc, &out, all, tr.orbit, tr.bodyID); err != nil {
		s.drop()
		return planMJOut{}, err
	}
	out.Placed = true
	out.Note = note
	return out, nil
}

func shortReason(errMsg string) string {
	if errMsg == "" {
		return "no reason given"
	}
	if isBindingNRE(errMsg) {
		return "MechJeb version mismatch"
	}
	return errMsg
}

// ================= plan_match_velocity =================

func (s *kspServer) planMatchVelocity() (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	tr, ok, err := s.requireTarget(fc, &out)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if !ok {
		return out, nil
	}

	st := s.mechjebState(fc)
	if !st.functional {
		out.Note = "Matching velocity with the target needs MechJeb (there's no native equivalent — it requires the target's " +
			"velocity at closest approach). " + st.degradeNote
		return out, nil
	}
	res, err := fc.c.PlanKillRelVel()
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	return s.finishMechJeb(fc, out, res, tr, "MechJeb match-velocity (kill relative velocity) node placed at closest approach. "+
		"Nothing fired — undo with node_delete/node_clear.")
}

// ================= plan_interplanetary =================

type interplanetaryInput struct {
	WaitForWindow *bool `json:"wait_for_window,omitempty" jsonschema:"wait for the optimal phase-angle transfer window (default true); false ejects as soon as possible"`
}

func (s *kspServer) planInterplanetary(in interplanetaryInput) (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	tr, ok, err := s.requireTarget(fc, &out)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if !ok {
		return out, nil
	}

	st := s.mechjebState(fc)
	if !st.functional {
		out.Note = "An interplanetary ejection burn needs MechJeb (no native equivalent yet). " + st.degradeNote
		return out, nil
	}
	if _, ok, err := s.noExistingNodesOrNote(fc, &out); err != nil {
		s.drop()
		return planMJOut{}, err
	} else if !ok {
		return out, nil
	}
	wait := true
	if in.WaitForWindow != nil {
		wait = *in.WaitForWindow
	}
	res, err := fc.c.PlanInterplanetary(wait)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	return s.finishMechJeb(fc, out, res, tr, "MechJeb interplanetary ejection burn placed. Nothing fired — undo with node_delete/node_clear.")
}

// ================= plan_return =================

type returnInput struct {
	ReturnAltitudeM *float64 `json:"return_altitude_m,omitempty" jsonschema:"target periapsis altitude (meters above the parent body's sea level) for the return; default 30000"`
}

func (s *kspServer) planReturn(in returnInput) (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	st := s.mechjebState(fc)
	if !st.functional {
		out.Note = "A moon-return burn needs MechJeb (no native equivalent yet). " + st.degradeNote
		return out, nil
	}
	if _, ok, err := s.noExistingNodesOrNote(fc, &out); err != nil {
		s.drop()
		return planMJOut{}, err
	} else if !ok {
		return out, nil
	}
	alt := 30000.0
	if in.ReturnAltitudeM != nil {
		alt = *in.ReturnAltitudeM
	}
	res, err := fc.c.PlanMoonReturn(alt)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	// No target needed; closest-approach readback is skipped (targetOrbit 0).
	return s.finishMechJeb(fc, out, res, targetResolved{}, fmt.Sprintf(
		"MechJeb moon-return burn placed, aiming for a %.0f km periapsis at the parent body. Nothing fired — undo with node_delete/node_clear.", alt/1000))
}

// ================= plan_match_planes =================

func (s *kspServer) planMatchPlanes() (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	tr, ok, err := s.requireTarget(fc, &out)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if !ok {
		return out, nil
	}

	st := s.mechjebState(fc)
	if !st.functional {
		out.Note = "MechJeb picks the cheaper plane-change node for you; it isn't usable here, so use calc_plane_change for the " +
			"delta-v at each node and place it with node_create. " + st.degradeNote
		return out, nil
	}
	if _, ok, err := s.noExistingNodesOrNote(fc, &out); err != nil {
		s.drop()
		return planMJOut{}, err
	} else if !ok {
		return out, nil
	}
	res, err := fc.c.PlanPlane()
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	return s.finishMechJeb(fc, out, res, tr, "MechJeb plane-match node placed at the cheaper relative node. Nothing fired — undo with node_delete/node_clear.")
}

// ================= refine_closest_approach =================

type refineInput struct {
	TargetDistanceM *float64 `json:"target_distance_m,omitempty" jsonschema:"desired closest-approach distance to the target, meters; default 1000"`
}

func (s *kspServer) refineClosestApproach(in refineInput) (planMJOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planMJOut{}, err
	}
	if !ok {
		return planMJOut{base: b}, nil
	}
	out := planMJOut{base: base{Available: true}, MechJebAvailable: fc.c.MechJebAvailable()}

	tr, ok, err := s.requireTarget(fc, &out)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	if !ok {
		return out, nil
	}

	st := s.mechjebState(fc)
	if !st.functional {
		out.Note = "Fine-tuning closest approach needs MechJeb's course-correction op (no native equivalent). " + st.degradeNote
		return out, nil
	}
	dist := 1000.0
	if in.TargetDistanceM != nil {
		dist = *in.TargetDistanceM
	}
	res, err := fc.c.PlanCourseCorrection(dist)
	if err != nil {
		s.drop()
		return planMJOut{}, err
	}
	return s.finishMechJeb(fc, out, res, tr, fmt.Sprintf(
		"MechJeb course-correction node placed to tighten closest approach toward %.0f m. Needs an existing intercept course to "+
			"refine. Nothing fired — undo with node_delete/node_clear.", dist))
}
