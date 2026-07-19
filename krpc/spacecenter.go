package krpc

// spacecenter.go — typed helpers over the SpaceCenter service for exactly what
// the read-only ksp-mcp tools need. Each returns a plain Go struct with units
// named in the field comments; the MCP layer formats them for the CAPCOM.
//
// These are deliberately NOT a full generated SpaceCenter client — just the
// read paths the copilot's status/orbit/telemetry/resources/nodes/crew tools
// consume. Every value flows through the dynamic Call layer, so the future
// command wave extends this file with mutating calls with no structural change.

import (
	"errors"
	"math"
)

// g0 is standard gravity, used to turn specific impulse (seconds) into exhaust
// velocity (m/s) for the burn-time estimate.
const g0 = 9.80665

// ErrNoVessel means there is no active vessel — the game is not in flight (e.g.
// the Space Center, Tracking Station, or editor scene). Tools surface this as a
// clear, non-error "nothing to report yet" rather than a failure.
var ErrNoVessel = errors.New("krpc: no active vessel (not in flight)")

// ActiveVessel returns the active vessel's object id, or ErrNoVessel if there is
// none. kRPC throws when not in flight; that is mapped to ErrNoVessel too.
func (c *Conn) ActiveVessel() (uint64, error) {
	id, err := c.callObject("SpaceCenter", "get_ActiveVessel")
	if err != nil {
		// Not-in-flight surfaces as a procedure error; treat as "no vessel".
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			return 0, ErrNoVessel
		}
		return 0, err
	}
	if id == 0 {
		return 0, ErrNoVessel
	}
	return id, nil
}

// UT returns the universal time (seconds since the game's epoch).
func (c *Conn) UT() (float64, error) {
	return c.callScalar("SpaceCenter", "get_UT")
}

// VesselStatus is the answer to "what/where am I": name, situation, body, and
// mission elapsed time.
type VesselStatus struct {
	Name          string  // vessel name
	Situation     string  // e.g. Orbiting, SubOrbital, Flying, Landed, Splashed, PreLaunch, Docked, Escaping
	SituationCode int32   // raw VesselSituation enum value
	Body          string  // celestial body currently orbited (e.g. Kerbin, Mun)
	METSeconds    float64 // mission elapsed time, seconds
}

// VesselStatus reads the active vessel's status.
func (c *Conn) VesselStatus(vessel uint64) (*VesselStatus, error) {
	vs := &VesselStatus{}
	var err error
	if vs.Name, err = c.callString("SpaceCenter", "Vessel_get_Name", EncodeObject(vessel)); err != nil {
		return nil, err
	}
	if vs.SituationCode, vs.Situation, err = c.callEnum("SpaceCenter", "Vessel_get_Situation", "SpaceCenter.VesselSituation", EncodeObject(vessel)); err != nil {
		return nil, err
	}
	if vs.METSeconds, err = c.callScalar("SpaceCenter", "Vessel_get_MET", EncodeObject(vessel)); err != nil {
		return nil, err
	}
	if vs.Body, err = c.vesselBodyName(vessel); err != nil {
		return nil, err
	}
	return vs, nil
}

// vesselBodyName resolves the name of the body the vessel currently orbits.
func (c *Conn) vesselBodyName(vessel uint64) (string, error) {
	orbit, err := c.callObject("SpaceCenter", "Vessel_get_Orbit", EncodeObject(vessel))
	if err != nil {
		return "", err
	}
	body, err := c.callObject("SpaceCenter", "Orbit_get_Body", EncodeObject(orbit))
	if err != nil {
		return "", err
	}
	return c.callString("SpaceCenter", "CelestialBody_get_Name", EncodeObject(body))
}

