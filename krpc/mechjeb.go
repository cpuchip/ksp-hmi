package krpc

// mechjeb.go — the KRPC.MechJeb (Genhis mod) write surface: it drives MechJeb's
// ManeuverPlanner to PLACE maneuver nodes for real intercepts, rendezvous, ejection
// burns, and returns. Sibling to nodes.go and, like it, the ONLY thing it mutates is
// the flight plan: every operation ends in MakeNodes, which draws node(s) on the
// navball and returns their SpaceCenter.Node ids. It NEVER touches MechJeb's
// NodeExecutor / autopilot / SmartASS / throttle / staging — no engine fires, nothing
// takes the stick. Removing a planned node (Node_Remove / Control_RemoveNodes in
// nodes.go) fully reverses anything here.
//
// The whole surface was discovered against the live game with cmd/ksp-dump
// (go run ./cmd/ksp-dump -service MechJeb) on 2026-07-19 — every procedure name,
// parameter type, and enum value below is copied from that real dump, not guessed.
//
// Pattern for every operation: fetch it off the active vessel's ManeuverPlanner
// (ManeuverPlanner_get_OperationX), set its properties (OperationX_set_Y and, where
// timing matters, its TimeSelector), call OperationX_MakeNodes to place the node(s),
// then read OperationX_get_ErrorMessage — MechJeb's own honest "why I couldn't"
// string, which is non-empty exactly when it placed nothing.

import "errors"

// TimeReference enum values (MechJeb.TimeReference). Verified via ksp-dump discovery
// against the live KRPC.MechJeb service on 2026-07-19.
const (
	TimeRefComputed        int32 = 0
	TimeRefXFromNow        int32 = 1
	TimeRefApoapsis        int32 = 2
	TimeRefPeriapsis       int32 = 3
	TimeRefAltitude        int32 = 4
	TimeRefEqAscending     int32 = 5
	TimeRefEqDescending    int32 = 6
	TimeRefRelAscending    int32 = 7
	TimeRefRelDescending   int32 = 8
	TimeRefClosestApproach int32 = 9
	TimeRefEqHighestAd     int32 = 10
	TimeRefEqNearestAd     int32 = 11
	TimeRefRelHighestAd    int32 = 12
	TimeRefRelNearestAd    int32 = 13
)

// MechJebAvailable reports whether the KRPC.MechJeb service is loaded in this kRPC
// install at all (the mod present). It reads the discovery catalogue, so it needs no
// round trip beyond the one GetServices every client already makes. False here is the
// signal for a tool to fall back to native math or return an honest "mod not
// installed" — it must NEVER crash on the son's machine, which may lack the mod.
func (c *Conn) MechJebAvailable() bool {
	c.ensureDiscovery()
	return c.procs["MechJeb.get_ManeuverPlanner"] != nil
}

// MechJebReady reports MechJeb.APIReady — true only when MechJeb is active on the
// ACTIVE vessel (it has a MechJeb-capable part / command module and has initialized).
// A vessel without MechJeb returns false here rather than throwing, so the tool can
// say so cleanly. This is distinct from MechJebAvailable (mod present at all).
func (c *Conn) MechJebReady() (bool, error) {
	return c.callBool("MechJeb", "get_APIReady")
}

// MechJebPlannerFunctional reports whether MechJeb's ManeuverPlanner can ACTUALLY
// place nodes on this install — the gate the tools trust, because APIReady lies.
//
// KRPC.MechJeb binds to MechJeb2's internals by reflection at load; when the two
// versions disagree (e.g. KRPC.MechJeb 0.7.1 against MechJeb2 2.15.3, verified on
// this machine 2026-07-19) that binding fails for the whole Operation hierarchy, yet
// MechJeb.InitInstance still sets APIReady=true after only logging the failure. The
// result: APIReady=true but every MakeNodes throws a bare NullReferenceException.
//
// This probe is side-effect-free: it reads OperationCircularize.ErrorMessage, whose
// getter dereferences the (possibly-null) reflection MethodInfo for
// MuMech.Operation.getErrorMessage. On a healthy install that returns a string; on a
// broken binding it throws — the reliable signal, with no node ever placed. The
// returned detail carries MechJeb's error text when broken (for an honest report).
func (c *Conn) MechJebPlannerFunctional() (ok bool, detail string) {
	planner, err := c.mjManeuverPlanner()
	if err != nil {
		return false, mjDetail(err)
	}
	op, err := c.callObject("MechJeb", "ManeuverPlanner_get_OperationCircularize", EncodeObject(planner))
	if err != nil {
		return false, mjDetail(err)
	}
	if _, err := c.mjErrorMessage("OperationCircularize", op); err != nil {
		return false, mjDetail(err)
	}
	return true, ""
}

