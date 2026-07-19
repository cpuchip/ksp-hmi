package main

// plan.go — Tier 2 burn math (read current state, compute with astro, return; no
// game write) and Tier 3 maneuver-node planning (the ONLY writes, all reversible,
// nodes only — never an engine/stage/SAS/warp). Each Tier 3 output echoes the
// resulting predicted orbit so the CAPCOM can read it back.

import (
	"fmt"
	"math"

	"github.com/cpuchip/ksp-hmi/astro"
)

// burnEstimate returns a rounded burn duration + spoken form for a delta-v using
// the active vessel's current mass/thrust/Isp, or a note when no engine is live.
func (fc flightCtx) burnEstimate(dv float64) (secs float64, spoken, note string) {
	d, err := fc.c.DeltaVInputs(fc.vessel)
	if err != nil {
		return 0, "", "burn estimate unavailable: " + err.Error()
	}
	t, _, ok := astro.BurnTime(d.Mass, d.AvailableThrust, d.SpecificImpulse, dv)
	if !ok {
		return 0, "", "no burn estimate (no active engine / thrust)"
	}
	return round2(t), fmtDuration(t), ""
}

// ================= Tier 2: burn math =================

// ---- calc_circularize ----

type calcCircularizeOut struct {
	base
	Body                   string  `json:"body,omitempty"`
	ApoapsisAltitudeM      float64 `json:"apoapsis_altitude_m"`
	PeriapsisAltitudeM     float64 `json:"periapsis_altitude_m"`
	DVAtApoapsisMS         float64 `json:"dv_at_apoapsis_ms"`
	DVAtPeriapsisMS        float64 `json:"dv_at_periapsis_ms"`
	BurnAtApoapsisSeconds  float64 `json:"burn_at_apoapsis_seconds,omitempty"`
	BurnAtApoapsis         string  `json:"burn_at_apoapsis,omitempty"`
	BurnAtPeriapsisSeconds float64 `json:"burn_at_periapsis_seconds,omitempty"`
	BurnAtPeriapsis        string  `json:"burn_at_periapsis,omitempty"`
	TimeToApoapsis         string  `json:"time_to_apoapsis,omitempty"`
	TimeToPeriapsis        string  `json:"time_to_periapsis,omitempty"`
	Note                   string  `json:"note,omitempty"`
}

func (s *kspServer) calcCircularize() (calcCircularizeOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return calcCircularizeOut{}, err
	}
	if !ok {
		return calcCircularizeOut{base: b}, nil
	}
	rApo, err := fc.c.OrbitApoapsisRadius(fc.orbit)
	if err != nil {
		s.drop()
		return calcCircularizeOut{}, err
	}
	rPer, err := fc.c.OrbitPeriapsisRadius(fc.orbit)
	if err != nil {
		s.drop()
		return calcCircularizeOut{}, err
	}
	oe, err := fc.c.OrbitElements(fc.orbit)
	if err != nil {
		s.drop()
		return calcCircularizeOut{}, err
	}
	out := calcCircularizeOut{
		base:               base{Available: true},
		Body:               fc.body,
		ApoapsisAltitudeM:  round2(oe.ApoapsisAltitude),
		PeriapsisAltitudeM: round2(oe.PeriapsisAltitude),
		TimeToApoapsis:     fmtDuration(oe.TimeToApoapsis),
		TimeToPeriapsis:    fmtDuration(oe.TimeToPeriapsis),
	}
	if rApo <= 0 || rPer <= 0 || rPer > rApo {
		out.Note = "This isn't a usable closed orbit (landed, sub-orbital, or escaping), so circularization doesn't apply. Get into orbit first."
		return out, nil
	}
	cz := astro.Circularize(fc.mu, rApo, rPer)
	out.DVAtApoapsisMS = round2(cz.AtApoapsisDV)
	out.DVAtPeriapsisMS = round2(cz.AtPeriapsisDV)
	out.BurnAtApoapsisSeconds, out.BurnAtApoapsis, _ = fc.burnEstimate(cz.AtApoapsisDV)
	out.BurnAtPeriapsisSeconds, out.BurnAtPeriapsis, _ = fc.burnEstimate(cz.AtPeriapsisDV)
	out.Note = "Positive dv is a prograde burn (at apoapsis, raises periapsis); negative is retrograde (at periapsis, lowers apoapsis)."
	return out, nil
}

// ---- calc_hohmann ----

