package krpc

// parts.go — READ helpers for the preflight / staging wave: the active vessel's
// part tree summarized into the categories a go/no-go check needs — parachutes,
// engines, decouplers — each with the staging index at which it activates, plus
// the whole-vessel part count and current stage. All read-only; nothing here
// mutates the game.
//
// verify-real-path NOTE: the kRPC procedure names below follow the kRPC 0.5.4
// SpaceCenter API (Parts.parachutes/engines/decouplers, Part.stage/
// decouple_stage/title, Parachute.armed/deployed/state/deploy_altitude,
// Engine.active/has_fuel/max_thrust, Decoupler.staged). They were confirmed
// against the published docs but the file was authored with no live server to
// re-dump against, so every category is read BEST-EFFORT: a category-level
// failure is captured in the matching *Err field (never fatal), and secondary
// per-part properties ignore their own read errors and leave the zero value.
// A single wrong name therefore degrades one field or one section, not the whole
// snapshot — and `ksp-mcp -smoke` against a running craft is the live oracle that
// confirms the names for real.

// ParachuteInfo is one parachute and how it is staged.
type ParachuteInfo struct {
	Title           string
	Stage           int32   // staging index that deploys it; -1 = not staged (manual/action group)
	Armed           bool    // set to deploy on the next trigger
	Deployed        bool    // canopy out (partially or fully)
	State           string  // ParachuteState enum name (e.g. Stowed/Armed/SemiDeployed/Deployed/Cut)
	DeployAltitudeM float64 // full-deploy altitude (stock chutes)
}

// EngineInfo is one engine (anything that generates thrust) and how it is staged.
type EngineInfo struct {
	Title      string
	Stage      int32 // staging index that ignites it; -1 = not staged
	Active     bool
	HasFuel    bool
	MaxThrustN float64
}

// DecouplerInfo is one decoupler/separator and how it is staged.
type DecouplerInfo struct {
	Title         string
	Stage         int32 // staging index that fires it; -1 = not staged
	DecoupleStage int32 // stage at which its part separates; -1 = never
	Staged        bool  // whether staging (vs. only a manual action) fires it
}

// PartsSnapshot is the categorized part tree the preflight/staging tools read.
// TotalParts is the whole-vessel count; each category is best-effort with its
// own error string (empty on success), mirroring Resources.StageErr.
type PartsSnapshot struct {
	TotalParts   int
	CurrentStage int32
	Parachutes   []ParachuteInfo
	Engines      []EngineInfo
	Decouplers   []DecouplerInfo
	PartsErr     string // non-empty if the whole-vessel part list couldn't be counted
	ParachuteErr string
	EngineErr    string
	DecouplerErr string
}

// PartsSnapshotFor reads and categorizes the active vessel's part tree. A non-nil
// error means the part container itself was unreadable (the whole feature can't
// run); a readable container with a failed category returns the snapshot with that
// category's *Err set so the caller can report it and still use the rest.
func (c *Conn) PartsSnapshotFor(vessel uint64) (PartsSnapshot, error) {
	var snap PartsSnapshot

	parts, err := c.callObject("SpaceCenter", "Vessel_get_Parts", EncodeObject(vessel))
	if err != nil {
		return snap, err
	}

	// Whole-vessel part count (best-effort — a failure here must not sink categories).
	if all, aerr := c.callObjectList("SpaceCenter", "Parts_get_All", EncodeObject(parts)); aerr == nil {
		snap.TotalParts = len(all)
	} else {
		snap.PartsErr = aerr.Error()
	}

	// Current stage, via the vessel's control (best-effort).
	if control, cerr := c.vesselControl(vessel); cerr == nil {
		if cs, serr := c.callInt("SpaceCenter", "Control_get_CurrentStage", EncodeObject(control)); serr == nil {
			snap.CurrentStage = cs
		}
	}

	snap.Parachutes, snap.ParachuteErr = c.readParachutes(parts)
	snap.Engines, snap.EngineErr = c.readEngines(parts)
	snap.Decouplers, snap.DecouplerErr = c.readDecouplers(parts)
	return snap, nil
}

