#!/usr/bin/env python3
"""List open pull requests that change Architecture Decision Records.

The script is the deterministic data-gathering half of the ``/adr-corner``
skill.  It uses only the GitHub CLI and Python's standard library so the skill
can run in the same environments as ``/nextwork``.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import UTC, datetime
from typing import Any

ADR_DIRECTORY_RE = re.compile(r"(?:^|/)(?:adr|adrs)(?:/|$)", re.IGNORECASE)
ADR_FILENAME_RE = re.compile(r"(?:^|/)adr[-_ ]?\d+\.md$", re.IGNORECASE)
BOT_LOGIN_RE = re.compile(r"(?:\[bot\]|(?:^|[-_])bot(?:$|[-_]))", re.IGNORECASE)
DISCUSSION_HINT_RE = re.compile(
    r"\b(?:but|however|concern|question|why|should|suggest|request|block|"
    r"change|agree|disagree|trade[- ]?off|must|need|open question|risk)\b",
    re.IGNORECASE,
)
SLASH_COMMAND_RE = re.compile(r"^/[A-Za-z0-9_-]+(?:\s|$)")
MARKDOWN_LINK_RE = re.compile(r"!?\[([^\]]+)\]\([^)]*\)")
WHITESPACE_RE = re.compile(r"\s+")

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
        title
        url
        createdAt
        updatedAt
        headRefOid
        author { login __typename }
        assignees(first: 20) { nodes { login __typename } }
        files(first: 100) {
          pageInfo { hasNextPage endCursor }
          nodes { path changeType }
        }
      }
    }
  }
}
"""

