package main

// stagedv.go — the stage_delta_v tool: per-stage delta-v for the active vessel,
// computed from the live part tree via the pure astro staging model. READ-ONLY.
// The model is a serial-staging estimate (it does not model fuel crossfeed /
// asparagus exactly), so the output is labeled "compare to the in-game readout"
// rather than claimed exact — the stock KSP delta-v display is the real oracle.

import (
	"github.com/cpuchip/ksp-hmi/astro"
)

type stageDVItem struct {
	Stage     int32   `json:"stage"`
	DeltaVMS  float64 `json:"delta_v_ms"`
	IspS      float64 `json:"isp_s,omitempty"`
	StartMass float64 `json:"start_mass_kg,omitempty"`
	EndMass   float64 `json:"end_mass_kg,omitempty"`
}

type stageDVOut struct {
	base
	TotalDeltaVMS float64       `json:"total_delta_v_ms"`
	Stages        []stageDVItem `json:"stages,omitempty"`
	Note          string        `json:"note"`
}

func (s *kspServer) stageDeltaV() (stageDVOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return stageDVOut{}, err
	}
	if !ok {
		return stageDVOut{base: b}, nil
	}
	parts, engines, err := c.StageMassProfile(vessel)
	if err != nil {
		s.drop()
		return stageDVOut{}, err
	}

	dvParts := make([]astro.DVPart, 0, len(parts))
	for _, p := range parts {
		dvParts = append(dvParts, astro.DVPart{
			DecoupleStage: int(p.DecoupleStage),
			WetMass:       p.WetMass,
			DryMass:       p.DryMass,
		})
	}
	dvEngines := make([]astro.DVEngine, 0, len(engines))
	for _, e := range engines {
		dvEngines = append(dvEngines, astro.DVEngine{
			ActivationStage: int(e.ActivationStage),
			DecoupleStage:   int(e.DecoupleStage),
			VacuumIsp:       e.VacuumIspS,
			VacuumThrust:    e.MaxVacuumThrustN,
		})
	}

	stages := astro.StageDeltaVs(dvParts, dvEngines)
	out := stageDVOut{
		base:          base{Available: true},
		TotalDeltaVMS: round2(astro.TotalDeltaV(stages)),
		Note: "Per-stage delta-v is a serial-staging estimate from the live part tree (vacuum Isp). " +
			"It matches KSP's in-game readout for standard rockets; fuel crossfeed / asparagus staging " +
			"can differ, so compare against the stock delta-v display. Values use current part masses, " +
			"so in flight this is REMAINING delta-v.",
	}
	for _, sd := range stages {
		item := stageDVItem{
			Stage:     int32(sd.Stage),
			DeltaVMS:  round2(sd.DeltaVMS),
			IspS:      round2(sd.IspS),
			StartMass: round2(sd.StartMass),
			EndMass:   round2(sd.EndMass),
		}
		out.Stages = append(out.Stages, item)
	}
	if len(out.Stages) == 0 {
		out.Note = "No staged engines/fuel found to compute delta-v from. " + out.Note
	}
	return out, nil
}
