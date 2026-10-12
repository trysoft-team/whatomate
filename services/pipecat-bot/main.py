"""Whatomate Pipecat sidecar — realtime AI Call voice loop.

Whatomate owns WhatsApp WebRTC. This service receives Opus over WebSocket,
runs STT → LLM (+ tools) → TTS, and signals transfer/hangup back to Go.
"""

from __future__ import annotations

import asyncio
import audioop
import base64
import json
import logging
import os
import struct
import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional, Tuple

import httpx
from fastapi import Depends, FastAPI, Header, HTTPException, WebSocket, WebSocketDisconnect
from fastapi.responses import JSONResponse

# opuslib uses ctypes.util.find_library('opus'), which often misses Homebrew.
import ctypes.util

_orig_find_library = ctypes.util.find_library


def _find_opus_library(name: str):
    if name == "opus":
        for path in (
            os.environ.get("OPUS_LIB"),
            "/opt/homebrew/lib/libopus.dylib",
            "/usr/local/lib/libopus.dylib",
            "/usr/lib/x86_64-linux-gnu/libopus.so.0",
            "/usr/lib/aarch64-linux-gnu/libopus.so.0",
        ):
            if path and os.path.exists(path):
                return path
    return _orig_find_library(name)


ctypes.util.find_library = _find_opus_library  # type: ignore[assignment]
import opuslib  # noqa: E402

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("pipecat-bot")
# Avoid logging full request URLs (API keys in query strings).
logging.getLogger("httpx").setLevel(logging.WARNING)
logging.getLogger("httpcore").setLevel(logging.WARNING)

AUTH_TOKEN = os.environ.get("PIPECAT_AUTH_TOKEN", "")
HOST = os.environ.get("PIPECAT_HOST", "0.0.0.0")
PORT = int(os.environ.get("PIPECAT_PORT", "7860"))
# Prefer Gemini Live (realtime) like pipecat-examples/whatsapp; set 0 to force turn-based.
USE_GEMINI_LIVE = os.environ.get("PIPECAT_GEMINI_LIVE", "1").strip().lower() not in (
    "0",
    "false",
    "no",
    "off",
)

# WhatsApp / WebRTC Opus is 48 kHz mono.
OPUS_RATE = 48000
PCM_RATE = 16000  # Whisper / most STT
FRAME_SAMPLES_48K = 960  # 20 ms at 48 kHz
VAD_RMS = 400
BARGE_RMS = 900  # higher during TTS to reduce echo false-triggers
BARGE_VOICED_FRAMES = 5  # ~100ms of speech before cutting TTS

app = FastAPI(title="Whatomate Pipecat Bot", version="0.1.0")


@dataclass
class Session:
    bootstrap: Dict[str, Any]
    created_at: float = field(default_factory=time.time)
    stop: asyncio.Event = field(default_factory=asyncio.Event)
    welcome_pcm: Optional[bytes] = None
    welcome_task: Optional[asyncio.Task] = None


sessions: Dict[str, Session] = {}

# Welcome / TTS PCM cache: overlaps Gemini generateContent (~7–10s) with ICE.
_tts_cache: Dict[str, bytes] = {}
_tts_cache_order: List[str] = []
_TTS_CACHE_MAX = 32
_tts_inflight: Dict[str, asyncio.Task] = {}


def _tts_cache_key(boot: Dict[str, Any], text: str) -> str:
    voice = (boot.get("voice_id") or "").strip() or "Puck"
    key = (boot.get("llm_api_key") or "").strip()
    # Fingerprint key without storing it.
    return f"{voice}|{len(key)}|{key[-6:] if key else ''}|{text}"


def tts_cache_get(boot: Dict[str, Any], text: str) -> Optional[bytes]:
    return _tts_cache.get(_tts_cache_key(boot, text))


def tts_cache_put(boot: Dict[str, Any], text: str, pcm: bytes) -> None:
    if not pcm or not text:
        return
    k = _tts_cache_key(boot, text)
    if k in _tts_cache:
        _tts_cache[k] = pcm
        return
    _tts_cache[k] = pcm
    _tts_cache_order.append(k)
    while len(_tts_cache_order) > _TTS_CACHE_MAX:
        old = _tts_cache_order.pop(0)
        _tts_cache.pop(old, None)


def require_auth(authorization: Optional[str] = Header(default=None)) -> None:
    if not AUTH_TOKEN:
        raise HTTPException(status_code=503, detail="PIPECAT_AUTH_TOKEN not configured")
    if not authorization or not authorization.startswith("Bearer "):
        raise HTTPException(status_code=401, detail="Unauthorized")
    if authorization[len("Bearer ") :].strip() != AUTH_TOKEN:
        raise HTTPException(status_code=401, detail="Unauthorized")


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/v1/sessions")
async def create_session(body: Dict[str, Any], _: None = Depends(require_auth)) -> JSONResponse:
    sid = body.get("session_id")
    if not sid:
        raise HTTPException(status_code=400, detail="session_id required")
    sess = Session(bootstrap=body)
    sessions[sid] = sess
    # Turn-based path only: prefetch welcome TTS (Live greets natively).
    welcome = (body.get("welcome_message") or "").strip()
    if (
        not USE_GEMINI_LIVE
        and welcome
        and (body.get("llm_api_key") or "").strip()
    ):
        cached = tts_cache_get(body, welcome)
        if cached:
            sess.welcome_pcm = cached
            log.info("welcome cache hit bytes=%d", len(cached))
        else:
            sess.welcome_task = asyncio.create_task(_prefetch_welcome(sess, welcome))
    log.info(
        "session created call_id=%s session_id=%s live=%s",
        body.get("call_id"),
        sid,
        USE_GEMINI_LIVE,
    )
    return JSONResponse({"session_id": sid, "ok": True, "gemini_live": USE_GEMINI_LIVE})


