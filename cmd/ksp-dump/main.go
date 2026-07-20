// Command ksp-dump is a discovery probe: it connects to a live kRPC server, calls
// KRPC.GetServices, and prints the full signature of one service's procedures,
// classes, and enumerations — parameter NAMES and TYPES, return types, defaults.
// It exists so we never GUESS a mod's API surface (KRPC.MechJeb in particular is
// fiddly): run it against the running game and read the real signatures.
//
//	go run ./cmd/ksp-dump -service MechJeb
//	go run ./cmd/ksp-dump -service MechJeb -grep Transfer
//	go run ./cmd/ksp-dump -list          # list every service + procedure count
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cpuchip/ksp-hmi/krpc"
	"github.com/cpuchip/ksp-hmi/krpc/pb"
)

func main() {
	host := flag.String("host", krpc.DefaultHost, "kRPC server host")
	rpcPort := flag.Int("rpc-port", krpc.DefaultRPCPort, "kRPC RPC port")
	service := flag.String("service", "MechJeb", "service to dump")
	grep := flag.String("grep", "", "only show procedures whose name contains this (case-insensitive)")
	list := flag.Bool("list", false, "list every service with its procedure/class/enum counts, then exit")
	mj := flag.Bool("mj", false, "live MechJeb probe: read active vessel/target + MechJeb readiness (read-only)")
	mjPlan := flag.String("mj-plan", "", "additionally ATTEMPT a MechJeb plan (transfer|killrelvel|plane|return|interplanetary) then CLEAN UP (only if the flight plan is empty)")
	flag.Parse()

	c, err := krpc.Dial(krpc.DialConfig{Host: *host, RPCPort: *rpcPort, StreamPort: 0})
	if err != nil {
		fmt.Printf("NOT CONNECTED: %v\n", err)
		fmt.Println("Bring up KSP + the kRPC server (Start server), then re-run.")
		os.Exit(1)
	}
	defer c.Close()

	if *mj {
		mjProbe(c, *mjPlan)
		return
	}

	svcs, err := c.Services()
	if err != nil {
		fmt.Printf("GetServices failed: %v\n", err)
		os.Exit(1)
	}

	if *list {
		names := make([]string, 0, len(svcs.Services))
		byName := map[string]*pb.Service{}
		for _, s := range svcs.Services {
			names = append(names, s.Name)
			byName[s.Name] = s
		}
		sort.Strings(names)
		for _, n := range names {
			s := byName[n]
			fmt.Printf("%-16s procedures=%-5d classes=%-4d enums=%d\n",
				s.Name, len(s.Procedures), len(s.Classes), len(s.Enumerations))
		}
		return
	}

	var target *pb.Service
	for _, s := range svcs.Services {
		if strings.EqualFold(s.Name, *service) {
			target = s
			break
		}
	}
	if target == nil {
		fmt.Printf("service %q not found. Services present:\n", *service)
		for _, s := range svcs.Services {
			fmt.Printf("  %s\n", s.Name)
		}
		os.Exit(1)
	}

	fmt.Printf("=== service %s: %d procedures, %d classes, %d enumerations ===\n",
		target.Name, len(target.Procedures), len(target.Classes), len(target.Enumerations))
	if target.Documentation != "" {
		fmt.Printf("doc: %s\n", oneLine(target.Documentation))
	}

	// Enumerations first — the time-selector enums the operations use.
	if len(target.Enumerations) > 0 {
		fmt.Printf("\n--- ENUMERATIONS ---\n")
		for _, e := range target.Enumerations {
			fmt.Printf("enum %s:\n", e.Name)
			for _, v := range e.Values {
				fmt.Printf("    %d = %s%s\n", v.Value, v.Name, doc(v.Documentation))
			}
		}
	}

	// Classes (names only; their members show up as ClassName_proc procedures).
	if len(target.Classes) > 0 {
		fmt.Printf("\n--- CLASSES ---\n")
		for _, cl := range target.Classes {
			fmt.Printf("class %s%s\n", cl.Name, doc(cl.Documentation))
		}
	}

	fmt.Printf("\n--- PROCEDURES ---\n")
	procs := make([]*pb.Procedure, len(target.Procedures))
	copy(procs, target.Procedures)
	sort.Slice(procs, func(i, j int) bool { return procs[i].Name < procs[j].Name })
	shown := 0
	for _, p := range procs {
		if *grep != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(*grep)) {
			continue
		}
		shown++
		fmt.Printf("\n%s(%s) -> %s\n", p.Name, params(p.Parameters), typeStr(p.ReturnType))
		if p.Documentation != "" {
			fmt.Printf("    // %s\n", oneLine(p.Documentation))
		}
		if p.Deprecated {
			fmt.Printf("    // DEPRECATED: %s\n", oneLine(p.DeprecatedReason))
		}
	}
	fmt.Printf("\n(%d procedures shown)\n", shown)
}

