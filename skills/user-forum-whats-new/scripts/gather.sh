#!/usr/bin/env bash
# Gather What's New candidates for the Fullsend user forum.
# Usage: bash gather.sh --since YYYY-MM-DD [--until YYYY-MM-DD]
#
# Dates are forum Tuesdays in America/New_York. --since is 08:00 ET that
# morning; --until is end of that day ET, or now (UTC) when until is today.
set -euo pipefail

SINCE=""
UNTIL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since)
      if [[ $# -lt 2 || -z "${2:-}" ]]; then
        echo "error: --since requires YYYY-MM-DD" >&2
        exit 2
      fi
      SINCE="$2"
      shift 2
      ;;
    --until)
      if [[ $# -lt 2 || -z "${2:-}" ]]; then
        echo "error: --until requires YYYY-MM-DD" >&2
        exit 2
      fi
      UNTIL="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: bash gather.sh --since YYYY-MM-DD [--until YYYY-MM-DD]"
      exit 0
      ;;
    *)
      echo "Unknown arg: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$SINCE" ]]; then
  echo "error: --since YYYY-MM-DD is required" >&2
  exit 2
fi

if [[ -z "$UNTIL" ]]; then
  UNTIL="$(date +%F)"
fi

date_ok='^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
if [[ ! "$SINCE" =~ $date_ok || ! "$UNTIL" =~ $date_ok ]]; then
  echo "error: dates must be YYYY-MM-DD" >&2
  exit 2
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh CLI required" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "error: python3 required" >&2
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Paginate releases — date filtering happens in Python after full fetch.
gh api --paginate "repos/fullsend-ai/fullsend/releases?per_page=100" \
  >"$TMP/rel-fullsend.json"
gh api --paginate "repos/fullsend-ai/agents/releases?per_page=100" \
  >"$TMP/rel-agents.json"

# PR search is date-granular; Python re-filters on merged_at timestamps.
gh search prs --repo fullsend-ai/fullsend --merged \
  --merged-at "${SINCE}..${UNTIL}" --limit 100 \
  --json number,title,url,closedAt,author >"$TMP/prs-fullsend.json"

gh search prs --repo fullsend-ai/agents --merged \
  --merged-at "${SINCE}..${UNTIL}" --limit 100 \
  --json number,title,url,closedAt,author >"$TMP/prs-agents.json"

python3 - "$SINCE" "$UNTIL" "$TMP" <<'PY'
import json
import sys
from datetime import date, datetime, time, timezone
from pathlib import Path
from typing import Optional
from zoneinfo import ZoneInfo

since, until, tmp = sys.argv[1], sys.argv[2], Path(sys.argv[3])
ET = ZoneInfo("America/New_York")


def load(name, fallback):
    p = tmp / name
    try:
        return json.loads(p.read_text())
    except Exception:
        return fallback


def parse_iso(iso: str) -> Optional[datetime]:
    if not iso:
        return None
    dt = datetime.fromisoformat(iso.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def window_bounds(since_day: str, until_day: str) -> tuple[datetime, datetime]:
    since_d = date.fromisoformat(since_day)
    until_d = date.fromisoformat(until_day)
    since_ts = datetime.combine(since_d, time(8, 0), tzinfo=ET).astimezone(
        timezone.utc
    )
    until_end_et = datetime.combine(
        until_d, time(23, 59, 59, 999999), tzinfo=ET
    ).astimezone(timezone.utc)
    now_utc = datetime.now(timezone.utc)
    until_ts = min(until_end_et, now_utc)
    if until_ts < since_ts:
        raise SystemExit(
            f"error: until ({until_day}) is before since ({since_day})"
        )
    return since_ts, until_ts


def in_window(iso: str, since_ts: datetime, until_ts: datetime) -> bool:
    dt = parse_iso(iso)
    if dt is None:
        return False
    return since_ts <= dt <= until_ts


since_ts, until_ts = window_bounds(since, until)

releases = []
for repo, fname in (
    ("fullsend-ai/fullsend", "rel-fullsend.json"),
    ("fullsend-ai/agents", "rel-agents.json"),
):
    for rel in load(fname, []):
        if rel.get("draft"):
            continue
        published = rel.get("published_at") or ""
        if in_window(published, since_ts, until_ts):
            releases.append({
                "repo": repo,
                "tag": rel.get("tag_name"),
                "published_at": published,
                "url": rel.get("html_url"),
                "name": rel.get("name"),
                # Candidate list only — never paste this as the recap.
                "body": rel.get("body") or "",
            })

prs = []
for repo, fname in (
    ("fullsend-ai/fullsend", "prs-fullsend.json"),
    ("fullsend-ai/agents", "prs-agents.json"),
):
    for pr in load(fname, []):
        merged_at = pr.get("closedAt") or ""
        if not in_window(merged_at, since_ts, until_ts):
            continue
        prs.append({
            "repo": repo,
            "number": pr.get("number"),
            "title": pr.get("title"),
            "url": pr.get("url"),
            "merged_at": merged_at,
            "author": (pr.get("author") or {}).get("login"),
        })

print(json.dumps({
    "since": since,
    "until": until,
    "window_start_utc": since_ts.isoformat().replace("+00:00", "Z"),
    "window_end_utc": until_ts.isoformat().replace("+00:00", "Z"),
    "releases": releases,
    "merged_prs": prs,
}, indent=2))
PY
