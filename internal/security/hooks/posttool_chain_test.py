#!/usr/bin/env python3
"""Integration tests for post-tool hook chain ordering (unicode before secret redact)."""

from __future__ import annotations

import io
import json
import os
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

HOOKS_DIR = Path(__file__).parent
UNICODE_HOOK = str(HOOKS_DIR / "unicode_posttool.py")
SECRET_HOOK = str(HOOKS_DIR / "secret_redact_posttool.py")
CHAIN_HOOK = str(HOOKS_DIR / "posttool_chain.py")

PLAIN_PAT = "ghp_FAKEtesttoken000000000000000000000000"


def obfuscate_with_char(text: str, char: str) -> str:
    """Insert invisible character between each codepoint."""
    return char.join(text)


def result_text(stdout: str) -> str:
    out = json.loads(stdout)
    updated = out.get("hookSpecificOutput", {}).get("updatedToolOutput")
    if isinstance(updated, dict) and isinstance(updated.get("stdout"), str):
        return updated["stdout"]
    if isinstance(updated, str):
        return updated
    return out["tool_result"]


def run_hook(
    script: str,
    payload: str | dict,
    *,
    key: str = "tool_result",
    env_extra: dict[str, str] | None = None,
    tool_name: str = "Read",
    tool_input: dict | None = None,
) -> tuple[int, str, str]:
    body: dict = {"tool_name": tool_name, key: payload}
    if tool_input is not None:
        body["tool_input"] = tool_input
    env = {k: v for k, v in os.environ.items() if k != "FULLSEND_CANARY_TOKEN"}
    env.update(env_extra or {})
    proc = subprocess.run(
        [sys.executable, script],
        input=json.dumps(body),
        capture_output=True,
        text=True,
        timeout=10,
        env=env,
    )
    return proc.returncode, proc.stdout, proc.stderr


def run_wrong_chain(tool_result: str) -> str:
    """Run secret_redact then unicode (wrong sandbox order — leaks obfuscated tokens)."""
    rc, stdout, stderr = run_hook(SECRET_HOOK, tool_result)
    if rc != 0:
        raise RuntimeError(f"secret_redact hook failed: rc={rc}, stderr={stderr}")
    if stdout.strip():
        tool_result = result_text(stdout)

    rc, stdout, stderr = run_hook(UNICODE_HOOK, tool_result)
    if rc != 0:
        raise RuntimeError(f"unicode hook failed: rc={rc}, stderr={stderr}")
    if stdout.strip():
        return result_text(stdout)
    return tool_result


def to_fullwidth_ascii(text: str) -> str:
    """Convert printable ASCII to fullwidth compatibility forms."""
    out: list[str] = []
    for c in text:
        o = ord(c)
        if 0x21 <= o <= 0x7E:
            out.append(chr(o + 0xFF00 - 0x20))
        else:
            out.append(c)
    return "".join(out)


def run_piped_chain(tool_result: str) -> str:
    """Run unicode_posttool then secret_redact_posttool (legacy sequential order)."""
    rc, stdout, stderr = run_hook(UNICODE_HOOK, tool_result)
    if rc != 0:
        raise RuntimeError(f"unicode hook failed: rc={rc}, stderr={stderr}")
    if stdout.strip():
        tool_result = result_text(stdout)

    rc, stdout, stderr = run_hook(SECRET_HOOK, tool_result)
    if rc != 0:
        raise RuntimeError(f"secret_redact hook failed: rc={rc}, stderr={stderr}")
    if stdout.strip():
        return result_text(stdout)
    return tool_result


def run_chain(payload: str | dict, *, key: str = "tool_response") -> str:
    """Run the in-process driver Claude Code actually invokes."""
    rc, stdout, stderr = run_hook(CHAIN_HOOK, payload, key=key)
    if rc != 0:
        raise RuntimeError(f"posttool_chain failed: rc={rc}, stderr={stderr}")
    if not stdout.strip():
        if isinstance(payload, str):
            return payload
        return json.dumps(payload)
    return result_text(stdout)


