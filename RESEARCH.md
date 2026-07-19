# KSP + AI/HMI + MCP — field survey and verdicts

**Binding question:** In the KSP + AI/HMI + MCP field, what exists, how mature is it, and what should
Michael's stack ADOPT (use as-is), BORROW (steal the pattern, build our own), or BUILD (nothing good
exists — our lane)? Centerpiece: *does a mature kRPC↔MCP bridge exist? If not, is a Go `ksp-mcp`
against kRPC's protobuf protocol the BUILD?*

**Method:** Discovery via web + Exa neural search; every "exists" claim below has a URL I fetched.
All maturity facts (stars, last-push dates, license, language) come from the GitHub API queried
**2026-07-19**. Dates are hard facts, not vibes. Unknowns are marked unknown.

**Headline answer:** The field is real and *more mature than a single scoping pass suggests* — but the
maturity is all in the **infrastructure layer** (kRPC, MechJeb2), not in the **AI-bridge layer**.
A mature kRPC↔MCP bridge does **not** exist: the half-dozen KSP MCP servers are solo, ≤4 stars, one
isn't on PyPI despite its README, and **none are in Go**. The concept is nonetheless *peer-reviewed
proven* — MIT Lincoln Laboratory ran a challenge where an LLM piloted KSP and placed 2nd. So the
verdict is not "nothing works," it's "the plumbing is solved, the copilot is unbuilt, and the copilot
is our lane."

---

## 1. The infrastructure layer — MATURE, ADOPT

This is the "read state out, feed commands in" problem, and it is thoroughly solved.

