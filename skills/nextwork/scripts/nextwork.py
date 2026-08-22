#!/usr/bin/env python3
"""Build a readiness-oriented queue of open issues/PRs via gh GraphQL (stdlib only).

Deterministic core for the `/nextwork` skill. Seeds a queue (assigned work or
explicit refs), follows open GitHub `blockedBy` links deepen-first (dependency
chains before unrelated seeds), classifies every item into a status catalog
(waiting on automation / blocked / assigned elsewhere / actionable), and
optionally applies trivial actions or persists prose-discovered blockers as
real GitHub dependency links.

See skills/nextwork/SKILL.md for the full flag reference and skill loop.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from collections import deque
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, NoReturn, Protocol

# --- Shared regex / link-parsing helpers (copied from skills/topissues/scripts/topissues.py) ---

PR_ISSUE_RE = re.compile(
    r"\b(?:close[sd]?|fix(?:es|ed)?|resolve[sd]?|partial-fix)\s+#(\d+)\b",
    re.IGNORECASE,
)

REF_URL_RE = re.compile(
    r"^https?://github\.com/([^/\s]+/[^/\s]+)/(?:issues|pull)/(\d+)/?(?:[/?#].*)?$"
)
REF_REPO_HASH_RE = re.compile(r"^([^/#\s]+/[^/#\s]+)#(\d+)$")
REF_BARE_RE = re.compile(r"^#?(\d+)$")

# Control labels that indicate an issue is already on a known automation path.
# Note: "blocked" is intentionally omitted — the label alone does not change
# readiness; only open structured blockedBy links yield blocked_by.
ISSUE_CONTROL_LABELS = {
    "needs-info",
    "needs-breakdown",
    "needs-design",
    "workflow-blocked",
    "ready-to-code",
    "triaged",
    "duplicate",
    "ready-for-triage",
    "question",
}

# Statuses whose next action is a single trivial gh mutation (assign or slash comment).
TRIVIAL_STATUSES = {"needs_assign", "needs_triage", "trigger_code", "trigger_review", "trigger_fix"}

# Trivial side-actions (orthogonal to primary status).
ASSIGN_SELF = "assign:self"
# Remove an orphaned blocked label (no open structured blockers).
REMOVE_BLOCKED_LABEL = "remove-label:blocked"

SLASH_COMMAND_BY_STATUS = {
    "needs_triage": "/fs-triage",
    "trigger_code": "/fs-code",
    "trigger_review": "/fs-review",
    "trigger_fix": "/fs-fix",
}

BODY_TRUNCATE_CHARS = 1000
COMMENT_TRUNCATE_CHARS = 500
INCLUDE_TEXT_COMMENT_COUNT = 3

MAX_QUEUE_VISITS = 100
# Cap open-PR pagination used for issue↔PR linking (100 nodes per page).
MAX_OPEN_PR_PAGES_FOR_LINKING = 5
# Soft page sizes for ITEM_QUERY connections (not paginated; full page ⇒ possible truncation).
COMMENTS_PAGE_SIZE = 50
BLOCKERS_PAGE_SIZE = 20
SUB_ISSUES_PAGE_SIZE = 50
REVIEW_THREADS_PAGE_SIZE = 50
# Comments scanned per unresolved review thread when detecting human replies.
REVIEW_THREAD_COMMENTS_PAGE_SIZE = 20


# ------------------------------- Ref parsing -------------------------------


class RefError(ValueError):
    """Raised when a CLI-supplied item reference cannot be parsed."""


def parse_ref(text: str, default_repo: str | None = None) -> tuple[str, int]:
    """Parse `owner/repo#N`, `#N`, `N`, or a GitHub issue/PR URL into (repo, number)."""
    text = text.strip()
    match = REF_URL_RE.match(text)
    if match:
        return match.group(1), int(match.group(2))
    match = REF_REPO_HASH_RE.match(text)
    if match:
        return match.group(1), int(match.group(2))
    match = REF_BARE_RE.match(text)
    if match:
        if not default_repo:
            raise RefError(f"cannot resolve bare ref {text!r} without --repo")
        return default_repo, int(match.group(1))
    raise RefError(f"cannot parse ref: {text!r}")


def format_ref(repo: str, number: int) -> str:
    return f"{repo}#{number}"


# ------------------------------- Time helpers -------------------------------


def parse_iso(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def created_at_key(value: str | None) -> datetime:
    """Sort/compare key for ISO timestamps (empty sorts earliest)."""
    if not value:
        return datetime.min.replace(tzinfo=UTC)
    return parse_iso(value)


def hours_since(iso_value: str | None, now: datetime) -> float:
    if not iso_value:
        return 0.0
    return (now - parse_iso(iso_value)).total_seconds() / 3600.0


def is_stale(iso_value: str | None, stale_hours: float, now: datetime) -> bool:
    if not iso_value:
        return False
    return hours_since(iso_value, now) >= stale_hours


# ------------------------- Linked-PR helpers (from topissues) -------------------------


def parse_pr_links(body: str | None, closing_issue_numbers: list[int]) -> set[int]:
    """Collect issue numbers linked from a PR body and closing-issue refs."""
    linked = set(closing_issue_numbers)
    if body:
        for match in PR_ISSUE_RE.finditer(body):
            linked.add(int(match.group(1)))
    return linked


def build_pr_links_by_issue(pulls: list[dict[str, Any]]) -> dict[int, list[int]]:
    """Map issue number -> sorted list of open PR numbers that reference it."""
    by_issue: dict[int, set[int]] = {}
    for pr in pulls:
        pr_number = pr["number"]
        closing = [
            node["number"] for node in pr.get("closingIssuesReferences", {}).get("nodes", [])
        ]
        for issue_num in parse_pr_links(pr.get("body"), closing):
            by_issue.setdefault(issue_num, set()).add(pr_number)
    return {k: sorted(v) for k, v in by_issue.items()}


def parse_open_blockers(blocked_by: dict[str, Any] | None) -> list[dict[str, Any]]:
    """Return open issues/PRs that block this item (GitHub blockedBy links)."""
    blockers: list[dict[str, Any]] = []
    for node in (blocked_by or {}).get("nodes", []):
        if node.get("state") != "OPEN":
            continue
        repo = (node.get("repository") or {}).get("nameWithOwner", "")
        blockers.append({"repo": repo, "number": node["number"]})
    return blockers


# HTML markers from internal/statuscomment — durable signal that an agent run is live.
AGENT_STATUS_MARKER = "fullsend:agent-status:"
AGENT_TERMINAL_MARKER = "fullsend:status:terminal"
# Sticky result posts (post-triage / post-review / prioritize), not human discussion.
AGENT_RESULT_MARKER_RE = re.compile(r"fullsend:[a-z0-9-]+-agent\b")
# Sticky triage summary (older runs may lack a terminal agent-status comment).
TRIAGE_RESULT_MARKER = "fullsend:triage-agent"

# Structured status-comment role prefix (internal/statuscomment startBodyRe / Finished line).
# Matches "🤖 Review · …" or "🤖 Finished Code · …" — not free-text skip reasons later in the body.
_ROLE_PREFIX_RE = re.compile(
    r"🤖\s+(?:Finished\s+)?(Review|Fix|Code|Triage)\b",
    re.IGNORECASE,
)
_ROLE_TO_WAITING = {
    "review": "waiting_review",
    "fix": "waiting_fix",
    "code": "waiting_code",
    "triage": "waiting_triage",
}

_INFLIGHT_REASON = {
    "waiting_review": "Review agent run in progress (non-terminal status comment)",
    "waiting_fix": "Fix agent run in progress (non-terminal status comment)",
    "waiting_code": "Code agent run in progress (non-terminal status comment)",
    "waiting_triage": "Triage agent run in progress (non-terminal status comment)",
    "waiting_agent": "Agent run in progress (non-terminal status comment)",
}

# waiting_* → (actionable status, slash action, stale reason)
_WAITING_TO_TRIGGER: dict[str, tuple[str, str, str]] = {
    "waiting_triage": (
        "needs_triage",
        "comment:/fs-triage",
        "Stale triage agent start; re-trigger",
    ),
    "waiting_code": (
        "trigger_code",
        "comment:/fs-code",
        "Stale code agent start; re-trigger",
    ),
    "waiting_review": (
        "trigger_review",
        "comment:/fs-review",
        "Stale review agent start; re-trigger",
    ),
    "waiting_fix": (
        "trigger_fix",
        "comment:/fs-fix",
        "Stale fix agent start; re-trigger",
    ),
}

# Launch label / slash → waiting / re-trigger (mirrors reusable-dispatch stages).
_LAUNCH_SPEC: dict[str, dict[str, str]] = {
    "triage": {
        "label": "ready-for-triage",
        "command": "/fs-triage",
        "waiting": "waiting_triage",
        "trigger": "needs_triage",
        "waiting_reason": "Waiting for triage automation",
        "stale_reason": "Stale triage launch wait; re-trigger",
    },
    "code": {
        "label": "ready-to-code",
        "command": "/fs-code",
        "waiting": "waiting_code",
        "trigger": "trigger_code",
        "waiting_reason": "Waiting for the code agent",
        "stale_reason": "Stale ready-to-code / /fs-code wait; re-trigger",
    },
    "review": {
        "label": "ready-for-review",
        "command": "/fs-review",
        "waiting": "waiting_review",
        "trigger": "trigger_review",
        "waiting_reason": "Waiting for review",
        "stale_reason": "Stale review launch wait; re-trigger",
    },
    "fix": {
        "label": "",
        "command": "/fs-fix",
        "waiting": "waiting_fix",
        "trigger": "trigger_fix",
        "waiting_reason": "Waiting for the fix agent",
        "stale_reason": "Stale fix launch wait; re-trigger",
    },
}

REVIEW_BOT_LOGIN = "fullsend-ai-review"
CODER_BOT_LOGIN = "fullsend-ai-coder"
TRIAGE_BOT_LOGIN = "fullsend-ai-triage"
RETRO_BOT_LOGIN = "fullsend-ai-retro"
PRIORITIZE_BOT_LOGIN = "fullsend-ai-prioritize"
# Only trust agent-status / sticky result markers from these bot logins.
FULLSEND_AGENT_BOTS = frozenset(
    {
        REVIEW_BOT_LOGIN,
        CODER_BOT_LOGIN,
        TRIAGE_BOT_LOGIN,
        RETRO_BOT_LOGIN,
        PRIORITIZE_BOT_LOGIN,
    }
)


def thread_is_bot_only(thread: dict[str, Any]) -> bool:
    """True when an unresolved review thread has no human comments (fix-eligible)."""
    if "bot_only" in thread:
        return bool(thread["bot_only"])
    authors = thread.get("authors")
    if authors is not None:
        return bool(authors) and all(a == REVIEW_BOT_LOGIN for a in authors)
    return thread.get("author") == REVIEW_BOT_LOGIN


TRIAGE_STALE_HOURS = 3 * 24
# Post-triage conversation only invalidates after this age (avoids noise right after triage).
TRIAGE_COMMENT_GRACE_HOURS = 6.0
# GitHub StatusState for statusCheckRollup.state (no IN_PROGRESS/QUEUED on this enum).
CHECKS_PENDING = frozenset({"PENDING", "EXPECTED"})
CHECKS_FAILED = frozenset({"FAILURE", "ERROR"})
# Comment.authorAssociation values treated as plausible /fs-* launchers.
# Approximation only — not a live permission check (see _is_trusted_fs_commenter).
TRUSTED_FS_ASSOCIATIONS = frozenset({"OWNER", "MEMBER", "COLLABORATOR"})


class FetchError(Exception):
    """Raised when a per-item GraphQL/API fetch fails (distinct from missing/closed)."""

    def __init__(self, repo: str, number: int, detail: str = "GraphQL/API failure"):
        self.repo = repo
        self.number = number
        self.detail = detail
        super().__init__(f"{repo}#{number}: {detail}")


def comment_command(body: str | None) -> str:
    """First whitespace token of the first line — mirrors extractCommentCommand / dispatch.yml."""
    if not body:
        return ""
    first_line = body.split("\n", 1)[0].replace("\r", "").strip()
    if not first_line:
        return ""
    return first_line.split(None, 1)[0]


def _is_agent_bot_comment(comment: dict[str, Any]) -> bool:
    return (comment.get("author") or "") in FULLSEND_AGENT_BOTS


def _is_trusted_fs_commenter(comment: dict[str, Any]) -> bool:
    """True when a /fs-* launch comment should count as a real launch signal.

    Approximation of dispatch's write boundary using Comment.authorAssociation
    (OWNER/MEMBER/COLLABORATOR) plus fullsend agent bots. This is *not* a live
    ``collaborators/<user>/permission`` check: GitHub's COLLABORATOR association
    includes read-only collaborators, while reusable-dispatch requires write (or
    triage for some stages). Bounded by ``--stale-hours`` rather than permanent.
    """
    author = comment.get("author") or ""
    if author in FULLSEND_AGENT_BOTS:
        return True
    assoc = comment.get("author_association") or ""
    return assoc in TRUSTED_FS_ASSOCIATIONS


def agent_terminal_succeeded(body: str) -> bool:
    """True when a terminal agent-status body reports success.

    Failed/cancelled/terminated/skipped runs must not clear launch waits. Sticky
    triage results without an explicit outcome are treated as success (legacy).
    """
    lower = body.lower()
    if "❌" in body or "⏭️" in body:
        return False
    if "terminated" in lower or "cancelled" in lower or "canceled" in lower:
        return False
    if re.search(r"\b(?:failure|failed|skipped)\b", lower):
        return False
    if "✅" in body or re.search(r"\bsuccess\b", lower):
        return True
    # Sticky triage / marker-only posts with no outcome line.
    return True


def parse_inflight_agent(comments: list[dict[str, Any]]) -> str | None:
    """Return a waiting_* status if the latest agent-status comment is non-terminal.

    Prefer classify_inflight_agent when stale-hours re-invoke is needed.
    """
    latest = latest_agent_status(comments)
    if latest is None or latest["terminal"]:
        return None
    return latest["waiting_status"]


def _role_waiting_status(body: str) -> str:
    """Map an agent-status body to waiting_* using the structured 🤖 role prefix only."""
    match = _ROLE_PREFIX_RE.search(body)
    if match:
        return _ROLE_TO_WAITING[match.group(1).lower()]
    return "waiting_agent"


def latest_agent_status(comments: list[dict[str, Any]]) -> dict[str, Any] | None:
    """Chronologically latest bot-authored agent-status comment."""
    agent_comments = [
        c
        for c in comments
        if _is_agent_bot_comment(c) and AGENT_STATUS_MARKER in (c.get("body") or "")
    ]
    if not agent_comments:
        return None
    latest = max(
        enumerate(agent_comments),
        key=lambda ic: (created_at_key(ic[1].get("created_at")), ic[0]),
    )[1]
    body = latest.get("body") or ""
    return {
        "created_at": latest.get("created_at") or "",
        "body": body,
        "terminal": AGENT_TERMINAL_MARKER in body,
        "succeeded": agent_terminal_succeeded(body) if AGENT_TERMINAL_MARKER in body else False,
        "waiting_status": _role_waiting_status(body),
        "author": latest.get("author"),
    }


def latest_terminal_agent(
    comments: list[dict[str, Any]], waiting_status: str
) -> dict[str, Any] | None:
    """Latest successful terminal agent-status for waiting_status (bot-authored only)."""
    matches = []
    for c in comments:
        if not _is_agent_bot_comment(c):
            continue
        body = c.get("body") or ""
        if AGENT_STATUS_MARKER not in body or AGENT_TERMINAL_MARKER not in body:
            continue
        if not agent_terminal_succeeded(body):
            continue
        if _role_waiting_status(body) == waiting_status:
            matches.append(c)
    if not matches:
        return None
    latest = max(
        enumerate(matches),
        key=lambda ic: (created_at_key(ic[1].get("created_at")), ic[0]),
    )[1]
    return {
        "created_at": latest.get("created_at") or "",
        "body": latest.get("body") or "",
        "terminal": True,
        "succeeded": True,
        "waiting_status": waiting_status,
        "author": latest.get("author"),
    }


def latest_completed_triage(comments: list[dict[str, Any]]) -> dict[str, Any] | None:
    """Latest successful triage completion: terminal agent-status or sticky marker.

    Sticky ``<!-- fullsend:triage-agent -->`` posts are a completion signal for
    older runs that never left a terminal agent-status comment. They are not
    used for in-flight detection. Only bot-authored comments count. When both
    exist, the chronologically later signal wins.
    """
    candidates: list[dict[str, Any]] = []
    terminal = latest_terminal_agent(comments, "waiting_triage")
    if terminal is not None and terminal.get("created_at"):
        candidates.append(terminal)
    matches = [
        c
        for c in comments
        if _is_agent_bot_comment(c) and TRIAGE_RESULT_MARKER in (c.get("body") or "")
    ]
    if matches:
        latest = max(
            enumerate(matches),
            key=lambda ic: (created_at_key(ic[1].get("created_at")), ic[0]),
        )[1]
        candidates.append(
            {
                "created_at": latest.get("created_at") or "",
                "body": latest.get("body") or "",
                "terminal": True,
                "succeeded": True,
                "waiting_status": "waiting_triage",
                "author": latest.get("author"),
            }
        )
    if not candidates:
        return None
    return max(
        enumerate(candidates),
        key=lambda ic: (created_at_key(ic[1].get("created_at")), ic[0]),
    )[1]


def latest_fs_command_at(comments: list[dict[str, Any]], command: str) -> str | None:
    """created_at of the latest trusted comment whose first-line command equals command."""
    matches = [
        c
        for c in comments
        if _is_trusted_fs_commenter(c) and comment_command(c.get("body") or "") == command
    ]
    if not matches:
        return None
    return max(matches, key=lambda c: created_at_key(c.get("created_at"))).get("created_at")


def launch_signal_at(
    item: dict[str, Any],
    role: str,
    comments: list[dict[str, Any]],
    *,
    extra_label: bool = False,
) -> str | None:
    """ISO timestamp when the agent was asked to run, or None if there is no launch signal.

    Prefer an explicit trusted ``/fs-*`` comment. When only a control label is
    present (or ``extra_label``), fall back to ``item.updated_at``. That clock
    resets on any subsequent activity (comments, edits, other labels) — not just
    label application — for every role that uses this helper (triage/code/review).
    """
    spec = _LAUNCH_SPEC[role]
    cmd_at = latest_fs_command_at(comments, spec["command"])
    if cmd_at:
        return cmd_at
    label = spec["label"]
    if label and label in item.get("labels", []):
        return item.get("updated_at")
    if extra_label:
        return item.get("updated_at")
    return None


def classify_inflight_agent(
    comments: list[dict[str, Any]], stale_hours: float, now: datetime
) -> Classification | None:
    """Non-terminal agent-status → waiting_* unless the start comment is past stale_hours."""
    latest = latest_agent_status(comments)
    if latest is None or latest["terminal"]:
        return None
    waiting = latest["waiting_status"]
    if is_stale(latest["created_at"], stale_hours, now):
        mapped = _WAITING_TO_TRIGGER.get(waiting)
        if mapped:
            status, action, reason = mapped
            return Classification(
                status=status,
                reason=reason,
                eliminated=False,
                suggested_actions=[action],
            )
        return Classification(
            status=waiting,
            reason=_INFLIGHT_REASON.get(waiting, _INFLIGHT_REASON["waiting_agent"]),
            eliminated=True,
        )
    return Classification(
        status=waiting,
        reason=_INFLIGHT_REASON.get(waiting, _INFLIGHT_REASON["waiting_agent"]),
        eliminated=True,
    )


def classify_launch_wait(
    item: dict[str, Any],
    role: str,
    comments: list[dict[str, Any]],
    stale_hours: float,
    now: datetime,
    *,
    signal_at: str | None = None,
) -> Classification | None:
    """Label and/or /fs-* asked for an agent that has not started yet."""
    spec = _LAUNCH_SPEC[role]
    at = signal_at if signal_at is not None else launch_signal_at(item, role, comments)
    if not at:
        return None
    # A matching non-terminal start is handled by classify_inflight_agent.
    latest = latest_agent_status(comments)
    if latest and not latest["terminal"] and latest["waiting_status"] == spec["waiting"]:
        return None
    # Slash/label launch already satisfied by a completed agent for this role.
    # Without this, a fresh /fs-* comment keeps waiting_* forever (until stale
    # hours flip it to a re-trigger) even after a successful terminal status.
    # Triage also accepts sticky <!-- fullsend:triage-agent --> when status is absent.
    if role == "triage":
        completed = latest_completed_triage(comments)
    else:
        completed = latest_terminal_agent(comments, spec["waiting"])
    if (
        completed
        and completed["created_at"]
        and created_at_key(completed["created_at"]) >= created_at_key(at)
    ):
        return None
    if is_stale(at, stale_hours, now):
        return Classification(
            status=spec["trigger"],
            reason=spec["stale_reason"],
            eliminated=False,
            suggested_actions=[f"comment:{spec['command']}"],
        )
    return Classification(
        status=spec["waiting"],
        reason=spec["waiting_reason"],
        eliminated=True,
    )


def is_completed_triage_stale(
    comments: list[dict[str, Any]],
    now: datetime,
    *,
    triage_stale_hours: float = TRIAGE_STALE_HOURS,
    comment_grace_hours: float = TRIAGE_COMMENT_GRACE_HOURS,
) -> bool:
    """Completed Triage older than triage_stale_hours, or aged post-triage comments.

    Completion is a terminal Triage agent-status, or a sticky
    ``<!-- fullsend:triage-agent -->`` result when status is missing.

    Non-exempt comments after completion only invalidate once they themselves
    are at least ``comment_grace_hours`` old, so ordinary conversation noise
    right after triage does not immediately flip to ``needs_triage``.
    """
    completed = latest_completed_triage(comments)
    if completed is None or not completed["created_at"]:
        return False
    if hours_since(completed["created_at"], now) >= triage_stale_hours:
        return True
    triage_at = completed["created_at"]
    for c in comments:
        created = c.get("created_at") or ""
        if created_at_key(created) <= created_at_key(triage_at):
            continue
        if hours_since(created, now) < comment_grace_hours:
            continue
        body = c.get("body") or ""
        if AGENT_STATUS_MARKER in body:
            continue
        if AGENT_RESULT_MARKER_RE.search(body):
            continue
        # Launch/promote slash commands are handled by classify_launch_wait /
        # waiting_code — they must not themselves flip completed triage stale.
        cmd = comment_command(body)
        if cmd in ("/fs-code", "/fs-triage", "/fs-review", "/fs-fix"):
            continue
        return True
    return False


def is_non_stale_code_wait(
    item: dict[str, Any],
    comments: list[dict[str, Any]],
    stale_hours: float,
    now: datetime,
) -> bool:
    """True when we are waiting on code and that wait is not yet stale."""
    latest = latest_agent_status(comments)
    if (
        latest
        and not latest["terminal"]
        and latest["waiting_status"] == "waiting_code"
        and latest["created_at"]
        and not is_stale(latest["created_at"], stale_hours, now)
    ):
        return True
    sig = launch_signal_at(item, "code", comments)
    if not sig:
        return False
    if latest and not latest["terminal"] and latest["waiting_status"] == "waiting_code":
        return False  # stale in-flight handled elsewhere
    terminal = latest_terminal_agent(comments, "waiting_code")
    if (
        terminal
        and terminal["created_at"]
        and created_at_key(terminal["created_at"]) >= created_at_key(sig)
    ):
        return False  # /fs-code or ready-to-code already completed
    return not is_stale(sig, stale_hours, now)


def has_newer_code_than_review(item: dict[str, Any], comments: list[dict[str, Any]]) -> bool:
    """True when head commits landed after the latest review signal (bot or human)."""
    head_at = item.get("head_committed_at")
    if not head_at:
        return False
    review_at: str | None = None
    bot_review = latest_terminal_agent(comments, "waiting_review")
    if bot_review and bot_review.get("created_at"):
        review_at = bot_review["created_at"]
    human_at = item.get("latest_approved_review_at")
    if human_at and (review_at is None or created_at_key(human_at) > created_at_key(review_at)):
        review_at = human_at
    if not review_at:
        return False
    return parse_iso(head_at) > parse_iso(review_at)


# ------------------------------- Classification -------------------------------


@dataclass
class Classification:
    status: str
    reason: str
    eliminated: bool
    blockers: list[dict[str, Any]] = field(default_factory=list)
    linked_prs: list[int] = field(default_factory=list)
    open_sub_issues: list[dict[str, Any]] = field(default_factory=list)
    suggested_actions: list[str] = field(default_factory=list)


def classify_issue(
    item: dict[str, Any],
    user: str,
    stale_hours: float,
    now: datetime,
    *,
    resolve_linked_prs: Callable[[], list[int]] | None = None,
    triage_stale_hours: float = TRIAGE_STALE_HOURS,
) -> Classification | None:
    """Classify a normalized open issue. Returns None if it should be dropped entirely."""
    labels = set(item["labels"])
    assignees = item["assignees"]
    comments = item.get("comments") or []

    if "duplicate" in labels:
        return None

    if item["blockers"]:
        return Classification(
            status="blocked_by",
            reason="Blocked by open issue(s)/PR(s)",
            eliminated=True,
            blockers=item["blockers"],
        )

    if assignees and user not in assignees:
        return Classification(
            status="assigned_elsewhere",
            reason=f"Assigned to {', '.join(sorted(assignees))}",
            eliminated=True,
        )

    inflight = classify_inflight_agent(comments, stale_hours, now)
    if inflight:
        return inflight

    open_subs = item.get("open_sub_issues") or []
    if open_subs:
        refs = ", ".join(f"#{s['number']}" for s in open_subs)
        return Classification(
            status="waiting_sub_issues",
            reason=f"Open sub-issue(s): {refs}",
            eliminated=True,
            open_sub_issues=open_subs,
        )

    sub_total = item.get("sub_issues_total") or 0
    if sub_total > 0:
        sub_completed = item.get("sub_issues_completed") or 0
        # Prefer summary totals over the capped subIssues page: open children may
        # sit past first:50 even when the page has no OPEN nodes.
        if sub_completed < sub_total:
            return Classification(
                status="waiting_sub_issues",
                reason=(
                    f"Sub-issues still open ({sub_completed}/{sub_total} completed; "
                    "open children may be beyond the first page)"
                ),
                eliminated=True,
                open_sub_issues=[],
            )
        return Classification(
            status="close_or_plan",
            reason="All sub-issues are closed; close this issue or plan further work",
            eliminated=False,
            suggested_actions=[
                "decision: close this issue, or plan further work / open new sub-issues"
            ],
        )

    linked_prs = item.get("linked_prs") or []
    if not linked_prs and resolve_linked_prs is not None:
        linked_prs = resolve_linked_prs()
        item["linked_prs"] = linked_prs
    if linked_prs:
        refs = ", ".join(f"#{n}" for n in linked_prs)
        return Classification(
            status="waiting_linked_pr",
            reason=f"Open linked PR(s): {refs}",
            eliminated=True,
            linked_prs=linked_prs,
        )

    if "needs-info" in labels:
        if item["author"] == user:
            return Classification(
                status="needs_info_self",
                reason="Needs-info; you are the author",
                eliminated=False,
                suggested_actions=["Provide the requested information or edit the issue body"],
            )
        return Classification(
            status="waiting_info_other",
            reason="Needs-info; waiting on the reporter",
            eliminated=True,
        )

    if "needs-breakdown" in labels:
        return Classification(
            status="needs_breakdown",
            reason="Needs breakdown into smaller issues",
            eliminated=False,
            suggested_actions=["Break this issue into smaller sub-issues"],
        )

    if "needs-design" in labels:
        return Classification(
            status="needs_design",
            reason="Needs design work before code",
            eliminated=False,
            suggested_actions=["Add the missing design details"],
        )

    if "workflow-blocked" in labels:
        return Classification(
            status="workflow_blocked",
            reason="Requires workflow changes the code agent cannot push",
            eliminated=False,
            suggested_actions=["Implement locally; the code agent cannot push workflow changes"],
        )

    # Non-stale code wait wins over stale completed triage.
    if is_non_stale_code_wait(item, comments, stale_hours, now):
        return Classification(
            status="waiting_code",
            reason="Waiting for the code agent",
            eliminated=True,
        )

    if is_completed_triage_stale(
        comments,
        now,
        triage_stale_hours=triage_stale_hours,
        comment_grace_hours=stale_hours,
    ):
        completed = latest_completed_triage(comments)
        triage_launch = launch_signal_at(item, "triage", comments)
        # A newer /fs-triage (or triage label signal) after completion means a
        # re-launch is already in flight — do not flip back to needs_triage.
        if (
            triage_launch
            and completed
            and completed.get("created_at")
            and created_at_key(triage_launch) > created_at_key(completed["created_at"])
        ):
            launch = classify_launch_wait(
                item, "triage", comments, stale_hours, now, signal_at=triage_launch
            )
            if launch:
                return launch
        # Stale ready-to-code / /fs-code should surface as trigger_code, not
        # re-triage — code launch wait wins over the age-only triage flip.
        code_launch = classify_launch_wait(item, "code", comments, stale_hours, now)
        if code_launch:
            return code_launch
        return Classification(
            status="needs_triage",
            reason="Stale completed triage; re-trigger",
            eliminated=False,
            suggested_actions=["comment:/fs-triage"],
        )

    has_control_label = bool(labels & ISSUE_CONTROL_LABELS)
    triage_launch = launch_signal_at(item, "triage", comments)
    # Issue creation is the initial triage launch when nothing more explicit
    # exists yet. Control labels (triaged, ready-to-code, …) mean the issue is
    # already past that gate — do not drag them back via created_at.
    if triage_launch is None and not has_control_label:
        triage_launch = item.get("created_at")
    if triage_launch:
        launch = classify_launch_wait(
            item, "triage", comments, stale_hours, now, signal_at=triage_launch
        )
        if launch:
            return launch

    code_launch = classify_launch_wait(item, "code", comments, stale_hours, now)
    if code_launch:
        return code_launch

    if "triaged" in labels:
        return Classification(
            status="promote_code",
            reason="Triaged; needs a promotion decision (feature work)",
            eliminated=False,
            suggested_actions=[
                "decision: promote to ready-to-code, or comment:/fs-code once confirmed"
            ],
        )

    if not assignees:
        return Classification(
            status="needs_assign",
            reason="Unassigned; no automation signal",
            eliminated=False,
            suggested_actions=[ASSIGN_SELF],
        )

    return Classification(
        status="human_work",
        reason="Assigned; no waiting/blocked signal",
        eliminated=False,
        suggested_actions=["Implement directly, or comment:/fs-code if eligible"],
    )


def _classify_fix_from_threads(
    item: dict[str, Any],
    comments: list[dict[str, Any]],
    unresolved: list[dict[str, Any]],
    stale_hours: float,
    now: datetime,
) -> Classification:
    """All unresolved threads are from the review bot → fix launch wait / trigger."""
    if "fullsend-no-fix" in item.get("labels", []):
        return Classification(
            status="needs_review_decision",
            reason="Unresolved review-bot threads but fullsend-no-fix is set",
            eliminated=False,
            suggested_actions=[
                "comment:/fs-fix (fullsend-no-fix only blocks automatic bot runs), "
                "or resolve threads yourself"
            ],
        )
    cmd_at = latest_fs_command_at(comments, "/fs-fix")
    thread_times = [
        created_at for t in unresolved if (created_at := t.get("created_at")) is not None
    ]
    thread_at = max(thread_times, key=created_at_key) if thread_times else None
    signal_at = cmd_at or thread_at or item.get("updated_at")
    launch = classify_launch_wait(item, "fix", comments, stale_hours, now, signal_at=signal_at)
    if launch:
        return launch
    return Classification(
        status="trigger_fix",
        reason="Unresolved review-bot threads; run fix",
        eliminated=False,
        suggested_actions=["comment:/fs-fix"],
    )


def classify_pr(
    item: dict[str, Any], user: str, stale_hours: float, now: datetime
) -> Classification | None:
    """Classify a normalized open pull request. Returns None if it should be dropped."""
    labels = set(item["labels"])
    assignees = item["assignees"]
    comments = item.get("comments") or []

    if assignees and user not in assignees:
        return Classification(
            status="assigned_elsewhere",
            reason=f"Assigned to {', '.join(sorted(assignees))}",
            eliminated=True,
        )

    # Merge-queue membership is authoritative regardless of labels.
    if item.get("in_merge_queue"):
        return Classification(
            status="waiting_merge_queue",
            reason="Already enqueued in the merge queue",
            eliminated=True,
        )

    inflight = classify_inflight_agent(comments, stale_hours, now)
    if inflight:
        return inflight

    if item.get("merge_state_status") == "DIRTY" or item.get("mergeable") == "CONFLICTING":
        return Classification(
            status="fix_conflicts",
            reason="Merge conflicts must be resolved",
            eliminated=False,
            suggested_actions=["Resolve merge conflicts"],
        )

    if "requires-manual-review" in labels or "needs-human" in labels:
        return Classification(
            status="needs_review_decision",
            reason="Requires a manual review decision",
            eliminated=False,
            suggested_actions=["Review and decide the next step"],
        )

    checks_state = item.get("checks_state")
    checks_pending = checks_state in CHECKS_PENDING
    if checks_state in CHECKS_FAILED:
        return Classification(
            status="needs_review_decision",
            reason=f"Commit check rollup failed ({checks_state})",
            eliminated=False,
            suggested_actions=["Inspect failed CI and decide the next step"],
        )

    review_decision = item.get("review_decision")
    unresolved = item.get("unresolved_threads") or []
    unresolved_count = len(unresolved)
    merge_state = item.get("merge_state_status")
    merge_ready_states = {"CLEAN", "UNSTABLE"}

    if unresolved_count > 0:
        if all(thread_is_bot_only(t) for t in unresolved):
            return _classify_fix_from_threads(item, comments, unresolved, stale_hours, now)
        return Classification(
            status="needs_review_decision",
            reason=f"{unresolved_count} unresolved review conversation(s) need a human decision",
            eliminated=False,
            suggested_actions=[
                "Resolve threads, or paste human review feedback into a /fs-fix instruction"
            ],
        )

    if review_decision == "CHANGES_REQUESTED":
        reason = "Reviewer requested changes"
        if "ready-for-merge" in labels:
            reason += " (ready-for-merge label is present)"
        return Classification(
            status="needs_review_decision",
            reason=reason,
            eliminated=False,
            suggested_actions=["Address review feedback or discuss with the reviewer"],
        )

    if "ready-for-merge" in labels:
        if checks_pending:
            return Classification(
                status="waiting_ci",
                reason="ready-for-merge label present but commit checks are still running",
                eliminated=True,
            )
        if review_decision == "REVIEW_REQUIRED":
            pass  # fall through to review launch wait
        elif merge_state == "BLOCKED":
            return Classification(
                status="needs_review_decision",
                reason="Merge blocked by branch protection",
                eliminated=False,
                suggested_actions=["Satisfy branch protection (reviews, conversations, checks)"],
            )
        elif merge_state in merge_ready_states and item.get("mergeable") != "CONFLICTING":
            return Classification(
                status="ready_to_merge",
                reason="Approved and ready to merge",
                eliminated=False,
                suggested_actions=["Merge, or enqueue in the merge queue"],
            )
        else:
            state = merge_state or "unknown"
            return Classification(
                status="needs_review_decision",
                reason=f"ready-for-merge label present but merge state is {state}",
                eliminated=False,
                suggested_actions=["Inspect PR merge readiness on GitHub"],
            )

    # Drafts surface as human_work even while CI is still running — authors need
    # to see their own draft rather than having it hide under waiting_ci.
    if item.get("is_draft"):
        return Classification(
            status="human_work",
            reason="Draft PR; mark ready for review when done",
            eliminated=False,
            suggested_actions=["Mark ready for review when complete"],
        )

    if checks_pending:
        return Classification(
            status="waiting_ci",
            reason="Commit checks are still running",
            eliminated=True,
        )

    if has_newer_code_than_review(item, comments):
        return Classification(
            status="trigger_review",
            reason="Newer commits since last review; re-trigger",
            eliminated=False,
            suggested_actions=["comment:/fs-review"],
        )

    # Already approved: do not treat a leftover ready-for-review label as waiting.
    if review_decision == "APPROVED":
        return Classification(
            status="human_work",
            reason="Approved but not labeled ready-for-merge",
            eliminated=False,
            suggested_actions=["Add ready-for-merge when merge-ready, or merge if allowed"],
        )

    # Default review path: everything that reached here already cleared
    # blockers/CI/draft/approval/fix/threads. A missing or REVIEW_REQUIRED
    # decision therefore means "needs review" — use updated_at as the launch
    # clock so stale PRs surface as trigger_review rather than human_work.
    review_signal = launch_signal_at(item, "review", comments)
    if not review_signal and review_decision in (None, "REVIEW_REQUIRED"):
        review_signal = item.get("updated_at")
    if (
        review_signal
        or "ready-for-review" in labels
        or review_decision
        in (
            None,
            "REVIEW_REQUIRED",
        )
    ):
        launch = classify_launch_wait(
            item,
            "review",
            comments,
            stale_hours,
            now,
            signal_at=review_signal or item.get("updated_at"),
        )
        if launch:
            return launch
        return Classification(
            status="waiting_review",
            reason="Waiting for review",
            eliminated=True,
        )

    return Classification(
        status="human_work",
        reason="Open PR; no clear next action",
        eliminated=False,
        suggested_actions=["Investigate PR status manually"],
    )


def annotate_unassigned_assign_self(
    classification: Classification, item: dict[str, Any]
) -> Classification:
    """If actionable and unassigned, suggest self-assignment first."""
    if (
        not classification.eliminated
        and not (item.get("assignees") or [])
        and ASSIGN_SELF not in classification.suggested_actions
    ):
        classification.suggested_actions = [ASSIGN_SELF, *classification.suggested_actions]
    return classification


def annotate_orphaned_blocked_label(
    classification: Classification, item: dict[str, Any], user: str
) -> Classification:
    """If an Issue is labeled blocked but has no open structured blockers, suggest removal.

    PRs never get ``remove-label:blocked``: GitHub has no PR-side ``blockedBy``, so
    the label is the only way to mark a PR blocked and ``--link-blocker`` cannot
    replace it.

    Ownership: only suggest when the issue is unassigned or ``user`` is among
    assignees — never strip someone else's orphaned ``blocked`` label while
    walking a dependency chain.
    """
    if item.get("kind") != "issue":
        return classification
    assignees = item.get("assignees") or []
    if assignees and user not in assignees:
        return classification
    labels = item.get("labels") or []
    blockers = item.get("blockers") or []
    already = REMOVE_BLOCKED_LABEL in classification.suggested_actions
    if "blocked" in labels and not blockers and not already:
        classification.suggested_actions = [
            *classification.suggested_actions,
            REMOVE_BLOCKED_LABEL,
        ]
    return classification


def classify_item(
    item: dict[str, Any],
    user: str,
    stale_hours: float,
    now: datetime,
    *,
    resolve_linked_prs: Callable[[], list[int]] | None = None,
    triage_stale_hours: float = TRIAGE_STALE_HOURS,
) -> Classification | None:
    if item["kind"] == "issue":
        classification = classify_issue(
            item,
            user,
            stale_hours,
            now,
            resolve_linked_prs=resolve_linked_prs,
            triage_stale_hours=triage_stale_hours,
        )
    else:
        classification = classify_pr(item, user, stale_hours, now)
    if classification is None:
        return None
    classification = annotate_unassigned_assign_self(classification, item)
    return annotate_orphaned_blocked_label(classification, item, user)


# ------------------------------- gh CLI plumbing -------------------------------


def _gh_not_found() -> NoReturn:
    print("error: gh CLI not found; install https://cli.github.com/", file=sys.stderr)
    sys.exit(1)


def try_run_gh(args: list[str]) -> str | None:
    """Run gh and return stdout, or None if the command failed."""
    return run_gh_soft(args, quiet=True)


def run_gh_soft(args: list[str], *, quiet: bool = False) -> str | None:
    """Like run_gh, but return None on failure instead of exiting (except missing gh)."""
    try:
        result = subprocess.run(["gh", *args], check=True, capture_output=True, text=True)
    except FileNotFoundError:
        _gh_not_found()
    except subprocess.CalledProcessError as exc:
        if not quiet:
            if exc.stderr:
                print(exc.stderr.strip(), file=sys.stderr)
            if exc.stdout:
                print(exc.stdout.strip(), file=sys.stderr)
        return None
    return result.stdout.strip()


def run_gh(args: list[str], *, quiet: bool = False) -> str:
    out = run_gh_soft(args, quiet=quiet)
    if out is None:
        sys.exit(3)
    return out


def graphql_var_flags(variables: dict[str, Any]) -> list[str]:
    """Build gh api graphql -f/-F flags. Int/bool/float must use -F (typed JSON)."""
    flags: list[str] = []
    for key, value in variables.items():
        if value is None:
            continue
        # -f always sends a string; GraphQL Int!/Boolean! reject coerced strings.
        if isinstance(value, bool):
            flags.extend(["-F", f"{key}={json.dumps(value)}"])
        elif isinstance(value, (int, float)) and not isinstance(value, bool):
            flags.extend(["-F", f"{key}={value}"])
        else:
            flags.extend(["-f", f"{key}={value}"])
    return flags


def gh_graphql(query: str, variables: dict[str, Any], *, quiet: bool = False) -> dict[str, Any]:
    args = ["api", "graphql", "-f", f"query={query}", *graphql_var_flags(variables)]
    raw = run_gh(args, quiet=quiet)
    data = json.loads(raw)
    if data.get("errors"):
        if not quiet:
            print(json.dumps(data["errors"], indent=2), file=sys.stderr)
        sys.exit(3)
    return data["data"]


def gh_graphql_or_none(
    query: str, variables: dict[str, Any], *, quiet: bool = False
) -> dict[str, Any] | None:
    """Like gh_graphql, but returns None on failure instead of exiting."""
    args = ["api", "graphql", "-f", f"query={query}", *graphql_var_flags(variables)]
    try:
        result = subprocess.run(["gh", *args], check=True, capture_output=True, text=True)
    except FileNotFoundError:
        _gh_not_found()
    except subprocess.CalledProcessError as exc:
        if not quiet:
            err = (exc.stderr or exc.stdout or "").strip()
            if err:
                print(err, file=sys.stderr)
        return None
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError:
        return None
    if data.get("errors"):
        if not quiet:
            print(json.dumps(data["errors"], indent=2), file=sys.stderr)
        return None
    return data.get("data")


def resolve_repo(override: str | None) -> str:
    if override:
        if "/" not in override or override.count("/") != 1:
            print(f"error: --repo must be owner/name, got: {override!r}", file=sys.stderr)
            sys.exit(2)
        return override
    try:
        result = subprocess.run(
            ["gh", "repo", "view", "--json", "nameWithOwner"],
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        _gh_not_found()
    except subprocess.CalledProcessError:
        print(
            "error: not inside a git repository known to gh; use --repo owner/name",
            file=sys.stderr,
        )
        sys.exit(1)
    repo = json.loads(result.stdout.strip())["nameWithOwner"]
    if not repo:
        print(
            "error: not inside a git repository known to gh; use --repo owner/name",
            file=sys.stderr,
        )
        sys.exit(1)
    return repo


def resolve_user(override: str | None, *, quiet: bool = False) -> str:
    if override:
        return override
    return run_gh(["api", "user", "--jq", ".login"], quiet=quiet)


# ------------------------------- GraphQL queries -------------------------------

ITEM_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issueOrPullRequest(number: $number) {
      __typename
      ... on Issue {
        number
        title
        url
        state
        author { login }
        assignees(first: 20) { nodes { login } }
        labels(first: 50) { nodes { name } }
        createdAt
        updatedAt
        body
        comments(last: 50) {
          nodes { author { login } authorAssociation body createdAt }
        }
      }
      ... on PullRequest {
        number
        title
        url
        state
        isDraft
        author { login }
        assignees(first: 20) { nodes { login } }
        labels(first: 50) { nodes { name } }
        createdAt
        updatedAt
        body
        baseRefName
        comments(last: 50) {
          nodes { author { login } authorAssociation body createdAt }
        }
        reviewDecision
        mergeable
        mergeStateStatus
        reviews(last: 30) {
          nodes { state submittedAt }
        }
        reviewThreads(first: 50) {
          nodes {
            id
            isResolved
            path
            line
            comments(last: 20) {
              nodes { author { login } body createdAt }
            }
          }
        }
        commits(last: 1) {
          nodes {
            commit {
              committedDate
              statusCheckRollup { state }
            }
          }
        }
      }
    }
  }
}
"""

