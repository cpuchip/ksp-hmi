# SETUP — from a fresh machine to talking to CAPCOM

This is the front door. Follow it top to bottom and you go from a clean machine to a
mission-control copilot that reads your live KSP flight. It's ordered **simplest
first**: the KSP mods and the one build are shared by everyone; then **Path A** is
text CAPCOM (type to it — works anywhere Claude Code or Claude Desktop runs, no voice
stack), and **Path B** is the full voice loop.

> **Target: KSP1 v1.12.5.** Every version and mod below is for KSP1. KSP2 is not
> supported (see RESEARCH.md).

**What each tier needs, honestly:**

| Tier | You get | You need |
|---|---|---|
| KSP mods + build (§1–2) | telemetry over kRPC + the `ksp-mcp` server | KSP1 1.12.5, CKAN, Go 1.26+ |
| **Path A — text CAPCOM** (§3) | type "what's my orbit?" and read the answer | Claude Code *or* Claude Desktop |
| **Path B — voice CAPCOM** (§4) | talk to Houston out loud | Python 3.11/3.12 + uv, a mic, and a *mind* that holds the tools (a loom seat, or any tool-calling OpenAI-compatible endpoint) |

The intercept/rendezvous planners (the MechJeb-backed tools) are **optional** — the
reads, the burn math, and the native node planners all work without MechJeb. Add
MechJeb only when you want real intercepts (§1, step 3).

---

## 1. KSP side (do this once)

### Install the mods with CKAN

