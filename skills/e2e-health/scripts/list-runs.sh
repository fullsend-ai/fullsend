#!/usr/bin/env bash
set -euo pipefail

SINCE=$(date -d "${1:-2 days ago}" +%Y-%m-%d)

gh run list \
  --workflow=e2e.yml \
  --branch=main \
  --created=">=$SINCE" \
  --limit=500 \
  --json databaseId,displayTitle,conclusion,status,createdAt,url
