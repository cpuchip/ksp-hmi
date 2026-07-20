package main

// preflight.go — CAPCOM's go/no-go checklist and staging inspector. Both are
// READS ONLY: they summarize the part tree (parachutes, engines, decouplers,
// staging) and existing telemetry into a spoken-friendly checklist and a
// stage-by-stage sequence. Nothing here fires an engine, stages, or writes to the
// game — that stays in the gated command wave.
//
// The verdict and staging-plan LOGIC are pure functions (evaluatePreflight,
// buildStagingPlan) over already-read facts, so they are unit-tested without a
// live game. The tool methods do the kRPC reads and hand the facts to them.
//
// Design bias (honesty): the checklist flags only HIGH-CONFIDENCE conditions —
// empty power, a chute already deployed before launch, no chutes on a crewed
// craft. It deliberately does NOT guess at fuzzier "is the staging sensible"
// heuristics that can't be verified against a real craft; it reports the facts
// (each chute's stage + deploy altitude, the full staging sequence) and lets the
// pilot judge. Better a true fact than a false alarm.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cpuchip/ksp-hmi/astro"
	"github.com/cpuchip/ksp-hmi/krpc"
)

// checkItem is one line of the go/no-go checklist.
type checkItem struct {
	Item   string `json:"item"`
	Status string `json:"status"` // "go" | "note" | "no-go" | "info"
	Detail string `json:"detail"`
}

// preflightFacts is the already-read state evaluatePreflight reasons over — a
// plain value so the verdict logic is pure and testable.
type preflightFacts struct {
	Situation         string
	CrewCount         int
	CrewNames         []string
	ECAmount          float64
	ECMax             float64
	Engines           []krpc.EngineInfo
	Parachutes        []krpc.ParachuteInfo
	TotalParts        int
	CurrentStage      int32
	StageDVEstimateMS float64
	TWRFull           float64
	Body              string
}

// evaluatePreflight turns the read facts into a spoken checklist and an overall
// verdict (GO / GO WITH NOTES / NO-GO = the worst single line). Pure.
func evaluatePreflight(f preflightFacts) (items []checkItem, verdict string) {
	add := func(item, status, detail string) {
		items = append(items, checkItem{Item: item, Status: status, Detail: detail})
	}

	// Crew — informational; a probe with no crew is perfectly valid.
	if f.CrewCount > 0 {
		add("crew", "info", fmt.Sprintf("%d aboard: %s", f.CrewCount, strings.Join(f.CrewNames, ", ")))
	} else {
		add("crew", "info", "uncrewed (probe core)")
	}

	// Power.
	switch {
	case f.ECMax <= 0:
		add("power", "note", "no electric-charge capacity detected — confirm a battery or probe core is aboard")
	case f.ECAmount <= 0:
		add("power", "no-go", "electric charge is empty — the craft has no power")
	case f.ECAmount/f.ECMax < 0.15:
		add("power", "note", fmt.Sprintf("electric charge low (%.0f%%)", 100*f.ECAmount/f.ECMax))
	default:
		add("power", "go", fmt.Sprintf("electric charge %.0f%%", 100*f.ECAmount/f.ECMax))
	}

	// Engines.
	active := 0
	for _, e := range f.Engines {
		if e.Active {
			active++
		}
	}
	switch {
	case len(f.Engines) == 0:
		add("engines", "note", "no engines detected — fine for a station or payload, not for a launch")
	case active == 0:
		add("engines", "note", fmt.Sprintf("%d engine(s) aboard, none firing yet — staging ignites the first stage", len(f.Engines)))
	default:
		add("engines", "go", fmt.Sprintf("%d engine(s), %d firing", len(f.Engines), active))
	}

	// Parachutes.
	deployedEarly := 0
	for _, p := range f.Parachutes {
		if p.Deployed || equalFold(p.State, "Deployed") || equalFold(p.State, "SemiDeployed") {
			deployedEarly++
		}
	}
	switch {
	case len(f.Parachutes) == 0 && f.CrewCount > 0:
		add("parachutes", "note", "crewed craft with no parachutes — confirm a powered-landing plan for the crew")
	case len(f.Parachutes) == 0:
		add("parachutes", "note", "no parachutes — confirm a powered landing or a craft not meant to return")
	case deployedEarly > 0 && isPreLaunch(f.Situation):
		add("parachutes", "no-go", fmt.Sprintf("%d parachute(s) already deployed before launch", deployedEarly))
	default:
		add("parachutes", "go", parachuteSummary(f.Parachutes))
	}

	// Staging — informational count; the full sequence is the staging_plan tool.
	add("staging", "info", fmt.Sprintf("%d parts, current stage %d — ask for the staging plan for the full sequence", f.TotalParts, f.CurrentStage))

	// Delta-v floor (honest single-stage estimate, same figure as delta_v_status).
	if f.StageDVEstimateMS > 0 {
		add("delta-v", "info", fmt.Sprintf("~%.0f m/s single-stage floor (whole ship, vacuum Isp — a staged craft has more), TWR %.2f at %s",
			f.StageDVEstimateMS, f.TWRFull, f.Body))
	}

	verdict = "GO"
	for _, it := range items {
		switch it.Status {
		case "no-go":
			return items, "NO-GO"
		case "note":
			verdict = "GO WITH NOTES"
		}
	}
	return items, verdict
}

