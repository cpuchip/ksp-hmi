package astro

import "sort"

// stagedv.go — a per-stage delta-v model over a vessel's parts and engines.
// It is pure (plain-number inputs, no kRPC) so it is unit-tested against a
// hand-computed rocket, and the MCP layer converts live part reads into these
// inputs.
//
// The model is the standard "serial staging" approximation:
//
//   - KSP fires stages high-numbered → low. A part's DecoupleStage is when it is
//     shed; -1 means it never separates. Its fuel is consumed in the stage just
//     ABOVE its decouple (burnStage = DecoupleStage+1, or 0 for a never-shed
//     core), which is where its feeding engines are lit.
//   - For stage s: present parts are those not yet shed (DecoupleStage < s, or
//     never-shed); the start mass is their summed mass; the fuel burned this stage
//     is the fuel of parts whose burnStage == s; the effective Isp is the
//     thrust-weighted vacuum Isp of the engines ignited and not yet shed at s.
//   - delta_v(s) = Isp_eff * G0 * ln(startMass / (startMass - fuelBurned)).
//
// This matches KSP's own stage delta-v for standard serial rockets. It does NOT
// model fuel crossfeed or asparagus staging exactly (those share fuel across
// decouple boundaries), so the caller labels the result "compare to the in-game
// readout" rather than claiming exactness. Feeding CURRENT part masses yields
// remaining delta-v from the present state; feeding full (wet) masses yields the
// design total.

// DVPart is one part's contribution to the mass budget.
type DVPart struct {
	DecoupleStage int     // stage at which the part separates; -1 = never
	WetMass       float64 // current mass including remaining propellant (kg)
	DryMass       float64 // mass without propellant (kg)
}

// DVEngine is one engine's contribution to a stage's thrust and Isp.
type DVEngine struct {
	ActivationStage int     // stage at which it ignites; -1 = not staged
	DecoupleStage   int     // stage at which its part separates; -1 = never
	VacuumIsp       float64 // seconds
	VacuumThrust    float64 // N (thrust-weight for the Isp average)
}

// StageDV is the computed delta-v for one stage.
type StageDV struct {
	Stage     int     `json:"stage"`
	DeltaVMS  float64 `json:"delta_v_ms"`
	StartMass float64 `json:"start_mass_kg"`
	EndMass   float64 `json:"end_mass_kg"`
	IspS      float64 `json:"isp_s"`
	FuelMass  float64 `json:"fuel_mass_kg"`
	HasEngine bool    `json:"has_engine"`
}

// burnStage is the stage in which a part's propellant is consumed: the stage just
// above where the part is shed, or the final stage (0) for a never-shed core.
func burnStage(decoupleStage int) int {
	if decoupleStage < 0 {
		return 0
	}
	return decoupleStage + 1
}

// present reports whether a part/engine with the given decouple stage is still
// attached during stage s (not yet shed, or never shed).
func present(decoupleStage, s int) bool {
	return decoupleStage < 0 || decoupleStage < s
}

// StageDeltaVs computes per-stage delta-v, highest stage first (KSP firing order).
// Stages with neither an active engine nor any burnable fuel are omitted. The
// returned slice's DeltaVMS values sum to the vehicle's total delta-v under the
// model.
func StageDeltaVs(parts []DVPart, engines []DVEngine) []StageDV {
	maxStage := 0
	for _, p := range parts {
		if bs := burnStage(p.DecoupleStage); bs > maxStage {
			maxStage = bs
		}
	}
	for _, e := range engines {
		if e.ActivationStage > maxStage {
			maxStage = e.ActivationStage
		}
	}

	var out []StageDV
	for s := maxStage; s >= 0; s-- {
		var startMass, fuel float64
		for _, p := range parts {
			if present(p.DecoupleStage, s) {
				startMass += p.WetMass
			}
			if burnStage(p.DecoupleStage) == s {
				fuel += p.WetMass - p.DryMass
			}
		}

		var thrustSum, ispThrustSum float64
		hasEngine := false
		for _, e := range engines {
			if e.ActivationStage >= s && present(e.DecoupleStage, s) && e.VacuumThrust > 0 {
				thrustSum += e.VacuumThrust
				ispThrustSum += e.VacuumThrust * e.VacuumIsp
				hasEngine = true
			}
		}

		sd := StageDV{Stage: s, StartMass: startMass, FuelMass: fuel, HasEngine: hasEngine}
		if hasEngine && thrustSum > 0 && fuel > 1e-9 && startMass > 0 {
			endMass := startMass - fuel
			if endMass > 0 {
				sd.IspS = ispThrustSum / thrustSum
				sd.EndMass = endMass
				sd.DeltaVMS = RocketEquationDV(sd.IspS, startMass, endMass)
			}
		}
		if sd.HasEngine || sd.FuelMass > 1e-9 {
			out = append(out, sd)
		}
	}
	// Already high→low from the loop; keep it explicit and stable.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stage > out[j].Stage })
	return out
}

// TotalDeltaV sums the per-stage delta-v (m/s).
func TotalDeltaV(stages []StageDV) float64 {
	var total float64
	for _, s := range stages {
		total += s.DeltaVMS
	}
	return total
}
