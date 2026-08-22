#!/usr/bin/env python3
"""Unit tests for unicode_posttool.py hook."""

import json
import subprocess
import sys
import unittest
from pathlib import Path

HOOK = str(Path(__file__).parent / "unicode_posttool.py")


def run_hook(tool_result: str | None = None, stdin_raw: str | None = None) -> tuple[int, str, str]:
    """Run the hook script and return (exit_code, stdout, stderr)."""
    if stdin_raw is None:
        if tool_result is None:
            stdin_raw = ""
        else:
            stdin_raw = json.dumps({"tool_name": "Read", "tool_result": tool_result})
    proc = subprocess.run(
        [sys.executable, HOOK],
        input=stdin_raw,
        capture_output=True,
        text=True,
        timeout=10,
    )
    return proc.returncode, proc.stdout, proc.stderr


class TestCleanInput(unittest.TestCase):
    def test_clean_text_passes_through(self):
        rc, stdout, _ = run_hook("Hello, world!")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_empty_stdin(self):
        rc, stdout, _ = run_hook(stdin_raw="")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_non_json_stdin(self):
        rc, stdout, _ = run_hook(stdin_raw="not json")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_non_dict_json(self):
        rc, stdout, _ = run_hook(stdin_raw=json.dumps([1, 2, 3]))
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_non_string_tool_result(self):
        rc, stdout, _ = run_hook(stdin_raw=json.dumps({"tool_result": 42}))
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_empty_tool_result(self):
        rc, stdout, _ = run_hook(tool_result="")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")


class TestZeroWidth(unittest.TestCase):
    def test_zero_width_space_stripped(self):
        rc, stdout, _ = run_hook("hello\u200bworld")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "helloworld")
        self.assertIn("zero_width", out["metadata"]["categories"])

    def test_soft_hyphen_stripped(self):
        rc, stdout, _ = run_hook("pass\u00adword")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "password")

    def test_word_joiner_stripped(self):
        rc, stdout, _ = run_hook("test\u2060data")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "testdata")
        self.assertIn("zero_width", out["metadata"]["categories"])


class TestBidiOverride(unittest.TestCase):
    def test_bidi_override_stripped(self):
        rc, stdout, _ = run_hook("abc\u202edef")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "abcdef")
        self.assertIn("bidi_override", out["metadata"]["categories"])

    def test_bidi_isolate_stripped(self):
        rc, stdout, _ = run_hook("abc\u2066def\u2069")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "abcdef")
        self.assertIn("bidi_override", out["metadata"]["categories"])


class TestTagCharacters(unittest.TestCase):
    def test_tag_chars_stripped(self):
        # Tag-encode "HI" (U+E0048 U+E0049)
        payload = "clean\U000e0048\U000e0049text"
        rc, stdout, _ = run_hook(payload)
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "cleantext")
        self.assertIn("tag_char", out["metadata"]["categories"])

    def test_tag_chars_always_exit_zero(self):
        payload = "\U000e0048\U000e0049"
        rc, _, _ = run_hook(payload)
        self.assertEqual(rc, 0)


class TestNullBytes(unittest.TestCase):
    def test_null_bytes_stripped(self):
        rc, stdout, _ = run_hook("hello\x00world")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "helloworld")
        self.assertIn("null_byte", out["metadata"]["categories"])


class TestAnsiEscape(unittest.TestCase):
    def test_ansi_color_stripped(self):
        rc, stdout, _ = run_hook("hello\x1b[31mred\x1b[0m")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "hellored")
        self.assertIn("ansi_escape", out["metadata"]["categories"])

    def test_osc_hyperlink_stripped(self):
        # OSC 8 hyperlink: ESC ] 8 ; ; url BEL text ESC ] 8 ; ; BEL
        payload = "before\x1b]8;;http://evil.com\x07click\x1b]8;;\x07after"
        rc, stdout, _ = run_hook(payload)
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertNotIn("evil.com", out["tool_result"])
        self.assertIn("osc_escape", out["metadata"]["categories"])


class TestNFKC(unittest.TestCase):
    def test_fullwidth_kept_and_reported(self):
        # Fullwidth A = U+FF21. Compatibility characters are content (CJK
        # punctuation, ligatures); they are reported, never rewritten, and
        # detection runs on a normalized copy in the chain driver.
        rc, stdout, _ = run_hook("\uff21\uff22\uff23")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertNotIn("hookSpecificOutput", out)
        self.assertIn("fullwidth", out["metadata"]["categories"])

    def test_cjk_content_untouched(self):
        text = "使い方：`make`（必須）！ \ufb01le \u00bd\n"
        rc, stdout, _ = run_hook(text)
        self.assertEqual(rc, 0)
        self.assertNotIn("hookSpecificOutput", stdout)


