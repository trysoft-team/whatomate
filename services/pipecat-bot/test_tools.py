import unittest

from tools import (
    DEFAULT_END_CALL_DESC,
    DEFAULT_TRANSFER_DESC,
    END_CALL_REQUIRED_NOTE,
    end_call_description,
    transfer_description,
    transfer_tool_response,
)

CUSTOM_TRANSFER = (
    "Transfer the caller to a human agent when they ask for a person, need booking help "
    "you cannot complete, or say transfer. Before you transfer the call confirm that "
    "you're now transferring the call to agents."
)


class BuiltinDescriptionTests(unittest.TestCase):
    def test_transfer_uses_node_description(self):
        boot = {"tools": [{"name": "transfer_to_agent", "description": CUSTOM_TRANSFER}]}
        self.assertEqual(transfer_description(boot), CUSTOM_TRANSFER)

    def test_transfer_falls_back_to_default(self):
        self.assertEqual(transfer_description({}), DEFAULT_TRANSFER_DESC)
        boot = {"tools": [{"name": "transfer_to_agent", "description": "  "}]}
        self.assertEqual(transfer_description(boot), DEFAULT_TRANSFER_DESC)

    def test_end_call_keeps_required_note_with_custom_description(self):
        boot = {"tools": [{"name": "end_call", "description": "End politely when done."}]}
        desc = end_call_description(boot)
        self.assertTrue(desc.startswith("End politely when done."))
        self.assertIn(END_CALL_REQUIRED_NOTE, desc)

    def test_end_call_default(self):
        self.assertEqual(end_call_description({}), DEFAULT_END_CALL_DESC)


class TransferResponseTests(unittest.TestCase):
    def test_asks_model_to_announce_when_nothing_was_said(self):
        resp = transfer_tool_response(already_speaking=False)
        self.assertEqual(resp["result"], "transferring")
        self.assertIn("instruction", resp)

    def test_no_second_announcement_when_already_speaking(self):
        self.assertEqual(transfer_tool_response(already_speaking=True), {"result": "transferring"})


class ToolListTests(unittest.TestCase):
    boot = {
        "tools": [
            {"name": "transfer_to_agent", "description": CUSTOM_TRANSFER, "handler": "transfer"},
            {"name": "create_booking", "description": "Book it.", "handler": "http"},
        ]
    }

    def test_live_tools_send_node_transfer_description(self):
        from gemini_live import _live_tools

        decls = {d["name"]: d for d in _live_tools(self.boot)[0]["functionDeclarations"]}
        self.assertEqual(decls["transfer_to_agent"]["description"], CUSTOM_TRANSFER)
        self.assertIn("create_booking", decls)
        self.assertEqual(sum(1 for d in _live_tools(self.boot)[0]["functionDeclarations"]
                             if d["name"] == "transfer_to_agent"), 1)

    def test_turn_based_tools_send_node_transfer_description(self):
        from main import build_openai_tools

        fns = {t["function"]["name"]: t["function"] for t in build_openai_tools(self.boot)}
        self.assertEqual(fns["transfer_to_agent"]["description"], CUSTOM_TRANSFER)


if __name__ == "__main__":
    unittest.main()
