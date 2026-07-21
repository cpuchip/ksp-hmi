#
# CAPCOM — a voice Mission Control for Kerbal Space Program.
#
#   mic -> Silero VAD (barge-in) -> faster-whisper STT (local, GPU)
#        -> the CAPCOM mind (a loom seat) -> Kokoro TTS (local) -> speaker
#
# CAPCOM is a DUMB PIPE by design. The mind is a loom `sonnet#capcom` seat that
# already holds the ksp-mcp telemetry tools AND the CAPCOM persona (its CLAUDE.md
# role home). This bot just carries speech to that seat and speaks its reply — it
# has NO tools, NO persona of its own, NO game connection. The seat reads the live
# ship; the bot is the voice on the loop. (See ../README.md and, on the host,
# ~/.stewards/capcom-seat-README.md for how the seat is wired.)
#
# Modeled on cpuchip/ai-hmi-jumpstart's starter + projects/spin's companion mode,
# stripped to CAPCOM. Its own port (7862), separate from spin's 7860.
#
#   uv run capcom_bot.py --host localhost --port 7862   # open http://localhost:7862
#

import os
import sys

# Windows consoles default to cp1252; the Pipecat runner prints emoji in its banner,
# which crashes on charmap. Force UTF-8 stdio so it encodes regardless of launcher.
try:
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
except Exception:
    pass


def _enable_cuda_dlls():
    """faster-whisper/ctranslate2 (C++) loads CUDA DLLs via PATH, NOT Python's
    add_dll_directory. Prepend the pip-installed nvidia bin dirs (cublas, cudnn,
    cudart, nvrtc) to PATH so GPU STT can load cublas64_12.dll on Windows. Without
    this, GPU inference fails with "cublas64_12.dll cannot be loaded". Must run
    BEFORE faster_whisper is imported. Harmless on Linux/macOS and when there is
    no GPU (run STT on CPU instead — see .env.example)."""
    import site

    sps = list(site.getsitepackages())
    if hasattr(site, "getusersitepackages"):
        sps.append(site.getusersitepackages())
    dirs = []
    for sp in sps:
        nvidia = os.path.join(sp, "nvidia")
        if os.path.isdir(nvidia):
            for sub in os.listdir(nvidia):
                d = os.path.join(nvidia, sub, "bin")
                if os.path.isdir(d):
                    dirs.append(d)
    if dirs:
        os.environ["PATH"] = os.pathsep.join(dirs) + os.pathsep + os.environ.get("PATH", "")
        for d in dirs:
            try:
                os.add_dll_directory(d)
            except Exception:
                pass


_enable_cuda_dlls()

from dotenv import load_dotenv
from loguru import logger

from pipecat.audio.vad.silero import SileroVADAnalyzer
from pipecat.frames.frames import LLMRunFrame
from pipecat.pipeline.pipeline import Pipeline
from pipecat.pipeline.worker import PipelineParams, PipelineWorker
from pipecat.processors.aggregators.llm_context import LLMContext
from pipecat.processors.aggregators.llm_response_universal import (
    LLMContextAggregatorPair,
    LLMUserAggregatorParams,
)
from pipecat.processors.frameworks.rtvi import RTVIObserverParams, RTVIProcessor
from pipecat.runner.types import RunnerArguments
from pipecat.runner.utils import create_transport
from pipecat.services.kokoro.tts import KokoroTTSService
from pipecat.services.openai.llm import OpenAILLMService
from pipecat.services.whisper.stt import Model as WhisperModel
from pipecat.services.whisper.stt import WhisperSTTService
from pipecat.transports.base_transport import BaseTransport, TransportParams
from pipecat.utils.text.markdown_text_filter import MarkdownTextFilter

load_dotenv(override=True)