@app.post("/v1/warm")
async def warm_tts(body: Dict[str, Any], _: None = Depends(require_auth)) -> JSONResponse:
    """Synthesize welcome during ICE (turn-based path only)."""
    if USE_GEMINI_LIVE:
        return JSONResponse({"ok": True, "skipped": True, "reason": "gemini_live"})
    welcome = (body.get("welcome_message") or body.get("text") or "").strip()
    if not welcome:
        raise HTTPException(status_code=400, detail="welcome_message required")
    if not (body.get("llm_api_key") or "").strip():
        raise HTTPException(status_code=400, detail="llm_api_key required")
    cached = tts_cache_get(body, welcome)
    if cached:
        log.info("warm cache hit bytes=%d", len(cached))
        return JSONResponse({"ok": True, "cached": True, "bytes": len(cached)})
    # Fire-and-forget; create_session will reuse via cache or await same synth.
    asyncio.create_task(_warm_synthesize(body, welcome))
    return JSONResponse({"ok": True, "cached": False, "started": True})


async def _warm_synthesize(boot: Dict[str, Any], welcome: str) -> None:
    t0 = time.time()
    try:
        pcm = await synthesize(boot, welcome)
        if pcm:
            tts_cache_put(boot, welcome, pcm)
            log.info("warm done bytes=%d ms=%d", len(pcm), int((time.time() - t0) * 1000))
        else:
            log.warning("warm produced no audio ms=%d", int((time.time() - t0) * 1000))
    except Exception:
        log.exception("warm failed")


async def _prefetch_welcome(sess: Session, welcome: str) -> None:
    t0 = time.time()
    try:
        cached = tts_cache_get(sess.bootstrap, welcome)
        if cached:
            sess.welcome_pcm = cached
            log.info("welcome prefetched from cache bytes=%d", len(cached))
            return
        pcm = await synthesize(sess.bootstrap, welcome)
        sess.welcome_pcm = pcm or b""
        if pcm:
            tts_cache_put(sess.bootstrap, welcome, pcm)
        log.info(
            "welcome prefetched bytes=%d ms=%d",
            len(sess.welcome_pcm),
            int((time.time() - t0) * 1000),
        )
    except Exception:
        log.exception("welcome prefetch failed")
        sess.welcome_pcm = b""


@app.websocket("/v1/sessions/{session_id}/audio")
async def audio_ws(websocket: WebSocket, session_id: str) -> None:
    auth = websocket.headers.get("authorization") or websocket.query_params.get("token")
    if AUTH_TOKEN:
        token = ""
        if auth and auth.lower().startswith("bearer "):
            token = auth[7:].strip()
        elif auth:
            token = auth.strip()
        if token != AUTH_TOKEN:
            await websocket.close(code=4401)
            return

    sess = sessions.get(session_id)
    if not sess:
        await websocket.close(code=4404)
        return

    await websocket.accept()
    await websocket.send_text(json.dumps({"type": "ready"}))
    log.info("audio ws connected session_id=%s", session_id)

    try:
        await run_voice_loop(websocket, sess)
    except WebSocketDisconnect:
        log.info("audio ws disconnected session_id=%s", session_id)
    except Exception:
        log.exception("audio ws failed session_id=%s", session_id)
        try:
            await notify_whatomate(sess.bootstrap, "error", error="voice loop failed")
        except Exception:
            pass
    finally:
        sessions.pop(session_id, None)


async def run_voice_loop(ws: WebSocket, sess: Session) -> None:
    """Duplex voice loop: Gemini Live (realtime) preferred, turn-based fallback."""
    boot = sess.bootstrap

    if USE_GEMINI_LIVE and (boot.get("llm_api_key") or "").strip():
        from gemini_live import run_gemini_live

        handled = await run_gemini_live(ws, boot, notify=notify_whatomate)
        if handled:
            return

    await run_voice_loop_turn_based(ws, sess)


