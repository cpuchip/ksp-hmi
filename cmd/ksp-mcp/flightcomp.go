package main

// flightcomp.go — the flight-computer tool methods: Tier 1 richer reads
// (target_info, list_vessels, delta_v_status, attitude, bodies), Tier 2 burn math
// (calc_circularize, calc_hohmann, calc_plane_change, calc_burn_time), and Tier 3
// maneuver-node planning (node_create, node_delete, node_clear, plan_circularize,
// plan_hohmann). Reads happen through the krpc client; all orbital math is the
// pure astro package so it is textbook-tested. Tier 3 is the only mutating
// surface and it writes NOTHING but maneuver nodes — no engine, stage, SAS, or
// warp anywhere.

import (
	"fmt"
	"math"
	"sort"

	"github.com/cpuchip/ksp-hmi/astro"
	"github.com/cpuchip/ksp-hmi/krpc"
)

// flightCtx is the active-flight context most tools need: the connection, the
// universal time, and the active vessel's orbit/body/mu plus the body-centered
// non-rotating frame used for exact relative geometry.
type flightCtx struct {
	c      *krpc.Conn
	ut     float64
	vessel uint64
	orbit  uint64
	bodyID uint64
	body   string
	mu     float64
	frame  uint64
}

// flightContext resolves the active vessel and loads the shared context. Returns
// ok=false with a filled base when the game isn't in flight or kRPC is down (a
// graceful answer, not an error); a non-nil error is an unexpected protocol fault.
func (s *kspServer) flightContext() (fc flightCtx, b base, ok bool, err error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil || !ok {
		return flightCtx{}, b, ok, err
	}
	fc = flightCtx{c: c, vessel: vessel}
	if fc.ut, err = c.UT(); err != nil {
		s.drop()
		return fc, base{}, false, err
	}
	if fc.orbit, err = c.VesselOrbitID(vessel); err != nil {
		s.drop()
		return fc, base{}, false, err
	}
	if fc.bodyID, err = c.OrbitBodyID(fc.orbit); err != nil {
		s.drop()
		return fc, base{}, false, err
	}
	if fc.body, err = c.BodyName(fc.bodyID); err != nil {
		s.drop()
		return fc, base{}, false, err
	}
	if fc.mu, err = c.BodyMu(fc.bodyID); err != nil {
		s.drop()
		return fc, base{}, false, err
	}
	if fc.frame, err = c.BodyNonRotatingFrame(fc.bodyID); err != nil {
		s.drop()
		return fc, base{}, false, err
	}
	return fc, base{Available: true}, true, nil
}

// activePosVel reads the active vessel's position and velocity in the active
// body's non-rotating frame (for phase angle, attitude, and closing geometry).
func (fc flightCtx) activePosVel() (pos, vel astro.Vec3, err error) {
	p, err := fc.c.VesselPosition(fc.vessel, fc.frame)
	if err != nil {
		return pos, vel, err
	}
	v, err := fc.c.VesselVelocity(fc.vessel, fc.frame)
	if err != nil {
		return pos, vel, err
	}
	return astro.V(p), astro.V(v), nil
}

// ---- target resolution (shared by target_info / calc_hohmann / plane / plan) ----

type targetResolved struct {
	kind     string // "vessel" | "body" | "none"
	name     string
	orbit    uint64 // the target's own orbit (path around its primary); 0 if none
	bodyID   uint64 // primary body of the target's orbit
	hasState bool
	pos, vel astro.Vec3 // in the active body's non-rotating frame
}

