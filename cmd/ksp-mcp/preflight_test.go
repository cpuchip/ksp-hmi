package main

import (
	"testing"

	"github.com/cpuchip/ksp-hmi/krpc"
)

// verdictOf runs evaluatePreflight and returns just the verdict for terse asserts.
func verdictOf(f preflightFacts) string {
	_, v := evaluatePreflight(f)
	return v
}

// statusOf returns the status of the named checklist item ("" if absent).
func statusOf(items []checkItem, item string) string {
	for _, it := range items {
		if it.Item == item {
			return it.Status
		}
	}
	return ""
}

// nominalInFlight is a healthy crewed craft in orbit: the clean GO baseline.
func nominalInFlight() preflightFacts {
	return preflightFacts{
		Situation:         "Orbiting",
		CrewCount:         1,
		CrewNames:         []string{"Jeb"},
		ECAmount:          100,
		ECMax:             100,
		Engines:           []krpc.EngineInfo{{Title: "LV-909", Stage: 1, Active: true, HasFuel: true}},
		Parachutes:        []krpc.ParachuteInfo{{Title: "Mk16", Stage: 0, Armed: true, State: "Stowed"}},
		TotalParts:        12,
		CurrentStage:      1,
		StageDVEstimateMS: 1200,
		TWRFull:           1.8,
		Body:              "Kerbin",
	}
}

func TestEvaluatePreflight_NominalIsGo(t *testing.T) {
	if got := verdictOf(nominalInFlight()); got != "GO" {
		items, _ := evaluatePreflight(nominalInFlight())
		t.Fatalf("nominal in-flight craft: verdict = %q, want GO; checklist = %+v", got, items)
	}
}

func TestEvaluatePreflight_EmptyPowerIsNoGo(t *testing.T) {
	f := nominalInFlight()
	f.ECAmount = 0 // capacity present, but drained
	items, v := evaluatePreflight(f)
	if v != "NO-GO" {
		t.Fatalf("drained battery: verdict = %q, want NO-GO", v)
	}
	if s := statusOf(items, "power"); s != "no-go" {
		t.Fatalf("power line status = %q, want no-go", s)
	}
}

func TestEvaluatePreflight_DeployedChuteBeforeLaunchIsNoGo(t *testing.T) {
	f := nominalInFlight()
	f.Situation = "PreLaunch"
	f.Parachutes = []krpc.ParachuteInfo{{Title: "Mk16", Stage: 0, Deployed: true, State: "Deployed"}}
	if v := verdictOf(f); v != "NO-GO" {
		t.Fatalf("chute deployed on the pad: verdict = %q, want NO-GO", v)
	}
	// The same deployed chute in flight is NOT a no-go (that's a normal descent).
	f.Situation = "Flying"
	if v := verdictOf(f); v == "NO-GO" {
		t.Fatalf("chute deployed while flying should not be NO-GO, got %q", v)
	}
}

func TestEvaluatePreflight_NoParachutesOnCrewedCraftIsNote(t *testing.T) {
	f := nominalInFlight()
	f.Parachutes = nil
	items, v := evaluatePreflight(f)
	if v != "GO WITH NOTES" {
		t.Fatalf("crewed, no chutes: verdict = %q, want GO WITH NOTES", v)
	}
	if s := statusOf(items, "parachutes"); s != "note" {
		t.Fatalf("parachutes line status = %q, want note", s)
	}
}

func TestEvaluatePreflight_EnginesInactiveOnPadIsNote(t *testing.T) {
	f := nominalInFlight()
	f.Situation = "PreLaunch"
	f.Engines = []krpc.EngineInfo{{Title: "LV-T45", Stage: 2, Active: false, HasFuel: true}}
	items, v := evaluatePreflight(f)
	if v != "GO WITH NOTES" {
		t.Fatalf("engines idle on the pad: verdict = %q, want GO WITH NOTES", v)
	}
	if s := statusOf(items, "engines"); s != "note" {
		t.Fatalf("engines line status = %q, want note", s)
	}
}

func TestEvaluatePreflight_UncrewedNoChutesStillNoteNotNoGo(t *testing.T) {
	f := nominalInFlight()
	f.CrewCount = 0
	f.CrewNames = nil
	f.Parachutes = nil
	if v := verdictOf(f); v != "GO WITH NOTES" {
		t.Fatalf("uncrewed probe with no chutes: verdict = %q, want GO WITH NOTES", v)
	}
}

func TestBuildStagingPlan_GroupsAndOrders(t *testing.T) {
	engines := []krpc.EngineInfo{
		{Title: "LV-T45", Stage: 2},
		{Title: "LV-909", Stage: 0},
		{Title: "Vernor", Stage: -1}, // manual / action group
	}
	decouplers := []krpc.DecouplerInfo{
		{Title: "TR-18A", Stage: 1, DecoupleStage: 1, Staged: true},
	}
	parachutes := []krpc.ParachuteInfo{
		{Title: "Mk16", Stage: 0},
	}
	steps, unstaged := buildStagingPlan(engines, decouplers, parachutes)

	// Stages present: 2, 1, 0 — descending (KSP fires high to low).
	if len(steps) != 3 {
		t.Fatalf("want 3 stages, got %d: %+v", len(steps), steps)
	}
	wantOrder := []int32{2, 1, 0}
	for i, w := range wantOrder {
		if steps[i].Stage != w {
			t.Fatalf("stage[%d] = %d, want %d (descending order)", i, steps[i].Stage, w)
		}
	}
	// Stage 2 ignites LV-T45; stage 0 ignites LV-909 and deploys the chute.
	if len(steps[0].Engines) != 1 || steps[0].Engines[0] != "LV-T45" {
		t.Fatalf("stage 2 engines = %+v, want [LV-T45]", steps[0].Engines)
	}
	if len(steps[2].Engines) != 1 || len(steps[2].Parachutes) != 1 {
		t.Fatalf("stage 0 = %+v, want 1 engine + 1 parachute", steps[2])
	}
	if len(steps[1].Decouplers) != 1 || steps[1].Decouplers[0] != "TR-18A" {
		t.Fatalf("stage 1 decouplers = %+v, want [TR-18A]", steps[1].Decouplers)
	}
	// The stage -1 engine is collected as not-staged, not dropped.
	if len(unstaged) != 1 || unstaged[0] != "engine: Vernor" {
		t.Fatalf("unstaged = %+v, want [engine: Vernor]", unstaged)
	}
}

func TestBuildStagingPlan_Empty(t *testing.T) {
	steps, unstaged := buildStagingPlan(nil, nil, nil)
	if len(steps) != 0 || len(unstaged) != 0 {
		t.Fatalf("empty craft: steps=%+v unstaged=%+v, want both empty", steps, unstaged)
	}
}

func TestElectricCharge(t *testing.T) {
	totals := []krpc.ResourceLevel{
		{Name: "LiquidFuel", Amount: 90, Max: 100},
		{Name: "ElectricCharge", Amount: 45, Max: 50},
	}
	amt, max := electricCharge(totals)
	if amt != 45 || max != 50 {
		t.Fatalf("electricCharge = (%v,%v), want (45,50)", amt, max)
	}
	if amt, max := electricCharge(nil); amt != 0 || max != 0 {
		t.Fatalf("electricCharge(nil) = (%v,%v), want (0,0)", amt, max)
	}
}
