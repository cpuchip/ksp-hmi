package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// noInput is the empty argument shape shared by every read-only tool: they all
// report on the active vessel / game and take no parameters.
type noInput struct{}

// registerReadTools wires the read-only copilot surface onto the server. This is
// the whole P1 tool set — reads only, nothing that mutates the game.
//
// The gated COMMAND wave slots in as a sibling registerCommandTools(s, srv)
// called right after this one; it will reuse the same kspServer connection and
// the same krpc.Conn.Call layer, add a go/no-go confirm gate, and register verbs
// like set_throttle / set_sas / stage / execute_node. Nothing here needs
// reshaping for it — that is the point of keeping reads isolated in this function.
func registerReadTools(s *mcp.Server, srv *kspServer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "vessel_status",
		Description: "Report the active vessel's name, flight situation (Orbiting, SubOrbital, Flying, " +
			"Landed, Splashed, PreLaunch, Docked, Escaping), the celestial body it's at, and mission " +
			"elapsed time (MET). Use when the pilot asks \"what's our status\", \"where are we\", \"what's " +
			"the mission clock\", or as the first read at the start of any exchange. If the game isn't in " +
			"flight it says so (available=false) rather than erroring — check game_state then.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, vesselStatusOut, error) {
		out, err := srv.vesselStatus()
		if err != nil {
			return toolError("vessel_status: %v", err), vesselStatusOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "orbit",
		Description: "Report the current orbit: apoapsis and periapsis ALTITUDE (meters above sea level), " +
			"eccentricity, inclination (degrees), orbital period, and time-to-apoapsis / time-to-periapsis " +
			"(seconds, plus a spoken form). Use when the pilot asks \"what's my apoapsis/periapsis\", \"are " +
			"we in a stable orbit\", \"how long to apoapsis\", or \"what's our inclination\". Altitudes are " +
			"above the body's sea level; apoapsis/periapsis from the body center are not reported here.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, orbitOut, error) {
		out, err := srv.orbit()
		if err != nil {
			return toolError("orbit: %v", err), orbitOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "flight_telemetry",
		Description: "Report live surface-relative flight data: altitude (mean sea level and above-terrain, " +
			"meters), vertical and horizontal speed (m/s), g-force, mach number (0 in vacuum), and attitude " +
			"— pitch, heading (0=north, 90=east), and roll in degrees. Use during ascent, descent, or " +
			"landing when the pilot asks \"how fast are we going\", \"what's our altitude\", \"what's our " +
			"heading/pitch\", or \"how many gees\". Values are in the vessel's surface reference frame.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, flightOut, error) {
		out, err := srv.flightTelemetry()
		if err != nil {
			return toolError("flight_telemetry: %v", err), flightOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "resources",
		Description: "Report propellant and power: for the whole vessel and (when available) the current " +
			"stage, each resource's current amount, capacity, and percent full — liquid fuel, oxidizer, " +
			"electric charge, monopropellant, and any others aboard. Use when the pilot asks \"how much fuel " +
			"do we have\", \"are we low on power\", \"what's left in this stage\", or before planning a burn. " +
			"Amounts are in KSP resource units.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, resourcesOut, error) {
		out, err := srv.resources()
		if err != nil {
			return toolError("resources: %v", err), resourcesOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "maneuver_nodes",
		Description: "Report EXISTING planned maneuver nodes (this tool never creates or edits them): each " +
			"node's delta-v and remaining delta-v (m/s), time until the node (seconds, plus spoken form), and " +
			"an APPROXIMATE burn duration from the rocket equation. Use when the pilot asks \"what's my next " +
			"burn\", \"how much delta-v is the maneuver\", \"how long until the node\", or \"how long is the " +
			"burn\". Returns count=0 with no nodes when none are planned. The burn estimate is a rough " +
			"single-stage figure, not a staging-aware plan.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, nodesOut, error) {
		out, err := srv.maneuverNodes()
		if err != nil {
			return toolError("maneuver_nodes: %v", err), nodesOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "crew",
		Description: "Report who is aboard the active vessel by name. Use when the pilot asks \"who's on " +
			"board\", \"is anyone crewing this\", or \"who's flying\". Returns count=0 for an uncrewed " +
			"(probe) craft.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, crewOut, error) {
		out, err := srv.crew()
		if err != nil {
			return toolError("crew: %v", err), crewOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "game_state",
		Description: "The honest \"can I even answer\" tool. Report whether kRPC is reachable, the kRPC " +
			"server version, the current game scene (Flight, SpaceCenter, TrackingStation, EditorVAB, " +
			"EditorSPH), whether the game is paused, and whether an active vessel exists. Use FIRST when any " +
			"other tool says it's unavailable, or when the pilot asks \"are you connected\", \"is the game " +
			"running\", or \"can you see the ship\". Never errors — it always returns a clear status, even " +
			"when kRPC is down or the game is at the Space Center with no craft in flight.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, gameStateOut, error) {
		return nil, srv.gameState(), nil
	})
}

// toolError builds a tool-execution error result (isError:true) — the model sees
// it and can react — distinct from a JSON-RPC protocol error. Used only for
// UNEXPECTED failures; "kRPC down" and "no vessel" are normal results carrying an
// Available:false + Message, so the CAPCOM relays them naturally.
func toolError(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
