#!/usr/bin/env bash
# jira-poll-record-digest.sh — Print the SHA-256 of one dispatch record
#
# Reads a JSON value on stdin and hashes its canonical form: keys sorted, no
# insignificant whitespace, strings written the way jq writes them. The Jira
# poll batch check (.github/workflows/jira-poll-batch-check.yml) hashes each
# record when it builds the batch and again in the matrix job that receives
# it. GitHub parses and re-serializes the record on the way, so the bytes
# differ, and the two digests agree only when the content is the same.
#
# Usage: jira-poll-record-digest.sh < record.json

set -euo pipefail

jq -cSj . | sha256sum | awk '{print $1}'