func mjDetail(err error) string {
	if msg, ok := rpcErrDescription(err); ok {
		return msg
	}
	return err.Error()
}

// MechJebNodes is the result of a MechJeb planning operation: the placed node object
// ids (flight-plan order) and MechJeb's own error message. Error is non-empty exactly
// when MechJeb refused — in which case Nodes is empty and nothing was placed.
type MechJebNodes struct {
	Nodes []uint64
	Error string
}

// ---- primitives (each a single kRPC Call to a verified MechJeb procedure) ----

func (c *Conn) mjManeuverPlanner() (uint64, error) {
	return c.callObject("MechJeb", "get_ManeuverPlanner")
}

// mjOperation fetches an operation object off the active vessel's ManeuverPlanner.
// getter is a ManeuverPlanner getter name, e.g. "get_OperationTransfer".
func (c *Conn) mjOperation(getter string) (uint64, error) {
	planner, err := c.mjManeuverPlanner()
	if err != nil {
		return 0, err
	}
	return c.callObject("MechJeb", "ManeuverPlanner_"+getter, EncodeObject(planner))
}

func (c *Conn) mjSetDouble(class, prop string, op uint64, v float64) error {
	_, err := c.Call("MechJeb", class+"_set_"+prop, EncodeObject(op), EncodeDouble(v))
	return err
}

func (c *Conn) mjSetBool(class, prop string, op uint64, v bool) error {
	_, err := c.Call("MechJeb", class+"_set_"+prop, EncodeObject(op), EncodeBool(v))
	return err
}

func (c *Conn) mjErrorMessage(class string, op uint64) (string, error) {
	return c.callString("MechJeb", class+"_get_ErrorMessage", EncodeObject(op))
}

func (c *Conn) mjMakeNodes(class string, op uint64) ([]uint64, error) {
	return c.callObjectList("MechJeb", class+"_MakeNodes", EncodeObject(op))
}

// mjSetTimeReference sets an operation's TimeSelector to a given TimeReference (used
// for the operations whose timing we pin, e.g. KillRelVel at closest approach).
func (c *Conn) mjSetTimeReference(class string, op uint64, ref int32) error {
	ts, err := c.callObject("MechJeb", class+"_get_TimeSelector", EncodeObject(op))
	if err != nil {
		return err
	}
	_, err = c.Call("MechJeb", "TimeSelector_set_TimeReference", EncodeObject(ts), EncodeEnum(ref))
	return err
}

// planOp is the shared getter -> setup -> MakeNodes -> ErrorMessage sequence. A
// MechJeb operation reports failure two ways and this normalizes both into
// MechJebNodes.Error (never a hard Go error): it either throws (surfaced as an
// RPCError we unwrap to its description) or returns no nodes with ErrorMessage set.
func (c *Conn) planOp(getter, class string, setup func(op uint64) error) (MechJebNodes, error) {
	op, err := c.mjOperation(getter)
	if err != nil {
		if msg, ok := rpcErrDescription(err); ok {
			return MechJebNodes{Error: msg}, nil
		}
		return MechJebNodes{}, err
	}
	if setup != nil {
		if err := setup(op); err != nil {
			if msg, ok := rpcErrDescription(err); ok {
				return MechJebNodes{Error: msg}, nil
			}
			return MechJebNodes{}, err
		}
	}
	nodes, err := c.mjMakeNodes(class, op)
	if err != nil {
		if msg, ok := rpcErrDescription(err); ok {
			return MechJebNodes{Error: msg}, nil
		}
		return MechJebNodes{}, err
	}
	errMsg, _ := c.mjErrorMessage(class, op)
	return MechJebNodes{Nodes: nodes, Error: errMsg}, nil
}

