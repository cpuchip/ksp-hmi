// Package astro is the pure orbital-mechanics core for ksp-mcp's burn-math tools.
// It has NO dependency on kRPC or the game: every function takes numbers in and
// returns numbers out, so it is unit-tested against textbook values
// (astro_test.go) independent of a live flight. The MCP layer reads the current
// state from kRPC, calls these, and formats the result for the CAPCOM.
//
// Conventions: SI units throughout — distances in meters (RADII from the body
// CENTER, not altitudes above sea level), speeds in m/s, mu (standard
// gravitational parameter) in m^3/s^2, angles in radians unless a name says
// otherwise, time in seconds. Every formula is cited in its function comment.
package astro

import "math"

// G0 is standard gravity (m/s^2), the constant that converts specific impulse in
// seconds to exhaust velocity in m/s (ve = Isp * g0).
const G0 = 9.80665

// CircularSpeed returns the speed of a circular orbit of radius r about a body of
// gravitational parameter mu:  v = sqrt(mu / r).  (Kepler / vis-viva with a = r.)
func CircularSpeed(mu, r float64) float64 {
	if mu <= 0 || r <= 0 {
		return 0
	}
	return math.Sqrt(mu / r)
}

// VisViva returns orbital speed at radius r on an orbit of semi-major axis a:
//
//	v = sqrt( mu * (2/r - 1/a) )
//
// This is the vis-viva equation (Bate, Mueller & White, "Fundamentals of
// Astrodynamics", eq. 1.5-2). For a circular orbit a == r and it reduces to
// CircularSpeed. Returns 0 for non-physical inputs (radius outside the orbit).
func VisViva(mu, r, a float64) float64 {
	if mu <= 0 || r <= 0 || a <= 0 {
		return 0
	}
	term := 2/r - 1/a
	if term <= 0 {
		return 0
	}
	return math.Sqrt(mu * term)
}

// Period returns the orbital period of a circular/elliptical orbit of semi-major
// axis a:  T = 2*pi*sqrt(a^3 / mu).  (Kepler's third law.)
func Period(mu, a float64) float64 {
	if mu <= 0 || a <= 0 {
		return 0
	}
	return 2 * math.Pi * math.Sqrt(a*a*a/mu)
}

// MeanMotion returns the mean angular rate (rad/s) of a circular orbit of radius
// r:  n = sqrt(mu / r^3).
func MeanMotion(mu, r float64) float64 {
	if mu <= 0 || r <= 0 {
		return 0
	}
	return math.Sqrt(mu / (r * r * r))
}

// CircularizeResult is the delta-v to make the current (elliptical) orbit
// circular at each apsis. Positive dv means a prograde burn.
type CircularizeResult struct {
	AtApoapsisDV  float64 // m/s prograde at apoapsis -> raises periapsis to apoapsis
	AtPeriapsisDV float64 // m/s prograde at periapsis -> lowers apoapsis to periapsis (dv is signed)
	ApoapsisR     float64 // apoapsis radius from body center, m
	PeriapsisR    float64 // periapsis radius from body center, m
}

// Circularize computes the delta-v to circularize at apoapsis and at periapsis,
// given apoapsis and periapsis RADII (from the body center). The current orbit's
// semi-major axis is a = (rApo + rPer)/2. At each apsis, dv = v_circular - v_now.
//
//   - At apoapsis the circular speed exceeds the (slow) apoapsis speed, so dv > 0
//     (prograde) and it raises periapsis up to apoapsis.
//   - At periapsis the circular speed is below the (fast) periapsis speed, so
//     dv < 0 (retrograde) and it lowers apoapsis down to periapsis.
func Circularize(mu, rApo, rPer float64) CircularizeResult {
	a := (rApo + rPer) / 2
	res := CircularizeResult{ApoapsisR: rApo, PeriapsisR: rPer}
	res.AtApoapsisDV = CircularSpeed(mu, rApo) - VisViva(mu, rApo, a)
	res.AtPeriapsisDV = CircularSpeed(mu, rPer) - VisViva(mu, rPer, a)
	return res
}

// HohmannResult is a two-burn Hohmann transfer between two circular, coplanar
// orbits of radii r1 (start) and r2 (target).
type HohmannResult struct {
	DepartureDV   float64 // m/s at r1: prograde if raising (r2>r1), retrograde (negative) if lowering
	ArrivalDV     float64 // m/s at r2 to circularize into the target orbit (signed like DepartureDV)
	TotalDV       float64 // |DepartureDV| + |ArrivalDV|
	TransferTime  float64 // seconds on the transfer half-ellipse
	PhaseAngleRad float64 // required phase of target AHEAD of chaser at departure (radians)
	TransferSMA   float64 // semi-major axis of the transfer ellipse, m
}

