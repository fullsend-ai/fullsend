#!/usr/bin/env bash
# Gather What's New candidates for the Fullsend user forum.
# Usage: bash gather.sh --since YYYY-MM-DD [--until YYYY-MM-DD]
set -euo pipefail

SINCE=""
UNTIL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) SINCE="${2:-}"; shift 2 ;;
    --until) UNTIL="${2:-}"; shift 2 ;;
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

gh api "repos/fullsend-ai/fullsend/releases?per_page=20" >"$TMP/rel-fullsend.json"
gh api "repos/fullsend-ai/agents/releases?per_page=20" >"$TMP/rel-agents.json"

# Merged PRs in the window (GitHub search date is inclusive).
# Limit 100: a busy week plus a release can exceed 50 per repo.
gh search prs --repo fullsend-ai/fullsend --merged \
  --merged-at "${SINCE}..${UNTIL}" --limit 100 \
  --json number,title,url,closedAt,author >"$TMP/prs-fullsend.json"

gh search prs --repo fullsend-ai/agents --merged \
  --merged-at "${SINCE}..${UNTIL}" --limit 100 \
  --json number,title,url,closedAt,author >"$TMP/prs-agents.json"

python3 - "$SINCE" "$UNTIL" "$TMP" <<'PY'
import json, sys
from pathlib import Path

since, until, tmp = sys.argv[1], sys.argv[2], Path(sys.argv[3])

def load(name, fallback):
    p = tmp / name
    try:
        return json.loads(p.read_text())
    except Exception:
        return fallback

def in_window(iso):
    if not iso:
        return False
    day = iso[:10]
    return since <= day <= until

releases = []
for repo, fname in (
    ("fullsend-ai/fullsend", "rel-fullsend.json"),
    ("fullsend-ai/agents", "rel-agents.json"),
):
    for rel in load(fname, []):
        if rel.get("draft"):
            continue
        published = rel.get("published_at") or ""
        if in_window(published):
            body = rel.get("body") or ""
            releases.append({
                "repo": repo,
                "tag": rel.get("tag_name"),
                "published_at": published,
                "url": rel.get("html_url"),
                "name": rel.get("name"),
                # Candidate list only — never paste this as the recap.
                "body": body[:8000],
            })

prs = []
for repo, fname in (
    ("fullsend-ai/fullsend", "prs-fullsend.json"),
    ("fullsend-ai/agents", "prs-agents.json"),
):
    for pr in load(fname, []):
        prs.append({
            "repo": repo,
            "number": pr.get("number"),
            "title": pr.get("title"),
            "url": pr.get("url"),
            "merged_at": pr.get("closedAt"),
            "author": (pr.get("author") or {}).get("login"),
        })

print(json.dumps({
    "since": since,
    "until": until,
    "releases": releases,
    "merged_prs": prs,
}, indent=2))
PY
