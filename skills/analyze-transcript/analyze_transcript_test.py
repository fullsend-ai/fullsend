#!/usr/bin/env python3
"""Unit tests for analyze-transcript.py (pi session normalization; no network)."""

from __future__ import annotations

import importlib.util
import io
import json
import os
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace

_SPEC = importlib.util.spec_from_file_location(
    "analyze_transcript", os.path.join(os.path.dirname(__file__), "analyze-transcript.py")
)
assert _SPEC is not None and _SPEC.loader is not None
at = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(at)


def _pi_session_lines():
    """A pi session file shaped like earendil-works/pi v0.84.2 writes it:
    ISO timestamps on entries, pi-ai messages inside (ms timestamps)."""
    return [
        {
            "type": "session",
            "version": 3,
            "id": "abc123",
            "timestamp": "2026-08-22T10:00:00.000Z",
            "cwd": "/r",
        },
        {
            "type": "model_change",
            "id": "x1",
            "parentId": None,
            "timestamp": "2026-08-22T10:00:00.050Z",
            "modelId": "m",
        },
        {
            "type": "session_info",
            "id": "s1",
            "parentId": "x1",
            "timestamp": "2026-08-22T10:00:00.100Z",
            "name": "triage",
        },
        {
            "type": "message",
            "id": "m1",
            "parentId": "s1",
            "timestamp": "2026-08-22T10:00:01.000Z",
            "message": {
                "role": "user",
                "content": "Run the agent task",
                "timestamp": 1787392801000,
            },
        },
        {
            "type": "message",
            "id": "m2",
            "parentId": "m1",
            "timestamp": "2026-08-22T10:00:05.000Z",
            "message": {
                "role": "assistant",
                "content": [
                    {"type": "thinking", "thinking": "hm"},
                    {"type": "text", "text": "Checking."},
                    {
                        "type": "toolCall",
                        "id": "t1",
                        "name": "bash",
                        "arguments": {"command": "gh issue view 1"},
                    },
                ],
                "model": "claude-opus-4-6",
                "usage": {"input": 120, "output": 30, "cacheRead": 50, "cacheWrite": 10},
                "stopReason": "toolUse",
                "timestamp": 1787392805000,
            },
        },
        {
            "type": "message",
            "id": "m3",
            "parentId": "m2",
            "timestamp": "2026-08-22T10:00:06.000Z",
            "message": {
                "role": "toolResult",
                "toolCallId": "t1",
                "toolName": "bash",
                "content": [
                    {"type": "text", "text": "Error: gh: not found"},
                    {"type": "image", "data": "", "mimeType": "image/png"},
                ],
                "isError": True,
                "timestamp": 1787392806000,
            },
        },
        {
            "type": "message",
            "id": "m4",
            "parentId": "m3",
            "timestamp": "2026-08-22T10:00:07.000Z",
            "message": {"role": "bashExecution"},
        },
        {
            "type": "message",
            "id": "m5",
            "parentId": "m4",
            "timestamp": "2026-08-22T10:00:09.000Z",
            "message": {
                "role": "assistant",
                "content": [],
                "model": "claude-opus-4-6",
                "usage": {"input": 200, "output": 0, "cacheRead": 0, "cacheWrite": 0},
                "stopReason": "error",
                "errorMessage": "429 quota exhausted",
                "timestamp": 1787392809000,
            },
        },
    ]


def _write(dirpath, name, lines):
    p = Path(dirpath) / name
    p.write_text("".join(json.dumps(line) + "\n" for line in lines))
    return str(p)


def _run(fn, **kwargs):
    defaults = {"line_range": None, "max_width": 0, "json_output": False, "pattern": None}
    args = SimpleNamespace(**{**defaults, **kwargs})
    buf = io.StringIO()
    with redirect_stdout(buf):
        fn(args)
    return buf.getvalue()