# Issue dependencies / sub-issues — fetched separately so a schema gap degrades
# one axis instead of failing the whole ITEM_QUERY (and thus the item).
ISSUE_DEPENDENCIES_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      blockedBy(first: 20) {
        nodes { number state repository { nameWithOwner } }
      }
      subIssuesSummary { total completed }
      subIssues(first: 50) {
        nodes { number state title repository { nameWithOwner } }
      }
    }
  }
}
"""

OPEN_PULLS_FOR_LINKING_QUERY = """
query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequests(
      first: 100
      after: $cursor
      states: OPEN
      orderBy: {field: CREATED_AT, direction: DESC}
    ) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number
        body
        closingIssuesReferences(first: 20) {
          nodes { number }
        }
      }
    }
  }
}
"""

MERGE_QUEUE_QUERY = """
query($owner: String!, $name: String!, $branch: String!) {
  repository(owner: $owner, name: $name) {
    mergeQueue(branch: $branch) {
      entries(first: 100) {
        nodes { pullRequest { number } }
      }
    }
  }
}
"""

DEFAULT_BRANCH_QUERY = """
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    defaultBranchRef { name }
  }
}
"""

ISSUE_ID_AND_BLOCKERS_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      id
      state
      blockedBy(first: 50) {
        nodes { number repository { nameWithOwner } }
      }
    }
  }
}
"""