# ---- The mind: a loom `sonnet#capcom` seat over loom's OpenAI shim ------------------
#
# The seat carries the ksp-mcp tools + the CAPCOM persona. Turns are real Claude
# sessions (~2-6s warm), so the mind actually reads the live ship instead of
# guessing — the whole point. Override any of these in .env.
#
# base_url: loom serve's shim. On the host loom binds its mesh IP; localhost is the
# conventional default. Set CAPCOM_LLM_BASE_URL to wherever your loom serve listens.
LLM_BASE_URL = os.getenv("CAPCOM_LLM_BASE_URL", "http://localhost:7791/v1")
# model "<model>#<role>": the role (capcom) selects the seat's claude-home; the model
# before # is passed straight to `claude --model`. sonnet is the proven tool-reliable
# pick; haiku#capcom trades some reliability for speed if you build a haiku home.
LLM_MODEL = os.getenv("CAPCOM_LLM_MODEL", "sonnet#capcom")
# The shim is keyless on loopback but 401s a WRONG bearer — and the OpenAI client
# always sends one. Read loom serve's real token from a file if given, else use the
# literal key (default "not-needed" works keyless).
LLM_API_KEY = os.getenv("CAPCOM_LLM_API_KEY", "not-needed")
_tok_file = os.getenv("CAPCOM_LLM_TOKEN_FILE", "")
if _tok_file:
    try:
        with open(os.path.expanduser(_tok_file), encoding="utf-8") as _f:
            LLM_API_KEY = _f.read().strip() or LLM_API_KEY
    except OSError:
        logger.warning(f"CAPCOM_LLM_TOKEN_FILE {_tok_file!r} unreadable — trying keyless")
LLM_TEMPERATURE = float(os.getenv("CAPCOM_LLM_TEMPERATURE", "0.6"))
# Sticky sessions (user = "sticky:<name>"): one living Claude seat per sitting, so
# turns resume (warm + cache) instead of replaying the transcript. loom forgets the
# mapping after ~2h idle = a fresh CAPCOM per session.
STICKY_USER = os.getenv("CAPCOM_STICKY", "sticky:capcom")

# Local STT (faster-whisper). large-v3-turbo is the speed/quality pick. cuda+float16
# on a GPU; set cpu+int8 in .env if you have no CUDA (or want to leave the GPU to KSP).
STT_DEVICE = os.getenv("CAPCOM_STT_DEVICE", "cuda")
STT_COMPUTE = os.getenv("CAPCOM_STT_COMPUTE", "float16")

# Local TTS (Kokoro). af_heart is the proven warm default; for a mission-control feel
# try am_michael / am_adam (American male) or bm_george (British male) — see .env.example.
TTS_VOICE = os.getenv("CAPCOM_TTS_VOICE", "af_heart")

# Optional WAKE WORD. Set CAPCOM_WAKE_PHRASES to a comma-separated list (e.g.
# "capcom, cap com") and CAPCOM stays muted until it hears one, then listens for
# CAPCOM_WAKE_KEEPALIVE more seconds of follow-up before muting again — so you say
# "CAPCOM" once, then talk freely. Empty (the default) = always listening, no gate.
# The filter matches the transcription (whole words, flexible spacing), so it's a
# software gate after STT, not a silent mic — Whisper still runs. "cap com" is worth
# including because Whisper sometimes splits the word.
WAKE_PHRASES = [p.strip() for p in os.getenv("CAPCOM_WAKE_PHRASES", "").split(",") if p.strip()]
WAKE_KEEPALIVE = float(os.getenv("CAPCOM_WAKE_KEEPALIVE", "20"))

# The persona lives in the SEAT (its CLAUDE.md). Sending it again here would
# double-instruct — a one-line transport note is enough (see ai-hmi-jumpstart
# PERSONA-TEMPLATE, "What to leave OUT").
PERSONA = os.getenv("CAPCOM_PERSONA") or (
    "Transport note: you are CAPCOM on a live voice loop right now — every word you "
    "write is spoken aloud. Your standing instructions govern everything."
)

# What CAPCOM says when the crew first keys the mic.
GREETING = os.getenv(
    "CAPCOM_GREETING",
    "The crew just keyed the mic. Greet them as CAPCOM in one short sentence and let "
    "them know you've got eyes on the ship whenever they want a read.",
)

# Transport selection for the runner. webrtc = the browser voice UI.
transport_params = {
    "webrtc": lambda: TransportParams(audio_in_enabled=True, audio_out_enabled=True),
}

