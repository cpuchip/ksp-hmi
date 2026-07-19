# ksp-hmi

**A voice-driven, MCP-native mission copilot for Kerbal Space Program 1 — built on our own local stack, for flying rockets with my son.**

> **Target: KSP1 (v1.12.5).** That's what we play, and it's the stable, alive, thoroughly-modded
> platform. KSP2 development ended in 2024 — see RESEARCH.md for that context. This project does not
> target KSP2.

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