func (s *kspServer) resolveTarget(fc flightCtx) (targetResolved, error) {
	tr := targetResolved{kind: "none"}
	tv, err := fc.c.TargetVessel()
	if err != nil {
		return tr, err
	}
	if tv != 0 {
		tr.kind = "vessel"
		vb, err := fc.c.VesselBrief(tv)
		if err != nil {
			return tr, err
		}
		tr.name = vb.Name
		if tr.orbit, err = fc.c.VesselOrbitID(tv); err != nil {
			return tr, err
		}
		if tr.bodyID, err = fc.c.OrbitBodyID(tr.orbit); err != nil {
			return tr, err
		}
		p, err := fc.c.VesselPosition(tv, fc.frame)
		if err != nil {
			return tr, err
		}
		v, err := fc.c.VesselVelocity(tv, fc.frame)
		if err != nil {
			return tr, err
		}
		tr.pos, tr.vel, tr.hasState = astro.V(p), astro.V(v), true
		return tr, nil
	}
	tb, err := fc.c.TargetBody()
	if err != nil {
		return tr, err
	}
	if tb != 0 {
		tr.kind = "body"
		if tr.name, err = fc.c.BodyName(tb); err != nil {
			return tr, err
		}
		if tr.orbit, err = fc.c.BodyOrbitID(tb); err != nil {
			return tr, err
		}
		if tr.orbit != 0 {
			if tr.bodyID, err = fc.c.OrbitBodyID(tr.orbit); err != nil {
				return tr, err
			}
		}
		p, err := fc.c.BodyPosition(tb, fc.frame)
		if err != nil {
			return tr, err
		}
		v, err := fc.c.BodyVelocity(tb, fc.frame)
		if err != nil {
			return tr, err
		}
		tr.pos, tr.vel, tr.hasState = astro.V(p), astro.V(v), true
		return tr, nil
	}
	return tr, nil
}

// ================= Tier 1: richer reads =================

// ---- target_info ----

type targetInfoOut struct {
	base
	HasTarget            bool    `json:"has_target"`
	Kind                 string  `json:"kind,omitempty"` // vessel | body | none
	Name                 string  `json:"name,omitempty"`
	TargetBody           string  `json:"target_body,omitempty"` // primary the target orbits
	DistanceM            float64 `json:"distance_m"`
	RelativeSpeedMS      float64 `json:"relative_speed_ms"`
	SamePrimaryBody      bool    `json:"same_primary_body"`
	ClosestApproachM     float64 `json:"closest_approach_m,omitempty"`
	TimeToClosestSeconds float64 `json:"time_to_closest_seconds,omitempty"`
	TimeToClosest        string  `json:"time_to_closest,omitempty"`
	PhaseAngleDeg        float64 `json:"phase_angle_deg,omitempty"`
	RelativeInclDeg      float64 `json:"relative_inclination_deg,omitempty"`
	TargetApoapsisM      float64 `json:"target_apoapsis_altitude_m,omitempty"`
	TargetPeriapsisM     float64 `json:"target_periapsis_altitude_m,omitempty"`
	Note                 string  `json:"note,omitempty"`
}

func (s *kspServer) targetInfo() (targetInfoOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return targetInfoOut{}, err
	}
	if !ok {
		return targetInfoOut{base: b}, nil
	}
	tr, err := s.resolveTarget(fc)
	if err != nil {
		s.drop()
		return targetInfoOut{}, err
	}
	out := targetInfoOut{base: base{Available: true}, Kind: tr.kind}
	if tr.kind == "none" {
		out.Note = "No target set. Right-click a vessel or body (or pick one in the map) to set a target, then ask again."
		return out, nil
	}
	out.HasTarget = true
	out.Name = tr.name

	// Relative position/velocity are exact: same non-rotating, body-centered frame
	// for both objects, so the frame's own motion cancels in the difference.
	aPos, aVel, err := fc.activePosVel()
	if err != nil {
		s.drop()
		return targetInfoOut{}, err
	}
	out.DistanceM = round2(tr.pos.Sub(aPos).Norm())
	out.RelativeSpeedMS = round2(tr.vel.Sub(aVel).Norm())

	if tr.bodyID != 0 {
		if name, err := fc.c.BodyName(tr.bodyID); err == nil {
			out.TargetBody = name
		}
	}
	out.SamePrimaryBody = tr.orbit != 0 && tr.bodyID == fc.bodyID

	// Target orbit apo/peri (altitudes) when the target has an orbit.
	if tr.orbit != 0 {
		if oe, err := fc.c.OrbitElements(tr.orbit); err == nil {
			out.TargetApoapsisM = round2(oe.ApoapsisAltitude)
			out.TargetPeriapsisM = round2(oe.PeriapsisAltitude)
		}
	}

	if out.SamePrimaryBody {
		// kRPC's own conic solver for closest approach (exact).
		if dist, tUT, err := fc.c.OrbitClosestApproach(fc.orbit, tr.orbit); err == nil {
			out.ClosestApproachM = round2(dist)
			out.TimeToClosestSeconds = round2(tUT - fc.ut)
			out.TimeToClosest = fmtDuration(tUT - fc.ut)
		}
		if ri, err := fc.c.OrbitRelativeInclinationDeg(fc.orbit, tr.orbit); err == nil {
			out.RelativeInclDeg = round2(ri)
		}
		// Phase angle: signed angle from chaser to target about the orbital
		// angular-momentum vector (positive = target ahead in direction of motion).
		normal := aPos.Cross(aVel)
		out.PhaseAngleDeg = round2(astro.RadToDeg(astro.SignedAngleInPlane(aPos, tr.pos, normal)))
	} else {
		out.Note = "Target orbits a different primary body, so closest-approach, phase angle, and relative inclination don't apply here. Distance and relative speed are still exact."
	}
	return out, nil
}

