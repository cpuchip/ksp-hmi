# ksp-hmi

**A voice-driven, MCP-native mission copilot for Kerbal Space Program 1 — built on our own local stack, for flying rockets with my son.**

> **Target: KSP1 (v1.12.5).** That's what we play, and it's the stable, alive, thoroughly-modded
> platform. KSP2 development ended in 2024 — see RESEARCH.md for that context. This project does not
> target KSP2.

> **Getting started?** [**SETUP.md**](SETUP.md) is the front door — a fresh-machine path from
> installing the KSP mods to talking to CAPCOM, honest about what each tier needs. Start there.
> The simplest working setup (text CAPCOM in Claude Code / Claude Desktop, no voice stack) is a
> couple of minutes past the one build.

---

## The one-page vision

Kerbal Space Program (KSP1, v1.12.5 — stable, modded, alive) already has a mature, protobuf-over-TCP
bridge called **kRPC** that reads full telemetry out and feeds commands in. Autopilot primitives
(ascent, landing, rendezvous, node execution) already exist in **MechJeb2** and are already exposed
over kRPC. The plumbing is solved.

What is *not* solved is a **mature, maintained bridge that lets a local AI mind — talking through a
voice HMI — actually fly the mission with you.** The half-dozen KSP MCP servers that exist are all
solo, low-star, one of them isn't even on PyPI despite its README, and **none are in Go**. This is a
wide-open lane that lands exactly on top of assets we already own.

**The picture:**

```
  You (voice)  ──►  ai-hmi-jumpstart      ──►  the mind (llama-chip / any OpenAI-compat)
   "Houston,        (Pipecat + Whisper +          │  reads telemetry, plans the burn,
    raise my         Kokoro, all local)           │  asks you to confirm, then acts
    apoapsis                                       ▼
    to 100k"                                  ksp-mcp (Go)  ◄── our BUILD
                                                   │  curated, confirm-gated copilot tools
                                                   ▼
                                             kRPC mod (protobuf/TCP)  ──►  KSP1 + MechJeb2
                                                   ▲
                          (P2) ESP32 physical panel ┘  real switches, real gauges, real burn light
```

Voice in, telemetry-aware plan, **confirm gate**, autopilot executes, CAPCOM reads it back. A local
copilot that flies with a nine-year-old — not a cloud API that flies *for* him.

## Why this is ours to build

We are not starting from a blank page. We are assembling parts we already own and proved:

| Asset (ours, public) | Role in ksp-hmi |
|---|---|
| **ai-hmi-jumpstart** (Pipecat + Whisper + Kokoro + OpenAI-compat mind, MIT) | The voice front end. Push-to-talk, local STT/TTS, the spoken-persona template → CAPCOM. |
| **llama-chip** (local model fleet) | The mind. No OpenAI key, no per-mission cost, runs on our GPUs. |
| **loom** (Go multi-agent harness) | P2 host for a long-lived "mission copilot" seat with memory + tools. |
| **Go MCP-server experience** + `mcp-server-go` skill | The centerpiece: `ksp-mcp` in Go speaking kRPC's wire protocol. |
| **space-center ESP32-P4 panel** (bridge-sim hardware) | P2 physical console — kRPC natively speaks serialio/Arduino. |
| **the son's PC** running the starter kit | Where the P0 loop lives. He flies; the copilot advises. |

Every other KSP-AI project reaches for a cloud LLM and an OpenAI key. We already have the local voice
loop and the local mind. The differentiated thing we can build that nobody else has is **a local,
confirm-gated, voice-first copilot with a real physical panel — and a kid in the seat.**

## The father-son angle

This is the point, not a footnote. First Orbit, Kernel Panic, Little Farm Game — the pattern is games
built *with* his kids, where the kid's hand is on the design. KSP is the real-physics version: a nine-
year-old learns orbits by *doing* them, with a patient copilot that explains the burn, waits for his
"go," and never takes the stick without permission. The confirm gate isn't just safety engineering —
it's the whole pedagogy. The AI is the co-pilot; the kid is the commander.

## Scope discipline

- **KSP1 only.** KSP2 development ended (Intercept Games closed June 2024; kRPC's own KSP2 repo is
  archived). KSP1 at 1.12.5 is frozen — and a frozen target is a *gift* for tooling.
- **Local-first.** The mind and voice run on our hardware. Cloud is a fallback, never the default.
- **Confirm-gated by default.** The copilot proposes; the human commits. Autonomy is opt-in per phase.

See **RESEARCH.md** for the full field survey with citations and the adopt/borrow/build verdicts, and
**ROADMAP.md** for the P0/P1/P2 plan.

---

## Running `ksp-mcp` (P2 flight computer — 28 tools, built)

`ksp-mcp` is a Go MCP server that turns a live KSP1 flight over kRPC into a flight computer the CAPCOM can
reason with. Requires KSP 1.12.5 with the **kRPC** mod; open the kRPC window in-game and click **Start
server** (default ports `50000`/`50001`).

