# ksp-hmi roadmap

**Target platform: KSP1 (v1.12.5).** Everything below assumes the KSP1 mod ecosystem — kRPC v0.5.4,
MechJeb2, KRPC.MechJeb, Telemachus Reborn. KSP2 is not a target (dev ended 2024; see RESEARCH.md §7).

Sketched from the RESEARCH.md verdicts. Phases are value-ordered, not date-bound. Each phase has a
clear "done" you can see, and an oracle where one makes sense.

---

## P0 — Prove the loop today (ADOPT-only, zero new code)

**Goal:** On the son's machine, a spoken sentence flows to a local mind that reads live KSP1 telemetry
and answers / advises. No custom bridge yet — borrow an existing one to prove the pipe end-to-end.

- Install **kRPC** (v0.5.4) in KSP1 1.12.5; start the in-game server (`:50000`/`:50001`).
- Stand up an existing kRPC MCP against it — start with **GeePT_MCP** (richest: telemetry snapshot,
  blueprint, execute_script, docs search) or **MaiaJP krpc-mcp** (cleanest curated copilot surface).
  Note: MaiaJP isn't on PyPI despite its README — clone and run from source.
- Point our **ai-hmi-jumpstart** voice loop (Pipecat + local Whisper + Kokoro) at a **llama-chip**
  model as the mind, with the MCP wired in as tools.
- **Done when:** he says "what's my apoapsis?" / "am I in a stable orbit?" and hears a correct spoken
  answer read from live telemetry. Voice → local mind → kRPC → game → voice, fully local except the
  game itself.
- **Oracle:** a scripted telemetry question with a known answer (park a craft at a known orbit; the
  copilot must read it back within tolerance).

*This phase is deliberately throwaway on the MCP side — it exists to de-risk the voice+telemetry loop
before we invest in the Go build.*

---

## P1 — `ksp-mcp` in Go (the BUILD centerpiece)

**Goal:** Replace the borrowed Python MCP with our own **Go `ksp-mcp`** — a curated, confirm-gated
copilot tool surface we own, that composes with loom and our MCP tooling.

- **Backend decision first (empirical):** try **Telemachus HTTP/WebSocket JSON** for a fast, low-dep
  first cut; if the API surface is too thin, implement the **kRPC protobuf/TCP** client (handshake +
  length-prefixed `Request`/`Response` + argument encoding + `KRPC.GetServices` discovery). The
  `.proto` is small and self-describing; the Rust `krpc-client` crate proves a from-scratch client is
  very doable. Keep whichever we don't pick as a documented fallback backend.
- **Curated copilot toolset (~15 legible verbs), not the raw 200:** `get_flight_snapshot`,
  `get_orbit`, `get_resources`, `set_throttle`, `set_sas_mode`, `stage`, `add_node`, `execute_node`,
  plus MechJeb planners (`circularize`, `hohmann`, `land_at`, `rendezvous`) via **KRPC.MechJeb**.
  A `full` mode flag exposes the raw 1:1 API for debugging (borrowed from MaiaJP's two-tier design).
- **Confirm gate is a first-class primitive:** planning tools create maneuver nodes but **never burn**;
  execution tools require an explicit "go." Borrow caseys' `--no-execute` and GeePT's cancellable-job
  + telemetry-watch + auto-abort-on-vessel-loss safety pattern.
- **Fold in a docs tool** (borrowed from Ljove02/GeePT) so the mind never hallucinates a kRPC/MechJeb
  call.
- **Done when:** the P0 experience runs entirely on our Go server, with a working confirm gate, and
  `ksp-mcp` is registered as a normal MCP server for Claude Code / our voice mind / loom.
- **Oracle:** a deterministic "plan a 100 km circularization node but do not burn" test — node exists
  with correct Δv, throttle stays at zero until an explicit execute call.
- **License:** Michael's own client → Apache-2.0 or MIT (a network client of the LGPL/GPL kRPC mod is
  not a derivative work). Public repo.

