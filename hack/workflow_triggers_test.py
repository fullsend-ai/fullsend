"""No workflow in .github/workflows/ may trigger on pull_request_target (#9245).

GitHub blocks pull_request_target by default on public repos from 2026-12-02,
and a blocked run ends in startup_failure, which cannot be re-run. Same-repo
PRs use pull_request; fork PRs run e2e through a maintainer's /ok-to-test
comment (e2e-ok-to-test.yml). The check reads the `on:` keys, so a workflow
that only compares github.event_name strings (reusable-dispatch.yml) passes.
"""

from pathlib import Path

import pytest
import yaml

WORKFLOWS = Path(__file__).resolve().parent.parent / ".github" / "workflows"

# The consumer shim moves in #9230; drop it from this set when that merges.
ALLOWED = {"fullsend.yaml"}


def triggers(path: Path) -> set[str]:
    doc = yaml.safe_load(path.read_text()) or {}
    # YAML 1.1 reads a bare `on` key as boolean True.
    on = doc.get("on", doc.get(True))
    if isinstance(on, str):
        return {on}
    if isinstance(on, list):
        return set(on)
    if isinstance(on, dict):
        return set(on)
    return set()


FILES = sorted(p for p in WORKFLOWS.iterdir() if p.suffix in {".yml", ".yaml"})


@pytest.mark.parametrize("path", FILES, ids=lambda p: p.name)
def test_no_pull_request_target_trigger(path: Path) -> None:
    if path.name in ALLOWED:
        pytest.skip("moves in #9230")
    assert "pull_request_target" not in triggers(path)


def test_allowlist_entries_exist() -> None:
    names = {p.name for p in FILES}
    assert names >= ALLOWED


def test_trigger_parser_reads_on_keys(tmp_path: Path) -> None:
    wf = tmp_path / "x.yml"
    wf.write_text("on:\n  pull_request_target:\n    types: [opened]\n")
    assert triggers(wf) == {"pull_request_target"}
    wf.write_text("on: [push, pull_request_target]\n")
    assert triggers(wf) == {"push", "pull_request_target"}
    wf.write_text(
        "on: workflow_call\njobs:\n  a:\n    if: github.event_name == 'pull_request_target'\n"
    )
    assert triggers(wf) == {"workflow_call"}
