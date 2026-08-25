#!/usr/bin/env python3
"""Gather What's New candidates for the Fullsend user forum.

Dates are forum Tuesdays in America/New_York. --since is 08:00 ET that
morning; --until is end of that day ET, clamped to now when that end is
still in the future (until_clamped in JSON).

Merged PRs are classified per repo against that repo's latest in-window
release publish time (released vs on_main).
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys

if sys.version_info < (3, 11):  # noqa: UP036 — skill may run outside this repo (symlink hosts)
    sys.stderr.write(
        f"error: Python 3.11+ required (found {sys.version.split()[0]}); "
        "gather.py uses datetime.UTC and zoneinfo America/New_York.\n"
    )
    raise SystemExit(1)

from datetime import UTC, date, datetime, time
from pathlib import Path
from typing import Any
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

try:
    ET = ZoneInfo("America/New_York")
except ZoneInfoNotFoundError as e:
    sys.stderr.write(
        "error: America/New_York timezone data is missing "
        f"({e}). Install your distro's IANA timezone data package "
        "(commonly `tzdata`) or `pip install tzdata`.\n"
    )
    raise SystemExit(1) from e

SEARCH_LIMIT = 1000
# Fail fast on hung gh network / credential prompts (four calls in fetch_into).
GH_TIMEOUT_SEC = 120
REPOS = (
    ("fullsend-ai/fullsend", "rel-fullsend.json", "prs-fullsend.json"),
    ("fullsend-ai/agents", "rel-agents.json", "prs-agents.json"),
)


def parse_ymd(day: str) -> date:
    """Strict YYYY-MM-DD (rejects ISO week-date / ordinal forms)."""
    return datetime.strptime(day, "%Y-%m-%d").date()


def parse_iso(iso: str) -> datetime | None:
    if not iso:
        return None
    try:
        dt = datetime.fromisoformat(str(iso).replace("Z", "+00:00"))
    except (ValueError, TypeError) as e:
        sys.stderr.write(f"warning: skipping malformed timestamp {iso!r}: {e}\n")
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=UTC)
    return dt.astimezone(UTC)


def to_z(dt: datetime) -> str:
    """UTC timestamp for GitHub search / JSON — seconds precision, Z suffix.

    GitHub search documents YYYY-MM-DDTHH:MM:SS+00:00 (not fractional
    seconds). Keep Z; both Z and +00:00 are accepted today.
    """
    return dt.astimezone(UTC).isoformat(timespec="seconds").replace("+00:00", "Z")


def search_merged_at_range(since_ts: datetime, until_ts: datetime) -> str:
    """Documented --merged-at range for gh search prs."""
    return f"{to_z(since_ts)}..{to_z(until_ts)}"


def window_bounds(
    since_day: str,
    until_day: str,
    *,
    now: datetime | None = None,
) -> tuple[datetime, datetime, bool]:
    """Return (since_ts, until_ts, until_clamped) in UTC for the forum window.

    until_ts is always min(ET end-of-day for until_day, now). That clamps
    any future --until (not only "today") so the gather never pretends to
    cover time that has not happened yet. until_clamped is True when that
    min chose now instead of the calendar end-of-day.
    """
    since_d = parse_ymd(since_day)
    until_d = parse_ymd(until_day)
    since_ts = datetime.combine(since_d, time(8, 0), tzinfo=ET).astimezone(UTC)
    until_end_et = datetime.combine(until_d, time(23, 59, 59, 999999), tzinfo=ET).astimezone(UTC)
    now_utc = now if now is not None else datetime.now(UTC)
    now_utc = now_utc.replace(tzinfo=UTC) if now_utc.tzinfo is None else now_utc.astimezone(UTC)
    until_clamped = until_end_et > now_utc
    until_ts = now_utc if until_clamped else until_end_et
    if until_ts < since_ts:
        raise ValueError(f"until ({until_day}) is before since ({since_day})")
    return since_ts, until_ts, until_clamped


def in_window(iso: str, since_ts: datetime, until_ts: datetime) -> bool:
    """True if event is after since_ts and at/before until_ts (half-open).

    Exclusive lower bound avoids double-counting events at the shared
    Tuesday 08:00 ET boundary across consecutive weekly gathers.
    """
    dt = parse_iso(iso)
    if dt is None:
        return False
    return since_ts < dt <= until_ts


def flatten_gh_slurp(data: Any) -> list[Any]:
    """Normalize `gh api --paginate --slurp` output to a flat object list.

    --slurp wraps each page in an outer array, so multi-page (and some
    single-page) responses arrive as [[obj, ...], ...]. Flat [obj, ...] is
    also accepted.
    """
    if not isinstance(data, list):
        raise RuntimeError(f"expected JSON array from gh, got {type(data).__name__}")
    if data and all(isinstance(x, list) for x in data):
        return [item for page in data for item in page]
    return data


def load_json(path: Path, fallback: Any = None, *, required: bool = False) -> Any:
    """Load JSON from path as UTF-8.

    When required=True (mandatory fetch outputs), parse/shape failures raise
    RuntimeError instead of returning a quiet empty fallback.
    """
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception as e:
        if required:
            raise RuntimeError(f"failed to parse required file {path.name}: {e}") from e
        sys.stderr.write(f"warning: failed to parse {path.name}: {e}\n")
        return [] if fallback is None else fallback
    if required and not isinstance(data, list):
        raise RuntimeError(
            f"required file {path.name} must be a JSON array, got {type(data).__name__}"
        )
    return data


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
        if not isinstance(rels, list):
            sys.stderr.write(f"warning: releases for {repo} are not a list; skipping\n")
            continue
        times: list[datetime] = []
        for rel in rels:
            if not isinstance(rel, dict):
                continue
            if rel.get("draft"):
                continue
            published = rel.get("published_at") or ""
            if not in_window(published, since_ts, until_ts):
                continue
            is_prerelease = bool(rel.get("prerelease"))
            pub_dt = parse_iso(published)
            # Prereleases stay visible as candidates, but never set the
            # Released/On-main cutoff (rc timestamps must not mark PRs shipped).
            if pub_dt is not None and not is_prerelease:
                times.append(pub_dt)
            releases.append(
                {
                    "repo": repo,
                    "tag": rel.get("tag_name"),
                    "published_at": published,
                    "url": rel.get("html_url"),
                    "name": rel.get("name"),
                    "prerelease": is_prerelease,
                    # Candidate list only — never paste this as the recap.
                    "body": rel.get("body") or "",
                }
            )
        if times:
            cutoffs[repo] = max(times)

    released_prs: list[dict[str, Any]] = []
    on_main_prs: list[dict[str, Any]] = []
    for repo, prs in prs_by_repo.items():
        if not isinstance(prs, list):
            sys.stderr.write(f"warning: PRs for {repo} are not a list; skipping\n")
            continue
        cutoff = cutoffs.get(repo)
        for pr in prs:
            if not isinstance(pr, dict):
                continue
            merged_at = pr.get("closedAt") or ""
            if not in_window(merged_at, since_ts, until_ts):
                continue
            author = pr.get("author") or {}
            author_login = author.get("login") if isinstance(author, dict) else None
            entry = {
                "repo": repo,
                "number": pr.get("number"),
                "title": pr.get("title"),
                "url": pr.get("url"),
                "merged_at": merged_at,
                "author": author_login,
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
    merged_range = search_merged_at_range(since_ts, until_ts)
    for repo, rel_name, prs_name in REPOS:
        with (tmp / rel_name).open("w", encoding="utf-8") as out:
            subprocess.run(
                [
                    "gh",
                    "api",
                    "--paginate",
                    "--slurp",
                    f"repos/{repo}/releases?per_page=100",
                ],
                check=True,
                stdout=out,
                timeout=GH_TIMEOUT_SEC,
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
                    "--base",
                    "main",
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
                timeout=GH_TIMEOUT_SEC,
            )


def warn_search_overflow(tmp: Path) -> None:
    for repo, _, prs_name in REPOS:
        # Same mandatory files as build_output — fail closed on corrupt payloads.
        prs = load_json(tmp / prs_name, required=True)
        if len(prs) >= SEARCH_LIMIT:
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
    since_ts, until_ts, until_clamped = window_bounds(since, until, now=now)
    releases_by_repo: dict[str, list[dict[str, Any]]] = {}
    prs_by_repo: dict[str, list[dict[str, Any]]] = {}
    for repo, rel_name, prs_name in REPOS:
        releases_by_repo[repo] = flatten_gh_slurp(load_json(tmp / rel_name, required=True))
        prs_by_repo[repo] = load_json(tmp / prs_name, required=True)

    classified = classify(releases_by_repo, prs_by_repo, since_ts, until_ts)
    return {
        "since": since,
        "until": until,
        "until_clamped": until_clamped,
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
            "defaults to today in America/New_York. Window end is "
            "min(ET end-of-day, now) — future dates are clamped to now "
            "(see until_clamped in JSON output)."
        ),
    )
    return p.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    since = args.since
    until = args.until or today_et()
    try:
        parse_ymd(since)
        parse_ymd(until)
    except ValueError:
        sys.stderr.write("error: dates must be YYYY-MM-DD\n")
        return 2

    now = datetime.now(UTC)
    try:
        since_ts, until_ts, _until_clamped = window_bounds(since, until, now=now)
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
        except subprocess.TimeoutExpired as e:
            sys.stderr.write(f"error: gh command timed out after {GH_TIMEOUT_SEC}s: {e.cmd}\n")
            return 1
        except subprocess.CalledProcessError as e:
            sys.stderr.write(f"error: gh command failed: {e}\n")
            return 1
        try:
            warn_search_overflow(tmp)
            out = build_output(since, until, tmp, now=now)
        except RuntimeError as e:
            sys.stderr.write(f"error: {e}\n")
            return 1
        print(json.dumps(out, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