// mjProbe reads the live MechJeb-relevant state (active vessel, target, readiness,
// existing nodes) and optionally attempts one plan, cleaning up after itself. It is
// the real-path oracle for the MechJeb tools: it proves the API against the running
// game before we trust it. Writes happen ONLY when -mj-plan is given AND the flight
// plan is empty, and are removed again at the end.
func mjProbe(c *krpc.Conn, plan string) {
	fmt.Printf("=== live MechJeb probe ===\n")
	fmt.Printf("MechJeb mod loaded (service present): %v\n", c.MechJebAvailable())
	if ready, err := c.MechJebReady(); err == nil {
		fmt.Printf("MechJeb APIReady (active on this vessel): %v\n", ready)
	} else {
		fmt.Printf("MechJeb APIReady: read error: %v\n", err)
	}

	vessel, err := c.ActiveVessel()
	if err != nil {
		fmt.Printf("no active vessel: %v (can't probe planning here)\n", err)
		return
	}
	vs, _ := c.VesselStatus(vessel)
	if vs != nil {
		fmt.Printf("active vessel: %q  situation=%s  body=%s\n", vs.Name, vs.Situation, vs.Body)
	}

	tv, _ := c.TargetVessel()
	tb, _ := c.TargetBody()
	switch {
	case tv != 0:
		if b, err := c.VesselBrief(tv); err == nil {
			fmt.Printf("target: VESSEL %q (%s at %s)\n", b.Name, b.Type, b.Body)
		}
	case tb != 0:
		if n, err := c.BodyName(tb); err == nil {
			fmt.Printf("target: BODY %s\n", n)
		}
	default:
		fmt.Printf("target: none set\n")
	}

	// The load-bearing check: can MechJeb's planner ACTUALLY place nodes here?
	// APIReady (above) can be a false positive — KRPC.MechJeb sets it true even when
	// its reflection binding to the installed MechJeb2 failed, in which case every
	// MakeNodes throws. MechJebPlannerFunctional does a side-effect-free probe.
	if ok, detail := c.MechJebPlannerFunctional(); ok {
		fmt.Printf("MechJeb planner FUNCTIONAL — the MechJeb-backed plan tools will run.\n")
	} else {
		fmt.Printf("MechJeb planner NOT functional: %s\n", detail)
		fmt.Printf("  (this is the KRPC.MechJeb <-> MechJeb2 version mismatch; see KSP.log for\n")
		fmt.Printf("   '[KRPC.MechJeb] ... not found' — the plan tools will fall back to native.)\n")
	}

	control, err := c.VesselControl(vessel)
	if err != nil {
		fmt.Printf("can't read control: %v\n", err)
		return
	}
	existing, _ := c.ControlNodes(control)
	fmt.Printf("existing maneuver nodes: %d\n", len(existing))

	if plan == "" {
		fmt.Printf("\n(read-only probe; pass -mj-plan transfer|killrelvel|plane|return|interplanetary to attempt a plan)\n")
		return
	}
	if len(existing) != 0 {
		fmt.Printf("\nSKIP -mj-plan: %d node(s) already on the plan — not clobbering them.\n", len(existing))
		return
	}

	fmt.Printf("\n--- attempting MechJeb plan: %s ---\n", plan)
	var res krpc.MechJebNodes
	switch plan {
	case "transfer":
		res, err = c.PlanTransfer(false, false)
	case "transfer-simple":
		res, err = c.PlanTransfer(true, false)
	case "killrelvel":
		res, err = c.PlanKillRelVel()
	case "plane":
		res, err = c.PlanPlane()
	case "return":
		res, err = c.PlanMoonReturn(30000)
	case "interplanetary":
		res, err = c.PlanInterplanetary(true)
	case "circularize":
		res, err = c.PlanCircularizeMJ()
	default:
		fmt.Printf("unknown -mj-plan %q\n", plan)
		return
	}
	if err != nil {
		fmt.Printf("hard error: %v\n", err)
		return
	}
	if res.Error != "" {
		fmt.Printf("MechJeb declined: %q (no nodes placed)\n", res.Error)
		return
	}
	fmt.Printf("MechJeb placed %d node(s):\n", len(res.Nodes))
	for i, nid := range res.Nodes {
		nd, err := c.NodeDetail(nid)
		if err != nil {
			fmt.Printf("  node %d: read error %v\n", i, err)
			continue
		}
		fmt.Printf("  node %d: dv=%.1f m/s  (pro=%.1f nrm=%.1f rad=%.1f)  in %.0fs\n",
			i, nd.DeltaV, nd.Prograde, nd.Normal, nd.Radial, nd.TimeTo)
		if nd.OrbitID != 0 {
			if oe, err := c.OrbitElements(nd.OrbitID); err == nil {
				fmt.Printf("           result orbit: apo=%.0f m  peri=%.0f m  around %s\n",
					oe.ApoapsisAltitude, oe.PeriapsisAltitude, oe.Body)
			}
		}
	}
	// Closest approach of the last node's orbit to the target orbit (same-primary only).
	if len(res.Nodes) > 0 && tv != 0 {
		last := res.Nodes[len(res.Nodes)-1]
		if nd, err := c.NodeDetail(last); err == nil && nd.OrbitID != 0 {
			if torbit, err := c.VesselOrbitID(tv); err == nil {
				if dist, tut, err := c.OrbitClosestApproach(nd.OrbitID, torbit); err == nil {
					fmt.Printf("predicted closest approach to target: %.0f m at UT %.0f\n", dist, tut)
				} else {
					fmt.Printf("closest-approach read: %v (likely cross-SOI)\n", err)
				}
			}
		}
	}

	// CLEAN UP — restore the flight plan to empty (as found).
	if err := c.RemoveAllNodes(control); err != nil {
		fmt.Printf("WARNING: cleanup RemoveAllNodes failed: %v — REMOVE THE NODE(S) MANUALLY\n", err)
		return
	}
	after, _ := c.ControlNodes(control)
	fmt.Printf("cleaned up: %d node(s) remain (want 0 — flight plan restored)\n", len(after))
}

