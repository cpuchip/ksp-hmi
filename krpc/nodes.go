package krpc

// nodes.go — the ONLY mutating kRPC surface in this client. These three writes
// add or remove maneuver NODES on the flight plan. A node is a planned burn drawn
// on the navball; creating or deleting one changes nothing physical — no engine
// fires, no stage separates, no attitude changes. Every write here is reversible
// (Node_Remove / Control_RemoveNodes). Deliberately kept out of spacecenter.go so
// the write surface is one small, auditable file; there is intentionally NO
// throttle/stage/SAS/warp/autopilot call anywhere in this package.

// AddNode places a maneuver node on the active vessel's flight plan at universal
// time ut, with the given prograde / normal / radial components (m/s), and
// returns the new node's object id. This MUTATES the flight plan (reversibly —
// see NodeRemove). kRPC signature: Control_AddNode(ut double, prograde float,
// normal float, radial float) -> Node; the three burn components are FLOAT on the
// wire, ut is DOUBLE.
func (c *Conn) AddNode(control uint64, ut, prograde, normal, radial float64) (uint64, error) {
	return c.callObject("SpaceCenter", "Control_AddNode",
		EncodeObject(control),
		EncodeDouble(ut),
		EncodeFloat(float32(prograde)),
		EncodeFloat(float32(normal)),
		EncodeFloat(float32(radial)),
	)
}

// NodeRemove deletes a single maneuver node (reverses one AddNode).
func (c *Conn) NodeRemove(node uint64) error {
	_, err := c.Call("SpaceCenter", "Node_Remove", EncodeObject(node))
	return err
}

// RemoveAllNodes clears every maneuver node from the active vessel's flight plan
// (Control_RemoveNodes). Reversible only by re-planning — it is the destructive
// one, so its MCP tool spells that out.
func (c *Conn) RemoveAllNodes(control uint64) error {
	_, err := c.Call("SpaceCenter", "Control_RemoveNodes", EncodeObject(control))
	return err
}

// ControlNodes returns the object ids of the existing maneuver nodes on a
// vessel's control, in flight-plan order (used to delete a node by index and to
// check whether any node already exists before planning a new one).
func (c *Conn) ControlNodes(control uint64) ([]uint64, error) {
	return c.callObjectList("SpaceCenter", "Control_get_Nodes", EncodeObject(control))
}

// VesselControl exposes the active vessel's Control object id (the handle
// AddNode / RemoveAllNodes / ControlNodes operate on).
func (c *Conn) VesselControl(vessel uint64) (uint64, error) {
	return c.vesselControl(vessel)
}

// NodeDetail is a single node's parameters plus its resulting predicted orbit id.
type NodeDetail struct {
	DeltaV          float64 // total planned delta-v, m/s
	RemainingDeltaV float64 // remaining delta-v, m/s
	UT              float64 // universal time of the node, seconds
	TimeTo          float64 // seconds until the node
	Prograde        float64 // burn component, m/s
	Normal          float64 // burn component, m/s
	Radial          float64 // burn component, m/s
	OrbitID         uint64  // the orbit the vessel will be on AFTER the burn
}

// NodeDetail reads a node's burn components, timing, and the id of the orbit that
// results from executing it (Node_get_Orbit) so the caller can read back the
// predicted apoapsis/periapsis.
func (c *Conn) NodeDetail(node uint64) (NodeDetail, error) {
	d := NodeDetail{}
	get := func(proc string) (float64, error) {
		return c.callScalar("SpaceCenter", proc, EncodeObject(node))
	}
	var err error
	if d.DeltaV, err = get("Node_get_DeltaV"); err != nil {
		return d, err
	}
	if d.RemainingDeltaV, err = get("Node_get_RemainingDeltaV"); err != nil {
		return d, err
	}
	if d.UT, err = get("Node_get_UT"); err != nil {
		return d, err
	}
	if d.TimeTo, err = get("Node_get_TimeTo"); err != nil {
		return d, err
	}
	if d.Prograde, err = get("Node_get_Prograde"); err != nil {
		return d, err
	}
	if d.Normal, err = get("Node_get_Normal"); err != nil {
		return d, err
	}
	if d.Radial, err = get("Node_get_Radial"); err != nil {
		return d, err
	}
	if d.OrbitID, err = c.callObject("SpaceCenter", "Node_get_Orbit", EncodeObject(node)); err != nil {
		return d, err
	}
	return d, nil
}