// parachuteSummary is a one-line spoken description of the chutes present.
func parachuteSummary(chutes []krpc.ParachuteInfo) string {
	armed := 0
	for _, p := range chutes {
		if p.Armed {
			armed++
		}
	}
	return fmt.Sprintf("%d parachute(s), %d armed", len(chutes), armed)
}

func isPreLaunch(situation string) bool { return equalFold(situation, "PreLaunch") }

// ---- staging plan (pure) ----

type stageStep struct {
	Stage      int32    `json:"stage"`
	Engines    []string `json:"engines_ignite,omitempty"`
	Decouplers []string `json:"decouplers_fire,omitempty"`
	Parachutes []string `json:"parachutes_deploy,omitempty"`
}

// buildStagingPlan groups engines/decouplers/parachutes by the staging index that
// activates each, newest (highest number) first — the order KSP fires them. Parts
// with stage -1 (activated manually / by action group, not staging) are collected
// separately so they aren't silently dropped. Pure.
func buildStagingPlan(engines []krpc.EngineInfo, decouplers []krpc.DecouplerInfo, parachutes []krpc.ParachuteInfo) (steps []stageStep, unstaged []string) {
	byStage := map[int32]*stageStep{}
	get := func(s int32) *stageStep {
		if byStage[s] == nil {
			byStage[s] = &stageStep{Stage: s}
		}
		return byStage[s]
	}
	for _, e := range engines {
		if e.Stage < 0 {
			unstaged = append(unstaged, "engine: "+e.Title)
			continue
		}
		st := get(e.Stage)
		st.Engines = append(st.Engines, e.Title)
	}
	for _, d := range decouplers {
		if d.Stage < 0 {
			unstaged = append(unstaged, "decoupler: "+d.Title)
			continue
		}
		st := get(d.Stage)
		st.Decouplers = append(st.Decouplers, d.Title)
	}
	for _, p := range parachutes {
		if p.Stage < 0 {
			unstaged = append(unstaged, "parachute: "+p.Title)
			continue
		}
		st := get(p.Stage)
		st.Parachutes = append(st.Parachutes, p.Title)
	}
	for _, st := range byStage {
		steps = append(steps, *st)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Stage > steps[j].Stage })
	return steps, unstaged
}

// ---- tool: preflight ----

type parachuteOut struct {
	Title           string  `json:"title"`
	Stage           int32   `json:"stage"`
	Armed           bool    `json:"armed"`
	State           string  `json:"state,omitempty"`
	DeployAltitudeM float64 `json:"deploy_altitude_m,omitempty"`
}

type preflightOut struct {
	base
	Verdict    string         `json:"verdict,omitempty"` // GO | GO WITH NOTES | NO-GO
	Situation  string         `json:"situation,omitempty"`
	Checklist  []checkItem    `json:"checklist,omitempty"`
	Parachutes []parachuteOut `json:"parachutes,omitempty"`
	Notes      []string       `json:"notes,omitempty"` // honest per-section read errors, if any
}

