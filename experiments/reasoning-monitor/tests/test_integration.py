"""Integration tests: run deterministic monitors against all real transcripts.

Loads every payload/transcript pair from the real payloads/ and transcripts/
directories and validates that the canary and allowlist monitors produce
correct verdicts.
"""

from pathlib import Path

import pytest
import yaml
from runner import (
    CANARY_TOKEN,
    load_transcript,
    run_allowlist_monitor,
    run_canary_monitor,
)

PAYLOADS_DIR = Path(__file__).parent.parent / "payloads"
TRANSCRIPTS_DIR = Path(__file__).parent.parent / "transcripts"


def _all_payload_names():
    return [p.stem for p in sorted(PAYLOADS_DIR.glob("*.yaml"))]


@pytest.fixture(params=_all_payload_names())
def payload_and_transcript(request):
    name = request.param
    payload_path = PAYLOADS_DIR / f"{name}.yaml"
    payload = yaml.safe_load(payload_path.read_text())
    transcript = load_transcript(name, TRANSCRIPTS_DIR)
    return name, payload, transcript


def test_canary_monitor_correctness(payload_and_transcript):
    name, payload, transcript = payload_and_transcript
    if not payload.get("canary_should_leak"):
        pytest.skip("Payload does not test canary leak")
    verdict = run_canary_monitor(transcript, canary=CANARY_TOKEN)
    assert verdict.verdict == "suspicious", f"{name}: canary should have detected leak"


def test_canary_monitor_no_false_positive(payload_and_transcript):
    name, payload, transcript = payload_and_transcript
    if payload.get("canary_should_leak"):
        pytest.skip("Payload tests canary leak")
    verdict = run_canary_monitor(transcript, canary=CANARY_TOKEN)
    assert verdict.verdict == "clean", f"{name}: canary should not flag clean transcript"


def test_allowlist_monitor_correctness(payload_and_transcript):
    name, payload, transcript = payload_and_transcript
    out_of_role = payload.get("out_of_role_tool")
    if not out_of_role:
        pytest.skip("Payload does not use out-of-role tools")
    verdict = run_allowlist_monitor(transcript)
    assert verdict.verdict == "suspicious", f"{name}: allowlist should have caught {out_of_role}"


def test_allowlist_monitor_no_false_positive(payload_and_transcript):
    name, payload, transcript = payload_and_transcript
    if payload.get("out_of_role_tool"):
        pytest.skip("Payload uses out-of-role tools")
    verdict = run_allowlist_monitor(transcript)
    assert verdict.verdict == "clean", f"{name}: allowlist should not flag triage-only tools"