NODE_ID_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issueOrPullRequest(number: $number) {
      __typename
      ... on Issue {
        id
        state
        assignees(first: 20) { nodes { login } }
      }
      ... on PullRequest {
        id
        state
        assignees(first: 20) { nodes { login } }
      }
    }
  }
}
"""

ISSUE_NODE_ID_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) { id state }
  }
}
"""

ADD_BLOCKED_BY_MUTATION = """
mutation($issueId: ID!, $blockingIssueId: ID!) {
  addBlockedBy(input: {issueId: $issueId, blockingIssueId: $blockingIssueId}) {
    issue { number }
  }
}
"""

RESOLVE_REVIEW_THREAD_MUTATION = """
mutation($threadId: ID!) {
  resolveReviewThread(input: {threadId: $threadId}) {
    thread { isResolved }
  }
}
"""


# ------------------------------- Fetch / normalize -------------------------------


def _warn_page_cap(kind: str, repo: str, number: int, count: int, cap: int, *, quiet: bool) -> None:
    if quiet or count < cap:
        return
    print(
        f"warning: {repo}#{number} {kind} page full ({cap}); "
        "some entries may be missing from classification",
        file=sys.stderr,
    )


def normalize_item(repo: str, node: dict[str, Any], *, quiet: bool = False) -> dict[str, Any]:
    """Turn a raw issueOrPullRequest GraphQL node into the internal item schema."""
    kind = "issue" if node["__typename"] == "Issue" else "pull"
    labels = [n["name"] for n in node.get("labels", {}).get("nodes", [])]
    assignees = [n["login"] for n in node.get("assignees", {}).get("nodes", [])]
    comment_nodes = node.get("comments", {}).get("nodes", [])
    _warn_page_cap(
        "comments",
        repo,
        node["number"],
        len(comment_nodes),
        COMMENTS_PAGE_SIZE,
        quiet=quiet,
    )
    comments = [
        {
            "author": (c.get("author") or {}).get("login"),
            "author_association": c.get("authorAssociation") or "",
            "body": c.get("body") or "",
            "created_at": c.get("createdAt"),
        }
        for c in comment_nodes
    ]
    item: dict[str, Any] = {
        "kind": kind,
        "repo": repo,
        "number": node["number"],
        "title": node["title"],
        "url": node["url"],
        "state": node["state"],
        "author": (node.get("author") or {}).get("login"),
        "assignees": assignees,
        "labels": labels,
        "created_at": node["createdAt"],
        "updated_at": node["updatedAt"],
        "body": node.get("body") or "",
        "comments": comments,
        "blockers": [],
        "linked_prs": [],
    }
    if kind == "issue":
        apply_issue_dependencies(item, node, quiet=quiet)
    else:
        item["is_draft"] = node.get("isDraft", False)
        item["base_ref_name"] = node.get("baseRefName") or ""
        item["review_decision"] = node.get("reviewDecision")
        item["mergeable"] = node.get("mergeable")
        item["merge_state_status"] = node.get("mergeStateStatus")
        approved_ats = [
            r.get("submittedAt")
            for r in (node.get("reviews") or {}).get("nodes") or []
            if r.get("state") == "APPROVED" and r.get("submittedAt")
        ]
        item["latest_approved_review_at"] = (
            max(approved_ats, key=created_at_key) if approved_ats else None
        )
        threads = (node.get("reviewThreads") or {}).get("nodes") or []
        _warn_page_cap(
            "reviewThreads",
            repo,
            node["number"],
            len(threads),
            REVIEW_THREADS_PAGE_SIZE,
            quiet=quiet,
        )
        unresolved_threads: list[dict[str, Any]] = []
        for t in threads:
            if t.get("isResolved") is not False:
                continue
            comment_nodes = (t.get("comments") or {}).get("nodes") or []
            _warn_page_cap(
                "reviewThread.comments",
                repo,
                node["number"],
                len(comment_nodes),
                REVIEW_THREAD_COMMENTS_PAGE_SIZE,
                quiet=quiet,
            )
            authors = [
                login for c in comment_nodes if (login := (c.get("author") or {}).get("login"))
            ]
            created_ats = [c.get("createdAt") for c in comment_nodes if c.get("createdAt")]
            # Prefer earliest comment time as the thread launch clock.
            created_at = min(created_ats, key=created_at_key) if created_ats else None
            bot_only = bool(authors) and all(a == REVIEW_BOT_LOGIN for a in authors)
            # First comment body gives context on what the finding was about.
            first_body = ""
            if comment_nodes:
                first_body = (comment_nodes[0].get("body") or "")[:COMMENT_TRUNCATE_CHARS]
            thread_entry: dict[str, Any] = {
                "author": authors[0] if authors else None,
                "authors": authors,
                "created_at": created_at,
                "bot_only": bot_only,
            }
            thread_id = t.get("id")
            if thread_id:
                thread_entry["id"] = thread_id
            thread_path = t.get("path")
            if thread_path:
                thread_entry["path"] = thread_path
            thread_line = t.get("line")
            if thread_line is not None:
                thread_entry["line"] = thread_line
            if first_body:
                thread_entry["body"] = first_body
            unresolved_threads.append(thread_entry)
        item["unresolved_threads"] = unresolved_threads
        item["unresolved_review_threads"] = len(unresolved_threads)
        checks_state = None
        head_committed_at = None
        commit_nodes = node.get("commits", {}).get("nodes", [])
        if commit_nodes:
            commit = commit_nodes[-1].get("commit") or {}
            head_committed_at = commit.get("committedDate")
            rollup = commit.get("statusCheckRollup")
            if rollup:
                checks_state = rollup.get("state")
        item["checks_state"] = checks_state
        item["head_committed_at"] = head_committed_at
        item["in_merge_queue"] = False
    return item