class TestPostToolChain(unittest.TestCase):
    def test_plain_pat_redacted_by_chain(self):
        result = run_chain(PLAIN_PAT)
        self.assertNotIn("ghp_FAKEtest", result)
        self.assertIn("...", result)

    def test_tool_result_fallback_still_redacts(self):
        result = run_chain(PLAIN_PAT, key="tool_result")
        self.assertNotIn("ghp_FAKEtest", result)

    def test_zero_width_obfuscated_pat_redacted_by_chain(self):
        obfuscated = obfuscate_with_char(PLAIN_PAT, "\u200c")
        result = run_chain(obfuscated)
        self.assertNotIn("ghp_FAKEtest", result)
        self.assertIn("...", result)

    def test_ltr_mark_obfuscated_pat_redacted_by_chain(self):
        obfuscated = obfuscate_with_char(PLAIN_PAT, "\u200e")
        result = run_chain(obfuscated)
        self.assertNotIn("ghp_FAKEtest", result)
        self.assertIn("...", result)

    def test_redact_alone_misses_zero_width_obfuscated_pat(self):
        obfuscated = obfuscate_with_char(PLAIN_PAT, "\u200c")
        rc, stdout, _ = run_hook(SECRET_HOOK, obfuscated)
        self.assertEqual(rc, 0)
        # secret_redact alone does not modify output when regex cannot match
        self.assertEqual(stdout.strip(), "")
        # Obfuscated token still present in source (would leak after unicode strips ZWNJ)
        self.assertIn("\u200c", obfuscated)

    def test_wrong_order_chain_leaks_obfuscated_pat(self):
        obfuscated = obfuscate_with_char(PLAIN_PAT, "\u200c")
        result = run_wrong_chain(obfuscated)
        self.assertIn("ghp_FAKEtest", result)

    def test_fullwidth_obfuscated_pat_redacted_by_chain(self):
        fullwidth = to_fullwidth_ascii(PLAIN_PAT)
        result = run_chain(fullwidth)
        self.assertNotIn("ghp_FAKEtest", result)
        self.assertIn("...", result)

    def test_piped_legacy_order_still_redacts(self):
        obfuscated = obfuscate_with_char(PLAIN_PAT, "\u200c")
        result = run_piped_chain(obfuscated)
        self.assertNotIn("ghp_FAKEtest", result)

    def test_bash_object_tool_response_preserves_shape(self):
        payload = {
            "stdout": f"token {PLAIN_PAT}\n",
            "stderr": "",
            "interrupted": False,
            "isImage": False,
        }
        rc, stdout, stderr = run_hook(CHAIN_HOOK, payload, key="tool_response")
        self.assertEqual(rc, 0, stderr)
        out = json.loads(stdout)
        updated = out["hookSpecificOutput"]["updatedToolOutput"]
        self.assertIsInstance(updated, dict)
        self.assertNotIn("ghp_FAKEtest", updated["stdout"])
        self.assertEqual(updated["stderr"], "")
        self.assertFalse(updated["interrupted"])

    def test_bash_object_unicode_then_redact(self):
        obfuscated = obfuscate_with_char(PLAIN_PAT, "\u200c")
        payload = {
            "stdout": f"token {obfuscated}\n",
            "stderr": "",
            "interrupted": False,
            "isImage": False,
        }
        rc, stdout, stderr = run_hook(CHAIN_HOOK, payload, key="tool_response")
        self.assertEqual(rc, 0, stderr)
        updated = json.loads(stdout)["hookSpecificOutput"]["updatedToolOutput"]
        self.assertIsInstance(updated, dict)
        self.assertNotIn("ghp_FAKEtest", updated["stdout"])
        self.assertNotIn("\u200c", updated["stdout"])
        self.assertEqual(updated["stderr"], "")

    def test_bash_object_suppress_clears_stderr(self):
        payload = {
            "stdout": "ok  \tgithub.com/org/repo/internal/foo\t0.123s\n",
            "stderr": "warning: verbose compiler noise\n",
            "interrupted": False,
            "isImage": False,
        }
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            payload,
            key="tool_response",
            tool_name="Bash",
            tool_input={"command": "go test ./internal/..."},
        )
        self.assertEqual(rc, 0, stderr)
        updated = json.loads(stdout)["hookSpecificOutput"]["updatedToolOutput"]
        self.assertIn("packages passed", updated["stdout"])
        self.assertEqual(updated["stderr"], "")
        self.assertFalse(updated["interrupted"])

    def test_emits_hook_specific_output(self):
        rc, stdout, _ = run_hook(CHAIN_HOOK, PLAIN_PAT, key="tool_response")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["hookSpecificOutput"]["hookEventName"], "PostToolUse")
        self.assertIn("updatedToolOutput", out["hookSpecificOutput"])

    def test_stderr_only_canary_blocks_and_redacts(self):
        payload = {
            "stdout": "",
            "stderr": "leaked SECRET_CANARY_xyz on stderr",
            "interrupted": False,
            "isImage": False,
        }
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            payload,
            key="tool_response",
            env_extra={"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
            tool_name="Bash",
        )
        self.assertEqual(rc, 1, stderr)
        out = json.loads(stdout)
        self.assertEqual(out["decision"], "block")
        updated = out["hookSpecificOutput"]["updatedToolOutput"]
        self.assertEqual(updated["stdout"], "")
        self.assertIn("[CANARY_REDACTED]", updated["stderr"])
        self.assertNotIn("SECRET_CANARY_xyz", updated["stderr"])

    def test_canary_and_secret_on_one_call(self):
        payload = {
            "stdout": f"token {PLAIN_PAT}\n",
            "stderr": "leaked SECRET_CANARY_xyz",
            "interrupted": False,
            "isImage": False,
        }
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            payload,
            key="tool_response",
            env_extra={"FULLSEND_CANARY_TOKEN": "SECRET_CANARY_xyz"},
            tool_name="Bash",
        )
        self.assertEqual(rc, 1, stderr)
        out = json.loads(stdout)
        self.assertEqual(out["decision"], "block")
        updated = out["hookSpecificOutput"]["updatedToolOutput"]
        self.assertNotIn("ghp_FAKEtest", updated["stdout"])
        self.assertIn("[CANARY_REDACTED]", updated["stderr"])
        self.assertNotIn("SECRET_CANARY_xyz", json.dumps(updated))

    def test_redact_stage_exception_fail_open(self):
        import posttool_chain

        payload = json.dumps({"tool_name": "Read", "tool_response": PLAIN_PAT})
        real_load = posttool_chain._load_stage

        def fake_load(token: str):
            mod = real_load(token)
            if token == "redact" and mod is not None:

                def boom(_text: str):
                    raise RuntimeError("boom")

                mod.redact_text = boom
            return mod

        buf = io.StringIO()
        with (
            mock.patch.object(posttool_chain, "_load_stage", fake_load),
            mock.patch.object(sys, "stdin", io.StringIO(payload)),
            mock.patch.object(sys, "stdout", buf),
            self.assertRaises(SystemExit) as cm,
        ):
            posttool_chain.main()
        self.assertEqual(cm.exception.code, 0)
        out = json.loads(buf.getvalue())
        self.assertTrue(out["metadata"]["redact_error"])
        # A stage error alone must not emit a rewrite: Claude Code lets one
        # hook's updatedToolOutput replace another's, so a no-op rewrite would
        # clobber a legitimate one from a repo-local hook.
        self.assertNotIn("hookSpecificOutput", out)


