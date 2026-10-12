"""Gemini Live (BidiGenerateContent) over Opus ↔ PCM for Whatomate AI Calls.

Matches the realtime path used by pipecat-examples/whatsapp (GeminiLiveLLMService):
continuous audio in/out instead of turn-based STT → LLM → TTS.
"""

from __future__ import annotations

import asyncio
import audioop
import base64
import ctypes.util
import io
import json
import logging
import os
import re
import time
import wave
from typing import Any, Dict, List, Optional, Tuple

import httpx
from fastapi import WebSocket, WebSocketDisconnect
from websockets.asyncio.client import connect as ws_connect

# opuslib uses ctypes.util.find_library('opus'), which often misses Homebrew.
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

log = logging.getLogger("pipecat-bot.live")

OPUS_RATE = 48000
PCM_IN_RATE = 16000  # Live API input
PCM_OUT_RATE = 24000  # Live API output
FRAME_SAMPLES_48K = 960  # 20 ms
REC_RATE = PCM_IN_RATE  # store/mix tracks at 16 kHz mono s16le
FRAME_DUR = 0.02  # 20 ms Opus pacing
# While assistant audio is playing, only forward uplink if RMS exceeds this
# (filters acoustic echo that otherwise self-interrupts Gemini mid-sentence).
# Sustained speech above threshold also flushes the local playback queue so
# barge-in feels instant (Gemini's `interrupted` follows shortly after).
BARGE_RMS_WHILE_PLAYING = 1400  # higher: WhatsApp speaker→mic echo is loud
BARGE_VOICED_FRAMES = 5  # ~100 ms sustained speech before local flush
PLAYING_HOLD_SECS = 0.12
# After end_call / farewell: drain audio, then notify hangup.
HANGUP_MIN_WAIT_SECS = 0.5
HANGUP_IDLE_SECS = 0.4
HANGUP_MAX_WAIT_SECS = 6.0
SOFT_CLIP = 28000
_SENT_END = re.compile(r"""[.!?…]["']?(?:\s+|$)""")
# Verbal goodbye without end_call tool (Live often speaks farewell and skips the tool).
_FAREWELL_RE = re.compile(
    r"(?i)\b(?:"
    r"good\s*bye|goodbye|bye-?bye|"
    r"have a (?:great|good|nice|wonderful) (?:day|one|evening|night)|"
    r"take care"
    r")\b|\bbye[.!]"
)
LIVE_HOST = "generativelanguage.googleapis.com"
# Prefer 3.1 Flash Live — native-audio-latest frequently closes mid-call with
# 1007 CONTENT_TYPE_AUDIO (Google-side; worse with tools). Keep native-audio
# dated previews as fallbacks; floating -latest last.
LIVE_MODELS = (
    "gemini-3.1-flash-live-preview",
    "gemini-2.5-flash-native-audio-preview-12-2025",
    "gemini-2.5-flash-native-audio-preview-09-2025",
    "gemini-2.5-flash-native-audio-latest",
)


async def run_gemini_live(
    ws: WebSocket,
    boot: Dict[str, Any],
    *,
    notify,
) -> bool:
    """Run a Live session bridged to the Whatomate Opus WebSocket.

    `notify(boot, event_type, team_id="", error="")` callbacks into Whatomate.
    Returns True if Live handled the call, False to fall back to turn-based.
    """
    api_key = (boot.get("llm_api_key") or "").strip()
    if not api_key:
        return False

    decoder = opuslib.Decoder(OPUS_RATE, 1)
    encoder = opuslib.Encoder(OPUS_RATE, 1, opuslib.APPLICATION_VOIP)
    voice = (boot.get("voice_id") or "").strip() or "Puck"
    system = (boot.get("system_prompt") or "You are a helpful phone assistant.").strip()
    welcome = (boot.get("welcome_message") or "").strip()
    system = (
        f"{system}\n\n"
        "Phone style: keep replies short and natural. "
        "If the caller speaks while you are talking, stop immediately and listen. "
        "When the caller is done or says goodbye: say a brief farewell AND call end_call. "
        "The line does not disconnect until end_call is called."
    )
    if welcome:
        system = (
            f"{system}\n\nWhen the call starts, greet the caller immediately with "
            f"this welcome (paraphrase naturally, keep it brief):\n{welcome}"
        )

    last_err = ""
    for model in LIVE_MODELS:
        try:
            await _live_session(
                wa_ws=ws,
                boot=boot,
                api_key=api_key,
                model=model,
                voice=voice,
                system=system,
                welcome=welcome,
                decoder=decoder,
                encoder=encoder,
                notify=notify,
            )
            return True
        except _LiveSetupError as e:
            last_err = str(e)
            log.warning("gemini live setup failed model=%s err=%s", model, last_err)
            continue
        except _LiveRetriableDrop as e:
            last_err = str(e)
            log.warning(
                "gemini live dropped early model=%s err=%s; trying next",
                model,
                last_err[:200],
            )
            continue
        except WebSocketDisconnect:
            raise
        except Exception:
            log.exception("gemini live session failed model=%s", model)
            return True  # don't double-speak with turn-based after a mid-call failure

    log.warning("gemini live unavailable (%s); falling back to turn-based", last_err or "no models")
    return False