// partStage reads a part's activation stage, decouple stage, and title — the three
// fields shared by every category. These are treated as category-critical: if they
// fail the caller stops that category (returns what it has plus the error).
func (c *Conn) partStage(part uint64) (stage, decoupleStage int32, title string, err error) {
	if stage, err = c.callInt("SpaceCenter", "Part_get_Stage", EncodeObject(part)); err != nil {
		return
	}
	if decoupleStage, err = c.callInt("SpaceCenter", "Part_get_DecoupleStage", EncodeObject(part)); err != nil {
		return
	}
	title, err = c.callString("SpaceCenter", "Part_get_Title", EncodeObject(part))
	return
}

func (c *Conn) readParachutes(parts uint64) ([]ParachuteInfo, string) {
	ids, err := c.callObjectList("SpaceCenter", "Parts_get_Parachutes", EncodeObject(parts))
	if err != nil {
		return nil, err.Error()
	}
	out := make([]ParachuteInfo, 0, len(ids))
	for _, id := range ids {
		part, err := c.callObject("SpaceCenter", "Parachute_get_Part", EncodeObject(id))
		if err != nil {
			return out, err.Error()
		}
		stage, _, title, err := c.partStage(part)
		if err != nil {
			return out, err.Error()
		}
		p := ParachuteInfo{Title: title, Stage: stage}
		// Secondary properties are best-effort: a missing one leaves the zero value.
		p.Armed, _ = c.callBool("SpaceCenter", "Parachute_get_Armed", EncodeObject(id))
		p.Deployed, _ = c.callBool("SpaceCenter", "Parachute_get_Deployed", EncodeObject(id))
		_, p.State, _ = c.callEnum("SpaceCenter", "Parachute_get_State", "SpaceCenter.ParachuteState", EncodeObject(id))
		p.DeployAltitudeM, _ = c.callScalar("SpaceCenter", "Parachute_get_DeployAltitude", EncodeObject(id))
		out = append(out, p)
	}
	return out, ""
}

func (c *Conn) readEngines(parts uint64) ([]EngineInfo, string) {
	ids, err := c.callObjectList("SpaceCenter", "Parts_get_Engines", EncodeObject(parts))
	if err != nil {
		return nil, err.Error()
	}
	out := make([]EngineInfo, 0, len(ids))
	for _, id := range ids {
		part, err := c.callObject("SpaceCenter", "Engine_get_Part", EncodeObject(id))
		if err != nil {
			return out, err.Error()
		}
		stage, _, title, err := c.partStage(part)
		if err != nil {
			return out, err.Error()
		}
		e := EngineInfo{Title: title, Stage: stage}
		e.Active, _ = c.callBool("SpaceCenter", "Engine_get_Active", EncodeObject(id))
		e.HasFuel, _ = c.callBool("SpaceCenter", "Engine_get_HasFuel", EncodeObject(id))
		e.MaxThrustN, _ = c.callScalar("SpaceCenter", "Engine_get_MaxThrust", EncodeObject(id))
		out = append(out, e)
	}
	return out, ""
}

func (c *Conn) readDecouplers(parts uint64) ([]DecouplerInfo, string) {
	ids, err := c.callObjectList("SpaceCenter", "Parts_get_Decouplers", EncodeObject(parts))
	if err != nil {
		return nil, err.Error()
	}
	out := make([]DecouplerInfo, 0, len(ids))
	for _, id := range ids {
		part, err := c.callObject("SpaceCenter", "Decoupler_get_Part", EncodeObject(id))
		if err != nil {
			return out, err.Error()
		}
		stage, decoupleStage, title, err := c.partStage(part)
		if err != nil {
			return out, err.Error()
		}
		d := DecouplerInfo{Title: title, Stage: stage, DecoupleStage: decoupleStage}
		d.Staged, _ = c.callBool("SpaceCenter", "Decoupler_get_Staged", EncodeObject(id))
		out = append(out, d)
	}
	return out, ""
}
