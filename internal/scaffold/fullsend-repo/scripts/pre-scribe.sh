#!/usr/bin/env bash
# pre-scribe.sh — Fetch meeting notes from Google Drive, scrub PII, prepare
# workspace for the scribe agent.
#
# Runs on the host before the sandbox starts. Downloads recent meeting notes,
# strips sensitive content, and prepares input files the agent will read.
#
# Required env vars:
#   SCRIBE_REPO           — GitHub repository (owner/name)
#   SCRIBE_SEARCH_QUERY   — Drive doc name search (e.g. "team sync")
#   GH_TOKEN              — GitHub token for backlog fetch
#   GOOGLE_APPLICATION_CREDENTIALS — path to GCP SA credentials
#
# Optional env vars:
#   SCRIBE_NAME_FILTER    — substring filter on doc names
#   SCRIBE_LOOKBACK_HOURS — how far back to search (default: 3)

set -euo pipefail

WORK_DIR="${RUNNER_TEMP:-/tmp}/scribe-workspace"
NOTES_DIR="${WORK_DIR}/notes"
BACKLOG_FILE="${WORK_DIR}/backlog.json"
META_FILE="${WORK_DIR}/scribe-meta.json"

mkdir -p "${NOTES_DIR}"

LOOKBACK="${SCRIBE_LOOKBACK_HOURS:-3}"
# RFC3339 with Z suffix — matches the Go code's time.RFC3339 format
CUTOFF_DATE=$(date -u -d "${LOOKBACK} hours ago" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null \
  || date -u -v-"${LOOKBACK}"H +"%Y-%m-%dT%H:%M:%SZ")

echo "Scribe pre-script: searching Drive for docs matching '${SCRIBE_SEARCH_QUERY}' since ${CUTOFF_DATE}"

# ============================================================
# Repo context — issues, PRs, and doc index
# ============================================================

CLOSED_ISSUES_FILE="${WORK_DIR}/closed-issues.json"
OPEN_PRS_FILE="${WORK_DIR}/open-prs.json"
REPO_DOCS_FILE="${WORK_DIR}/repo-docs-index.json"

# Open issues with bodies (truncated to 500 chars to keep context lean)
echo "Fetching open issues from ${SCRIBE_REPO}..."
gh issue list --repo "${SCRIBE_REPO}" --state open \
  --json number,title,body,labels,milestone,url --limit 500 \
  | jq '[.[] | .body = ((.body // "")[:500] + if ((.body // "") | length) > 500 then "…" else "" end)]' \
  > "${BACKLOG_FILE}"
ISSUE_COUNT=$(jq 'length' "${BACKLOG_FILE}")
echo "Fetched ${ISSUE_COUNT} open issues for backlog context."

# Recently closed issues (last 50) — helps avoid duplicates and enables
# "this was resolved in #N" references
echo "Fetching recently closed issues..."
gh issue list --repo "${SCRIBE_REPO}" --state closed \
  --json number,title,labels,url --limit 50 \
  > "${CLOSED_ISSUES_FILE}"
CLOSED_COUNT=$(jq 'length' "${CLOSED_ISSUES_FILE}")
echo "Fetched ${CLOSED_COUNT} recently closed issues."

# Open PRs — gives awareness of in-flight work so the agent can link
# meeting topics about "the caching PR" to actual PR numbers
echo "Fetching open pull requests..."
gh pr list --repo "${SCRIBE_REPO}" --state open \
  --json number,title,labels,url,headRefName --limit 100 \
  > "${OPEN_PRS_FILE}"
PR_COUNT=$(jq 'length' "${OPEN_PRS_FILE}")
echo "Fetched ${PR_COUNT} open pull requests."

# Repo doc index — ADRs, problem docs, guides. One API call using the
# git tree (recursive) so the agent can reference docs by path.
echo "Fetching repo doc index..."
gh api "repos/${SCRIBE_REPO}/git/trees/main?recursive=1" \
  --jq '[.tree[] | select(.path | startswith("docs/") and (.path | endswith(".md"))) | .path]' \
  > "${REPO_DOCS_FILE}" 2>/dev/null || echo '[]' > "${REPO_DOCS_FILE}"
DOC_PATH_COUNT=$(jq 'length' "${REPO_DOCS_FILE}")
echo "Indexed ${DOC_PATH_COUNT} doc paths from repo tree."

