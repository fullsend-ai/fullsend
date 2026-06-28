#!/usr/bin/env python3
"""Build a merged RICE priority table via gh GraphQL (stdlib only)."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any

RICE_SCORE_FIELD = "RICE Score"
PR_ISSUE_RE = re.compile(
    r"\b(?:closes|fixes|resolves|partial-fix)\s+#(\d+)\b",
    re.IGNORECASE,
)

PULLS_QUERY = """
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

PROJECT_ITEMS_QUERY = """
query($projectId: ID!, $cursor: String) {
  node(id: $projectId) {
    ... on ProjectV2 {
      items(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          fieldValues(first: 20) {
            nodes {
              ... on ProjectV2ItemFieldNumberValue {
                field { ... on ProjectV2Field { id name } }
                number
              }
            }
          }
          content {
            ... on Issue {
              number
              title
              state
              url
              assignees(first: 10) { nodes { login } }
              blockedBy(first: 20) {
                nodes {
                  number
                  state
                  ... on Issue {
                    number
                    state
                    repository { nameWithOwner }
                  }
                }
              }
              repository { name owner { login } }
            }
          }
        }
      }
    }
  }
}
"""


@dataclass(frozen=True)
class IssueRow:
    number: int
    title: str
    score: float
    mine: bool
    open_prs: tuple[int, ...]


def parse_rice_score_from_fields(field_values: list[dict[str, Any]]) -> float | None:
    """Return RICE Score from project item field values (RICE Score field only)."""
    for node in field_values:
        field = node.get("field") or {}
        if field.get("name") != RICE_SCORE_FIELD:
            continue
        number = node.get("number")
        if number is None:
            return None
        return float(number)
    return None


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
    """Return open issues that block this issue (GitHub blockedBy links)."""
    blockers: list[dict[str, Any]] = []
    for node in (blocked_by or {}).get("nodes", []):
        if node.get("state") != "OPEN":
            continue
        repo = (node.get("repository") or {}).get("nameWithOwner", "")
        blockers.append({"repo": repo, "number": node["number"]})
    return blockers


def issue_has_open_pr(issue_number: int, pr_links_by_issue: dict[int, list[int]]) -> bool:
    return bool(pr_links_by_issue.get(issue_number))


def issue_is_blocked(issue: dict[str, Any]) -> bool:
    return bool(issue.get("open_blockers"))


def normalize_project_item(raw: dict[str, Any], repo: str) -> dict[str, Any] | None:
    """Map a project item to issue dict when it is an open issue in repo with RICE Score."""
    content = raw.get("content") or {}
    if not content or content.get("state") != "OPEN":
        return None
    owner = (content.get("repository") or {}).get("owner", {}).get("login")
    name = (content.get("repository") or {}).get("name")
    if not owner or not name or f"{owner}/{name}" != repo:
        return None
    field_nodes = (raw.get("fieldValues") or {}).get("nodes") or []
    score = parse_rice_score_from_fields(field_nodes)
    if score is None:
        return None
    assignees = [n["login"] for n in content.get("assignees", {}).get("nodes", [])]
    open_blockers = parse_open_blockers(content.get("blockedBy"))
    return {
        "number": content["number"],
        "title": content["title"],
        "assignees": assignees,
        "open_blockers": open_blockers,
        "score": score,
    }


def build_top_pool(
    issues: list[dict[str, Any]],
    pr_links_by_issue: dict[int, list[int]],
    top_n: int,
) -> list[dict[str, Any]]:
    """Top N open unassigned issues with no open linked PR, no open blockers, and a RICE score."""
    eligible = [
        issue
        for issue in issues
        if not issue["assignees"]
        and not issue_has_open_pr(issue["number"], pr_links_by_issue)
        and not issue_is_blocked(issue)
        and issue["score"] is not None
    ]
    eligible.sort(key=lambda i: (-i["score"], i["number"]))
    return eligible[:top_n]


def build_mine_pool(
    issues: list[dict[str, Any]],
    user: str,
) -> list[dict[str, Any]]:
    """Open issues assigned to user that have a RICE score."""
    eligible = [
        issue for issue in issues if user in issue["assignees"] and issue["score"] is not None
    ]
    eligible.sort(key=lambda i: (-i["score"], i["number"]))
    return eligible


def merge_rows(
    top_pool: list[dict[str, Any]],
    mine_pool: list[dict[str, Any]],
    user: str,
    pr_links_by_issue: dict[int, list[int]],
) -> list[IssueRow]:
    """Union top and mine pools by issue number, sorted by score descending."""
    merged: dict[int, IssueRow] = {}
    for issue in top_pool + mine_pool:
        num = issue["number"]
        mine = user in issue["assignees"]
        prs = tuple(pr_links_by_issue.get(num, ()))
        row = IssueRow(
            number=num,
            title=issue["title"],
            score=issue["score"],
            mine=mine,
            open_prs=prs,
        )
        if num not in merged or row.score > merged[num].score:
            merged[num] = row
    return sorted(merged.values(), key=lambda r: (-r.score, r.number))


