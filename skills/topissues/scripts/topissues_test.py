#!/usr/bin/env python3
"""Unit tests for topissues.py (no network)."""

from __future__ import annotations

import json
import os
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(__file__))

from topissues import (  # noqa: E402
    IssueRow,
    build_mine_pool,
    build_pr_links_by_issue,
    build_top_pool,
    format_markdown_table,
    issue_is_blocked,
    merge_rows,
    normalize_project_item,
    parse_open_blockers,
    parse_pr_links,
    parse_rice_score_from_fields,
    resolve_project_number,
)


def load_fixture(name: str):
    path = os.path.join(os.path.dirname(__file__), "testdata", name)
    with open(path, encoding="utf-8") as f:
        return json.load(f)


class TestResolveProjectNumber(unittest.TestCase):
    def test_invalid_env_falls_through_to_error(self):
        with (
            patch.dict(os.environ, {"FULLSEND_PROJECT_NUMBER": "not-a-number"}),
            patch("topissues.try_run_gh", return_value=None),
            self.assertRaises(SystemExit) as ctx,
        ):
            resolve_project_number("acme", None)
        self.assertEqual(ctx.exception.code, 1)

    def test_valid_env_is_used(self):
        with patch.dict(os.environ, {"FULLSEND_PROJECT_NUMBER": "42"}):
            self.assertEqual(resolve_project_number("acme", None), 42)


class TestParseRiceScoreFromFields(unittest.TestCase):
    def test_reads_rice_score_field_only(self):
        fields = load_fixture("field_values_sample.json")
        self.assertEqual(parse_rice_score_from_fields(fields), 42.5)

    def test_missing_score_field(self):
        fields = [{"field": {"name": "RICE Reach"}, "number": 1}]
        self.assertIsNone(parse_rice_score_from_fields(fields))

    def test_ignores_other_dimensions(self):
        fields = [
            {"field": {"name": "RICE Reach"}, "number": 10},
            {"field": {"name": "RICE Impact"}, "number": 5},
        ]
        self.assertIsNone(parse_rice_score_from_fields(fields))


class TestNormalizeProjectItem(unittest.TestCase):
    def test_open_issue_in_repo_with_score(self):
        item = {
            "fieldValues": {"nodes": load_fixture("field_values_sample.json")},
            "content": {
                "number": 100,
                "title": "Example",
                "state": "OPEN",
                "assignees": {"nodes": []},
                "blockedBy": {"nodes": []},
                "repository": {"owner": {"login": "acme"}, "name": "widget"},
            },
        }
        got = normalize_project_item(item, "acme/widget")
        self.assertIsNotNone(got)
        assert got is not None
        self.assertEqual(got["number"], 100)
        self.assertEqual(got["score"], 42.5)
        self.assertEqual(got["open_blockers"], [])

    def test_open_blockers_from_blocked_by(self):
        item = {
            "fieldValues": {"nodes": load_fixture("field_values_sample.json")},
            "content": {
                "number": 470,
                "title": "Blocked issue",
                "state": "OPEN",
                "assignees": {"nodes": []},
                "blockedBy": {
                    "nodes": [
                        {
                            "number": 788,
                            "state": "OPEN",
                            "repository": {"nameWithOwner": "fullsend-ai/fullsend"},
                        }
                    ]
                },
                "repository": {"owner": {"login": "fullsend-ai"}, "name": "fullsend"},
            },
        }
        got = normalize_project_item(item, "fullsend-ai/fullsend")
        assert got is not None
        self.assertEqual(got["open_blockers"], [{"repo": "fullsend-ai/fullsend", "number": 788}])

    def test_skips_closed_or_other_repo(self):
        item = {
            "fieldValues": {"nodes": load_fixture("field_values_sample.json")},
            "content": {
                "number": 1,
                "title": "X",
                "state": "CLOSED",
                "assignees": {"nodes": []},
                "repository": {"owner": {"login": "acme"}, "name": "widget"},
            },
        }
        self.assertIsNone(normalize_project_item(item, "acme/widget"))


class TestParseOpenBlockers(unittest.TestCase):
    def test_open_blocker(self):
        blocked_by = {
            "nodes": [
                {
                    "number": 788,
                    "state": "OPEN",
                    "repository": {"nameWithOwner": "fullsend-ai/fullsend"},
                }
            ]
        }
        self.assertEqual(
            parse_open_blockers(blocked_by),
            [{"repo": "fullsend-ai/fullsend", "number": 788}],
        )

    def test_ignores_closed_blockers(self):
        blocked_by = {
            "nodes": [
                {
                    "number": 787,
                    "state": "CLOSED",
                    "repository": {"nameWithOwner": "fullsend-ai/fullsend"},
                }
            ]
        }
        self.assertEqual(parse_open_blockers(blocked_by), [])


