# voice/ — CAPCOM, a voice Mission Control for KSP

Talk to Houston while you fly. CAPCOM listens on the mic, reads your **live** vessel
telemetry, and answers in a calm mission-control voice — apoapsis, fuel, crew, time to
your next burn. Reads only, for now; it can see the ship, it can't fly her yet.

```
  mic → Silero VAD → faster-whisper (local) → the CAPCOM mind → Kokoro (local) → speaker
                                                    │
                          a loom `sonnet#capcom` seat that holds ksp-mcp + the persona
                                                    │
                                              ksp-mcp (stdio) → kRPC → KSP1 + your ship
```

## The one important idea: CAPCOM is a dumb pipe

`capcom_bot.py` has **no tools, no persona of its own, and no connection to the game.**
The mind does all of that. The mind is a [loom](https://github.com/cpuchip/loom) seat —
`sonnet#capcom` over loom serve's OpenAI-compatible shim — and that seat is where the
`ksp-mcp` telemetry tools and the CAPCOM persona (its `CLAUDE.md`) live. The bot just
carries your speech to the seat and speaks the reply. Swap the mind by changing one
URL and one model name in `.env`.

Why a Claude seat and not a raw fast model? Because CAPCOM's whole job is reading the
**live** ship, and the seat is what carries the `ksp-mcp` tools. loom's OpenAI shim
drives a Claude seat (that's the path that gets tools + persona + warm sessions); a
tool-less fast model would just guess at your numbers, which is the one thing mission
control never does. Warm turns land in ~2–6 seconds. (Host-side seat wiring and the
"why not an OpenAI/terra mind" verdict: `~/.stewards/capcom-seat-README.md`.)

## Run it

```powershell
# Windows (from this voice/ dir)
./run-capcom.ps1                      # sets up the venv, copies .env, launches on :7862
```

or manually, cross-platform:

```sh
uv venv
uv pip install -r requirements.txt
cp .env.example .env                  # edit if loom serve isn't on localhost:7791
uv run capcom_bot.py --host localhost --port 7862
```

First run downloads the Whisper model (`large-v3-turbo`) and the Kokoro voice — a
one-time wait. Then open **http://localhost:7862** on the same machine, allow the
microphone, click connect, and talk. Talk over CAPCOM and it stops to listen (barge-in).

### Wake word (optional)

By default CAPCOM is always listening. To make it wait for its name, set
`CAPCOM_WAKE_PHRASES=capcom, cap com` in `.env` (include `cap com` — Whisper sometimes
splits the word). Then CAPCOM stays muted until it hears "CAPCOM", listens for
`CAPCOM_WAKE_KEEPALIVE` seconds (default 20) so follow-ups don't need the wake word
again, and mutes once you go quiet. It's a software gate after speech-to-text (Whisper
still runs), not a hardware mic switch — fine for hands-busy flying, not a privacy
control. The opening greeting is unaffected; CAPCOM still calls in when you connect.

### Prereqs

- **loom serve** up with a `capcom` seat. On the reference host that's
  `sonnet#capcom` against `~/.stewards/capcom-claude-home` — see
  `~/.stewards/capcom-seat-README.md`.
- **KSP1 (v1.12.5)** running, with the **kRPC** server started in-game (the seat's
  `ksp-mcp` dials it). No ship in flight? CAPCOM will say so honestly.
- **Python 3.11 / 3.12**, [uv](https://github.com/astral-sh/uv), and a GPU for the STT
  (or set `CAPCOM_STT_DEVICE=cpu` / `CAPCOM_STT_COMPUTE=int8` to leave the GPU to KSP).

## Try saying

- "CAPCOM, what's my orbit?"
- "How much fuel is left in this stage?"
- "Who's aboard?"
- "How long until my next burn?"
- "Are we connected — can you see the ship?"

## Config

Everything is env-driven — see [`.env.example`](.env.example). The knobs that matter:
`CAPCOM_LLM_BASE_URL` (where loom serve listens), `CAPCOM_LLM_MODEL` (`sonnet#capcom`),
`CAPCOM_STICKY` (the sticky session name), `CAPCOM_STT_DEVICE`, `CAPCOM_TTS_VOICE`.

## Port

CAPCOM runs on **7862** — deliberately separate from spin's 7860, so both can run at
once. Desktop-first on `localhost` (the browser mic needs a secure context, which
localhost is). For a phone, bind `--host 0.0.0.0` and reach it over TLS or a native
WebRTC client.

## Credits

Modeled on [cpuchip/ai-hmi-jumpstart](https://github.com/cpuchip/ai-hmi-jumpstart)'s
starter and the companion mode of `projects/spin` — stripped to a clean, separate
CAPCOM with no companion/substrate identity.
