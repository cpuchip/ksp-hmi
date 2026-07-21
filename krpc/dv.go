package krpc

// dv.go — the heavier per-part read behind the per-stage delta-v tool: it walks
// the whole part tree for each part's mass, dry mass, and decouple stage, plus
// each engine's activation stage, decouple stage, vacuum Isp, and vacuum thrust.
// These feed the pure staging-delta-v model in the astro package (the MCP layer
// converts these structs into astro's inputs). It is READ-ONLY. Because it reads
// several properties per part it is deliberately kept OUT of the light
// PartsSnapshotFor path that preflight/staging_plan use — only the delta-v tool
// pays for the full walk.

// PartMass is one part's mass budget for the staging delta-v computation.
type PartMass struct {
	DecoupleStage int32   // stage at which the part separates; -1 = never
	WetMass       float64 // current mass incl. remaining propellant (kg)
	DryMass       float64 // mass without propellant (kg)
}

// EngineDV is one engine's thrust/Isp contribution for the staging computation.
type EngineDV struct {
	ActivationStage  int32   // the engine part's activation stage (ignition)
	DecoupleStage    int32   // the engine part's decouple stage; -1 = never
	VacuumIspS       float64 // vacuum specific impulse, seconds
	MaxVacuumThrustN float64 // vacuum thrust, newtons (thrust-weight for Isp average)
}

// StageMassProfile reads every part's mass/decouple-stage and every engine's
// activation stage, decouple stage, vacuum Isp, and vacuum thrust. Best-effort
// per part/engine: a single unreadable property is skipped (leaving its zero
// value) rather than failing the whole profile — the caller labels the result an
// estimate anyway. A non-nil error means the part container itself was unreadable.
func (c *Conn) StageMassProfile(vessel uint64) (parts []PartMass, engines []EngineDV, err error) {
	partsObj, err := c.callObject("SpaceCenter", "Vessel_get_Parts", EncodeObject(vessel))
	if err != nil {
		return nil, nil, err
	}

	allIDs, err := c.callObjectList("SpaceCenter", "Parts_get_All", EncodeObject(partsObj))
	if err != nil {
		return nil, nil, err
	}
	parts = make([]PartMass, 0, len(allIDs))
	for _, id := range allIDs {
		pm := PartMass{}
		pm.DecoupleStage, _ = c.callInt("SpaceCenter", "Part_get_DecoupleStage", EncodeObject(id))
		pm.WetMass, _ = c.callScalar("SpaceCenter", "Part_get_Mass", EncodeObject(id))
		pm.DryMass, _ = c.callScalar("SpaceCenter", "Part_get_DryMass", EncodeObject(id))
		parts = append(parts, pm)
	}

	engIDs, err := c.callObjectList("SpaceCenter", "Parts_get_Engines", EncodeObject(partsObj))
	if err != nil {
		return parts, nil, err
	}
	engines = make([]EngineDV, 0, len(engIDs))
	for _, eid := range engIDs {
		ed := EngineDV{}
		part, perr := c.callObject("SpaceCenter", "Engine_get_Part", EncodeObject(eid))
		if perr == nil {
			ed.ActivationStage, _ = c.callInt("SpaceCenter", "Part_get_Stage", EncodeObject(part))
			ed.DecoupleStage, _ = c.callInt("SpaceCenter", "Part_get_DecoupleStage", EncodeObject(part))
		}
		ed.VacuumIspS, _ = c.callScalar("SpaceCenter", "Engine_get_VacuumSpecificImpulse", EncodeObject(eid))
		ed.MaxVacuumThrustN, _ = c.callScalar("SpaceCenter", "Engine_get_MaxVacuumThrust", EncodeObject(eid))
		engines = append(engines, ed)
	}
	return parts, engines, nil
}
