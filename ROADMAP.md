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

---

## Design invariants (hold across all phases)

- **KSP1 only.** Frozen platform = stable tooling. No chasing a moving target.
- **Local-first.** Mind (llama-chip) and voice (Whisper + Kokoro) run on our hardware. Cloud is a
  fallback, never the default.
- **Confirm-gated by default.** The copilot proposes; the commander (the kid) commits. Autonomy is
  opt-in per phase, loosened only with his buy-in. This is pedagogy, not just safety.
- **Build the oracle first.** Every autonomous maneuver gets a deterministic check before it's trusted.
- **Reuse our own stack** (ai-hmi-jumpstart, loom, llama-chip, space-center) over rebuilding.