PR_DETAILS_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      body
      author { login __typename }
      assignees(first: 20) { nodes { login __typename } }
      closingIssuesReferences(first: 20) {
        nodes {
          number
          title
          url
          state
          author { login __typename }
          assignees(first: 20) { nodes { login __typename } }
        }
      }
    }
  }
}
"""

PR_COMMITS_QUERY = """
query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      commits(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          commit {
            author { name user { login __typename } }
            committer { name user { login __typename } }
          }
        }
      }
    }
  }
}
"""

PR_COMMENTS_QUERY = """
query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      comments(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { author { login __typename } body createdAt updatedAt }
      }
    }
  }
}
"""

PR_REVIEWS_QUERY = """
query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          author { login __typename }
          body
          state
          createdAt
          submittedAt
        }
      }
    }
  }
}
"""

PR_THREADS_QUERY = """
query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { id isResolved }
      }
    }
  }
}
"""

THREAD_COMMENTS_QUERY = """
query($threadId: ID!, $cursor: String) {
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { author { login __typename } body createdAt }
      }
    }
  }
}
"""

FILES_QUERY = """
query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      files(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { path changeType }
      }
    }
  }
}
"""

BLOB_QUERY = """
query($owner: String!, $name: String!, $expression: String!) {
  repository(owner: $owner, name: $name) {
    object(expression: $expression) {
      ... on Blob { text }
    }
  }
}
"""


def is_bot(actor: dict[str, Any] | None, login: str | None = None) -> bool:
    """Classify a GitHub actor as a bot using GraphQL type plus safe heuristics."""
    actor = actor or {}
    candidate = login or actor.get("login") or ""
    return actor.get("__typename") == "Bot" or bool(BOT_LOGIN_RE.search(candidate))


def is_adr_path(path: str) -> bool:
    """Return whether a changed path is conventionally an ADR Markdown file."""
    normalized = path.replace("\\", "/").strip("/")
    if not normalized.lower().endswith(".md"):
        return False
    if normalized.rsplit("/", 1)[-1].lower() in {"readme.md", "index.md"}:
        return False
    return bool(ADR_DIRECTORY_RE.search(normalized) or ADR_FILENAME_RE.search(normalized))


def normalize_text(text: str) -> str:
    """Make Markdown suitable for a compact one-line report excerpt."""
    text = re.sub(r"<!--.*?-->", " ", text, flags=re.DOTALL)
    text = re.sub(r"```.*?```", " ", text, flags=re.DOTALL)
    text = MARKDOWN_LINK_RE.sub(r"\1", text)
    text = re.sub(r"[#>*`_]", " ", text)
    return WHITESPACE_RE.sub(" ", text).strip()


def truncate(text: str, limit: int) -> str:
    text = normalize_text(text)
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def adr_summary(text: str | None, path: str) -> str:
    """Extract a short decision-oriented summary from an ADR body."""
    label = path.rsplit("/", 1)[-1].removesuffix(".md")
    if not text:
        return f"{label}: ADR content unavailable at the PR head"

    lines = text.splitlines()
    sections: dict[str, list[str]] = {}
    current = "__intro__"
    sections[current] = []
    for line in lines:
        heading = re.match(r"^#{1,6}\s+(.+?)\s*$", line)
        if heading:
            current = normalize_text(heading.group(1)).lower()
            sections.setdefault(current, [])
        else:
            sections.setdefault(current, []).append(line)

    preferred = ("decision", "summary", "context", "problem", "status")
    candidates: list[str] = []
    for section in preferred:
        body = normalize_text(" ".join(sections.get(section, [])))
        if body:
            candidates.append(body)
    if not candidates:
        candidates = [
            normalize_text(" ".join(part.splitlines()))
            for part in re.split(r"\n\s*\n", text)
            if normalize_text(part)
        ]
    summary = candidates[0] if candidates else "No summary text found"
    return f"{label}: {truncate(summary, 280)}"


def _actor_login(actor: dict[str, Any] | None) -> str | None:
    return (actor or {}).get("login")


def _human_login(actor: dict[str, Any] | None) -> str | None:
    login = _actor_login(actor)
    return login if login and not is_bot(actor, login) else None


def _unique_humans(candidates: list[tuple[str | None, str]]) -> list[dict[str, str]]:
    seen: set[str] = set()
    result: list[dict[str, str]] = []
    for login, source in candidates:
        if not login or login in seen or is_bot({}, login):
            continue
        seen.add(login)
        result.append({"login": login, "source": source})
    return result


def infer_adr_authors(pr: dict[str, Any]) -> list[dict[str, str]]:
    """Infer the human responsible for a bot-authored ADR PR.

    The order mirrors the code-agent ownership fallback: human PR author,
    human commit author, PR assignee, linked-issue assignee, linked-issue
    creator.  The source is retained so the report does not present an
    inference as a direct GitHub fact.
    """
    pr_author = pr.get("author") or {}
    candidates: list[tuple[str | None, str]] = []
    direct = _human_login(pr_author)
    if direct:
        candidates.append((direct, "PR author"))
    for commit_node in (pr.get("commits") or {}).get("nodes", []):
        commit = commit_node.get("commit") or {}
        for field, source in (("author", "commit author"), ("committer", "commit committer")):
            actor = commit.get(field) or {}
            login = _human_login(actor.get("user"))
            if login:
                candidates.append((login, source))
    for node in (pr.get("assignees") or {}).get("nodes", []):
        candidates.append((_human_login(node), "PR assignee"))
    for issue in (pr.get("closingIssuesReferences") or {}).get("nodes", []):
        for node in (issue.get("assignees") or {}).get("nodes", []):
            candidates.append((_human_login(node), "linked issue assignee"))
    for issue in (pr.get("closingIssuesReferences") or {}).get("nodes", []):
        candidates.append((_human_login(issue.get("author")), "linked issue creator"))
    return _unique_humans(candidates)


def should_include_adr_pr(adr_files: list[dict[str, Any]], authors: list[dict[str, str]]) -> bool:
    """Keep human-owned edits, or unattributed PRs that add a new ADR."""
    return bool(authors) or any(file.get("changeType") == "ADDED" for file in adr_files)


def summary_files_for_pr(adr_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Prefer newly added ADRs; fall back to updates when there are no additions."""
    added = [file for file in adr_files if file.get("changeType") == "ADDED"]
    return added or adr_files


def _comment_record(
    actor: dict[str, Any] | None, body: str | None, created_at: str | None, kind: str
) -> dict[str, Any] | None:
    body = normalize_text(body or "")
    command = SLASH_COMMAND_RE.match(body)
    if command:
        body = body[command.end() :].lstrip()
    login = _actor_login(actor)
    if not body:
        return None
    return {
        "author": login or "unknown",
        "body": body,
        "created_at": created_at,
        "kind": kind,
        "bot": is_bot(actor, login),
    }


