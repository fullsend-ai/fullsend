# Secretarial Agent

The secretarial agent reads meeting notes from Google Drive and updates the
GitHub issue backlog — posting comments on existing issues and filing new
issues for topics not yet tracked.

See [issue #149](https://github.com/fullsend-ai/fullsend/issues/149) for the
original design discussion.

## How it works

```
Google Drive  ──→  Download & scrub PII  ──→  LLM extraction  ──→  Security gate  ──→  GitHub
(meeting notes)    (ScrubSensitiveContent)    (Vertex AI Claude)    (deterministic)     (gh CLI)
```

1. **Discover docs** — searches Google Drive for recently created meeting notes
   matching a name query (e.g., "fullsend team sync").
2. **Download & scrub** — exports each doc as plain text, then strips PII,
   secrets, and suspicious Unicode before the text reaches the LLM.
3. **Extract topics** — sends the scrubbed text + open issue backlog to Claude
   on Vertex AI. The LLM identifies which discussion topics map to existing
   issues and which warrant new issues. Falls back to regex heuristics if the
   LLM is not configured or fails.
4. **Expand new issues** — for topics that create new issues, a second LLM call
   generates a problem-focused markdown body with Problem, Options Considered,
   Acceptance Criteria, and Related sections.
5. **Security gate** — every LLM output passes through `ValidateForPublishing`,
   a deterministic gate that checks confidence, length, sensitive content, code
   blocks, and suspicious Unicode. The gate **rejects** — it never
   scrubs-and-posts.
6. **Write to GitHub** — posts comments on existing issues and files new issues
   with a `meeting-notes` + `triage` label. Idempotency check prevents
   duplicate comments.

## Running locally

```bash
export GOOGLE_APPLICATION_CREDENTIALS_JSON="$(cat /path/to/sa-key.json)"
export GH_TOKEN="$(gh auth token)"

go run ./cmd/secretarial-agent \
    --repo fullsend-ai/fullsend \
    --search-query "fullsend" \
    --gcp-project-id your-gcp-project \
    --lookback-hours 24 \
    --dry-run \
    --verbose
```

`--dry-run` previews all actions without writing to GitHub.
`--verbose` prints full topic detail to stderr (local use only — never in CI).

### CLI flags and environment variables

Every flag can also be set via an environment variable. Flags take precedence.

| Flag | Env var | Required | Description |
|------|---------|----------|-------------|
| `--repo` | `GITHUB_REPOSITORY` | Yes | GitHub repository (`owner/name`) |
| `--search-query` | `GDRIVE_SEARCH_QUERY` | Yes | Search docs by name (works with Shared Drives) |
| `--name-filter` | `GDRIVE_NAME_FILTER` | No | Only process docs whose name contains this substring |
| `--gcp-project-id` | `GCP_PROJECT_ID` | No (enables LLM) | GCP project for Vertex AI |
| `--vertex-region` | `VERTEX_REGION` | No | Vertex AI region (default `us-east5`) |
| `--llm-model` | `LLM_MODEL` | No | Claude model ID (default `claude-sonnet-4-6`) |
| `--issue-limit` | `ISSUE_LIMIT` | No | Max open issues for LLM context (default `500`) |
| `--lookback-hours` | `LOOKBACK_HOURS` | No | How far back to look for docs (default `3`) |
| `--comments-only` | `COMMENTS_ONLY` | No | Only comment on existing issues; skip new issue creation |
| `--dry-run` | `DRY_RUN` | No | Preview without writing to GitHub |
| `--verbose` | — | No | Show full topic detail (flag only, never in CI) |
| — | `GOOGLE_APPLICATION_CREDENTIALS_JSON` | Yes | GCP service-account key JSON (env only) |
| — | `GH_TOKEN` | Yes (in CI: automatic) | GitHub token for `gh` CLI (env only) |

## GitHub Actions setup

The workflow at `.github/workflows/secretarial-agent.yml` runs Mon–Thu at
16:10 UTC (shortly after the 9–11 AM ET team sync) and supports manual
dispatch with `dry_run`, `comments_only`, and `lookback_hours` inputs.

### Step 1: Create a GCP service account

The agent needs a service account with:
- **Google Drive API** read-only access (to download meeting notes)
- **Vertex AI API** access (to call Claude for topic extraction)

```bash
# Create the service account
gcloud iam service-accounts create secretarial-agent \
    --display-name="Fullsend Secretarial Agent" \
    --project=YOUR_GCP_PROJECT

# Grant Vertex AI user role
gcloud projects add-iam-policy-binding YOUR_GCP_PROJECT \
    --member="serviceAccount:secretarial-agent@YOUR_GCP_PROJECT.iam.gserviceaccount.com" \
    --role="roles/aiplatform.user"

# Create and download the key
gcloud iam service-accounts keys create sa-key.json \
    --iam-account=secretarial-agent@YOUR_GCP_PROJECT.iam.gserviceaccount.com
```

### Step 2: Grant Drive access to meeting notes

The service account needs read access to your Gemini meeting notes. The
simplest approach is to invite the service account to your recurring
meeting and configure Google Meet to auto-share notes with all attendees:

1. **Add the service account as a guest** to your recurring sync meeting
   in Google Calendar. Use the service account's email address
   (e.g., `secretarial-agent@YOUR_GCP_PROJECT.iam.gserviceaccount.com`).
2. **Configure Google Meet note sharing** — in the meeting settings, set
   the auto-share option to share notes with all invited users, including
   those outside the organization. This ensures the Gemini-generated notes
   are automatically shared with the service account after each meeting.

Once configured, every new meeting's notes will be visible to the service
account via the Drive API with no further manual sharing needed.

### Step 3: Configure GitHub secrets

Go to your repository's **Settings → Secrets and variables → Actions**.

#### Secrets (required)

| Secret name | Value |
|-------------|-------|
| `GOOGLE_APPLICATION_CREDENTIALS` | The full JSON content of the service account key file (`sa-key.json`) |
| `GCP_PROJECT_ID` | Your GCP project ID (e.g., `my-project-123`) |

`GITHUB_TOKEN` is provided automatically by GitHub Actions — no setup needed.

#### Variables (Settings → Variables → Actions)

| Variable name | Example value | Required |
|---------------|---------------|----------|
| `SECRETARIAL_GDRIVE_SEARCH_QUERY` | `fullsend team sync` | Yes |
| `SECRETARIAL_GDRIVE_NAME_FILTER` | `Notes by Gemini` | Optional |
| `SECRETARIAL_VERTEX_REGION` | `us-east5` | Optional (default: `us-east5`) |
| `SECRETARIAL_LLM_MODEL` | `claude-sonnet-4-6` | Optional |
| `SECRETARIAL_ISSUE_LIMIT` | `500` | Optional |

### Step 4: Enable the workflow

The workflow is disabled by default on new forks. Enable it:
1. Go to **Actions** tab in your repository
2. Find "Secretarial Agent — Meeting Notes to Backlog"
3. Click "Enable workflow"

### Step 5: Test with a manual run

Trigger a manual run with `dry_run: true` first:
1. Go to **Actions → Secretarial Agent**
2. Click "Run workflow"
3. Set `dry_run` to `true`
4. Click "Run workflow"
5. Check the logs — you should see topic extraction and the actions it
   *would* take without any writes to GitHub

Once satisfied, run with `dry_run: false`.

## Security model

The agent treats LLM output as untrusted. Security is enforced at three layers:

1. **Input scrubbing** (`ScrubSensitiveContent`) — regex-based removal of
   emails, phone numbers, IPs, SSNs, API keys, tokens, JWTs, PEM keys, Slack
   webhooks, and suspicious Unicode before the text reaches the LLM.

2. **Deterministic gate** (`ValidateForPublishing`) — every extracted topic
   must pass through this gate before any GitHub write. The gate checks:
   - Confidence threshold (≥ 0.6)
   - Sensitive content in summary, title, and new issue title
   - Code blocks in comments (allowed in new issue bodies)
   - Summary length (≤ 2,000 for comments, ≤ 15,000 for new issue bodies)
   - Issue title length (≤ 200)
   - Suspicious Unicode (zero-width chars, tag characters)

   The gate **rejects** — it never redacts-and-posts. Over-rejection is the
   safe failure mode.

3. **CI log safety** — the `--verbose` flag is never set in CI. Without it,
   meeting content, doc names, and topic details are never logged. Only
   aggregate counts appear in public CI logs.

## Tests

```bash
go test ./internal/secretarial/... -count=1 -v
```

56 tests covering: PII scrubbing, JSON parsing, security gate rules (including
differential limits for comments vs new issue bodies), heuristic extraction,
deduplication, issue context formatting, and helper functions.

## Architecture notes

- **Two-pass LLM extraction** — the first pass extracts topics with brief
  summaries in a reliable JSON format. For new issues, a second pass generates
  the full markdown body as raw markdown (no JSON wrapping), avoiding the JSON
  escaping problems that occur with complex multi-line markdown in JSON strings.

- **LLM retry** — if the first extraction pass returns invalid JSON, the agent
  retries once, sending the failed response as context with a reinforced prompt.

- **Google Drive uses `createdTime`** — not `modifiedTime` — because meeting
  notes are created once per meeting. Filtering by `modifiedTime` would
  reprocess old docs whenever anyone views or organizes them.

- **Idempotency** — before posting a comment, the agent checks existing
  comments for the meeting notes URL. If already posted, it skips.
