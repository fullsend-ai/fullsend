#!/usr/bin/env python3
"""Unit tests for nextwork.py (no network)."""

from __future__ import annotations

import json
import os
import sys
import unittest
from datetime import UTC, datetime
from unittest.mock import Mock, patch

sys.path.insert(0, os.path.dirname(__file__))

from nextwork import (  # noqa: E402
    ASSIGN_SELF,
    DECISION_STATUSES,
    REMOVE_BLOCKED_LABEL,
    GhFetcher,
    RefError,
    agent_terminal_succeeded,
    apply_trivial_actions,
    build_pr_links_by_issue,
    build_queue,
    classify_issue,
    classify_item,
    classify_pr,
    comment_command,
    format_json_output,
    format_markdown_output,
    graphql_var_flags,
    hours_since,
    is_stale,
    latest_agent_status,
    latest_completed_triage,
    link_blocker,
    maybe_check_merge_queue,
    normalize_item,
    parse_args,
    parse_inflight_agent,
    parse_link_blocker_spec,
    parse_open_blockers,
    parse_pr_links,
    parse_ref,
    parse_take_over_specs,
    resolve_repo,
    resolve_threads,
    seed_from_assigned,
    take_over,
)

NOW = datetime(2024, 1, 10, tzinfo=UTC)  # 8 days after 2024-01-02T00:00:00Z


class TestGraphqlVarFlags(unittest.TestCase):
    def test_int_uses_typed_flag(self):
        self.assertEqual(
            graphql_var_flags({"owner": "acme", "number": 5479}),
            ["-f", "owner=acme", "-F", "number=5479"],
        )

    def test_bool_uses_typed_flag(self):
        self.assertEqual(
            graphql_var_flags({"draft": True}),
            ["-F", "draft=true"],
        )

    def test_skips_none(self):
        self.assertEqual(graphql_var_flags({"cursor": None, "owner": "acme"}), ["-f", "owner=acme"])


class TestCommentCommand(unittest.TestCase):
    def test_first_token_of_first_line(self):
        self.assertEqual(comment_command("/fs-code please"), "/fs-code")
        self.assertEqual(comment_command("  /fs-triage\r\nmore"), "/fs-triage")
        self.assertEqual(comment_command("not a command"), "not")
        self.assertEqual(comment_command(""), "")
        self.assertEqual(comment_command("line1\n/fs-code"), "line1")


def load_fixture(name: str):
    path = os.path.join(os.path.dirname(__file__), "testdata", name)
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def make_issue(**overrides):
    item = {
        "kind": "issue",
        "repo": "acme/widget",
        "number": 1,
        "title": "An issue",
        "url": "https://github.com/acme/widget/issues/1",
        "state": "OPEN",
        "author": "alice",
        "assignees": [],
        "labels": [],
        "created_at": "2024-01-09T00:00:00Z",
        "updated_at": "2024-01-09T00:00:00Z",
        "body": "",
        "comments": [],
        "blockers": [],
        "linked_prs": [],
        "sub_issues_total": 0,
        "sub_issues_completed": 0,
        "open_sub_issues": [],
    }
    item.update(overrides)
    return item


def make_pr(**overrides):
    item = {
        "kind": "pull",
        "repo": "acme/widget",
        "number": 99,
        "title": "A pull request",
        "url": "https://github.com/acme/widget/pull/99",
        "state": "OPEN",
        "author": "alice",
        "assignees": [],
        "labels": [],
        "created_at": "2024-01-09T00:00:00Z",
        "updated_at": "2024-01-09T00:00:00Z",
        "body": "",
        "comments": [],
        "blockers": [],
        "linked_prs": [],
        "is_draft": False,
        "base_ref_name": "main",
        "review_decision": None,
        "mergeable": "MERGEABLE",
        "merge_state_status": "CLEAN",
        "unresolved_threads": [],
        "unresolved_review_threads": 0,
        "checks_state": "SUCCESS",
        "head_committed_at": None,
        "latest_approved_review_at": None,
        "in_merge_queue": False,
    }
    item.update(overrides)
    if "unresolved_threads" in overrides and "unresolved_review_threads" not in overrides:
        item["unresolved_review_threads"] = len(item["unresolved_threads"])
    elif (
        "unresolved_review_threads" in overrides
        and "unresolved_threads" not in overrides
        and item["unresolved_review_threads"]
        and not item["unresolved_threads"]
    ):
        # Legacy count-only tests: invent human-authored threads so they stay decisions.
        n = int(item["unresolved_review_threads"])
        item["unresolved_threads"] = [
            {"author": "alice", "created_at": item["updated_at"]} for _ in range(n)
        ]
    return item


def agent_comment(body, created_at, *, author=None):
    """Build a comment dict with a trusted fullsend bot author for marker tests."""
    if author is None:
        lower = body.lower()
        if "triage" in lower or "fullsend:triage-agent" in lower:
            author = "fullsend-ai-triage[bot]"
        elif "fix" in lower or "code" in lower:
            author = "fullsend-ai-coder[bot]"
        else:
            author = "fullsend-ai-review[bot]"
    return {"author": author, "body": body, "created_at": created_at}


def trusted_fs_comment(body, created_at, *, author="alice", association="MEMBER"):
    """Human /fs-* launch comment trusted for launch-signal classification."""
    return {
        "author": author,
        "author_association": association,
        "body": body,
        "created_at": created_at,
    }