async def run_voice_loop_turn_based(ws: WebSocket, sess: Session) -> None:
    """Legacy duplex loop with interruptible TTS (barge-in).

    Collects Opus until ~1.2s of silence (or max utterance), transcribes,
    calls LLM (with tools), speaks reply as Opus frames. Caller speech
    during TTS aborts playback and becomes the next user turn.
    """
    boot = sess.bootstrap
    decoder = opuslib.Decoder(OPUS_RATE, 1)
    encoder = opuslib.Encoder(OPUS_RATE, 1, opuslib.APPLICATION_VOIP)

    history: list[Dict[str, Any]] = [
        {"role": "system", "content": boot.get("system_prompt") or "You are a helpful phone assistant."}
    ]

    # Speak welcome first if present (interruptible). Prefer prefetched PCM
    # (warmed during ICE / session create) so we don't pay TTS latency twice.
    welcome = (boot.get("welcome_message") or "").strip()
    barge = bytearray()
    if welcome:
        history.append({"role": "assistant", "content": welcome})
        pcm = await _await_welcome_pcm(sess, timeout=15.0)
        if pcm:
            barge = await play_pcm16(ws, encoder, decoder, pcm)
        else:
            barge = await speak_text(ws, encoder, decoder, boot, welcome)

    max_secs = int(boot.get("max_session_secs") or 600)
    deadline = time.time() + max_secs
    pcm_buf = bytearray(barge)
    last_voice = time.time() if barge else time.time()
    uttered = bool(barge)

    while time.time() < deadline and not sess.stop.is_set():
        try:
            msg = await asyncio.wait_for(ws.receive(), timeout=0.5)
        except asyncio.TimeoutError:
            if uttered and time.time() - last_voice > 1.2 and len(pcm_buf) > PCM_RATE * 2:
                text = await transcribe(boot, bytes(pcm_buf))
                pcm_buf.clear()
                uttered = False
                if text.strip():
                    log.info("stt text=%r", text.strip()[:120])
                    history.append({"role": "user", "content": text.strip()})
                    reply, action = await llm_turn(boot, history)
                    if action == "transfer":
                        await notify_whatomate(boot, "transfer", team_id=boot.get("transfer_team_id") or "")
                        await ws.send_text(json.dumps({"type": "event", "event": "transfer"}))
                        return
                    if action == "hangup":
                        # Finish last words before disconnecting.
                        bye = (reply or "").strip() or "Thank you for calling. Goodbye."
                        await speak_text(ws, encoder, decoder, boot, bye)
                        await notify_whatomate(boot, "hangup")
                        await ws.send_text(json.dumps({"type": "event", "event": "hangup"}))
                        return
                    if reply:
                        history.append({"role": "assistant", "content": reply})
                        barge = await speak_text(ws, encoder, decoder, boot, reply)
                        if barge:
                            pcm_buf.clear()
                            pcm_buf.extend(barge)
                            uttered = True
                            last_voice = time.time()
            continue

        if msg.get("type") == "websocket.disconnect":
            return
        data = msg.get("bytes")
        if data:
            pcm16 = _decode_uplink(decoder, data)
            if pcm16 is None:
                continue
            energy = audioop.rms(pcm16, 2)
            if energy > VAD_RMS:
                last_voice = time.time()
                uttered = True
                pcm_buf.extend(pcm16)
            elif uttered:
                pcm_buf.extend(pcm16)
            if len(pcm_buf) > PCM_RATE * 2 * 12:
                last_voice = 0
            continue

        text_payload = msg.get("text")
        if text_payload:
            try:
                ev = json.loads(text_payload)
            except json.JSONDecodeError:
                continue
            if ev.get("event") in ("stop", "hangup"):
                return


def _decode_uplink(decoder: opuslib.Decoder, opus_packet: bytes) -> Optional[bytes]:
    """Decode one Opus packet to mono PCM16 @ 16 kHz, or None on error."""
    try:
        pcm48 = decoder.decode(opus_packet, FRAME_SAMPLES_48K)
    except opuslib.OpusError:
        return None
    pcm16, _ = audioop.ratecv(pcm48, 2, 1, OPUS_RATE, PCM_RATE, None)
    return pcm16


async def _await_welcome_pcm(sess: Session, timeout: float = 15.0) -> bytes:
    """Wait for session-create / ICE warm prefetch before speaking welcome."""
    if sess.welcome_pcm is not None:
        return sess.welcome_pcm
    task = sess.welcome_task
    if task is None:
        # Try global TTS cache (warmed during ICE before this session existed).
        welcome = (sess.bootstrap.get("welcome_message") or "").strip()
        if welcome:
            cached = tts_cache_get(sess.bootstrap, welcome)
            if cached:
                sess.welcome_pcm = cached
                return cached
        return b""
    try:
        await asyncio.wait_for(asyncio.shield(task), timeout=timeout)
    except asyncio.TimeoutError:
        log.warning("welcome prefetch timed out after %.0fs", timeout)
    except Exception:
        log.exception("welcome prefetch wait failed")
    return sess.welcome_pcm or b""


async def play_pcm16(
    ws: WebSocket,
    encoder: opuslib.Encoder,
    decoder: opuslib.Decoder,
    pcm16: bytes,
) -> bytearray:
    """Play pre-synthesized PCM16@16k with barge-in; return barge PCM if interrupted."""
    if not pcm16:
        return bytearray()
    pcm48, _ = audioop.ratecv(pcm16, 2, 1, PCM_RATE, OPUS_RATE, None)
    return await _play_pcm48_interruptible(ws, encoder, decoder, pcm48)


async def speak_text(
    ws: WebSocket,
    encoder: opuslib.Encoder,
    decoder: opuslib.Decoder,
    boot: Dict[str, Any],
    text: str,
) -> bytearray:
    """Stream TTS; prefer chunked Gemini playback for lower time-to-first-audio."""
    llm_key = (boot.get("llm_api_key") or "").strip()
    voice_id = (boot.get("voice_id") or "").strip() or "Puck"
    if llm_key:
        barge = await speak_gemini_streaming(ws, encoder, decoder, llm_key, text, voice_id)
        if barge is not None:
            return barge
    pcm16 = await synthesize(boot, text)
    if not pcm16:
        return bytearray()
    return await play_pcm16(ws, encoder, decoder, pcm16)


