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
	registerReadTools(s, srv)

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
		registerReadTools(s, srv)
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

	fmt.Println("\nsmoke: OK (connected). Every read tool was driven against the live game above.")
	return 0
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