### P1 status — reads wave built 2026-07-19

Backend chosen: **kRPC protobuf/TCP** (the real build). Telemachus HTTP/JSON stays documented as the
fallback backend but is not implemented.

Done — unit + MCP-protocol oracles green (`go build/vet/test -race`, all pass):

- [x] Go kRPC client (`krpc/`): RPC + stream handshakes, length-prefixed `Request`/`Response`,
      argument + return-value serialization, `KRPC.GetServices` self-discovery, stream subscriptions.
- [x] Wire codec verified **byte-for-byte** against kRPC's own reference client test vectors
      (goldens in `krpc/wire_test.go`); handshake/call/discovery/stream exercised against an
      in-process fake kRPC server (`krpc/client_test.go`, `krpc/stream_test.go`).
- [x] Return values decoded by their **declared** type from discovery (no float-vs-double guessing);
      enums resolved to names version-robustly.
- [x] `ksp-mcp` (`cmd/ksp-mcp/`): 7 read-only tools — `vessel_status`, `orbit`, `flight_telemetry`,
      `resources`, `maneuver_nodes` (reads existing nodes only), `crew`, `game_state`.
- [x] stdio transport (primary) + optional `-http` Streamable HTTP; graceful degradation when kRPC is
      down or not in flight (structured `available:false` + spoken message, never a hard error).
- [x] Full MCP round-trip verified through the real SDK (in-memory client + live stdio probe): 7 tools
      listed, `game_state`/`vessel_status` return graceful structured results.
- [x] Licensed Apache-2.0 (`LICENSE` + `NOTICE`); vendored `krpc.proto` kept under kRPC's terms.

Live-verify **pending** (needs KSP running with the kRPC server started):

- [ ] `go run ./cmd/ksp-mcp -smoke` — the standing live oracle. Connects, runs `GetServices` discovery,
      and drives **every** tool against the live game, printing the real outputs. Exits 1 with the exact
      bring-it-up instruction while kRPC is down (verified), 0 once it connects.

Deliberately **not** in this wave (reads-only ruling): command/confirm-gate tools
(`set_throttle`/`set_sas`/`stage`/`execute_node`), MechJeb planners, and the docs tool. The client's
`Call` layer + `Encode*` argument helpers and the `registerReadTools` registry are shaped so the gated
command wave slots in as a sibling `registerCommandTools` with no reshaping.

---

## P2 — The mission copilot: CAPCOM persona, loom seat, and physical panels

**Goal:** Turn the tool surface into a *copilot that flies a mission with you* — the engbman "Houston"
CAPCOM experience, but on our local stack, and eventually with real hardware.

- **CAPCOM persona** on the ai-hmi-jumpstart spoken-persona template: push-to-talk, mission-phase
  tracking (prelaunch → ascent → orbit → transfer → insertion → descent → landing → return), live
  **go-calls**, and **telemetry-verified milestones** — all borrowed from engbman's UX, all local
  (no OpenAI key, no per-mission cost).
- **loom mission-copilot seat:** a long-lived agent with memory + the `ksp-mcp` tools, so the copilot
  remembers the mission plan across the flight and can run a **confirm-gated** phase autonomously on
  the kid's "go." Borrow KOSMOS's **AuditAgent** idea — a second check that the mission is on-plan —
  as an independent validator.
- **ESP32 physical panel (the differentiator):** drive the **space-center ESP32-P4** hardware from
  live telemetry — real burn light, staging LEDs, gauges — and feed physical switches back as
  commands. kRPC speaks **serialio (Arduino)** natively; Kerlexa (2019) proved the concept. This is
  the lane nobody else is doing well, and it's the most fun for a nine-year-old commander.
