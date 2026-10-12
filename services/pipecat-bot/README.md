# Whatomate Pipecat sidecar (realtime AI Call)
#
# Go keeps WhatsApp WebRTC; this service handles voice AI over Opus WS.
#
#   cd services/pipecat-bot
#   python3 -m venv .venv && source .venv/bin/activate
#   pip install -r requirements.txt
#   export PIPECAT_AUTH_TOKEN=dev-secret
#   uvicorn main:app --host 127.0.0.1 --port 7860
#
# Enable in config.local.toml:
#
#   [pipecat]
#   enabled = true
#   base_url = "http://127.0.0.1:7860"
#   ws_url = "ws://127.0.0.1:7860"
#   auth_token = "dev-secret"
#   callback_url = "http://127.0.0.1:8080"
#
# Default voice path: Gemini Live (BidiGenerateContent) — same realtime approach
# as https://github.com/pipecat-ai/pipecat-examples/tree/main/whatsapp
# (GeminiLiveLLMService). Continuous audio in/out; no STT→LLM→TTS round-trip.
#
#   PIPECAT_GEMINI_LIVE=1   # default
#   PIPECAT_GEMINI_LIVE=0   # force legacy turn-based STT→LLM→TTS
#
# Live voice: Puck (or other Gemini prebuilt voices via voice_id).
# Tools: transfer_to_agent / end_call (+ relayed custom tools) still work.
# Barge-in: local RMS flush + Live `interrupted` clear the playback queue so
# the AI pauses when the caller speaks.
# Hangup: end_call waits for farewell audio to finish (idle drain) before
# notifying Whatomate — no mid-sentence disconnect.