CANARY = "FULLSEND_CANARY_ABC123XYZ"


def to_fullwidth(text: str) -> str:
    return "".join(chr(ord(c) + 0xFEE0) if 33 <= ord(c) <= 126 else c for c in text)


class TestCanaryObfuscationBypass(unittest.TestCase):
    """A canary split by obfuscation characters must not survive the chain.

    Regression: canary detection used to run on the raw payload, before the
    unicode stage. An obfuscated token evaded detection and the unicode stage
    then reassembled the clean token into updatedToolOutput — a leak the hook
    is specifically there to stop.
    """

    def _run(self, leaked: str) -> tuple[int, dict]:
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            {"stdout": f"leak: {leaked}", "stderr": "", "interrupted": False, "isImage": False},
            key="tool_response",
            env_extra={"FULLSEND_CANARY_TOKEN": CANARY},
            tool_name="Bash",
            tool_input={"command": "echo hi"},
        )
        self.assertEqual(rc, 1, stderr)
        return rc, json.loads(stdout)

    def _assert_blocked_and_clean(self, out: dict) -> None:
        self.assertEqual(out["decision"], "block")
        self.assertIs(out["continue"], False)
        emitted = json.dumps(out.get("hookSpecificOutput", {}).get("updatedToolOutput"))
        self.assertNotIn(CANARY, emitted)
        self.assertIn("[CANARY_REDACTED]", emitted)

    def test_zero_width_split_canary_blocks(self):
        _, out = self._run(obfuscate_with_char(CANARY, "\u200b"))
        self._assert_blocked_and_clean(out)

    def test_fullwidth_canary_blocks(self):
        _, out = self._run(to_fullwidth(CANARY))
        self._assert_blocked_and_clean(out)

    def test_plain_canary_still_blocks_and_halts(self):
        _, out = self._run(CANARY)
        self._assert_blocked_and_clean(out)

    def test_cross_field_boundary_is_not_a_false_positive(self):
        """ "ABC" + "DEF" must not match canary "CDEF" — such a hit spans two
        fields and the redactor, which rewrites fields independently, could
        never remove it."""
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            {"stdout": "ABC", "stderr": "DEF", "interrupted": False, "isImage": False},
            key="tool_response",
            env_extra={"FULLSEND_CANARY_TOKEN": "CDEF"},
            tool_name="Bash",
            tool_input={"command": "echo hi"},
        )
        self.assertEqual(rc, 0, stderr)