def apply_issue_dependencies(
    item: dict[str, Any], node: dict[str, Any], *, quiet: bool = False
) -> None:
    """Merge blockedBy / sub-issues fields from an Issue GraphQL node into ``item``."""
    repo = item["repo"]
    number = item["number"]
    blocked_nodes = (node.get("blockedBy") or {}).get("nodes") or []
    _warn_page_cap("blockedBy", repo, number, len(blocked_nodes), BLOCKERS_PAGE_SIZE, quiet=quiet)
    item["blockers"] = parse_open_blockers(node.get("blockedBy"))
    summary = node.get("subIssuesSummary") or {}
    item["sub_issues_total"] = int(summary.get("total") or 0)
    item["sub_issues_completed"] = int(summary.get("completed") or 0)
    child_nodes = (node.get("subIssues") or {}).get("nodes") or []
    _warn_page_cap(
        "subIssues",
        repo,
        number,
        len(child_nodes),
        SUB_ISSUES_PAGE_SIZE,
        quiet=quiet,
    )
    open_subs: list[dict[str, Any]] = []
    for child in child_nodes:
        if child.get("state") != "OPEN":
            continue
        child_repo = (child.get("repository") or {}).get("nameWithOwner") or repo
        open_subs.append(
            {
                "repo": child_repo,
                "number": child["number"],
                "title": child.get("title") or "",
            }
        )
    item["open_sub_issues"] = open_subs