// OrbitInfo describes the current orbit. Altitudes are above the body's sea
// level (ApoapsisAltitude/PeriapsisAltitude), distinct from apoapsis/periapsis
// measured from the body center.
type OrbitInfo struct {
	Body               string  // body being orbited
	ApoapsisAltitude   float64 // meters above sea level
	PeriapsisAltitude  float64 // meters above sea level
	Eccentricity       float64 // 0 = circular, <1 = elliptical, >=1 = escape
	InclinationDeg     float64 // degrees (converted from kRPC radians)
	PeriodSeconds      float64 // orbital period, seconds
	TimeToApoapsis     float64 // seconds until apoapsis
	TimeToPeriapsis    float64 // seconds until periapsis
	SemiMajorAxisMeter float64 // meters
}

// Orbit reads the active vessel's orbital elements.
func (c *Conn) Orbit(vessel uint64) (*OrbitInfo, error) {
	orbit, err := c.callObject("SpaceCenter", "Vessel_get_Orbit", EncodeObject(vessel))
	if err != nil {
		return nil, err
	}
	return c.OrbitElements(orbit)
}

// OrbitElements reads the orbital elements of any Orbit object (the active
// vessel's, a target's, or a body's), so the target/transfer tools can read a
// second orbit without going through a vessel. Angles are converted to degrees;
// altitudes are above the body's sea level.
func (c *Conn) OrbitElements(orbit uint64) (*OrbitInfo, error) {
	oi := &OrbitInfo{}
	var err error
	get := func(proc string) (float64, error) {
		return c.callScalar("SpaceCenter", proc, EncodeObject(orbit))
	}
	if oi.ApoapsisAltitude, err = get("Orbit_get_ApoapsisAltitude"); err != nil {
		return nil, err
	}
	if oi.PeriapsisAltitude, err = get("Orbit_get_PeriapsisAltitude"); err != nil {
		return nil, err
	}
	if oi.Eccentricity, err = get("Orbit_get_Eccentricity"); err != nil {
		return nil, err
	}
	incRad, err := get("Orbit_get_Inclination")
	if err != nil {
		return nil, err
	}
	oi.InclinationDeg = incRad * 180 / math.Pi
	if oi.PeriodSeconds, err = get("Orbit_get_Period"); err != nil {
		return nil, err
	}
	if oi.TimeToApoapsis, err = get("Orbit_get_TimeToApoapsis"); err != nil {
		return nil, err
	}
	if oi.TimeToPeriapsis, err = get("Orbit_get_TimeToPeriapsis"); err != nil {
		return nil, err
	}
	if oi.SemiMajorAxisMeter, err = get("Orbit_get_SemiMajorAxis"); err != nil {
		return nil, err
	}
	body, err := c.callObject("SpaceCenter", "Orbit_get_Body", EncodeObject(orbit))
	if err != nil {
		return nil, err
	}
	if oi.Body, err = c.callString("SpaceCenter", "CelestialBody_get_Name", EncodeObject(body)); err != nil {
		return nil, err
	}
	return oi, nil
}

// FlightTelemetry is surface-relative flight data (in the vessel's surface
// reference frame), which is what a pilot reads.
type FlightTelemetry struct {
	MeanAltitudeMeter    float64 // altitude above sea level, meters
	SurfaceAltitudeMeter float64 // altitude above terrain, meters
	VerticalSpeed        float64 // m/s (positive = climbing)
	HorizontalSpeed      float64 // m/s
	GForce               float64 // g
	Mach                 float64 // mach number (0 in vacuum / when unavailable)
	PitchDeg             float64 // degrees
	HeadingDeg           float64 // degrees (0 = north, 90 = east)
	RollDeg              float64 // degrees
}