class _LiveSetupError(Exception):
    pass


class _LiveRetriableDrop(Exception):
    """Session died before useful audio — safe to try the next model."""

    pass


async def _live_session(
    *,
    wa_ws: WebSocket,
    boot: Dict[str, Any],
    api_key: str,
    model: str,
    voice: str,
    system: str,
    welcome: str,
    decoder: opuslib.Decoder,
    encoder: opuslib.Encoder,
    notify,
) -> None:
    uri = (
        f"wss://{LIVE_HOST}/ws/google.ai.generativelanguage.v1beta."
        f"GenerativeService.BidiGenerateContent?key={api_key}"
    )
    t0 = time.time()
    async with ws_connect(uri, additional_headers={"Content-Type": "application/json"}) as live:
        setup_body: Dict[str, Any] = {
            "model": f"models/{model}",
            "generationConfig": {
                "responseModalities": ["AUDIO"],
                "speechConfig": {
                    "voiceConfig": {
                        "prebuiltVoiceConfig": {"voiceName": voice},
                    }
                },
            },
            "systemInstruction": {
                "parts": [{"text": system}],
            },
            "tools": _live_tools(boot),
            # Barge-in on user speech; echo gate in uplink filters speaker→mic loop.
            "realtimeInputConfig": {
                "automaticActivityDetection": {
                    "disabled": False,
                    # HIGH start so caller can interrupt mid-sentence; LOW end
                    # so ambient hiss doesn't hold the turn open forever.
                    "startOfSpeechSensitivity": "START_SENSITIVITY_HIGH",
                    "endOfSpeechSensitivity": "END_SENSITIVITY_LOW",
                    "prefixPaddingMs": 20,
                    "silenceDurationMs": 400,
                },
                "activityHandling": "START_OF_ACTIVITY_INTERRUPTS",
            },
        }
        if boot.get("transcription_enabled"):
            # Empty objects enable Live input/output audio transcription.
            setup_body["inputAudioTranscription"] = {}
            setup_body["outputAudioTranscription"] = {}
        setup = {"setup": setup_body}
        await live.send(json.dumps(setup))
        raw = await asyncio.wait_for(live.recv(), timeout=15.0)
        if isinstance(raw, bytes):
            raw = raw.decode("utf-8", errors="replace")
        setup_resp = json.loads(raw)
        if setup_resp.get("error"):
            raise _LiveSetupError(json.dumps(setup_resp["error"])[:240])
        if "setupComplete" not in setup_resp and "setup_complete" not in setup_resp:
            raise _LiveSetupError(json.dumps(setup_resp)[:240])

        log.info(
            "gemini live connected model=%s voice=%s setup_ms=%d",
            model,
            voice,
            int((time.time() - t0) * 1000),
        )

        # Kick off greeting. Prefer realtimeInput.text — required after setup for
        # gemini-3.1-flash-live-preview; also works on native-audio models.
        kick = welcome or "Greet the caller briefly and ask how you can help."
        await live.send(
            json.dumps(
                {
                    "realtimeInput": {
                        "text": (
                            "The phone call is now connected. "
                            f"Speak first now: {kick}"
                        )
                    }
                }
            )
        )

        out_q: asyncio.Queue[Optional[bytes]] = asyncio.Queue(maxsize=256)  # PCM24 chunks; None=stop, b""=flush
        stop = asyncio.Event()
        first_audio_ms = -1
        session_close_err = ""
        transcript_lines: list[dict[str, str]] = []
        do_record = bool(boot.get("recording_enabled"))
        do_transcript = bool(boot.get("transcription_enabled"))
        timeline = _TimelineRecorder(REC_RATE) if do_record else None
        asst_rate_state = None
        tx = _TranscriptAssembler()
        # Shared: uplink suppresses quiet echo while downlink is actively playing.
        playing_until = 0.0
        interrupt_count = 0
        hangup_req_at = 0.0  # monotonic; 0 = not requested
        hangup_task: Optional[asyncio.Task] = None
        playback_busy = False  # True while downlink has buffered PCM

        try:
            encoder.bitrate = 32000
        except Exception:
            pass

        def _flush_playback() -> None:
            """Drop queued assistant audio so barge-in / interrupt pauses immediately."""
            nonlocal playing_until, playback_busy
            playing_until = 0.0
            playback_busy = False
            while not out_q.empty():
                try:
                    out_q.get_nowait()
                except asyncio.QueueEmpty:
                    break
            try:
                out_q.put_nowait(b"")
            except asyncio.QueueFull:
                pass

        def _playback_idle() -> bool:
            # Prefer playing_until + queue; playback_busy can stick on partial frames.
            return out_q.empty() and time.monotonic() >= playing_until

        async def _graceful_hangup() -> None:
            """Drain farewell audio, then always notify hangup to Whatomate."""
            started = hangup_req_at or time.monotonic()
            deadline = started + HANGUP_MAX_WAIT_SECS
            idle_since = 0.0
            try:
                while not stop.is_set() and time.monotonic() < deadline:
                    elapsed = time.monotonic() - started
                    if _playback_idle():
                        if idle_since <= 0:
                            idle_since = time.monotonic()
                        elif (
                            elapsed >= HANGUP_MIN_WAIT_SECS
                            and (time.monotonic() - idle_since) >= HANGUP_IDLE_SECS
                        ):
                            break
                    else:
                        idle_since = 0.0
                    await asyncio.sleep(0.05)
                if not stop.is_set():
                    await asyncio.sleep(0.2)
            finally:
                # Always notify — cancel/timeout must not leave the call hanging.
                log.info(
                    "graceful hangup notify after %.0fms idle=%s",
                    (time.monotonic() - started) * 1000,
                    _playback_idle(),
                )
                try:
                    await notify(boot, "hangup")
                except Exception:
                    log.exception("hangup notify failed")
                try:
                    await wa_ws.send_text(json.dumps({"type": "event", "event": "hangup"}))
                except Exception:
                    pass
                stop.set()
                try:
                    out_q.put_nowait(None)
                except asyncio.QueueFull:
                    pass

        def _request_hangup(reason: str = "end_call") -> None:
            nonlocal hangup_req_at, hangup_task
            if hangup_req_at > 0:
                return
            hangup_req_at = time.monotonic()
            log.info("hangup requested reason=%s", reason)
            hangup_task = asyncio.create_task(_graceful_hangup())

        async def uplink() -> None:
            """WhatsApp Opus → Live PCM16@16k."""
            nonlocal playing_until
            rate_state = None
            barge_run = 0
            while not stop.is_set():
                try:
                    msg = await asyncio.wait_for(wa_ws.receive(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                if msg.get("type") == "websocket.disconnect":
                    stop.set()
                    return
                data = msg.get("bytes")
                if data:
                    try:
                        pcm48 = decoder.decode(data, FRAME_SAMPLES_48K)
                    except opuslib.OpusError:
                        continue
                    pcm16, rate_state = audioop.ratecv(
                        pcm48, 2, 1, OPUS_RATE, PCM_IN_RATE, rate_state
                    )
                    if timeline is not None:
                        timeline.write_user(pcm16)
                    # During farewell hangup, do not local-flush — let last words play.
                    playing = time.monotonic() < playing_until or playback_busy
                    if playing and hangup_req_at <= 0:
                        rms = audioop.rms(pcm16, 2)
                        if rms < BARGE_RMS_WHILE_PLAYING:
                            barge_run = 0
                            continue
                        barge_run += 1
                        if barge_run >= BARGE_VOICED_FRAMES:
                            _flush_playback()
                            barge_run = 0
                            log.info("local barge-in flush rms=%d", rms)
                    else:
                        barge_run = 0
                        # Still drop quiet echo while farewell audio is on the wire.
                        if playing and hangup_req_at > 0:
                            if audioop.rms(pcm16, 2) < BARGE_RMS_WHILE_PLAYING:
                                continue
                    payload = {
                        "realtimeInput": {
                            "audio": {
                                "data": base64.b64encode(pcm16).decode("ascii"),
                                "mimeType": f"audio/pcm;rate={PCM_IN_RATE}",
                            }
                        }
                    }
                    try:
                        await live.send(json.dumps(payload))
                    except Exception:
                        stop.set()
                        return
                    continue
                text_payload = msg.get("text")
                if text_payload:
                    try:
                        ev = json.loads(text_payload)
                    except json.JSONDecodeError:
                        continue
                    if ev.get("event") in ("stop", "hangup"):
                        stop.set()
                        return

        async def from_live() -> None:
            """Live WS → audio queue + tool calls."""
            nonlocal first_audio_ms, interrupt_count, session_close_err
            try:
                async for raw_msg in live:
                    if stop.is_set():
                        break
                    if isinstance(raw_msg, bytes):
                        raw_msg = raw_msg.decode("utf-8", errors="replace")
                    try:
                        msg = json.loads(raw_msg)
                    except json.JSONDecodeError:
                        continue

                    tool_call = msg.get("toolCall") or msg.get("tool_call")
                    if tool_call:
                        if do_transcript:
                            transcript_lines.extend(tx.flush_all())
                        await _handle_tool_call(
                            wa_ws,
                            boot,
                            live,
                            tool_call,
                            notify,
                            on_hangup=lambda: _request_hangup("end_call"),
                        )
                        continue

                    sc = msg.get("serverContent") or msg.get("server_content") or {}
                    if sc.get("interrupted"):
                        interrupt_count += 1
                        log.info("live interrupted n=%d", interrupt_count)
                        if do_transcript:
                            transcript_lines.extend(tx.flush_all())
                        # Don't wipe farewell audio once hangup is in progress.
                        if hangup_req_at <= 0:
                            _flush_playback()
                        continue

                    turn_done = bool(
                        sc.get("turnComplete")
                        or sc.get("turn_complete")
                        or sc.get("generationComplete")
                        or sc.get("generation_complete")
                    )

                    if do_transcript:
                        in_tx = sc.get("inputTranscription") or sc.get("input_transcription") or {}
                        out_tx = sc.get("outputTranscription") or sc.get("output_transcription") or {}
                        if in_tx.get("text"):
                            for line in tx.feed_user(str(in_tx["text"]), finished=bool(in_tx.get("finished"))):
                                transcript_lines.append(line)
                                log.info("live transcript user: %s", line["text"][:160])
                        if out_tx.get("text"):
                            for line in tx.feed_assistant(str(out_tx["text"]), finished=bool(out_tx.get("finished"))):
                                transcript_lines.append(line)
                                log.info("live transcript assistant: %s", line["text"][:160])
                                if hangup_req_at <= 0 and _FAREWELL_RE.search(line["text"] or ""):
                                    # Model said goodbye but may skip end_call — hang up after audio.
                                    _request_hangup("farewell_transcript")
                        if turn_done:
                            for line in tx.flush_all():
                                transcript_lines.append(line)
                                log.info("live transcript %s: %s", line["role"], line["text"][:160])
                                if (
                                    hangup_req_at <= 0
                                    and line.get("role") == "assistant"
                                    and _FAREWELL_RE.search(line.get("text") or "")
                                ):
                                    _request_hangup("farewell_transcript")
                    elif turn_done and hangup_req_at <= 0:
                        # Transcription off: still hang up if end_call already pending only.
                        pass

                    parts = ((sc.get("modelTurn") or sc.get("model_turn") or {}).get("parts")) or []
                    for p in parts:
                        inline = p.get("inlineData") or p.get("inline_data") or {}
                        b64 = inline.get("data") or ""
                        if not b64:
                            continue
                        pcm = base64.b64decode(b64)
                        if first_audio_ms < 0:
                            first_audio_ms = int((time.time() - t0) * 1000)
                            log.info(
                                "gemini live first_audio_ms=%d model=%s",
                                first_audio_ms,
                                model,
                            )
                        await out_q.put(pcm)
            except Exception as e:
                session_close_err = str(e)
                if "CONTENT_TYPE_AUDIO" in session_close_err or "1007" in session_close_err:
                    log.warning(
                        "gemini live closed by server model=%s err=%s",
                        model,
                        session_close_err[:240],
                    )
                elif not stop.is_set():
                    log.exception("gemini live receive failed")
            finally:
                stop.set()
                try:
                    out_q.put_nowait(None)
                except asyncio.QueueFull:
                    pass

        async def downlink() -> None:
            """PCM24 queue → Opus@48k → WhatsApp, paced at realtime."""
            nonlocal playing_until, asst_rate_state, playback_busy
            pcm24_buf = bytearray()
            pcm48_buf = bytearray()
            frame24 = int(PCM_OUT_RATE * FRAME_DUR) * 2  # 20ms @ 24k mono s16
            need48 = FRAME_SAMPLES_48K * 2
            next_send = time.monotonic()
            while True:
                # Mark idle while blocked so graceful hangup can proceed.
                if not pcm24_buf and not pcm48_buf:
                    playback_busy = False
                try:
                    chunk = await asyncio.wait_for(out_q.get(), timeout=0.1)
                except asyncio.TimeoutError:
                    # Drop stranded partial frames so hangup idle can complete.
                    if hangup_req_at > 0 and (pcm24_buf or pcm48_buf):
                        pcm24_buf.clear()
                        pcm48_buf.clear()
                        playback_busy = False
                    continue
                if chunk is None:
                    playback_busy = False
                    return
                if chunk == b"":
                    pcm24_buf.clear()
                    pcm48_buf.clear()
                    playback_busy = False
                    playing_until = 0.0
                    next_send = time.monotonic()
                    continue
                pcm24_buf.extend(chunk)
                playback_busy = True
                while len(pcm24_buf) >= frame24:
                    frame = bytes(pcm24_buf[:frame24])
                    del pcm24_buf[:frame24]
                    pcm48 = _upsample_2x_s16(frame)
                    if timeline is not None:
                        pcm16, asst_rate_state = audioop.ratecv(
                            frame, 2, 1, PCM_OUT_RATE, REC_RATE, asst_rate_state
                        )
                        timeline.write_assistant(pcm16)
                    pcm48_buf.extend(pcm48)
                    while len(pcm48_buf) >= need48:
                        packet_pcm = _soft_limit_s16(bytes(pcm48_buf[:need48]))
                        del pcm48_buf[:need48]
                        try:
                            packet = encoder.encode(packet_pcm, FRAME_SAMPLES_48K)
                        except opuslib.OpusError:
                            continue
                        now = time.monotonic()
                        delay = next_send - now
                        if delay > 0:
                            await asyncio.sleep(delay)
                        elif delay < -0.1:
                            next_send = time.monotonic()
                        try:
                            await wa_ws.send_bytes(packet)
                        except Exception:
                            stop.set()
                            return
                        playing_until = time.monotonic() + PLAYING_HOLD_SECS
                        next_send += FRAME_DUR
                if not pcm24_buf and not pcm48_buf and out_q.empty():
                    playback_busy = False

        up_task = asyncio.create_task(uplink())
        live_task = asyncio.create_task(from_live())
        down_task = asyncio.create_task(downlink())
        try:
            done, pending = await asyncio.wait(
                {up_task, live_task, down_task},
                return_when=asyncio.FIRST_COMPLETED,
            )
            # If hangup is draining farewell audio, finish notify before teardown.
            if hangup_task is not None and not hangup_task.done() and hangup_req_at > 0:
                try:
                    await asyncio.wait_for(
                        hangup_task, timeout=max(1.0, HANGUP_MAX_WAIT_SECS + 1.0)
                    )
                except (asyncio.TimeoutError, asyncio.CancelledError):
                    hangup_task.cancel()
                    await asyncio.gather(hangup_task, return_exceptions=True)
            stop.set()
            for t in pending:
                if t is not hangup_task:
                    t.cancel()
            await asyncio.gather(up_task, live_task, down_task, return_exceptions=True)
            if hangup_task is not None and not hangup_task.done():
                hangup_task.cancel()
                await asyncio.gather(hangup_task, return_exceptions=True)
        finally:
            stop.set()
            if do_transcript:
                transcript_lines.extend(tx.flush_all())
            user_pcm = b""
            assistant_pcm = b""
            if timeline is not None:
                user_pcm, assistant_pcm = timeline.finalize()
            log.info(
                "live session done model=%s interrupts=%d transcript_lines=%d first_audio_ms=%d",
                model,
                interrupt_count,
                len(transcript_lines),
                first_audio_ms,
            )
            await _persist_ai_call_artifacts(
                boot,
                transcript_lines=transcript_lines,
                user_pcm=user_pcm,
                assistant_pcm=assistant_pcm,
            )
            # If Google closed before any assistant audio, try the next model.
            if first_audio_ms < 0 and session_close_err:
                raise _LiveRetriableDrop(session_close_err[:240])


class _TimelineRecorder:
    """Wall-clock aligned mono PCM16 buffers so stereo sounds like a conversation."""

    def __init__(self, sample_rate: int = REC_RATE) -> None:
        self.rate = sample_rate
        self.t0 = time.monotonic()
        self.user = bytearray()
        self.assistant = bytearray()

    def _pos_bytes(self) -> int:
        elapsed = max(0.0, time.monotonic() - self.t0)
        return int(elapsed * self.rate) * 2

    @staticmethod
    def _pad_to(buf: bytearray, pos: int) -> None:
        if pos > len(buf):
            buf.extend(b"\x00" * (pos - len(buf)))

    def write_user(self, pcm16: bytes) -> None:
        if not pcm16:
            return
        pos = self._pos_bytes()
        self._pad_to(self.user, pos)
        self._pad_to(self.assistant, pos)
        self.user.extend(pcm16)

    def write_assistant(self, pcm16: bytes) -> None:
        if not pcm16:
            return
        pos = self._pos_bytes()
        self._pad_to(self.assistant, pos)
        self._pad_to(self.user, pos)
        self.assistant.extend(pcm16)

    def finalize(self) -> Tuple[bytes, bytes]:
        n = max(len(self.user), len(self.assistant), self._pos_bytes())
        n -= n % 2
        self._pad_to(self.user, n)
        self._pad_to(self.assistant, n)
        return bytes(self.user), bytes(self.assistant)


class _TranscriptAssembler:
    """Merge Live partial word fragments into sentence-level turns."""

    def __init__(self) -> None:
        self._user = ""
        self._assistant = ""

    def feed_user(self, piece: str, *, finished: bool = False) -> List[Dict[str, str]]:
        # User started talking — close any open assistant sentence first.
        out = self._flush_role("assistant")
        self._user = _merge_transcript_piece(self._user, piece)
        out.extend(self._extract_sentences("user"))
        if finished:
            out.extend(self._flush_role("user"))
        return out

    def feed_assistant(self, piece: str, *, finished: bool = False) -> List[Dict[str, str]]:
        out = self._flush_role("user")
        self._assistant = _merge_transcript_piece(self._assistant, piece)
        out.extend(self._extract_sentences("assistant"))
        if finished:
            out.extend(self._flush_role("assistant"))
        return out

    def flush_all(self) -> List[Dict[str, str]]:
        out = self._flush_role("user")
        out.extend(self._flush_role("assistant"))
        return out

    def _flush_role(self, role: str) -> List[Dict[str, str]]:
        buf = self._user if role == "user" else self._assistant
        text = buf.strip()
        if role == "user":
            self._user = ""
        else:
            self._assistant = ""
        if not text:
            return []
        return [{"role": role, "text": text}]

    def _extract_sentences(self, role: str) -> List[Dict[str, str]]:
        buf = self._user if role == "user" else self._assistant
        done: List[Dict[str, str]] = []
        while True:
            m = _SENT_END.search(buf)
            if not m:
                break
            # Require trailing whitespace so "UTC." alone waits for more or flush.
            matched = m.group(0)
            if not matched[-1].isspace() and m.end() >= len(buf):
                break
            sent = buf[: m.end()].strip()
            buf = buf[m.end() :].lstrip()
            if sent:
                done.append({"role": role, "text": sent})
        if role == "user":
            self._user = buf
        else:
            self._assistant = buf
        return done


def _merge_transcript_piece(buf: str, piece: str) -> str:
    piece = (piece or "").strip()
    if not piece:
        return buf
    if not buf:
        return piece
    # Cumulative rewrite (some models re-send the growing utterance).
    if piece.startswith(buf) or buf.startswith(piece):
        return piece if len(piece) >= len(buf) else buf
    # Live often sends bare tokens ("is", "Monday,") — keep word spacing.
    if piece[0].isalnum() and not buf[-1].isspace():
        return buf + " " + piece
    return buf + piece


def _upsample_2x_s16(pcm: bytes) -> bytes:
    """Exact 2× upsample mono s16le with linear interpolation (24k→48k)."""
    if len(pcm) < 2:
        return b""
    n = len(pcm) // 2
    out = bytearray(n * 4)
    for i in range(n):
        off = i * 2
        s = int.from_bytes(pcm[off : off + 2], "little", signed=True)
        out[i * 4 : i * 4 + 2] = pcm[off : off + 2]
        if i + 1 < n:
            s2 = int.from_bytes(pcm[off + 2 : off + 4], "little", signed=True)
            mid = (s + s2) // 2
        else:
            mid = s
        out[i * 4 + 2 : i * 4 + 4] = int(mid).to_bytes(2, "little", signed=True)
    return bytes(out)


def _soft_limit_s16(pcm: bytes, thresh: int = SOFT_CLIP) -> bytes:
    """Gentle clip to avoid Opus splatters on near-full-scale Gemini PCM."""
    if len(pcm) < 2:
        return pcm
    out = bytearray(len(pcm))
    for i in range(0, len(pcm) - 1, 2):
        s = int.from_bytes(pcm[i : i + 2], "little", signed=True)
        if s > thresh:
            s = thresh
        elif s < -thresh:
            s = -thresh
        out[i : i + 2] = int(s).to_bytes(2, "little", signed=True)
    return bytes(out)


def _pcm16_wav(pcm: bytes, sample_rate: int = REC_RATE, channels: int = 1) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(2)
        w.setframerate(sample_rate)
        w.writeframes(pcm)
    return buf.getvalue()


def _stereo_pcm16(left: bytes, right: bytes) -> bytes:
    """Interleave two mono s16le tracks (pad shorter with silence)."""
    # Ensure even byte lengths (complete samples).
    left = left[: len(left) - (len(left) % 2)]
    right = right[: len(right) - (len(right) % 2)]
    n = max(len(left), len(right))
    if len(left) < n:
        left = left + b"\x00" * (n - len(left))
    if len(right) < n:
        right = right + b"\x00" * (n - len(right))
    out = bytearray(n * 2)
    for i in range(0, n, 2):
        out[i * 2 : i * 2 + 2] = left[i : i + 2]
        out[i * 2 + 2 : i * 2 + 4] = right[i : i + 2]
    return bytes(out)


async def _persist_ai_call_artifacts(
    boot: Dict[str, Any],
    *,
    transcript_lines: list[dict[str, str]],
    user_pcm: bytes,
    assistant_pcm: bytes,
) -> None:
    cb = (boot.get("callback_url") or "").rstrip("/")
    token = boot.get("callback_token") or ""
    call_id = boot.get("call_id") or ""
    if not cb or not call_id or not token:
        return

    headers = {"Authorization": f"Bearer {token}"}
    try:
        async with httpx.AsyncClient(timeout=60.0) as client:
            if transcript_lines and boot.get("transcription_enabled"):
                resp = await client.post(
                    f"{cb}/api/internal/pipecat/transcript/{call_id}",
                    headers={**headers, "Content-Type": "application/json"},
                    json={"lines": transcript_lines},
                )
                if resp.status_code >= 300:
                    log.warning(
                        "transcript upload failed status=%s body=%s",
                        resp.status_code,
                        (resp.text or "")[:200],
                    )
                else:
                    log.info("transcript uploaded lines=%d call_id=%s", len(transcript_lines), call_id)

            if not boot.get("recording_enabled"):
                return

            uploads: list[tuple[str, bytes]] = []
            # Both tracks are already 16 kHz mono on a shared wall-clock timeline.
            user16 = user_pcm
            asst16 = assistant_pcm
            if user16:
                uploads.append(("user", _pcm16_wav(user16)))
            if asst16:
                uploads.append(("assistant", _pcm16_wav(asst16)))
            if user16 or asst16:
                stereo = _stereo_pcm16(user16 or b"", asst16 or b"")
                uploads.append(("stereo", _pcm16_wav(stereo, channels=2)))

            for kind, wav in uploads:
                resp = await client.post(
                    f"{cb}/api/internal/pipecat/recordings/{call_id}",
                    params={"kind": kind},
                    headers={**headers, "Content-Type": "audio/wav"},
                    content=wav,
                )
                if resp.status_code >= 300:
                    log.warning(
                        "recording upload failed kind=%s status=%s body=%s",
                        kind,
                        resp.status_code,
                        (resp.text or "")[:200],
                    )
                else:
                    log.info(
                        "recording uploaded kind=%s bytes=%d call_id=%s",
                        kind,
                        len(wav),
                        call_id,
                    )
    except Exception:
        log.exception("persist AI Call artifacts failed call_id=%s", call_id)


def _live_tools(boot: Dict[str, Any]) -> List[Dict[str, Any]]:
    decls: List[Dict[str, Any]] = [
        {
            "name": "transfer_to_agent",
            "description": "Transfer the caller to a human agent.",
            "parameters": {
                "type": "object",
                "properties": {"reason": {"type": "string"}},
            },
        },
        {
            "name": "end_call",
            "description": (
                "REQUIRED to disconnect. Call this when the caller says goodbye "
                "or the conversation is finished. Speak a brief farewell, then call "
                "end_call (or call it and speak farewell). The line stays open until "
                "this tool is used."
            ),
            "parameters": {"type": "object", "properties": {}},
        },
    ]
    for t in boot.get("tools") or []:
        name = (t.get("name") or "").strip()
        if not name or name in ("transfer_to_agent", "end_call"):
            continue
        decls.append(
            {
                "name": name,
                "description": t.get("description") or name,
                "parameters": t.get("parameters") or {"type": "object", "properties": {}},
            }
        )
    return [{"functionDeclarations": decls}]


async def _handle_tool_call(
    wa_ws: WebSocket,
    boot: Dict[str, Any],
    live: Any,
    tool_call: Dict[str, Any],
    notify,
    *,
    on_hangup: Optional[Any] = None,
) -> None:
    fcs = tool_call.get("functionCalls") or tool_call.get("function_calls") or []
    responses = []
    for fc in fcs:
        name = (fc.get("name") or "").strip()
        args = fc.get("args") or fc.get("arguments") or {}
        if isinstance(args, str):
            try:
                args = json.loads(args)
            except json.JSONDecodeError:
                args = {}
        call_id = fc.get("id") or name

        if name in ("transfer_to_agent", "transfer"):
            await notify(boot, "transfer", team_id=boot.get("transfer_team_id") or "")
            try:
                await wa_ws.send_text(json.dumps({"type": "event", "event": "transfer"}))
            except Exception:
                pass
            responses.append(
                {
                    "id": call_id,
                    "name": name,
                    "response": {"result": "transferring"},
                }
            )
        elif name in ("end_call", "hangup"):
            log.info("tool end_call received")
            # Drain farewell audio, then notify Whatomate.
            if on_hangup:
                on_hangup()
            else:
                await notify(boot, "hangup")
                try:
                    await wa_ws.send_text(json.dumps({"type": "event", "event": "hangup"}))
                except Exception:
                    pass
            responses.append(
                {
                    "id": call_id,
                    "name": name,
                    "response": {"result": "ending"},
                }
            )
        else:
            result = await _relay_tool(boot, name, args)
            responses.append(
                {
                    "id": call_id,
                    "name": name,
                    "response": {"result": result},
                }
            )

    if responses:
        await live.send(json.dumps({"toolResponse": {"functionResponses": responses}}))


async def _relay_tool(boot: Dict[str, Any], name: str, args: Dict[str, Any]) -> str:
    cb = (boot.get("callback_url") or "").rstrip("/")
    token = boot.get("callback_token") or ""
    call_id = boot.get("call_id") or ""
    if not cb or not call_id:
        return "ok"
    try:
        async with httpx.AsyncClient(timeout=20.0) as client:
            resp = await client.post(
                f"{cb}/api/internal/pipecat/tools/{call_id}",
                headers={"Authorization": f"Bearer {token}"},
                json={"name": name, "arguments": args},
            )
            if resp.status_code >= 300:
                return f"error {resp.status_code}"
            data = resp.json() if resp.content else {}
            return str(data.get("result") or data.get("ok") or "ok")
    except Exception as e:
        return f"error: {e}"