class ItemFetcher(Protocol):
    """Structural interface build_queue depends on, so tests can stub it without gh."""

    def fetch_item(self, repo: str, number: int) -> dict[str, Any] | None: ...


class MergeQueueChecker(Protocol):
    """Structural interface for merge-queue membership checks."""

    def is_in_merge_queue(
        self, repo: str, number: int, *, base_branch: str | None = None
    ) -> bool: ...


class GhFetcher:
    """Fetches and caches item + linking data from gh GraphQL. Isolated for testability."""

    def __init__(self, *, quiet: bool = False):
        self.quiet = quiet
        self._pulls_by_repo: dict[str, list[dict[str, Any]]] = {}
        # Cache merge-queue PR numbers per (repo, base branch).
        self._merge_queue_entries_by_branch: dict[tuple[str, str], set[int]] = {}
        self._default_branch_by_repo: dict[str, str | None] = {}

    def fetch_item(self, repo: str, number: int) -> dict[str, Any] | None:
        owner, name = repo.split("/", 1)
        data = gh_graphql_or_none(
            ITEM_QUERY, {"owner": owner, "name": name, "number": number}, quiet=self.quiet
        )
        if data is None:
            raise FetchError(repo, number)
        node = (data.get("repository") or {}).get("issueOrPullRequest")
        if node is None:
            return None
        item = normalize_item(repo, node, quiet=self.quiet)
        if item["kind"] == "issue":
            self._enrich_issue_dependencies(item)
        return item

    def _enrich_issue_dependencies(self, item: dict[str, Any]) -> None:
        """Fetch blockedBy/sub-issues separately; leave empty on schema/API failure."""
        owner, name = item["repo"].split("/", 1)
        data = gh_graphql_or_none(
            ISSUE_DEPENDENCIES_QUERY,
            {"owner": owner, "name": name, "number": item["number"]},
            quiet=self.quiet,
        )
        if data is None:
            if not self.quiet:
                print(
                    f"warning: issue dependencies unavailable for "
                    f"{format_ref(item['repo'], item['number'])}; "
                    "continuing without blockedBy/sub-issues",
                    file=sys.stderr,
                )
            return
        issue = (data.get("repository") or {}).get("issue")
        if issue is None:
            return
        apply_issue_dependencies(item, issue, quiet=self.quiet)

    def _pulls_for_linking(self, repo: str) -> list[dict[str, Any]]:
        if repo not in self._pulls_by_repo:
            owner, name = repo.split("/", 1)
            nodes: list[dict[str, Any]] = []
            cursor: str | None = None
            pages = 0
            while True:
                data = gh_graphql_or_none(
                    OPEN_PULLS_FOR_LINKING_QUERY,
                    {"owner": owner, "name": name, "cursor": cursor},
                    quiet=self.quiet,
                )
                if data is None:
                    break
                repo_data = data.get("repository") or {}
                conn = repo_data.get("pullRequests")
                if conn is None:
                    break
                nodes.extend(conn.get("nodes") or [])
                pages += 1
                page = conn.get("pageInfo") or {}
                if not page.get("hasNextPage"):
                    break
                if pages >= MAX_OPEN_PR_PAGES_FOR_LINKING:
                    if not self.quiet:
                        print(
                            f"warning: linked-PR scan capped at "
                            f"{MAX_OPEN_PR_PAGES_FOR_LINKING} pages for {repo}; "
                            "some links may be missed",
                            file=sys.stderr,
                        )
                    break
                cursor = page.get("endCursor")
            self._pulls_by_repo[repo] = nodes
        return self._pulls_by_repo[repo]

    def get_linked_prs(self, repo: str, issue_number: int) -> list[int]:
        by_issue = build_pr_links_by_issue(self._pulls_for_linking(repo))
        return by_issue.get(issue_number, [])

    def _default_branch(self, repo: str) -> str | None:
        if repo not in self._default_branch_by_repo:
            owner, name = repo.split("/", 1)
            data = gh_graphql_or_none(
                DEFAULT_BRANCH_QUERY, {"owner": owner, "name": name}, quiet=self.quiet
            )
            branch = None
            if data is not None:
                ref = (data.get("repository") or {}).get("defaultBranchRef")
                branch = ref.get("name") if ref else None
            self._default_branch_by_repo[repo] = branch
        return self._default_branch_by_repo[repo]

    def is_in_merge_queue(self, repo: str, number: int, *, base_branch: str | None = None) -> bool:
        branch = base_branch or self._default_branch(repo)
        if not branch:
            return False
        owner, name = repo.split("/", 1)
        cache_key = (repo, branch)
        if cache_key not in self._merge_queue_entries_by_branch:
            data = gh_graphql_or_none(
                MERGE_QUEUE_QUERY,
                {"owner": owner, "name": name, "branch": branch},
                quiet=self.quiet,
            )
            numbers: set[int] = set()
            if data is not None:
                queue = (data.get("repository") or {}).get("mergeQueue")
                if queue:
                    for entry in queue.get("entries", {}).get("nodes", []) or []:
                        pr_num = (entry.get("pullRequest") or {}).get("number")
                        if pr_num is not None:
                            numbers.add(pr_num)
            self._merge_queue_entries_by_branch[cache_key] = numbers
        return number in self._merge_queue_entries_by_branch[cache_key]