def format_pr_links(repo: str, pr_numbers: tuple[int, ...]) -> str:
    if not pr_numbers:
        return "—"
    parts = [f"[#{n}](https://github.com/{repo}/pull/{n})" for n in pr_numbers]
    return ", ".join(parts)


def format_markdown_table(
    rows: list[IssueRow],
    repo: str,
    user: str,
    project_number: int,
    top_count: int,
    mine_count: int,
) -> str:
    lines = [
        "| Issue | Score | Mine | Open PRs | Title |",
        "|-------|-------|------|----------|-------|",
    ]
    for row in rows:
        issue_link = f"[#{row.number}](https://github.com/{repo}/issues/{row.number})"
        mine = "Yes" if row.mine else "No"
        prs = format_pr_links(repo, row.open_prs)
        title = row.title.replace("|", "\\|")
        lines.append(f"| {issue_link} | {row.score:g} | {mine} | {prs} | {title} |")
    ts = datetime.now(UTC).strftime("%Y-%m-%d %H:%M UTC")
    lines.append("")
    lines.append(
        f"_Generated {ts} · {repo} · project #{project_number} · user {user} · "
        f"{len(rows)} issue(s) ({top_count} top backlog + {mine_count} assigned)_"
    )
    return "\n".join(lines)


def format_json_output(
    rows: list[IssueRow],
    repo: str,
    user: str,
    project_number: int,
    top_count: int,
    mine_count: int,
) -> str:
    payload = {
        "repo": repo,
        "project_number": project_number,
        "user": user,
        "generated_at": datetime.now(UTC).isoformat(),
        "counts": {
            "total": len(rows),
            "top_backlog": top_count,
            "mine": mine_count,
        },
        "issues": [
            {
                "number": r.number,
                "title": r.title,
                "score": r.score,
                "mine": r.mine,
                "open_prs": list(r.open_prs),
            }
            for r in rows
        ],
    }
    return json.dumps(payload, indent=2)


def _gh_not_found() -> None:
    print("error: gh CLI not found; install https://cli.github.com/", file=sys.stderr)
    sys.exit(1)