// hohmannComputed is the shared result used by calc_hohmann and plan_hohmann.
type hohmannComputed struct {
	r1, r2          float64
	h               astro.HohmannResult
	hasTargetObject bool
	targetName      string
	samePrimary     bool
	currentPhaseRad float64
	canTime         bool
	timeToDeparture float64 // seconds until the departure burn (canTime only)
	departureUT     float64
	note            string
}

// computeHohmann reads the current state and computes a transfer either to a
// requested target ALTITUDE (meters above the current body's sea level) or, when
// altitude is nil, to the current in-game target. Timing (phase angle, time to
// burn) is only possible against a real target object sharing the primary body.
func (s *kspServer) computeHohmann(fc flightCtx, targetAltitude *float64) (hohmannComputed, error) {
	var hc hohmannComputed
	r1, err := fc.c.OrbitSemiMajorAxis(fc.orbit)
	if err != nil {
		return hc, err
	}
	hc.r1 = r1

	if targetAltitude != nil {
		radius, err := fc.c.BodyEquatorialRadius(fc.bodyID)
		if err != nil {
			return hc, err
		}
		hc.r2 = *targetAltitude + radius
		hc.note = fmt.Sprintf("Transfer to a %.0f km circular orbit around %s. No target object, so I can't time the burn — this is the delta-v and geometry only.", *targetAltitude/1000, fc.body)
		hc.h = astro.Hohmann(fc.mu, hc.r1, hc.r2)
		return hc, nil
	}

	tr, err := s.resolveTarget(fc)
	if err != nil {
		return hc, err
	}
	if tr.kind == "none" {
		hc.note = "No target set and no altitude given. Set a target (or pass a target altitude) so I can compute the transfer."
		return hc, nil
	}
	hc.hasTargetObject = true
	hc.targetName = tr.name
	if tr.orbit == 0 {
		hc.note = "The target has no orbit to transfer to."
		return hc, nil
	}
	hc.samePrimary = tr.bodyID == fc.bodyID
	if !hc.samePrimary {
		hc.note = fmt.Sprintf("%s orbits a different body than you, so this simple same-primary Hohmann doesn't apply — that's an interplanetary transfer.", tr.name)
		return hc, nil
	}
	hc.r2, err = fc.c.OrbitSemiMajorAxis(tr.orbit)
	if err != nil {
		return hc, err
	}
	hc.h = astro.Hohmann(fc.mu, hc.r1, hc.r2)

	// Timing: current phase angle (target ahead positive), then synodic wait.
	aPos, aVel, err := fc.activePosVel()
	if err != nil {
		return hc, err
	}
	normal := aPos.Cross(aVel)
	hc.currentPhaseRad = astro.SignedAngleInPlane(aPos, tr.pos, normal)
	nCh := astro.MeanMotion(fc.mu, hc.r1)
	nT := astro.MeanMotion(fc.mu, hc.r2)
	wait := astro.SynodicWait(hc.currentPhaseRad, hc.h.PhaseAngleRad, nCh, nT)
	if !math.IsInf(wait, 0) {
		hc.canTime = true
		hc.timeToDeparture = wait
		hc.departureUT = fc.ut + wait
	}
	return hc, nil
}

type calcHohmannOut struct {
	base
	TargetName          string  `json:"target_name,omitempty"`
	DepartureDVMS       float64 `json:"departure_dv_ms"`
	ArrivalDVMS         float64 `json:"arrival_dv_ms"`
	TotalDVMS           float64 `json:"total_dv_ms"`
	TransferTimeSeconds float64 `json:"transfer_time_seconds"`
	TransferTime        string  `json:"transfer_time,omitempty"`
	RequiredPhaseDeg    float64 `json:"required_phase_angle_deg"`
	CurrentPhaseDeg     float64 `json:"current_phase_angle_deg,omitempty"`
	TimeToBurnSeconds   float64 `json:"time_to_departure_burn_seconds,omitempty"`
	TimeToBurn          string  `json:"time_to_departure_burn,omitempty"`
	DepartureBurnSecs   float64 `json:"departure_burn_estimate_seconds,omitempty"`
	DepartureBurn       string  `json:"departure_burn_estimate,omitempty"`
	Note                string  `json:"note,omitempty"`
}

type hohmannInput struct {
	TargetAltitudeM *float64 `json:"target_altitude_m,omitempty" jsonschema:"optional target circular-orbit altitude in meters above sea level; omit to transfer to the current in-game target"`
}