def discussion_points(pr: dict[str, Any], limit: int = 4) -> list[str]:
    """Select concise human discussion excerpts that signal disagreement or review."""
    records: list[dict[str, Any]] = []
    for node in (pr.get("comments") or {}).get("nodes", []):
        record = _comment_record(
            node.get("author"), node.get("body"), node.get("createdAt"), "comment"
        )
        if record:
            records.append(record)
    for node in (pr.get("reviews") or {}).get("nodes", []):
        record = _comment_record(
            node.get("author"),
            node.get("body"),
            node.get("submittedAt") or node.get("createdAt"),
            "review",
        )
        if record:
            record["state"] = node.get("state")
            records.append(record)
    for thread in (pr.get("reviewThreads") or {}).get("nodes", []):
        for node in (thread.get("comments") or {}).get("nodes", []):
            record = _comment_record(
                node.get("author"), node.get("body"), node.get("createdAt"), "review thread"
            )
            if record:
                records.append(record)

    human = [record for record in records if not record["bot"]]
    hinted = [
        record
        for record in human
        if DISCUSSION_HINT_RE.search(record["body"]) or record.get("state") == "CHANGES_REQUESTED"
    ]
    ordinary = [record for record in human if record not in hinted]
    selected = list(reversed(hinted)) + list(reversed(ordinary))
    result: list[str] = []
    seen: set[str] = set()
    for record in selected:
        excerpt = truncate(record["body"], 220)
        key = excerpt.casefold()
        if key in seen:
            continue
        seen.add(key)
        result.append(f"{record['author']} ({record['kind']}): {excerpt}")
        if len(result) >= limit:
            break
    return result


def parse_iso(value: str | None) -> datetime | None:
    if not value:
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def friendly_datetime(value: str | None) -> str:
    parsed = parse_iso(value)
    if parsed is None:
        return "unknown"
    parsed = parsed.astimezone(UTC)
    return f"{parsed.day} {parsed.strftime('%b %Y, %H:%M UTC')}"