# --- Obtain Drive-scoped access token ---
# The Drive API is a Workspace API that requires its own OAuth scope
# (drive.readonly). The default cloud-platform scope from gcloud doesn't
# cover it. Mint a Drive-scoped token from the SA key using a signed JWT,
# matching what the Go code does with google.CredentialsFromJSON.
# SCRIBE_DRIVE_CREDENTIALS points to the SA key that has been invited to the
# Google Calendar meeting (separate from the Vertex AI SA).
SA_KEY_FILE="${SCRIBE_DRIVE_CREDENTIALS:-${GOOGLE_APPLICATION_CREDENTIALS:-}}"
if [[ -z "${SA_KEY_FILE}" || ! -f "${SA_KEY_FILE}" ]]; then
  echo "ERROR: neither SCRIBE_DRIVE_CREDENTIALS nor GOOGLE_APPLICATION_CREDENTIALS is set or file missing"
  exit 1
fi

SA_EMAIL=$(jq -r '.client_email' "${SA_KEY_FILE}")
echo "::add-mask::${SA_EMAIL}"
NOW=$(date +%s)
EXP=$((NOW + 3600))

JWT_HEADER=$(printf '{"alg":"RS256","typ":"JWT"}' | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
JWT_CLAIMS=$(printf '{"iss":"%s","scope":"https://www.googleapis.com/auth/drive.readonly","aud":"https://oauth2.googleapis.com/token","exp":%d,"iat":%d}' \
  "${SA_EMAIL}" "${EXP}" "${NOW}" | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
JWT_SIGNATURE=$(printf '%s.%s' "${JWT_HEADER}" "${JWT_CLAIMS}" \
  | openssl dgst -sha256 -sign <(jq -r '.private_key' "${SA_KEY_FILE}") \
  | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')

JWT_ASSERTION="${JWT_HEADER}.${JWT_CLAIMS}.${JWT_SIGNATURE}"
echo "::add-mask::${JWT_ASSERTION}"

TOKEN_RESPONSE=$(printf 'grant_type=urn%%3Aietf%%3Aparams%%3Aoauth%%3Agrant-type%%3Ajwt-bearer&assertion=%s' "${JWT_ASSERTION}" \
  | curl -fsSL -X POST https://oauth2.googleapis.com/token \
    -H "Content-Type: application/x-www-form-urlencoded" \
    --data-binary @- 2>/dev/null)
TOKEN_CURL_RC=$?

unset JWT_ASSERTION JWT_HEADER JWT_CLAIMS JWT_SIGNATURE

ACCESS_TOKEN=$(printf '%s' "${TOKEN_RESPONSE}" | jq -r '.access_token // empty')
if [[ ${TOKEN_CURL_RC} -ne 0 ]] || [[ -z "${ACCESS_TOKEN}" ]]; then
  echo "ERROR: could not obtain Drive-scoped access token"
  TOKEN_ERROR=$(printf '%s' "${TOKEN_RESPONSE}" | jq -r '.error // .error_description // "unknown error"' 2>/dev/null || echo "non-JSON response")
  echo "Token error: ${TOKEN_ERROR}"
  unset TOKEN_RESPONSE
  exit 1
fi
echo "::add-mask::${ACCESS_TOKEN}"
unset TOKEN_RESPONSE
echo "Obtained Drive-scoped access token"

# --- Search Google Drive for meeting notes ---
ESCAPED_QUERY=$(printf '%s' "${SCRIBE_SEARCH_QUERY}" | sed "s/'/\\\\'/g")
QUERY="name contains '${ESCAPED_QUERY}' and mimeType = 'application/vnd.google-apps.document' and trashed = false and createdTime > '${CUTOFF_DATE}'"

if [[ -n "${SCRIBE_NAME_FILTER:-}" ]]; then
  ESCAPED_FILTER=$(printf '%s' "${SCRIBE_NAME_FILTER}" | sed "s/'/\\\\'/g")
  QUERY="${QUERY} and name contains '${ESCAPED_FILTER}'"
fi

echo "Drive query: ${QUERY}"
ENCODED_QUERY=$(printf '%s' "${QUERY}" | jq -sRr @uri)
DRIVE_URL="https://www.googleapis.com/drive/v3/files?q=${ENCODED_QUERY}&fields=files(id,name,createdTime,modifiedTime,webViewLink)&orderBy=createdTime+desc&pageSize=20&supportsAllDrives=true&includeItemsFromAllDrives=true"

# Do NOT use -f here — we want to see error responses from the API
DRIVE_HTTP_CODE=$(curl -sS -o "${WORK_DIR}/drive-response.json" -w '%{http_code}' \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  "${DRIVE_URL}")

echo "Drive API HTTP status: ${DRIVE_HTTP_CODE}"

if [[ "${DRIVE_HTTP_CODE}" != "200" ]]; then
  echo "ERROR: Drive API returned non-200 status"
  echo "Response body:"
  cat "${WORK_DIR}/drive-response.json"
  exit 1
fi

DOC_COUNT=$(jq '.files | length' "${WORK_DIR}/drive-response.json")
echo "Found ${DOC_COUNT} matching document(s)"

if [[ "${DOC_COUNT}" -gt 0 ]]; then
  jq -r '.files[] | "  \(.name) (created: \(.createdTime))"' "${WORK_DIR}/drive-response.json"
fi

if [[ "${DOC_COUNT}" -eq 0 ]]; then
  echo "No documents found — agent will produce empty result."
  rm -f "${WORK_DIR}/drive-response.json"
  unset ACCESS_TOKEN SA_EMAIL
  jq -n \
    --arg cutoff "${CUTOFF_DATE}" \
    --arg repo "${SCRIBE_REPO}" \
    --argjson doc_count 0 \
    --argjson issue_count "${ISSUE_COUNT}" \
    --argjson closed_count "${CLOSED_COUNT}" \
    --argjson pr_count "${PR_COUNT}" \
    --argjson doc_path_count "${DOC_PATH_COUNT}" \
    '{cutoff_date: $cutoff, notes_url: "", repo: $repo, docs_downloaded: $doc_count, backlog_issues: $issue_count, closed_issues: $closed_count, open_prs: $pr_count, repo_docs: $doc_path_count}' \
    > "${META_FILE}"
  echo "Workspace: ${WORK_DIR}"
  exit 0
fi

MAX_DOC_BYTES=$((2 * 1024 * 1024))  # 2 MiB cap per document

export_doc_with_retry() {
  local doc_id="$1" attempt max_attempts=3
  for attempt in 1 2 3; do
    local http_code body_file="${WORK_DIR}/doc-export-tmp"
    http_code=$(curl -sS -o "${body_file}" -w '%{http_code}' \
      -H "Authorization: Bearer ${ACCESS_TOKEN}" \
      "https://www.googleapis.com/drive/v3/files/${doc_id}/export?mimeType=text/plain" \
      2>/dev/null || echo "000")

    if [[ "${http_code}" == "200" ]]; then
      local file_size
      file_size=$(wc -c < "${body_file}")
      if [[ "${file_size}" -gt "${MAX_DOC_BYTES}" ]]; then
        echo "  WARNING: doc ${doc_id} exceeds ${MAX_DOC_BYTES} byte limit (${file_size}), skipping"
        rm -f "${body_file}"
        return 1
      fi
      cat "${body_file}"
      rm -f "${body_file}"
      return 0
    fi

    rm -f "${body_file}"
    if [[ "${http_code}" =~ ^5 ]] && [[ "${attempt}" -lt "${max_attempts}" ]]; then
      local wait=$((1 << (attempt - 1)))
      echo "  WARNING: Drive export returned ${http_code}, retrying in ${wait}s (attempt ${attempt}/${max_attempts})"
      sleep "${wait}"
      continue
    fi

    echo "  WARNING: Drive export failed with HTTP ${http_code} after ${attempt} attempt(s)"
    return 1
  done
  return 1
}

DOC_INDEX=0
DOCS_FAILED=0
while read -r doc; do
  DOC_ID=$(echo "${doc}" | jq -r '.id')
  DOC_NAME=$(echo "${doc}" | jq -r '.name')
  DOC_URL=$(echo "${doc}" | jq -r '.webViewLink')

  echo "  Downloading: ${DOC_NAME}"

  RAW_TEXT=$(export_doc_with_retry "${DOC_ID}")
  if [[ $? -ne 0 ]] || [[ -z "${RAW_TEXT}" ]]; then
    echo "  WARNING: could not export doc ${DOC_ID}, skipping"
    DOCS_FAILED=$((DOCS_FAILED + 1))
    continue
  fi

  # --- Suspicious Unicode removal (prompt injection defense) ---
  # Strip tag characters (U+E0000–E007F), zero-width chars, BOM, bidi overrides
  CLEAN_UNICODE=$(printf '%s' "${RAW_TEXT}" \
    | perl -CS -pe 's/[\x{E0000}-\x{E007F}\x{200B}\x{200C}\x{200D}\x{FEFF}\x{202A}-\x{202E}\x{2066}-\x{2069}]//g')

  # --- Structural scrubbing (Gemini meeting notes format) ---
  # Gemini notes have: Summary (safe, uses "the team"/"participants"),
  # Next steps (has [Person Name] attributions), and Details (near-verbatim
  # transcript with extensive per-person attributions). The Details section
  # is the primary leakage risk — it's essentially a private transcript
  # with statements attributed to named individuals.
  #
  # Strategy: keep Summary + Next steps (with names stripped), drop Details
  # and everything after it (transcript, timestamps, editor boilerplate).
  STRUCTURAL_SCRUB=$(printf '%s' "${CLEAN_UNICODE}" \
    | tr -d '\r' \
    | sed -E '/^Invited /d' \
    | sed -E '/^Attendees:?/d' \
    | sed -E '/^Participants:?$/d' \
    | sed -E 's/^(Organizer|Host|Co-host):?.*/[meeting role line removed]/g' \
    | sed -n '/^Details/,$!p' \
    | sed -E 's/\[[A-Z][a-zA-Z .,-]+\]/[attendee]/g')

  # --- PII pattern scrubbing ---
  # Ordered: specific patterns first, generic last (matches Go sanitizer.go)
  SCRUBBED=$(echo "${STRUCTURAL_SCRUB}" \
    | sed -E 's/\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/[REDACTED]/g' \
    | sed -E 's/\b(\+?1[-. ]?)?\(?\d{3}\)?[-. ]?\d{3}[-. ]?\d{4}\b/[REDACTED]/g' \
    | sed -E 's/\+\d{1,3}[-. ]?\d{4,14}\b/[REDACTED]/g' \
    | sed -E 's/\b([0-9]{1,3}\.){3}[0-9]{1,3}\b/[REDACTED]/g' \
    | sed -E 's/\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b/[REDACTED]/g' \
    | sed -E 's/\b([0-9][ -]?){13,19}\b/[REDACTED]/g' \
    | sed -E 's/\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b/[REDACTED]/g' \
    | sed -E 's/[Aa][Ww][Ss].?[Ss][Ee][Cc][Rr][Ee][Tt].?([Aa][Cc][Cc][Ee][Ss][Ss])?.?[Kk][Ee][Yy][[:space:]]*[:=][[:space:]]*[A-Za-z0-9\/+=]{40}/[REDACTED]/g' \
    | sed -E 's/\b(ghp|gho|ghs|ghr)_[A-Za-z0-9_]{36,255}\b/[REDACTED]/g' \
    | sed -E 's|https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+|[REDACTED]|g' \
    | sed -E 's/-----BEGIN[[:space:]]+(RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----.*/[REDACTED]/g' \
    | sed -E 's/[Pp][Rr][Ii][Vv][Aa][Tt][Ee]_[Kk][Ee][Yy]_[Ii][Dd][[:space:]]*[:=][[:space:]]*['"'"'"]?[a-f0-9]{40}['"'"'"]?/[REDACTED]/g' \
    | sed -E 's/\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/[REDACTED]/g' \
    | sed -E 's/([Aa][Pp][Ii][_-]?[Kk][Ee][Yy]|[Tt][Oo][Kk][Ee][Nn]|[Ss][Ee][Cc][Rr][Ee][Tt]|[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]|[Bb][Ee][Aa][Rr][Ee][Rr])[[:space:]]*[:=][[:space:]]*['"'"'"]?[A-Za-z0-9_.~+\/-]{20,}['"'"'"]?/[REDACTED]/g')

  echo "${SCRUBBED}" > "${NOTES_DIR}/doc-${DOC_INDEX}.txt"
  echo "${DOC_URL}" > "${NOTES_DIR}/doc-${DOC_INDEX}.url"

  DOC_INDEX=$((DOC_INDEX + 1))
done < <(jq -c '.files[]' "${WORK_DIR}/drive-response.json")

if [[ "${DOCS_FAILED}" -gt 0 ]]; then
  echo "WARNING: ${DOCS_FAILED} doc(s) failed to export (continued with remaining)"
fi

NOTES_URL=""
if [[ -f "${NOTES_DIR}/doc-0.url" ]]; then
  NOTES_URL=$(cat "${NOTES_DIR}/doc-0.url")
fi

jq -n \
  --arg cutoff "${CUTOFF_DATE}" \
  --arg notes_url "${NOTES_URL}" \
  --arg repo "${SCRIBE_REPO}" \
  --argjson doc_count "${DOC_COUNT}" \
  --argjson issue_count "${ISSUE_COUNT}" \
  --argjson closed_count "${CLOSED_COUNT}" \
  --argjson pr_count "${PR_COUNT}" \
  --argjson doc_path_count "${DOC_PATH_COUNT}" \
  '{
    cutoff_date: $cutoff,
    notes_url: $notes_url,
    repo: $repo,
    docs_downloaded: $doc_count,
    backlog_issues: $issue_count,
    closed_issues: $closed_count,
    open_prs: $pr_count,
    repo_docs: $doc_path_count
  }' > "${META_FILE}"

# Cleanup: remove Drive API response (contains doc IDs and metadata)
rm -f "${WORK_DIR}/drive-response.json" "${WORK_DIR}/doc-export-tmp"
unset ACCESS_TOKEN SA_EMAIL

echo "Pre-scribe complete. ${DOC_COUNT} docs scraped, ${ISSUE_COUNT} issues + ${CLOSED_COUNT} closed + ${PR_COUNT} PRs + ${DOC_PATH_COUNT} doc paths."
echo "Workspace: ${WORK_DIR}"