// FlightTelemetry reads surface-relative flight data. It resolves the vessel's
// surface reference frame explicitly (rather than relying on kRPC's default
// argument) so pitch/heading/roll and vertical/horizontal speed are unambiguous.
func (c *Conn) FlightTelemetry(vessel uint64) (*FlightTelemetry, error) {
	srf, err := c.callObject("SpaceCenter", "Vessel_get_SurfaceReferenceFrame", EncodeObject(vessel))
	if err != nil {
		return nil, err
	}
	flight, err := c.callObject("SpaceCenter", "Vessel_Flight", EncodeObject(vessel), EncodeObject(srf))
	if err != nil {
		return nil, err
	}
	ft := &FlightTelemetry{}
	get := func(proc string) (float64, error) {
		return c.callScalar("SpaceCenter", proc, EncodeObject(flight))
	}
	if ft.MeanAltitudeMeter, err = get("Flight_get_MeanAltitude"); err != nil {
		return nil, err
	}
	if ft.SurfaceAltitudeMeter, err = get("Flight_get_SurfaceAltitude"); err != nil {
		return nil, err
	}
	if ft.VerticalSpeed, err = get("Flight_get_VerticalSpeed"); err != nil {
		return nil, err
	}
	if ft.HorizontalSpeed, err = get("Flight_get_HorizontalSpeed"); err != nil {
		return nil, err
	}
	if ft.GForce, err = get("Flight_get_GForce"); err != nil {
		return nil, err
	}
	// Mach is only meaningful in atmosphere; kRPC returns 0 in vacuum.
	if ft.Mach, err = get("Flight_get_Mach"); err != nil {
		return nil, err
	}
	if ft.PitchDeg, err = get("Flight_get_Pitch"); err != nil {
		return nil, err
	}
	if ft.HeadingDeg, err = get("Flight_get_Heading"); err != nil {
		return nil, err
	}
	if ft.RollDeg, err = get("Flight_get_Roll"); err != nil {
		return nil, err
	}
	return ft, nil
}

// ResourceLevel is one resource's current and maximum amount. Percent is
// computed (0 when Max is 0).
type ResourceLevel struct {
	Name    string  // e.g. LiquidFuel, Oxidizer, ElectricCharge, MonoPropellant
	Amount  float64 // current units
	Max     float64 // capacity units
	Percent float64 // 0..100
}

// ResourcesInfo reports whole-vessel totals and, when available, the current
// decouple stage's resources.
type ResourcesInfo struct {
	Total       []ResourceLevel
	Stage       []ResourceLevel // may be empty if the stage query is unavailable
	StageNumber int32           // the current stage the Stage figures are for
	StageErr    string          // non-empty if stage resources could not be read (totals still valid)
}

// Resources reads the active vessel's resource levels.
func (c *Conn) Resources(vessel uint64) (*ResourcesInfo, error) {
	total, err := c.callObject("SpaceCenter", "Vessel_get_Resources", EncodeObject(vessel))
	if err != nil {
		return nil, err
	}
	ri := &ResourcesInfo{}
	if ri.Total, err = c.resourceLevels(total); err != nil {
		return nil, err
	}
	// Stage resources are best-effort: if any step fails, keep totals and note it.
	control, err := c.callObject("SpaceCenter", "Vessel_get_Control", EncodeObject(vessel))
	if err != nil {
		ri.StageErr = err.Error()
		return ri, nil
	}
	stage, err := c.callInt("SpaceCenter", "Control_get_CurrentStage", EncodeObject(control))
	if err != nil {
		ri.StageErr = err.Error()
		return ri, nil
	}
	ri.StageNumber = stage
	stageRes, err := c.callObject("SpaceCenter", "Vessel_ResourcesInDecoupleStage",
		EncodeObject(vessel), EncodeSint32(stage), EncodeBool(false))
	if err != nil {
		ri.StageErr = err.Error()
		return ri, nil
	}
	if ri.Stage, err = c.resourceLevels(stageRes); err != nil {
		ri.StageErr = err.Error()
	}
	return ri, nil
}

// resourceLevels enumerates a Resources object's named resources with amounts.
func (c *Conn) resourceLevels(resources uint64) ([]ResourceLevel, error) {
	names, err := c.callStringList("SpaceCenter", "Resources_get_Names", EncodeObject(resources))
	if err != nil {
		return nil, err
	}
	out := make([]ResourceLevel, 0, len(names))
	for _, name := range names {
		amount, err := c.callScalar("SpaceCenter", "Resources_Amount", EncodeObject(resources), EncodeString(name))
		if err != nil {
			return nil, err
		}
		max, err := c.callScalar("SpaceCenter", "Resources_Max", EncodeObject(resources), EncodeString(name))
		if err != nil {
			return nil, err
		}
		pct := 0.0
		if max > 0 {
			pct = amount / max * 100
		}
		out = append(out, ResourceLevel{Name: name, Amount: amount, Max: max, Percent: pct})
	}
	return out, nil
}