class TestCanaryFailsClosed(unittest.TestCase):
    def test_scan_failure_blocks(self):
        """A canary scan that raises must block, not silently allow."""
        import posttool_chain

        payload = json.dumps({"tool_name": "Read", "tool_response": "harmless"})

        def boom(_value):
            raise RuntimeError("boom")

        buf = io.StringIO()
        with (
            mock.patch.dict(os.environ, {"FULLSEND_CANARY_TOKEN": CANARY}),
            mock.patch.object(posttool_chain.hook_io, "scan_text", boom),
            mock.patch.object(sys, "stdin", io.StringIO(payload)),
            mock.patch.object(sys, "stdout", buf),
            self.assertRaises(SystemExit) as cm,
        ):
            posttool_chain.main()
        self.assertEqual(cm.exception.code, 1)
        out = json.loads(buf.getvalue())
        self.assertEqual(out["decision"], "block")
        self.assertIn("CANARY_SCAN_ERROR", out["reason"])

    def test_redaction_failure_withholds_output_and_still_blocks(self):
        """If the canary cannot be redacted, block without emitting output."""
        import posttool_chain

        payload = json.dumps({"tool_name": "Read", "tool_response": f"leak {CANARY}"})

        def boom(_value, _canary):
            raise RuntimeError("boom")

        buf = io.StringIO()
        with (
            mock.patch.dict(os.environ, {"FULLSEND_CANARY_TOKEN": CANARY}),
            mock.patch.object(posttool_chain.hook_io, "redact_canary", boom),
            mock.patch.object(sys, "stdin", io.StringIO(payload)),
            mock.patch.object(sys, "stdout", buf),
            self.assertRaises(SystemExit) as cm,
        ):
            posttool_chain.main()
        self.assertEqual(cm.exception.code, 1)
        out = json.loads(buf.getvalue())
        self.assertEqual(out["decision"], "block")
        self.assertNotIn("hookSpecificOutput", out)
        self.assertNotIn(CANARY, buf.getvalue())

    def test_emit_failure_still_blocks_with_hardcoded_json(self):
        """If emitting the block fails, fall back to hard-coded JSON and exit 1."""
        import posttool_chain

        payload = json.dumps({"tool_name": "Read", "tool_response": f"leak {CANARY}"})

        def boom(*_args, **_kwargs):
            raise RuntimeError("boom")

        buf = io.StringIO()
        with (
            mock.patch.dict(os.environ, {"FULLSEND_CANARY_TOKEN": CANARY}),
            mock.patch.object(posttool_chain.hook_io, "emit_block", boom),
            mock.patch.object(sys, "stdin", io.StringIO(payload)),
            mock.patch.object(sys, "stdout", buf),
            self.assertRaises(SystemExit) as cm,
        ):
            posttool_chain.main()
        self.assertEqual(cm.exception.code, 1)
        out = json.loads(buf.getvalue())
        self.assertEqual(out["decision"], "block")
        self.assertIs(out["continue"], False)
        self.assertNotIn(CANARY, buf.getvalue())


