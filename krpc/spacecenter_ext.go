package krpc

// spacecenter_ext.go — the READ helpers added for the flight-computer wave:
// targets, the vessel list, relative geometry primitives (position/velocity/
// direction in a chosen reference frame), kRPC's own exact orbit relations
// (closest approach, relative inclination), the delta-v inputs, and celestial
// body facts. All are read-only. The vector-returning helpers hand back a plain
// [3]float64 so the MCP layer does the geometry with the astro package; kRPC
// stays the wire client and owns no orbital math.

import "math"

// ---- small object plumbing (unexported) ----

func (c *Conn) vesselOrbit(vessel uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "Vessel_get_Orbit", EncodeObject(vessel))
}

func (c *Conn) vesselControl(vessel uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "Vessel_get_Control", EncodeObject(vessel))
}

func (c *Conn) orbitBodyID(orbit uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "Orbit_get_Body", EncodeObject(orbit))
}

func (c *Conn) bodyName(body uint64) (string, error) {
	return c.callString("SpaceCenter", "CelestialBody_get_Name", EncodeObject(body))
}

// BodyName exposes a celestial body's name.
func (c *Conn) BodyName(body uint64) (string, error) { return c.bodyName(body) }

// bodyNonRotatingFrame returns the body-centered, non-rotating (inertial-axes)
// reference frame. Positions and velocities of two objects expressed in the SAME
// such frame subtract to give exact relative position and relative velocity — the
// frame's own motion cancels, and because it does not rotate there is no
// spin-contamination of the velocity difference.
func (c *Conn) bodyNonRotatingFrame(body uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "CelestialBody_get_NonRotatingReferenceFrame", EncodeObject(body))
}

func (c *Conn) bodyMu(body uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "CelestialBody_get_GravitationalParameter", EncodeObject(body))
}

// ---- targets ----

// TargetVessel returns the currently targeted vessel's object id, or 0 if no
// vessel is targeted.
func (c *Conn) TargetVessel() (uint64, error) {
	return c.callObject("SpaceCenter", "get_TargetVessel")
}

// TargetBody returns the currently targeted celestial body's object id, or 0 if
// no body is targeted.
func (c *Conn) TargetBody() (uint64, error) {
	return c.callObject("SpaceCenter", "get_TargetBody")
}

// ---- vessel list ----

// VesselBrief is a light per-vessel summary for the vessel list: identity, type,
// situation, and the body it is at. Distance to the active vessel is computed by
// the MCP layer (it needs a common reference frame), not here.
type VesselBrief struct {
	ID        uint64
	Name      string
	Type      string // Ship, Probe, Lander, Debris, EVA, Station, Relay, Rover, ...
	Situation string // Orbiting, SubOrbital, Flying, Landed, Splashed, PreLaunch, Docked, Escaping
	Body      string
}

// Vessels returns the object ids of every vessel in the game.
func (c *Conn) Vessels() ([]uint64, error) {
	return c.callObjectList("SpaceCenter", "get_Vessels")
}

// VesselBrief reads a vessel's identity, type, situation, and current body.
func (c *Conn) VesselBrief(vessel uint64) (VesselBrief, error) {
	vb := VesselBrief{ID: vessel}
	var err error
	if vb.Name, err = c.callString("SpaceCenter", "Vessel_get_Name", EncodeObject(vessel)); err != nil {
		return vb, err
	}
	if _, vb.Type, err = c.callEnum("SpaceCenter", "Vessel_get_Type", "SpaceCenter.VesselType", EncodeObject(vessel)); err != nil {
		return vb, err
	}
	if _, vb.Situation, err = c.callEnum("SpaceCenter", "Vessel_get_Situation", "SpaceCenter.VesselSituation", EncodeObject(vessel)); err != nil {
		return vb, err
	}
	orbit, err := c.vesselOrbit(vessel)
	if err != nil {
		return vb, err
	}
	body, err := c.orbitBodyID(orbit)
	if err != nil {
		return vb, err
	}
	if vb.Body, err = c.bodyName(body); err != nil {
		return vb, err
	}
	return vb, nil
}