class TestVariationSelector(unittest.TestCase):
    def test_variation_selector_stripped(self):
        rc, stdout, _ = run_hook("test\ufe0fdata")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "testdata")
        self.assertIn("variation_selector", out["metadata"]["categories"])

    def test_emoji_presentation_selector_kept(self):
        # One selector after a non-ASCII character is ordinary text ("⚠️");
        # an Edit composed from a stripped copy would not match the file.
        rc, stdout, _ = run_hook("\u26a0\ufe0f warn \u845b\ufe00\u57ce")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_variation_selector_run_stripped(self):
        rc, stdout, _ = run_hook("a\ufe0f\ufe0f\ufe0fb")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "ab")
        self.assertIn("variation_selector", out["metadata"]["categories"])


class TestNFKCEscapeBypass(unittest.TestCase):
    def test_fullwidth_bracket_csi_detected_post_nfkc(self):
        """R2-2: Fullwidth [ + ESC must be caught after NFKC normalization."""
        # ESC + fullwidth [ (U+FF3B) + "31m" → NFKC → ESC[31m (valid CSI)
        payload = "\x1b\uff3b31m"
        rc, stdout, _ = run_hook(f"text{payload}more")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertNotIn("\x1b", out["tool_result"])
        self.assertIn("ansi_escape", out["metadata"]["categories"])
        self.assertIn("fullwidth", out["metadata"]["categories"])

    def test_fullwidth_bracket_osc_detected_post_nfkc(self):
        """R2-2: Fullwidth ] + ESC must be caught after NFKC normalization."""
        # ESC + fullwidth ] (U+FF3D) + OSC payload + BEL
        payload = "\x1b\uff3d8;;http://evil.com\x07"
        rc, stdout, _ = run_hook(f"before{payload}after")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertNotIn("evil.com", out["tool_result"])
        self.assertIn("osc_escape", out["metadata"]["categories"])


class TestOSCPerformance(unittest.TestCase):
    def test_unterminated_osc_linear_time(self):
        """R2-3: Dense unterminated ESC] must not cause quadratic backtracking."""
        import time

        # 10K unterminated ESC] sequences — should complete in well under 1s
        payload = "\x1b]AAAA" * 10000
        start = time.time()
        rc, stdout, _ = run_hook(payload)
        elapsed = time.time() - start
        self.assertEqual(rc, 0)
        self.assertLess(elapsed, 1.0, f"OSC scan took {elapsed:.2f}s, expected < 1s")


class TestProtocol(unittest.TestCase):
    def test_no_decoded_text_in_stdout(self):
        """C1: Decoded tag char text must NOT appear in stdout (injection vector)."""
        # Tag-encode "INJECT" → U+E0049 U+E004E U+E004A U+E0045 U+E0043 U+E0054
        payload = "\U000e0049\U000e004e\U000e004a\U000e0045\U000e0043\U000e0054"
        rc, stdout, _ = run_hook(f"clean{payload}text")
        self.assertEqual(rc, 0)
        self.assertNotIn("INJECT", stdout)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "cleantext")

    def test_always_returns_tool_result(self):
        rc, stdout, _ = run_hook("has\u200bzero\u200bwidth")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertIn("tool_result", out)
        self.assertEqual(out["hookSpecificOutput"]["hookEventName"], "PostToolUse")
        self.assertEqual(out["hookSpecificOutput"]["updatedToolOutput"], out["tool_result"])

    def test_tool_response_payload(self):
        rc, stdout, _ = run_hook(
            stdin_raw=json.dumps({"tool_name": "Read", "tool_response": "hello\u200bworld"})
        )
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "helloworld")
        self.assertEqual(out["hookSpecificOutput"]["updatedToolOutput"], "helloworld")

    def test_metadata_present(self):
        rc, stdout, _ = run_hook("has\u200bzero")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertIn("metadata", out)
        self.assertIn("unicode_findings", out["metadata"])
        self.assertIn("categories", out["metadata"])


class TestIdeographicVariationSequences(unittest.TestCase):
    def test_single_ivs_kept(self):
        # U+845B + U+E0100 is a registered ideographic variation sequence.
        rc, stdout, _ = run_hook("\u845b\U000e0100\u57ce")
        self.assertEqual(rc, 0)
        self.assertEqual(stdout, "")

    def test_ivs_run_stripped(self):
        rc, stdout, _ = run_hook("a\U000e0100\U000e0101b")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertEqual(out["tool_result"], "ab")
        self.assertIn("variation_selector", out["metadata"]["categories"])

    def test_detection_only_findings_do_not_emit_a_rewrite(self):
        rc, stdout, _ = run_hook("\uff21\uff22")
        self.assertEqual(rc, 0)
        out = json.loads(stdout)
        self.assertNotIn("hookSpecificOutput", out)
        self.assertIn("fullwidth", out["metadata"]["categories"])


if __name__ == "__main__":
    unittest.main()
