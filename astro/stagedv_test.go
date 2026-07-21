package astro

import (
	"math"
	"testing"
)

// TestStageDeltaVs_SingleStage — one part, one engine, the plain rocket equation.
// dv = 300 * 9.80665 * ln(10000/5000) = 2039.6 m/s.
func TestStageDeltaVs_SingleStage(t *testing.T) {
	parts := []DVPart{{DecoupleStage: -1, WetMass: 10000, DryMass: 5000}}
	engines := []DVEngine{{ActivationStage: 0, DecoupleStage: -1, VacuumIsp: 300, VacuumThrust: 200000}}
	stages := StageDeltaVs(parts, engines)
	if len(stages) != 1 {
		t.Fatalf("want 1 stage, got %d: %+v", len(stages), stages)
	}
	want := 300 * G0 * math.Log(10000.0/5000)
	if math.Abs(stages[0].DeltaVMS-want) > 0.5 {
		t.Fatalf("stage 0 dv = %.1f, want %.1f", stages[0].DeltaVMS, want)
	}
	if math.Abs(TotalDeltaV(stages)-want) > 0.5 {
		t.Fatalf("total dv = %.1f, want %.1f", TotalDeltaV(stages), want)
	}
}

// TestStageDeltaVs_TwoStageSerial — the hand-computed serial two-stage anchor.
//
//	Lower stage: engine+tank, activation 1, decouple 0, wet 15000, dry 4000, Isp 290, thrust 200k.
//	Upper stage: engine+tank+pod, activation 0, decouple -1, wet 5000, dry 3000, Isp 340, thrust 60k.
//
// Stage 1 (lower fires, full stack): start 20000, burn 11000 → end 9000.
//	dv1 = 290 * 9.80665 * ln(20000/9000) = 2271.6 m/s
// Stage 0 (upper fires, lower shed): start 5000, burn 2000 → end 3000.
//	dv0 = 340 * 9.80665 * ln(5000/3000) = 1703.4 m/s
// Total ≈ 3975 m/s.
func TestStageDeltaVs_TwoStageSerial(t *testing.T) {
	parts := []DVPart{
		{DecoupleStage: 0, WetMass: 15000, DryMass: 4000},   // lower stage, shed at stage 0
		{DecoupleStage: -1, WetMass: 5000, DryMass: 3000},   // upper stage + pod, never shed
	}
	engines := []DVEngine{
		{ActivationStage: 1, DecoupleStage: 0, VacuumIsp: 290, VacuumThrust: 200000},
		{ActivationStage: 0, DecoupleStage: -1, VacuumIsp: 340, VacuumThrust: 60000},
	}
	stages := StageDeltaVs(parts, engines)
	if len(stages) != 2 {
		t.Fatalf("want 2 stages, got %d: %+v", len(stages), stages)
	}
	// Highest stage first.
	if stages[0].Stage != 1 || stages[1].Stage != 0 {
		t.Fatalf("stage order = [%d,%d], want [1,0]", stages[0].Stage, stages[1].Stage)
	}
	dv1 := 290 * G0 * math.Log(20000.0/9000)
	dv0 := 340 * G0 * math.Log(5000.0/3000)
	if math.Abs(stages[0].DeltaVMS-dv1) > 0.5 {
		t.Errorf("stage 1 dv = %.1f, want %.1f", stages[0].DeltaVMS, dv1)
	}
	if math.Abs(stages[1].DeltaVMS-dv0) > 0.5 {
		t.Errorf("stage 0 dv = %.1f, want %.1f", stages[1].DeltaVMS, dv0)
	}
	// Stage 1 uses the LOWER engine's Isp (upper isn't lit yet); stage 0 the upper's.
	if math.Abs(stages[0].IspS-290) > 0.01 {
		t.Errorf("stage 1 Isp = %.2f, want 290 (lower engine only)", stages[0].IspS)
	}
	if math.Abs(stages[1].IspS-340) > 0.01 {
		t.Errorf("stage 0 Isp = %.2f, want 340 (upper engine only)", stages[1].IspS)
	}
	total := dv1 + dv0
	if math.Abs(TotalDeltaV(stages)-total) > 1.0 {
		t.Errorf("total dv = %.1f, want %.1f", TotalDeltaV(stages), total)
	}
}

// TestStageDeltaVs_ThrustWeightedIsp — two engines in the same stage with
// different Isp average by thrust, not count.
func TestStageDeltaVs_ThrustWeightedIsp(t *testing.T) {
	parts := []DVPart{{DecoupleStage: -1, WetMass: 20000, DryMass: 10000}}
	engines := []DVEngine{
		{ActivationStage: 0, DecoupleStage: -1, VacuumIsp: 300, VacuumThrust: 300000}, // 3/4 weight
		{ActivationStage: 0, DecoupleStage: -1, VacuumIsp: 340, VacuumThrust: 100000}, // 1/4 weight
	}
	stages := StageDeltaVs(parts, engines)
	if len(stages) != 1 {
		t.Fatalf("want 1 stage, got %d", len(stages))
	}
	wantIsp := (300*300000.0 + 340*100000) / 400000 // 310
	if math.Abs(stages[0].IspS-wantIsp) > 0.01 {
		t.Errorf("thrust-weighted Isp = %.2f, want %.2f", stages[0].IspS, wantIsp)
	}
}

// TestStageDeltaVs_DeadStageNoEngine — a spent drop tank with no engine contributes
// no delta-v and isn't emitted as a phantom stage, but its mass still counts.
func TestStageDeltaVs_DeadStageNoEngine(t *testing.T) {
	parts := []DVPart{
		{DecoupleStage: 2, WetMass: 3000, DryMass: 3000}, // empty tank shed at 2 (no fuel, no engine)
		{DecoupleStage: -1, WetMass: 8000, DryMass: 4000},
	}
	engines := []DVEngine{{ActivationStage: 0, DecoupleStage: -1, VacuumIsp: 320, VacuumThrust: 100000}}
	stages := StageDeltaVs(parts, engines)
	// Only the core stage produces delta-v; the empty drop tank yields no stage.
	got := 0
	for _, s := range stages {
		if s.DeltaVMS > 0 {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("want 1 delta-v-producing stage, got %d: %+v", got, stages)
	}
}