def sort_rows_oldest_first(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Sort report rows by PR creation time, oldest first; unknown dates last."""
    latest = datetime.max.replace(tzinfo=UTC)
    return sorted(rows, key=lambda row: parse_iso(row.get("created_at")) or latest)


def graphql_var_flags(variables: dict[str, Any]) -> list[str]:
    flags: list[str] = []
    for key, value in variables.items():
        if value is None:
            continue
        flag = "-F" if isinstance(value, (bool, int, float)) else "-f"
        rendered = str(value).lower() if isinstance(value, bool) else str(value)
        flags.extend([flag, f"{key}={rendered}"])
    return flags


def _gh_not_found() -> None:
    print("error: gh CLI not found; install https://cli.github.com/", file=sys.stderr)
    raise SystemExit(1)


def run_gh(args: list[str], *, quiet: bool = False) -> str:
    try:
        result = subprocess.run(["gh", *args], check=True, capture_output=True, text=True)
    except FileNotFoundError:
        _gh_not_found()
    except subprocess.CalledProcessError as exc:
        if not quiet:
            output = "\n".join(part.strip() for part in (exc.stderr, exc.stdout) if part).strip()
            if "error connecting to api.github.com" in output.lower():
                print(
                    "error: GitHub API is unreachable from this shell; "
                    "run /adr-corner with outbound HTTPS access to api.github.com "
                    "or approve a network-enabled shell, then retry.",
                    file=sys.stderr,
                )
            elif output:
                print(output, file=sys.stderr)
        raise SystemExit(3) from exc
    return result.stdout.strip()


def gh_graphql(query: str, variables: dict[str, Any], *, quiet: bool = False) -> dict[str, Any]:
    raw = run_gh(
        ["api", "graphql", "-f", f"query={query}", *graphql_var_flags(variables)], quiet=quiet
    )
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        print(f"error: invalid GraphQL response: {exc}", file=sys.stderr)
        raise SystemExit(3) from exc
    if data.get("errors"):
        if not quiet:
            print(json.dumps(data["errors"], indent=2), file=sys.stderr)
        raise SystemExit(3)
    return data["data"]


def resolve_repo(override: str | None) -> str:
    if override:
        if override.count("/") != 1:
            print(f"error: --repo must be owner/name, got {override!r}", file=sys.stderr)
            raise SystemExit(2)
        return override
    raw = run_gh(["repo", "view", "--json", "nameWithOwner"])
    repo = json.loads(raw).get("nameWithOwner")
    if not repo:
        print(
            "error: not inside a git repository known to gh; use --repo owner/name", file=sys.stderr
        )
        raise SystemExit(1)
    return repo


def fetch_open_pulls(owner: str, name: str, *, quiet: bool = False) -> list[dict[str, Any]]:
    pulls: list[dict[str, Any]] = []
    cursor: str | None = None
    while True:
        data = gh_graphql(
            PULLS_QUERY, {"owner": owner, "name": name, "cursor": cursor}, quiet=quiet
        )
        connection = data["repository"]["pullRequests"]
        pulls.extend(connection["nodes"])
        if not connection["pageInfo"]["hasNextPage"]:
            return pulls
        cursor = connection["pageInfo"]["endCursor"]


def fetch_pr_details(owner: str, name: str, number: int, *, quiet: bool = False) -> dict[str, Any]:
    data = gh_graphql(
        PR_DETAILS_QUERY,
        {"owner": owner, "name": name, "number": number},
        quiet=quiet,
    )
    details = data["repository"]["pullRequest"]
    details["commits"] = {"nodes": fetch_pr_commits(owner, name, number, quiet=quiet)}
    return details


def fetch_paginated_nodes(
    query: str,
    variables: dict[str, Any],
    connection_path: tuple[str, ...],
    *,
    quiet: bool = False,
) -> list[dict[str, Any]]:
    nodes: list[dict[str, Any]] = []
    cursor: str | None = None
    while True:
        data = gh_graphql(query, {**variables, "cursor": cursor}, quiet=quiet)
        connection: Any = data
        for key in connection_path:
            connection = connection[key]
        nodes.extend(connection["nodes"])
        if not connection["pageInfo"]["hasNextPage"]:
            return nodes
        cursor = connection["pageInfo"]["endCursor"]


def fetch_pr_commits(
    owner: str, name: str, number: int, *, quiet: bool = False
) -> list[dict[str, Any]]:
    return fetch_paginated_nodes(
        PR_COMMITS_QUERY,
        {"owner": owner, "name": name, "number": number},
        ("repository", "pullRequest", "commits"),
        quiet=quiet,
    )


def fetch_pr_discussion(
    owner: str, name: str, number: int, *, quiet: bool = False
) -> dict[str, Any]:
    variables = {"owner": owner, "name": name, "number": number}
    comments = fetch_paginated_nodes(
        PR_COMMENTS_QUERY,
        variables,
        ("repository", "pullRequest", "comments"),
        quiet=quiet,
    )
    reviews = fetch_paginated_nodes(
        PR_REVIEWS_QUERY,
        variables,
        ("repository", "pullRequest", "reviews"),
        quiet=quiet,
    )
    threads = fetch_paginated_nodes(
        PR_THREADS_QUERY,
        variables,
        ("repository", "pullRequest", "reviewThreads"),
        quiet=quiet,
    )
    for thread in threads:
        thread["comments"] = {
            "nodes": fetch_paginated_nodes(
                THREAD_COMMENTS_QUERY,
                {"threadId": thread["id"]},
                ("node", "comments"),
                quiet=quiet,
            )
        }
    return {
        "comments": {"nodes": comments},
        "reviews": {"nodes": reviews},
        "reviewThreads": {"nodes": threads},
    }


def fetch_all_files(
    owner: str, name: str, pr: dict[str, Any], *, quiet: bool = False
) -> list[dict[str, Any]]:
    connection = pr.get("files") or {}
    files = list(connection.get("nodes") or [])
    cursor = (connection.get("pageInfo") or {}).get("endCursor")
    while (connection.get("pageInfo") or {}).get("hasNextPage"):
        data = gh_graphql(
            FILES_QUERY,
            {"owner": owner, "name": name, "number": pr["number"], "cursor": cursor},
            quiet=quiet,
        )
        connection = data["repository"]["pullRequest"]["files"]
        files.extend(connection["nodes"])
        cursor = connection["pageInfo"]["endCursor"]
    return files


def fetch_blob(owner: str, name: str, expression: str, *, quiet: bool = False) -> str | None:
    data = gh_graphql(
        BLOB_QUERY, {"owner": owner, "name": name, "expression": expression}, quiet=quiet
    )
    blob = (data.get("repository") or {}).get("object") or {}
    return blob.get("text")


def enrich_pr(
    owner: str, name: str, pr: dict[str, Any], *, quiet: bool = False
) -> list[dict[str, Any]] | None:
    files = fetch_all_files(owner, name, pr, quiet=quiet)
    adr_files = [file for file in files if is_adr_path(file.get("path", ""))]
    if not adr_files:
        return None
    details = fetch_pr_details(owner, name, pr["number"], quiet=quiet)
    full_pr = {**pr, **details}
    authors = infer_adr_authors(full_pr)
    if not should_include_adr_pr(adr_files, authors):
        return None
    discussion = fetch_pr_discussion(owner, name, pr["number"], quiet=quiet)
    full_pr.update(discussion)
    summary_files = summary_files_for_pr(adr_files)
    summaries: list[str] = []
    head = pr.get("headRefOid")
    for file in summary_files:
        content = fetch_blob(owner, name, f"{head}:{file['path']}", quiet=quiet) if head else None
        summaries.append(adr_summary(content, file["path"]))
    common = {
        "number": pr["number"],
        "title": pr.get("title", ""),
        "url": pr["url"],
        "adr_author": authors[0]["login"] if authors else "unknown",
        "adr_author_source": authors[0]["source"] if authors else "no human attribution found",
        "adr_author_candidates": authors,
        "discussion": discussion_points(full_pr),
        "created_at": full_pr.get("createdAt"),
        "updated_at": full_pr.get("updatedAt"),
        "created": friendly_datetime(full_pr.get("createdAt")),
        "updated": friendly_datetime(full_pr.get("updatedAt")),
    }
    return [
        {
            **common,
            "adr_file": file["path"],
            "adr_change_type": file.get("changeType"),
            "adr_summary": summary,
        }
        for file, summary in zip(summary_files, summaries, strict=True)
    ]


def format_markdown(rows: list[dict[str, Any]], repo: str) -> str:
    lines = [
        "| PR | ADR | ADR author | ADR summary | Discussion / contention | "
        "Created | Last updated |",
        "|---|---|---|---|---|---|---|",
    ]
    for row in rows:
        pr = f"[PR #{row['number']}]({row['url']})"
        discussion = "<br>".join(row["discussion"]) if row["discussion"] else "—"
        cells = [
            pr,
            row["adr_file"],
            row["adr_author"],
            row["adr_summary"],
            discussion,
            row["created"],
            row["updated"],
        ]
        lines.append("| " + " | ".join(escape_markdown_cell(cell) for cell in cells) + " |")
    lines.append("")
    lines.append(
        f"_Generated {friendly_datetime(datetime.now(UTC).isoformat())} · {repo} · "
        f"{len(rows)} open ADR row(s)_"
    )
    return "\n".join(lines)


def escape_markdown_cell(value: str) -> str:
    """Escape Markdown table syntax without allowing backslashes to escape pipes."""
    return value.replace("\\", "\\\\").replace("|", "\\|").replace("\n", " ")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="List open PRs that change ADR Markdown files.")
    parser.add_argument("--repo", help="Repository as owner/name (default: current repo)")
    parser.add_argument("--format", choices=("markdown", "json"), default="markdown")
    parser.add_argument("--quiet", action="store_true", help="Suppress stderr on API failures")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> None:
    args = parse_args(argv)
    repo = resolve_repo(args.repo)
    owner, name = repo.split("/", 1)
    rows: list[dict[str, Any]] = []
    for pr in fetch_open_pulls(owner, name, quiet=args.quiet):
        rows.extend(enrich_pr(owner, name, pr, quiet=args.quiet) or [])
    rows = sort_rows_oldest_first(rows)
    if args.format == "json":
        print(
            json.dumps(
                {"repo": repo, "generated_at": datetime.now(UTC).isoformat(), "prs": rows}, indent=2
            )
        )
    else:
        print(format_markdown(rows, repo))


if __name__ == "__main__":
    main()