### kRPC — the bridge (ADOPT)
- **[krpc/krpc](https://github.com/krpc/krpc)** — C#, **731 stars, 170 forks**. License: **LGPL-3.0**
  for the core, with `service/SpaceCenter/*` under **GPLv3** and the codegen/service-definition tools
  GPLv3 (verified from the repo's `LICENSE`). Latest **release v0.5.4, 2024-06-10**, for KSP 1.12.5
  ([releases](https://github.com/krpc/krpc/releases)). The git repo still gets commits, but there has
  been no new release in ~13 months — which is fine, because KSP1 itself is frozen at 1.12.5.
- **What it exposes:** essentially all of KSP's control/telemetry API — vessel state, flight data,
  orbit, resources, control (throttle/SAS/RCS/staging/gear), maneuver nodes, autopilot, plus support
  for popular mods (Ferram Aerospace, Kerbal Alarm Clock, Infernal Robotics)
  ([docs](https://krpc.github.io/krpc/)).
- **Transport:** length-prefixed **protobuf over TCP** — a dedicated RPC connection (default `:50000`)
  and a separate Stream connection (`:50001`) for pushed telemetry. Also supports **websockets** and
  **serialio (Arduino)** — the last one matters for the ESP32 panel.
- **Clients already exist** in C#, C++, C, Java, Lua, Python, and **Rust** (`krpc-client` crate). No
  Go client (see §5).

### MechJeb2 + KRPC.MechJeb — the autopilot (ADOPT)
- **[MuMech/MechJeb2](https://github.com/MuMech/MechJeb2)** — C#, **1112 stars, 290 forks**, actively
  maintained (last push 2026-07-13). The de-facto autopilot: ascent, landing, rendezvous, docking,
  node execution.
- **[Genhis/KRPC.MechJeb](https://github.com/Genhis/KRPC.MechJeb)** — C#, **GPL-3.0**, 21 stars, last
  push 2024-12-23. Exposes MechJeb over kRPC: SmartASS, ascent/landing autopilots, NodeExecutor,
  ManeuverPlanner (circularize / Hohmann / change Ap-Pe). **This is the glue** that lets an AI issue
  *high-level* maneuvers ("land at target," "execute next node") instead of hand-flying a PID loop.

### kOS — scriptable autopilot (context)
- **[KSP-KOS/KOS](https://ksp-kos.github.io/KOS/)** — in-game "kerboscript" runtime, a scriptable
  flight computer. Relevant because two of the MCP servers below drive kOS rather than raw kRPC.

### Telemachus — the HTTP/WebSocket alternative (BORROW, optional)
- **[Telemachus Reborn](https://spacedock.info/mod/2012/Telemachus%20Reborn)** (repo
  [KSP-Telemachus/Telemachus](https://github.com/KSP-Telemachus/Telemachus)) — **v1.11.0, released
  2026-03-31** for KSP 1.12.5, actively revived after a 6-year gap. Exposes telemetry & control over
  **plain HTTP + WebSocket JSON** with a browser dashboard at `localhost:8085`.
- **Why it matters to us:** JSON-over-WebSocket is dramatically simpler to consume from Go than kRPC's
  protobuf handshake — no `.proto` codegen, no dynamic service discovery. If the Go `ksp-mcp` wants a
  fast first cut, Telemachus is a legitimate lower-surface backend. Trade-off: kRPC's API is far
  richer and is what MechJeb/KSPDG/everyone else targets.

**Verdict for §1: ADOPT kRPC + MechJeb2 + KRPC.MechJeb wholesale.** Do not hand-roll orbital
mechanics or a telemetry bridge; that work is done and battle-tested. Telemachus is a BORROW-able
fallback backend if protobuf-in-Go proves annoying.

---

## 2. KSP MCP servers — the AI-bridge layer — IMMATURE, BORROW the patterns

Every one of these is a solo project with ≤4 stars. They prove the pattern works; none is a foundation
to depend on. Sorted roughly by relevance.

| Repo | Lang | License | Created | Last push | Stars | What it exposes |
|---|---|---|---|---|---|---|
| [MaiaJP-AIXplain/krpc-mcp](https://github.com/MaiaJP-AIXplain/krpc-mcp) | Python | MIT | 2026-05-04 | **2026-05-05** | 1 | **The cleanest copilot surface.** Curated tools over kRPC (vessel/flight/orbit/resources) + MechJeb planners; `KRPC_MCP_TOOL_MODE=full` drops to raw 1:1 kRPC. |
| [G4ertner/GeePT_MCP](https://github.com/G4ertner/kRPC_docs_MCP) | Python | MIT | 2025-10-19 | 2025-12-31 | 2 | **The richest surface.** `execute_script` (live kRPC Python in-game), blueprint/part-tree/stage-plan, `get_flight_snapshot`, screenshots, **LLM save/load checkpoints**, **cancellable script jobs**, KSP-wiki + kRPC-docs + community-snippet search, playbooks. Auto-pauses the game after each script. |
| [Nodenester/KerbalMCP](https://github.com/Nodenester/KerbalMCP) | C# | MIT | 2026-04-21 | 2026-04-21 | 0 | Claims "**80+ tools, MechJeb-powered planners**," control from Claude Code. Single-commit, depth unverified. |
| [caseys/ksp-mcp](https://github.com/caseys/ksp-mcp) | TypeScript | MIT | 2025-12-06 | 2026-02-05 | **4** | The most-starred *control* MCP. Drives **kOS + MechJeb2** (writes/executes kOS scripts, not raw kRPC). CLI/MCP parity, `--no-execute` plan-only mode. |
| [Ljove02/krpc-mcp](https://github.com/Ljove02/krpc-mcp) | Python | MIT | 2026-02-08 | 2026-02-11 | 4 | **Docs-only, not control** — indexes kRPC's Python API docs so an LLM stops hallucinating kRPC calls. A clever adjunct. |

**Honesty flags:**
- MaiaJP-AIXplain/krpc-mcp's README says install via `claude mcp add krpc -- uvx krpc-mcp` from PyPI —
  but `https://pypi.org/pypi/krpc-mcp/json` returns **404**. It is *not* on PyPI. Its "maturity" is one
  day of commits and 1 star; treat it as a design reference, not a dependency.
- These are **read-from-the-README** capability claims for the ones I didn't run. I verified existence,
  language, license, stars, and dates via the API; I did **not** verify that "80+ tools" actually
  works end-to-end in-game.

**What's worth stealing (BORROW):**
- **Two-tier tool surface** (MaiaJP): a small *curated copilot* toolset by default, a *full 1:1* mode
  behind a flag. This is exactly right for a voice copilot — you want ~15 legible verbs, not 200.
- **Confirm/plan-only gates** (caseys' `--no-execute`; MaiaJP's node-plan-then-execute): create the
  maneuver node, *don't* burn until told. This is our confirm-gate, already validated by others.
- **Cancellable script jobs + telemetry-watch loop** (GeePT): start a burn as a job, stream logs,
  interleave `get_flight_snapshot`, `cancel_job` if telemetry goes sideways, auto-abort if the vessel
  disappears (crash/revert). This is the right safety pattern for anything that runs longer than a
  tool timeout.
- **A "docs" tool so the mind doesn't hallucinate the API** (Ljove02, GeePT): fold kRPC/MechJeb API
  reference into the MCP itself. Cheap, high-value.

**Verdict for §2: BORROW the patterns, ADOPT one Python server for the same-day P0, BUILD the durable
one in Go.** For the immediate loop, GeePT_MCP is the richest and MaiaJP is the cleanest copilot shape
— stand one up on the son's machine to prove the voice→mind→game loop today. Do **not** build the
long-term system on any of these: all solo, all low-star, none in Go, one vaporware-on-PyPI.

---

## 3. AI agent frameworks (non-MCP) — reference designs

- **[cjohnson74/KOSMOS](https://github.com/cjohnson74/KOSMOS)** — Python, MIT, 3 stars, last push
  2025-12-09. Multi-agent: **FlightAgent** (working) + MissionControl / Maneuver / **AuditAgent**
  (partial), GPT-4 over kRPC, with reusable "control primitives." *The AuditAgent — a separate agent
  that validates the mission is going to plan — is a pattern worth stealing for a copilot that flies
  with a kid.*
- **[proj-airi/game-playing-ai-kerbal-space-program](https://github.com/proj-airi/game-playing-ai-kerbal-space-program)**
  — Python, Apache-2.0, 4 stars, last push 2026-04-10. Part of **Project AIRI** (a Neuro-sama-style
  local-LLM VTuber, [moeru-ai/airi](https://github.com/moeru-ai/airi)). Notably **local-LLM driven**.
  But AIRI's general paradigm is **vision (YOLO/ONNX) + synthetic keyboard/mouse** in a two-layer
  agent (high-freq controller under a low-freq LLM planner) — a *harder, less reliable* path than
  kRPC's structured telemetry. Lesson: **we should not go the vision route** when kRPC hands us exact
  state for free. (Their KSP integration does still use kRPC + a DarkMultiPlayer server.)
- **[Mystic-Red/M.U.N.A.](https://github.com/Mystic-Red/M.U.N.A.)** — C#, MIT, 1 star, 2026-05. An
  *in-game* AI companion part (installs on every command pod via ModuleManager), Groq **or local**
  LLM, answers telemetry questions in character ("Can I reach the Mun with this fuel?"). Chatbot, not
  control — but the "AI persona bolted into the cockpit" framing is charming and on-theme.

**Verdict for §3: BORROW the AuditAgent (independent validation) idea; note AIRI as the "don't do
vision" cautionary tale; note M.U.N.A. as persona flavor.**

---

## 4. Voice / HMI / physical panels — the closest neighbors, and the gap

- **[engbman/ksp-mission-control](https://github.com/engbman/ksp-mission-control-download)** (SpaceDock
  [mod 4378](https://spacedock.info/mod/4378)) — no license (closed download), created 2026-07-01,
  last push **2026-07-13** (recent!). **Voice-controlled CAPCOM "Houston":** push-to-talk (F6),
  **OpenAI Whisper + TTS** (cloud, ~$0.20/mission), kRPC telemetry **verification of milestones**,
  live **go-calls** (TLI/LOI/ignition/return), 7 voices, full mission-phase tracking, Windows-only.
  **This is the nearest thing to our vision that exists — and it's the clearest steelman.** It nails
  the UX (phase tracking, go-calls, telemetry-verified milestones, NASA chatter). Its *weakness is
  exactly our advantage*: it depends on cloud OpenAI for both ears and voice. **We already have local
  Whisper + Kokoro in ai-hmi-jumpstart** — we can build the same CAPCOM experience with no API key, no
  per-mission cost, and a mind that runs on our own GPUs.
- **[AustinMathuw/Kerlexa](https://github.com/AustinMathuw/Kerlexa)** — C#, no license, 2019 hackathon,
  abandoned (last push 2022). Alexa skill + KSP mod + **Arduino physical rocket model** (LEDs for
  staging/burn, motors for yaw/roll). Dead, but it's proof that **voice + a physical panel** is a
  coherent build, and it prefigures the ESP32 lane.
- **YouTube evidence** (demos, not verified code): "[KSP With FULLY VOICED AI Copilot]
  (https://www.youtube.com/watch?v=Eph_KnCnU-s)" (2023), and Carnasa's "[I Let AI Run My Space
  Agency... It was bad](https://www.youtube.com/watch?v=WZX7VQ3NtDE)" (2026-04) where ChatGPT-5.4 runs
  an RP-1 agency and mostly fails comically — a useful reminder that *unconstrained* AI-runs-everything
  is entertaining but bad; *confirm-gated copilot* is the sound design.
- **Telemachus lineage** (§1) is the long-running "telemetry out to external dashboards / physical
  cockpits" ecosystem — the streaming-overlay and hardware-panel hobby has run on it for years.

**Verdict for §4: BORROW engbman's CAPCOM UX wholesale (phases, go-calls, telemetry-verified
milestones); BUILD it on our local voice stack instead of cloud OpenAI. The physical-panel lane
(Kerlexa → our ESP32) is a genuine BUILD differentiator nobody else is doing well.**

---

## 5. The centerpiece question: is a Go `ksp-mcp` the BUILD? — YES

**Does a mature kRPC↔MCP bridge exist?** No. The candidates are Python (MaiaJP, GeePT), TypeScript
(caseys), or C# (Nodenester); all solo; the most-starred is 4; the cleanest copilot one isn't on PyPI.
**There is no Go kRPC client at all** (`gh search repos "krpc golang"` → empty) and no Go KSP MCP.

**Is kRPC's wire protocol tractable from Go?** Yes — cleanly. From `protobuf/krpc.proto` (verified),
the whole protocol is a small, well-defined protobuf schema:

- Handshake: `ConnectionRequest` / `ConnectionResponse` (type = RPC or STREAM; STREAM reuses the
  client identifier from the RPC connection's response).
- Calls: `Request` carries repeated `ProcedureCall` (service + procedure + `Argument`s);
  `Response` carries repeated `ProcedureResult` / `Error`.
- Streams: `StreamUpdate` / `StreamResult` pushed on the stream connection.
- Discovery: `Services` / `Service` / `Procedure` / `Parameter` / `Class` / `Enumeration` / `Type` —
  the API is **self-describing** via `KRPC.GetServices`, so a client can call any procedure by name
  without generated stubs.
- Framing: length-prefixed protobuf messages over TCP.

A from-scratch client in **Rust** already exists (`krpc-client` crate), which is direct proof that a
systems-language client is very achievable. A Go client is the same shape: implement the handshake +
length-prefixed `Request`/`Response` + argument encoding, then expose a **curated, confirm-gated
copilot toolset** as MCP tools. Michael has deep Go MCP-server experience, the `mcp-server-go` skill,
and **loom is already Go** — this slots straight in.

**Two viable backends for the Go build:**
1. **kRPC protobuf/TCP** — richest API, what everything targets, needs `.proto` codegen + a thin
   protocol layer. The "real" build.
2. **Telemachus HTTP/WebSocket JSON** — much simpler from Go, smaller API surface, great for a fast
   first cut or a low-dependency path.

**Verdict for §5: BUILD `ksp-mcp` in Go.** It's a genuine gap, it's squarely in Michael's strongest
language, it composes with loom and ai-hmi-jumpstart, and it can license cleanly (a network client of
the LGPL/GPL kRPC *mod* is not a derivative work — Michael can Apache/MIT his own client). Start against
kRPC's protobuf; keep Telemachus JSON as a documented fallback backend.

---

## 6. The credibility anchor — LLMs piloting KSP is peer-reviewed real

Not a hobby-only space. **MIT Lincoln Laboratory** built and ran a formal challenge:

- **[mit-ll/spacegym-kspdg](https://github.com/mit-ll/spacegym-kspdg)** (KSPDG) — Python, **MIT**,
  **133 stars, 19 forks**, created 2022-11-29, last push 2026-01-22 (maintained). Non-cooperative
  satellite-ops (pursuit-evasion) challenge problems as **OpenAI Gym / Gymnasium** environments on
  KSP1, driven by **kRPC** + **PhysicsRangeExtender**, requiring the **Making History** expansion.
  Explicitly "environments, not agents." Challenge page:
  [MIT-LL KSPDG](https://www.ll.mit.edu/conferences-events/2024/01/kerbal-space-program-differential-game-challenge).
- **[arXiv:2404.00413](https://arxiv.org/abs/2404.00413)** — *"Language Models are Spacecraft
  Operators"* (Rodriguez-Fernandez, Carrasco, Cheng, Scharf, Siew, Linares; MIT ARC Lab / Universidad
  Politécnica de Madrid). Verbatim from the abstract: *"Our approach leverages prompt engineering,
  few-shot prompting, and fine-tuning techniques to create an effective LLM-based agent that ranked
  2nd in the competition."*
- Follow-ons: [arXiv:2501.07802](https://arxiv.org/pdf/2501.07802) *"Visual Language Models as
  Operator Agents in the Space Domain"* and [arXiv:2505.19896](https://arxiv.org/pdf/2505.19896)
  *"Large Language Models as Autonomous Spacecraft Operators in Kerbal Space Program."*

**Verdict for §6: BORROW/ADOPT KSPDG as an optional evaluation harness and a credibility citation.**
The Gym env is a rigorous way to *test* the copilot's maneuvering later, and the paper is the honest
answer to "does this actually work?" — yes, a plain LLM with good prompting placed 2nd against
optimal-control and RL entries. (KSPDG needs the Making History DLC + PhysicsRangeExtender — a
heavier setup than the P0 copilot loop, so treat it as P1+/optional.)

---

## 7. KSP2 reality — do not target it

- **Intercept Games (the KSP2 studio) was closed by Take-Two in June 2024**; the game's **last update
  was June 2024**, with **no new content since December 2023**. It sits in early-access limbo — no
  official cancellation, no active development. In November 2024 Take-Two sold Private Division (and
  the KSP franchise) to Haveli Investments / former Annapurna Interactive staff.
  ([Wikipedia](https://en.wikipedia.org/wiki/Kerbal_Space_Program_2),
  [TechRaptor](https://techraptor.net/gaming/news/intercept-games-shut-down-kerbal-space-program-2)).
- Corroborating signal from our own domain: **[krpc/krpc2](https://github.com/krpc/krpc2)** (the KSP2
  RPC bridge) is **archived**, with only a single `SpaceCenter2` service exposing basic telemetry.

**Verdict for §7: target KSP1 (1.12.5). It's frozen — and a frozen target is a virtue for tooling:
kRPC v0.5.4, MechJeb2, and Telemachus are all pinned to it and stable. No moving platform to chase.**

---

## Summary — the adopt / borrow / build table

| Field finding | Verdict | Why |
|---|---|---|
| **kRPC** (bridge) | **ADOPT** | Mature (731★), de-facto standard, protobuf/TCP + serialio + websockets, frozen to KSP 1.12.5. Don't reinvent the bridge. |
| **MechJeb2 + KRPC.MechJeb** (autopilot over kRPC) | **ADOPT** | 1112★, actively maintained; gives high-level maneuvers (ascent/land/rendezvous/node-exec) — don't hand-roll orbital mechanics. |
| **Telemachus** (HTTP/WS JSON) | **BORROW** (fallback backend) | Simpler than protobuf from Go; smaller API. Optional low-dep path for the Go build. |
| **Existing KSP MCP servers** (MaiaJP, GeePT, caseys, Nodenester, Ljove02) | **BORROW patterns; ADOPT one for P0** | Two-tier tool surface, confirm/plan-only gates, cancellable jobs, a "docs" tool. All solo/≤4★/none-in-Go → not a foundation. Run GeePT/MaiaJP to prove the loop today. |
| **KOSMOS / AIRI** (agent frameworks) | **BORROW** (AuditAgent idea); avoid AIRI's vision route | Independent-validation agent is gold for flying with a kid; vision+synthetic-input is the wrong paradigm when kRPC gives exact state. |
| **engbman voice CAPCOM** (Houston) | **BORROW UX; BUILD on our local stack** | Nails the CAPCOM UX but is cloud-OpenAI-bound. We have local Whisper+Kokoro → same experience, no key, no cost. |
| **KSPDG + arXiv 2404.00413** (MIT-LL) | **ADOPT** (eval harness + citation) | Peer-reviewed proof LLMs can pilot KSP (2nd place). Gym env to rigorously test maneuvers later. Needs Making History DLC. |
| **Physical panel** (Kerlexa → our ESP32) | **BUILD** | Genuinely differentiated; kRPC speaks serialio natively; we already have bridge-sim hardware in space-center. |
| **`ksp-mcp` in Go against kRPC protobuf** | **BUILD** (centerpiece) | No Go client, no Go MCP exists; kRPC's `.proto` is small & self-describing (Rust proves feasibility); slots into loom (Go) + ai-hmi-jumpstart. Ours to own. |
| **KSP2** | **IGNORE** | Dev ended 2024; kRPC2 archived. Target KSP1 1.12.5. |

## Open questions this survey did not answer

- **Does `ksp-mcp` need raw kRPC, or is Telemachus JSON enough for the P0 copilot?** Decide empirically
  by trying the simpler backend first.
- **How much of the flying should the AI *do* vs. *advise*?** The whole design tension. Start
  maximally confirm-gated (advise + execute-on-go) and loosen per phase only with the kid's buy-in.
- **Does the Making History DLC (required by KSPDG) matter for us?** Only if we adopt the KSPDG eval
  harness in P1+. The core copilot loop doesn't need it.
- **Verify the unrun servers' real capability** (esp. Nodenester's "80+ tools") before borrowing any
  code rather than just patterns.

---

*Provenance: all repo maturity facts pulled from the GitHub API on 2026-07-19; every external claim
carries a fetched URL. Full working notes retained in the research scratch file for this session.*
