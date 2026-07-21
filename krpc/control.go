package krpc

// control.go — the LIVE-CONTROL write surface: throttle, staging, SAS, and the
// autopilot (engage + target pitch/heading in the surface frame). This is the
// ONLY file besides nodes.go that mutates the flight, and unlike nodes.go (which
// only draws maneuver nodes) THIS one actually moves the vessel — it fires
// engines and stages. It is deliberately isolated here so the mutating surface is
// one small, auditable file, and nothing calls it unless the ksp-mcp server is
// started with -enable-flight AND the pilot has armed + given the go (see
// cmd/ksp-mcp/flight.go). All procedure names verified live against kRPC 0.5.4.

// VesselAutoPilot returns the vessel's AutoPilot object id.
func (c *Conn) VesselAutoPilot(vessel uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "Vessel_get_AutoPilot", EncodeObject(vessel))
}

// VesselSurfaceFrame returns the vessel's surface reference frame — the frame in
// which target pitch/heading are the intuitive "point the nose this way relative
// to the horizon and compass" angles.
func (c *Conn) VesselSurfaceFrame(vessel uint64) (uint64, error) {
	return c.callObject("SpaceCenter", "Vessel_get_SurfaceReferenceFrame", EncodeObject(vessel))
}

// SetThrottle sets the main throttle (0..1). The caller clamps; this trusts it.
func (c *Conn) SetThrottle(control uint64, throttle float64) error {
	_, err := c.Call("SpaceCenter", "Control_set_Throttle", EncodeObject(control), EncodeFloat(float32(throttle)))
	return err
}

// ActivateNextStage fires the next stage (equivalent to pressing space). kRPC
// returns the list of vessels that result; we don't need it here.
func (c *Conn) ActivateNextStage(control uint64) error {
	_, err := c.Call("SpaceCenter", "Control_ActivateNextStage", EncodeObject(control))
	return err
}

// SetSAS toggles SAS.
func (c *Conn) SetSAS(control uint64, on bool) error {
	_, err := c.Call("SpaceCenter", "Control_set_SAS", EncodeObject(control), EncodeBool(on))
	return err
}

// SetRCS toggles RCS.
func (c *Conn) SetRCS(control uint64, on bool) error {
	_, err := c.Call("SpaceCenter", "Control_set_RCS", EncodeObject(control), EncodeBool(on))
	return err
}

// AutopilotEngage / AutopilotDisengage turn the kRPC autopilot on/off. While
// engaged, kRPC steers the vessel toward the last target pitch/heading.
func (c *Conn) AutopilotEngage(autopilot uint64) error {
	_, err := c.Call("SpaceCenter", "AutoPilot_Engage", EncodeObject(autopilot))
	return err
}

func (c *Conn) AutopilotDisengage(autopilot uint64) error {
	_, err := c.Call("SpaceCenter", "AutoPilot_Disengage", EncodeObject(autopilot))
	return err
}

// AutopilotSetReferenceFrame sets the frame the target pitch/heading are relative
// to — set this to the vessel's surface frame before commanding pitch/heading.
func (c *Conn) AutopilotSetReferenceFrame(autopilot, frame uint64) error {
	_, err := c.Call("SpaceCenter", "AutoPilot_set_ReferenceFrame", EncodeObject(autopilot), EncodeObject(frame))
	return err
}

// AutopilotTargetPitchAndHeading commands the autopilot to hold a pitch (degrees
// above the horizon) and heading (compass degrees, 0=N, 90=E) in its reference
// frame.
func (c *Conn) AutopilotTargetPitchAndHeading(autopilot uint64, pitch, heading float64) error {
	_, err := c.Call("SpaceCenter", "AutoPilot_TargetPitchAndHeading",
		EncodeObject(autopilot), EncodeFloat(float32(pitch)), EncodeFloat(float32(heading)))
	return err
}