async def speak_gemini_streaming(
    ws: WebSocket,
    encoder: opuslib.Encoder,
    decoder: opuslib.Decoder,
    api_key: str,
    text: str,
    voice_id: str,
) -> Optional[bytearray]:
    """Play Gemini TTS via streamGenerateContent; None if streaming unavailable."""
    voice = _gemini_voice_name(voice_id)
    body = {
        "contents": [{"parts": [{"text": text}]}],
        "generationConfig": {
            "responseModalities": ["AUDIO"],
            "speechConfig": {
                "voiceConfig": {
                    "prebuiltVoiceConfig": {"voiceName": voice},
                }
            },
        },
    }
    model = GEMINI_TTS_MODELS[0]
    url = (
        f"https://generativelanguage.googleapis.com/v1beta/models/"
        f"{model}:streamGenerateContent?alt=sse&key={api_key}"
    )
    t0 = time.time()
    first_ms = -1
    pcm24_buf = bytearray()
    frame_bytes_24 = int(GEMINI_TTS_RATE * 0.02) * 2  # 20ms @ 24k mono s16
    barge = bytearray()
    voiced_run = 0
    sent_frames = 0

    try:
        async with httpx.AsyncClient(timeout=60.0) as client:
            async with client.stream("POST", url, json=body) as resp:
                if resp.status_code >= 300:
                    log.warning("gemini tts stream failed %s", resp.status_code)
                    return None
                async for line in resp.aiter_lines():
                    if not line:
                        continue
                    if line.startswith("data:"):
                        payload = line[5:].strip()
                    else:
                        payload = line.strip()
                    if not payload or payload == "[DONE]":
                        continue
                    try:
                        data = json.loads(payload)
                    except json.JSONDecodeError:
                        continue
                    # streamGenerateContent may return a list or object
                    chunks = data if isinstance(data, list) else [data]
                    for item in chunks:
                        parts = (((item.get("candidates") or [{}])[0].get("content") or {}).get("parts")) or []
                        for p in parts:
                            inline = p.get("inlineData") or p.get("inline_data") or {}
                            b64 = inline.get("data") or ""
                            if not b64:
                                continue
                            mime = (inline.get("mimeType") or inline.get("mime_type") or "").lower()
                            raw = base64.b64decode(b64)
                            pcm24_buf.extend(_gemini_audio_to_pcm24(raw, mime))

                            while len(pcm24_buf) >= frame_bytes_24:
                                frame24 = bytes(pcm24_buf[:frame_bytes_24])
                                del pcm24_buf[:frame_bytes_24]
                                frame16, _ = audioop.ratecv(frame24, 2, 1, GEMINI_TTS_RATE, PCM_RATE, None)
                                frame48, _ = audioop.ratecv(frame16, 2, 1, PCM_RATE, OPUS_RATE, None)
                                # Pad/trim to exact Opus frame
                                need = FRAME_SAMPLES_48K * 2
                                if len(frame48) < need:
                                    frame48 = frame48 + b"\x00" * (need - len(frame48))
                                else:
                                    frame48 = frame48[:need]
                                try:
                                    packet = encoder.encode(frame48, FRAME_SAMPLES_48K)
                                except opuslib.OpusError:
                                    continue
                                await ws.send_bytes(packet)
                                sent_frames += 1
                                if first_ms < 0:
                                    first_ms = int((time.time() - t0) * 1000)
                                    log.info(
                                        "gemini tts first_audio_ms=%d model=%s voice=%s",
                                        first_ms,
                                        model,
                                        voice,
                                    )

                                # Barge-in listen for ~20ms
                                slice_deadline = time.time() + 0.02
                                while time.time() < slice_deadline:
                                    remaining = slice_deadline - time.time()
                                    if remaining <= 0:
                                        break
                                    try:
                                        msg = await asyncio.wait_for(ws.receive(), timeout=remaining)
                                    except asyncio.TimeoutError:
                                        break
                                    if msg.get("type") == "websocket.disconnect":
                                        raise WebSocketDisconnect()
                                    data_b = msg.get("bytes")
                                    if not data_b:
                                        continue
                                    uplink = _decode_uplink(decoder, data_b)
                                    if uplink is None:
                                        continue
                                    energy = audioop.rms(uplink, 2)
                                    if energy > BARGE_RMS:
                                        voiced_run += 1
                                        barge.extend(uplink)
                                        if voiced_run >= BARGE_VOICED_FRAMES:
                                            log.info("barge-in detected (%d frames)", voiced_run)
                                            try:
                                                await ws.send_text(
                                                    json.dumps({"type": "event", "event": "interrupt"})
                                                )
                                            except Exception:
                                                pass
                                            return barge
                                    else:
                                        if voiced_run > 0:
                                            barge.extend(uplink)
                                        voiced_run = 0

        # Flush remainder
        if pcm24_buf and sent_frames > 0:
            pad = frame_bytes_24 - (len(pcm24_buf) % frame_bytes_24)
            if pad != frame_bytes_24:
                pcm24_buf.extend(b"\x00" * pad)
            while len(pcm24_buf) >= frame_bytes_24:
                frame24 = bytes(pcm24_buf[:frame_bytes_24])
                del pcm24_buf[:frame_bytes_24]
                frame16, _ = audioop.ratecv(frame24, 2, 1, GEMINI_TTS_RATE, PCM_RATE, None)
                frame48, _ = audioop.ratecv(frame16, 2, 1, PCM_RATE, OPUS_RATE, None)
                need = FRAME_SAMPLES_48K * 2
                if len(frame48) < need:
                    frame48 = frame48 + b"\x00" * (need - len(frame48))
                else:
                    frame48 = frame48[:need]
                try:
                    packet = encoder.encode(frame48, FRAME_SAMPLES_48K)
                except opuslib.OpusError:
                    continue
                await ws.send_bytes(packet)
                await asyncio.sleep(0.02)

        if sent_frames == 0:
            return None
        log.info("gemini tts stream done frames=%d first_ms=%d", sent_frames, first_ms)
        return bytearray()
    except WebSocketDisconnect:
        raise
    except Exception:
        log.exception("gemini tts stream error")
        return None