class TestIdentifierFieldsPreserved(unittest.TestCase):
    def test_write_file_path_is_not_normalized(self):
        """The chain now matches ``*``. Rewriting a path would tell Claude it
        wrote a file that does not exist on disk."""
        nfd_path = "/p/cafe\u0301.txt"
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            {"filePath": nfd_path, "success": True},
            key="tool_response",
            tool_name="Write",
        )
        self.assertEqual(rc, 0, stderr)
        # The NFC form must appear nowhere: either nothing is emitted, or the
        # decomposed path is emitted verbatim.
        self.assertNotIn("caf\u00e9", stdout)
        if stdout.strip():
            updated = json.loads(stdout)["hookSpecificOutput"]["updatedToolOutput"]
            self.assertEqual(updated["filePath"], nfd_path)

    def test_secrets_are_still_redacted_in_identifier_fields(self):
        """Redaction still walks identifier fields — it only replaces matched
        secret patterns, it does not reshape the string."""
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            {"url": f"https://example.test/?t={PLAIN_PAT}", "success": True},
            key="tool_response",
            tool_name="WebFetch",
        )
        self.assertEqual(rc, 0, stderr)
        self.assertNotIn(PLAIN_PAT, stdout)


class TestChainFailsClosedOnUnreadableInput(unittest.TestCase):
    """The chain is the only PostToolUse entry point Claude Code schedules.

    Input it cannot read must not silently skip canary detection the way the
    fail-open sanitizer stages do.
    """

    def _run(self, raw: str, *, armed: bool = True) -> tuple[int, str]:
        env = {k: v for k, v in os.environ.items() if k != "FULLSEND_CANARY_TOKEN"}
        if armed:
            env["FULLSEND_CANARY_TOKEN"] = CANARY
        proc = subprocess.run(
            [sys.executable, CHAIN_HOOK],
            input=raw,
            capture_output=True,
            text=True,
            timeout=30,
            env=env,
        )
        return proc.returncode, proc.stdout

    def test_malformed_json_blocks_when_canary_armed(self):
        rc, stdout = self._run("{not json")
        self.assertEqual(rc, 1)
        out = json.loads(stdout)
        self.assertEqual(out["decision"], "block")
        self.assertIs(out["continue"], False)

    def test_oversized_input_blocks_when_canary_armed(self):
        oversized = json.dumps({"tool_name": "Read", "tool_response": "x" * (11 * 1024 * 1024)})
        rc, stdout = self._run(oversized)
        self.assertEqual(rc, 1)
        self.assertEqual(json.loads(stdout)["decision"], "block")

    def test_malformed_json_still_passes_through_when_canary_disarmed(self):
        """Without a canary token the chain stays fail-open, as documented."""
        rc, stdout = self._run("{not json", armed=False)
        self.assertEqual(rc, 0)
        self.assertEqual(stdout.strip(), "")

    def test_empty_stdin_is_allowed(self):
        rc, stdout = self._run("")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout.strip(), "")


class TestCanaryRedactionIsCaseFoldSafe(unittest.TestCase):
    def test_case_expanding_prefix_does_not_leave_token_behind(self):
        """Regression: redaction walked a ``str.lower()`` copy using indices
        from the original string. A character that case-folds to more code
        points than it started with (``İ`` → two) desynchronized the two, and
        a detected token could survive into updatedToolOutput."""
        leaked = "\u0130" * 20 + CANARY
        rc, stdout, stderr = run_hook(
            CHAIN_HOOK,
            {"stdout": leaked, "stderr": "", "interrupted": False, "isImage": False},
            key="tool_response",
            env_extra={"FULLSEND_CANARY_TOKEN": CANARY},
            tool_name="Bash",
            tool_input={"command": "echo hi"},
        )
        self.assertEqual(rc, 1, stderr)
        self.assertNotIn(CANARY, stdout)
        out = json.loads(stdout)
        self.assertEqual(out["decision"], "block")

    def test_hook_io_redacts_what_it_detects(self):
        import hook_io

        for prefix in ("", "\u0130" * 5, "\u0130" * 20):
            value = {"stdout": prefix + CANARY}
            self.assertTrue(hook_io.contains_canary(value, CANARY), prefix)
            redacted = hook_io.redact_canary(value, CANARY)
            self.assertFalse(hook_io.contains_canary(redacted, CANARY), prefix)