// ---- list_vessels ----

type vesselListItem struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Situation string  `json:"situation"`
	Body      string  `json:"body"`
	DistanceM float64 `json:"distance_m"`
	Active    bool    `json:"active,omitempty"`
}

type listVesselsOut struct {
	base
	Count   int              `json:"count"`
	Vessels []vesselListItem `json:"vessels,omitempty"`
}

func (s *kspServer) listVessels() (listVesselsOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return listVesselsOut{}, err
	}
	if !ok {
		return listVesselsOut{base: b}, nil
	}
	ids, err := fc.c.Vessels()
	if err != nil {
		s.drop()
		return listVesselsOut{}, err
	}
	aPos, _, err := fc.activePosVel()
	if err != nil {
		s.drop()
		return listVesselsOut{}, err
	}
	out := listVesselsOut{base: base{Available: true}}
	for _, id := range ids {
		vb, err := fc.c.VesselBrief(id)
		if err != nil {
			s.drop()
			return listVesselsOut{}, err
		}
		item := vesselListItem{Name: vb.Name, Type: vb.Type, Situation: vb.Situation, Body: vb.Body, Active: id == fc.vessel}
		// Distance is frame-invariant, so the active body's non-rotating frame is a
		// fine common frame even for a vessel around another body.
		if p, err := fc.c.VesselPosition(id, fc.frame); err == nil {
			item.DistanceM = round2(astro.V(p).Sub(aPos).Norm())
		}
		out.Vessels = append(out.Vessels, item)
	}
	sort.SliceStable(out.Vessels, func(i, j int) bool {
		return out.Vessels[i].DistanceM < out.Vessels[j].DistanceM
	})
	out.Count = len(out.Vessels)
	return out, nil
}

// ---- delta_v_status ----

type deltaVStatusOut struct {
	base
	MassKg                float64 `json:"mass_kg"`
	DryMassKg             float64 `json:"dry_mass_kg"`
	CurrentThrustN        float64 `json:"current_thrust_n"`
	AvailableThrustN      float64 `json:"available_thrust_n"`
	VacuumThrustN         float64 `json:"vacuum_thrust_n"`
	CurrentISP            float64 `json:"current_isp_s"`
	VacuumISP             float64 `json:"vacuum_isp_s"`
	TWRCurrent            float64 `json:"twr_current"`
	TWRFull               float64 `json:"twr_full"`
	StageDeltaVEstimateMS float64 `json:"stage_delta_v_estimate_ms"`
	Body                  string  `json:"body,omitempty"`
	Note                  string  `json:"note,omitempty"`
}

func (s *kspServer) deltaVStatus() (deltaVStatusOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return deltaVStatusOut{}, err
	}
	if !ok {
		return deltaVStatusOut{base: b}, nil
	}
	d, err := fc.c.DeltaVInputs(fc.vessel)
	if err != nil {
		s.drop()
		return deltaVStatusOut{}, err
	}
	g, err := fc.c.BodySurfaceGravity(fc.bodyID)
	if err != nil {
		s.drop()
		return deltaVStatusOut{}, err
	}
	out := deltaVStatusOut{
		base:             base{Available: true},
		MassKg:           round2(d.Mass),
		DryMassKg:        round2(d.DryMass),
		CurrentThrustN:   round2(d.Thrust),
		AvailableThrustN: round2(d.AvailableThrust),
		VacuumThrustN:    round2(d.MaxVacuumThrust),
		CurrentISP:       round2(d.SpecificImpulse),
		VacuumISP:        round2(d.VacuumSpecificImpulse),
		TWRCurrent:       round2(astro.TWR(d.Thrust, d.Mass, g)),
		TWRFull:          round2(astro.TWR(d.AvailableThrust, d.Mass, g)),
		Body:             fc.body,
	}
	// Single-stage delta-v estimate (Tsiolkovsky, whole ship as one stage). This is
	// a floor, honestly labeled — a multi-stage craft has MORE than this.
	out.StageDeltaVEstimateMS = round2(astro.RocketEquationDV(d.VacuumSpecificImpulse, d.Mass, d.DryMass))
	out.Note = fmt.Sprintf("Delta-v is a single-stage estimate (whole ship, vacuum Isp) — multi-stage craft have more. TWR uses %s surface gravity (%.2f m/s^2).", fc.body, g)
	if d.AvailableThrust <= 0 {
		out.Note = "No active engine (available thrust is zero), so TWR and the delta-v estimate are zero. Stage or activate an engine first. " + out.Note
	}
	return out, nil
}