func (s *kspServer) calcHohmann(in hohmannInput) (calcHohmannOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return calcHohmannOut{}, err
	}
	if !ok {
		return calcHohmannOut{base: b}, nil
	}
	hc, err := s.computeHohmann(fc, in.TargetAltitudeM)
	if err != nil {
		s.drop()
		return calcHohmannOut{}, err
	}
	out := calcHohmannOut{base: base{Available: true}, TargetName: hc.targetName, Note: hc.note}
	if hc.r2 == 0 { // couldn't compute (see note)
		return out, nil
	}
	out.DepartureDVMS = round2(hc.h.DepartureDV)
	out.ArrivalDVMS = round2(hc.h.ArrivalDV)
	out.TotalDVMS = round2(hc.h.TotalDV)
	out.TransferTimeSeconds = round2(hc.h.TransferTime)
	out.TransferTime = fmtDuration(hc.h.TransferTime)
	out.RequiredPhaseDeg = round2(astro.RadToDeg(hc.h.PhaseAngleRad))
	if hc.hasTargetObject && hc.samePrimary {
		out.CurrentPhaseDeg = round2(astro.RadToDeg(hc.currentPhaseRad))
	}
	if hc.canTime {
		out.TimeToBurnSeconds = round2(hc.timeToDeparture)
		out.TimeToBurn = fmtDuration(hc.timeToDeparture)
	}
	out.DepartureBurnSecs, out.DepartureBurn, _ = fc.burnEstimate(hc.h.DepartureDV)
	return out, nil
}

// ---- calc_plane_change ----

type calcPlaneChangeOut struct {
	base
	TargetName      string  `json:"target_name,omitempty"`
	RelativeInclDeg float64 `json:"relative_inclination_deg"`
	DVAtApoapsisMS  float64 `json:"dv_at_apoapsis_ms"`
	DVAtPeriapsisMS float64 `json:"dv_at_periapsis_ms"`
	CheapestNode    string  `json:"cheapest_node,omitempty"`
	CheapestDVMS    float64 `json:"cheapest_dv_ms,omitempty"`
	Note            string  `json:"note,omitempty"`
}

func (s *kspServer) calcPlaneChange() (calcPlaneChangeOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return calcPlaneChangeOut{}, err
	}
	if !ok {
		return calcPlaneChangeOut{base: b}, nil
	}
	tr, err := s.resolveTarget(fc)
	if err != nil {
		s.drop()
		return calcPlaneChangeOut{}, err
	}
	out := calcPlaneChangeOut{base: base{Available: true}, TargetName: tr.name}
	if tr.kind == "none" || tr.orbit == 0 {
		out.Note = "Set a target with an orbit first — a plane change is measured against the target's orbital plane."
		return out, nil
	}
	if tr.bodyID != fc.bodyID {
		out.Note = fmt.Sprintf("%s orbits a different body, so a same-orbit plane change doesn't apply.", tr.name)
		return out, nil
	}
	relIncDeg, err := fc.c.OrbitRelativeInclinationDeg(fc.orbit, tr.orbit)
	if err != nil {
		s.drop()
		return calcPlaneChangeOut{}, err
	}
	out.RelativeInclDeg = round2(relIncDeg)

	rApo, err := fc.c.OrbitApoapsisRadius(fc.orbit)
	if err != nil {
		s.drop()
		return calcPlaneChangeOut{}, err
	}
	rPer, err := fc.c.OrbitPeriapsisRadius(fc.orbit)
	if err != nil {
		s.drop()
		return calcPlaneChangeOut{}, err
	}
	sma := (rApo + rPer) / 2
	vApo := astro.VisViva(fc.mu, rApo, sma) // slowest -> cheapest plane change
	vPer := astro.VisViva(fc.mu, rPer, sma)
	relIncRad := astro.DegToRad(relIncDeg)
	out.DVAtApoapsisMS = round2(astro.PlaneChangeDV(vApo, relIncRad))
	out.DVAtPeriapsisMS = round2(astro.PlaneChangeDV(vPer, relIncRad))
	out.CheapestNode = "apoapsis"
	out.CheapestDVMS = out.DVAtApoapsisMS
	out.Note = "A pure plane change is cheapest where you're slowest — at apoapsis. Do it at the ascending or descending node nearest apoapsis."
	return out, nil
}

// ---- calc_burn_time ----

type burnTimeInput struct {
	DeltaVMS float64 `json:"delta_v_ms" jsonschema:"the delta-v of the planned burn, in meters per second"`
}

