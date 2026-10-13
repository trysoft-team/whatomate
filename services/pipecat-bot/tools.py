"""Built-in AI Call tools (transfer_to_agent, end_call) shared by Live and turn-based paths.

The IVR node may carry its own description for these tools; that text is the
operator's policy for when to transfer / hang up, so it must reach the model.
"""

from __future__ import annotations

from typing import Any, Dict

DEFAULT_TRANSFER_DESC = "Transfer the caller to a human agent."
END_CALL_REQUIRED_NOTE = "The line stays open until this tool is used."
DEFAULT_END_CALL_DESC = (
    "REQUIRED to disconnect. Call this when the caller says goodbye "
    "or the conversation is finished. Speak a brief farewell, then call "
    "end_call (or call it and speak farewell). " + END_CALL_REQUIRED_NOTE
)
TRANSFER_ANNOUNCEMENT = "I'm transferring you to an agent now. Please hold."


def _node_description(boot: Dict[str, Any], name: str) -> str:
    for t in boot.get("tools") or []:
        if (t.get("name") or "").strip() == name:
            return (t.get("description") or "").strip()
    return ""


def transfer_description(boot: Dict[str, Any]) -> str:
    return _node_description(boot, "transfer_to_agent") or DEFAULT_TRANSFER_DESC


def end_call_description(boot: Dict[str, Any]) -> str:
    custom = _node_description(boot, "end_call")
    if not custom:
        return DEFAULT_END_CALL_DESC
    return f"{custom} {END_CALL_REQUIRED_NOTE}"


def transfer_tool_response(*, already_speaking: bool) -> Dict[str, Any]:
    """Tool result for transfer_to_agent.

    The transfer waits for queued speech to finish, so if the model has not
    said anything yet we ask it to announce the handover in one sentence.
    """
    if already_speaking:
        return {"result": "transferring"}
    return {
        "result": "transferring",
        "instruction": (
            "In one short sentence, tell the caller you are transferring them to an "
            "agent now. Do not call any more tools."
        ),
    }