[CKAN](https://github.com/KSP-CKAN/CKAN/releases) is the mod manager; it handles
dependencies and versions so you don't hand-place DLLs.

1. Install CKAN and point it at your KSP1 1.12.5 install (CKAN auto-detects Steam
   installs; otherwise "Select KSP install" → the folder with `KSP_x64.exe`).
2. **Install the core kRPC mod.** In CKAN's search box type `kRPC` and install
   **kRPC** (the Remote Procedure Call server, maintained by *djungelom*, **v0.5.4**).
   This is the bridge `ksp-mcp` talks to. **Do not** confuse it with *KRPC.MechJeb* —
   that's the separate MechJeb bridge in step 3, not needed for basic use.
3. **(Optional — only for the intercept/rendezvous planners)** install **both**:
   - **MechJeb2**, pinned to **2.14.3.0**
   - **KRPC.MechJeb** **0.7.1** (its latest release, Dec 2024)

   **The version pin matters — this is a lesson paid for in a wasted evening.**
   KRPC.MechJeb binds to MechJeb2's internals by reflection when it loads. KRPC.MechJeb
   0.7.1 targets **MechJeb2 2.14.3.0**. With **MechJeb2 2.15.x the binding silently
   fails** — worse, KRPC.MechJeb still reports itself "ready," then every plan throws a
   `NullReferenceException`. `ksp-mcp` detects this and falls back to the native
   textbook math (so you're never stuck), but the *real* MechJeb planner only lights up
   on the matched pair. Run **MechJeb2 2.14.3.0 + KRPC.MechJeb 0.7.1** and it works with
   zero code change.

   In CKAN, install MechJeb2, then use the version selector to pin it to **2.14.3.0**
   (right-click the mod → *Versions* → choose 2.14.3.0; the exact UI path varies by CKAN
   release). If CKAN won't offer that version for your game, the reliable fallback is a
   manual install: download **MechJeb2 2.14.3.0** from its
   [SpaceDock](https://spacedock.info/mod/12/MechJeb%202)/GitHub release and drop it in
   `GameData/`, then install KRPC.MechJeb 0.7.1 from CKAN. The goal is simply: **MechJeb2
   == 2.14.3.0** alongside **KRPC.MechJeb == 0.7.1**.

### Start the kRPC server in-game

1. Launch KSP1. A small **kRPC** window appears (drag it somewhere handy).
2. Click **Start server**. Default ports are **50000** (RPC) and **50001** (stream) —
   leave them unless something else is using them.
3. Check **"Auto-accept new clients."** This is the recommended posture: without it, kRPC
   holds each new connection waiting for you to click *Allow* in-game, and a client can
   time out before you get to it. (If you'd rather approve each client manually, that's
   fine — just be quick, or see Troubleshooting.)

That's the whole KSP side. The mods stay installed; you'll just click **Start server**
each time you play.

---

## 2. Build `ksp-mcp` (once)

You need [Go 1.26+](https://go.dev/dl/). From the repo root:

```bash
go build -o ksp-mcp ./cmd/ksp-mcp        # Linux/macOS
# Windows:
go build -o ksp-mcp.exe ./cmd/ksp-mcp
```

That produces one self-contained binary. Confirm it can reach the game with the standing
live self-test (KSP running, kRPC server started, a craft in flight is ideal):

```bash
go run ./cmd/ksp-mcp -smoke
```

`-smoke` connects, runs kRPC service discovery, and drives **every** tool against your
real flight, printing the outputs. If KSP or the kRPC server is down it prints exactly
how to bring it up and exits non-zero — so a green `-smoke` is your proof the whole
telemetry path works before you wire up any mind.

---

## 3. Path A — text CAPCOM (start here)

**This is the recommended entry point.** No voice stack, no loom, no GPU — just a chat
client that can mount an MCP server and load a system prompt. It gets you to "talking to
CAPCOM" (by typing) in a couple of minutes, on any machine.

You wire up two things: **the tools** (`ksp-mcp` via `.mcp.json`) and **the brain**
(`persona/CAPCOM.md` as the system/project instructions).

### With Claude Code

1. Copy the template and set the path:
   ```bash
   cp .mcp.json.example .mcp.json
   ```
   Edit `.mcp.json` so `command` is the **absolute** path to the binary you built in §2
   (Windows: the `ksp-mcp.exe`). The file:
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
   Leave `args` empty when Claude Code runs on the **same machine** as KSP. (Different
   machine/container? See §5.)
2. Load the persona: put `persona/CAPCOM.md`'s content where Claude Code reads project
   instructions — simplest is to copy it to a `CLAUDE.md` at the root of wherever you run
   Claude Code, or paste it into your session. That makes the mind *be* CAPCOM.
3. Start Claude Code in that directory, confirm the `ksp` tools are listed, and type:
   > what's my orbit?

### With Claude Desktop

1. Open Claude Desktop's config (`claude_desktop_config.json` — Settings → Developer →
   Edit Config) and add the same `ksp` server block under `mcpServers` (the shape in
   `.mcp.json.example`), with the absolute binary path. Restart Claude Desktop.
2. Create a **Project**, and paste the content of `persona/CAPCOM.md` into the project's
   **custom instructions**. That project's chats are now CAPCOM.
3. In a chat in that project, type:
   > what's my orbit?

If the game is at the Space Center with no craft in flight, CAPCOM will honestly say so
rather than invent numbers — roll a craft out and ask again.

---

## 4. Path B — voice CAPCOM

The full loop: **mic → local Whisper (STT) → the mind → local Kokoro (TTS) → speaker.**
The `voice/` bot (`capcom_bot.py`) is a **dumb pipe** — it has no tools and no persona of
its own. All the intelligence lives in **the mind**, which is where `ksp-mcp` and
`persona/CAPCOM.md` are mounted. See [`voice/README.md`](voice/README.md) to run the bot
itself; this section is about the **mind** it points at.

For the voice-stack lessons this bot inherits (Pipecat + Whisper + Kokoro, push-to-talk,
barge-in), see [cpuchip/ai-hmi-jumpstart](https://github.com/cpuchip/ai-hmi-jumpstart).

You have two honest options for the mind:

### (b1) Any OpenAI-compatible endpoint with tool-calling + `ksp-mcp` wired in

The voice bot speaks the OpenAI `/v1/chat/completions` shape. **So any mind runtime that
(a) exposes an OpenAI-compatible endpoint AND (b) actually mounts `ksp-mcp` as
tool-callable tools** can drive CAPCOM. Point `voice/.env`'s `CAPCOM_LLM_BASE_URL` and
`CAPCOM_LLM_MODEL` at it, and set `CAPCOM_PERSONA` to the content of `persona/CAPCOM.md`
(needed here because a bare endpoint has no prompt of its own).

Be clear-eyed about the requirement: a *plain* chat endpoint won't do — CAPCOM's whole
job is reading the **live** ship, so the mind must genuinely hold and call the `ksp-mcp`
tools. A tool-less model will just guess numbers, which is the one thing mission control
never does. If your runtime supports MCP tool-mounting, this path is fully local and has
no Claude dependency.

### (b2) A loom `sonnet#capcom` seat (the reference setup)

This is the path we run and have proven. [loom](https://github.com/cpuchip/loom) is a Go
multi-agent harness; its `loom serve` exposes an OpenAI-compatible shim backed by a
long-lived Claude seat that holds MCP tools + a persona + warm sessions (turns land in
~2–6 s). It needs **loom** installed and a **Claude login** (the seat rides your existing
Claude credentials).

The seat is defined by a small **home directory** — reproduce it generically like this
(no machine-specific paths required):

1. Make a home dir for the seat, e.g. `capcom-home/`, containing three files:
   - **`CLAUDE.md`** — copy `persona/CAPCOM.md` here. This is the seat's brain.
   - **`stewards-mcp.json`** — mounts `ksp-mcp` as a stdio server for the seat, same shape
     as `.mcp.json.example`. If the seat runs in a **container** while KSP runs on the
     host, set the tool's args to `["-host", "host.docker.internal"]` (see §5); if the
     seat runs on the same host as KSP, no `-host` is needed.
   - **`settings.json`** — set `env.ENABLE_TOOL_SEARCH` to `"false"`. This is
     load-bearing: it keeps the ~30 ksp tools eagerly loaded instead of deferred behind a
     ToolSearch that doesn't index `--mcp-config` stdio servers on current Claude Code.
     The 30 tool descriptions cost only ~3–5k tokens, so eager-load is cheap.
2. Run `loom serve` configured to resolve this home for a role (e.g. `capcom`), then point
   `voice/.env` at it: `CAPCOM_LLM_BASE_URL=http://<loom-host>:7791/v1`,
   `CAPCOM_LLM_MODEL=sonnet#capcom`, `CAPCOM_STICKY=sticky:capcom`, and the token file
   loom prints. Leave `CAPCOM_PERSONA` unset — the seat's `CLAUDE.md` already *is* the
   persona.

See loom's own docs for the exact `loom serve` invocation and role-home resolution — the
pieces above are the KSP-specific parts; loom owns the rest.

---

## 5. The kRPC-host gotcha (where the mind runs vs. where KSP runs)

`ksp-mcp` dials the kRPC server. **Who it dials depends on where the mind lives:**

- **Same machine as KSP** (the common case — text CAPCOM, or a voice bot + local mind on
  the gaming PC): use the default. No `-host` flag; it reaches `localhost:50000`.
- **Containerized mind** (e.g. a loom seat in Docker while KSP runs on the host): pass
  `-host host.docker.internal` so the container reaches the host's kRPC.
- **Remote/LAN mind** (mind on a different box than KSP): pass `-host <the KSP machine's
  IP>`, e.g. `-host 192.168.1.50`. Make sure the kRPC ports (50000/50001) are reachable.

Set this in the `args` of whichever config mounts `ksp-mcp` (`.mcp.json`,
`claude_desktop_config.json`, or the seat's `stewards-mcp.json`). The RPC/stream ports are
`-rpc-port` / `-stream-port` if you changed them in-game.

---

## 6. Troubleshooting

- **No tools showing up in the mind.** Rebuild the binary (§2) and re-point the config at
  it. If you're using **loom warm seats**, a warm seat won't pick up a new binary or a
  new `stewards-mcp.json` until it's **cold-cycled** — restart/downgrade the seat so it
  spawns fresh.
- **Handshake / connection timeout** when a tool first runs. kRPC is holding the
  connection for in-game approval. Check **"Auto-accept new clients"** in the kRPC window,
  or click **Allow** promptly when the client appears in kRPC's list.
- **`connection refused` / "I've lost the data link."** The kRPC server isn't running:
  in KSP, open the kRPC window and click **Start server**. Confirm ports 50000/50001.
- **Everything reads `available: false`.** You're at the Space Center or another
  no-flight scene — there's no vessel to read. Roll a craft out to the launchpad/runway
  and ask again. (This is correct, honest behavior, not a bug.)
- **"The intercept / rendezvous planner isn't cooperating"** (it fell back to the simple
  method, or says it needs a different mod version). This is the MechJeb version mismatch:
  install **MechJeb2 2.14.3.0** with **KRPC.MechJeb 0.7.1** (§1, step 3). The reads, the
  burn math, and the native node planners work regardless — only the MechJeb-backed
  planners need the matched pair.
- **Diagnosing MechJeb specifically.** `go run ./cmd/ksp-dump -mj` reports whether
  MechJeb's planner is actually functional on the live install (it doesn't trust the mod's
  self-reported "ready" flag); `go run ./cmd/ksp-dump -service MechJeb` dumps the live
  MechJeb API.

---

Once §1–2 are done and `-smoke` is green, pick **Path A** to type to CAPCOM or **Path B**
to talk to her. The persona is the same brain either way — `persona/CAPCOM.md`.
