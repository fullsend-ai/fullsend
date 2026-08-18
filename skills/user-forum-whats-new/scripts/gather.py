#!/usr/bin/env python3
"""Gather What's New candidates for the Fullsend user forum.

Dates are forum Tuesdays in America/New_York. --since is 08:00 ET that
morning; --until is end of that day ET, or now (UTC) when until is today
(or the until day is still in progress).

Merged PRs are classified per repo against that repo's latest in-window
release publish time (released vs on_main).
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import UTC, date, datetime, time
from pathlib import Path
from typing import Any
from zoneinfo import ZoneInfo

ET = ZoneInfo("America/New_York")
SEARCH_LIMIT = 1000
REPOS = (
    ("fullsend-ai/fullsend", "rel-fullsend.json", "prs-fullsend.json"),
    ("fullsend-ai/agents", "rel-agents.json", "prs-agents.json"),
)


def parse_iso(iso: str) -> datetime | None:
    if not iso:
        return None
    dt = datetime.fromisoformat(iso.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=UTC)
    return dt.astimezone(UTC)


def to_z(dt: datetime) -> str:
    return dt.astimezone(UTC).isoformat().replace("+00:00", "Z")


def window_bounds(
    since_day: str,
    until_day: str,
    *,
    now: datetime | None = None,
) -> tuple[datetime, datetime]:
    """Return (since_ts, until_ts) in UTC for the forum window."""
    since_d = date.fromisoformat(since_day)
    until_d = date.fromisoformat(until_day)
    since_ts = datetime.combine(since_d, time(8, 0), tzinfo=ET).astimezone(UTC)
    until_end_et = datetime.combine(until_d, time(23, 59, 59, 999999), tzinfo=ET).astimezone(UTC)
    now_utc = now if now is not None else datetime.now(UTC)
    now_utc = now_utc.replace(tzinfo=UTC) if now_utc.tzinfo is None else now_utc.astimezone(UTC)
    until_ts = min(until_end_et, now_utc)
    if until_ts < since_ts:
        raise ValueError(f"until ({until_day}) is before since ({since_day})")
    return since_ts, until_ts


def in_window(iso: str, since_ts: datetime, until_ts: datetime) -> bool:
    dt = parse_iso(iso)
    if dt is None:
        return False
    return since_ts <= dt <= until_ts


def load_json(path: Path, fallback: Any) -> Any:
    try:
        return json.loads(path.read_text())
    except Exception as e:
        sys.stderr.write(f"warning: failed to parse {path.name}: {e}\n")
        return fallback


def classify(
    releases_by_repo: dict[str, list[dict[str, Any]]],
    prs_by_repo: dict[str, list[dict[str, Any]]],
    since_ts: datetime,
    until_ts: datetime,
) -> dict[str, Any]:
    """Filter and split releases / PRs into released vs on_main per repo."""
    releases: list[dict[str, Any]] = []
    cutoffs: dict[str, datetime] = {}

    for repo, rels in releases_by_repo.items():
        times: list[datetime] = []
        for rel in rels:
            if rel.get("draft"):
                continue
            published = rel.get("published_at") or ""
            if not in_window(published, since_ts, until_ts):
                continue
            pub_dt = parse_iso(published)
            if pub_dt is not None:
                times.append(pub_dt)
            releases.append(
                {
                    "repo": repo,
                    "tag": rel.get("tag_name"),
                    "published_at": published,
                    "url": rel.get("html_url"),
                    "name": rel.get("name"),
                    # Candidate list only — never paste this as the recap.
                    "body": rel.get("body") or "",
                }
            )
        if times:
            cutoffs[repo] = max(times)

    released_prs: list[dict[str, Any]] = []
    on_main_prs: list[dict[str, Any]] = []
    for repo, prs in prs_by_repo.items():
        cutoff = cutoffs.get(repo)
        for pr in prs:
            merged_at = pr.get("closedAt") or ""
            if not in_window(merged_at, since_ts, until_ts):
                continue
            entry = {
                "repo": repo,
                "number": pr.get("number"),
                "title": pr.get("title"),
                "url": pr.get("url"),
                "merged_at": merged_at,
                "author": (pr.get("author") or {}).get("login"),
            }
            merged_dt = parse_iso(merged_at)
            if cutoff is None or merged_dt is None or merged_dt > cutoff:
                on_main_prs.append(entry)
            else:
                released_prs.append(entry)

    return {
        "releases": releases,
        "merged_prs": {
            "released": released_prs,
            "on_main": on_main_prs,
        },
        "release_cutoff_utc": {repo: to_z(ts) for repo, ts in sorted(cutoffs.items())} or None,
    }


def fetch_into(tmp: Path, since_ts: datetime, until_ts: datetime) -> None:
    merged_range = f"{to_z(since_ts)}..{to_z(until_ts)}"
    for repo, rel_name, prs_name in REPOS:
        with (tmp / rel_name).open("w", encoding="utf-8") as out:
            subprocess.run(
                [
                    "gh",
                    "api",
                    "--paginate",
                    f"repos/{repo}/releases?per_page=100",
                ],
                check=True,
                stdout=out,
            )
        with (tmp / prs_name).open("w", encoding="utf-8") as out:
            subprocess.run(
                [
                    "gh",
                    "search",
                    "prs",
                    "--repo",
                    repo,
                    "--merged",
                    "--merged-at",
                    merged_range,
                    "--limit",
                    str(SEARCH_LIMIT),
                    "--sort",
                    "created",
                    "--order",
                    "desc",
                    "--json",
                    "number,title,url,closedAt,author",
                ],
                check=True,
                stdout=out,
            )


def warn_search_overflow(tmp: Path) -> None:
    for repo, _, prs_name in REPOS:
        prs = load_json(tmp / prs_name, [])
        if isinstance(prs, list) and len(prs) >= SEARCH_LIMIT:
            sys.stderr.write(
                f"warning: {repo} hit the search limit ({SEARCH_LIMIT}); "
                "results may be incomplete\n"
            )


def build_output(
    since: str,
    until: str,
    tmp: Path,
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    since_ts, until_ts = window_bounds(since, until, now=now)
    releases_by_repo: dict[str, list[dict[str, Any]]] = {}
    prs_by_repo: dict[str, list[dict[str, Any]]] = {}
    for repo, rel_name, prs_name in REPOS:
        releases_by_repo[repo] = load_json(tmp / rel_name, [])
        prs_by_repo[repo] = load_json(tmp / prs_name, [])

    classified = classify(releases_by_repo, prs_by_repo, since_ts, until_ts)
    return {
        "since": since,
        "until": until,
        "window_start_utc": to_z(since_ts),
        "window_end_utc": to_z(until_ts),
        **classified,
    }


def today_et() -> str:
    return datetime.now(ET).date().isoformat()


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Gather What's New candidates for the Fullsend user forum."
    )
    p.add_argument(
        "--since",
        required=True,
        help="Forum Tuesday date (YYYY-MM-DD); window starts 08:00 America/New_York",
    )
    p.add_argument(
        "--until",
        default=None,
        help=(
            "End date (YYYY-MM-DD, America/New_York calendar); "
            "defaults to today in America/New_York"
        ),
    )
    return p.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    since = args.since
    until = args.until or today_et()
    date_ok = len(since) == 10 and len(until) == 10
    try:
        date.fromisoformat(since)
        date.fromisoformat(until)
    except ValueError:
        date_ok = False
    if not date_ok:
        sys.stderr.write("error: dates must be YYYY-MM-DD\n")
        return 2

    try:
        since_ts, until_ts = window_bounds(since, until)
    except ValueError as e:
        sys.stderr.write(f"error: {e}\n")
        return 2

    import tempfile

    with tempfile.TemporaryDirectory() as tmp_s:
        tmp = Path(tmp_s)
        try:
            fetch_into(tmp, since_ts, until_ts)
        except FileNotFoundError:
            sys.stderr.write("error: gh CLI required\n")
            return 1
        except subprocess.CalledProcessError as e:
            sys.stderr.write(f"error: gh command failed: {e}\n")
            return 1
        warn_search_overflow(tmp)
        out = build_output(since, until, tmp)
        print(json.dumps(out, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