// ---- attitude ----

type attitudeOut struct {
	base
	PitchDeg         float64 `json:"pitch_deg"`
	HeadingDeg       float64 `json:"heading_deg"`
	RollDeg          float64 `json:"roll_deg"`
	OffProgradeDeg   float64 `json:"off_prograde_deg"`
	OffRetrogradeDeg float64 `json:"off_retrograde_deg"`
	OffNormalDeg     float64 `json:"off_normal_deg"`
	OffAntiNormalDeg float64 `json:"off_antinormal_deg"`
	OffRadialOutDeg  float64 `json:"off_radial_out_deg"`
	OffRadialInDeg   float64 `json:"off_radial_in_deg"`
	OffTargetDeg     float64 `json:"off_target_deg,omitempty"`
	HasTarget        bool    `json:"has_target"`
	NearestMarker    string  `json:"nearest_marker,omitempty"`
	NearestOffsetDeg float64 `json:"nearest_offset_deg,omitempty"`
	Note             string  `json:"note,omitempty"`
}

func (s *kspServer) attitude() (attitudeOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return attitudeOut{}, err
	}
	if !ok {
		return attitudeOut{base: b}, nil
	}
	// Pitch/heading/roll from the surface-frame flight reading.
	ft, err := fc.c.FlightTelemetry(fc.vessel)
	if err != nil {
		s.drop()
		return attitudeOut{}, err
	}
	// Facing and orbital vectors in the body non-rotating frame.
	fwdA, err := fc.c.VesselDirection(fc.vessel, fc.frame)
	if err != nil {
		s.drop()
		return attitudeOut{}, err
	}
	fwd := astro.V(fwdA)
	pos, vel, err := fc.activePosVel()
	if err != nil {
		s.drop()
		return attitudeOut{}, err
	}
	prograde := vel.Unit()
	radialOut := pos.Unit()
	normal := pos.Cross(vel).Unit()
	neg := func(v astro.Vec3) astro.Vec3 { return astro.Vec3{X: -v.X, Y: -v.Y, Z: -v.Z} }

	deg := func(target astro.Vec3) float64 { return round2(astro.RadToDeg(astro.AngleBetween(fwd, target))) }
	out := attitudeOut{
		base:             base{Available: true},
		PitchDeg:         round2(ft.PitchDeg),
		HeadingDeg:       round2(ft.HeadingDeg),
		RollDeg:          round2(ft.RollDeg),
		OffProgradeDeg:   deg(prograde),
		OffRetrogradeDeg: deg(neg(prograde)),
		OffNormalDeg:     deg(normal),
		OffAntiNormalDeg: deg(neg(normal)),
		OffRadialOutDeg:  deg(radialOut),
		OffRadialInDeg:   deg(neg(radialOut)),
	}

	// Nearest navball marker to where the nose points — the one-line "you're X off Y".
	// Radial is always well-defined; prograde/normal need motion, so only offer them
	// as candidates when the vessel is actually moving (else "0 off prograde" would
	// be a spurious reading from a zero velocity vector).
	markers := []struct {
		name string
		off  float64
	}{
		{"radial-out", out.OffRadialOutDeg}, {"radial-in", out.OffRadialInDeg},
	}
	moving := vel.Norm() >= 0.1
	if moving {
		markers = append(markers,
			struct {
				name string
				off  float64
			}{"prograde", out.OffProgradeDeg},
			struct {
				name string
				off  float64
			}{"retrograde", out.OffRetrogradeDeg},
			struct {
				name string
				off  float64
			}{"normal", out.OffNormalDeg},
			struct {
				name string
				off  float64
			}{"anti-normal", out.OffAntiNormalDeg},
		)
	}

	tr, err := s.resolveTarget(fc)
	if err != nil {
		s.drop()
		return attitudeOut{}, err
	}
	if tr.hasState {
		toTarget := tr.pos.Sub(pos).Unit()
		out.HasTarget = true
		out.OffTargetDeg = deg(toTarget)
		markers = append(markers, struct {
			name string
			off  float64
		}{"target", out.OffTargetDeg})
	}
	best := markers[0]
	for _, m := range markers[1:] {
		if m.off < best.off {
			best = m
		}
	}
	out.NearestMarker = best.name
	out.NearestOffsetDeg = best.off
	out.Note = fmt.Sprintf("Nose is %.0f degrees off %s (closest navball marker).", best.off, best.name)
	if !moving {
		out.Note += " Moving too slowly for a prograde/normal reference — those offsets are omitted from the nearest-marker pick."
	}
	return out, nil
}