# A lean live-transcript stream over the WebRTC data channel, so a custom client (or
# the browser UI) can render the loop as text. Additive — never touches the voice loop.
RTVI_OBSERVER_PARAMS = RTVIObserverParams(
    user_transcription_enabled=True,
    user_speaking_enabled=True,
    bot_llm_enabled=True,
    bot_speaking_enabled=True,
    metrics_enabled=False,
)


def build_services():
    """Construct the STT / LLM / TTS engines for one session."""
    stt = WhisperSTTService(
        # Pass the enum's string .value — passing the enum object leaks it into
        # faster-whisper's filename check (TypeError). A real gotcha.
        settings=WhisperSTTService.Settings(model=WhisperModel.LARGE_V3_TURBO.value),
        device=STT_DEVICE,
        compute_type=STT_COMPUTE,
    )
    # Strip markdown before speaking — a model emits *italics*/**bold** even when told
    # not to, and Kokoro reads the asterisks aloud literally ("asterisk").
    tts = KokoroTTSService(
        settings=KokoroTTSService.Settings(voice=TTS_VOICE),
        text_filters=[MarkdownTextFilter()],
    )
    # sticky user rides in the standard OpenAI `user` field via extra.
    llm = OpenAILLMService(
        api_key=LLM_API_KEY,
        base_url=LLM_BASE_URL,
        settings=OpenAILLMService.Settings(
            model=LLM_MODEL,
            system_instruction=PERSONA,
            temperature=LLM_TEMPERATURE,
            extra={"user": STICKY_USER},
        ),
    )
    return stt, llm, tts


async def run_bot(transport: BaseTransport, runner_args: RunnerArguments):
    """Run one CAPCOM voice session over the given transport."""
    logger.info(f"CAPCOM starting — model={LLM_MODEL} user={STICKY_USER} @ {LLM_BASE_URL}")
    stt, llm, tts = build_services()

    context = LLMContext()
    user_aggregator, assistant_aggregator = LLMContextAggregatorPair(
        context,
        # The VAD gates the user turn AND drives barge-in: talk over CAPCOM and
        # Pipecat cancels the in-flight TTS and listens.
        user_params=LLMUserAggregatorParams(vad_analyzer=SileroVADAnalyzer()),
    )

    # A wake word, when configured, gates transcriptions right after STT: nothing
    # reaches the mind until CAPCOM hears its name, then it stays awake through the
    # keepalive so follow-ups don't need repeating. The greeting is unaffected (it's
    # a developer message, not a user transcription), so CAPCOM still calls in first.
    processors = [transport.input(), stt]
    if WAKE_PHRASES:
        from pipecat.processors.filters import WakeCheckFilter

        processors.append(
            WakeCheckFilter(wake_phrases=WAKE_PHRASES, keepalive_timeout=WAKE_KEEPALIVE)
        )
        logger.info(f"Wake word active: {WAKE_PHRASES} (keepalive {WAKE_KEEPALIVE}s)")
    processors += [user_aggregator, llm, tts, transport.output(), assistant_aggregator]

    pipeline = Pipeline(processors)

    rtvi = RTVIProcessor()
    worker = PipelineWorker(
        pipeline,
        params=PipelineParams(enable_metrics=True, enable_usage_metrics=True),
        rtvi_processor=rtvi,
        rtvi_observer_params=RTVI_OBSERVER_PARAMS,
    )

    @transport.event_handler("on_client_connected")
    async def on_client_connected(transport, client):
        logger.info("Crew connected — CAPCOM going on the loop")
        # A developer-role nudge + an LLMRunFrame makes CAPCOM speak first.
        context.add_message({"role": "developer", "content": GREETING})
        await worker.queue_frames([LLMRunFrame()])

    @transport.event_handler("on_client_disconnected")
    async def on_client_disconnected(transport, client):
        logger.info("Crew disconnected — CAPCOM off the loop")
        await worker.cancel()

    from pipecat.workers.runner import WorkerRunner

    runner = WorkerRunner(handle_sigint=runner_args.handle_sigint)
    await runner.add_workers(worker)
    await runner.run()


async def bot(runner_args: RunnerArguments):
    """Entry point the Pipecat runner calls per session."""
    transport = await create_transport(runner_args, transport_params)
    await run_bot(transport, runner_args)


if __name__ == "__main__":
    from pipecat.runner.run import main

    main()
