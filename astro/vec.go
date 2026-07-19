package astro

import "math"

// Vec3 is a plain 3-vector used to turn kRPC's position/velocity/direction tuples
// into distances, closing rates, and angles. Pure math — no game dependency.
type Vec3 struct{ X, Y, Z float64 }

// V builds a Vec3 from a [3]float64 (the shape krpc.DecodeVector3 returns).
func V(a [3]float64) Vec3 { return Vec3{a[0], a[1], a[2]} }

func (a Vec3) Add(b Vec3) Vec3 { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3 { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Dot(b Vec3) float64 {
	return a.X*b.X + a.Y*b.Y + a.Z*b.Z
}
func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}
func (a Vec3) Norm() float64 { return math.Sqrt(a.Dot(a)) }

// Unit returns a as a unit vector, or the zero vector if a has no length.
func (a Vec3) Unit() Vec3 {
	n := a.Norm()
	if n == 0 {
		return Vec3{}
	}
	return Vec3{a.X / n, a.Y / n, a.Z / n}
}

// AngleBetween returns the unsigned angle (radians, 0..pi) between two vectors.
// Returns 0 if either has no length. Clamped to guard against acos domain error
// from floating-point dot products slightly outside [-1, 1].
func AngleBetween(a, b Vec3) float64 {
	na, nb := a.Norm(), b.Norm()
	if na == 0 || nb == 0 {
		return 0
	}
	c := a.Dot(b) / (na * nb)
	if c > 1 {
		c = 1
	} else if c < -1 {
		c = -1
	}
	return math.Acos(c)
}

// SignedAngleInPlane returns the angle from a to b (radians, -pi..pi) measured
// about the given plane normal — positive in the right-hand sense around normal.
// Used for phase angle: positive means b (target) is AHEAD of a (chaser) in the
// direction of orbital motion when normal is the orbital angular-momentum vector.
func SignedAngleInPlane(a, b, normal Vec3) float64 {
	unsigned := AngleBetween(a, b)
	// Sign from the triple product a x b . normal.
	if a.Cross(b).Dot(normal) < 0 {
		return -unsigned
	}
	return unsigned
}