def run_raw(body: dict, env_extra: dict[str, str] | None = None) -> tuple[int, str, str]:
    env = {k: v for k, v in os.environ.items() if k != "FULLSEND_CANARY_TOKEN"}
    env.update(env_extra or {})
    proc = subprocess.run(
        [sys.executable, CHAIN_HOOK],
        input=json.dumps(body),
        capture_output=True,
        text=True,
        timeout=10,
        env=env,
    )
    return proc.returncode, proc.stdout, proc.stderr


def read_payload(content: str) -> dict:
    return {
        "type": "text",
        "file": {
            "filePath": "/r/f",
            "content": content,
            "numLines": 1,
            "startLine": 1,
            "totalLines": 1,
        },
    }


class TestContentPreservedAndRewriteNotes(unittest.TestCase):
    """What the agent reads must be what is on disk unless a control fired,
    and when one fires the agent is told (additionalContext)."""

    def test_source_read_not_rewritten(self):
        rc, stdout, _ = run_hook(
            CHAIN_HOOK,
            read_payload("const token = request.headers.authorization;\n"),
            key="tool_response",
            tool_input={"file_path": "/r/f"},
        )
        self.assertEqual(rc, 0)
        self.assertNotIn("updatedToolOutput", stdout)

    def test_cjk_read_not_rewritten(self):
        rc, stdout, _ = run_hook(
            CHAIN_HOOK,
            read_payload("使い方：`make`（必須）！ \ufb01le\n"),
            key="tool_response",
            tool_input={"file_path": "/r/f"},
        )
        self.assertEqual(rc, 0)
        self.assertNotIn("updatedToolOutput", stdout)

    def test_redaction_adds_additional_context(self):
        rc, stdout, _ = run_hook(
            CHAIN_HOOK,
            {
                "stdout": "export API_KEY=supersecretvalue\n",
                "stderr": "",
                "interrupted": False,
                "isImage": False,
            },
            key="tool_response",
            tool_name="Bash",
            tool_input={"command": "cat .env"},
        )
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertNotIn(
            "supersecretvalue", json.dumps(out["hookSpecificOutput"]["updatedToolOutput"])
        )
        self.assertIn("masked", out["hookSpecificOutput"]["additionalContext"])

    def test_suppression_adds_additional_context(self):
        rc, stdout, _ = run_hook(
            CHAIN_HOOK,
            {
                "stdout": "ok  \tgithub.com/o/r\t0.1s\n",
                "stderr": "",
                "interrupted": False,
                "isImage": False,
            },
            key="tool_response",
            tool_name="Bash",
            tool_input={"command": "go test ./..."},
        )
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertIn("condensed", out["hookSpecificOutput"]["additionalContext"])
        self.assertIn("go test ./...", out["hookSpecificOutput"]["additionalContext"])

    def test_compound_failing_command_not_condensed(self):
        rc, stdout, _ = run_hook(
            CHAIN_HOOK,
            {
                "stdout": "3 failed, 2 passed in 1.2s\nok  \tfoo\t0.5s\nEXIT=1\n",
                "stderr": "",
                "interrupted": False,
                "isImage": False,
            },
            key="tool_response",
            tool_name="Bash",
            tool_input={"command": "uvx pytest -q; go test ./... ; echo EXIT=$?"},
        )
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")


class TestPostToolUseFailure(unittest.TestCase):
    """Failed calls carry error text only: canary detection halts, nothing
    is rewritten, sanitizers do not run."""

    def _body(self, error: str) -> dict:
        return {
            "hook_event_name": "PostToolUseFailure",
            "tool_name": "Bash",
            "tool_input": {"command": "curl x"},
            "error": error,
        }

    def test_canary_in_error_blocks_and_halts(self):
        rc, stdout, _ = run_raw(
            self._body(f"Exit code 1\nleak {CANARY}"), {"FULLSEND_CANARY_TOKEN": CANARY}
        )
        self.assertEqual(rc, 1)
        out = json.loads(stdout)
        self.assertEqual(out["decision"], "block")
        self.assertIs(out["continue"], False)
        self.assertNotIn("updatedToolOutput", stdout)

    def test_fullwidth_canary_in_error_blocks(self):
        rc, stdout, _ = run_raw(
            self._body(f"Exit code 1\nleak {to_fullwidth(CANARY)}"),
            {"FULLSEND_CANARY_TOKEN": CANARY},
        )
        self.assertEqual(rc, 1)
        self.assertEqual(json.loads(stdout)["decision"], "block")

    def test_clean_error_passes(self):
        rc, stdout, _ = run_raw(
            self._body("Exit code 1\nno such file"), {"FULLSEND_CANARY_TOKEN": CANARY}
        )
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_sanitizers_do_not_run_on_failures(self):
        rc, stdout, _ = run_raw(self._body("Exit code 1\nexport API_KEY=supersecretvalue"))
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")