# ------------------------------- Seeding + deepen-first queue -------------------------------


def seed_from_cli(items: list[str], default_repo: str | None) -> list[tuple[str, int]]:
    refs: list[tuple[str, int]] = []
    seen: set[tuple[str, int]] = set()
    for text in items:
        repo, number = parse_ref(text, default_repo)
        ref = (repo, number)
        if ref not in seen:
            seen.add(ref)
            refs.append(ref)
    return refs


def seed_from_assigned(repo: str, user: str, *, quiet: bool = False) -> list[tuple[str, int]]:
    refs: list[tuple[str, int]] = []
    # gh defaults --limit to 30; raise so "dozens" of assigned items are not truncated.
    issues_raw = run_gh(
        [
            "issue",
            "list",
            "--repo",
            repo,
            "--assignee",
            user,
            "--state",
            "open",
            "--limit",
            "1000",
            "--json",
            "number",
        ],
        quiet=quiet,
    )
    for row in json.loads(issues_raw or "[]"):
        refs.append((repo, row["number"]))
    pulls_raw = run_gh(
        [
            "pr",
            "list",
            "--repo",
            repo,
            "--assignee",
            user,
            "--state",
            "open",
            "--limit",
            "1000",
            "--json",
            "number",
        ],
        quiet=quiet,
    )
    for row in json.loads(pulls_raw or "[]"):
        refs.append((repo, row["number"]))
    return refs


def build_queue(
    seeds: list[tuple[str, int]],
    fetcher: ItemFetcher,
    user: str,
    stale_hours: float,
    now: datetime,
    *,
    max_visits: int = MAX_QUEUE_VISITS,
    triage_stale_hours: float = TRIAGE_STALE_HOURS,
    quiet: bool = False,
) -> tuple[list[dict[str, Any]], int, list[dict[str, Any]]]:
    """Walk seed refs, deepen-first via open blockedBy / sub-issues.

    Only classified open items count toward ``max_visits`` (closed, missing, and
    duplicate-dropped fetches are de-duped but do not burn budget). Discovered
    blockers and sub-issues are prepended so dependency chains complete before
    remaining unrelated seeds.

    Returns ``(results, remaining, fetch_errors)`` where ``remaining`` is how
    many queued refs were left unprocessed when the visit cap was hit (0 if the
    queue drained), and ``fetch_errors`` lists per-item API failures (distinct
    from missing/closed).
    """
    visited: set[tuple[str, int]] = set()
    to_visit: deque[tuple[str, int]] = deque(seeds)
    results: list[dict[str, Any]] = []
    fetch_errors: list[dict[str, Any]] = []

    while to_visit and len(results) < max_visits:
        ref = to_visit.popleft()
        if ref in visited:
            continue
        visited.add(ref)
        repo, number = ref
        try:
            item = fetcher.fetch_item(repo, number)
        except FetchError as exc:
            fetch_errors.append(
                {
                    "repo": exc.repo,
                    "number": exc.number,
                    "detail": exc.detail,
                }
            )
            if not quiet:
                print(
                    f"warning: failed to fetch {exc.repo}#{exc.number}: {exc.detail}",
                    file=sys.stderr,
                )
            continue
        if item is None or item["state"] != "OPEN":
            continue

        resolve_linked_prs: Callable[[], list[int]] | None = None
        get_linked = getattr(fetcher, "get_linked_prs", None)
        if item["kind"] == "issue" and callable(get_linked):

            def _resolve_linked(r=repo, n=number, fn=get_linked) -> list[int]:
                return list(fn(r, n))

            resolve_linked_prs = _resolve_linked

        classification = classify_item(
            item,
            user,
            stale_hours,
            now,
            resolve_linked_prs=resolve_linked_prs,
            triage_stale_hours=triage_stale_hours,
        )
        if classification is None:
            continue

        result = dict(item)
        result["status"] = classification.status
        result["reason"] = classification.reason
        result["eliminated"] = classification.eliminated
        result["suggested_actions"] = classification.suggested_actions
        if classification.status == "assigned_elsewhere":
            result["assignees"] = item["assignees"]
        if classification.blockers:
            result["blockers"] = classification.blockers
        if classification.linked_prs:
            result["linked_prs"] = classification.linked_prs
        if classification.open_sub_issues:
            result["open_sub_issues"] = classification.open_sub_issues
        results.append(result)

        if classification.status == "blocked_by":
            for blocker in reversed(classification.blockers):
                bref = (blocker["repo"], blocker["number"])
                if bref not in visited:
                    to_visit.appendleft(bref)
        elif classification.status == "waiting_sub_issues":
            for child in reversed(classification.open_sub_issues):
                cref = (child["repo"], child["number"])
                if cref not in visited:
                    to_visit.appendleft(cref)

    remaining = sum(1 for ref in to_visit if ref not in visited)
    if remaining and not quiet:
        print(
            f"warning: visit cap ({max_visits}) reached; {remaining} queued ref(s) not processed",
            file=sys.stderr,
        )
    return results, remaining, fetch_errors


def maybe_check_merge_queue(items: list[dict[str, Any]], fetcher: MergeQueueChecker) -> None:
    """Second pass: hits the merge-queue API for all open PRs."""
    for item in items:
        if item["kind"] == "pull":
            item["in_merge_queue"] = fetcher.is_in_merge_queue(
                item["repo"],
                item["number"],
                base_branch=item.get("base_ref_name") or None,
            )


# ------------------------------- Apply / take-over / link-blocker -------------------------------


def apply_trivial_actions(
    items: list[dict[str, Any]], user: str, *, quiet: bool = False
) -> list[dict[str, Any]]:
    applied: list[dict[str, Any]] = []
    for item in items:
        sub = "issue" if item["kind"] == "issue" else "pr"
        suggested = item.get("suggested_actions") or []
        base = {
            "kind": item["kind"],
            "repo": item["repo"],
            "number": item["number"],
            "status": item["status"],
        }

        # Self-assign first (actionable unassigned side-action).
        if ASSIGN_SELF in suggested:
            if (
                run_gh_soft(
                    [
                        sub,
                        "edit",
                        str(item["number"]),
                        "--repo",
                        item["repo"],
                        "--add-assignee",
                        user,
                    ],
                    quiet=quiet,
                )
                is None
            ):
                applied.append({**base, "action": "error", "detail": f"failed {ASSIGN_SELF}"})
            else:
                applied.append({**base, "action": ASSIGN_SELF})

        # Primary trivial status (slash commands; needs_assign is assign-only).
        if (
            not item.get("eliminated")
            and item.get("status") in TRIVIAL_STATUSES
            and item["status"] != "needs_assign"
        ):
            command = SLASH_COMMAND_BY_STATUS[item["status"]]
            if (
                run_gh_soft(
                    [
                        sub,
                        "comment",
                        str(item["number"]),
                        "--repo",
                        item["repo"],
                        "--body",
                        command,
                    ],
                    quiet=quiet,
                )
                is None
            ):
                applied.append(
                    {
                        **base,
                        "action": "error",
                        "detail": f"failed comment:{command}",
                    }
                )
            else:
                applied.append({**base, "action": f"comment:{command}"})

        # Orphaned blocked label — trivial side-action for any primary status,
        # but only on issues the current user owns (or that are unassigned).
        if REMOVE_BLOCKED_LABEL in suggested:
            assignees = item.get("assignees") or []
            if not assignees or user in assignees:
                if (
                    run_gh_soft(
                        [
                            sub,
                            "edit",
                            str(item["number"]),
                            "--repo",
                            item["repo"],
                            "--remove-label",
                            "blocked",
                        ],
                        quiet=quiet,
                    )
                    is None
                ):
                    applied.append(
                        {
                            **base,
                            "action": "error",
                            "detail": f"failed {REMOVE_BLOCKED_LABEL}",
                        }
                    )
                else:
                    applied.append({**base, "action": REMOVE_BLOCKED_LABEL})
    return applied


