#!/usr/bin/env python3
"""Unit tests for adr_corner.py (no network)."""

from __future__ import annotations

import os
import subprocess
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(__file__))

from adr_corner import (  # noqa: E402
    adr_summary,
    discussion_points,
    escape_markdown_cell,
    fetch_paginated_nodes,
    friendly_datetime,
    graphql_var_flags,
    infer_adr_authors,
    is_adr_path,
    run_gh,
    should_include_adr_pr,
    sort_rows_oldest_first,
    summary_files_for_pr,
)


class TestAdrPaths(unittest.TestCase):
    def test_conventional_and_generic_adr_paths(self):
        self.assertTrue(is_adr_path("docs/ADRs/0125-gitlab.md"))
        self.assertTrue(is_adr_path("architecture/adr/decision.md"))
        self.assertTrue(is_adr_path("ADR-001.md"))
        self.assertFalse(is_adr_path("commands/adr-corner.md"))
        self.assertFalse(is_adr_path("docs/guides/architecture.md"))
        self.assertFalse(is_adr_path("docs/ADRs/README.txt"))
        self.assertFalse(is_adr_path("docs/ADRs/README.md"))


class TestAdrSummary(unittest.TestCase):
    def test_prefers_decision_section_and_strips_markdown(self):
        text = (
            "# 12. Example\n\n## Context\nOld system.\n\n"
            "## Decision\nUse [one](https://example.test) **shared** path.\n"
        )
        self.assertEqual(
            adr_summary(text, "docs/ADRs/0012-example.md"),
            "0012-example: Use one shared path.",
        )

    def test_missing_content_is_explicit(self):
        self.assertIn("content unavailable", adr_summary(None, "docs/ADRs/0042.md"))


class TestAttribution(unittest.TestCase):
    def test_bot_pr_uses_human_commit_then_fallbacks(self):
        pr = {
            "author": {"login": "fullsend-ai-coder[bot]", "__typename": "Bot"},
            "commits": {"nodes": [{"commit": {"author": {"user": {"login": "alice"}}}}]},
            "assignees": {"nodes": [{"login": "bob", "__typename": "User"}]},
            "closingIssuesReferences": {
                "nodes": [
                    {
                        "assignees": {"nodes": [{"login": "carol", "__typename": "User"}]},
                        "author": {"login": "dave"},
                    }
                ]
            },
        }
        self.assertEqual(
            infer_adr_authors(pr)[:3],
            [
                {"login": "alice", "source": "commit author"},
                {"login": "bob", "source": "PR assignee"},
                {"login": "carol", "source": "linked issue assignee"},
            ],
        )

    def test_human_pr_author_wins(self):
        pr = {"author": {"login": "alice", "__typename": "User"}, "commits": {"nodes": []}}
        self.assertEqual(infer_adr_authors(pr)[0], {"login": "alice", "source": "PR author"})

    def test_unsuffixed_graphql_bot_is_not_attributed_to_a_human(self):
        pr = {
            "author": {"login": "fullsend-ai-coder", "__typename": "Bot"},
            "commits": {"nodes": []},
        }
        self.assertEqual(infer_adr_authors(pr), [])


class TestInclusion(unittest.TestCase):
    def test_unattributed_modification_only_pr_is_excluded(self):
        files = [{"path": "docs/ADRs/0054-auth.md", "changeType": "MODIFIED"}]
        self.assertFalse(should_include_adr_pr(files, []))

    def test_unattributed_new_adr_is_included(self):
        files = [{"path": "docs/ADRs/0130-new.md", "changeType": "ADDED"}]
        self.assertTrue(should_include_adr_pr(files, []))

    def test_human_attributed_modification_only_pr_is_included(self):
        files = [{"path": "docs/ADRs/0089-existing.md", "changeType": "MODIFIED"}]
        self.assertTrue(should_include_adr_pr(files, [{"login": "alice", "source": "PR author"}]))

    def test_new_adr_files_take_precedence_over_updates(self):
        files = [
            {"path": "docs/ADRs/0130-new.md", "changeType": "ADDED"},
            {"path": "docs/ADRs/0054-auth.md", "changeType": "MODIFIED"},
        ]
        self.assertEqual(summary_files_for_pr(files), [files[0]])

    def test_updates_are_summary_fallback_when_no_adr_is_added(self):
        files = [
            {"path": "docs/ADRs/0054-auth.md", "changeType": "MODIFIED"},
            {"path": "docs/ADRs/0075-profiles.md", "changeType": "MODIFIED"},
        ]
        self.assertEqual(summary_files_for_pr(files), files)


