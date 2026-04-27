# Scribe Agent

The scribe agent reads meeting notes from Google Drive and updates the
GitHub issue backlog — posting comments on existing issues and filing new
issues for topics not yet tracked.

Renamed from "secretarial agent" per [issue #222](https://github.com/fullsend-ai/fullsend/issues/222).
See [issue #149](https://github.com/fullsend-ai/fullsend/issues/149) for the
original design discussion.

## Architecture

The scribe follows the standard fullsend agent lifecycle (harness, sandbox,
pre/post scripts) rather than running as a standalone binary. This keeps it
installable and configurable the same way as triage, code, and review agents.

```
pre-scribe.sh          agents/scribe.md           post-scribe.sh
─────────────          ────────────────           ──────────────
Google Drive ──→       LLM extraction     ──→     Security gate ──→ GitHub
fetch + PII scrub      (sandbox, Vertex AI)        (deterministic)    (gh CLI)
```

### Pre-script (`scripts/pre-scribe.sh`)

Runs on the host before the sandbox starts:
1. Authenticates to Google Drive via Application Default Credentials
   (set up by `google-github-actions/auth@v3` in the workflow)
2. Searches for recent meeting notes matching the configured query
3. Exports each doc as plain text
4. Strips PII, secrets, and suspicious Unicode via regex scrubbing
5. Fetches the open issue backlog via `gh issue list`
6. Writes cleaned notes + backlog JSON to the workspace

### Agent (`agents/scribe.md`)

Runs inside the OpenShell sandbox via `fullsend run scribe`:
1. Reads the cleaned meeting notes and backlog from the workspace
2. Identifies discussion topics, matches to existing issues or proposes new ones
3. Applies recency, public-appropriateness, and substance filters
4. Writes structured JSON to `agent-result.json`

The agent prompt consolidates the extraction logic that was previously
split across `extract.go` (system prompts) and `llm.go` (API calls) in the
prototype PR #219.

### Post-script (`scripts/post-scribe.sh`)

Runs on the host after sandbox cleanup:
1. Parses the agent's JSON output
2. Applies the deterministic security gate:
   - Confidence threshold (see [Confidence threshold](#confidence-threshold) below)
   - Sensitive content detection (PII, secrets)
   - Length limits (2000 chars for comments, 15000 for issue bodies)
   - Code block rejection in comments
3. Deduplication check (won't re-post if notes URL already commented)
4. Posts comments on existing issues / creates new issues via `gh`

**SAFETY:** The post-script refuses to run if `SCRIBE_DRY_RUN` is not
explicitly set. Scheduled runs default to `dry_run=true`.

## Confidence threshold

The scribe agent assigns a **confidence score** (0.0–1.0) to every topic
it extracts. The post-script enforces a **minimum confidence threshold** —
topics scoring below it are rejected before any GitHub write happens. This
is a deterministic, non-LLM gate: the threshold check runs in plain bash,
not inside the sandbox.

### How the LLM assigns confidence

The agent prompt instructs the LLM to calibrate confidence based on
discussion quality, not topic importance:

| Score range | Meaning | Example |
|-------------|---------|---------|
| **0.8–1.0** | Clear decision, concrete action item with owner, specific technical conclusion | "Decided to use Redis for caching; Greg will open the PR this week" |
| **0.5–0.7** | Substantive discussion without clear resolution; open question explored but not answered | "Discussed three options for auth middleware; no decision yet" |
| **< 0.5** | Passing mention, deferred topic, brainstorming with no takeaway | "Someone briefly mentioned maybe trying a new CI provider" |

The confidence score represents the LLM's judgment about how well-supported
a topic is by the meeting notes. Higher scores mean the notes contain
explicit decisions, action items, or explored trade-offs. Lower scores mean
the topic was mentioned but not substantively discussed.

### How the gate uses it

The post-script reads `SCRIBE_MIN_CONFIDENCE` (default: `0.6`) and rejects
any topic or new issue proposal whose confidence falls below it. When a
topic is rejected, the post-script logs:

```
GATE REJECTED: [Topic title] — confidence 0.45 below threshold 0.6
```

This means the topic was extracted by the agent but did not meet the bar
for automated posting. Rejected topics still appear in the `agent-result.json`
artifact for manual review.

### Tuning the threshold

The threshold is configurable at three levels (highest precedence first):

1. **Manual dispatch input** — when triggering via `workflow_dispatch`, set
   the `min_confidence` field. Useful for one-off experiments.
2. **Repository variable** — set `SCRIBE_MIN_CONFIDENCE` as a repo-level
   variable on the `.fullsend` repo. This is the recommended way to tune
   for your team. Applies to both scheduled and manual runs.
3. **Default** — `0.6` if neither of the above is set.

**Guidance for choosing a value:**

| Value | Effect |
|-------|--------|
| **0.8** | Conservative — only posts clear decisions and action items. May miss valuable open discussions. Good starting point for public repos where every post is visible. |
| **0.6** | Balanced (default) — posts decisions and substantive discussions. Filters passing mentions. |
| **0.4** | Permissive — posts most extracted topics. Higher volume, may include some noise. Useful for private repos or teams that want comprehensive coverage. |

Start with the default `0.6`, run a few cycles in dry-run mode, and examine
the `GATE REJECTED` lines in the workflow logs to decide if you're filtering
too aggressively or too loosely.

## Configuration

The scribe is an opt-in role — its scaffold files are **not** installed
by default when you run `fullsend admin install`. To enable it, include
`scribe` in the `--agents` flag:

```bash
fullsend admin install <org> --agents triage,coder,review,scribe
```

Or add it to your org's `config.yaml` roles and re-run install:

```yaml
agents:
  - role: scribe
    name: Scribe
    slug: scribe-agent
```

### Required secrets (on `.fullsend` repo)

| Secret | Description |
|--------|-------------|
| `FULLSEND_SCRIBE_APP_PRIVATE_KEY` | GitHub App PEM for the scribe role |
| `FULLSEND_GCP_SA_KEY_JSON` | GCP service account key (Drive + Vertex AI) |
| `FULLSEND_GCP_PROJECT_ID` | GCP project ID for Vertex AI |

### Required variables (on `.fullsend` repo)

| Variable | Example | Required |
|----------|---------|----------|
| `FULLSEND_SCRIBE_CLIENT_ID` | `Iv1.abc123...` | Yes |
| `SCRIBE_GDRIVE_SEARCH_QUERY` | `fullsend team sync` | Yes |
| `SCRIBE_GDRIVE_NAME_FILTER` | `Notes by Gemini` | Optional |
| `SCRIBE_TARGET_REPO` | `fullsend` | Optional (defaults to `.fullsend` repo name) |
| `FULLSEND_GCP_REGION` | `us-east5` | Optional |
| `SCRIBE_MIN_CONFIDENCE` | `0.6` | Optional (see [Confidence threshold](#confidence-threshold)) |

### Granting Drive access to meeting notes

The service account needs read access to Google Meet's Gemini-generated
meeting notes. The simplest approach is to invite the SA to the recurring
meeting and configure note sharing:

**Step 1 — Add the SA as a calendar guest.**
In Google Calendar, edit your recurring meeting (e.g. "fullsend team sync")
and add the service account email as a guest:

```
<sa-name>@<gcp-project>.iam.gserviceaccount.com
```

The SA will appear in the attendee list alongside human participants.

**Step 2 — Enable note sharing for external users.**
Because the service account email belongs to a GCP project (not your Google
Workspace org), Google treats it as an external user. By default, Gemini
meeting notes are only shared with internal attendees. To fix this:

1. Open the meeting event in Google Calendar
2. Click the **pencil icon** (Edit event)
3. Under the Google Meet section, find the meeting notes / auto-sharing
   setting
4. Change it to **automatically share notes with all invited users,
   including those outside the organization**
5. Save the event (apply to all future occurrences)

Once configured, every new meeting's Gemini notes will be automatically
shared with the SA via Google Drive, and the pre-script can discover
them with the configured search query.

> **Note:** The SA only needs **read** access. It never modifies meeting
> notes. If your org has Google Workspace policies restricting external
> sharing, an admin may need to allowlist the SA's domain or adjust the
> sharing policy for the relevant organizational unit.

### Trigger model

Unlike triage/code/review (event-driven via shims), the scribe runs on a
**cron schedule** — Mon–Thu at 16:10 UTC by default. Adjust the cron in
`.github/workflows/scribe.yml` to match your meeting schedule.

Manual dispatch is also supported via `workflow_dispatch` with `dry_run`
and `lookback_hours` inputs.

## Security model

The scribe processes private meeting content for a public issue tracker.
The agent treats both meeting notes and LLM output as untrusted. Security
is enforced at five layers:

1. **Input scrubbing** (pre-script) — removes PII (emails, phone numbers,
   IPs, SSNs), API keys, tokens, and meeting-specific metadata (attendee
   lists, organizer/host lines) before the text reaches the LLM sandbox.
   This is the primary defense: scrubbed data never enters the agent.

2. **Deterministic gate** (post-script) — every extracted topic must pass
   through the gate before any GitHub write. The gate checks
   [confidence threshold](#confidence-threshold) (configurable, default 0.6),
   sensitive content, length limits, code blocks, and rejects rather than
   scrubs-and-posts.

3. **Sandbox isolation** — the LLM runs inside an OpenShell sandbox with a
   restrictive network policy (only `*.googleapis.com`). It cannot reach
   GitHub directly. All GitHub writes happen in the post-script on the host.

4. **Credential isolation** — GCP credentials are delivered via `host_files`
   in the harness (per ADR 0017/0025). The GitHub App token is generated
   per-run and scoped to the scribe role.

5. **Artifact hygiene** — the fullsend composite action excludes agent
   transcripts (`**/transcripts/**`) from uploaded artifacts. Transcripts
   contain the full agent conversation including tool inputs/outputs, which
   may include scrubbed-but-still-sensitive meeting content. Only the
   structured `agent-result.json` (which the agent prompt forbids from
   containing participant names) is uploaded.

> **Important:** If the `.fullsend` repository is **public**, workflow logs
> and artifacts are visible to everyone. The pre-script scrubbing and
> transcript exclusion are designed for this scenario, but operators should
> review artifact contents after initial deployment and before disabling
> dry-run mode.

## Differences from prototype PR #219

| PR #219 (prototype) | Scaffold (this) |
|---------------------|-----------------|
| Standalone Go binary (`cmd/secretarial-agent/`) | Agent prompt + pre/post scripts |
| Manual Vertex AI HTTP calls | `fullsend run` with sandbox |
| Credentials handled in Go code | `host_files` + `google-github-actions/auth` |
| Workflow in fullsend repo | Workflow in `.fullsend` config repo |
| Prompts embedded in Go constants | Prompt in `agents/scribe.md` |
| No sandbox isolation | OpenShell sandbox with network policy |
| Custom flag parsing | Standard harness `runner_env` |