def take_over(repo: str, number: int, user: str, *, quiet: bool = False) -> dict[str, Any]:
    """Assign ``user`` exclusively (removes other assignees) on an open issue/PR."""
    owner, name = repo.split("/", 1)
    data = gh_graphql_or_none(
        NODE_ID_QUERY, {"owner": owner, "name": name, "number": number}, quiet=quiet
    )
    node = (data or {}).get("repository", {}).get("issueOrPullRequest") if data else None
    if node is None:
        return {"ref": format_ref(repo, number), "action": "error", "detail": "ref not found"}
    if node.get("state") != "OPEN":
        return {
            "ref": format_ref(repo, number),
            "action": "error",
            "detail": f"ref is not open (state={node.get('state')})",
        }
    sub = "issue" if node["__typename"] == "Issue" else "pr"
    if (
        run_gh_soft(
            [sub, "edit", str(number), "--repo", repo, "--add-assignee", user],
            quiet=quiet,
        )
        is None
    ):
        return {
            "ref": format_ref(repo, number),
            "action": "error",
            "detail": f"failed to assign {user}",
        }
    prior = [
        n.get("login")
        for n in (node.get("assignees") or {}).get("nodes") or []
        if n.get("login") and n.get("login") != user
    ]
    removed: list[str] = []
    for login in prior:
        if (
            run_gh_soft(
                [sub, "edit", str(number), "--repo", repo, "--remove-assignee", login],
                quiet=quiet,
            )
            is None
        ):
            return {
                "ref": format_ref(repo, number),
                "action": "error",
                "detail": f"assigned {user} but failed to remove {login}",
            }
        removed.append(login)
    detail = f"assigned to {user}"
    if removed:
        detail += f"; removed {', '.join(removed)}"
    return {"ref": format_ref(repo, number), "action": "assigned", "detail": detail}


def link_blocker(
    dependent: tuple[str, int], blocker: tuple[str, int], *, quiet: bool = False
) -> dict[str, Any]:
    dep_repo, dep_number = dependent
    blk_repo, blk_number = blocker
    if dependent == blocker:
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "error",
            "detail": "an issue cannot block itself",
        }
    dep_owner, dep_name = dep_repo.split("/", 1)

    data = gh_graphql_or_none(
        ISSUE_ID_AND_BLOCKERS_QUERY,
        {"owner": dep_owner, "name": dep_name, "number": dep_number},
        quiet=quiet,
    )
    issue = (data or {}).get("repository", {}).get("issue") if data else None
    if issue is None:
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "error",
            "detail": "dependent ref is not an Issue (GitHub blocked-by is issue-only)",
        }
    if issue.get("state") != "OPEN":
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "error",
            "detail": "dependent Issue is not open",
        }

    existing = {
        (n["repository"]["nameWithOwner"], n["number"]) for n in issue["blockedBy"]["nodes"]
    }
    if (blk_repo, blk_number) in existing:
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "already_linked",
            "detail": "blockedBy link already exists",
        }

    blk_owner, blk_name = blk_repo.split("/", 1)
    # addBlockedBy requires Issue IDs on both sides — do not use issueOrPullRequest here.
    blocker_data = gh_graphql_or_none(
        ISSUE_NODE_ID_QUERY,
        {"owner": blk_owner, "name": blk_name, "number": blk_number},
        quiet=quiet,
    )
    blocker_issue = (
        (blocker_data or {}).get("repository", {}).get("issue") if blocker_data else None
    )
    if blocker_issue is None or not blocker_issue.get("id"):
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "error",
            "detail": "blocker ref is not an Issue (GitHub blocked-by is issue-only)",
        }
    if blocker_issue.get("state") != "OPEN":
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "error",
            "detail": "blocker Issue is not open",
        }

    mutation = gh_graphql_or_none(
        ADD_BLOCKED_BY_MUTATION,
        {"issueId": issue["id"], "blockingIssueId": blocker_issue["id"]},
        quiet=quiet,
    )
    if mutation is None:
        return {
            "dependent": format_ref(dep_repo, dep_number),
            "blocker": format_ref(blk_repo, blk_number),
            "action": "error",
            "detail": "failed to create blockedBy link",
        }
    return {
        "dependent": format_ref(dep_repo, dep_number),
        "blocker": format_ref(blk_repo, blk_number),
        "action": "linked",
        "detail": "created blockedBy link",
    }


def resolve_threads(items: list[dict[str, Any]], *, quiet: bool = False) -> list[dict[str, Any]]:
    """Resolve bot-only unresolved review threads on classified PRs.

    Only resolves threads where all comments are from the review bot
    (``bot_only: true``). Threads with human comments are left open —
    they require a human decision.
    """
    results: list[dict[str, Any]] = []
    for item in items:
        if item.get("kind") != "pull":
            continue
        threads = item.get("unresolved_threads") or []
        for thread in threads:
            if not thread.get("bot_only"):
                continue
            thread_id = thread.get("id")
            if not thread_id:
                results.append(
                    {
                        "repo": item["repo"],
                        "number": item["number"],
                        "thread_path": thread.get("path") or "unknown",
                        "action": "error",
                        "detail": "thread id not available (pre-enrichment data)",
                    }
                )
                continue
            data = gh_graphql_or_none(
                RESOLVE_REVIEW_THREAD_MUTATION,
                {"threadId": thread_id},
                quiet=quiet,
            )
            path = thread.get("path") or "unknown"
            if data is None:
                results.append(
                    {
                        "repo": item["repo"],
                        "number": item["number"],
                        "thread_id": thread_id,
                        "thread_path": path,
                        "action": "error",
                        "detail": "resolveReviewThread mutation failed",
                    }
                )
            else:
                results.append(
                    {
                        "repo": item["repo"],
                        "number": item["number"],
                        "thread_id": thread_id,
                        "thread_path": path,
                        "action": "resolved",
                        "detail": f"resolved bot-only thread at {path}",
                    }
                )
    return results


def parse_link_blocker_spec(spec: str) -> tuple[str, str]:
    if "=" not in spec:
        raise RefError(f"--link-blocker must be DEPENDENT=BLOCKER, got: {spec!r}")
    dependent, blocker = spec.split("=", 1)
    dependent, blocker = dependent.strip(), blocker.strip()
    if not dependent or not blocker:
        raise RefError(f"--link-blocker must be DEPENDENT=BLOCKER, got: {spec!r}")
    return dependent, blocker


def parse_take_over_specs(specs: list[str]) -> list[str]:
    refs: list[str] = []
    for spec in specs:
        for part in spec.split(","):
            part = part.strip()
            if part:
                refs.append(part)
    return refs


# ------------------------------- Output formatting -------------------------------

DECISION_STATUSES = {
    "needs_info_self",
    "promote_code",
    "needs_review_decision",
    "ready_to_merge",
    "fix_conflicts",
    "human_work",
    "close_or_plan",
    "needs_breakdown",
    "needs_design",
    "workflow_blocked",
}

WAITING_PREFIX = "waiting_"


def item_output_dict(item: dict[str, Any], *, include_text: bool) -> dict[str, Any]:
    out = {
        "kind": item["kind"],
        "repo": item["repo"],
        "number": item["number"],
        "title": item["title"],
        "url": item["url"],
        "status": item["status"],
        "eliminated": item["eliminated"],
        "reason": item["reason"],
        "assignees": item.get("assignees", []) if item["status"] == "assigned_elsewhere" else [],
        "blockers": item.get("blockers", []),
        "suggested_actions": item.get("suggested_actions", []),
    }
    if item.get("linked_prs"):
        out["linked_prs"] = item["linked_prs"]
    if item.get("open_sub_issues"):
        out["open_sub_issues"] = item["open_sub_issues"]
    if item.get("sub_issues_total"):
        out["sub_issues_total"] = item["sub_issues_total"]
        out["sub_issues_completed"] = item.get("sub_issues_completed", 0)
    if item.get("unresolved_threads"):
        out["unresolved_threads"] = item["unresolved_threads"]
    if include_text:
        out["body"] = item.get("body", "")[:BODY_TRUNCATE_CHARS]
        out["comments"] = [
            {
                **c,
                "body": (c.get("body") or "")[:COMMENT_TRUNCATE_CHARS],
            }
            for c in item.get("comments", [])[-INCLUDE_TEXT_COMMENT_COUNT:]
        ]
    return out


def format_json_output(
    items: list[dict[str, Any]],
    repo: str,
    user: str,
    stale_hours: float,
    applied: list[dict[str, Any]],
    *,
    include_text: bool,
    link_results: list[dict[str, Any]] | None = None,
    take_over_results: list[dict[str, Any]] | None = None,
    resolve_results: list[dict[str, Any]] | None = None,
    truncated_remaining: int = 0,
    fetch_errors: list[dict[str, Any]] | None = None,
) -> str:
    payload: dict[str, Any] = {
        "repo": repo,
        "user": user,
        "generated_at": datetime.now(UTC).isoformat(),
        "stale_hours": stale_hours,
        "items": [item_output_dict(i, include_text=include_text) for i in items],
        "applied": applied,
    }
    if truncated_remaining:
        payload["truncated"] = True
        payload["truncated_remaining"] = truncated_remaining
    if fetch_errors:
        payload["fetch_errors"] = fetch_errors
    if link_results:
        payload["link_results"] = link_results
    if take_over_results:
        payload["take_over_results"] = take_over_results
    if resolve_results:
        payload["resolve_results"] = resolve_results
    return json.dumps(payload, indent=2)


def _format_item_line(item: dict[str, Any]) -> str:
    link = f"[{item['kind']}#{item['number']}]({item['url']})"
    title = item["title"].replace("|", "\\|")
    return f"- {link} {title} — _{item['status']}_: {item['reason']}"