class TestDiscussion(unittest.TestCase):
    def test_prioritizes_human_contention_and_ignores_bots(self):
        pr = {
            "comments": {
                "nodes": [
                    {
                        "author": {"login": "bot[bot]", "__typename": "Bot"},
                        "body": "automated",
                        "createdAt": "2026-01-01T00:00:00Z",
                    },
                    {
                        "author": {"login": "alice"},
                        "body": "Why should this be global?",
                        "createdAt": "2026-01-02T00:00:00Z",
                    },
                ]
            },
            "reviews": {"nodes": []},
            "reviewThreads": {"nodes": []},
        }
        points = discussion_points(pr)
        self.assertEqual(len(points), 1)
        self.assertIn("Why should this be global?", points[0])

    def test_preserves_path_like_leading_slashes(self):
        pr = {
            "comments": {
                "nodes": [
                    {
                        "author": {"login": "alice"},
                        "body": "/docs/ADRs/0125.md should use the new heading",
                        "createdAt": "2026-01-02T00:00:00Z",
                    }
                ]
            },
            "reviews": {"nodes": []},
            "reviewThreads": {"nodes": []},
        }
        self.assertEqual(len(discussion_points(pr)), 1)

    def test_contention_is_prioritized_over_newer_routine_comments(self):
        comments = [
            {
                "author": {"login": f"alice-{index}"},
                "body": f"Routine update {index}",
                "createdAt": f"2026-01-0{index + 1}T00:00:00Z",
            }
            for index in range(4)
        ]
        comments.append(
            {
                "author": {"login": "reviewer"},
                "body": "Why should this remain a separate decision?",
                "createdAt": "2026-01-01T00:00:00Z",
            }
        )
        pr = {
            "comments": {"nodes": comments},
            "reviews": {"nodes": []},
            "reviewThreads": {"nodes": []},
        }
        points = discussion_points(pr)
        self.assertIn("Why should this remain", points[0])

    def test_ignores_command_only_comments(self):
        pr = {
            "comments": {
                "nodes": [
                    {
                        "author": {"login": "alice"},
                        "body": "/fs-review",
                        "createdAt": "2026-01-02T00:00:00Z",
                    }
                ]
            },
            "reviews": {"nodes": []},
            "reviewThreads": {"nodes": []},
        }
        self.assertEqual(discussion_points(pr), [])

    def test_keeps_prose_after_a_slash_command(self):
        pr = {
            "comments": {
                "nodes": [
                    {
                        "author": {"login": "alice"},
                        "body": "/fs-review Why should this remain a separate decision?",
                        "createdAt": "2026-01-02T00:00:00Z",
                    }
                ]
            },
            "reviews": {"nodes": []},
            "reviewThreads": {"nodes": []},
        }
        points = discussion_points(pr)
        self.assertEqual(len(points), 1)
        self.assertIn("Why should this remain", points[0])

    def test_unsuffixed_graphql_bots_are_ignored(self):
        pr = {
            "comments": {
                "nodes": [
                    {
                        "author": {"login": "fullsend-ai-review", "__typename": "Bot"},
                        "body": "Why should this remain?",
                        "createdAt": "2026-01-02T00:00:00Z",
                    }
                ]
            },
            "reviews": {"nodes": []},
            "reviewThreads": {"nodes": []},
        }
        self.assertEqual(discussion_points(pr), [])


class TestDates(unittest.TestCase):
    def test_human_friendly_utc(self):
        self.assertEqual(friendly_datetime("2026-09-30T12:34:56Z"), "30 Sep 2026, 12:34 UTC")

    def test_human_friendly_utc_is_portable_for_single_digit_days(self):
        self.assertEqual(friendly_datetime("2026-09-01T12:34:56Z"), "1 Sep 2026, 12:34 UTC")


class TestGraphqlFlags(unittest.TestCase):
    def test_integer_uses_typed_gh_flag(self):
        self.assertEqual(graphql_var_flags({"number": 12, "cursor": None}), ["-F", "number=12"])


class TestPagination(unittest.TestCase):
    @patch("adr_corner.gh_graphql")
    def test_fetch_paginated_nodes_follows_cursors(self, graphql):
        graphql.side_effect = [
            {
                "repository": {
                    "pullRequest": {
                        "reviews": {
                            "pageInfo": {"hasNextPage": True, "endCursor": "cursor-1"},
                            "nodes": [{"body": "first"}],
                        }
                    }
                }
            },
            {
                "repository": {
                    "pullRequest": {
                        "reviews": {
                            "pageInfo": {"hasNextPage": False, "endCursor": None},
                            "nodes": [{"body": "second"}],
                        }
                    }
                }
            },
        ]
        nodes = fetch_paginated_nodes(
            "query",
            {"owner": "fullsend-ai", "name": "fullsend", "number": 1},
            ("repository", "pullRequest", "reviews"),
        )
        self.assertEqual([node["body"] for node in nodes], ["first", "second"])
        self.assertEqual(graphql.call_args_list[1].args[1]["cursor"], "cursor-1")


class TestMarkdown(unittest.TestCase):
    def test_escapes_backslashes_before_pipes(self):
        self.assertEqual(escape_markdown_cell(r"left \| right"), r"left \\\| right")


class TestOrdering(unittest.TestCase):
    def test_rows_are_oldest_first_and_unknown_dates_last(self):
        rows = [
            {"number": 3, "created_at": None},
            {"number": 1, "created_at": "2026-09-30T12:00:00Z"},
            {"number": 2, "created_at": "2026-09-01T12:00:00Z"},
        ]
        self.assertEqual([row["number"] for row in sort_rows_oldest_first(rows)], [2, 1, 3])


class TestNetworkErrors(unittest.TestCase):
    @patch("adr_corner.subprocess.run")
    def test_connection_error_exits_with_actionable_message(self, run):
        run.side_effect = subprocess.CalledProcessError(
            1, ["gh"], stderr="error connecting to api.github.com"
        )
        with self.assertRaises(SystemExit) as raised:
            run_gh(["api", "rate_limit"])
        self.assertEqual(raised.exception.code, 3)


if __name__ == "__main__":
    unittest.main()