class TestPostToolUseFailureShapeDrift(unittest.TestCase):
    def test_structured_error_is_still_scanned(self):
        body = {
            "hook_event_name": "PostToolUseFailure",
            "tool_name": "Bash",
            "tool_input": {"command": "curl x"},
            "error": {"message": "Exit code 1", "stderr": f"leak {CANARY}"},
        }
        rc, stdout, _ = run_raw(body, {"FULLSEND_CANARY_TOKEN": CANARY})
        self.assertEqual(rc, 1)
        self.assertEqual(json.loads(stdout)["decision"], "block")

    def test_nfkc_redaction_prefers_hidden_secret_over_equal_count(self):
        # One plain PAT the original catches, one fullwidth PAT only the
        # normalized copy catches: counts tie at 1 unless compared by value.
        plain = "ghp_" + "B" * 36
        hidden = to_fullwidth("ghp_" + "C" * 36)
        rc, stdout, _ = run_hook(CHAIN_HOOK, f"{plain} {hidden}", key="tool_response")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        emitted = json.dumps(out["hookSpecificOutput"]["updatedToolOutput"])
        self.assertNotIn(hidden, emitted)
        self.assertNotIn(plain, emitted)


class TestObfuscationRoundTwo(unittest.TestCase):
    def test_failure_payload_with_other_key_still_scanned(self):
        body = {
            "hook_event_name": "PostToolUseFailure",
            "tool_name": "Bash",
            "tool_input": {"command": "curl x"},
            "tool_error": f"Exit code 1\nleak {CANARY}",
        }
        rc, stdout, _ = run_raw(body, {"FULLSEND_CANARY_TOKEN": CANARY})
        self.assertEqual(rc, 1)

    def test_selector_interleaved_fullwidth_secret_redacted(self):
        pat = "ghp_" + "D" * 36
        hidden = "\ufe0f".join(to_fullwidth(pat))
        rc, stdout, _ = run_hook(CHAIN_HOOK, f"GITHUB_TOKEN={hidden}", key="tool_response")
        self.assertEqual(rc, 0)
        emitted = json.dumps(json.loads(stdout)["hookSpecificOutput"]["updatedToolOutput"])
        self.assertNotIn(hidden, emitted)
        self.assertNotIn(pat, emitted)

    def test_combining_mark_canary_blocks(self):
        hidden = "".join(c + "\u0300" for c in CANARY)
        rc, stdout, _ = run_hook(
            CHAIN_HOOK,
            {"stdout": f"leak {hidden}", "stderr": "", "interrupted": False, "isImage": False},
            key="tool_response",
            env_extra={"FULLSEND_CANARY_TOKEN": CANARY},
            tool_name="Bash",
            tool_input={"command": "echo"},
        )
        self.assertEqual(rc, 1)
        self.assertEqual(json.loads(stdout)["decision"], "block")


class TestNfkcSamePrefix(unittest.TestCase):
    def test_same_prefix_fullwidth_secret_still_redacted(self):
        plain = "abcd1234efgh5678"
        hidden = to_fullwidth("abcd5678ijklmnop")
        rc, stdout, _ = run_hook(
            CHAIN_HOOK, f"TOKEN={plain}\nTOKEN2={hidden}\n", key="tool_response"
        )
        self.assertEqual(rc, 0)
        emitted = json.dumps(json.loads(stdout)["hookSpecificOutput"]["updatedToolOutput"])
        self.assertNotIn(hidden, emitted)
        self.assertNotIn(plain, emitted)


if __name__ == "__main__":
    unittest.main()