// ---- relative geometry primitives ----

// VesselPosition returns the vessel's position (meters) in the given reference
// frame as a plain [3]float64.
func (c *Conn) VesselPosition(vessel, frame uint64) ([3]float64, error) {
	b, err := c.Call("SpaceCenter", "Vessel_Position", EncodeObject(vessel), EncodeObject(frame))
	if err != nil {
		return [3]float64{}, err
	}
	return DecodeVector3(b)
}

// VesselVelocity returns the vessel's velocity (m/s) in the given reference frame.
func (c *Conn) VesselVelocity(vessel, frame uint64) ([3]float64, error) {
	b, err := c.Call("SpaceCenter", "Vessel_Velocity", EncodeObject(vessel), EncodeObject(frame))
	if err != nil {
		return [3]float64{}, err
	}
	return DecodeVector3(b)
}

// VesselDirection returns the unit vector the vessel is pointing (its nose) in the
// given reference frame.
func (c *Conn) VesselDirection(vessel, frame uint64) ([3]float64, error) {
	b, err := c.Call("SpaceCenter", "Vessel_Direction", EncodeObject(vessel), EncodeObject(frame))
	if err != nil {
		return [3]float64{}, err
	}
	return DecodeVector3(b)
}

// BodyPosition returns a celestial body's position (meters) in the given frame.
func (c *Conn) BodyPosition(body, frame uint64) ([3]float64, error) {
	b, err := c.Call("SpaceCenter", "CelestialBody_Position", EncodeObject(body), EncodeObject(frame))
	if err != nil {
		return [3]float64{}, err
	}
	return DecodeVector3(b)
}

// BodyVelocity returns a celestial body's velocity (m/s) in the given frame.
func (c *Conn) BodyVelocity(body, frame uint64) ([3]float64, error) {
	b, err := c.Call("SpaceCenter", "CelestialBody_Velocity", EncodeObject(body), EncodeObject(frame))
	if err != nil {
		return [3]float64{}, err
	}
	return DecodeVector3(b)
}

// ---- orbit radii (from body center) ----

// OrbitApoapsisRadius returns the apoapsis distance from the body CENTER (meters)
// — the radius the burn math wants, not the altitude above sea level.
func (c *Conn) OrbitApoapsisRadius(orbit uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "Orbit_get_Apoapsis", EncodeObject(orbit))
}

// OrbitPeriapsisRadius returns the periapsis distance from the body center (m).
func (c *Conn) OrbitPeriapsisRadius(orbit uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "Orbit_get_Periapsis", EncodeObject(orbit))
}

// OrbitRadiusNow returns the current orbital radius from the body center (m).
func (c *Conn) OrbitRadiusNow(orbit uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "Orbit_get_Radius", EncodeObject(orbit))
}

// OrbitSemiMajorAxis returns the orbit's semi-major axis (m).
func (c *Conn) OrbitSemiMajorAxis(orbit uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "Orbit_get_SemiMajorAxis", EncodeObject(orbit))
}

// VesselOrbitID exposes the active vessel's Orbit object id.
func (c *Conn) VesselOrbitID(vessel uint64) (uint64, error) { return c.vesselOrbit(vessel) }

// OrbitBodyID exposes the id of the body an orbit is around.
func (c *Conn) OrbitBodyID(orbit uint64) (uint64, error) { return c.orbitBodyID(orbit) }

// BodyOrbitID returns a celestial body's OWN orbit (its path around its parent),
// or 0 for a body with no parent (the Sun).
func (c *Conn) BodyOrbitID(body uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "CelestialBody_get_Orbit", EncodeObject(body))
}

// BodyNonRotatingFrame exposes a body's non-rotating reference frame id (the
// common frame the MCP layer expresses positions/velocities in for exact
// relative geometry).
func (c *Conn) BodyNonRotatingFrame(body uint64) (uint64, error) {
	return c.bodyNonRotatingFrame(body)
}