async def _play_pcm48_interruptible(
    ws: WebSocket,
    encoder: opuslib.Encoder,
    decoder: opuslib.Decoder,
    pcm48: bytes,
) -> bytearray:
    frame_bytes = FRAME_SAMPLES_48K * 2
    packets: List[bytes] = []
    for i in range(0, len(pcm48) - frame_bytes + 1, frame_bytes):
        frame = pcm48[i : i + frame_bytes]
        try:
            packets.append(encoder.encode(frame, FRAME_SAMPLES_48K))
        except opuslib.OpusError:
            continue

    barge = bytearray()
    voiced_run = 0
    for packet in packets:
        await ws.send_bytes(packet)
        slice_deadline = time.time() + 0.02
        while time.time() < slice_deadline:
            remaining = slice_deadline - time.time()
            if remaining <= 0:
                break
            try:
                msg = await asyncio.wait_for(ws.receive(), timeout=remaining)
            except asyncio.TimeoutError:
                break
            if msg.get("type") == "websocket.disconnect":
                raise WebSocketDisconnect()
            data = msg.get("bytes")
            if not data:
                continue
            uplink = _decode_uplink(decoder, data)
            if uplink is None:
                continue
            energy = audioop.rms(uplink, 2)
            if energy > BARGE_RMS:
                voiced_run += 1
                barge.extend(uplink)
                if voiced_run >= BARGE_VOICED_FRAMES:
                    log.info("barge-in detected (%d frames)", voiced_run)
                    try:
                        await ws.send_text(json.dumps({"type": "event", "event": "interrupt"}))
                    except Exception:
                        pass
                    return barge
            else:
                if voiced_run > 0:
                    barge.extend(uplink)
                voiced_run = 0
    return bytearray()


async def transcribe(boot: Dict[str, Any], pcm16: bytes) -> str:
    """STT: Gemini multimodal when Google key is present, else OpenAI Whisper."""
    wav = pcm16_to_wav(pcm16, PCM_RATE)
    provider = (boot.get("llm_provider") or "").lower()
    llm_key = (boot.get("llm_api_key") or "").strip()

    # Prefer Gemini whenever we have a Google/Gemini key (API keys often start with AQ.).
    if llm_key and (provider in ("google", "gemini", "") or llm_key.startswith("AQ.")):
        text = await transcribe_gemini(llm_key, boot.get("llm_model") or "", wav, boot.get("locale") or "")
        if text:
            return text

    openai_key = (boot.get("openai_key") or "").strip()
    if not openai_key and provider == "openai":
        openai_key = llm_key
    if openai_key and not openai_key.startswith("AQ."):
        text = await transcribe_whisper(openai_key, wav, boot.get("locale") or "")
        if text:
            return text

    log.warning(
        "no STT result (provider=%s gemini=%s openai=%s)",
        provider or "?",
        bool(llm_key),
        bool(openai_key and not openai_key.startswith("AQ.")),
    )
    return ""


async def transcribe_gemini(api_key: str, model: str, wav: bytes, locale: str) -> str:
    """Transcribe WAV via Gemini multimodal generateContent (inline audio)."""
    preferred = (model or "").strip() or "gemini-2.0-flash"
    # Org chat models (e.g. gemini-3.6-flash) may 503 on audio; try audio-capable fallbacks.
    candidates = []
    for m in (
        preferred,
        "gemini-2.0-flash",
        "gemini-2.5-flash",
        "gemini-1.5-flash",
        "gemini-2.5-flash-lite",
    ):
        if m and m not in candidates and not m.startswith("gemini-1.0"):
            candidates.append(m)

    lang_hint = ""
    lang = (locale or "").strip()
    if lang:
        lang_hint = f" The spoken language is likely {lang}."
    prompt = (
        "Transcribe this phone-call audio. Return ONLY the caller's spoken words "
        "as plain text with no quotes, labels, or commentary. If there is no speech, "
        "return an empty string."
        + lang_hint
    )
    body = {
        "contents": [
            {
                "role": "user",
                "parts": [
                    {"text": prompt},
                    {
                        "inline_data": {
                            "mime_type": "audio/wav",
                            "data": base64.b64encode(wav).decode("ascii"),
                        }
                    },
                ],
            }
        ],
        "generationConfig": {"maxOutputTokens": 256, "temperature": 0},
    }

    async with httpx.AsyncClient(timeout=60.0) as client:
        last_err = ""
        for model_id in candidates:
            url = (
                f"https://generativelanguage.googleapis.com/v1beta/models/"
                f"{model_id}:generateContent?key={api_key}"
            )
            for attempt in range(3):
                resp = await client.post(url, json=body)
                if resp.status_code in (429, 503):
                    last_err = f"{model_id} {resp.status_code}"
                    await asyncio.sleep(0.4 * (attempt + 1))
                    continue
                if resp.status_code >= 300:
                    last_err = f"{model_id} {resp.status_code} {resp.text[:160]}"
                    break  # try next model
                data = resp.json()
                parts = (((data.get("candidates") or [{}])[0].get("content") or {}).get("parts")) or []
                text = " ".join(p.get("text") or "" for p in parts).strip()
                low = text.lower()
                if low in ("", "empty", "(empty)", "n/a", "none", "no speech", "silence"):
                    return ""
                log.info("gemini stt ok model=%s chars=%d", model_id, len(text))
                return text
        log.warning("gemini stt failed %s", last_err)
        return ""