func (s *kspServer) preflight() (preflightOut, error) {
	fc, b, ok, err := s.flightContext()
	if err != nil {
		return preflightOut{}, err
	}
	if !ok {
		return preflightOut{base: b}, nil
	}

	vs, err := fc.c.VesselStatus(fc.vessel)
	if err != nil {
		s.drop()
		return preflightOut{}, err
	}
	names, err := fc.c.CrewMembers(fc.vessel)
	if err != nil {
		s.drop()
		return preflightOut{}, err
	}
	snap, err := fc.c.PartsSnapshotFor(fc.vessel)
	if err != nil {
		s.drop()
		return preflightOut{}, err
	}
	ri, err := fc.c.Resources(fc.vessel)
	if err != nil {
		s.drop()
		return preflightOut{}, err
	}
	ecAmt, ecMax := electricCharge(ri.Total)

	d, err := fc.c.DeltaVInputs(fc.vessel)
	if err != nil {
		s.drop()
		return preflightOut{}, err
	}
	g, err := fc.c.BodySurfaceGravity(fc.bodyID)
	if err != nil {
		s.drop()
		return preflightOut{}, err
	}

	facts := preflightFacts{
		Situation:         vs.Situation,
		CrewCount:         len(names),
		CrewNames:         names,
		ECAmount:          ecAmt,
		ECMax:             ecMax,
		Engines:           snap.Engines,
		Parachutes:        snap.Parachutes,
		TotalParts:        snap.TotalParts,
		CurrentStage:      snap.CurrentStage,
		StageDVEstimateMS: astro.RocketEquationDV(d.VacuumSpecificImpulse, d.Mass, d.DryMass),
		TWRFull:           astro.TWR(d.AvailableThrust, d.Mass, g),
		Body:              fc.body,
	}
	items, verdict := evaluatePreflight(facts)

	out := preflightOut{
		base:      base{Available: true},
		Verdict:   verdict,
		Situation: vs.Situation,
		Checklist: items,
		Notes:     partsNotes(snap),
	}
	for _, p := range snap.Parachutes {
		out.Parachutes = append(out.Parachutes, parachuteOut{
			Title:           p.Title,
			Stage:           p.Stage,
			Armed:           p.Armed,
			State:           p.State,
			DeployAltitudeM: round2(p.DeployAltitudeM),
		})
	}
	return out, nil
}

// ---- tool: staging_plan ----

type stagingPlanOut struct {
	base
	CurrentStage int32       `json:"current_stage"`
	TotalParts   int         `json:"total_parts"`
	Stages       []stageStep `json:"stages,omitempty"`
	NotStaged    []string    `json:"not_staged,omitempty"` // parts fired manually / by action group
	Notes        []string    `json:"notes,omitempty"`
}

func (s *kspServer) stagingPlan() (stagingPlanOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return stagingPlanOut{}, err
	}
	if !ok {
		return stagingPlanOut{base: b}, nil
	}
	snap, err := c.PartsSnapshotFor(vessel)
	if err != nil {
		s.drop()
		return stagingPlanOut{}, err
	}
	steps, unstaged := buildStagingPlan(snap.Engines, snap.Decouplers, snap.Parachutes)
	return stagingPlanOut{
		base:         base{Available: true},
		CurrentStage: snap.CurrentStage,
		TotalParts:   snap.TotalParts,
		Stages:       steps,
		NotStaged:    unstaged,
		Notes:        partsNotes(snap),
	}, nil
}

// electricCharge pulls the ElectricCharge amount and capacity out of the resource
// totals (0,0 if the craft carries none).
func electricCharge(totals []krpc.ResourceLevel) (amount, max float64) {
	for _, r := range totals {
		if equalFold(r.Name, "ElectricCharge") {
			return r.Amount, r.Max
		}
	}
	return 0, 0
}

// partsNotes surfaces any best-effort category read failures honestly, so a
// degraded section is visible rather than silently empty.
func partsNotes(snap krpc.PartsSnapshot) []string {
	var notes []string
	if snap.PartsErr != "" {
		notes = append(notes, "part count unavailable: "+snap.PartsErr)
	}
	if snap.ParachuteErr != "" {
		notes = append(notes, "parachute read incomplete: "+snap.ParachuteErr)
	}
	if snap.EngineErr != "" {
		notes = append(notes, "engine read incomplete: "+snap.EngineErr)
	}
	if snap.DecouplerErr != "" {
		notes = append(notes, "decoupler read incomplete: "+snap.DecouplerErr)
	}
	return notes
}
