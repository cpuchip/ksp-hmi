package astro

import (
	"math"
	"testing"
)

// Physical constants used as textbook anchors.
const (
	muEarth  = 3.986004418e14 // m^3/s^2
	rEarth   = 6378000.0      // m
	geoR     = 42164000.0     // m (geostationary radius)
	muKerbin = 3.5316e12      // m^3/s^2 (KSP Kerbin)
	rKerbin  = 600000.0       // m (KSP Kerbin radius)
)

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f (±%.4f)", name, got, want, tol)
	}
}

// TestCircularSpeedKerbinLKO — the canonical KSP number every player knows: a
// 100 km circular orbit around Kerbin is ~2246 m/s.
func TestCircularSpeedKerbinLKO(t *testing.T) {
	v := CircularSpeed(muKerbin, rKerbin+100000)
	approx(t, "Kerbin 100km circular", v, 2246.1, 1.0)
}

// TestVisVivaReducesToCircular — vis-viva with a == r must equal CircularSpeed.
func TestVisVivaReducesToCircular(t *testing.T) {
	r := rKerbin + 100000
	if got, want := VisViva(muKerbin, r, r), CircularSpeed(muKerbin, r); math.Abs(got-want) > 1e-9 {
		t.Errorf("VisViva(a=r) = %v, want %v", got, want)
	}
}

// TestHohmannLEOtoGEO — the textbook LEO(200 km)→GEO transfer. Standard results:
// departure ≈ 2.45 km/s, arrival ≈ 1.48 km/s, total ≈ 3.93 km/s, half-ellipse
// time ≈ 5.26 h. (Curtis, Orbital Mechanics for Engineering Students, §6.2.)
func TestHohmannLEOtoGEO(t *testing.T) {
	r1 := rEarth + 200000
	h := Hohmann(muEarth, r1, geoR)
	approx(t, "LEO->GEO departure dv", h.DepartureDV, 2454.0, 15)
	approx(t, "LEO->GEO arrival dv", h.ArrivalDV, 1478.0, 15)
	approx(t, "LEO->GEO total dv", h.TotalDV, 3932.0, 25)
	approx(t, "LEO->GEO transfer time", h.TransferTime, 18925.0, 120) // ~5.26 h
	approx(t, "LEO->GEO transfer SMA", h.TransferSMA, (r1+geoR)/2, 1)
	// Raising transfer: both burns prograde (positive).
	if h.DepartureDV <= 0 || h.ArrivalDV <= 0 {
		t.Errorf("raising transfer should have positive burns: dep=%.1f arr=%.1f", h.DepartureDV, h.ArrivalDV)
	}
}

// TestHohmannLoweringIsRetrograde — a transfer to a LOWER orbit must produce
// negative (retrograde) burns; the magnitudes match the reverse raising transfer.
func TestHohmannLowering(t *testing.T) {
	up := Hohmann(muEarth, rEarth+200000, geoR)
	down := Hohmann(muEarth, geoR, rEarth+200000)
	if down.DepartureDV >= 0 || down.ArrivalDV >= 0 {
		t.Errorf("lowering transfer should be retrograde: dep=%.1f arr=%.1f", down.DepartureDV, down.ArrivalDV)
	}
	approx(t, "lowering total == raising total", down.TotalDV, up.TotalDV, 1e-6)
}

// TestCircularizeApsisSigns — at apoapsis circularization is a prograde (positive)
// burn that raises periapsis; at periapsis it is a retrograde (negative) burn.
// A circular orbit needs ~zero at both.
func TestCircularize(t *testing.T) {
	rApo := rKerbin + 100000
	rPer := rKerbin + 70000
	c := Circularize(muKerbin, rApo, rPer)
	if c.AtApoapsisDV <= 0 {
		t.Errorf("apoapsis circularize should be prograde, got %.2f", c.AtApoapsisDV)
	}
	if c.AtPeriapsisDV >= 0 {
		t.Errorf("periapsis circularize should be retrograde, got %.2f", c.AtPeriapsisDV)
	}
	// Already circular -> ~0 both apses.
	cc := Circularize(muKerbin, rApo, rApo)
	approx(t, "circular apo dv", cc.AtApoapsisDV, 0, 1e-6)
	approx(t, "circular peri dv", cc.AtPeriapsisDV, 0, 1e-6)
}

// TestPlaneChange — dv = 2 v sin(di/2). A 10° change at 2246 m/s ≈ 391.5 m/s.
func TestPlaneChange(t *testing.T) {
	dv := PlaneChangeDV(2246.1, DegToRad(10))
	approx(t, "10deg plane change at LKO", dv, 391.5, 0.5)
	// Zero inclination difference -> zero dv.
	approx(t, "0deg plane change", PlaneChangeDV(2246.1, 0), 0, 1e-9)
}

// TestBurnTimeAndLead — Tsiolkovsky constant-thrust burn: 10 t, 60 kN, 300 s Isp,
// 1000 m/s -> ~141 s, lead half of that. ok=false when thrust/Isp missing.
func TestBurnTime(t *testing.T) {
	burn, lead, ok := BurnTime(10000, 60000, 300, 1000)
	if !ok {
		t.Fatal("BurnTime ok=false for valid inputs")
	}
	approx(t, "burn duration", burn, 141.3, 0.5)
	approx(t, "burn lead", lead, burn/2, 1e-9)
	if _, _, ok := BurnTime(10000, 0, 300, 1000); ok {
		t.Error("BurnTime should be ok=false with zero thrust")
	}
}

// TestRocketEquationDV — dv = ve ln(m0/mf). 300 s Isp, 2:1 mass ratio -> 2039 m/s.
func TestRocketEquationDV(t *testing.T) {
	approx(t, "tsiolkovsky 2:1", RocketEquationDV(300, 10000, 5000), 2039.4, 0.5)
	if RocketEquationDV(300, 5000, 10000) != 0 {
		t.Error("mf > m0 should give 0")
	}
}

// TestTWR — F/(m g). 60 kN / (10 t * 9.81) ≈ 0.61.
func TestTWR(t *testing.T) {
	approx(t, "twr", TWR(60000, 10000, 9.81), 0.6116, 0.001)
}

// TestSynodicWait — a phase that must decrease by (2π−δ) at the relative rate
// returns the correct positive wait, and equal rates give +Inf.
func TestSynodicWait(t *testing.T) {
	// chaser faster (inner): rate = nTarget - nChaser < 0, phi decreases.
	w := SynodicWait(0, 1.0, 0.001, 0.0005)
	// phi must fall from 0 to 1.0 (i.e. to 1.0-2π) at 0.0005 rad/s.
	want := (2*math.Pi - 1.0) / 0.0005
	approx(t, "synodic wait", w, want, 1e-6)
	if !math.IsInf(SynodicWait(0, 1, 0.001, 0.001), 1) {
		t.Error("equal rates should give +Inf wait")
	}
	// Result always within one synodic period and non-negative.
	if w < 0 {
		t.Error("wait must be non-negative")
	}
}
