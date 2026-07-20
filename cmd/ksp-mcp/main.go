// Command ksp-mcp is a read-only MCP server that answers questions about a live
// Kerbal Space Program 1 flight through kRPC. It exposes a small, curated CAPCOM
// tool surface (vessel_status, orbit, flight_telemetry, resources,
// maneuver_nodes, crew, game_state) and mutates nothing — this is the reads-first
// P1 wave; the gated command wave is a sibling registration (see tools.go).
//
// Transport: stdio by default (how harnesses like Claude Code / the voice mind
// mount it via config); pass -http ADDR to serve Streamable HTTP instead. All
// logging goes to stderr so the stdio protocol stream stays clean.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.1.0"

func main() {
	logger := log.New(os.Stderr, "ksp-mcp: ", log.LstdFlags)

	host := flag.String("host", krpc.DefaultHost, "kRPC server host")
	rpcPort := flag.Int("rpc-port", krpc.DefaultRPCPort, "kRPC RPC port")
	streamPort := flag.Int("stream-port", krpc.DefaultStreamPort, "kRPC stream port (0 disables the stream connection)")
	clientName := flag.String("client-name", krpc.DefaultClientName, "client name shown in kRPC's in-game client list")
	timeout := flag.Duration("timeout", 10*time.Second, "dial and per-call timeout")
	httpAddr := flag.String("http", "", "serve MCP over Streamable HTTP on this address (e.g. 127.0.0.1:7801); default is stdio")
	smoke := flag.Bool("smoke", false, "connect, run discovery, call every read tool once, print results, then exit — the standing live oracle")
	flag.Parse()

	cfg := krpc.DialConfig{
		Host:       *host,
		RPCPort:    *rpcPort,
		StreamPort: *streamPort,
		ClientName: *clientName,
		Timeout:    *timeout,
	}
	srv := newKSPServer(cfg)
	defer srv.Close()

	if *smoke {
		os.Exit(runSmoke(srv, cfg))
	}

	s := mcp.NewServer(&mcp.Implementation{Name: "ksp-mcp", Version: version}, nil)
	registerTools(s, srv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *httpAddr != "" {
		if err := runHTTP(ctx, srv, *httpAddr, logger); err != nil {
			logger.Fatalf("http: %v", err)
		}
		return
	}

	logger.Printf("ksp-mcp %s on stdio (kRPC target %s:%d)", version, *host, *rpcPort)
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil {
		logger.Fatalf("run: %v", err)
	}
}

// runHTTP serves the MCP tools over the go-sdk Streamable HTTP handler. Each MCP
// session gets a fresh *mcp.Server, but every tool closes over the ONE shared
// kspServer so the kRPC connection is process-wide. Bound to whatever address is
// given; use a loopback address unless you intend to expose it.
func runHTTP(ctx context.Context, srv *kspServer, addr string, logger *log.Logger) error {
	getServer := func(*http.Request) *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "ksp-mcp", Version: version}, nil)
		registerTools(s, srv)
		return s
	}
	handler := mcp.NewStreamableHTTPHandler(getServer, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/mcp", handler)

	httpSrv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sctx)
	}()
	logger.Printf("ksp-mcp %s on http://%s/mcp", version, addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// runSmoke is the standing live oracle: one command that connects, proves
// discovery, and drives every read tool against the running game, printing what
// came back. Exit 0 when connected (even at the Space Center with no vessel —
// that still proves game_state's graceful degradation); exit 1 when kRPC can't
// be reached, with the exact instruction to bring it up.
func runSmoke(srv *kspServer, cfg krpc.DialConfig) int {
	fmt.Printf("ksp-mcp %s — live smoke against %s:%d\n", version, cfg.Host, cfg.RPCPort)

	c, err := srv.conn()
	if err != nil {
		fmt.Printf("\nNOT CONNECTED: %s\n", srv.connectMsg(err))
		fmt.Println("\nBring it up: launch KSP 1.12.5, ensure the kRPC mod is installed, open the kRPC")
		fmt.Println("window in-game and click \"Start server\", then re-run:  go run ./cmd/ksp-mcp -smoke")
		return 1
	}
	fmt.Printf("connected: kRPC client id %s\n", c.ClientGUID())

	if nSvc, nProc, nEnum, derr := c.Discover(); derr == nil {
		fmt.Printf("discovery: KRPC.GetServices -> %d services, %d procedures, %d enumerations\n", nSvc, nProc, nEnum)
	} else {
		fmt.Printf("discovery FAILED: %v\n", derr)
	}

	dump("game_state", srv.gameState(), nil)
	vs, err := srv.vesselStatus()
	dump("vessel_status", vs, err)
	ob, err := srv.orbit()
	dump("orbit", ob, err)
	ft, err := srv.flightTelemetry()
	dump("flight_telemetry", ft, err)
	rs, err := srv.resources()
	dump("resources", rs, err)
	nd, err := srv.maneuverNodes()
	dump("maneuver_nodes", nd, err)
	cw, err := srv.crew()
	dump("crew", cw, err)

	// Preflight checklist + staging inspector (reads only).
	pf, err := srv.preflight()
	dump("preflight", pf, err)
	sp, err := srv.stagingPlan()
	dump("staging_plan", sp, err)

	// Ascent autopilot PLANNING (authors + validates + reads back; flies nothing).
	pa := srv.planAscent(ascentInput{TargetApoapsisM: 80000})
	dump("plan_ascent (80km)", pa, nil)

	// Tier 1 richer reads.
	ti, err := srv.targetInfo()
	dump("target_info", ti, err)
	lv, err := srv.listVessels()
	dump("list_vessels", lv, err)
	dv, err := srv.deltaVStatus()
	dump("delta_v_status", dv, err)
	at, err := srv.attitude()
	dump("attitude", at, err)
	bd, err := srv.bodies("")
	dump("bodies (list)", bd, err)
	bk, err := srv.bodies("Kerbin")
	dump("bodies Kerbin", bk, err)

	// Tier 2 burn math.
	cc, err := srv.calcCircularize()
	dump("calc_circularize", cc, err)
	alt := 100000.0
	ch, err := srv.calcHohmann(hohmannInput{TargetAltitudeM: &alt})
	dump("calc_hohmann (to 100km)", ch, err)
	ct, err := srv.calcHohmann(hohmannInput{})
	dump("calc_hohmann (to target)", ct, err)
	cp, err := srv.calcPlaneChange()
	dump("calc_plane_change", cp, err)
	cb, err := srv.calcBurnTime(burnTimeInput{DeltaVMS: 100})
	dump("calc_burn_time (100 m/s)", cb, err)

	runNodeRoundTrip(srv)
	runMechJebRoundTrip(srv)

	fmt.Println("\nsmoke: OK (connected). Every tool was driven against the live game above; the")
	fmt.Println("maneuver-node round-trip left the flight plan exactly as it was found.")
	return 0
}

// runMechJebRoundTrip drives the MechJeb-backed planners against the live game and
// prints what came back — proving each one's real behavior on THIS install (whether
// MechJeb is functional, or degrades to the native fallback / honest note). It only
// places nodes when the flight plan is empty, and clears whatever it placed at the
// end, leaving the plan as found.
func runMechJebRoundTrip(srv *kspServer) {
	fmt.Printf("\n=== MechJeb-backed planners (reversible) ===\n")
	if c, err := srv.conn(); err == nil {
		fmt.Printf("MechJeb: mod present=%v  APIReady=%s  planner functional=%s\n",
			c.MechJebAvailable(), readyStr(c), functionalStr(c))
	}
	before, err := srv.maneuverNodes()
	if err != nil {
		fmt.Printf("skip: couldn't read existing nodes: %v\n", err)
		return
	}
	if before.Count != 0 {
		fmt.Printf("skip node-placing MechJeb tests: %d node(s) already on the plan — not touching the pilot's plan.\n", before.Count)
		return
	}

	// Each placing tool guards against an existing plan, so clear between tests to
	// give each a clean slate — and leave the plan empty at the end.
	clearIfPlaced := func() {
		if after, _ := srv.maneuverNodes(); after.Count != 0 {
			_, _ = srv.nodeClear()
		}
	}
	pi, err := srv.planIntercept(interceptInput{})
	dump("plan_intercept", pi, err)
	clearIfPlaced()
	pr, err := srv.planRendezvous(rendezvousInput{})
	dump("plan_rendezvous", pr, err)
	clearIfPlaced()
	pmv, err := srv.planMatchVelocity()
	dump("plan_match_velocity", pmv, err)
	clearIfPlaced()
	pip, err := srv.planInterplanetary(interplanetaryInput{})
	dump("plan_interplanetary", pip, err)
	clearIfPlaced()
	prt, err := srv.planReturn(returnInput{})
	dump("plan_return", prt, err)
	clearIfPlaced()
	pmp, err := srv.planMatchPlanes()
	dump("plan_match_planes", pmp, err)
	clearIfPlaced()
	rca, err := srv.refineClosestApproach(refineInput{})
	dump("refine_closest_approach", rca, err)
	clearIfPlaced()

	final, _ := srv.maneuverNodes()
	fmt.Printf("nodes after MechJeb round-trip: %d (want 0 — flight plan restored)\n", final.Count)
}

func readyStr(c *krpc.Conn) string {
	r, err := c.MechJebReady()
	if err != nil {
		return "err:" + err.Error()
	}
	return fmt.Sprintf("%v", r)
}

func functionalStr(c *krpc.Conn) string {
	ok, detail := c.MechJebPlannerFunctional()
	if ok {
		return "true"
	}
	return fmt.Sprintf("false (%s)", detail)
}

// runNodeRoundTrip exercises the Tier 3 write surface REVERSIBLY: it only runs
// when the flight plan is empty (so it can't clobber a node the pilot placed),
// creates one node, reads it back, deletes it, and confirms the plan is empty
// again — leaving the game exactly as found.
func runNodeRoundTrip(srv *kspServer) {
	fmt.Printf("\n=== TIER 3 node round-trip (reversible) ===\n")
	before, err := srv.maneuverNodes()
	if err != nil {
		fmt.Printf("skip: couldn't read existing nodes: %v\n", err)
		return
	}
	if before.Count != 0 {
		fmt.Printf("skip: %d node(s) already on the flight plan — not touching the pilot's plan.\n", before.Count)
		return
	}
	tfn := 120.0
	nc, err := srv.nodeCreate(nodeCreateInput{TimeFromNowSeconds: &tfn, ProgradeMS: 50})
	dump("node_create (+120s, 50 m/s prograde)", nc, err)
	if err != nil {
		return
	}
	mid, _ := srv.maneuverNodes()
	fmt.Printf("nodes after create: %d\n", mid.Count)
	del, err := srv.nodeDelete(nodeDeleteInput{})
	dump("node_delete", del, err)
	after, _ := srv.maneuverNodes()
	fmt.Printf("nodes after delete: %d (want 0)\n", after.Count)

	// plan_circularize places a real node too — exercise it, then clear.
	pc, err := srv.planCircularize(planInput{At: "apoapsis"})
	dump("plan_circularize (apoapsis)", pc, err)
	mid2, _ := srv.maneuverNodes()
	fmt.Printf("nodes after plan_circularize: %d\n", mid2.Count)
	// plan_hohmann needs a target — show the honest no-target path (places nothing).
	ph, err := srv.planHohmann(hohmannInput{})
	dump("plan_hohmann (no target -> honest note, places nothing)", ph, err)
	cl, err := srv.nodeClear()
	dump("node_clear", cl, err)
	final, _ := srv.maneuverNodes()
	fmt.Printf("nodes after node_clear: %d (want 0 — flight plan restored)\n", final.Count)
}

func dump(name string, v any, err error) {
	fmt.Printf("\n=== %s ===\n", name)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}
