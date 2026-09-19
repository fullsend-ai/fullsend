#!/usr/bin/env python3
"""Unit tests for ADR reference-title checks in lint-adr-frontmatter."""

from __future__ import annotations

import importlib.util
import unittest
from importlib.machinery import SourceFileLoader
from pathlib import Path

HOOK = Path(__file__).resolve().parent / "lint-adr-frontmatter"
_LOADER = SourceFileLoader("lint_adr_frontmatter", str(HOOK))
_SPEC = importlib.util.spec_from_loader(_LOADER.name, _LOADER)
assert _SPEC is not None
lint_adr_frontmatter = importlib.util.module_from_spec(_SPEC)
_LOADER.exec_module(lint_adr_frontmatter)


class TestExpectedRefTitle(unittest.TestCase):
    def test_strips_leading_number_prefix(self):
        self.assertEqual(
            lint_adr_frontmatter.expected_ref_title(
                "7. Per-role GitHub Apps with manifest-based creation"
            ),
            "Per-role GitHub Apps with manifest-based creation",
        )

    def test_strips_multi_digit_prefix(self):
        self.assertEqual(
            lint_adr_frontmatter.expected_ref_title(
                "17. Credential Isolation for Sandboxed Agents"
            ),
            "Credential Isolation for Sandboxed Agents",
        )

    def test_unchanged_when_no_prefix(self):
        self.assertEqual(
            lint_adr_frontmatter.expected_ref_title("Use ADRs for decision making"),
            "Use ADRs for decision making",
        )


class TestCheckReferenceTitles(unittest.TestCase):
    def setUp(self):
        self.titles = {
            "0007": "Per-role GitHub Apps with manifest-based creation",
            "0017": "Credential Isolation for Sandboxed Agents",
        }
        self.path = "docs/ADRs/0002-initial-fullsend-design.md"

    def test_matching_title_ok(self):
        content = (
            "- [ADR 0007 — Per-role GitHub Apps with manifest-based creation]"
            "(0007-per-role-github-apps.md)\n"
        )
        self.assertEqual(
            lint_adr_frontmatter.check_reference_titles(self.path, content, self.titles),
            [],
        )

    def test_truncated_title_fails(self):
        content = "- [ADR 0007 — Per-role GitHub Apps](0007-per-role-github-apps.md)\n"
        errors = lint_adr_frontmatter.check_reference_titles(self.path, content, self.titles)
        self.assertEqual(len(errors), 1)
        self.assertIn("does not match frontmatter title", errors[0])
        self.assertIn("Per-role GitHub Apps with manifest-based creation", errors[0])

    def test_case_mismatch_fails(self):
        content = (
            "- [ADR 0017 — Credential isolation for sandboxed agents]"
            "(./0017-credential-isolation-for-sandboxed-agents.md)\n"
        )
        errors = lint_adr_frontmatter.check_reference_titles(self.path, content, self.titles)
        self.assertEqual(len(errors), 1)
        self.assertIn("Credential Isolation for Sandboxed Agents", errors[0])

    def test_colon_form_ignored(self):
        content = (
            "- [ADR 0007: Per-role GitHub Apps](0007-per-role-github-apps.md) "
            "— authentication model\n"
        )
        self.assertEqual(
            lint_adr_frontmatter.check_reference_titles(self.path, content, self.titles),
            [],
        )

    def test_number_only_link_ignored(self):
        content = "- [ADR 0007](0007-per-role-github-apps.md) — original design\n"
        self.assertEqual(
            lint_adr_frontmatter.check_reference_titles(self.path, content, self.titles),
            [],
        )

    def test_unknown_number_fails(self):
        content = "- [ADR 9999 — Missing](9999-missing.md)\n"
        errors = lint_adr_frontmatter.check_reference_titles(self.path, content, {})
        self.assertEqual(len(errors), 1)
        self.assertIn("unknown ADR 9999", errors[0])

    def test_hyphen_form_ignored(self):
        content = (
            "- [ADR 0007 - Per-role GitHub Apps with manifest-based creation]"
            "(0007-per-role-github-apps.md)\n"
        )
        self.assertEqual(
            lint_adr_frontmatter.check_reference_titles(self.path, content, self.titles),
            [],
        )


if __name__ == "__main__":
    unittest.main()