- **(Optional) KSPDG eval harness:** adopt MIT-LL's **spacegym-kspdg** Gym environments to rigorously
  benchmark the copilot's maneuvering (pursuit-evasion, etc.). Requires the **Making History** DLC +
  PhysicsRangeExtender — heavier setup, so this is opt-in, not on the critical path.
- **Done when:** he flies a full Mun mission by voice, the copilot narrates and go-calls each phase,
  the physical panel lights track the flight, and the AI never takes the stick without his "go."

### P2 status — flight-computer wave built 2026-07-19

The tool surface went from 7 reads to **21 tools** — a real flight computer the CAPCOM can reason with.
`go build/vet/test -race` all green; textbook unit tests for the math; every tool live-driven against
the real game (see "P2 LIVE-VERIFIED" below).

Tier 1 — richer reads (read-only):
- [x] `target_info` — target (vessel/body) + relative geometry: distance, closing speed, closest-approach
      distance & time, phase angle, relative inclination (kRPC's own conic solver for closest approach).
- [x] `list_vessels` — every vessel, nearest-first, with type/situation/body/distance.
- [x] `delta_v_status` — TWR (current + full throttle), thrust, mass/dry mass, Isp, single-stage Δv estimate.
- [x] `attitude` — pitch/heading/roll + degrees off prograde/retrograde/normal/anti-normal/radial/target.
- [x] `bodies` — a body's radius, gravity, SOI, day length, μ, atmosphere (the transfer-math facts).

Tier 2 — burn math (pure `astro` package, textbook-tested, no game write):
- [x] `calc_circularize` — Δv at apoapsis & periapsis (vis-viva).
- [x] `calc_hohmann` — departure/arrival Δv, transfer time, required phase, and time-to-window vs a target.
- [x] `calc_plane_change` — Δv to match a target's plane, cheapest at apoapsis.
- [x] `calc_burn_time` — Δv → burn duration + half-burn lead (rocket equation).
- [x] Oracle: `astro/astro_test.go` asserts against textbook anchors — Kerbin 100 km circular = 2246 m/s,
      LEO(200 km)→GEO Hohmann ≈ 2454 + 1478 = 3932 m/s over ~5.26 h. Live `bodies Kerbin` returned μ and
      radius **identical** to those test constants — the math and the game agree.

Tier 3 — maneuver-node planning (the ONLY writes; reversible; nodes only, **never** fires an engine):
- [x] `node_create` / `node_delete` / `node_clear` — add/remove nodes (isolated in `krpc/nodes.go`).
- [x] `plan_circularize` — computes AND places a circularization node; live test produced a 961 m/940 m
      (near-circular) result orbit.
- [x] `plan_hohmann` — places the departure node for a transfer to the current target at the next window.
- [x] Each write tool's description is marked **COMMAND**, states it modifies the flight plan, and that it
      is reversible and fires nothing.

**Deliberately NOT built (Tier 4 — the later spoken go/no-go wave):** throttle, staging, SAS, time-warp,
node execution — anything that fires an engine or takes the stick. There is no such call anywhere in the
codebase; the mutating surface is one small file (`krpc/nodes.go`) that touches only maneuver nodes.

**MechJeb presence verdict — PRESENT; now WIRED for node-making (Tier 3), executor still deferred (Tier 4).**
Discovery shows the **`MechJeb` service is loaded** (KRPC.MechJeb / Genhis mod): a full `NodeExecutor`
(ExecuteOneNode/ExecuteAllNodes, lead-time, auto-warp) and the whole `ManeuverPlanner` operation set —
`OperationTransfer`, `OperationLambert`, `OperationKillRelVel`, `OperationInterplanetaryTransfer`,
`OperationMoonReturn`, `OperationPlane`, `OperationCourseCorrection`, `OperationCircularize`, and more, each
`MakeNode`/`MakeNodes` → `SpaceCenter.Node`. The MechJeb-backed **planning** tools (Tier 3, below) now drive
this to *make* nodes for real intercepts/rendezvous; the native `astro` math stays as the transparent,
mod-free teaching path and fallback. MechJeb's `NodeExecutor` (to *fly* the nodes) is **still deferred** to
the future Tier 4 command wave, behind the spoken go/no-go — placing a node never fires anything.

Field note — tool-search: with 28 tools the eager-load cost is ~3.3k tokens of descriptions (~5k with
schemas), light enough to keep `ENABLE_TOOL_SEARCH=false` in the seat (which also dodges the known
ToolSearch indexing bug for `--mcp-config` stdio servers, anthropics/claude-code #40314).

---

## P2.1 — MechJeb-backed intercept/rendezvous planners built 2026-07-19

The tool surface grew from 21 → **28**: seven MechJeb-backed planners that drive KRPC.MechJeb's
`ManeuverPlanner` for the hard problems our native Hohmann can't do well. Tier 3, writes, **nodes only** —
they place real maneuver node(s), read back the resulting orbit + total Δv + predicted closest approach, and
fire nothing. `go build/vet/test -race` all green.

New tools (`cmd/ksp-mcp/mechjeb.go`, client surface `krpc/mechjeb.go`):
- [x] `plan_intercept` — MechJeb `OperationTransfer` to the current target (intercept-timed). **Native
      fallback:** the textbook Hohmann departure node (`plan_hohmann`) when MechJeb is unavailable.
- [x] `plan_rendezvous` — two burns: transfer-to-intercept, then `OperationKillRelVel` at closest approach.
- [x] `plan_match_velocity` — `OperationKillRelVel` alone (the final-approach matching burn).
- [x] `plan_interplanetary` — `OperationInterplanetaryTransfer` ejection burn to a target planet.
- [x] `plan_return` — `OperationMoonReturn` (get home from a moon).
- [x] `plan_match_planes` — `OperationPlane` (MechJeb picks the cheaper AN/DN node).
- [x] `refine_closest_approach` — `OperationCourseCorrection` (MechJeb's fine-tune-closest-approach op; it
      exists, so no native iterator was needed).

**API discovered, not guessed.** `cmd/ksp-dump` (new) dumps the live MechJeb service — every procedure name,
parameter type, and enum value (`TimeReference`, etc.) came from `go run ./cmd/ksp-dump -service MechJeb`
against the running game. The KRPC.MechJeb workflow: fetch an operation off `ManeuverPlanner`, set its
properties (setters + `TimeSelector`), call `OperationX_MakeNodes` (places node(s)), then read
`OperationX_get_ErrorMessage` — MechJeb's honest failure string.

**Hard finding — verified on the real path (KSP.log), name it honestly:** on this dev machine the MechJeb
planner is **non-functional** because **KRPC.MechJeb 0.7.1 is incompatible with the installed MechJeb2
2.15.3**. KRPC.MechJeb binds to MechJeb2 internals by reflection at load; 97 bindings failed (log:
`[KRPC.MechJeb] MuMech.Operation.MakeNodesImpl() not found`, every `Operation*.timeSelector not found`, and
`Cannot initialize class ManeuverPlanner`). Worse, KRPC.MechJeb still sets **`APIReady = true`** after only
logging the failure, so APIReady is a **false positive** — every `MakeNodes` then throws a bare
`NullReferenceException`. The tools therefore do NOT trust APIReady; `MechJebPlannerFunctional()` runs a
side-effect-free probe (reads `OperationCircularize.ErrorMessage`, which NREs iff the binding is broken) and
degrades. **Fix (Michael's call — his actively-played install):** run **MechJeb2 2.14.3.0** (what
KRPC.MechJeb 0.7.1 targets) — the real planner path then lights up with **zero code change** (the tools
auto-detect functionality). No KRPC.MechJeb release supports MechJeb2 2.15.x yet.

**What that means for verification:** the MechJeb *happy path* is verified-by-code + confirmed against the
real live MechJeb API (proc/param/enum names all from the discovery dump), but could **not** be verified live
on this install (the version mismatch). What WAS verified live (via `-smoke`, active vessel "Minmus Rangers
Lander" orbiting Minmus, target "Minmus Rangers"): all seven tools ran on the real path and degraded
correctly — `plan_intercept`/`plan_rendezvous` placed a real **native** Hohmann node (6.02 m/s, result orbit
read back) and the other five returned honest "needs a compatible MechJeb" notes; the round-trip left the
flight plan empty (as found). Graceful degradation with kRPC down is unit-tested
(`TestMechJebToolsDegradeWhenDown`).

**Deliberately NOT built (still Tier 4):** MechJeb's `NodeExecutor` (ExecuteOneNode/ExecuteAllNodes, autowarp)
— the "go for the burn" that would *fly* the planned nodes. Placing nodes never fires anything; wiring the
executor is the future spoken go/no-go wave, not this one.

---

## Design invariants (hold across all phases)

- **KSP1 only.** Frozen platform = stable tooling. No chasing a moving target.
- **Local-first.** Mind (llama-chip) and voice (Whisper + Kokoro) run on our hardware. Cloud is a
  fallback, never the default.
- **Confirm-gated by default.** The copilot proposes; the commander (the kid) commits. Autonomy is
  opt-in per phase, loosened only with his buy-in. This is pedagogy, not just safety.
- **Build the oracle first.** Every autonomous maneuver gets a deterministic check before it's trusted.
- **Reuse our own stack** (ai-hmi-jumpstart, loom, llama-chip, space-center) over rebuilding.

## P1 LIVE-VERIFIED 2026-07-19
All 7 read tools driven against a real flight (Minmus Rangers, orbiting Minmus,
crew of 5, MET 6d18h). Discovery: 10 services / 1866 procedures / 49 enums.
Graceful degradation confirmed at Space Center (no vessel) AND full reads in Flight.
- **Field note — client-accept:** kRPC holds a new connection awaiting in-game approval
  unless "Auto-accept new clients" is checked; our handshake read timeout was too short
  for a human to click "Allow". FIX (P1.1): lengthen/config the handshake timeout so
  manual-accept works — matters on machines that don't enable auto-accept (e.g. the
  son's). Auto-accept + localhost is the recommended posture; documented in README.

## P2 LIVE-VERIFIED 2026-07-19
All **21 tools** driven against the real game via `-smoke` (extended to cover every new
tool plus a reversible Tier-3 node round-trip). Active vessel during the run was an EVA
kerbal (Bob) on a sub-orbital hop at Minmus — so some orbital scenarios were degenerate,
but every tool ran on the real path and returned honest values:
- **Reads:** `list_vessels` returned 20 real craft (Minmus Rangers Lander, station parts,
  debris) sorted by distance with correct types/bodies. `bodies Kerbin` returned radius
  600 000 m, μ 3.5316e12, SOI 84 159 286 m, day 5h59m, 70 km atmosphere — μ and radius
  **identical** to the astro test constants. `attitude` correctly read the kerbal pointing
  radial-out (0.1° off). `target_info` honestly reported "no target set."
- **Math:** `calc_hohmann` to a 100 km Minmus orbit → 71 + 46 = 117 m/s, 36m39s, phase 97°.
- **Tier 3 (reversible, left the flight plan exactly as found):** `node_create` (+120 s,
  50 m/s prograde) placed a node and read back the resulting orbit; `node_delete` restored
  count to 0; `plan_circularize` placed a 119 m/s node whose result orbit was **961 m / 940 m
  — near-circular** (the math works); `plan_hohmann` with no target honestly placed nothing;
  `node_clear` wiped the plan back to empty.
- **Graceful degradation** re-confirmed for all 14 new tools with kRPC down (unit test
  `TestNewToolsDegradeWhenDown`) — `Available:false`, never a hard error, and the Tier-3
  writes attempt no mutation when they can't even connect.