**Safety tiers.** Tiers 1–2 (reads + burn math) mutate nothing. Tier 3 (maneuver-node planning — both the
native planners and the MechJeb-backed intercept/rendezvous planners) is the only write surface and it writes
ONLY maneuver nodes — planned burns drawn on the navball, fully reversible; it **never** fires an engine,
stages, toggles SAS, or time-warps. Tier 4 (flight commands, incl. MechJeb's NodeExecutor "go for the burn")
is the later spoken go/no-go wave and is deliberately not built — there is no throttle/stage/SAS/warp/execute
call anywhere in the code.

```bash
go build ./...                 # build the client + server
go test ./...                  # unit + MCP-protocol oracles (no game needed)
go run ./cmd/ksp-mcp -smoke    # LIVE oracle: connect, discover, drive every tool, print outputs
```

`-smoke` is the standing live check: with the game up it connects, runs `KRPC.GetServices` discovery, and
calls every tool against the real flight; with the game down it prints exactly how to bring it up and
exits non-zero.

**Tools (28):**

- **Reads (Tier 1):** `vessel_status`, `orbit`, `flight_telemetry`, `resources`, `maneuver_nodes` (reads
  existing nodes), `crew`, `game_state` (the honest "can I even answer" tool), plus `target_info` (target +
  relative geometry: distance, closing speed, closest approach, phase angle, relative inclination),
  `list_vessels` (all craft, nearest first), `delta_v_status` (TWR, thrust, mass, Isp, Δv estimate),
  `attitude` (offsets from every navball marker), and `bodies` (radius/gravity/SOI/day/atmosphere).
- **Burn math (Tier 2, pure `astro` package, textbook-tested):** `calc_circularize`, `calc_hohmann`,
  `calc_plane_change`, `calc_burn_time`.
- **Native maneuver-node planning (Tier 3 — writes, reversible, nodes only):** `node_create`, `node_delete`,
  `node_clear`, `plan_circularize`, `plan_hohmann`. Each is marked a COMMAND, modifies only the flight plan,
  and fires nothing.
- **MechJeb-backed planning (Tier 3 — writes, reversible, nodes only):** `plan_intercept`, `plan_rendezvous`,
  `plan_match_velocity`, `plan_interplanetary`, `plan_return`, `plan_match_planes`, `refine_closest_approach`.
  These drive **KRPC.MechJeb**'s `ManeuverPlanner` for real intercepts/rendezvous and read back the resulting
  orbit, total Δv, and predicted closest approach. Each degrades gracefully: if the MechJeb mod is absent —
  or present but its version doesn't match the installed MechJeb2 (see below) — `plan_intercept` falls back to
  the native Hohmann transfer and the rest return an honest "needs a compatible MechJeb" answer. They never
  crash and never fabricate.

### MechJeb version compatibility (matters for the real planner path)

The MechJeb-backed tools need **KRPC.MechJeb** and **MechJeb2** at compatible versions. KRPC.MechJeb binds to
MechJeb2's internals by reflection at load; when the versions disagree, that binding fails for the whole
`Operation` hierarchy, and — importantly — KRPC.MechJeb still reports `APIReady = true` while every plan
throws a bare `NullReferenceException`. So the tools do **not** trust `APIReady`; they run a side-effect-free
functional probe (`OperationCircularize.ErrorMessage`) and fall back when it fails.

Verified on this repo's dev machine (2026-07-19): **MechJeb2 2.15.3 + KRPC.MechJeb 0.7.1 are incompatible**
(97 failed reflection bindings; `KSP.log` shows `[KRPC.MechJeb] MuMech.Operation.MakeNodesImpl() not found`,
etc.). KRPC.MechJeb 0.7.1 (its latest release, Dec 2024) targets **MechJeb2 2.14.3.0**. To light up the real
MechJeb planner path, run **MechJeb2 2.14.3.0** with KRPC.MechJeb 0.7.1 — no code change needed; the tools
detect functionality and switch automatically. Until then the native fallback carries `plan_intercept`.

`cmd/ksp-dump` is the discovery/diagnostic tool behind this: `go run ./cmd/ksp-dump -service MechJeb` dumps
the live MechJeb API (procedures, params, enums — never guessed), and `go run ./cmd/ksp-dump -mj` reports
whether MechJeb's planner is actually functional on the current install.

### Mounting it as a CAPCOM tool (stdio)

Build a binary (`go build -o ksp-mcp.exe ./cmd/ksp-mcp`) and register it with any MCP harness. For Claude
Code / the voice mind, in `.mcp.json`:

```json
{
  "mcpServers": {
    "ksp": {
      "type": "stdio",
      "command": "C:/path/to/ksp-hmi/ksp-mcp.exe",
      "args": []
    }
  }
}
```

Flags: `-host` / `-rpc-port` / `-stream-port` target a non-default kRPC server; `-http 127.0.0.1:7801`
serves Streamable HTTP on `/mcp` instead of stdio (for a harness that mounts over HTTP). All logging is on
stderr, so the stdio protocol stream stays clean.