type calcBurnTimeOut struct {
	base
	DeltaVMS     float64 `json:"delta_v_ms"`
	BurnSeconds  float64 `json:"burn_seconds"`
	Burn         string  `json:"burn,omitempty"`
	LeadSeconds  float64 `json:"start_before_node_seconds"`
	Lead         string  `json:"start_before_node,omitempty"`
	AvailThrustN float64 `json:"available_thrust_n"`
	ISP          float64 `json:"isp_s"`
	Note         string  `json:"note,omitempty"`
}

func (s *kspServer) calcBurnTime(in burnTimeInput) (calcBurnTimeOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return calcBurnTimeOut{}, err
	}
	if !ok {
		return calcBurnTimeOut{base: b}, nil
	}
	d, err := fc.c.DeltaVInputs(fc.vessel)
	if err != nil {
		s.drop()
		return calcBurnTimeOut{}, err
	}
	out := calcBurnTimeOut{
		base:         base{Available: true},
		DeltaVMS:     round2(in.DeltaVMS),
		AvailThrustN: round2(d.AvailableThrust),
		ISP:          round2(d.SpecificImpulse),
	}
	t, lead, okc := astro.BurnTime(d.Mass, d.AvailableThrust, d.SpecificImpulse, in.DeltaVMS)
	if !okc {
		out.Note = "No active engine (available thrust is zero) — stage or light an engine, then ask again."
		return out, nil
	}
	out.BurnSeconds = round2(t)
	out.Burn = fmtDuration(t)
	out.LeadSeconds = round2(lead)
	out.Lead = fmtDuration(lead)
	out.Note = "Start the burn half its length before the node (the lead figure) to center the impulse on the maneuver."
	return out, nil
}

// ================= Tier 3: maneuver-node planning (writes, reversible) =================

// nodeResultOut is the readback shared by every node-creating tool: the created
// node's burn + the predicted orbit it produces.
type nodeResultOut struct {
	DeltaVMS          float64 `json:"delta_v_ms"`
	ProgradeMS        float64 `json:"prograde_ms"`
	NormalMS          float64 `json:"normal_ms"`
	RadialMS          float64 `json:"radial_ms"`
	TimeToNodeSeconds float64 `json:"time_to_node_seconds"`
	TimeToNode        string  `json:"time_to_node,omitempty"`
	ResultApoapsisM   float64 `json:"result_apoapsis_altitude_m"`
	ResultPeriapsisM  float64 `json:"result_periapsis_altitude_m"`
	BurnEstimateSecs  float64 `json:"burn_estimate_seconds,omitempty"`
	BurnEstimate      string  `json:"burn_estimate,omitempty"`
}

// readNodeResult reads a freshly-created node and its predicted orbit.
func (s *kspServer) readNodeResult(fc flightCtx, node uint64) (nodeResultOut, error) {
	nd, err := fc.c.NodeDetail(node)
	if err != nil {
		return nodeResultOut{}, err
	}
	out := nodeResultOut{
		DeltaVMS:          round2(nd.DeltaV),
		ProgradeMS:        round2(nd.Prograde),
		NormalMS:          round2(nd.Normal),
		RadialMS:          round2(nd.Radial),
		TimeToNodeSeconds: round2(nd.TimeTo),
		TimeToNode:        fmtDuration(nd.TimeTo),
	}
	if nd.OrbitID != 0 {
		if oe, err := fc.c.OrbitElements(nd.OrbitID); err == nil {
			out.ResultApoapsisM = round2(oe.ApoapsisAltitude)
			out.ResultPeriapsisM = round2(oe.PeriapsisAltitude)
		}
	}
	out.BurnEstimateSecs, out.BurnEstimate, _ = fc.burnEstimate(nd.DeltaV)
	return out, nil
}

// ---- node_create ----

type nodeCreateInput struct {
	TimeFromNowSeconds *float64 `json:"time_from_now_seconds,omitempty" jsonschema:"when to place the node, in seconds from now; provide this OR ut_seconds"`
	UTSeconds          *float64 `json:"ut_seconds,omitempty" jsonschema:"absolute universal time for the node; provide this OR time_from_now_seconds"`
	ProgradeMS         float64  `json:"prograde_ms" jsonschema:"prograde burn component in m/s (negative = retrograde)"`
	NormalMS           float64  `json:"normal_ms" jsonschema:"normal burn component in m/s (negative = anti-normal)"`
	RadialMS           float64  `json:"radial_ms" jsonschema:"radial-out burn component in m/s (negative = radial-in)"`
}