// BodySurfaceGravity returns a body's surface gravitational acceleration (m/s^2),
// used for thrust-to-weight ratios.
func (c *Conn) BodySurfaceGravity(body uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "CelestialBody_get_SurfaceGravity", EncodeObject(body))
}

// BodyMu exposes a body's standard gravitational parameter (m^3/s^2).
func (c *Conn) BodyMu(body uint64) (float64, error) { return c.bodyMu(body) }

// VesselMET returns mission elapsed time (seconds) — used by the flight-control
// loop's per-tick telemetry.
func (c *Conn) VesselMET(vessel uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "Vessel_get_MET", EncodeObject(vessel))
}

// VesselAvailableThrust returns the full-throttle thrust (N) of the engines that
// are currently active AND have fuel. The flight loop uses >0 as its "the active
// stage still has a live engine" signal: it drops to 0 when the active stage
// flames out, which is what drives auto-staging.
func (c *Conn) VesselAvailableThrust(vessel uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "Vessel_get_AvailableThrust", EncodeObject(vessel))
}

// BodyEquatorialRadius returns a body's equatorial radius (m) — used to turn a
// requested target ALTITUDE into a radius from the body center for transfer math.
func (c *Conn) BodyEquatorialRadius(body uint64) (float64, error) {
	return c.callScalar("SpaceCenter", "CelestialBody_get_EquatorialRadius", EncodeObject(body))
}

// ---- exact kRPC orbit relations ----

// OrbitClosestApproach asks kRPC (which runs KSP's own conic solver) for the
// closest approach between two orbits: the separation in meters and the universal
// time at which it occurs. Meaningful only when both orbits are about the same
// primary body — the caller checks that first.
func (c *Conn) OrbitClosestApproach(orbit, targetOrbit uint64) (dist, timeUT float64, err error) {
	if dist, err = c.callScalar("SpaceCenter", "Orbit_DistanceAtClosestApproach",
		EncodeObject(orbit), EncodeObject(targetOrbit)); err != nil {
		return 0, 0, err
	}
	if timeUT, err = c.callScalar("SpaceCenter", "Orbit_TimeOfClosestApproach",
		EncodeObject(orbit), EncodeObject(targetOrbit)); err != nil {
		return 0, 0, err
	}
	return dist, timeUT, nil
}

// OrbitRelativeInclinationDeg returns the angle (degrees) between two orbital
// planes, from kRPC's Orbit_RelativeInclination (which returns radians).
func (c *Conn) OrbitRelativeInclinationDeg(orbit, targetOrbit uint64) (float64, error) {
	rad, err := c.callScalar("SpaceCenter", "Orbit_RelativeInclination",
		EncodeObject(orbit), EncodeObject(targetOrbit))
	if err != nil {
		return 0, err
	}
	return rad * 180 / math.Pi, nil
}

// ---- delta-v inputs ----

// DeltaVInputs are the live figures the delta_v_status / burn tools need. Masses
// in kg, thrusts in newtons, specific impulses in seconds. AvailableThrust is the
// thrust of currently-active engines at the current throttle-capable maximum;
// MaxVacuumThrust is the full-throttle vacuum figure.
type DeltaVInputs struct {
	Mass                  float64
	DryMass               float64
	Thrust                float64 // actual thrust right now (throttle-dependent), N
	AvailableThrust       float64 // full-throttle thrust of active engines, N
	MaxThrust             float64
	MaxVacuumThrust       float64
	SpecificImpulse       float64 // current, seconds
	VacuumSpecificImpulse float64 // seconds
}