class NormalizePiMessageTest(unittest.TestCase):
    def test_user_string_and_list_content(self):
        role, msg = at.normalize_pi_message({"message": {"role": "user", "content": "hi"}})
        self.assertEqual(role, "user")
        self.assertEqual(list(at.extract_content_blocks(msg)), [("text", "hi")])
        role, msg = at.normalize_pi_message(
            {"message": {"role": "user", "content": [{"type": "text", "text": "hi"}]}}
        )
        self.assertEqual([b for b, _ in at.extract_content_blocks(msg)], ["text"])

    def test_tool_call_becomes_tool_use(self):
        _, msg = at.normalize_pi_message(_pi_session_lines()[4])
        blocks = dict((b, d) for b, d in at.extract_content_blocks(msg))
        self.assertEqual(blocks["tool_use"]["name"], "bash")
        self.assertEqual(blocks["tool_use"]["input"], {"command": "gh issue view 1"})
        self.assertEqual(msg["stop_reason"], "toolUse")
        self.assertEqual(msg["usage"]["input_tokens"], 120)
        self.assertEqual(msg["usage"]["cache_creation_input_tokens"], 10)

    def test_tool_result_becomes_user_tool_result(self):
        role, msg = at.normalize_pi_message(_pi_session_lines()[5])
        self.assertEqual(role, "user")
        ((btype, block),) = at.extract_content_blocks(msg)
        self.assertEqual(btype, "tool_result")
        self.assertTrue(at._is_error_result(block))
        self.assertEqual(at.get_tool_result_text(block), "Error: gh: not found")

    def test_model_error_surfaces_as_text(self):
        _, msg = at.normalize_pi_message(_pi_session_lines()[7])
        ((btype, block),) = at.extract_content_blocks(msg)
        self.assertEqual(btype, "text")
        self.assertTrue(block["model_error"])
        self.assertIn("429 quota exhausted", block["text"])
        self.assertEqual(msg["error_message"], "429 quota exhausted")

    def test_unknown_role_and_missing_message(self):
        self.assertIsNone(at.normalize_pi_message({"message": {"role": "bashExecution"}}))
        self.assertIsNone(at.normalize_pi_message({"type": "message"}))

    def test_empty_assistant_content_without_error(self):
        _, msg = at.normalize_pi_message(
            {"message": {"role": "assistant", "content": [], "stopReason": "stop"}}
        )
        self.assertEqual(list(at.extract_content_blocks(msg)), [])
        self.assertEqual(msg["usage"], {})


class TimestampTest(unittest.TestCase):
    def test_iso_and_millis(self):
        iso = at._parse_timestamp("2026-08-22T10:00:00Z")
        self.assertIsNotNone(iso)
        ms = at._parse_timestamp(int(iso.timestamp() * 1000))
        self.assertEqual(iso, ms)
        naive = at._parse_timestamp("2026-08-22T10:00:00")
        self.assertEqual(naive, iso)
        self.assertIsNone(at._parse_timestamp("nope"))
        self.assertIsNone(at._parse_timestamp(True))
        self.assertIsNone(at._parse_timestamp(None))