async def transcribe_whisper(api_key: str, wav: bytes, locale: str) -> str:
    async with httpx.AsyncClient(timeout=60.0) as client:
        files = {"file": ("audio.wav", wav, "audio/wav")}
        data = {"model": "whisper-1"}
        lang = (locale or "")[:2]
        if lang:
            data["language"] = lang
        resp = await client.post(
            "https://api.openai.com/v1/audio/transcriptions",
            headers={"Authorization": f"Bearer {api_key}"},
            files=files,
            data=data,
        )
        if resp.status_code >= 300:
            log.warning("whisper failed %s %s", resp.status_code, resp.text[:200])
            return ""
        return (resp.json().get("text") or "").strip()


# Gemini TTS voices (same set as Pipecat GeminiTTSService.AVAILABLE_VOICES)
GEMINI_TTS_VOICES = {
    "Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Aoede", "Autonoe",
    "Callirrhoe", "Charon", "Despina", "Enceladus", "Erinome", "Fenrir",
    "Gacrux", "Iapetus", "Kore", "Laomedeia", "Leda", "Orus", "Puck",
    "Pulcherrima", "Rasalgethi", "Sadachbia", "Sadaltager", "Schedar",
    "Sulafat", "Umbriel", "Vindemiatrix", "Zephyr", "Zubenelgenubi",
}
# Prefer the GenAI-API model names GeminiTTSService uses with an API key.
GEMINI_TTS_MODELS = (
    "gemini-2.5-flash-preview-tts",
    "gemini-3.1-flash-tts-preview",
    "gemini-2.5-pro-preview-tts",
)
GEMINI_TTS_RATE = 24000  # GeminiTTSService.GOOGLE_SAMPLE_RATE


async def synthesize(boot: Dict[str, Any], text: str) -> bytes:
    """Return mono PCM16 @ 16 kHz via Gemini TTS (GeminiTTSService GenAI path)."""
    if not text:
        return b""
    cached = tts_cache_get(boot, text)
    if cached:
        return cached

    key = _tts_cache_key(boot, text)
    existing = _tts_inflight.get(key)
    if existing and not existing.done():
        return await existing

    async def _do() -> bytes:
        try:
            return await _synthesize_uncached(boot, text)
        finally:
            _tts_inflight.pop(key, None)

    task = asyncio.create_task(_do())
    _tts_inflight[key] = task
    return await task


async def _synthesize_uncached(boot: Dict[str, Any], text: str) -> bytes:
    llm_key = (boot.get("llm_api_key") or "").strip()
    voice_id = (boot.get("voice_id") or "").strip() or "Puck"

    if llm_key:
        pcm = await tts_gemini(llm_key, text, voice_id)
        if pcm:
            tts_cache_put(boot, text, pcm)
            return pcm

    openai_key = (boot.get("openai_key") or "").strip()
    if openai_key and not openai_key.startswith("AQ."):
        pcm = await tts_openai(openai_key, "alloy", text)
        if pcm:
            tts_cache_put(boot, text, pcm)
            return pcm

    log.warning("no TTS credentials (need Google llm_api_key for Gemini TTS)")
    return b""


def _gemini_voice_name(voice_id: str) -> str:
    v = (voice_id or "").strip()
    if v in GEMINI_TTS_VOICES:
        return v
    # Case-insensitive match
    for name in GEMINI_TTS_VOICES:
        if name.lower() == v.lower():
            return name
    return "Puck"  # same default as voice-google-gemini.py


