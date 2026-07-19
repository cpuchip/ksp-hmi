package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools wires the WHOLE surface: the original reads, the flight-computer
// reads (Tier 1), the burn math (Tier 2), and the maneuver-node planners (Tier 3,
// the only writes — reversible, nodes only). main.go (stdio) and the HTTP handler
// both call this so every transport exposes the same tools.
func registerTools(s *mcp.Server, srv *kspServer) {
	registerReadTools(s, srv)  // 7 original reads
	registerTier1Reads(s, srv) // 5 flight-computer reads
	registerMathTools(s, srv)  // 4 burn-math tools
	registerNodeTools(s, srv)  // 5 maneuver-node planners (writes, reversible)
}

// registerTier1Reads adds the richer read-only tools of the flight-computer wave.
func registerTier1Reads(s *mcp.Server, srv *kspServer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "target_info",
		Description: "Report the current target (vessel or celestial body) and the relative geometry the pilot " +
			"needs to rendezvous or intercept: straight-line DISTANCE and RELATIVE (closing) SPEED — both exact — " +
			"plus, when you share a primary body, the CLOSEST-APPROACH distance and time-to-closest, the PHASE " +
			"ANGLE (target ahead is positive), and the RELATIVE INCLINATION between your orbital planes. Use when " +
			"the pilot asks \"where's my target\", \"how far to the target\", \"when do we get closest\", \"are we " +
			"on the same plane\", or \"which way is it\". If no target is set it says so honestly.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, targetInfoOut, error) {
		out, err := srv.targetInfo()
		if err != nil {
			return toolError("target_info: %v", err), targetInfoOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_vessels",
		Description: "List every vessel in the game — name, type (Ship, Probe, Lander, Debris, EVA, Station, " +
			"Relay, Rover…), flight situation, the body it's at, and its distance from the active vessel — sorted " +
			"nearest first, with the active vessel flagged. Use when the pilot asks \"what else is up here\", " +
			"\"are there other ships nearby\", \"where's my station\", or to find something to set as a target.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listVesselsOut, error) {
		out, err := srv.listVessels()
		if err != nil {
			return toolError("list_vessels: %v", err), listVesselsOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delta_v_status",
		Description: "Report the vessel's performance: thrust-to-weight ratio (current throttle AND at full " +
			"throttle), current and available thrust, current and dry mass, current and vacuum specific impulse, " +
			"and a single-stage delta-v ESTIMATE (Tsiolkovsky, whole ship as one stage — a floor; multi-stage " +
			"craft have more). Use when the pilot asks \"what's my TWR\", \"can this thing get off the ground\", " +
			"\"how much delta-v do I have\", or before planning any burn. TWR uses the current body's gravity.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, deltaVStatusOut, error) {
		out, err := srv.deltaVStatus()
		if err != nil {
			return toolError("delta_v_status: %v", err), deltaVStatusOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "attitude",
		Description: "Report which way the vessel is pointing and how far off each navball marker it is: pitch, " +
			"heading, and roll, plus the ANGLE (degrees) between the nose and prograde, retrograde, normal, " +
			"anti-normal, radial-out, radial-in, and the target (if one is set), and which marker the nose is " +
			"nearest. Use when the pilot asks \"which way am I pointing\", \"am I on prograde\", \"how far off " +
			"retrograde am I\", or \"point me at the target\" — so you can say \"you're twelve degrees off prograde.\"",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, attitudeOut, error) {
		out, err := srv.attitude()
		if err != nil {
			return toolError("attitude: %v", err), attitudeOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "bodies",
		Description: "Report the key facts of a celestial body — equatorial radius, surface gravity, sphere of " +
			"influence, day length (rotational period), gravitational parameter, and atmosphere (yes/no and " +
			"height) — the numbers transfer and landing math need. Pass a body name (Kerbin, Mun, Minmus, Duna…); " +
			"leave it empty to list every body. Use when the pilot asks \"how big is the Mun\", \"does Duna have " +
			"an atmosphere\", \"what's Minmus's gravity\", or \"how long is a day here\".",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bodyInput) (*mcp.CallToolResult, bodiesOut, error) {
		out, err := srv.bodies(in.Name)
		if err != nil {
			return toolError("bodies: %v", err), bodiesOut{}, nil
		}
		return nil, out, nil
	})
}

// registerMathTools adds the Tier 2 burn-math tools. Each reads the current state
// and COMPUTES — none writes to the game.
func registerMathTools(s *mcp.Server, srv *kspServer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "calc_circularize",
		Description: "Compute (does NOT place) the delta-v to circularize the current orbit at apoapsis AND at " +
			"periapsis, with a burn-time estimate and the time to each apsis. Positive dv is prograde (at " +
			"apoapsis, raises periapsis); negative is retrograde (at periapsis, lowers apoapsis). Use when the " +
			"pilot asks \"how much to circularize\", \"what's my circularization burn\", or is about to round out " +
			"an orbit. To actually place the node, use plan_circularize.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, calcCircularizeOut, error) {
		out, err := srv.calcCircularize()
		if err != nil {
			return toolError("calc_circularize: %v", err), calcCircularizeOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "calc_hohmann",
		Description: "Compute (does NOT place) a Hohmann transfer: the departure and arrival delta-v, total " +
			"delta-v, transfer time, the required phase angle, and — when a real target is set in the same system " +
			"— the current phase angle and the TIME UNTIL the departure burn (the transfer window). Pass " +
			"target_altitude_m to transfer to a bare circular altitude, or omit it to transfer to the current " +
			"in-game target (vessel or moon). Use for \"how do I get to the Mun\", \"when's my transfer window\", " +
			"\"how much delta-v to raise to a hundred kilometers\". To place the departure node, use plan_hohmann.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in hohmannInput) (*mcp.CallToolResult, calcHohmannOut, error) {
		out, err := srv.calcHohmann(in)
		if err != nil {
			return toolError("calc_hohmann: %v", err), calcHohmannOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "calc_plane_change",
		Description: "Compute (does NOT place) the delta-v to match the current target's orbital PLANE — the " +
			"relative inclination and the burn cost at apoapsis (cheapest, since you're slowest there) and at " +
			"periapsis — and note that the burn goes at the ascending/descending node nearest apoapsis. Requires " +
			"a target with an orbit around the same body. Use when the pilot asks \"how much to match planes\", " +
			"\"why can't I catch it, we're in different planes\", or before a rendezvous plane-matching burn.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, calcPlaneChangeOut, error) {
		out, err := srv.calcPlaneChange()
		if err != nil {
			return toolError("calc_plane_change: %v", err), calcPlaneChangeOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "calc_burn_time",
		Description: "Given a delta-v (delta_v_ms), compute how long the burn will take with the vessel's current " +
			"mass, thrust, and Isp, and how many seconds BEFORE the node to start it (the half-burn lead that " +
			"centers the impulse). Use when the pilot asks \"how long is this burn\", \"when do I start burning " +
			"for a node\", or to translate any delta-v figure into a stopwatch.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in burnTimeInput) (*mcp.CallToolResult, calcBurnTimeOut, error) {
		out, err := srv.calcBurnTime(in)
		if err != nil {
			return toolError("calc_burn_time: %v", err), calcBurnTimeOut{}, nil
		}
		return nil, out, nil
	})
}

// registerNodeTools adds the Tier 3 maneuver-node planners. These are the ONLY
// tools that change the game, and they change ONLY the flight plan: they add or
// remove maneuver nodes drawn on the navball. They NEVER fire an engine, stage,
// touch SAS, or time-warp — every one is reversible (node_delete / node_clear).
func registerNodeTools(s *mcp.Server, srv *kspServer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "node_create",
		Description: "COMMAND (modifies the flight plan, reversible): add a maneuver node at a given time " +
			"(time_from_now_seconds or ut_seconds) with prograde/normal/radial burn components in m/s, and read " +
			"back the resulting predicted orbit. This PLANS a burn on the navball — it does NOT fire the engine " +
			"and changes nothing physical; undo it with node_delete or node_clear. Use only when the pilot asks " +
			"you to set up or place a specific maneuver.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nodeCreateInput) (*mcp.CallToolResult, nodeCreateOut, error) {
		out, err := srv.nodeCreate(in)
		if err != nil {
			return toolError("node_create: %v", err), nodeCreateOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "node_delete",
		Description: "COMMAND (modifies the flight plan, reversible): delete one maneuver node — by index (0 = " +
			"the next node) or, if no index is given, the last one — and report how many remain. Removes a planned " +
			"burn from the navball; nothing physical changes. Use when the pilot says \"scrap that node\", \"delete " +
			"the maneuver\", or wants to redo a plan.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nodeDeleteInput) (*mcp.CallToolResult, nodeDeleteOut, error) {
		out, err := srv.nodeDelete(in)
		if err != nil {
			return toolError("node_delete: %v", err), nodeDeleteOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "node_clear",
		Description: "COMMAND (modifies the flight plan): remove ALL maneuver nodes from the flight plan and " +
			"report how many were cleared. This wipes the planned burns off the navball — nothing physical " +
			"changes, but the whole plan is gone, so confirm with the pilot first. Use for \"clear the plan\", " +
			"\"delete all nodes\", \"start the plan over\".",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, nodeClearOut, error) {
		out, err := srv.nodeClear()
		if err != nil {
			return toolError("node_clear: %v", err), nodeClearOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_circularize",
		Description: "COMMAND (modifies the flight plan, reversible): compute the circularization delta-v AND " +
			"place the maneuver node — at apoapsis (default) or periapsis (pass at). Reads back the resulting " +
			"near-circular orbit. This draws the burn on the navball; it does NOT fire the engine. Undo with " +
			"node_delete/node_clear. Use when the pilot says \"set up my circularization burn\" or \"plan the " +
			"circularization at apoapsis.\"",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in planInput) (*mcp.CallToolResult, planCircularizeOut, error) {
		out, err := srv.planCircularize(in)
		if err != nil {
			return toolError("plan_circularize: %v", err), planCircularizeOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_hohmann",
		Description: "COMMAND (modifies the flight plan, reversible): compute a Hohmann transfer to the current " +
			"target AND place the departure node at the next transfer window, reading back the resulting orbit. " +
			"Needs a target vessel/moon in the same system so the burn can be timed. This draws the departure " +
			"burn on the navball; it does NOT fire the engine, and you'll still want an arrival/capture burn. " +
			"Undo with node_delete/node_clear. Use for \"plan my transfer to the Mun\" or \"set up the intercept.\"",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in hohmannInput) (*mcp.CallToolResult, planHohmannOut, error) {
		out, err := srv.planHohmann(in)
		if err != nil {
			return toolError("plan_hohmann: %v", err), planHohmannOut{}, nil
		}
		return nil, out, nil
	})
}