// DeltaVInputs reads the mass/thrust/Isp figures for the active vessel.
func (c *Conn) DeltaVInputs(vessel uint64) (DeltaVInputs, error) {
	d := DeltaVInputs{}
	get := func(proc string) (float64, error) {
		return c.callScalar("SpaceCenter", proc, EncodeObject(vessel))
	}
	var err error
	if d.Mass, err = get("Vessel_get_Mass"); err != nil {
		return d, err
	}
	if d.DryMass, err = get("Vessel_get_DryMass"); err != nil {
		return d, err
	}
	if d.Thrust, err = get("Vessel_get_Thrust"); err != nil {
		return d, err
	}
	if d.AvailableThrust, err = get("Vessel_get_AvailableThrust"); err != nil {
		return d, err
	}
	if d.MaxThrust, err = get("Vessel_get_MaxThrust"); err != nil {
		return d, err
	}
	if d.MaxVacuumThrust, err = get("Vessel_get_MaxVacuumThrust"); err != nil {
		return d, err
	}
	if d.SpecificImpulse, err = get("Vessel_get_SpecificImpulse"); err != nil {
		return d, err
	}
	if d.VacuumSpecificImpulse, err = get("Vessel_get_VacuumSpecificImpulse"); err != nil {
		return d, err
	}
	return d, nil
}

// ---- celestial body facts ----

// BodyFacts are the fixed properties of a celestial body the transfer math needs.
type BodyFacts struct {
	Name                 string
	EquatorialRadiusM    float64
	SurfaceGravityMS2    float64
	SphereOfInfluenceM   float64 // Inf for the Sun (no SOI limit); reported honestly by the MCP layer
	RotationalPeriodS    float64
	GravitationalParamMu float64 // m^3/s^2
	MassKg               float64
	HasAtmosphere        bool
	AtmosphereDepthM     float64
}

// Bodies returns a name->object-id map of every celestial body in the game, from
// SpaceCenter.get_Bodies (a dictionary<string, CelestialBody>).
func (c *Conn) Bodies() (map[string]uint64, error) {
	b, err := c.Call("SpaceCenter", "get_Bodies")
	if err != nil {
		return nil, err
	}
	entries, err := DecodeDictionary(b)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(entries))
	for _, e := range entries {
		name, err := DecodeString(e.Key)
		if err != nil {
			return nil, err
		}
		id, err := DecodeObject(e.Value)
		if err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, nil
}

// BodyFacts reads the fixed facts of a celestial body.
func (c *Conn) BodyFacts(body uint64) (BodyFacts, error) {
	f := BodyFacts{}
	get := func(proc string) (float64, error) {
		return c.callScalar("SpaceCenter", proc, EncodeObject(body))
	}
	var err error
	if f.Name, err = c.bodyName(body); err != nil {
		return f, err
	}
	if f.EquatorialRadiusM, err = get("CelestialBody_get_EquatorialRadius"); err != nil {
		return f, err
	}
	if f.SurfaceGravityMS2, err = get("CelestialBody_get_SurfaceGravity"); err != nil {
		return f, err
	}
	if f.SphereOfInfluenceM, err = get("CelestialBody_get_SphereOfInfluence"); err != nil {
		return f, err
	}
	if f.RotationalPeriodS, err = get("CelestialBody_get_RotationalPeriod"); err != nil {
		return f, err
	}
	if f.GravitationalParamMu, err = get("CelestialBody_get_GravitationalParameter"); err != nil {
		return f, err
	}
	if f.MassKg, err = get("CelestialBody_get_Mass"); err != nil {
		return f, err
	}
	if f.HasAtmosphere, err = c.callBool("SpaceCenter", "CelestialBody_get_HasAtmosphere", EncodeObject(body)); err != nil {
		return f, err
	}
	if f.AtmosphereDepthM, err = get("CelestialBody_get_AtmosphereDepth"); err != nil {
		return f, err
	}
	return f, nil
}

// callBool invokes a bool-returning procedure.
func (c *Conn) callBool(service, proc string, args ...[]byte) (bool, error) {
	b, err := c.Call(service, proc, args...)
	if err != nil {
		return false, err
	}
	return DecodeBool(b)
}