async def tts_gemini(api_key: str, text: str, voice_id: str) -> bytes:
    """Gemini TTS via generateContent AUDIO modality (GeminiTTSService GenAI path)."""
    voice = _gemini_voice_name(voice_id)
    body = {
        "contents": [{"parts": [{"text": text}]}],
        "generationConfig": {
            "responseModalities": ["AUDIO"],
            "speechConfig": {
                "voiceConfig": {
                    "prebuiltVoiceConfig": {"voiceName": voice},
                }
            },
        },
    }
    async with httpx.AsyncClient(timeout=60.0) as client:
        last_err = ""
        for model in GEMINI_TTS_MODELS:
            url = (
                f"https://generativelanguage.googleapis.com/v1beta/models/"
                f"{model}:generateContent?key={api_key}"
            )
            resp = await client.post(url, json=body)
            if resp.status_code >= 300:
                last_err = f"{model} {resp.status_code} {resp.text[:160]}"
                continue
            data = resp.json()
            parts = (((data.get("candidates") or [{}])[0].get("content") or {}).get("parts")) or []
            for p in parts:
                inline = p.get("inlineData") or p.get("inline_data") or {}
                b64 = inline.get("data") or ""
                mime = (inline.get("mimeType") or inline.get("mime_type") or "").lower()
                if not b64:
                    continue
                raw = base64.b64decode(b64)
                pcm24 = _gemini_audio_to_pcm24(raw, mime)
                if not pcm24:
                    continue
                pcm16, _ = audioop.ratecv(pcm24, 2, 1, GEMINI_TTS_RATE, PCM_RATE, None)
                log.info("gemini tts ok model=%s voice=%s bytes=%d", model, voice, len(pcm16))
                return pcm16
            last_err = f"{model} no audio parts"
        log.warning("gemini tts failed %s", last_err)
        return b""


def _gemini_audio_to_pcm24(raw: bytes, mime: str) -> bytes:
    """Normalize Gemini TTS payload to mono PCM16 @ 24 kHz."""
    if not raw:
        return b""
    if "wav" in mime or raw[:4] == b"RIFF":
        return wav_to_pcm16(raw)
    # Most GenAI TTS responses are raw LINEAR16 @ 24 kHz.
    return raw


async def tts_openai(api_key: str, voice: str, text: str) -> bytes:
    async with httpx.AsyncClient(timeout=60.0) as client:
        resp = await client.post(
            "https://api.openai.com/v1/audio/speech",
            headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
            json={"model": "gpt-4o-mini-tts", "voice": voice or "alloy", "input": text, "response_format": "wav"},
        )
        if resp.status_code >= 300:
            # Fallback model
            resp = await client.post(
                "https://api.openai.com/v1/audio/speech",
                headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
                json={"model": "tts-1", "voice": voice or "alloy", "input": text, "response_format": "wav"},
            )
        if resp.status_code >= 300:
            log.warning("openai tts failed %s", resp.status_code)
            return b""
        return wav_to_pcm16(resp.content)


async def llm_turn(boot: Dict[str, Any], history: list[Dict[str, Any]]) -> Tuple[str, Optional[str]]:
    """Returns (reply_text, action) where action is transfer|hangup|None."""
    provider = (boot.get("llm_provider") or "openai").lower()
    tools = build_openai_tools(boot)
    if provider in ("google", "gemini"):
        return await llm_google(boot, history, tools)
    return await llm_openai(boot, history, tools)


def build_openai_tools(boot: Dict[str, Any]) -> list[Dict[str, Any]]:
    tools = [
        {
            "type": "function",
            "function": {
                "name": "transfer_to_agent",
                "description": "Transfer the caller to a human agent.",
                "parameters": {
                    "type": "object",
                    "properties": {"reason": {"type": "string"}},
                },
            },
        },
        {
            "type": "function",
            "function": {
                "name": "end_call",
                "description": (
                    "End the call politely when the caller is done. "
                    "Speak a brief goodbye in your reply before or when calling this."
                ),
                "parameters": {"type": "object", "properties": {}},
            },
        },
    ]
    for t in boot.get("tools") or []:
        name = t.get("name")
        if not name or name in ("transfer_to_agent", "end_call"):
            continue
        tools.append(
            {
                "type": "function",
                "function": {
                    "name": name,
                    "description": t.get("description") or name,
                    "parameters": t.get("parameters") or {"type": "object", "properties": {}},
                },
            }
        )
    return tools


async def llm_openai(boot: Dict[str, Any], history: list[Dict[str, Any]], tools: list[Dict[str, Any]]) -> Tuple[str, Optional[str]]:
    key = boot.get("llm_api_key") or ""
    model = boot.get("llm_model") or "gpt-4o-mini"
    max_tokens = int(boot.get("llm_max_tokens") or 300)
    async with httpx.AsyncClient(timeout=60.0) as client:
        body: Dict[str, Any] = {
            "model": model,
            "messages": history,
            "max_tokens": max_tokens,
            "tools": tools,
            "tool_choice": "auto",
        }
        resp = await client.post(
            "https://api.openai.com/v1/chat/completions",
            headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
            json=body,
        )
        if resp.status_code >= 300:
            log.warning("openai llm failed %s %s", resp.status_code, resp.text[:200])
            return "", None
        msg = resp.json()["choices"][0]["message"]
        return await handle_tool_calls(boot, history, msg)


