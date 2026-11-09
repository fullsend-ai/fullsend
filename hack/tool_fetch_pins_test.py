"""Tests for the check-tool-fetch-pins hook and its Renovate managers (#9226)."""

import json
import re
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
CHECKED_FILES = [ROOT / ".pre-commit-config.yaml", ROOT / "Makefile"]


def hook_pattern() -> re.Pattern[str]:
    text = (ROOT / ".pre-commit-config.yaml").read_text()
    m = re.search(r"- id: check-tool-fetch-pins\n(?:\s+\w+:.*\n)*?\s+entry: '(.*)'\n", text)
    assert m, "check-tool-fetch-pins entry not found"
    return re.compile(m.group(1).replace("''", "'"))


def renovate_patterns() -> list[re.Pattern[str]]:
    config = json.loads((ROOT / "renovate.json").read_text())
    return [
        re.compile(s.replace("(?<", "(?P<"))
        for manager in config["customManagers"]
        for s in manager["matchStrings"]
        if re.match(r"\\b(uvx|npx)", s)
    ]


@pytest.mark.parametrize(
    "line",
    [
        "uvx ty check .",
        "\tuvx ruff format .",
        "npx lint-staged --allow-empty",
        "npx @scope/tool run",
        "uvx ty@latest check",
        "npx lint-staged@^16",
        "uvx --from gitlint-core gitlint",
        "npx -y lint-staged",
        "uvx ty==0.0.99 check",
        'bash -c "uvx ty"',
    ],
)
def test_flags_unpinned_fetch(line: str) -> None:
    assert hook_pattern().search(line)


@pytest.mark.parametrize(
    "line",
    [
        "uvx ty@0.0.99 check --ignore import-not-found",
        "\tuvx ruff@0.15.7 format .",
        "npx lint-staged@16.4.0 --allow-empty",
        "npx @scope/tool@1.2.3 run",
    ],
)
def test_passes_pinned_fetch(line: str) -> None:
    assert not hook_pattern().search(line)


@pytest.mark.parametrize("path", CHECKED_FILES, ids=lambda p: p.name)
def test_checked_files_are_clean_and_tracked(path: Path) -> None:
    """Every uvx/npx call passes the hook, and Renovate extracts a version from each one."""
    pattern = hook_pattern()
    trackers = renovate_patterns()
    for line in path.read_text().splitlines():
        assert not pattern.search(line), line
        for call in re.finditer(r"\b(uvx|npx)\s+\S+", line):
            assert any(t.match(line, call.start()) for t in trackers), line