// ManeuverNode is one planned burn. BurnEstimateSeconds is an APPROXIMATION from
// the rocket equation using current mass, available thrust, and specific impulse
// — it is unset (0) with BurnEstimateNote populated when thrust/Isp are zero.
type ManeuverNode struct {
	DeltaV              float64 // planned delta-v magnitude, m/s
	RemainingDeltaV     float64 // remaining delta-v, m/s
	UT                  float64 // universal time of the node, seconds
	TimeToSeconds       float64 // seconds until the node
	BurnEstimateSeconds float64 // approximate burn duration, seconds (0 if not estimable)
	BurnEstimateNote    string  // populated when a burn estimate can't be made
}

// ManeuverNodes reads existing maneuver nodes (it never creates or edits them).
func (c *Conn) ManeuverNodes(vessel uint64) ([]ManeuverNode, error) {
	control, err := c.callObject("SpaceCenter", "Vessel_get_Control", EncodeObject(vessel))
	if err != nil {
		return nil, err
	}
	nodeIDs, err := c.callObjectList("SpaceCenter", "Control_get_Nodes", EncodeObject(control))
	if err != nil {
		return nil, err
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	// Ship parameters for the burn estimate (read once; approximate).
	mass, _ := c.callScalar("SpaceCenter", "Vessel_get_Mass", EncodeObject(vessel))
	thrust, _ := c.callScalar("SpaceCenter", "Vessel_get_AvailableThrust", EncodeObject(vessel))
	isp, _ := c.callScalar("SpaceCenter", "Vessel_get_SpecificImpulse", EncodeObject(vessel))

	out := make([]ManeuverNode, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n := ManeuverNode{}
		if n.DeltaV, err = c.callScalar("SpaceCenter", "Node_get_DeltaV", EncodeObject(id)); err != nil {
			return nil, err
		}
		if n.RemainingDeltaV, err = c.callScalar("SpaceCenter", "Node_get_RemainingDeltaV", EncodeObject(id)); err != nil {
			return nil, err
		}
		if n.UT, err = c.callScalar("SpaceCenter", "Node_get_UT", EncodeObject(id)); err != nil {
			return nil, err
		}
		if n.TimeToSeconds, err = c.callScalar("SpaceCenter", "Node_get_TimeTo", EncodeObject(id)); err != nil {
			return nil, err
		}
		n.BurnEstimateSeconds, n.BurnEstimateNote = burnTime(mass, thrust, isp, n.DeltaV)
		out = append(out, n)
	}
	return out, nil
}

// burnTime approximates burn duration via the rocket equation:
//
//	t = (m0 * ve / F) * (1 - e^(-dv/ve)),  ve = Isp * g0
//
// It needs positive thrust and Isp; otherwise it returns 0 with an explanatory
// note. This is an estimate for the current stage's active engines, not a
// staging-aware plan.
func burnTime(mass, thrust, isp, dv float64) (float64, string) {
	if thrust <= 0 || isp <= 0 || mass <= 0 {
		return 0, "no burn estimate (no active thrust / Isp)"
	}
	ve := isp * g0
	t := (mass * ve / thrust) * (1 - math.Exp(-dv/ve))
	return t, ""
}

// CrewMembers reads the names of the crew aboard the active vessel.
func (c *Conn) CrewMembers(vessel uint64) ([]string, error) {
	ids, err := c.callObjectList("SpaceCenter", "Vessel_get_Crew", EncodeObject(vessel))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name, err := c.callString("SpaceCenter", "CrewMember_get_Name", EncodeObject(id))
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}
