"""Tests for the ForbiddenTypeScope gitlint rule."""

import pytest
from gitlint.rules import RuleViolation

from gitlint_rules.forbidden_type_scope import ForbiddenTypeScope


class FakeCommit:
    """Minimal stand-in for a gitlint commit object."""

    def __init__(self, title):
        self.message = type("msg", (), {"title": title})()


def run_rule(title):
    """Run ForbiddenTypeScope against a commit title and return violations."""
    rule = ForbiddenTypeScope()
    commit = FakeCommit(title)
    return rule.validate(commit)


# --- should be rejected ---


@pytest.mark.parametrize(
    "title",
    [
        "fix(ci): update workflow",
        "feat(ci): add new job",
        "fix(e2e): repair test",
        "feat(e2e): add new test",
    ],
)
def test_rejects_forbidden_combinations(title):
    violations = run_rule(title)
    assert violations, f"Expected violation for {title!r}"
    assert len(violations) == 1
    assert isinstance(violations[0], RuleViolation)


def test_fix_ci_suggests_ci_subsystem():
    violations = run_rule("fix(ci): update workflow")
    assert "ci(<subsystem>)" in violations[0].message.lower()


def test_feat_e2e_suggests_ci_e2e():
    violations = run_rule("feat(e2e): add new test")
    assert "ci(e2e)" in violations[0].message.lower()


# --- should be allowed ---


@pytest.mark.parametrize(
    "title",
    [
        "ci(lint): update linter config",
        "ci(e2e): fix flaky test",
        "fix(mint): correct token refresh",
        "feat(review-agent): add outcome labels",
        "chore(ci): bump action version",
        "test(e2e): add new scenario",
        "refactor(ci): simplify matrix",
        "docs: update readme",
        "fix(#123): handle nil pointer",
    ],
)
def test_allows_valid_combinations(title):
    violations = run_rule(title)
    assert not violations, f"Unexpected violation for {title!r}: {violations}"


# --- should not crash on non-conventional titles ---


@pytest.mark.parametrize(
    "title",
    [
        "just a plain message",
        "WIP",
        "",
    ],
)
def test_ignores_non_conventional(title):
    violations = run_rule(title)
    assert not violations