// ---- bodies ----

type bodyInput struct {
	Name string `json:"name" jsonschema:"the celestial body to describe, e.g. Kerbin, Mun, Minmus, Duna. Leave empty to list every body's name."`
}

type bodiesOut struct {
	base
	Names              []string `json:"names,omitempty"` // when no name is given
	Name               string   `json:"name,omitempty"`
	RadiusM            float64  `json:"equatorial_radius_m,omitempty"`
	SurfaceGravityMS2  float64  `json:"surface_gravity_ms2,omitempty"`
	SphereOfInfluenceM float64  `json:"sphere_of_influence_m,omitempty"`
	RotationalPeriodS  float64  `json:"rotational_period_s,omitempty"`
	RotationalPeriod   string   `json:"rotational_period,omitempty"`
	MuM3S2             float64  `json:"gravitational_parameter_m3s2,omitempty"`
	HasAtmosphere      bool     `json:"has_atmosphere"`
	AtmosphereDepthM   float64  `json:"atmosphere_depth_m,omitempty"`
	Note               string   `json:"note,omitempty"`
}

func (s *kspServer) bodies(name string) (bodiesOut, error) {
	c, err := s.conn()
	if err != nil {
		return bodiesOut{base: base{Available: false, Message: s.connectMsg(err)}}, nil
	}
	bmap, err := c.Bodies()
	if err != nil {
		s.drop()
		return bodiesOut{}, err
	}
	if name == "" {
		out := bodiesOut{base: base{Available: true}}
		for n := range bmap {
			out.Names = append(out.Names, n)
		}
		sort.Strings(out.Names)
		out.Note = "Name a body to get its radius, gravity, SOI, day length, and atmosphere."
		return out, nil
	}
	// Case-insensitive lookup.
	id, found := bmap[name]
	if !found {
		for n, bid := range bmap {
			if equalFold(n, name) {
				id, found = bid, true
				break
			}
		}
	}
	if !found {
		names := make([]string, 0, len(bmap))
		for n := range bmap {
			names = append(names, n)
		}
		sort.Strings(names)
		return bodiesOut{base: base{Available: true}, Note: fmt.Sprintf("No body named %q. Known bodies: %v", name, names)}, nil
	}
	f, err := c.BodyFacts(id)
	if err != nil {
		s.drop()
		return bodiesOut{}, err
	}
	out := bodiesOut{
		base:              base{Available: true},
		Name:              f.Name,
		RadiusM:           round2(f.EquatorialRadiusM),
		SurfaceGravityMS2: round2(f.SurfaceGravityMS2),
		RotationalPeriodS: round2(f.RotationalPeriodS),
		RotationalPeriod:  fmtDuration(f.RotationalPeriodS),
		MuM3S2:            f.GravitationalParamMu,
		HasAtmosphere:     f.HasAtmosphere,
	}
	if math.IsInf(f.SphereOfInfluenceM, 1) {
		out.Note = "Sphere of influence is unbounded (this is the central star)."
	} else {
		out.SphereOfInfluenceM = round2(f.SphereOfInfluenceM)
	}
	if f.HasAtmosphere {
		out.AtmosphereDepthM = round2(f.AtmosphereDepthM)
	}
	return out, nil
}

// equalFold is a tiny ASCII case-insensitive compare (body names are ASCII), kept
// local to avoid importing strings just for this.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