async def llm_google(boot: Dict[str, Any], history: list[Dict[str, Any]], tools: list[Dict[str, Any]]) -> Tuple[str, Optional[str]]:
    """Google Gemini generateContent with function declarations."""
    key = boot.get("llm_api_key") or ""
    model = boot.get("llm_model") or "gemini-2.0-flash"
    max_tokens = int(boot.get("llm_max_tokens") or 300)
    contents = []
    system = ""
    for m in history:
        role = m.get("role")
        if role == "system":
            system = m.get("content") or ""
            continue
        contents.append(
            {
                "role": "user" if role == "user" else "model",
                "parts": [{"text": m.get("content") or ""}],
            }
        )
    decls = []
    for t in tools:
        fn = t.get("function") or {}
        decls.append(
            {
                "name": fn.get("name"),
                "description": fn.get("description") or "",
                "parameters": fn.get("parameters") or {"type": "object", "properties": {}},
            }
        )
    body: Dict[str, Any] = {
        "contents": contents,
        "generationConfig": {"maxOutputTokens": max_tokens},
        "tools": [{"functionDeclarations": decls}],
    }
    if system:
        body["systemInstruction"] = {"parts": [{"text": system}]}
    url = f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={key}"
    async with httpx.AsyncClient(timeout=60.0) as client:
        resp = await client.post(url, json=body)
        if resp.status_code >= 300:
            log.warning("google llm failed %s %s", resp.status_code, resp.text[:200])
            return "", None
        data = resp.json()
        parts = (((data.get("candidates") or [{}])[0].get("content") or {}).get("parts")) or []
        text_bits = []
        for p in parts:
            if "functionCall" in p:
                fc = p["functionCall"]
                name = fc.get("name") or ""
                args = fc.get("args") or {}
                action = await dispatch_tool(boot, name, args)
                if action:
                    return "", action
                # Feed tool result back as plain reply hint
                text_bits.append(f"(tool {name} ok)")
            elif "text" in p:
                text_bits.append(p["text"])
        return " ".join(text_bits).strip(), None


async def handle_tool_calls(boot: Dict[str, Any], history: list[Dict[str, Any]], msg: Dict[str, Any]) -> Tuple[str, Optional[str]]:
    tool_calls = msg.get("tool_calls") or []
    if not tool_calls:
        return (msg.get("content") or "").strip(), None
    history.append(msg)
    for tc in tool_calls:
        fn = tc.get("function") or {}
        name = fn.get("name") or ""
        try:
            args = json.loads(fn.get("arguments") or "{}")
        except json.JSONDecodeError:
            args = {}
        action = await dispatch_tool(boot, name, args)
        if action:
            # Preserve any spoken content so hangup can finish last words.
            return (msg.get("content") or "").strip(), action
        history.append(
            {
                "role": "tool",
                "tool_call_id": tc.get("id"),
                "content": json.dumps({"ok": True}),
            }
        )
    # One follow-up completion without tools for spoken reply
    return await llm_openai_followup(boot, history)


async def llm_openai_followup(boot: Dict[str, Any], history: list[Dict[str, Any]]) -> Tuple[str, Optional[str]]:
    key = boot.get("llm_api_key") or ""
    model = boot.get("llm_model") or "gpt-4o-mini"
    async with httpx.AsyncClient(timeout=60.0) as client:
        resp = await client.post(
            "https://api.openai.com/v1/chat/completions",
            headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
            json={"model": model, "messages": history, "max_tokens": int(boot.get("llm_max_tokens") or 300)},
        )
        if resp.status_code >= 300:
            return "", None
        return (resp.json()["choices"][0]["message"].get("content") or "").strip(), None


async def dispatch_tool(boot: Dict[str, Any], name: str, args: Dict[str, Any]) -> Optional[str]:
    if name in ("transfer_to_agent", "transfer"):
        return "transfer"
    if name in ("end_call", "hangup"):
        return "hangup"
    # Relay other tools to Whatomate
    call_id = boot.get("call_id") or ""
    cb = (boot.get("callback_url") or "").rstrip("/")
    token = boot.get("callback_token") or AUTH_TOKEN
    if not cb or not call_id:
        return None
    async with httpx.AsyncClient(timeout=30.0) as client:
        resp = await client.post(
            f"{cb}/api/internal/pipecat/tools/{call_id}",
            headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
            json={"name": name, "arguments": args},
        )
        if resp.status_code >= 300:
            log.warning("tool relay failed %s %s", name, resp.status_code)
    return None


async def notify_whatomate(boot: Dict[str, Any], event_type: str, team_id: str = "", error: str = "") -> None:
    cb = (boot.get("callback_url") or "").rstrip("/")
    token = boot.get("callback_token") or AUTH_TOKEN
    call_id = boot.get("call_id") or ""
    if not cb or not call_id:
        return
    payload = {
        "call_id": call_id,
        "type": event_type,
        "team_id": team_id or boot.get("transfer_team_id") or "",
        "error": error,
    }
    async with httpx.AsyncClient(timeout=15.0) as client:
        resp = await client.post(
            f"{cb}/api/internal/pipecat/events",
            headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
            json=payload,
        )
        log.info("notify whatomate type=%s status=%s", event_type, resp.status_code)


def pcm16_to_wav(pcm: bytes, rate: int) -> bytes:
    # Minimal WAV header
    data_size = len(pcm)
    header = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF",
        36 + data_size,
        b"WAVE",
        b"fmt ",
        16,
        1,
        1,
        rate,
        rate * 2,
        2,
        16,
        b"data",
        data_size,
    )
    return header + pcm


def wav_to_pcm16(wav: bytes) -> bytes:
    if len(wav) < 44 or wav[0:4] != b"RIFF":
        return wav
    # Find data chunk
    i = 12
    while i + 8 <= len(wav):
        chunk = wav[i : i + 4]
        size = struct.unpack_from("<I", wav, i + 4)[0]
        if chunk == b"data":
            return wav[i + 8 : i + 8 + size]
        i += 8 + size
    return wav[44:]


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("main:app", host=HOST, port=PORT, reload=False)