func params(ps []*pb.Parameter) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := fmt.Sprintf("%s %s", typeStr(p.Type), p.Name)
		if len(p.DefaultValue) > 0 {
			s += "=" + base64.StdEncoding.EncodeToString(p.DefaultValue)
		}
		if p.Nullable {
			s += "?"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// typeStr renders a kRPC Type: primitives by their code name, class/enum by
// Service.Name, collections with their element types.
func typeStr(t *pb.Type) string {
	if t == nil {
		return "void"
	}
	switch t.Code {
	case pb.Type_CLASS, pb.Type_ENUMERATION:
		kind := "class"
		if t.Code == pb.Type_ENUMERATION {
			kind = "enum"
		}
		return fmt.Sprintf("%s:%s.%s", kind, t.Service, t.Name)
	case pb.Type_LIST:
		return "List<" + elems(t.Types) + ">"
	case pb.Type_SET:
		return "Set<" + elems(t.Types) + ">"
	case pb.Type_TUPLE:
		return "Tuple<" + elems(t.Types) + ">"
	case pb.Type_DICTIONARY:
		return "Dict<" + elems(t.Types) + ">"
	default:
		if name, ok := pb.Type_TypeCode_name[int32(t.Code)]; ok {
			return name
		}
		return fmt.Sprintf("code(%d)", t.Code)
	}
}

func elems(ts []*pb.Type) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, typeStr(t))
	}
	return strings.Join(parts, ",")
}

func doc(s string) string {
	s = oneLine(s)
	if s == "" {
		return ""
	}
	return "  // " + s
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		s = s[:217] + "..."
	}
	return s
}