// rpcErrDescription unwraps a MechJeb-thrown error into its human description so the
// tool can relay MechJeb's own reason instead of a raw protocol failure.
func rpcErrDescription(err error) (string, bool) {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		if rpcErr.e.Description != "" {
			return rpcErr.e.Description, true
		}
		if rpcErr.e.Name != "" {
			return rpcErr.e.Name, true
		}
		return "MechJeb rejected the maneuver", true
	}
	return "", false
}

// ---- the operations (one per planning tool) ----

// PlanTransfer places a Hohmann/optimized transfer to the current target
// (OperationTransfer — MechJeb's "Hohmann transfer to target"), timed for an
// intercept. simple=true is the fast coplanar transfer; simple=false runs MechJeb's
// optimizer for the best closest approach (handles inclination). interceptOnly=true
// omits the arrival capture burn (what you want before a KillRelVel rendezvous).
func (c *Conn) PlanTransfer(simple, interceptOnly bool) (MechJebNodes, error) {
	return c.planOp("get_OperationTransfer", "OperationTransfer", func(op uint64) error {
		if err := c.mjSetBool("OperationTransfer", "SimpleTransfer", op, simple); err != nil {
			return err
		}
		return c.mjSetBool("OperationTransfer", "InterceptOnly", op, interceptOnly)
	})
}

// PlanLambert places an arbitrary-intercept transfer (OperationLambert) with a fixed
// time-of-flight to the target, in seconds.
func (c *Conn) PlanLambert(interceptInterval float64) (MechJebNodes, error) {
	return c.planOp("get_OperationLambert", "OperationLambert", func(op uint64) error {
		return c.mjSetDouble("OperationLambert", "InterceptInterval", op, interceptInterval)
	})
}

// PlanKillRelVel places a match-velocity (kill relative velocity) node at closest
// approach to the target — the second burn of a rendezvous, and the standalone final
// approach.
func (c *Conn) PlanKillRelVel() (MechJebNodes, error) {
	return c.planOp("get_OperationKillRelVel", "OperationKillRelVel", func(op uint64) error {
		return c.mjSetTimeReference("OperationKillRelVel", op, TimeRefClosestApproach)
	})
}

// PlanInterplanetary places an interplanetary ejection burn to the current target
// planet (OperationInterplanetaryTransfer). waitForWindow=true plans at the next
// optimal phase-angle window; false ejects as soon as possible.
func (c *Conn) PlanInterplanetary(waitForWindow bool) (MechJebNodes, error) {
	return c.planOp("get_OperationInterplanetaryTransfer", "OperationInterplanetaryTransfer", func(op uint64) error {
		return c.mjSetBool("OperationInterplanetaryTransfer", "WaitForPhaseAngle", op, waitForWindow)
	})
}

// PlanMoonReturn places a burn to leave the current moon and return to its parent
// body, targeting the given return periapsis ALTITUDE (meters above the parent's sea
// level) — OperationMoonReturn.
func (c *Conn) PlanMoonReturn(returnAltitude float64) (MechJebNodes, error) {
	return c.planOp("get_OperationMoonReturn", "OperationMoonReturn", func(op uint64) error {
		return c.mjSetDouble("OperationMoonReturn", "MoonReturnAltitude", op, returnAltitude)
	})
}

// PlanPlane places a plane-match node at the cheaper of the relative
// ascending/descending nodes (OperationPlane) — MechJeb picks the node for you.
func (c *Conn) PlanPlane() (MechJebNodes, error) {
	return c.planOp("get_OperationPlane", "OperationPlane", nil)
}

// PlanCircularizeMJ places a circularization node via MechJeb (probe/diagnostic —
// needs no target, so it isolates target-reading NREs from planner-setup NREs).
func (c *Conn) PlanCircularizeMJ() (MechJebNodes, error) {
	return c.planOp("get_OperationCircularize", "OperationCircularize", nil)
}

// PlanCourseCorrection places a fine-tune-closest-approach correction node
// (OperationCourseCorrection), aiming for the given intercept distance (meters). This
// needs an existing intercept course toward the target to refine.
func (c *Conn) PlanCourseCorrection(interceptDistance float64) (MechJebNodes, error) {
	return c.planOp("get_OperationCourseCorrection", "OperationCourseCorrection", func(op uint64) error {
		return c.mjSetDouble("OperationCourseCorrection", "InterceptDistance", op, interceptDistance)
	})
}
