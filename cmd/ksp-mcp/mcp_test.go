package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPRoundTrip drives the server through the SDK's real client + protocol
// (in-memory transport): initialize, tools/list, and two tools/call — proving
// the MCP layer end-to-end, not just that it compiles. kRPC is pointed at a dead
// port so the calls exercise graceful degradation (structured Available:false,
// never a protocol error).
func TestMCPRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newKSPServer(krpc.DialConfig{Host: "127.0.0.1", RPCPort: 59322, Timeout: 300 * time.Millisecond})
	defer srv.Close()

	s := mcp.NewServer(&mcp.Implementation{Name: "ksp-mcp", Version: version}, nil)
	registerTools(s, srv, false) // default surface: flight-control tools OFF

	serverT, clientT := mcp.NewInMemoryTransports()
	go func() { _ = s.Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	// tools/list — the whole surface: 7 original reads + 2 preflight reads +
	// 5 Tier-1 reads + 1 stage-dV read + 4 burn-math + 1 ascent planner + 5 native
	// maneuver-node planners + 7 MechJeb-backed planners = 32 tools, each described.
	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		// reads
		"vessel_status": false, "orbit": false, "flight_telemetry": false,
		"resources": false, "maneuver_nodes": false, "crew": false, "game_state": false,
		// preflight checklist + staging inspector (reads only)
		"preflight": false, "staging_plan": false,
		// Tier 1
		"target_info": false, "list_vessels": false, "delta_v_status": false,
		"attitude": false, "bodies": false,
		// Tier 2
		"calc_circularize": false, "calc_hohmann": false, "calc_plane_change": false,
		"calc_burn_time": false,
		// per-stage delta-v (read-only)
		"stage_delta_v": false,
		// ascent planner (plans/validates/reads back only — flies nothing)
		"plan_ascent": false,
		// Tier 3 native (writes: maneuver nodes only)
		"node_create": false, "node_delete": false, "node_clear": false,
		"plan_circularize": false, "plan_hohmann": false,
		// Tier 3 MechJeb-backed (writes: maneuver nodes only)
		"plan_intercept": false, "plan_rendezvous": false, "plan_match_velocity": false,
		"plan_interplanetary": false, "plan_return": false, "plan_match_planes": false,
		"refine_closest_approach": false,
	}
	for _, tl := range lt.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
		if tl.Description == "" {
			t.Errorf("tool %s has no description", tl.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q missing from tools/list", name)
		}
	}
	if len(lt.Tools) != len(want) {
		t.Errorf("tools/list returned %d tools, want %d", len(lt.Tools), len(want))
	}

	// game_state — never an error; structured result reports disconnected.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "game_state", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool game_state: %v", err)
	}
	if res.IsError {
		t.Error("game_state returned IsError; want a graceful structured result")
	}
	if got := structField(t, res.StructuredContent, "krpc_connected"); got != false {
		t.Errorf("game_state krpc_connected = %v, want false", got)
	}

	// vessel_status — graceful degrade, Available:false, not IsError.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "vessel_status", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool vessel_status: %v", err)
	}
	if res.IsError {
		t.Error("vessel_status returned IsError; want graceful degrade")
	}
	if got := structField(t, res.StructuredContent, "available"); got != false {
		t.Errorf("vessel_status available = %v, want false", got)
	}
}

// structField marshals the structured content and reads one top-level field.
func structField(t *testing.T, sc any, key string) any {
	t.Helper()
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	return m[key]
}