class TestIssueIsBlocked(unittest.TestCase):
    def test_open_blocker(self):
        self.assertTrue(issue_is_blocked({"open_blockers": [{"repo": "acme/x", "number": 1}]}))

    def test_not_blocked(self):
        self.assertFalse(issue_is_blocked({"open_blockers": []}))


class TestParsePrLinks(unittest.TestCase):
    def test_body_keywords_and_closing_refs(self):
        body = "This closes #42 and partial-fix #43"
        linked = parse_pr_links(body, [44])
        self.assertEqual(linked, {42, 43, 44})

    def test_closing_refs_only(self):
        self.assertEqual(parse_pr_links(None, [7, 8]), {7, 8})

    def test_ignores_bare_hash_mentions(self):
        body = "See also #99 for context"
        self.assertEqual(parse_pr_links(body, []), set())


class TestBuildPrLinksByIssue(unittest.TestCase):
    def test_fixture_pulls(self):
        pulls = load_fixture("pulls_sample.json")
        by_issue = build_pr_links_by_issue(pulls)
        self.assertEqual(by_issue[100], [10])
        self.assertEqual(by_issue[200], [10])
        self.assertNotIn(102, by_issue)


class TestPoolsAndMerge(unittest.TestCase):
    def setUp(self):
        self.issues = load_fixture("issues_sample.json")
        self.pr_links = build_pr_links_by_issue(load_fixture("pulls_sample.json"))

    def test_build_top_pool_excludes_assigned_scored_and_linked_pr(self):
        top = build_top_pool(self.issues, self.pr_links, top_n=10)
        numbers = [i["number"] for i in top]
        self.assertEqual(numbers, [102])
        self.assertNotIn(100, numbers)
        self.assertNotIn(101, numbers)
        self.assertNotIn(103, numbers)

    def test_build_top_pool_excludes_blocked(self):
        issues = [
            {
                "number": 1,
                "title": "blocked",
                "assignees": [],
                "open_blockers": [{"repo": "acme/widget", "number": 788}],
                "score": 99.0,
            },
            {"number": 2, "title": "ok", "assignees": [], "open_blockers": [], "score": 1.0},
        ]
        top = build_top_pool(issues, {}, top_n=10)
        self.assertEqual([i["number"] for i in top], [2])

    def test_build_top_pool_respects_top_n(self):
        issues = [
            {"number": 1, "title": "a", "assignees": [], "open_blockers": [], "score": 3.0},
            {"number": 2, "title": "b", "assignees": [], "open_blockers": [], "score": 9.0},
            {"number": 3, "title": "c", "assignees": [], "open_blockers": [], "score": 6.0},
        ]
        top = build_top_pool(issues, {}, top_n=2)
        self.assertEqual([i["number"] for i in top], [2, 3])

    def test_build_mine_pool(self):
        mine = build_mine_pool(self.issues, "alice")
        self.assertEqual([i["number"] for i in mine], [200, 202])

    def test_merge_rows_union_and_sort(self):
        top = build_top_pool(self.issues, self.pr_links, top_n=10)
        mine = build_mine_pool(self.issues, "alice")
        rows = merge_rows(top, mine, "alice", self.pr_links)
        self.assertEqual([r.number for r in rows], [200, 102, 202])
        self.assertTrue(rows[0].mine)
        self.assertFalse(rows[1].mine)
        self.assertEqual(rows[0].open_prs, (10,))


class TestFormatMarkdownTable(unittest.TestCase):
    def test_columns_and_pr_links(self):
        rows = [
            IssueRow(
                number=42,
                title="Fix | pipe",
                score=7.5,
                mine=True,
                open_prs=(10, 11),
            ),
            IssueRow(
                number=7,
                title="Backlog item",
                score=3.0,
                mine=False,
                open_prs=(),
            ),
        ]
        out = format_markdown_table(rows, "acme/widget", "alice", 3, 1, 1)
        self.assertIn("| Issue | Score | Mine | Open PRs | Title |", out)
        self.assertIn("project #3", out)
        self.assertIn(
            "[#42](https://github.com/acme/widget/issues/42) | 7.5 | Yes | "
            "[#10](https://github.com/acme/widget/pull/10), "
            "[#11](https://github.com/acme/widget/pull/11) | Fix \\| pipe |",
            out,
        )


if __name__ == "__main__":
    unittest.main()