class TestParseInflightAgent(unittest.TestCase):
    def test_no_comments(self):
        self.assertIsNone(parse_inflight_agent([]))

    def test_started_only_is_inflight(self):
        comments = [
            agent_comment(
                "<!-- fullsend:agent-status:run-1 -->\n🤖 Review · Started 1:00 PM UTC",
                "2024-01-09T13:00:00Z",
            )
        ]
        self.assertEqual(parse_inflight_agent(comments), "waiting_review")

    def test_terminal_is_not_inflight(self):
        comments = [
            agent_comment(
                (
                    "<!-- fullsend:agent-status:run-1 -->\n"
                    "<!-- fullsend:status:terminal -->\n"
                    "🤖 Finished Review · ✅ Success · Started 1:00 PM UTC · Completed 1:10 PM UTC"
                ),
                "2024-01-09T13:10:00Z",
            )
        ]
        self.assertIsNone(parse_inflight_agent(comments))

    def test_latest_terminal_wins_over_older_started(self):
        comments = [
            agent_comment(
                "<!-- fullsend:agent-status:run-1 -->\n🤖 Review · Started 1:00 PM UTC",
                "2024-01-09T13:00:00Z",
            ),
            agent_comment(
                (
                    "<!-- fullsend:agent-status:run-1 -->\n"
                    "<!-- fullsend:status:terminal -->\n"
                    "🤖 Finished Review · ✅ Success"
                ),
                "2024-01-09T13:10:00Z",
            ),
        ]
        self.assertIsNone(parse_inflight_agent(comments))

    def test_latest_started_wins_over_older_terminal(self):
        comments = [
            agent_comment(
                (
                    "<!-- fullsend:agent-status:run-1 -->\n"
                    "<!-- fullsend:status:terminal -->\n"
                    "🤖 Finished Fix · ✅ Success"
                ),
                "2024-01-09T12:00:00Z",
            ),
            agent_comment(
                "<!-- fullsend:agent-status:run-2 -->\n🤖 Review · Started 1:00 PM UTC",
                "2024-01-09T13:00:00Z",
            ),
        ]
        self.assertEqual(parse_inflight_agent(comments), "waiting_review")

    def test_role_mapping(self):
        cases = [
            ("🤖 Fix · Started", "waiting_fix"),
            ("🤖 Code · Started", "waiting_code"),
            ("🤖 Triage · Started", "waiting_triage"),
            ("🤖 Working · Started", "waiting_agent"),
        ]
        for body_role, expected in cases:
            with self.subTest(body_role=body_role):
                comments = [
                    agent_comment(
                        f"<!-- fullsend:agent-status:x -->\n{body_role}",
                        "2024-01-09T13:00:00Z",
                    )
                ]
                self.assertEqual(parse_inflight_agent(comments), expected)

    def test_issue_inflight_code_before_ready_to_code(self):
        item = make_issue(
            labels=["ready-to-code"],
            assignees=["alice"],
            updated_at="2024-01-09T23:00:00Z",
            comments=[
                agent_comment(
                    "<!-- fullsend:agent-status:r1 -->\n🤖 Code · Started 1:00 PM UTC",
                    "2024-01-09T23:00:00Z",
                )
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_code")
        self.assertTrue(result.eliminated)


class TestParseRef(unittest.TestCase):
    def test_repo_hash_number(self):
        self.assertEqual(parse_ref("acme/widget#42"), ("acme/widget", 42))

    def test_bare_hash_with_default_repo(self):
        self.assertEqual(parse_ref("#42", "acme/widget"), ("acme/widget", 42))

    def test_bare_number_with_default_repo(self):
        self.assertEqual(parse_ref("42", "acme/widget"), ("acme/widget", 42))

    def test_bare_number_without_default_repo_raises(self):
        with self.assertRaises(RefError):
            parse_ref("42", None)

    def test_issue_url(self):
        self.assertEqual(parse_ref("https://github.com/acme/widget/issues/42"), ("acme/widget", 42))

    def test_pull_url_with_trailing_slash(self):
        self.assertEqual(parse_ref("https://github.com/acme/widget/pull/42/"), ("acme/widget", 42))

    def test_pull_url_with_query_suffix(self):
        self.assertEqual(
            parse_ref("https://github.com/acme/widget/pull/42/files"), ("acme/widget", 42)
        )

    def test_garbage_raises(self):
        with self.assertRaises(RefError):
            parse_ref("not-a-ref")


class TestTimeHelpers(unittest.TestCase):
    def test_hours_since(self):
        now = datetime(2024, 1, 2, 6, tzinfo=UTC)
        self.assertAlmostEqual(hours_since("2024-01-02T00:00:00Z", now), 6.0)

    def test_is_stale_true(self):
        now = datetime(2024, 1, 2, 6, tzinfo=UTC)
        self.assertTrue(is_stale("2024-01-02T00:00:00Z", 6, now))

    def test_is_stale_false(self):
        now = datetime(2024, 1, 2, 5, tzinfo=UTC)
        self.assertFalse(is_stale("2024-01-02T00:00:00Z", 6, now))

    def test_empty_created_at_is_not_stale(self):
        now = datetime(2024, 1, 2, 6, tzinfo=UTC)
        self.assertEqual(hours_since("", now), 0.0)
        self.assertEqual(hours_since(None, now), 0.0)
        self.assertFalse(is_stale("", 6, now))
        self.assertFalse(is_stale(None, 6, now))


class TestParsePrLinks(unittest.TestCase):
    def test_body_keywords_and_closing_refs(self):
        body = "This closes #42 and partial-fix #43"
        self.assertEqual(parse_pr_links(body, [44]), {42, 43, 44})

    def test_alternate_verb_forms(self):
        self.assertEqual(parse_pr_links("Fix #42", []), {42})
        self.assertEqual(parse_pr_links("Closed #43", []), {43})
        self.assertEqual(parse_pr_links("Resolved #44", []), {44})

    def test_ignores_bare_hash_mentions(self):
        self.assertEqual(parse_pr_links("See also #99 for context", []), set())


class TestBuildPrLinksByIssue(unittest.TestCase):
    def test_fixture_pulls(self):
        pulls = load_fixture("pulls_for_linking_sample.json")
        by_issue = build_pr_links_by_issue(pulls)
        self.assertEqual(by_issue[100], [10])
        self.assertEqual(by_issue[200], [10])
        self.assertEqual(by_issue[999], [11])  # "fixes #999" keyword match, no closing ref


class TestParseOpenBlockers(unittest.TestCase):
    def test_open_blocker_kept_closed_dropped(self):
        blocked_by = {
            "nodes": [
                {"number": 7, "state": "OPEN", "repository": {"nameWithOwner": "acme/widget"}},
                {"number": 8, "state": "CLOSED", "repository": {"nameWithOwner": "acme/widget"}},
            ]
        }
        self.assertEqual(parse_open_blockers(blocked_by), [{"repo": "acme/widget", "number": 7}])

    def test_empty(self):
        self.assertEqual(parse_open_blockers(None), [])


class TestNormalizeItem(unittest.TestCase):
    def test_issue_node(self):
        node = load_fixture("issue_node_sample.json")
        item = normalize_item("acme/widget", node)
        self.assertEqual(item["kind"], "issue")
        self.assertEqual(item["number"], 42)
        self.assertEqual(item["assignees"], ["alice"])
        self.assertEqual(item["labels"], ["ready-to-code"])
        self.assertEqual(item["blockers"], [{"repo": "acme/widget", "number": 7}])
        self.assertEqual(len(item["comments"]), 1)
        self.assertEqual(item["sub_issues_total"], 2)
        self.assertEqual(item["sub_issues_completed"], 1)
        self.assertEqual(
            item["open_sub_issues"],
            [{"repo": "acme/widget", "number": 50, "title": "Child open"}],
        )

    def test_cross_repo_sub_issue_keeps_child_repo(self):
        node = load_fixture("issue_node_sample.json")
        node["subIssues"]["nodes"][0]["repository"] = {"nameWithOwner": "acme/gadget"}
        item = normalize_item("acme/widget", node, quiet=True)
        self.assertEqual(
            item["open_sub_issues"],
            [{"repo": "acme/gadget", "number": 50, "title": "Child open"}],
        )

    def test_issue_node_without_sub_issues_fields(self):
        node = load_fixture("issue_node_sample.json")
        del node["subIssuesSummary"]
        del node["subIssues"]
        item = normalize_item("acme/widget", node)
        self.assertEqual(item["sub_issues_total"], 0)
        self.assertEqual(item["sub_issues_completed"], 0)
        self.assertEqual(item["open_sub_issues"], [])

    def test_pull_node(self):
        node = load_fixture("pr_node_sample.json")
        item = normalize_item("acme/widget", node)
        self.assertEqual(item["kind"], "pull")
        self.assertEqual(item["review_decision"], "REVIEW_REQUIRED")
        self.assertEqual(item["mergeable"], "MERGEABLE")
        self.assertEqual(item["merge_state_status"], "CLEAN")
        self.assertEqual(item["unresolved_review_threads"], 1)
        self.assertEqual(len(item["unresolved_threads"]), 1)
        thread = item["unresolved_threads"][0]
        self.assertEqual(thread["author"], "fullsend-ai-review[bot]")
        self.assertEqual(thread["authors"], ["fullsend-ai-review[bot]"])
        self.assertEqual(thread["created_at"], "2024-01-02T01:00:00Z")
        self.assertTrue(thread["bot_only"])
        self.assertEqual(thread["id"], "PRRT_unresolved_1")
        self.assertEqual(thread["path"], "internal/handler.go")
        self.assertEqual(thread["line"], 42)
        self.assertEqual(thread["body"], "Error return value is not checked.")
        self.assertEqual(item["checks_state"], "SUCCESS")
        self.assertEqual(item["head_committed_at"], "2024-01-02T00:30:00Z")
        self.assertIsNone(item["latest_approved_review_at"])
        self.assertFalse(item["is_draft"])
        self.assertFalse(item["in_merge_queue"])
        self.assertEqual(item["base_ref_name"], "main")
        self.assertEqual(item["blockers"], [])

    def test_pull_node_bot_thread_with_human_reply(self):
        node = load_fixture("pr_node_sample.json")
        node["reviewThreads"]["nodes"][1]["comments"]["nodes"].append(
            {
                "author": {"login": "carol"},
                "body": "I disagree with this finding.",
                "createdAt": "2024-01-02T02:00:00Z",
            }
        )
        item = normalize_item("acme/widget", node, quiet=True)
        self.assertEqual(item["unresolved_review_threads"], 1)
        thread = item["unresolved_threads"][0]
        self.assertFalse(thread["bot_only"])
        self.assertEqual(thread["authors"], ["fullsend-ai-review[bot]", "carol"])
        self.assertEqual(thread["created_at"], "2024-01-02T01:00:00Z")
        # id, path, line preserved even with human reply
        self.assertEqual(thread["id"], "PRRT_unresolved_1")
        self.assertEqual(thread["path"], "internal/handler.go")
        self.assertEqual(thread["line"], 42)


class TestClassifyIssue(unittest.TestCase):
    def test_duplicate_drops(self):
        item = make_issue(labels=["duplicate"])
        self.assertIsNone(classify_issue(item, "alice", 6, NOW))

    def test_blocked_by_structured_blocker(self):
        item = make_issue(blockers=[{"repo": "acme/widget", "number": 7}])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "blocked_by")
        self.assertTrue(result.eliminated)
        self.assertEqual(result.blockers, [{"repo": "acme/widget", "number": 7}])
        self.assertNotIn(REMOVE_BLOCKED_LABEL, result.suggested_actions)

    def test_blocked_label_only_is_ignored(self):
        # Orphaned blocked label must not eliminate; create is triage launch →
        # stale needs_triage (default make_issue created_at is older than stale-hours).
        item = make_issue(labels=["blocked"])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_triage")
        self.assertFalse(result.eliminated)
        self.assertIn(REMOVE_BLOCKED_LABEL, result.suggested_actions)
        self.assertIn("comment:/fs-triage", result.suggested_actions)

    def test_blocked_label_with_triaged_suggests_remove(self):
        item = make_issue(labels=["blocked", "triaged"], assignees=["alice"])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertFalse(result.eliminated)
        self.assertIn(REMOVE_BLOCKED_LABEL, result.suggested_actions)

    def test_orphaned_blocked_not_suggested_for_assigned_elsewhere(self):
        item = make_issue(labels=["blocked"], assignees=["bob"])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "assigned_elsewhere")
        self.assertTrue(result.eliminated)
        self.assertNotIn(REMOVE_BLOCKED_LABEL, result.suggested_actions)

    def test_assigned_elsewhere(self):
        item = make_issue(assignees=["bob"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "assigned_elsewhere")
        self.assertTrue(result.eliminated)

    def test_waiting_linked_pr(self):
        item = make_issue(linked_prs=[10, 11])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_linked_pr")
        self.assertEqual(result.linked_prs, [10, 11])

    def test_waiting_sub_issues(self):
        open_subs = [
            {"repo": "acme/widget", "number": 2, "title": "Child A"},
            {"repo": "acme/widget", "number": 3, "title": "Child B"},
        ]
        item = make_issue(
            labels=["triaged"],
            open_sub_issues=open_subs,
            sub_issues_total=3,
            sub_issues_completed=1,
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_sub_issues")
        self.assertTrue(result.eliminated)
        self.assertEqual(result.open_sub_issues, open_subs)
        self.assertIn("#2", result.reason)
        self.assertIn("#3", result.reason)

    def test_waiting_sub_issues_before_linked_pr(self):
        item = make_issue(
            linked_prs=[99],
            open_sub_issues=[{"repo": "acme/widget", "number": 2, "title": "Child"}],
            sub_issues_total=1,
            sub_issues_completed=0,
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_sub_issues")

    def test_close_or_plan_when_all_sub_issues_closed(self):
        item = make_issue(
            labels=["triaged"],
            open_sub_issues=[],
            sub_issues_total=2,
            sub_issues_completed=2,
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "close_or_plan")
        self.assertFalse(result.eliminated)
        self.assertTrue(any("close this issue" in a for a in result.suggested_actions))
        self.assertIn("close_or_plan", DECISION_STATUSES)

    def test_waiting_sub_issues_when_summary_shows_incomplete(self):
        # Truncated first page has no OPEN nodes, but summary proves work remains.
        item = make_issue(
            labels=["triaged"],
            open_sub_issues=[],
            sub_issues_total=60,
            sub_issues_completed=50,
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_sub_issues")
        self.assertTrue(result.eliminated)
        self.assertIn("50/60", result.reason)

    def test_no_sub_issues_keeps_promote_code(self):
        item = make_issue(labels=["triaged"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")

    def test_needs_info_self(self):
        item = make_issue(author="alice", labels=["needs-info"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_info_self")
        self.assertFalse(result.eliminated)

    def test_waiting_info_other(self):
        item = make_issue(author="bob", labels=["needs-info"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_info_other")
        self.assertTrue(result.eliminated)

    def test_waiting_triage_no_labels_recent(self):
        item = make_issue(created_at="2024-01-09T23:00:00Z")  # 1 hour before NOW
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_triage")
        self.assertTrue(result.eliminated)

    def test_needs_triage_no_labels_stale_from_created_at(self):
        # Issue creation is the initial triage launch; past stale-hours → needs_triage.
        item = make_issue(created_at="2024-01-01T00:00:00Z")
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_triage")
        self.assertFalse(result.eliminated)
        self.assertIn("comment:/fs-triage", result.suggested_actions)

    def test_create_triage_launch_cleared_by_completed_triage(self):
        # Completed triage after create clears the create-as-launch wait.
        item = make_issue(
            created_at="2024-01-01T00:00:00Z",
            assignees=[],
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:run-1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-09T12:00:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_assign")
        self.assertFalse(result.eliminated)

    def test_ready_to_code_not_trapped_by_create_as_triage_launch(self):
        # Control label ready-to-code skips created_at triage fallback.
        item = make_issue(
            labels=["ready-to-code"],
            created_at="2024-01-01T00:00:00Z",
            updated_at="2024-01-09T23:00:00Z",
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_code")
        self.assertTrue(result.eliminated)

    def test_waiting_triage_label_takes_priority_over_other_control_labels(self):
        # ready-for-triage forces the triage-wait branch even alongside another control label,
        # as long as the launch wait itself isn't stale (clock from updated_at).
        item = make_issue(
            labels=["ready-for-triage", "triaged"],
            created_at="2024-01-09T23:00:00Z",
            updated_at="2024-01-09T23:00:00Z",
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_triage")

    def test_completed_triage_clears_recent_fs_triage_launch(self):
        # A fresh /fs-triage must not stay waiting_triage after a successful
        # terminal Triage status.
        # after a successful terminal Triage status that follows it.
        item = make_issue(
            labels=["triaged", "feature"],
            assignees=[],
            comments=[
                trusted_fs_comment("/fs-triage", "2024-01-09T22:00:00Z"),
                {
                    "author": "fullsend-ai-triage[bot]",
                    "body": (
                        "<!-- fullsend:agent-status:run-1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "created_at": "2024-01-09T22:05:00Z",
                },
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertFalse(result.eliminated)

    def test_newer_fs_triage_after_terminal_still_waits(self):
        item = make_issue(
            labels=["triaged"],
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:run-1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-09T21:00:00Z",
                ),
                trusted_fs_comment("/fs-triage", "2024-01-09T23:00:00Z"),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_triage")
        self.assertTrue(result.eliminated)

    def test_completed_code_clears_recent_fs_code_launch(self):
        item = make_issue(
            labels=["triaged"],
            comments=[
                trusted_fs_comment("/fs-code", "2024-01-09T22:00:00Z"),
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:run-1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Code · ✅ Success"
                    ),
                    "2024-01-09T22:30:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertFalse(result.eliminated)

    def test_needs_triage_label_stale(self):
        item = make_issue(
            labels=["ready-for-triage"],
            created_at="2024-01-01T00:00:00Z",
            updated_at="2024-01-01T00:00:00Z",
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_triage")
        self.assertFalse(result.eliminated)

    def test_waiting_code(self):
        item = make_issue(labels=["ready-to-code"], updated_at="2024-01-09T23:00:00Z")
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_code")
        self.assertTrue(result.eliminated)

    def test_trigger_code_stale(self):
        item = make_issue(labels=["ready-to-code"], updated_at="2024-01-01T00:00:00Z")
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_code")
        self.assertFalse(result.eliminated)
        self.assertIn("comment:/fs-code", result.suggested_actions)

    def test_promote_code(self):
        item = make_issue(labels=["triaged"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertFalse(result.eliminated)

    def test_needs_assign_unassigned_with_control_label(self):
        item = make_issue(labels=["triaged"], assignees=[])
        result = classify_issue(item, "alice", 6, NOW)
        # triaged is checked before the unassigned fallback
        self.assertEqual(result.status, "promote_code")

    def test_needs_assign_no_signal(self):
        item = make_issue(labels=["question"], assignees=[])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_assign")
        self.assertIn(ASSIGN_SELF, result.suggested_actions)

    def test_human_work_assigned_no_signal(self):
        item = make_issue(labels=["question"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "human_work")
        self.assertFalse(result.eliminated)

    def test_classify_item_prepending_assign_on_needs_triage(self):
        item = make_issue(
            labels=["ready-for-triage"],
            assignees=[],
            updated_at="2024-01-01T00:00:00Z",
        )
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_triage")
        self.assertEqual(result.suggested_actions[0], ASSIGN_SELF)
        self.assertIn("comment:/fs-triage", result.suggested_actions)

    def test_classify_item_prepending_assign_on_trigger_code(self):
        item = make_issue(labels=["ready-to-code"], assignees=[], updated_at="2024-01-01T00:00:00Z")
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_code")
        self.assertEqual(result.suggested_actions[0], ASSIGN_SELF)
        self.assertIn("comment:/fs-code", result.suggested_actions)

    def test_classify_item_prepending_assign_on_promote_code(self):
        item = make_issue(labels=["triaged"], assignees=[])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertEqual(result.suggested_actions[0], ASSIGN_SELF)

    def test_classify_item_no_assign_on_waiting_triage(self):
        # Fresh create-as-launch wait (within stale-hours) stays eliminated.
        item = make_issue(assignees=[], created_at="2024-01-09T23:00:00Z")
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_triage")
        self.assertTrue(result.eliminated)
        self.assertNotIn(ASSIGN_SELF, result.suggested_actions)

    def test_classify_item_no_assign_on_assigned_elsewhere(self):
        item = make_issue(assignees=["bob"], labels=["question"])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "assigned_elsewhere")
        self.assertNotIn(ASSIGN_SELF, result.suggested_actions)

    def test_classify_item_no_assign_when_already_assigned(self):
        item = make_issue(labels=["triaged"], assignees=["alice"])
        result = classify_item(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertNotIn(ASSIGN_SELF, result.suggested_actions)

    def test_stale_completed_triage_overrides_promote_code(self):
        item = make_issue(
            labels=["triaged"],
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-01T00:00:00Z",
                ),
                {
                    "body": "Please reconsider scope",
                    "created_at": "2024-01-05T00:00:00Z",
                },
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_triage")

    def test_fresh_post_triage_comment_does_not_stale_within_grace(self):
        # Comment younger than --stale-hours must not flip a fresh triage.
        item = make_issue(
            labels=["triaged"],
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-09T20:00:00Z",
                ),
                {
                    "author": "bob",
                    "body": "Thanks!",
                    "created_at": "2024-01-09T22:00:00Z",
                },
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")

    def test_untrusted_fs_command_is_ignored_as_launch_signal(self):
        item = make_issue(
            labels=["ready-to-code"],
            updated_at="2024-01-01T00:00:00Z",
            comments=[
                {
                    "author": "drive-by",
                    "author_association": "NONE",
                    "body": "/fs-code",
                    "created_at": "2024-01-09T23:00:00Z",
                },
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_code")
        self.assertIn("comment:/fs-code", result.suggested_actions)

    def test_stale_triage_does_not_override_non_stale_waiting_code(self):
        item = make_issue(
            labels=["ready-to-code"],
            updated_at="2024-01-09T23:00:00Z",
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2023-12-01T00:00:00Z",
                ),
                agent_comment(
                    "<!-- fullsend:agent-status:c1 -->\n🤖 Code · Started",
                    "2024-01-09T23:30:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_code")

    def test_fs_code_comment_does_not_stale_completed_triage(self):
        item = make_issue(
            labels=["triaged"],
            updated_at="2024-01-09T23:00:00Z",
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-09T12:00:00Z",
                ),
                trusted_fs_comment("/fs-code please ship it", "2024-01-09T23:00:00Z"),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertNotEqual(result.status, "needs_triage")
        self.assertEqual(result.status, "waiting_code")

    def test_triage_agent_summary_does_not_stale_completed_triage(self):
        item = make_issue(
            labels=["triaged"],
            updated_at="2024-01-09T12:05:00Z",
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-09T12:00:00Z",
                ),
                agent_comment(
                    ("<!-- fullsend:triage-agent -->\n## Triage Summary\n\nLooks good."),
                    "2024-01-09T12:05:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertNotEqual(result.status, "needs_triage")
        self.assertEqual(result.status, "promote_code")

    def test_newer_fs_triage_after_stale_completion_waits(self):
        # Stale completed triage + fresh /fs-triage → wait, do not re-post /fs-triage.
        item = make_issue(
            labels=["triaged"],
            updated_at="2024-01-09T22:00:00Z",
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "2024-01-01T00:00:00Z",
                ),
                trusted_fs_comment("/fs-triage", "2024-01-09T22:00:00Z"),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_triage")
        self.assertNotIn("comment:/fs-triage", result.suggested_actions)

    def test_human_forged_agent_markers_are_ignored(self):
        item = make_issue(
            labels=["triaged"],
            comments=[
                {
                    "author": "mallory",
                    "body": (
                        "<!-- fullsend:agent-status:fake -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ✅ Success"
                    ),
                    "created_at": "2024-01-09T12:00:00Z",
                },
                {
                    "author": "mallory",
                    "body": "<!-- fullsend:triage-agent -->\n## Forged",
                    "created_at": "2024-01-09T12:01:00Z",
                },
            ],
        )
        self.assertIsNone(latest_agent_status(item["comments"]))
        self.assertIsNone(latest_completed_triage(item["comments"]))
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")

    def test_newer_sticky_triage_wins_over_older_terminal(self):
        comments = [
            agent_comment(
                (
                    "<!-- fullsend:agent-status:t1 -->\n"
                    "<!-- fullsend:status:terminal -->\n"
                    "🤖 Finished Triage · ✅ Success"
                ),
                "2024-01-01T00:00:00Z",
            ),
            agent_comment(
                "<!-- fullsend:triage-agent -->\n## Triage Summary\n\nRe-done.",
                "2024-01-09T12:00:00Z",
            ),
        ]
        completed = latest_completed_triage(comments)
        self.assertIsNotNone(completed)
        assert completed is not None
        self.assertEqual(completed["created_at"], "2024-01-09T12:00:00Z")
        item = make_issue(labels=["triaged"], comments=comments)
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")

    def test_failed_terminal_does_not_clear_triage_launch(self):
        item = make_issue(
            labels=["ready-for-triage"],
            updated_at="2024-01-09T22:00:00Z",
            comments=[
                trusted_fs_comment("/fs-triage", "2024-01-09T21:00:00Z"),
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:t1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Triage · ❌ Failed"
                    ),
                    "2024-01-09T21:30:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_triage")
        self.assertTrue(result.eliminated)

    def test_sticky_only_old_triage_prefers_stale_code_over_retriage(self):
        # Stale completed triage + stale ready-to-code → trigger_code, not re-triage.
        item = make_issue(
            labels=["ready-to-code"],
            updated_at="2024-01-01T00:00:00Z",
            comments=[
                agent_comment(
                    ("<!-- fullsend:triage-agent -->\n## Triage Summary\n\nReady to implement."),
                    "2024-01-01T00:00:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_code")
        self.assertIn("comment:/fs-code", result.suggested_actions)

    def test_sticky_only_fresh_triage_does_not_stale(self):
        item = make_issue(
            labels=["triaged"],
            updated_at="2024-01-09T12:00:00Z",
            comments=[
                agent_comment(
                    ("<!-- fullsend:triage-agent -->\n## Triage Summary\n\nLooks good."),
                    "2024-01-09T12:00:00Z",
                ),
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertNotEqual(result.status, "needs_triage")
        self.assertEqual(result.status, "promote_code")

    def test_sticky_only_clears_recent_fs_triage_launch(self):
        item = make_issue(
            labels=["triaged", "feature"],
            assignees=[],
            comments=[
                trusted_fs_comment("/fs-triage", "2024-01-09T22:00:00Z"),
                {
                    "author": "fullsend-ai-triage[bot]",
                    "body": ("<!-- fullsend:triage-agent -->\n## Triage Summary\n\nDone."),
                    "created_at": "2024-01-09T22:05:00Z",
                },
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "promote_code")
        self.assertFalse(result.eliminated)

    def test_needs_breakdown_actionable(self):
        """Issue with needs-breakdown label is actionable with suggested_actions."""
        item = make_issue(labels=["needs-breakdown"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_breakdown")
        self.assertFalse(result.eliminated)
        self.assertEqual(result.suggested_actions, ["Break this issue into smaller sub-issues"])

    def test_needs_breakdown_unassigned(self):
        """Unassigned needs-breakdown issue is still actionable (not needs_assign)."""
        item = make_issue(labels=["needs-breakdown"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_breakdown")
        self.assertFalse(result.eliminated)

    def test_needs_breakdown_with_triaged(self):
        """needs-breakdown takes priority over triaged (would be promote_code)."""
        item = make_issue(labels=["needs-breakdown", "triaged"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_breakdown")
        self.assertFalse(result.eliminated)

    def test_needs_design_actionable(self):
        """Issue with needs-design label is actionable with suggested_actions."""
        item = make_issue(labels=["needs-design"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_design")
        self.assertFalse(result.eliminated)
        self.assertEqual(result.suggested_actions, ["Add the missing design details"])

    def test_needs_design_with_triaged(self):
        """needs-design takes priority over triaged (would be promote_code)."""
        item = make_issue(labels=["needs-design", "triaged"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_design")
        self.assertFalse(result.eliminated)

    def test_workflow_blocked_actionable(self):
        """Triaged issue with workflow-blocked is actionable with suggested_actions."""
        item = make_issue(labels=["triaged", "workflow-blocked"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "workflow_blocked")
        self.assertFalse(result.eliminated)
        self.assertEqual(
            result.suggested_actions,
            ["Implement locally; the code agent cannot push workflow changes"],
        )

    def test_workflow_blocked_without_triaged(self):
        """workflow-blocked alone is still classified as workflow_blocked."""
        item = make_issue(labels=["workflow-blocked"], assignees=["alice"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "workflow_blocked")
        self.assertFalse(result.eliminated)

    def test_blockers_win_over_needs_breakdown(self):
        """Structured blockers take priority over needs-breakdown."""
        item = make_issue(
            labels=["needs-breakdown"],
            blockers=[{"repo": "acme/widget", "number": 7}],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "blocked_by")

    def test_assigned_elsewhere_wins_over_needs_design(self):
        """assigned_elsewhere takes priority over needs-design."""
        item = make_issue(labels=["needs-design"], assignees=["bob"])
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "assigned_elsewhere")

    def test_needs_info_wins_over_needs_breakdown(self):
        """needs-info takes priority over needs-breakdown."""
        item = make_issue(
            labels=["needs-info", "needs-breakdown"],
            author="alice",
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_info_self")

    def test_stale_inflight_triage_retriggers(self):
        item = make_issue(
            labels=["ready-for-triage"],
            comments=[
                agent_comment(
                    "<!-- fullsend:agent-status:t1 -->\n🤖 Triage · Started",
                    "2024-01-01T00:00:00Z",
                )
            ],
        )
        result = classify_issue(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_triage")
        self.assertIn("comment:/fs-triage", result.suggested_actions)


class TestClassifyPr(unittest.TestCase):
    def test_blocked_label_only_is_ignored(self):
        item = make_pr(labels=["blocked"])
        result = classify_item(item, "alice", 6, NOW)
        self.assertNotEqual(result.status, "blocked_by")
        # PRs keep the blocked label — no structured blockedBy replacement exists.
        self.assertNotIn(REMOVE_BLOCKED_LABEL, result.suggested_actions)

    def test_assigned_elsewhere(self):
        item = make_pr(assignees=["bob"])
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "assigned_elsewhere")

    def test_fix_conflicts(self):
        item = make_pr(merge_state_status="DIRTY")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "fix_conflicts")
        self.assertFalse(result.eliminated)

    def test_fix_conflicts_via_mergeable_conflicting(self):
        # GraphQL sometimes returns UNKNOWN mergeStateStatus until mergeable is computed.
        item = make_pr(merge_state_status="UNKNOWN", mergeable="CONFLICTING")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "fix_conflicts")

    def test_needs_review_decision_manual_review(self):
        item = make_pr(labels=["requires-manual-review"])
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")

    def test_needs_review_decision_needs_human(self):
        item = make_pr(labels=["needs-human"])
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")

    def test_ready_to_merge(self):
        item = make_pr(
            labels=["ready-for-merge"],
            in_merge_queue=False,
            merge_state_status="CLEAN",
            mergeable="MERGEABLE",
            unresolved_review_threads=0,
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "ready_to_merge")
        self.assertFalse(result.eliminated)

    def test_ready_for_merge_with_unresolved_threads(self):
        # ready-for-merge label but open review conversations.
        item = make_pr(
            labels=["ready-for-merge"],
            review_decision=None,
            merge_state_status="BLOCKED",
            mergeable="MERGEABLE",
            unresolved_review_threads=2,
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertIn("unresolved", result.reason)

    def test_ready_for_merge_blocked_without_threads(self):
        item = make_pr(
            labels=["ready-for-merge"],
            review_decision=None,
            merge_state_status="BLOCKED",
            mergeable="MERGEABLE",
            unresolved_review_threads=0,
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertIn("branch protection", result.reason)

    def test_ready_for_merge_unknown_merge_state_not_ready(self):
        item = make_pr(
            labels=["ready-for-merge"],
            merge_state_status="UNKNOWN",
            mergeable="MERGEABLE",
            unresolved_review_threads=0,
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertNotEqual(result.status, "ready_to_merge")
        self.assertEqual(result.status, "needs_review_decision")

    def test_ready_for_merge_with_pending_checks_is_waiting_ci(self):
        item = make_pr(
            labels=["ready-for-merge"],
            checks_state="PENDING",
            review_decision="APPROVED",
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_ci")
        self.assertTrue(result.eliminated)

    def test_ready_for_merge_with_review_required_is_waiting_review(self):
        item = make_pr(
            labels=["ready-for-merge", "ready-for-review"],
            review_decision="REVIEW_REQUIRED",
            updated_at="2024-01-09T23:00:00Z",
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_review")
        self.assertTrue(result.eliminated)

    def test_ready_for_merge_with_inflight_review_comment(self):
        # Stale ready-for-merge while review agent has Started.
        item = make_pr(
            labels=["ready-for-merge", "ready-for-review"],
            review_decision="REVIEW_REQUIRED",
            comments=[
                {
                    "author": "fullsend-ai-review[bot]",
                    "body": (
                        "<!-- fullsend:agent-status:30004814427 -->\n"
                        "🤖 Review · Started 11:54 AM UTC\n"
                        "Commit: `7ada4e0`"
                    ),
                    "created_at": "2024-01-09T23:54:00Z",
                }
            ],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_review")
        self.assertTrue(result.eliminated)

    def test_waiting_merge_queue(self):
        item = make_pr(labels=["ready-for-merge"], in_merge_queue=True)
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_merge_queue")
        self.assertTrue(result.eliminated)

    def test_waiting_merge_queue_without_ready_for_merge_label(self):
        """Merge-queue detection works regardless of which labels are present."""
        item = make_pr(labels=["requires-manual-review"], in_merge_queue=True)
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_merge_queue")
        self.assertTrue(result.eliminated)

    def test_waiting_merge_queue_no_labels(self):
        """Merge-queue detection works even with no control labels at all."""
        item = make_pr(labels=[], in_merge_queue=True)
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_merge_queue")
        self.assertTrue(result.eliminated)

    def test_waiting_fix_bot_author(self):
        item = make_pr(
            author="fullsend-ai-coder[bot]",
            review_decision="CHANGES_REQUESTED",
            updated_at="2024-01-09T23:00:00Z",
            unresolved_threads=[
                {"author": "fullsend-ai-review[bot]", "created_at": "2024-01-09T23:00:00Z"}
            ],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_fix")
        self.assertTrue(result.eliminated)

    def test_bot_thread_with_human_reply_needs_decision(self):
        item = make_pr(
            author="fullsend-ai-coder[bot]",
            review_decision="CHANGES_REQUESTED",
            updated_at="2024-01-09T23:00:00Z",
            unresolved_threads=[
                {
                    "author": "fullsend-ai-review[bot]",
                    "authors": ["fullsend-ai-review[bot]", "carol"],
                    "created_at": "2024-01-09T23:00:00Z",
                    "bot_only": False,
                }
            ],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertFalse(result.eliminated)

    def test_trigger_fix_stale_bot_author(self):
        item = make_pr(
            author="fullsend-ai-coder[bot]",
            review_decision="CHANGES_REQUESTED",
            updated_at="2024-01-01T00:00:00Z",
            unresolved_threads=[
                {"author": "fullsend-ai-review[bot]", "created_at": "2024-01-01T00:00:00Z"}
            ],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_fix")
        self.assertFalse(result.eliminated)

    def test_waiting_fix_human_with_fullsend_fix_label(self):
        # Human threads are decisions even with fullsend-fix; only review-bot threads are trivial.
        item = make_pr(
            author="carol",
            labels=["fullsend-fix"],
            review_decision="CHANGES_REQUESTED",
            updated_at="2024-01-09T23:00:00Z",
            unresolved_threads=[{"author": "carol", "created_at": "2024-01-09T23:00:00Z"}],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")

    def test_changes_requested_without_threads_not_fix(self):
        item = make_pr(author="carol", review_decision="CHANGES_REQUESTED")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertIn("requested changes", result.reason.lower())
        self.assertNotEqual(result.status, "trigger_fix")
        self.assertNotEqual(result.status, "waiting_fix")

    def test_changes_requested_with_ready_for_merge(self):
        item = make_pr(
            labels=["ready-for-merge"],
            review_decision="CHANGES_REQUESTED",
            checks_state="SUCCESS",
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertIn("ready-for-merge", result.reason)

    def test_fullsend_no_fix_with_review_bot_threads(self):
        item = make_pr(
            author="fullsend-ai-coder[bot]",
            labels=["fullsend-no-fix"],
            review_decision="CHANGES_REQUESTED",
            unresolved_threads=[
                {"author": "fullsend-ai-review[bot]", "created_at": "2024-01-09T23:00:00Z"}
            ],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertEqual(len(result.suggested_actions), 1)
        suggestion = result.suggested_actions[0]
        self.assertIn("comment:/fs-fix", suggestion)
        self.assertIn("only blocks automatic", suggestion)
        self.assertNotIn("remove fullsend-no-fix", suggestion)

    def test_waiting_ci(self):
        item = make_pr(checks_state="PENDING")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_ci")
        self.assertTrue(result.eliminated)

    def test_failed_ci_needs_review_decision(self):
        item = make_pr(checks_state="FAILURE", review_decision="REVIEW_REQUIRED")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")
        self.assertIn("failed", result.reason.lower())

    def test_newer_code_after_review_triggers_review(self):
        item = make_pr(
            labels=["ready-for-review"],
            review_decision="REVIEW_REQUIRED",
            updated_at="2024-01-09T23:00:00Z",
            head_committed_at="2024-01-09T22:00:00Z",
            comments=[
                agent_comment(
                    (
                        "<!-- fullsend:agent-status:r1 -->\n"
                        "<!-- fullsend:status:terminal -->\n"
                        "🤖 Finished Review · ✅ Success"
                    ),
                    "2024-01-09T12:00:00Z",
                )
            ],
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_review")

    def test_newer_code_after_human_approval_triggers_review(self):
        item = make_pr(
            labels=["ready-for-review"],
            review_decision="APPROVED",
            updated_at="2024-01-09T23:00:00Z",
            head_committed_at="2024-01-09T22:00:00Z",
            latest_approved_review_at="2024-01-09T12:00:00Z",
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_review")

    def test_mixed_review_threads_need_decision(self):
        item = make_pr(
            unresolved_threads=[
                {"author": "fullsend-ai-review[bot]", "created_at": "2024-01-09T23:00:00Z"},
                {"author": "carol", "created_at": "2024-01-09T23:30:00Z"},
            ]
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "needs_review_decision")

    def test_draft_pr(self):
        item = make_pr(is_draft=True)
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "human_work")
        self.assertFalse(result.eliminated)

    def test_draft_pr_with_pending_checks_is_human_work(self):
        item = make_pr(is_draft=True, checks_state="PENDING")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "human_work")
        self.assertFalse(result.eliminated)

    def test_waiting_review(self):
        item = make_pr(labels=["ready-for-review"], updated_at="2024-01-09T23:00:00Z")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "waiting_review")
        self.assertTrue(result.eliminated)

    def test_trigger_review_stale(self):
        item = make_pr(labels=["ready-for-review"], updated_at="2024-01-01T00:00:00Z")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "trigger_review")
        self.assertFalse(result.eliminated)

    def test_fallback_human_work(self):
        item = make_pr(review_decision="APPROVED", checks_state="SUCCESS")
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "human_work")
        self.assertFalse(result.eliminated)

    def test_approved_with_stale_ready_for_review_is_actionable(self):
        item = make_pr(
            labels=["ready-for-review"],
            review_decision="APPROVED",
            checks_state="SUCCESS",
        )
        result = classify_pr(item, "alice", 6, NOW)
        self.assertEqual(result.status, "human_work")
        self.assertFalse(result.eliminated)
        self.assertNotEqual(result.status, "waiting_review")


class FakeFetcher:
    """Stub GhFetcher for build_queue tests: no gh subprocess calls."""

    def __init__(self, items_by_ref):
        self._items_by_ref = items_by_ref

    def fetch_item(self, repo, number):
        item = self._items_by_ref.get((repo, number))
        return dict(item) if item else None


class TestBuildQueue(unittest.TestCase):
    def test_follows_open_blockers_cross_repo(self):
        items = {
            ("acme/widget", 1): make_issue(
                repo="acme/widget",
                number=1,
                blockers=[{"repo": "acme/other", "number": 2}],
            ),
            ("acme/other", 2): make_issue(repo="acme/other", number=2, labels=["question"]),
        }
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1)], fetcher, "alice", 6, NOW
        )
        numbers = {(r["repo"], r["number"]) for r in results}
        self.assertEqual(numbers, {("acme/widget", 1), ("acme/other", 2)})
        first = next(r for r in results if r["number"] == 1)
        self.assertEqual(first["status"], "blocked_by")

    def test_does_not_revisit(self):
        items = {("acme/widget", 1): make_issue(repo="acme/widget", number=1)}
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1), ("acme/widget", 1)], fetcher, "alice", 6, NOW
        )
        self.assertEqual(len(results), 1)

    def test_drops_closed_items(self):
        items = {("acme/widget", 1): make_issue(repo="acme/widget", number=1, state="CLOSED")}
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1)], fetcher, "alice", 6, NOW
        )
        self.assertEqual(results, [])

    def test_drops_duplicate_labeled(self):
        items = {("acme/widget", 1): make_issue(repo="acme/widget", number=1, labels=["duplicate"])}
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1)], fetcher, "alice", 6, NOW
        )
        self.assertEqual(results, [])

    def test_respects_max_visits_cap(self):
        # Long blocker chain: 1 <- 2 <- 3 <- ...; visit cap stops deepen-first walking.
        items = {}
        for n in range(1, 10):
            blockers = [{"repo": "acme/widget", "number": n + 1}] if n < 9 else []
            items[("acme/widget", n)] = make_issue(repo="acme/widget", number=n, blockers=blockers)
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1)], fetcher, "alice", 6, NOW, max_visits=3
        )
        self.assertEqual(len(results), 3)

    def test_enqueues_open_sub_issues(self):
        items = {
            ("acme/widget", 1): make_issue(
                repo="acme/widget",
                number=1,
                labels=["triaged"],
                sub_issues_total=2,
                sub_issues_completed=0,
                open_sub_issues=[
                    {"repo": "acme/widget", "number": 2, "title": "Child A"},
                    {"repo": "acme/widget", "number": 3, "title": "Child B"},
                ],
            ),
            ("acme/widget", 2): make_issue(
                repo="acme/widget", number=2, labels=["question"], assignees=["alice"]
            ),
            ("acme/widget", 3): make_issue(
                repo="acme/widget", number=3, labels=["question"], assignees=["alice"]
            ),
        }
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1)], fetcher, "alice", 6, NOW
        )
        numbers = {r["number"] for r in results}
        self.assertEqual(numbers, {1, 2, 3})
        parent = next(r for r in results if r["number"] == 1)
        self.assertEqual(parent["status"], "waiting_sub_issues")
        self.assertTrue(parent["eliminated"])
        self.assertEqual(len(parent["open_sub_issues"]), 2)

    def test_deepen_first_before_unrelated_seeds(self):
        # Seeds: blocked parent, then a leaf. FIFO append would visit the leaf
        # before the blocker under max_visits=2; deepen-first must reach the root.
        items = {
            ("acme/widget", 1): make_issue(
                repo="acme/widget",
                number=1,
                blockers=[{"repo": "acme/widget", "number": 2}],
            ),
            ("acme/widget", 2): make_issue(
                repo="acme/widget", number=2, labels=["question"], assignees=["alice"]
            ),
            ("acme/widget", 3): make_issue(
                repo="acme/widget", number=3, labels=["question"], assignees=["alice"]
            ),
        }
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 1), ("acme/widget", 3)],
            fetcher,
            "alice",
            6,
            NOW,
            max_visits=2,
        )
        numbers = [r["number"] for r in results]
        self.assertEqual(numbers, [1, 2])
        self.assertNotIn(3, numbers)

    def test_dropped_fetches_do_not_burn_visit_budget(self):
        # Closed + duplicate seeds must not steal slots from an open blocker chain.
        items = {
            ("acme/widget", 99): make_issue(repo="acme/widget", number=99, state="CLOSED"),
            ("acme/widget", 98): make_issue(repo="acme/widget", number=98, labels=["duplicate"]),
        }
        for n in range(1, 6):
            blockers = [{"repo": "acme/widget", "number": n + 1}] if n < 5 else []
            items[("acme/widget", n)] = make_issue(repo="acme/widget", number=n, blockers=blockers)
        fetcher = FakeFetcher(items)
        results, _remaining, _fetch_errors = build_queue(
            [("acme/widget", 99), ("acme/widget", 98), ("acme/widget", 1)],
            fetcher,
            "alice",
            6,
            NOW,
            max_visits=3,
        )
        self.assertEqual(len(results), 3)
        self.assertEqual([r["number"] for r in results], [1, 2, 3])


class TestApplyTrivialActions(unittest.TestCase):
    @patch("nextwork.run_gh_soft", return_value="")
    def test_assigns_unassigned(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 1,
                "status": "needs_assign",
                "eliminated": False,
                "suggested_actions": [ASSIGN_SELF],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(len(applied), 1)
        self.assertEqual(applied[0]["action"], ASSIGN_SELF)
        mock_run_gh_soft.assert_called_once_with(
            ["issue", "edit", "1", "--repo", "acme/widget", "--add-assignee", "alice"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    def test_posts_slash_command_for_pr(self, mock_run_gh_soft):
        items = [
            {
                "kind": "pull",
                "repo": "acme/widget",
                "number": 99,
                "status": "trigger_review",
                "eliminated": False,
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(applied[0]["action"], "comment:/fs-review")
        mock_run_gh_soft.assert_called_once_with(
            ["pr", "comment", "99", "--repo", "acme/widget", "--body", "/fs-review"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    def test_assign_before_slash_comment(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 7,
                "status": "trigger_code",
                "eliminated": False,
                "suggested_actions": [ASSIGN_SELF, "comment:/fs-code"],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        actions = [a["action"] for a in applied]
        self.assertEqual(actions, [ASSIGN_SELF, "comment:/fs-code"])
        self.assertEqual(
            mock_run_gh_soft.call_args_list[0].args[0],
            ["issue", "edit", "7", "--repo", "acme/widget", "--add-assignee", "alice"],
        )
        self.assertEqual(
            mock_run_gh_soft.call_args_list[1].args[0],
            ["issue", "comment", "7", "--repo", "acme/widget", "--body", "/fs-code"],
        )

    @patch("nextwork.run_gh_soft", return_value="")
    def test_assign_on_pr_uses_pr_subcommand(self, mock_run_gh_soft):
        items = [
            {
                "kind": "pull",
                "repo": "acme/widget",
                "number": 99,
                "status": "trigger_review",
                "eliminated": False,
                "suggested_actions": [ASSIGN_SELF, "comment:/fs-review"],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(applied[0]["action"], ASSIGN_SELF)
        mock_run_gh_soft.assert_any_call(
            ["pr", "edit", "99", "--repo", "acme/widget", "--add-assignee", "alice"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    def test_skips_eliminated_and_non_trivial(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "a/b",
                "number": 1,
                "status": "waiting_code",
                "eliminated": True,
            },
            {
                "kind": "issue",
                "repo": "a/b",
                "number": 2,
                "status": "human_work",
                "eliminated": False,
            },
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(applied, [])
        mock_run_gh_soft.assert_not_called()

    @patch("nextwork.run_gh_soft", return_value="")
    def test_removes_orphaned_blocked_label(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 5,
                "status": "waiting_code",
                "eliminated": True,
                "assignees": ["alice"],
                "suggested_actions": [REMOVE_BLOCKED_LABEL],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(len(applied), 1)
        self.assertEqual(applied[0]["action"], REMOVE_BLOCKED_LABEL)
        mock_run_gh_soft.assert_called_once_with(
            ["issue", "edit", "5", "--repo", "acme/widget", "--remove-label", "blocked"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    def test_skips_remove_blocked_for_assigned_elsewhere(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 5,
                "status": "assigned_elsewhere",
                "eliminated": True,
                "assignees": ["bob"],
                "suggested_actions": [REMOVE_BLOCKED_LABEL],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(applied, [])
        mock_run_gh_soft.assert_not_called()

    @patch("nextwork.run_gh_soft", return_value="")
    def test_apply_both_primary_and_remove_blocked(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 99,
                "status": "needs_assign",
                "eliminated": False,
                "assignees": [],
                "suggested_actions": [ASSIGN_SELF, REMOVE_BLOCKED_LABEL],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        actions = [a["action"] for a in applied]
        self.assertEqual(actions, [ASSIGN_SELF, REMOVE_BLOCKED_LABEL])
        self.assertEqual(mock_run_gh_soft.call_count, 2)
        mock_run_gh_soft.assert_any_call(
            ["issue", "edit", "99", "--repo", "acme/widget", "--add-assignee", "alice"],
            quiet=False,
        )
        mock_run_gh_soft.assert_any_call(
            ["issue", "edit", "99", "--repo", "acme/widget", "--remove-label", "blocked"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    def test_apply_assign_on_decision_status(self, mock_run_gh_soft):
        items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 3,
                "status": "promote_code",
                "eliminated": False,
                "suggested_actions": [ASSIGN_SELF, "decision: promote"],
            }
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual([a["action"] for a in applied], [ASSIGN_SELF])
        mock_run_gh_soft.assert_called_once_with(
            ["issue", "edit", "3", "--repo", "acme/widget", "--add-assignee", "alice"],
            quiet=False,
        )


class TestAgentTerminalSucceeded(unittest.TestCase):
    def test_success_and_failure_markers(self):
        self.assertTrue(agent_terminal_succeeded("🤖 Finished · ✅ Success"))
        self.assertFalse(agent_terminal_succeeded("🤖 Finished · ❌ Failed"))
        self.assertFalse(agent_terminal_succeeded("Terminated by user"))
        self.assertFalse(agent_terminal_succeeded("run cancelled"))
        self.assertFalse(
            agent_terminal_succeeded(
                "🤖 Finished Code · ⏭️ Skipped (change is already in review on #123)"
            )
        )

    def test_ambiguous_body_defaults_to_success(self):
        # Legacy sticky / marker-only posts without an explicit outcome line.
        self.assertTrue(agent_terminal_succeeded("<!-- fullsend:status:terminal -->"))
        self.assertTrue(agent_terminal_succeeded("🤖 Finished Triage · done"))


class TestRoleWaitingStatus(unittest.TestCase):
    def test_uses_structured_prefix_not_skip_reason(self):
        from nextwork import _role_waiting_status

        body = (
            "<!-- fullsend:agent-status:1 -->\n<!-- fullsend:status:terminal -->\n"
            "🤖 Finished Code · ⏭️ Skipped (change is already in review on #123)"
        )
        self.assertEqual(_role_waiting_status(body), "waiting_code")
        self.assertEqual(
            _role_waiting_status("🤖 Review · Started 1:00 PM UTC"),
            "waiting_review",
        )


class TestTakeOver(unittest.TestCase):
    @patch("nextwork.run_gh_soft", return_value="")
    @patch("nextwork.gh_graphql_or_none")
    def test_assigns_issue(self, mock_gql, mock_run_gh_soft):
        mock_gql.return_value = {
            "repository": {
                "issueOrPullRequest": {
                    "__typename": "Issue",
                    "id": "I_1",
                    "state": "OPEN",
                    "assignees": {"nodes": []},
                }
            }
        }
        result = take_over("acme/widget", 1, "alice")
        self.assertEqual(result["action"], "assigned")
        mock_run_gh_soft.assert_called_once_with(
            ["issue", "edit", "1", "--repo", "acme/widget", "--add-assignee", "alice"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    @patch("nextwork.gh_graphql_or_none")
    def test_assigns_pull_request(self, mock_gql, mock_run_gh_soft):
        mock_gql.return_value = {
            "repository": {
                "issueOrPullRequest": {
                    "__typename": "PullRequest",
                    "id": "PR_1",
                    "state": "OPEN",
                    "assignees": {"nodes": []},
                }
            }
        }
        result = take_over("acme/widget", 99, "alice")
        self.assertEqual(result["action"], "assigned")
        mock_run_gh_soft.assert_called_once_with(
            ["pr", "edit", "99", "--repo", "acme/widget", "--add-assignee", "alice"],
            quiet=False,
        )

    @patch("nextwork.run_gh_soft", return_value="")
    @patch("nextwork.gh_graphql_or_none")
    def test_removes_prior_assignees(self, mock_gql, mock_run_gh_soft):
        mock_gql.return_value = {
            "repository": {
                "issueOrPullRequest": {
                    "__typename": "Issue",
                    "id": "I_1",
                    "state": "OPEN",
                    "assignees": {"nodes": [{"login": "bob"}, {"login": "carol"}]},
                }
            }
        }
        result = take_over("acme/widget", 1, "alice")
        self.assertEqual(result["action"], "assigned")
        self.assertIn("removed bob, carol", result["detail"])
        calls = [c.args[0] for c in mock_run_gh_soft.call_args_list]
        self.assertEqual(
            calls,
            [
                ["issue", "edit", "1", "--repo", "acme/widget", "--add-assignee", "alice"],
                ["issue", "edit", "1", "--repo", "acme/widget", "--remove-assignee", "bob"],
                ["issue", "edit", "1", "--repo", "acme/widget", "--remove-assignee", "carol"],
            ],
        )

    @patch("nextwork.gh_graphql_or_none")
    def test_skips_closed(self, mock_gql):
        mock_gql.return_value = {
            "repository": {
                "issueOrPullRequest": {
                    "__typename": "Issue",
                    "id": "I_1",
                    "state": "CLOSED",
                    "assignees": {"nodes": []},
                }
            }
        }
        result = take_over("acme/widget", 1, "alice")
        self.assertEqual(result["action"], "error")
        self.assertIn("not open", result["detail"])

    @patch("nextwork.gh_graphql_or_none", return_value=None)
    def test_ref_not_found(self, _mock_gql):
        result = take_over("acme/widget", 404, "alice")
        self.assertEqual(result["action"], "error")


class TestGhFetcher(unittest.TestCase):
    @patch("nextwork.gh_graphql_or_none")
    def test_fetch_item_normalizes_issue(self, mock_gql):
        issue_node = {
            "__typename": "Issue",
            "number": 7,
            "title": "Ship it",
            "url": "https://github.com/acme/widget/issues/7",
            "state": "OPEN",
            "author": {"login": "alice"},
            "assignees": {"nodes": []},
            "labels": {"nodes": [{"name": "triaged"}]},
            "createdAt": "2024-01-01T00:00:00Z",
            "updatedAt": "2024-01-09T00:00:00Z",
            "body": "hello",
            "comments": {"nodes": []},
        }
        deps_node = {
            "blockedBy": {"nodes": []},
            "subIssuesSummary": {"total": 0, "completed": 0},
            "subIssues": {"nodes": []},
        }
        mock_gql.side_effect = [
            {"repository": {"issueOrPullRequest": issue_node}},
            {"repository": {"issue": deps_node}},
        ]
        item = GhFetcher(quiet=True).fetch_item("acme/widget", 7)
        self.assertIsNotNone(item)
        assert item is not None
        self.assertEqual(item["kind"], "issue")
        self.assertEqual(item["number"], 7)
        self.assertEqual(item["labels"], ["triaged"])
        self.assertEqual(mock_gql.call_count, 2)

    @patch("nextwork.gh_graphql_or_none")
    def test_fetch_item_continues_when_dependencies_fail(self, mock_gql):
        issue_node = {
            "__typename": "Issue",
            "number": 7,
            "title": "Ship it",
            "url": "https://github.com/acme/widget/issues/7",
            "state": "OPEN",
            "author": {"login": "alice"},
            "assignees": {"nodes": []},
            "labels": {"nodes": [{"name": "triaged"}]},
            "createdAt": "2024-01-01T00:00:00Z",
            "updatedAt": "2024-01-09T00:00:00Z",
            "body": "hello",
            "comments": {"nodes": []},
        }
        mock_gql.side_effect = [
            {"repository": {"issueOrPullRequest": issue_node}},
            None,  # ISSUE_DEPENDENCIES_QUERY fails (schema gap)
        ]
        item = GhFetcher(quiet=True).fetch_item("acme/widget", 7)
        self.assertIsNotNone(item)
        assert item is not None
        self.assertEqual(item["kind"], "issue")
        self.assertEqual(item["blockers"], [])
        self.assertEqual(item["open_sub_issues"], [])
        self.assertEqual(item["sub_issues_total"], 0)

    @patch("nextwork.gh_graphql_or_none", return_value=None)
    def test_fetch_item_raises_on_gh_failure(self, _mock_gql):
        from nextwork import FetchError

        with self.assertRaises(FetchError) as ctx:
            GhFetcher(quiet=True).fetch_item("acme/widget", 1)
        self.assertEqual(ctx.exception.repo, "acme/widget")
        self.assertEqual(ctx.exception.number, 1)

    @patch("nextwork.gh_graphql_or_none")
    def test_get_linked_prs_caches_pull_pages(self, mock_gql):
        mock_gql.return_value = {
            "repository": {
                "pullRequests": {
                    "nodes": [
                        {
                            "number": 10,
                            "body": "Fixes #7",
                            "closingIssuesReferences": {"nodes": []},
                        }
                    ],
                    "pageInfo": {"hasNextPage": False, "endCursor": None},
                }
            }
        }
        fetcher = GhFetcher(quiet=True)
        self.assertEqual(fetcher.get_linked_prs("acme/widget", 7), [10])
        self.assertEqual(fetcher.get_linked_prs("acme/widget", 7), [10])
        self.assertEqual(mock_gql.call_count, 1)

    @patch("nextwork.gh_graphql_or_none")
    def test_is_in_merge_queue(self, mock_gql):
        merge_queue = {
            "repository": {"mergeQueue": {"entries": {"nodes": [{"pullRequest": {"number": 42}}]}}}
        }
        mock_gql.side_effect = [
            {"repository": {"defaultBranchRef": {"name": "main"}}},
            merge_queue,
        ]
        fetcher = GhFetcher(quiet=True)
        self.assertTrue(fetcher.is_in_merge_queue("acme/widget", 42))
        self.assertFalse(fetcher.is_in_merge_queue("acme/widget", 99))
        # default branch + queue cached; second call hits neither GraphQL again
        self.assertEqual(mock_gql.call_count, 2)

    @patch("nextwork.gh_graphql_or_none")
    def test_is_in_merge_queue_uses_base_branch(self, mock_gql):
        merge_queue = {
            "repository": {"mergeQueue": {"entries": {"nodes": [{"pullRequest": {"number": 7}}]}}}
        }
        mock_gql.return_value = merge_queue
        fetcher = GhFetcher(quiet=True)
        self.assertTrue(fetcher.is_in_merge_queue("acme/widget", 7, base_branch="release-1"))
        # No default-branch query when base_branch is provided.
        self.assertEqual(mock_gql.call_count, 1)
        self.assertEqual(mock_gql.call_args.args[1]["branch"], "release-1")


class TestLinkBlocker(unittest.TestCase):
    @patch("nextwork.gh_graphql_or_none")
    def test_creates_new_link(self, mock_gql):
        mock_gql.side_effect = [
            {"repository": {"issue": {"id": "I_DEP", "state": "OPEN", "blockedBy": {"nodes": []}}}},
            {"repository": {"issue": {"id": "I_BLK", "state": "OPEN"}}},
            {"addBlockedBy": {"issue": {"number": 1}}},
        ]
        result = link_blocker(("acme/widget", 1), ("acme/widget", 2))
        self.assertEqual(result["action"], "linked")
        self.assertEqual(mock_gql.call_count, 3)
        _args, _kwargs = mock_gql.call_args_list[2]
        self.assertEqual(_args[1], {"issueId": "I_DEP", "blockingIssueId": "I_BLK"})

    @patch("nextwork.gh_graphql_or_none")
    def test_already_linked_is_idempotent(self, mock_gql):
        mock_gql.return_value = {
            "repository": {
                "issue": {
                    "id": "I_DEP",
                    "state": "OPEN",
                    "blockedBy": {
                        "nodes": [{"number": 2, "repository": {"nameWithOwner": "acme/widget"}}]
                    },
                }
            }
        }
        result = link_blocker(("acme/widget", 1), ("acme/widget", 2))
        self.assertEqual(result["action"], "already_linked")
        self.assertEqual(mock_gql.call_count, 1)

    @patch("nextwork.gh_graphql_or_none", return_value={"repository": {"issue": None}})
    def test_dependent_not_an_issue_errors(self, _mock_gql):
        result = link_blocker(("acme/widget", 99), ("acme/widget", 2))
        self.assertEqual(result["action"], "error")
        self.assertIn("not an Issue", result["detail"])

    @patch("nextwork.gh_graphql_or_none")
    def test_dependent_closed_errors(self, mock_gql):
        mock_gql.return_value = {
            "repository": {"issue": {"id": "I_DEP", "state": "CLOSED", "blockedBy": {"nodes": []}}}
        }
        result = link_blocker(("acme/widget", 1), ("acme/widget", 2))
        self.assertEqual(result["action"], "error")
        self.assertIn("not open", result["detail"])

    @patch("nextwork.gh_graphql_or_none")
    def test_blocker_not_an_issue_errors(self, mock_gql):
        mock_gql.side_effect = [
            {"repository": {"issue": {"id": "I_DEP", "state": "OPEN", "blockedBy": {"nodes": []}}}},
            {"repository": {"issue": None}},
        ]
        result = link_blocker(("acme/widget", 1), ("acme/widget", 404))
        self.assertEqual(result["action"], "error")
        self.assertIn("issue-only", result["detail"])

    @patch("nextwork.gh_graphql_or_none")
    def test_blocker_closed_errors(self, mock_gql):
        mock_gql.side_effect = [
            {"repository": {"issue": {"id": "I_DEP", "state": "OPEN", "blockedBy": {"nodes": []}}}},
            {"repository": {"issue": {"id": "I_BLK", "state": "CLOSED"}}},
        ]
        result = link_blocker(("acme/widget", 1), ("acme/widget", 2))
        self.assertEqual(result["action"], "error")
        self.assertIn("blocker Issue is not open", result["detail"])

    def test_self_blocker_errors(self):
        result = link_blocker(("acme/widget", 5), ("acme/widget", 5))
        self.assertEqual(result["action"], "error")
        self.assertIn("cannot block itself", result["detail"])


class TestSpecParsing(unittest.TestCase):
    def test_parse_link_blocker_spec(self):
        self.assertEqual(
            parse_link_blocker_spec("acme/widget#1=acme/widget#2"),
            ("acme/widget#1", "acme/widget#2"),
        )

    def test_parse_link_blocker_spec_missing_equals(self):
        with self.assertRaises(RefError):
            parse_link_blocker_spec("acme/widget#1")

    def test_parse_take_over_specs_comma_and_repeat(self):
        self.assertEqual(
            parse_take_over_specs(["a/b#1,a/b#2", "a/b#3"]),
            ["a/b#1", "a/b#2", "a/b#3"],
        )


class TestFormatOutputs(unittest.TestCase):
    def setUp(self):
        self.items = [
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 1,
                "title": "Actionable | item",
                "url": "https://github.com/acme/widget/issues/1",
                "status": "needs_assign",
                "eliminated": False,
                "reason": "Unassigned",
                "suggested_actions": ["assign:self"],
                "blockers": [],
            },
            {
                "kind": "issue",
                "repo": "acme/widget",
                "number": 2,
                "title": "Blocked item",
                "url": "https://github.com/acme/widget/issues/2",
                "status": "blocked_by",
                "eliminated": True,
                "reason": "Blocked by open issue(s)/PR(s)",
                "suggested_actions": [],
                "blockers": [{"repo": "acme/widget", "number": 3}],
            },
        ]

    def test_json_output_shape(self):
        out = format_json_output(self.items, "acme/widget", "alice", 6, [], include_text=False)
        payload = json.loads(out)
        self.assertEqual(payload["repo"], "acme/widget")
        self.assertEqual(len(payload["items"]), 2)
        self.assertEqual(payload["items"][0]["status"], "needs_assign")
        self.assertNotIn("body", payload["items"][0])

    def test_json_output_include_text(self):
        item = dict(self.items[0], body="hello world", comments=[{"author": "bob", "body": "hi"}])
        out = format_json_output([item], "acme/widget", "alice", 6, [], include_text=True)
        payload = json.loads(out)
        self.assertIn("body", payload["items"][0])
        self.assertIn("comments", payload["items"][0])

    def test_markdown_hides_blocked_by_default(self):
        out = format_markdown_output(self.items, "acme/widget", "alice", 6, [], show_blocked=False)
        self.assertIn("Actionable", out)
        self.assertNotIn("## Blocked", out)

    def test_markdown_shows_blocked_when_requested(self):
        out = format_markdown_output(self.items, "acme/widget", "alice", 6, [], show_blocked=True)
        self.assertIn("## Blocked", out)
        self.assertIn("Blocked item", out)

    def test_markdown_shows_link_and_take_over_results(self):
        link_results = [
            {
                "dependent": "acme/widget#1",
                "blocker": "acme/widget#2",
                "action": "linked",
            }
        ]
        take_over_results = [
            {"ref": "acme/widget#3", "action": "assigned", "detail": "assigned to alice"}
        ]
        out = format_markdown_output(
            self.items,
            "acme/widget",
            "alice",
            6,
            [],
            show_blocked=False,
            link_results=link_results,
            take_over_results=take_over_results,
        )
        self.assertIn("## Link blockers", out)
        self.assertIn("linked", out)
        self.assertIn("## Take-over", out)
        self.assertIn("acme/widget#3", out)
        self.assertIn("assigned to alice", out)

    def test_markdown_shows_queue_truncation(self):
        out = format_markdown_output(
            self.items,
            "acme/widget",
            "alice",
            6,
            [],
            show_blocked=False,
            truncated_remaining=7,
        )
        self.assertIn("Queue truncated", out)
        self.assertIn("7 remaining", out)

    def test_decision_statuses_disjoint_from_trivial(self):
        from nextwork import TRIVIAL_STATUSES

        self.assertEqual(DECISION_STATUSES & TRIVIAL_STATUSES, set())


class TestResolveRepo(unittest.TestCase):
    def test_override_ok(self):
        self.assertEqual(resolve_repo("acme/widget"), "acme/widget")

    def test_override_invalid_exits_2(self):
        with self.assertRaises(SystemExit) as ctx:
            resolve_repo("not-a-repo")
        self.assertEqual(ctx.exception.code, 2)

    @patch("nextwork.subprocess.run")
    def test_gh_failure_exits_1(self, mock_run):
        import subprocess

        mock_run.side_effect = subprocess.CalledProcessError(1, ["gh"])
        with self.assertRaises(SystemExit) as ctx:
            resolve_repo(None)
        self.assertEqual(ctx.exception.code, 1)

    @patch("nextwork.subprocess.run")
    def test_gh_success(self, mock_run):
        mock_run.return_value = Mock(stdout='{"nameWithOwner":"acme/widget"}\n')
        self.assertEqual(resolve_repo(None), "acme/widget")


class TestSeedFromAssigned(unittest.TestCase):
    @patch("nextwork.run_gh")
    def test_merges_issues_and_prs(self, mock_run_gh):
        mock_run_gh.side_effect = [
            '[{"number":1},{"number":2}]',
            '[{"number":9}]',
        ]
        refs = seed_from_assigned("acme/widget", "alice")
        self.assertEqual(refs, [("acme/widget", 1), ("acme/widget", 2), ("acme/widget", 9)])
        self.assertEqual(mock_run_gh.call_count, 2)

    @patch("nextwork.run_gh", side_effect=SystemExit(3))
    def test_propagates_gh_failure(self, _mock_run_gh):
        with self.assertRaises(SystemExit) as ctx:
            seed_from_assigned("acme/widget", "alice")
        self.assertEqual(ctx.exception.code, 3)


class TestParseArgs(unittest.TestCase):
    def test_rejects_negative_stale_hours(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--stale-hours", "-1"])
        self.assertEqual(ctx.exception.code, 2)

    def test_rejects_zero_max_visits(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--max-visits", "0"])
        self.assertEqual(ctx.exception.code, 2)

    def test_accepts_triage_stale_hours(self):
        args = parse_args(["--triage-stale-hours", "24"])
        self.assertEqual(args.triage_stale_hours, 24)

    def test_apply_requires_confirmed(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--apply"])
        self.assertEqual(ctx.exception.code, 2)

    def test_take_over_requires_confirmed(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--take-over", "acme/widget#1"])
        self.assertEqual(ctx.exception.code, 2)

    def test_link_blocker_requires_confirmed(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--link-blocker", "acme/widget#1=acme/widget#2"])
        self.assertEqual(ctx.exception.code, 2)

    def test_confirmed_alone_rejected(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--confirmed"])
        self.assertEqual(ctx.exception.code, 2)

    def test_apply_with_confirmed_ok(self):
        args = parse_args(["--apply", "--confirmed"])
        self.assertTrue(args.apply)
        self.assertTrue(args.confirmed)


class TestMaybeCheckMergeQueue(unittest.TestCase):
    def test_checks_all_prs_regardless_of_labels(self):
        class StubFetcher:
            def __init__(self):
                self.calls = []

            def is_in_merge_queue(self, repo, number, *, base_branch=None):
                self.calls.append((repo, number, base_branch))
                return number == 1

        stub = StubFetcher()
        items = [
            {"kind": "pull", "repo": "a/b", "number": 1, "labels": ["requires-manual-review"]},
            {
                "kind": "pull",
                "repo": "a/b",
                "number": 2,
                "labels": ["ready-for-merge"],
                "base_ref_name": "release",
            },
            {"kind": "issue", "repo": "a/b", "number": 3, "labels": ["ready-for-merge"]},
        ]
        maybe_check_merge_queue(items, stub)
        self.assertTrue(items[0]["in_merge_queue"])
        self.assertFalse(items[1]["in_merge_queue"])
        # Issues are skipped.
        self.assertNotIn("in_merge_queue", items[2])
        # Both PRs checked (regardless of labels), issue skipped.
        self.assertEqual(len(stub.calls), 2)
        self.assertIn(("a/b", 1, None), stub.calls)
        self.assertIn(("a/b", 2, "release"), stub.calls)


class TestFetchErrors(unittest.TestCase):
    def test_build_queue_records_fetch_errors(self):
        from nextwork import FetchError

        class FlakyFetcher:
            def fetch_item(self, repo, number):
                if number == 1:
                    raise FetchError(repo, number)
                return make_issue(number=number, assignees=["alice"], labels=["triaged"])

        results, _remaining, fetch_errors = build_queue(
            [("acme/widget", 1), ("acme/widget", 2)],
            FlakyFetcher(),
            "alice",
            6,
            NOW,
            quiet=True,
        )
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["number"], 2)
        self.assertEqual(
            fetch_errors,
            [{"repo": "acme/widget", "number": 1, "detail": "GraphQL/API failure"}],
        )

    def test_json_includes_fetch_errors(self):
        out = format_json_output(
            [],
            "a/b",
            "alice",
            6,
            [],
            include_text=False,
            fetch_errors=[{"repo": "a/b", "number": 9, "detail": "GraphQL/API failure"}],
        )
        payload = json.loads(out)
        self.assertEqual(payload["fetch_errors"][0]["number"], 9)


class TestApplyContinuesOnError(unittest.TestCase):
    @patch("nextwork.run_gh_soft", side_effect=[None, ""])
    def test_records_error_and_continues(self, _mock_soft):
        items = [
            {
                "kind": "issue",
                "repo": "a/b",
                "number": 1,
                "status": "needs_assign",
                "eliminated": False,
                "suggested_actions": [ASSIGN_SELF],
            },
            {
                "kind": "issue",
                "repo": "a/b",
                "number": 2,
                "status": "needs_assign",
                "eliminated": False,
                "suggested_actions": [ASSIGN_SELF],
            },
        ]
        applied = apply_trivial_actions(items, "alice")
        self.assertEqual(applied[0]["action"], "error")
        self.assertEqual(applied[1]["action"], ASSIGN_SELF)


class TestJsonTruncationFlag(unittest.TestCase):
    def test_truncated_fields(self):
        out = format_json_output(
            [],
            "a/b",
            "alice",
            6,
            [],
            include_text=False,
            truncated_remaining=3,
        )
        payload = json.loads(out)
        self.assertTrue(payload["truncated"])
        self.assertEqual(payload["truncated_remaining"], 3)


class TestUnresolvedThreadsInOutput(unittest.TestCase):
    def test_json_includes_unresolved_threads(self):
        items = [
            {
                "kind": "pull",
                "repo": "acme/widget",
                "number": 99,
                "title": "A PR",
                "url": "https://github.com/acme/widget/pull/99",
                "status": "needs_review_decision",
                "eliminated": False,
                "reason": "2 unresolved review conversation(s) need a human decision",
                "suggested_actions": [],
                "blockers": [],
                "unresolved_threads": [
                    {
                        "id": "PRRT_1",
                        "author": "fullsend-ai-review[bot]",
                        "authors": ["fullsend-ai-review[bot]"],
                        "path": "internal/handler.go",
                        "line": 42,
                        "body": "Error return value is not checked.",
                        "created_at": "2024-01-02T01:00:00Z",
                        "bot_only": True,
                    },
                    {
                        "id": "PRRT_2",
                        "author": "carol",
                        "authors": ["carol"],
                        "path": "internal/server.go",
                        "line": 10,
                        "body": "This function is too long.",
                        "created_at": "2024-01-02T02:00:00Z",
                        "bot_only": False,
                    },
                ],
            }
        ]
        out = format_json_output(items, "acme/widget", "alice", 6, [], include_text=False)
        payload = json.loads(out)
        item = payload["items"][0]
        self.assertEqual(len(item["unresolved_threads"]), 2)
        self.assertEqual(item["unresolved_threads"][0]["id"], "PRRT_1")
        self.assertEqual(item["unresolved_threads"][0]["path"], "internal/handler.go")
        self.assertTrue(item["unresolved_threads"][0]["bot_only"])
        self.assertEqual(item["unresolved_threads"][1]["path"], "internal/server.go")
        self.assertFalse(item["unresolved_threads"][1]["bot_only"])

    def test_json_omits_empty_unresolved_threads(self):
        items = [
            {
                "kind": "pull",
                "repo": "acme/widget",
                "number": 99,
                "title": "A PR",
                "url": "https://github.com/acme/widget/pull/99",
                "status": "ready_to_merge",
                "eliminated": False,
                "reason": "Approved and ready to merge",
                "suggested_actions": [],
                "blockers": [],
                "unresolved_threads": [],
            }
        ]
        out = format_json_output(items, "acme/widget", "alice", 6, [], include_text=False)
        payload = json.loads(out)
        self.assertNotIn("unresolved_threads", payload["items"][0])

    def test_normalize_thread_without_id_omits_it(self):
        """Threads without id/path/line fields gracefully omit them."""
        node = load_fixture("pr_node_sample.json")
        # Remove optional fields from the thread node
        del node["reviewThreads"]["nodes"][1]["id"]
        del node["reviewThreads"]["nodes"][1]["path"]
        del node["reviewThreads"]["nodes"][1]["line"]
        item = normalize_item("acme/widget", node, quiet=True)
        thread = item["unresolved_threads"][0]
        self.assertNotIn("id", thread)
        self.assertNotIn("path", thread)
        self.assertNotIn("line", thread)
        # body still present from first comment
        self.assertIn("body", thread)


class TestResolveThreads(unittest.TestCase):
    @patch("nextwork.gh_graphql_or_none")
    def test_resolves_bot_only_threads(self, mock_gql):
        mock_gql.return_value = {"resolveReviewThread": {"thread": {"isResolved": True}}}
        items = [
            make_pr(
                unresolved_threads=[
                    {
                        "id": "PRRT_1",
                        "author": "fullsend-ai-review[bot]",
                        "authors": ["fullsend-ai-review[bot]"],
                        "path": "internal/handler.go",
                        "line": 42,
                        "body": "Error return value is not checked.",
                        "created_at": "2024-01-02T01:00:00Z",
                        "bot_only": True,
                    },
                ],
            ),
        ]
        results = resolve_threads(items)
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["action"], "resolved")
        self.assertEqual(results[0]["thread_id"], "PRRT_1")
        self.assertEqual(results[0]["thread_path"], "internal/handler.go")
        mock_gql.assert_called_once()

    @patch("nextwork.gh_graphql_or_none")
    def test_skips_human_threads(self, mock_gql):
        items = [
            make_pr(
                unresolved_threads=[
                    {
                        "id": "PRRT_1",
                        "author": "carol",
                        "authors": ["carol"],
                        "path": "internal/handler.go",
                        "created_at": "2024-01-02T01:00:00Z",
                        "bot_only": False,
                    },
                ],
            ),
        ]
        results = resolve_threads(items)
        self.assertEqual(results, [])
        mock_gql.assert_not_called()

    @patch("nextwork.gh_graphql_or_none")
    def test_skips_issues(self, mock_gql):
        items = [make_issue()]
        results = resolve_threads(items)
        self.assertEqual(results, [])
        mock_gql.assert_not_called()

    @patch("nextwork.gh_graphql_or_none", return_value=None)
    def test_records_error_on_mutation_failure(self, _mock_gql):
        items = [
            make_pr(
                unresolved_threads=[
                    {
                        "id": "PRRT_1",
                        "author": "fullsend-ai-review[bot]",
                        "authors": ["fullsend-ai-review[bot]"],
                        "path": "internal/handler.go",
                        "created_at": "2024-01-02T01:00:00Z",
                        "bot_only": True,
                    },
                ],
            ),
        ]
        results = resolve_threads(items)
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["action"], "error")
        self.assertIn("mutation failed", results[0]["detail"])

    def test_records_error_when_thread_has_no_id(self):
        items = [
            make_pr(
                unresolved_threads=[
                    {
                        "author": "fullsend-ai-review[bot]",
                        "authors": ["fullsend-ai-review[bot]"],
                        "path": "internal/handler.go",
                        "created_at": "2024-01-02T01:00:00Z",
                        "bot_only": True,
                    },
                ],
            ),
        ]
        results = resolve_threads(items)
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["action"], "error")
        self.assertIn("thread id not available", results[0]["detail"])

    @patch("nextwork.gh_graphql_or_none")
    def test_resolves_only_bot_threads_in_mixed_list(self, mock_gql):
        mock_gql.return_value = {"resolveReviewThread": {"thread": {"isResolved": True}}}
        items = [
            make_pr(
                unresolved_threads=[
                    {
                        "id": "PRRT_1",
                        "author": "fullsend-ai-review[bot]",
                        "authors": ["fullsend-ai-review[bot]"],
                        "path": "internal/handler.go",
                        "created_at": "2024-01-02T01:00:00Z",
                        "bot_only": True,
                    },
                    {
                        "id": "PRRT_2",
                        "author": "carol",
                        "authors": ["carol"],
                        "path": "internal/server.go",
                        "created_at": "2024-01-02T02:00:00Z",
                        "bot_only": False,
                    },
                ],
            ),
        ]
        results = resolve_threads(items)
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["action"], "resolved")
        self.assertEqual(results[0]["thread_path"], "internal/handler.go")
        mock_gql.assert_called_once()


class TestResolveThreadsArgs(unittest.TestCase):
    def test_resolve_threads_requires_confirmed(self):
        with self.assertRaises(SystemExit) as ctx:
            parse_args(["--resolve-threads"])
        self.assertEqual(ctx.exception.code, 2)

    def test_resolve_threads_with_confirmed_ok(self):
        args = parse_args(["--resolve-threads", "--confirmed"])
        self.assertTrue(args.resolve_threads)
        self.assertTrue(args.confirmed)


class TestResolveResultsInOutput(unittest.TestCase):
    def test_json_includes_resolve_results(self):
        resolve_results = [
            {
                "repo": "acme/widget",
                "number": 99,
                "thread_id": "PRRT_1",
                "thread_path": "internal/handler.go",
                "action": "resolved",
                "detail": "resolved bot-only thread at internal/handler.go",
            }
        ]
        out = format_json_output(
            [],
            "acme/widget",
            "alice",
            6,
            [],
            include_text=False,
            resolve_results=resolve_results,
        )
        payload = json.loads(out)
        self.assertEqual(len(payload["resolve_results"]), 1)
        self.assertEqual(payload["resolve_results"][0]["action"], "resolved")

    def test_markdown_includes_resolve_section(self):
        resolve_results = [
            {
                "repo": "acme/widget",
                "number": 99,
                "thread_id": "PRRT_1",
                "thread_path": "internal/handler.go",
                "action": "resolved",
                "detail": "resolved bot-only thread at internal/handler.go",
            }
        ]
        out = format_markdown_output(
            [],
            "acme/widget",
            "alice",
            6,
            [],
            show_blocked=False,
            resolve_results=resolve_results,
        )
        self.assertIn("## Resolved threads", out)
        self.assertIn("internal/handler.go", out)
        self.assertIn("resolved", out)


if __name__ == "__main__":
    unittest.main()