def _format_mutation_line(entry: dict[str, Any]) -> str:
    """One markdown bullet for an apply / link / take-over / resolve result row."""
    action = entry.get("action") or "?"
    detail = entry.get("detail")
    if "dependent" in entry and "blocker" in entry:
        line = f"- {entry['dependent']} ← {entry['blocker']}: {action}"
    elif "ref" in entry:
        line = f"- {entry['ref']}: {action}"
    elif "thread_path" in entry:
        repo = entry.get("repo") or "?"
        number = entry.get("number")
        path = entry["thread_path"]
        line = f"- #{number} ({repo}) `{path}`: {action}"
    else:
        kind = entry.get("kind") or "item"
        number = entry.get("number")
        repo = entry.get("repo") or "?"
        ref = f"{kind}#{number}" if number is not None else kind
        line = f"- {ref} ({repo}): {action}"
    if detail:
        line = f"{line} — {detail}"
    return line


def format_markdown_output(
    items: list[dict[str, Any]],
    repo: str,
    user: str,
    stale_hours: float,
    applied: list[dict[str, Any]],
    *,
    show_blocked: bool,
    link_results: list[dict[str, Any]] | None = None,
    take_over_results: list[dict[str, Any]] | None = None,
    resolve_results: list[dict[str, Any]] | None = None,
    truncated_remaining: int = 0,
) -> str:
    do_now = [i for i in items if not i["eliminated"]]
    waiting = [i for i in items if i["eliminated"] and i["status"].startswith(WAITING_PREFIX)]
    blocked = [i for i in items if i["eliminated"] and i["status"] == "blocked_by"]
    elsewhere = [i for i in items if i["eliminated"] and i["status"] == "assigned_elsewhere"]

    lines = ["## Do now", ""]
    if do_now:
        lines.extend(_format_item_line(i) for i in do_now)
    else:
        lines.append("_Nothing actionable right now._")
    lines.append("")

    if show_blocked:
        for title, group in (
            ("Waiting", waiting),
            ("Blocked", blocked),
            ("Assigned elsewhere", elsewhere),
        ):
            lines.append(f"## {title}")
            lines.append("")
            if group:
                lines.extend(_format_item_line(i) for i in group)
            else:
                lines.append("_None._")
            lines.append("")

    if applied:
        lines.append("## Applied")
        lines.append("")
        for action in applied:
            lines.append(_format_mutation_line(action))
        lines.append("")

    if link_results:
        lines.append("## Link blockers")
        lines.append("")
        for entry in link_results:
            lines.append(_format_mutation_line(entry))
        lines.append("")

    if take_over_results:
        lines.append("## Take-over")
        lines.append("")
        for entry in take_over_results:
            lines.append(_format_mutation_line(entry))
        lines.append("")

    if resolve_results:
        lines.append("## Resolved threads")
        lines.append("")
        for entry in resolve_results:
            lines.append(_format_mutation_line(entry))
        lines.append("")

    ts = datetime.now(UTC).strftime("%Y-%m-%d %H:%M UTC")
    lines.append(
        f"_Generated {ts} · {repo} · user {user} · stale-hours {stale_hours:g} · "
        f"{len(do_now)} actionable, {len(items) - len(do_now)} waiting/blocked/elsewhere_"
    )
    if truncated_remaining:
        lines.append("")
        lines.append(
            f"_Queue truncated at `--max-visits`; {truncated_remaining} remaining "
            "seed/blocker(s) not classified. Re-run with a higher `--max-visits` "
            "or seed specific refs._"
        )
    return "\n".join(lines)


# ------------------------------- CLI -------------------------------


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Build a readiness-oriented queue of open issues/PRs: assigned work, "
            "GitHub blockedBy links (BFS, cross-repo), and recommended next actions."
        ),
    )
    parser.add_argument(
        "items",
        nargs="*",
        metavar="ITEMS",
        help="Seed refs: owner/repo#N, #N, N (needs --repo), or a GitHub issue/PR URL",
    )
    parser.add_argument("--repo", help="Repository as owner/name (default: current repo)")
    parser.add_argument("--user", help="GitHub login (default: authenticated user)")
    parser.add_argument(
        "--format", choices=("markdown", "json"), default="markdown", help="Output format"
    )
    parser.add_argument(
        "--show-blocked",
        action="store_true",
        help="Include waiting/blocked/assigned-elsewhere details in markdown output",
    )
    parser.add_argument(
        "--apply",
        action="store_true",
        help=(
            "Perform trivial actions: assign:self first when suggested, "
            "post /fs-* comments, remove orphaned blocked labels"
        ),
    )
    parser.add_argument(
        "--take-over",
        action="append",
        default=[],
        metavar="REFS",
        help="Assign listed refs (comma-separated or repeatable) to --user, then classify normally",
    )
    parser.add_argument(
        "--link-blocker",
        action="append",
        default=[],
        metavar="DEPENDENT=BLOCKER",
        help="Persist a real GitHub blockedBy link (repeatable). Idempotent if already linked.",
    )
    parser.add_argument(
        "--resolve-threads",
        action="store_true",
        help=(
            "Resolve bot-only unresolved review threads on classified PRs. "
            "Only threads where all comments are from the review bot are resolved; "
            "threads with human comments are left open. Requires --confirmed."
        ),
    )
    parser.add_argument(
        "--confirmed",
        action="store_true",
        help=(
            "Required with --apply / --take-over / --link-blocker: acknowledges that "
            "GitHub mutations will run (skill/CLI confirmation gate)"
        ),
    )
    parser.add_argument(
        "--decisions-only",
        action="store_true",
        help="Filter output to non-trivial decisions only (hides waiting/blocked/trivial items)",
    )
    parser.add_argument(
        "--stale-hours",
        type=float,
        default=6,
        metavar="N",
        help=(
            "Hours after which a stuck in-flight agent start or never-started "
            "launch label/command becomes actionable (default: 6)"
        ),
    )
    parser.add_argument(
        "--triage-stale-hours",
        type=float,
        default=TRIAGE_STALE_HOURS,
        metavar="N",
        help=(
            "Hours after which a completed triage is considered stale "
            f"(default: {TRIAGE_STALE_HOURS:g})"
        ),
    )
    parser.add_argument(
        "--max-visits",
        type=int,
        default=MAX_QUEUE_VISITS,
        metavar="N",
        help=f"Max classified items to visit when walking blockers (default: {MAX_QUEUE_VISITS})",
    )
    parser.add_argument("--quiet", action="store_true", help="Suppress stderr on API failures")
    parser.add_argument(
        "--include-text",
        action="store_true",
        help="Include truncated body + last comments in JSON output (for prose-dependency mining)",
    )
    args = parser.parse_args(argv)
    if args.stale_hours < 0:
        print("error: --stale-hours must be non-negative", file=sys.stderr)
        sys.exit(2)
    if args.triage_stale_hours < 0:
        print("error: --triage-stale-hours must be non-negative", file=sys.stderr)
        sys.exit(2)
    if args.max_visits < 1:
        print("error: --max-visits must be at least 1", file=sys.stderr)
        sys.exit(2)
    mutating = args.apply or bool(args.take_over) or bool(args.link_blocker) or args.resolve_threads
    if mutating and not args.confirmed:
        print(
            "error: --apply / --take-over / --link-blocker / --resolve-threads require "
            "--confirmed (defense-in-depth confirmation gate)",
            file=sys.stderr,
        )
        sys.exit(2)
    if args.confirmed and not mutating:
        print(
            "error: --confirmed is only valid with "
            "--apply / --take-over / --link-blocker / --resolve-threads",
            file=sys.stderr,
        )
        sys.exit(2)
    return args


def main(argv: list[str] | None = None) -> None:
    args = parse_args(argv)
    repo = resolve_repo(args.repo)
    user = resolve_user(args.user, quiet=args.quiet)
    now = datetime.now(UTC)

    link_results: list[dict[str, Any]] = []
    for spec in args.link_blocker:
        try:
            dep_text, blk_text = parse_link_blocker_spec(spec)
            dependent = parse_ref(dep_text, repo)
            blocker = parse_ref(blk_text, repo)
        except RefError as exc:
            print(f"error: {exc}", file=sys.stderr)
            sys.exit(2)
        link_results.append(link_blocker(dependent, blocker, quiet=args.quiet))

    take_over_results: list[dict[str, Any]] = []
    for ref_text in parse_take_over_specs(args.take_over):
        try:
            take_repo, take_number = parse_ref(ref_text, repo)
        except RefError as exc:
            print(f"error: {exc}", file=sys.stderr)
            sys.exit(2)
        take_over_results.append(take_over(take_repo, take_number, user, quiet=args.quiet))

    try:
        if args.items:
            seeds = seed_from_cli(args.items, repo)
        else:
            seeds = seed_from_assigned(repo, user, quiet=args.quiet)
    except RefError as exc:
        print(f"error: {exc}", file=sys.stderr)
        sys.exit(2)

    fetcher = GhFetcher(quiet=args.quiet)
    items, truncated_remaining, fetch_errors = build_queue(
        seeds,
        fetcher,
        user,
        args.stale_hours,
        now,
        max_visits=args.max_visits,
        triage_stale_hours=args.triage_stale_hours,
        quiet=args.quiet,
    )
    maybe_check_merge_queue(items, fetcher)
    # Merge-queue membership can change any status -> waiting_merge_queue; reclassify.
    for item in items:
        if item["kind"] == "pull" and item.get("in_merge_queue"):
            classification = classify_item(
                item,
                user,
                args.stale_hours,
                now,
                triage_stale_hours=args.triage_stale_hours,
            )
            if classification is not None:
                item["status"] = classification.status
                item["reason"] = classification.reason
                item["eliminated"] = classification.eliminated
                item["suggested_actions"] = classification.suggested_actions

    applied: list[dict[str, Any]] = []
    if args.apply:
        applied = apply_trivial_actions(items, user, quiet=args.quiet)
        applied_refs = {(a["repo"], a["number"]) for a in applied if a.get("action") != "error"}
        for item in items:
            if (item["repo"], item["number"]) in applied_refs:
                item["reason"] = f"{item['reason']} (applied)"

    resolve_results: list[dict[str, Any]] = []
    if args.resolve_threads:
        resolve_results = resolve_threads(items, quiet=args.quiet)

    if args.decisions_only:
        items = [i for i in items if i["status"] in DECISION_STATUSES]

    if args.format == "json":
        print(
            format_json_output(
                items,
                repo,
                user,
                args.stale_hours,
                applied,
                include_text=args.include_text,
                link_results=link_results,
                take_over_results=take_over_results,
                resolve_results=resolve_results,
                truncated_remaining=truncated_remaining,
                fetch_errors=fetch_errors,
            )
        )
    else:
        print(
            format_markdown_output(
                items,
                repo,
                user,
                args.stale_hours,
                applied,
                show_blocked=args.show_blocked,
                link_results=link_results,
                take_over_results=take_over_results,
                resolve_results=resolve_results,
                truncated_remaining=truncated_remaining,
            )
        )
    mutation_error = any(
        r.get("action") == "error"
        for r in (*applied, *link_results, *take_over_results, *resolve_results)
    )
    if fetch_errors or mutation_error:
        sys.exit(3)


if __name__ == "__main__":
    main()
