"""Tests for hook_io PostToolUse helpers."""

from __future__ import annotations

import hook_io


def test_scan_text_sees_stderr_when_stdout_empty():
    text = hook_io.scan_text({"stdout": "", "stderr": "CANARY_LEAK", "interrupted": False})
    assert "CANARY_LEAK" in text


def test_scan_text_joins_fields_on_a_boundary():
    """Fields are newline-joined so a needle cannot match across a boundary.

    A cross-boundary match would be unredactable: the redactors rewrite each
    string field independently.
    """
    text = hook_io.scan_text({"stdout": "out-", "stderr": "err"})
    assert text == "out-\nerr"
    assert "out-err" not in text


def test_scan_text_skips_empty_fields():
    assert hook_io.scan_text({"stdout": "out", "stderr": ""}) == "out"


def test_v1_text_serializes_structured_shapes():
    assert hook_io.v1_text("plain") == "plain"
    assert hook_io.v1_text({"stdout": "a", "stderr": "b"}) == '{"stdout": "a", "stderr": "b"}'


def test_transform_strings_skips_identifier_keys():
    value = {"filePath": "/p/a\u200bb.txt", "stdout": "x\u200by"}
    out = hook_io.transform_strings(
        value, lambda t: t.replace("\u200b", ""), skip_keys=hook_io.IDENTIFIER_KEYS
    )
    assert out["filePath"] == "/p/a\u200bb.txt"
    assert out["stdout"] == "xy"


def test_scan_text_walks_nested_mcp_body():
    assert "SECRET" in hook_io.scan_text({"body": "SECRET"})


def test_apply_text_blanks_stderr_on_bash_object():
    original = {"stdout": "verbose\nlogs\n", "stderr": "warning", "interrupted": False}
    updated = hook_io.apply_text(original, "go test: passed")
    assert updated["stdout"] == "go test: passed"
    assert updated["stderr"] == ""
    assert updated["interrupted"] is False


def test_apply_text_preserves_unrecognized_dict_shape():
    original = {"chunks": [{"bytes": "secret"}], "interrupted": False}
    updated = hook_io.apply_text(original, "summary")
    assert updated is original
    assert not hook_io.has_text_slot(original)


def test_has_text_slot_bash_object():
    assert hook_io.has_text_slot({"stdout": "", "stderr": "x"})


def test_looks_failed_exit_code_prefix():
    assert hook_io.looks_failed("Exit code 1\nboom", "Exit code 1\nboom")


def test_looks_failed_interrupted_bash_object():
    value = {"stdout": "partial", "stderr": "", "interrupted": True}
    assert hook_io.looks_failed(value, hook_io.scan_text(value))


def test_looks_failed_successful_bash_object():
    value = {"stdout": "ok", "stderr": "", "interrupted": False}
    assert not hook_io.looks_failed(value, hook_io.scan_text(value))


def test_redact_canary_empty_token_is_noop():
    value = {"stdout": "hello", "stderr": "world"}
    assert hook_io.redact_canary(value, "") is value
    assert hook_io.redact_canary(value, "   ") is value


def test_redact_canary_walks_stderr():
    value = {"stdout": "", "stderr": "leaked SECRET_CANARY_xyz here"}
    updated = hook_io.redact_canary(value, "SECRET_CANARY_xyz")
    assert updated["stderr"] == "leaked [CANARY_REDACTED] here"
    assert updated["stdout"] == ""


def test_nfkc_copy_normalizes_every_string():
    value = {"stdout": "\uff21\uff22", "nested": ["\uff23"], "n": 1}
    assert hook_io.nfkc(value) == {"stdout": "AB", "nested": ["C"], "n": 1}
    assert value["stdout"] == "\uff21\uff22"  # a copy, never in place


def test_emit_updated_carries_additional_context(capsys):
    hook_io.emit_updated({"stdout": "x", "stderr": ""}, additional_context="fullsend: note")
    out = capsys.readouterr().out
    assert '"additionalContext": "fullsend: note"' in out
    hook_io.emit_updated("y")
    assert "additionalContext" not in capsys.readouterr().out


def test_detection_form_strips_marks_and_selectors():
    assert hook_io.nfkc("C\u0300A\ufe0fN\U000e0100") == "CAN"


def test_detection_form_folds_every_splitter():
    for char in ("\u200b", "\u202e", "\U000e0041", "\u0300", "\ufe0f"):
        assert hook_io.nfkc("CAN" + char + "ARY") == "CANARY", repr(char)


def test_detection_form_keeps_field_separators():
    # scan_text joins fields with a newline so a needle cannot match across a
    # boundary; folding that away would manufacture unredactable matches.
    assert hook_io.nfkc("out-\nerr") == "out-\nerr"


def test_detection_form_folds_separators_controls_and_escapes():
    for char in ("\u2028", "\u2029", "\x00", "\x1b[31m", "\x1b]8;;X\x07", "\x1b"):
        assert hook_io.nfkc("CAN" + char + "ARY") == "CANARY", repr(char)


def test_detection_form_keeps_tabs_and_returns_with_the_newline():
    # scan_text's field separator and ordinary whitespace must survive, or
    # detection would manufacture matches across field boundaries.
    assert hook_io.nfkc("a\tb\r\nc") == "a\tb\r\nc"