class SubcommandsOnPiSessionTest(unittest.TestCase):
    def setUp(self):
        self.tmp = TemporaryDirectory()
        self.path = _write(
            self.tmp.name, "triage-2026-08-22T10-00-00-000Z_abc123.jsonl", _pi_session_lines()
        )

    def tearDown(self):
        self.tmp.cleanup()

    def test_detect_file_type_accepts_pi_session(self):
        self.assertIsNone(at.detect_file_type(self.path))

    def test_summary(self):
        s = at._accumulate_stats(self.path)
        self.assertEqual(s["agent"], "triage")
        self.assertEqual(s["session_ids"], ["abc123"])
        self.assertEqual(s["models"], ["claude-opus-4-6"])
        self.assertEqual(dict(s["messages"]), {"user": 2, "assistant": 2})
        self.assertEqual(
            s["tokens"], {"input": 320, "output": 30, "cache_read": 50, "cache_create": 10}
        )
        self.assertEqual(s["duration_seconds"], 8.0)
        self.assertEqual(dict(s["tool_calls"]), {"bash": 1})
        self.assertEqual(dict(s["stop_reasons"]), {"toolUse": 1, "error": 1})

    def test_agent_falls_back_to_filename_prefix_without_session_info(self):
        lines = [line for line in _pi_session_lines() if line["type"] != "session_info"]
        path = _write(self.tmp.name, "code-2026-08-22T11-00-00-000Z_def.jsonl", lines)
        self.assertEqual(at._accumulate_stats(path)["agent"], "code")
        # Hyphenated agent label, and the Go-side shape without millis/Z.
        path = _write(self.tmp.name, "code-review-2026-08-22T11-00-00_def.jsonl", lines)
        self.assertEqual(at._accumulate_stats(path)["agent"], "code-review")
        # A Claude-style name never matches the pi pattern.
        self.assertIsNone(at._PI_SESSION_FILENAME.match("triage-0c1f2e3d-4a5b.jsonl"))

    def test_aborted_without_error_message(self):
        lines = _pi_session_lines()[:4] + [
            {
                "type": "message",
                "id": "m9",
                "parentId": "m1",
                "timestamp": "2026-08-22T10:00:02.000Z",
                "message": {"role": "assistant", "content": [], "stopReason": "aborted"},
            }
        ]
        path = _write(self.tmp.name, "triage-2026-08-22T10-00-00_x.jsonl", lines)
        _, msg = at.normalize_pi_message(lines[-1])
        ((_, block),) = at.extract_content_blocks(msg)
        self.assertEqual(block["text"], "Model error (stopReason=aborted)")
        out = _run(at.cmd_errors, file=path)
        self.assertIn("ERROR: Model error (stopReason=aborted)", out)

    def test_model_error_is_not_also_a_keyword_mention(self):
        lines = _pi_session_lines()[:4] + [
            {
                "type": "message",
                "id": "m9",
                "parentId": "m1",
                "timestamp": "2026-08-22T10:00:02.000Z",
                "message": {
                    "role": "assistant",
                    "content": [],
                    "stopReason": "error",
                    "errorMessage": "API Error: 529 overloaded",
                },
            }
        ]
        path = _write(self.tmp.name, "triage-2026-08-22T10-00-00_y.jsonl", lines)
        errors, mentions = at._collect_errors(path, 0)
        self.assertEqual(len(errors), 1)
        self.assertEqual(mentions, [])

    def test_duration_from_unix_ms_entry_timestamps(self):
        lines = _pi_session_lines()
        for line in lines:
            line["timestamp"] = int(at._parse_timestamp(line["timestamp"]).timestamp() * 1000)
        path = _write(self.tmp.name, "triage-2026-08-22T10-00-00_z.jsonl", lines)
        self.assertEqual(at._accumulate_stats(path)["duration_seconds"], 8.0)

    def test_errors_reports_tool_error_and_model_error(self):
        out = _run(at.cmd_errors, file=self.path)
        self.assertIn("ERROR: Error: gh: not found", out)
        self.assertIn("Model error (stopReason=error): 429 quota exhausted", out)

    def test_conversation_search_tools_audit(self):
        conv = _run(at.cmd_conversation, file=self.path)
        self.assertIn("TOOL CALL: bash", conv)
        self.assertIn("RESULT: Error: gh: not found", conv)
        self.assertIn("ASSISTANT: Model error", conv)
        search = _run(at.cmd_search, file=self.path, pattern="quota")
        self.assertIn("[assistant]", search)
        tools = _run(at.cmd_tools, file=self.path)
        self.assertIn("bash", tools)
        audit = _run(at.cmd_audit, file=self.path)
        self.assertIn("Errors (2)", audit)

    def test_claude_shape_unchanged(self):
        lines = [
            {"type": "agent-setting", "agentSetting": "triage", "sessionId": "s-1"},
            {
                "type": "user",
                "timestamp": "2026-08-22T10:00:00Z",
                "message": {"role": "user", "content": "go"},
            },
            {
                "type": "assistant",
                "timestamp": "2026-08-22T10:00:04Z",
                "message": {
                    "role": "assistant",
                    "model": "claude-opus-4-6",
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 10, "output_tokens": 5},
                    "content": [{"type": "text", "text": "done"}],
                },
            },
        ]
        path = _write(self.tmp.name, "triage-s-1.jsonl", lines)
        s = at._accumulate_stats(path)
        self.assertEqual(s["agent"], "triage")
        self.assertEqual(s["duration_seconds"], 4.0)
        self.assertEqual(s["tokens"]["input"], 10)
        self.assertEqual(dict(s["stop_reasons"]), {"end_turn": 1})


if __name__ == "__main__":
    unittest.main()