// Hohmann computes the classic minimum-energy two-impulse transfer between two
// coplanar circular orbits (Bate/Mueller/White §3.4; Curtis "Orbital Mechanics"
// §6.2). r1, r2 are RADII from the body center.
//
//	a_t = (r1 + r2) / 2
//	dv1 = sqrt(mu/r1) * ( sqrt(2*r2/(r1+r2)) - 1 )
//	dv2 = sqrt(mu/r2) * ( 1 - sqrt(2*r1/(r1+r2)) )
//	t   = pi * sqrt(a_t^3 / mu)
//
// The required phase angle (how far ahead the target must be at the burn) is
//
//	alpha = pi - n_target * t = pi * ( 1 - (1/(2*sqrt2)) * sqrt((r1/r2 + 1)^3) )
//
// For a lowering transfer (r2 < r1) dv1 and dv2 come out negative (retrograde),
// which is physically correct.
func Hohmann(mu, r1, r2 float64) HohmannResult {
	res := HohmannResult{}
	if mu <= 0 || r1 <= 0 || r2 <= 0 {
		return res
	}
	at := (r1 + r2) / 2
	res.TransferSMA = at
	res.DepartureDV = VisViva(mu, r1, at) - CircularSpeed(mu, r1)
	res.ArrivalDV = CircularSpeed(mu, r2) - VisViva(mu, r2, at)
	res.TotalDV = math.Abs(res.DepartureDV) + math.Abs(res.ArrivalDV)
	res.TransferTime = math.Pi * math.Sqrt(at*at*at/mu)
	// Required phase angle: target angular travel during transfer is n2*t; the
	// target must start (pi - n2*t) ahead so it arrives at the rendezvous point.
	nTarget := MeanMotion(mu, r2)
	res.PhaseAngleRad = math.Pi - nTarget*res.TransferTime
	return res
}

// SynodicWait returns the time (seconds, >= 0) until the phase angle between a
// chaser (inner or outer) and its target next equals phiRequired, given the
// current phase phiCurrent (target-minus-chaser angle) and each body's mean
// motion. All angles in radians.
//
// The phase angle phi = theta_target - theta_chaser changes at d(phi)/dt =
// nTarget - nChaser. We advance to the next time phi == phiRequired (mod 2*pi),
// always returning a non-negative wait. If the two rates are equal (no relative
// motion) it returns +Inf.
func SynodicWait(phiCurrent, phiRequired, nChaser, nTarget float64) float64 {
	rate := nTarget - nChaser // d(phi)/dt
	if rate == 0 {
		return math.Inf(1)
	}
	// Smallest non-negative delta to bring phiCurrent to phiRequired along rate.
	delta := phiRequired - phiCurrent
	twoPi := 2 * math.Pi
	// Normalize delta/rate into a non-negative wait.
	t := delta / rate
	period := twoPi / math.Abs(rate) // synodic period
	for t < 0 {
		t += period
	}
	// Reduce to the first occurrence within one synodic period.
	for t >= period {
		t -= period
	}
	return t
}

// PlaneChangeDV returns the delta-v to rotate the velocity vector by an angle
// (the relative inclination, radians) at a point where orbital speed is v:
//
//	dv = 2 * v * sin(deltaInc / 2)
//
// (Simple plane-change / vector-triangle identity.) A pure plane change is
// cheapest where v is smallest — i.e. at apoapsis — so the caller passes the
// apoapsis speed for the cheapest node and the periapsis speed for the costliest.
func PlaneChangeDV(v, deltaIncRad float64) float64 {
	return 2 * v * math.Sin(math.Abs(deltaIncRad)/2)
}

// BurnTime approximates the burn duration and the half-burn lead for a given
// delta-v using the rocket equation with constant thrust:
//
//	t = (m0 * ve / F) * (1 - e^(-|dv|/ve)),   ve = Isp * g0
//
// Lead is t/2 — the "start the burn half its length before the node" rule that
// centers the impulse on the maneuver time. Returns ok=false (with a reason) when
// thrust or Isp is non-positive, since no estimate is possible.
func BurnTime(mass, thrust, isp, dv float64) (burn, lead float64, ok bool) {
	if mass <= 0 || thrust <= 0 || isp <= 0 {
		return 0, 0, false
	}
	ve := isp * G0
	t := (mass * ve / thrust) * (1 - math.Exp(-math.Abs(dv)/ve))
	return t, t / 2, true
}

// RocketEquationDV returns the ideal delta-v available from burning a vessel of
// wet mass m0 down to dry mass mf at exhaust velocity ve = Isp*g0
// (Tsiolkovsky):  dv = ve * ln(m0/mf).
func RocketEquationDV(isp, m0, mf float64) float64 {
	if isp <= 0 || m0 <= 0 || mf <= 0 || m0 < mf {
		return 0
	}
	return isp * G0 * math.Log(m0/mf)
}

// TWR returns the thrust-to-weight ratio for a given thrust (N), mass (kg), and
// surface gravity (m/s^2):  TWR = F / (m * g).
func TWR(thrust, mass, g float64) float64 {
	if mass <= 0 || g <= 0 {
		return 0
	}
	return thrust / (mass * g)
}

// DegToRad and RadToDeg are convenience conversions used by the MCP layer.
func DegToRad(d float64) float64 { return d * math.Pi / 180 }
func RadToDeg(r float64) float64 { return r * 180 / math.Pi }