def try_run_gh(args: list[str]) -> str | None:
    """Run gh and return stdout, or None if the command failed."""
    try:
        result = subprocess.run(
            ["gh", *args],
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        _gh_not_found()
    except subprocess.CalledProcessError:
        return None
    return result.stdout.strip()


def run_gh(args: list[str], *, quiet: bool = False) -> str:
    try:
        result = subprocess.run(
            ["gh", *args],
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        _gh_not_found()
    except subprocess.CalledProcessError as exc:
        if not quiet:
            if exc.stderr:
                print(exc.stderr.strip(), file=sys.stderr)
            if exc.stdout:
                print(exc.stdout.strip(), file=sys.stderr)
        sys.exit(3)
    return result.stdout.strip()


def gh_graphql(query: str, variables: dict[str, Any], *, quiet: bool = False) -> dict[str, Any]:
    args = ["api", "graphql", "-f", f"query={query}"]
    for key, value in variables.items():
        if value is None:
            continue
        args.extend(["-f", f"{key}={value}"])
    raw = run_gh(args, quiet=quiet)
    data = json.loads(raw)
    if data.get("errors"):
        if not quiet:
            print(json.dumps(data["errors"], indent=2), file=sys.stderr)
        sys.exit(3)
    return data["data"]


def paginate_project_items(project_id: str, *, quiet: bool = False) -> list[dict[str, Any]]:
    nodes: list[dict[str, Any]] = []
    cursor: str | None = None
    while True:
        data = gh_graphql(
            PROJECT_ITEMS_QUERY,
            {"projectId": project_id, "cursor": cursor},
            quiet=quiet,
        )
        items = data["node"]["items"]
        nodes.extend(items["nodes"])
        page = items["pageInfo"]
        if not page["hasNextPage"]:
            break
        cursor = page["endCursor"]
    return nodes


def paginate_connection(
    repo_owner: str,
    repo_name: str,
    connection_path: list[str],
    query: str,
    *,
    quiet: bool = False,
) -> list[dict[str, Any]]:
    nodes: list[dict[str, Any]] = []
    cursor: str | None = None
    while True:
        data = gh_graphql(
            query,
            {"owner": repo_owner, "name": repo_name, "cursor": cursor},
            quiet=quiet,
        )
        repo = data["repository"]
        conn = repo
        for part in connection_path:
            conn = conn[part]
        nodes.extend(conn["nodes"])
        page = conn["pageInfo"]
        if not page["hasNextPage"]:
            break
        cursor = page["endCursor"]
    return nodes


def resolve_repo(override: str | None) -> str:
    if override:
        if "/" not in override or override.count("/") != 1:
            print(
                f"error: --repo must be owner/name, got: {override!r}",
                file=sys.stderr,
            )
            sys.exit(2)
        return override
    try:
        raw = run_gh(["repo", "view", "--json", "nameWithOwner"])
    except SystemExit:
        raise
    repo = json.loads(raw)["nameWithOwner"]
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


def resolve_project_number(
    org: str,
    override: int | None,
    *,
    quiet: bool = False,
) -> int:
    if override is not None:
        return override
    env_val = os.environ.get("FULLSEND_PROJECT_NUMBER", "").strip()
    if env_val:
        try:
            return int(env_val)
        except ValueError:
            pass
    raw = try_run_gh(["variable", "get", "FULLSEND_PROJECT_NUMBER", "-o", org])
    if raw is not None:
        try:
            return int(raw.strip())
        except ValueError:
            pass
    print(
        "error: project number required; set FULLSEND_PROJECT_NUMBER, "
        f"use --project N, or configure org variable on {org}",
        file=sys.stderr,
    )
    sys.exit(1)


def fetch_project_id(org: str, project_number: int, *, quiet: bool = False) -> str:
    raw = run_gh(
        ["project", "view", str(project_number), "--owner", org, "--format", "json"],
        quiet=quiet,
    )
    project_id = json.loads(raw).get("id")
    if not project_id:
        print(
            f"error: project #{project_number} not found for org {org}",
            file=sys.stderr,
        )
        sys.exit(3)
    return project_id


def fetch_scored_issues_from_project(
    org: str,
    project_number: int,
    repo: str,
    *,
    quiet: bool = False,
) -> list[dict[str, Any]]:
    project_id = fetch_project_id(org, project_number, quiet=quiet)
    items = paginate_project_items(project_id, quiet=quiet)
    issues: list[dict[str, Any]] = []
    for item in items:
        normalized = normalize_project_item(item, repo)
        if normalized is not None:
            issues.append(normalized)
    return issues


def fetch_open_pulls(owner: str, name: str, *, quiet: bool = False) -> list[dict[str, Any]]:
    return paginate_connection(owner, name, ["pullRequests"], PULLS_QUERY, quiet=quiet)


def build_table(
    repo: str,
    user: str,
    top_n: int,
    issues: list[dict[str, Any]],
    pulls: list[dict[str, Any]],
) -> tuple[list[IssueRow], int, int]:
    pr_links = build_pr_links_by_issue(pulls)
    top_pool = build_top_pool(issues, pr_links, top_n)
    mine_pool = build_mine_pool(issues, user)
    rows = merge_rows(top_pool, mine_pool, user, pr_links)
    return rows, len(top_pool), len(mine_pool)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Build a merged RICE priority table from the org project board "
            "(top backlog + your assigned issues)."
        ),
    )
    parser.add_argument(
        "--top",
        type=int,
        default=10,
        metavar="N",
        help="Number of top unassigned backlog issues (default: 10)",
    )
    parser.add_argument("--repo", help="Repository as owner/name (default: current repo)")
    parser.add_argument(
        "--project",
        type=int,
        metavar="N",
        help="GitHub Project number (default: FULLSEND_PROJECT_NUMBER env or org variable)",
    )
    parser.add_argument("--user", help="GitHub login (default: authenticated user)")
    parser.add_argument(
        "--format",
        choices=("markdown", "json"),
        default="markdown",
        help="Output format (default: markdown)",
    )
    parser.add_argument(
        "--quiet",
        action="store_true",
        help="Suppress stderr messages on API failures",
    )
    args = parser.parse_args(argv)
    if args.top < 1:
        print("error: --top must be at least 1", file=sys.stderr)
        sys.exit(2)
    return args


def main(argv: list[str] | None = None) -> None:
    args = parse_args(argv)
    repo = resolve_repo(args.repo)
    owner, name = repo.split("/", 1)
    user = resolve_user(args.user, quiet=args.quiet)
    project_number = resolve_project_number(owner, args.project, quiet=args.quiet)

    issues = fetch_scored_issues_from_project(owner, project_number, repo, quiet=args.quiet)
    pulls = fetch_open_pulls(owner, name, quiet=args.quiet)
    rows, top_count, mine_count = build_table(repo, user, args.top, issues, pulls)

    if args.format == "json":
        print(format_json_output(rows, repo, user, project_number, top_count, mine_count))
    else:
        print(format_markdown_table(rows, repo, user, project_number, top_count, mine_count))


if __name__ == "__main__":
    main()