type nodeCreateOut struct {
	base
	Created bool          `json:"created"`
	Node    nodeResultOut `json:"node"`
	Note    string        `json:"note,omitempty"`
}

func (s *kspServer) nodeCreate(in nodeCreateInput) (nodeCreateOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return nodeCreateOut{}, err
	}
	if !ok {
		return nodeCreateOut{base: b}, nil
	}
	var ut float64
	switch {
	case in.UTSeconds != nil:
		ut = *in.UTSeconds
	case in.TimeFromNowSeconds != nil:
		ut = fc.ut + *in.TimeFromNowSeconds
	default:
		return nodeCreateOut{base: base{Available: true}, Note: "Give either time_from_now_seconds or ut_seconds for the node."}, nil
	}
	control, err := fc.c.VesselControl(fc.vessel)
	if err != nil {
		s.drop()
		return nodeCreateOut{}, err
	}
	node, err := fc.c.AddNode(control, ut, in.ProgradeMS, in.NormalMS, in.RadialMS)
	if err != nil {
		s.drop()
		return nodeCreateOut{}, err
	}
	res, err := s.readNodeResult(fc, node)
	if err != nil {
		s.drop()
		return nodeCreateOut{}, err
	}
	return nodeCreateOut{base: base{Available: true}, Created: true, Node: res,
		Note: "Node added to the flight plan. Nothing has fired — this only draws the maneuver on the navball. Remove it with node_delete or node_clear."}, nil
}

// ---- node_delete ----

type nodeDeleteInput struct {
	Index *int `json:"index,omitempty" jsonschema:"which node to delete (0 = the first/next node); omit to delete the last node"`
}

type nodeDeleteOut struct {
	base
	Deleted        bool   `json:"deleted"`
	RemainingNodes int    `json:"remaining_nodes"`
	Note           string `json:"note,omitempty"`
}

func (s *kspServer) nodeDelete(in nodeDeleteInput) (nodeDeleteOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return nodeDeleteOut{}, err
	}
	if !ok {
		return nodeDeleteOut{base: b}, nil
	}
	control, err := fc.c.VesselControl(fc.vessel)
	if err != nil {
		s.drop()
		return nodeDeleteOut{}, err
	}
	nodes, err := fc.c.ControlNodes(control)
	if err != nil {
		s.drop()
		return nodeDeleteOut{}, err
	}
	if len(nodes) == 0 {
		return nodeDeleteOut{base: base{Available: true}, Note: "No maneuver nodes to delete."}, nil
	}
	idx := len(nodes) - 1
	if in.Index != nil {
		idx = *in.Index
	}
	if idx < 0 || idx >= len(nodes) {
		return nodeDeleteOut{base: base{Available: true}, RemainingNodes: len(nodes),
			Note: fmt.Sprintf("No node at index %d — there are %d node(s), indices 0..%d.", idx, len(nodes), len(nodes)-1)}, nil
	}
	if err := fc.c.NodeRemove(nodes[idx]); err != nil {
		s.drop()
		return nodeDeleteOut{}, err
	}
	return nodeDeleteOut{base: base{Available: true}, Deleted: true, RemainingNodes: len(nodes) - 1,
		Note: "Node removed from the flight plan."}, nil
}

// ---- node_clear ----

type nodeClearOut struct {
	base
	Cleared bool   `json:"cleared"`
	Removed int    `json:"removed"`
	Note    string `json:"note,omitempty"`
}

func (s *kspServer) nodeClear() (nodeClearOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return nodeClearOut{}, err
	}
	if !ok {
		return nodeClearOut{base: b}, nil
	}
	control, err := fc.c.VesselControl(fc.vessel)
	if err != nil {
		s.drop()
		return nodeClearOut{}, err
	}
	nodes, err := fc.c.ControlNodes(control)
	if err != nil {
		s.drop()
		return nodeClearOut{}, err
	}
	n := len(nodes)
	if n == 0 {
		return nodeClearOut{base: base{Available: true}, Note: "No maneuver nodes to clear."}, nil
	}
	if err := fc.c.RemoveAllNodes(control); err != nil {
		s.drop()
		return nodeClearOut{}, err
	}
	return nodeClearOut{base: base{Available: true}, Cleared: true, Removed: n,
		Note: fmt.Sprintf("Cleared all %d maneuver node(s) from the flight plan.", n)}, nil
}

// ---- plan_circularize ----

type planInput struct {
	At string `json:"at,omitempty" jsonschema:"where to circularize: apoapsis (default) or periapsis"`
}

type planCircularizeOut struct {
	base
	At     string        `json:"at"`
	DVMS   float64       `json:"dv_ms"`
	Node   nodeResultOut `json:"node"`
	Placed bool          `json:"placed"`
	Note   string        `json:"note,omitempty"`
}

func (s *kspServer) planCircularize(in planInput) (planCircularizeOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planCircularizeOut{}, err
	}
	if !ok {
		return planCircularizeOut{base: b}, nil
	}
	at := "apoapsis"
	if equalFold(in.At, "periapsis") || equalFold(in.At, "peri") {
		at = "periapsis"
	}
	rApo, err := fc.c.OrbitApoapsisRadius(fc.orbit)
	if err != nil {
		s.drop()
		return planCircularizeOut{}, err
	}
	rPer, err := fc.c.OrbitPeriapsisRadius(fc.orbit)
	if err != nil {
		s.drop()
		return planCircularizeOut{}, err
	}
	if rApo <= 0 || rPer <= 0 || rPer > rApo {
		return planCircularizeOut{base: base{Available: true}, At: at,
			Note: "Not a usable closed orbit (landed/sub-orbital/escaping) — can't place a circularization node. Get into orbit first."}, nil
	}
	oe, err := fc.c.OrbitElements(fc.orbit)
	if err != nil {
		s.drop()
		return planCircularizeOut{}, err
	}
	cz := astro.Circularize(fc.mu, rApo, rPer)
	var dv, timeToApsis float64
	if at == "apoapsis" {
		dv, timeToApsis = cz.AtApoapsisDV, oe.TimeToApoapsis
	} else {
		dv, timeToApsis = cz.AtPeriapsisDV, oe.TimeToPeriapsis
	}
	control, err := fc.c.VesselControl(fc.vessel)
	if err != nil {
		s.drop()
		return planCircularizeOut{}, err
	}
	node, err := fc.c.AddNode(control, fc.ut+timeToApsis, dv, 0, 0)
	if err != nil {
		s.drop()
		return planCircularizeOut{}, err
	}
	res, err := s.readNodeResult(fc, node)
	if err != nil {
		s.drop()
		return planCircularizeOut{}, err
	}
	return planCircularizeOut{
		base: base{Available: true}, At: at, DVMS: round2(dv), Node: res, Placed: true,
		Note: "Circularization node placed on the flight plan (nothing fired). Remove it with node_delete/node_clear.",
	}, nil
}

// ---- plan_hohmann ----

type planHohmannOut struct {
	base
	TargetName        string        `json:"target_name,omitempty"`
	DepartureDVMS     float64       `json:"departure_dv_ms"`
	TimeToBurnSeconds float64       `json:"time_to_departure_burn_seconds,omitempty"`
	TimeToBurn        string        `json:"time_to_departure_burn,omitempty"`
	Node              nodeResultOut `json:"node"`
	Placed            bool          `json:"placed"`
	Note              string        `json:"note,omitempty"`
}

func (s *kspServer) planHohmann(in hohmannInput) (planHohmannOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return planHohmannOut{}, err
	}
	if !ok {
		return planHohmannOut{base: b}, nil
	}
	hc, err := s.computeHohmann(fc, in.TargetAltitudeM)
	if err != nil {
		s.drop()
		return planHohmannOut{}, err
	}
	out := planHohmannOut{base: base{Available: true}, TargetName: hc.targetName}
	if !hc.canTime {
		out.Note = "Can't time a departure burn without a target object in the same orbit — set a target vessel/moon and try again. (Use calc_hohmann for the delta-v of a bare altitude change.) " + hc.note
		return out, nil
	}
	control, err := fc.c.VesselControl(fc.vessel)
	if err != nil {
		s.drop()
		return planHohmannOut{}, err
	}
	node, err := fc.c.AddNode(control, hc.departureUT, hc.h.DepartureDV, 0, 0)
	if err != nil {
		s.drop()
		return planHohmannOut{}, err
	}
	res, err := s.readNodeResult(fc, node)
	if err != nil {
		s.drop()
		return planHohmannOut{}, err
	}
	out.DepartureDVMS = round2(hc.h.DepartureDV)
	out.TimeToBurnSeconds = round2(hc.timeToDeparture)
	out.TimeToBurn = fmtDuration(hc.timeToDeparture)
	out.Node = res
	out.Placed = true
	out.Note = "Departure node placed at the next transfer window (nothing fired). This raises/lowers your orbit to intercept; you'll still want an arrival/capture burn. Remove with node_delete/node_clear."
	return out, nil
}
