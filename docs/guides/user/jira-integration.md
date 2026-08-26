# Jira Integration

> **Pre-alpha.** This feature is under active development and not ready for general use. If you're reading this, you're probably helping build it — thank you. Expect rough edges, breaking changes, and missing pieces.

Connect fullsend to a Jira project so that Jira issue activity — comments, label changes — triggers the same agents that run on GitHub and GitLab.

## How it works

A scheduled GitHub Actions workflow runs `fullsend poll --input-driver jira-poll` on a cron. Each cycle:

1. Queries Jira for recently updated issues in your project.
2. Detects new comments and label changes since the last poll.
3. Converts each change to a [NormalizedEvent](../../normative/normalized-event/v1/) — the forge-neutral event struct that all input drivers (GitHub, GitLab, Jira) produce.
4. Routes through the standard agent routing rules.
5. Writes dispatch records that trigger agent workflows.

For architectural details on the polling protocol, see [Architecture](../../architecture.md#agent-dispatch-and-coordination-layer).

The same conventions work across forges:

| Jira action | Agent triggered |
|---|---|
| Comment starting with `/fs-triage` | triage |
| Comment starting with `/fs-code` | code |
| Label `ready-to-code` added | code |
| Comment on an issue with `needs-info` label | triage |

A slash command is only recognized as the **first token of the comment's first line**; the rest of that line becomes the instruction passed to the agent. A command buried mid-sentence ("please /fs-triage this") or on a later line does not trigger. Slash commands follow the `/fs-{agent}` pattern for stages that can legitimately run against a bare Jira issue. `review`, `fix`, and `retro` are **not** among them: per the [jira-poll adapter spec](../../normative/normalized-event/v1/jira-poll-adapter.md#state), those stages are change-proposal-scoped (they act on an existing PR) and harness CEL triggers for them MUST require `entity.kind == 'change_proposal'` — which a Jira issue comment alone never has. A `/fs-review` comment on a Jira issue is not expected to dispatch anything.

## Event semantics — input only

The `event_type` field in dispatch records (e.g. `"comment_added"`, `"label_changed"`) describes the Jira-side activity that **triggered** the dispatch — it is an **input** event, not a description of what the agent will do. When you see `event_type: "comment_added"` in `dispatches.json`, it means "a comment was added to a Jira issue, and that comment matched a routing rule." It says nothing about the agent's output.

Currently, agents process Jira-sourced dispatches but write all results — comments, labels, status updates — back to **GitHub only** by default. The agent runs against the target GitHub repo exactly as it would for a GitHub-sourced event: triage output becomes a GitHub issue comment, code agent output becomes a pull request, and so on. The Jira issue that triggered the dispatch receives no automatic update.

The CLI primitive for posting comments to Jira exists — `fullsend issues post-comment --tracker jira` — along with the underlying `tracker.Client` Jira implementation ([#5989](https://github.com/fullsend-ai/fullsend/issues/5989)). However, the built-in agent pipeline does not use it yet: agent pre/post scripts still expect a GitHub issue number, not a Jira key ([#2264](https://github.com/fullsend-ai/fullsend/issues/2264)).

This means the person who commented `/fs-triage` on a Jira issue will not see the triage result in Jira — they need to check the linked GitHub repo. Until agent-pipeline integration lands ([#2264](https://github.com/fullsend-ai/fullsend/issues/2264)), treat Jira as a **trigger source only** for built-in agents: it can start agent work, but all output appears in GitHub. Custom agents can use `fullsend issues post-comment --tracker jira` directly to post results back to Jira.

## Prerequisites

- A GitHub repo with fullsend installed (`fullsend github setup` completed).
- A Jira Cloud instance. **Jira Data Center is not currently supported** — the client is hard-wired to Cloud-only APIs (REST v3, cursor-based search pagination, `groupId`-based group lookup), so requests against a Data Center instance will fail. Tracked as future work.
- A Jira API token ([Create API token](https://id.atlassian.com/manage-profile/security/api-tokens)).
- The Jira user must have read access to the target project and write access to issue entity properties (used for poll coordination state).

## Credential setup

1. Open your GitHub repo's **Settings > Secrets and variables > Actions**.
2. Add the following secrets and variables:

| Secret / variable | Value |
|---|---|
| `JIRA_TOKEN` | Your Jira API token |
| `JIRA_USER_EMAIL` | Email associated with the token |
| `JIRA_BASE_URL` | Jira instance URL, e.g. `https://myteam.atlassian.net` |

### Sandbox credentials and network access

The fullsend scaffold includes an OpenShell credential provider (`providers/atlassian-cloud.yaml`) and network profile (`profiles/fullsend-atlassian-cloud.yaml`) that grant sandboxed agents access to Jira Cloud. These are layered content — they are provided at runtime by reusable workflows, not written to the `.fullsend` directory:

- **Provider** (`atlassian-cloud`) — declares the `atlassian-cloud` credential provider using the `fullsend-atlassian-cloud` profile type. Jira Cloud Basic auth requires a base64-encoded `email:api-token` string; because OpenShell does not yet support composite credential injection, provide a single pre-encoded token (generate with `printf 'you@example.com:your-api-token' | base64`).
- **Profile** (`fullsend-atlassian-cloud`) — allows outbound HTTPS to `*.atlassian.net:443` with read-write access and endpoint enforcement. Permitted binaries are `curl` and `node`.

The wildcard host (`*.atlassian.net`) permits egress to any Atlassian Cloud tenant. Per-tenant scoping is not yet supported by OpenShell; this is an accepted risk for now.

## Repo configuration

No special harness or config changes are needed to *receive* Jira-sourced dispatches: the Jira poller produces the same [NormalizedEvents](../../normative/normalized-event/v1/) that GitHub and GitLab do, so routing and triggers work unchanged. However, built-in agent output is currently written to GitHub only — the CLI primitive for posting to Jira exists (`fullsend issues post-comment --tracker jira`), but the agent pipeline does not use it yet. See [Event semantics — input only](#event-semantics--input-only) for details. Additionally, the built-in agents' pre/post scripts do not yet understand Jira work-item payloads (they expect a GitHub issue number, not a Jira key — [#2264](https://github.com/fullsend-ai/fullsend/issues/2264)), so dispatched agent runs will not complete successfully until that follow-up lands. See the Troubleshooting section below.

If your repo already has a `.fullsend/config.yaml` from `fullsend github setup`, you are ready to go.

## Scheduled workflow

1. Create `.github/workflows/fullsend-poll-jira.yml` with the following content:

```yaml
name: fullsend Jira poll

on:
  schedule:
    - cron: "*/5 * * * *"  # every 5 minutes
  workflow_dispatch: {}     # allow manual runs

permissions:
  actions: write
  contents: write
  id-token: write
  issues: write
  packages: read
  pull-requests: write

jobs:
  poll:
    runs-on: ubuntu-24.04
    concurrency:
      group: fullsend-jira-poll
      cancel-in-progress: false
    outputs:
      matrix: ${{ steps.build-matrix.outputs.matrix }}
    steps:
      - uses: actions/checkout@v4

      - name: Install fullsend
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          gh release download --repo fullsend-ai/fullsend -p 'fullsend_*_linux_amd64.tar.gz' -O - | tar xz
          sudo mv fullsend /usr/local/bin/

      - name: Poll Jira
        env:
          JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
          JIRA_USER_EMAIL: ${{ secrets.JIRA_USER_EMAIL }}
          JIRA_BASE_URL: ${{ vars.JIRA_BASE_URL }}
          TARGET_REPO: ${{ github.repository }}
        run: |
          fullsend poll \
            --input-driver jira-poll \
            --jira-url "${JIRA_BASE_URL}" \
            --jira-project PROJ \
            --target-repo "${TARGET_REPO}" \
            --output dispatches.json \
            --fullsend-dir .fullsend

      - name: Build dispatch matrix
        id: build-matrix
        run: |
          set -euo pipefail
          if ! jq -e 'length > 0' dispatches.json > /dev/null 2>&1; then
            echo "No dispatches."
            echo 'matrix={"include":[]}' >> "${GITHUB_OUTPUT}"
            exit 0
          fi
          MATRIX=$(jq -c '{include: .}' dispatches.json)
          DELIM="MATRIX_$(openssl rand -hex 8)"
          {
            echo "matrix<<${DELIM}"
            printf '%s' "${MATRIX}"
            echo
            echo "${DELIM}"
          } >> "${GITHUB_OUTPUT}"

  harness:
    name: Harness
    needs: poll
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v0
    with:
      matrix: ${{ needs.poll.outputs.matrix }}
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      gcp_region: ${{ vars.FULLSEND_GCP_REGION }}
      jira_base_url: ${{ vars.JIRA_BASE_URL }}
    secrets:
      FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}
      FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
      OTEL_EXPORTER_OTLP_TRACES_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}
      OTEL_EXPORTER_OTLP_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_HEADERS }}
      JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
      JIRA_USER_EMAIL: ${{ secrets.JIRA_USER_EMAIL }}
```

2. Replace `PROJ` with your Jira project key.
3. Commit and push the workflow file.

**How this works:** The `poll` job queries Jira and builds a dispatch matrix in the format expected by `reusable-dispatch.yml`. When the `matrix` input is provided, `reusable-dispatch.yml` skips its routing and dispatch steps and goes directly to running harness agents with the pre-computed matrix. This approach maintains ADR 62's inlining decision (no version skew) while enabling custom pollers to reuse fullsend's harness infrastructure. See [Custom Poller Example](custom-poller-example.md) for more details on this pattern.

**`concurrency.cancel-in-progress: false`** ensures overlapping poll cycles queue rather than cancel each other, which is the primary defense against concurrent runs — the GitHub Actions concurrency group means only one poll cycle for this workflow ever runs at a time in the common case. The poller's own Jira entity-property locking is a secondary guard for cases outside that group (e.g. a manually triggered run overlapping a scheduled one): it re-checks for a live lock immediately before writing, narrowing the race to the jitter window between that check and the write, but Jira entity properties have no compare-and-swap, so a lock created in that narrow window can still be clobbered by a genuinely concurrent poller. Treat the GHA concurrency group as the real safety mechanism, not the lock.

### Custom JQL

By default the poller searches for all non-done issues in the project, ordered by most recently updated. To narrow the scope, use `--jql`:

```bash
fullsend poll \
  --input-driver jira-poll \
  --jira-url "${JIRA_BASE_URL}" \
  --jira-project PROJ \
  --jql 'project = PROJ AND labels = "fullsend" AND statusCategory != Done ORDER BY updated DESC' \
  --target-repo "${{ github.repository }}" \
  --output dispatches.json \
  --fullsend-dir .fullsend
```

**`--jira-project` is required for any project using slash commands.** Without it the poller cannot resolve Jira project roles, so all actors default to the `external` role and every `/fs-*` command silently fails the role gate. Always provide `--jira-project` alongside `--jql` unless you are certain your routing rules do not depend on actor roles.

Technically `--jql` can be used without `--jira-project`, but the only scenario where that is safe is a read-only polling setup with no slash-command-based dispatch.

Custom JQL must be a **bounded query**: Jira's enhanced search endpoint rejects queries without a search restriction (e.g. a bare `ORDER BY updated DESC`) with a 400 on every cycle. Always include at least a `project = ...` or similar restriction, as the examples above do.

## Local testing

You can run `fullsend poll` locally to verify your Jira connection and inspect dispatch output before deploying the scheduled workflow.

### Required environment variables

Set the same credentials you would configure as GitHub Actions secrets:

```bash
export JIRA_TOKEN="your-jira-api-token"
export JIRA_USER_EMAIL="you@example.com"
export JIRA_BASE_URL="https://myteam.atlassian.net"
```

### Running the poller

From the repo root, use `go run` (per [AGENTS.md](../../../AGENTS.md) convention — do not use a `fullsend` binary from `$PATH` or another checkout):

```bash
go run ./cmd/fullsend poll \
  --input-driver jira-poll \
  --jira-url "${JIRA_BASE_URL}" \
  --jira-project PROJ \
  --target-repo owner/repo \
  --output dispatches.json \
  --fullsend-dir .fullsend
```

Replace `PROJ` with your Jira project key and `owner/repo` with the GitHub repository slug where agent workflows run.

> **Note:** `--jira-project` is effectively required when your project uses slash commands (`/fs-triage`, `/fs-code`, etc.). Without it, the poller cannot resolve Jira project roles and all actors default to `external`, which silently fails the role gate for write-gated commands. See [#6089](https://github.com/fullsend-ai/fullsend/issues/6089) for details.

### Inspecting output

The poller writes dispatch records to the `--output` path. Inspect them with `jq`:

```bash
# Pretty-print all dispatch records
jq '.' dispatches.json

# Count dispatches
jq 'length' dispatches.json

# Show agent and event type for each dispatch
jq '.[] | {agent, event_type, status_number}' dispatches.json
```

Key fields in each dispatch record:

| Field | Description |
|---|---|
| `agent` | Agent to dispatch (e.g., `triage`, `code`) |
| `event_type` | What triggered the dispatch (e.g., `comment_added`, `label_changed`) |
| `event_payload` | JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — inspect with `jq -r '.event_payload \| fromjson' dispatches.json` |
| `source_repo` | GitHub repo slug where the agent runs |
| `status_repo` | GitHub repo slug for status tracking |
| `status_number` | Entity identifier as a string (Jira numeric issue ID) |

### Dry-run tip

To inspect what the poller *would* dispatch without triggering any agent workflows, run the `fullsend poll` command above and stop there — do not run the "Dispatch agent workflows" step from the [scheduled workflow](#scheduled-workflow). The poll step writes `dispatches.json` and advances Jira checkpoints, but no workflows are dispatched until the separate dispatch script reads that file and calls `gh workflow run`.

If you want to avoid advancing Jira checkpoints during testing, poll against a dedicated test project or use a narrowly scoped `--jql` that selects only test issues.

## Dispatch record format

> **Implementation detail.** Once Jira support is stabilized, users will not need to know this dispatch format — it will be an internal detail between the poll job and the `fullsend run` workflows. This section is documented now because the integration is pre-alpha and operators may need to debug or inspect dispatch records directly.

The poll step writes an array of dispatch records to `dispatches.json`. Each record is an execution ref compatible with the `workflow_call` shim (`reusable-dispatch.yml`). Fields:

| Field | Type | Description |
|---|---|---|
| `agent` | string | Agent to run (e.g., `"triage"`, `"code"`). Determined by CEL trigger evaluation. |
| `role` | string | Harness role for this agent. |
| `event_type` | string | Jira event type that triggered the dispatch (e.g., `"comment_added"`, `"label_changed"`, `"opened"`, `"reopened"`, `"edited"`, `"closed"`). |
| `event_payload` | string | JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/). Example below. |
| `source_repo` | string | GitHub repo slug where the agent workflow runs. |
| `trigger_source` | string | (Optional) Trigger source identifier. |
| `status_repo` | string | GitHub repo slug for status tracking. |
| `status_number` | string | Entity identifier as a string (Jira's numeric issue ID). |

### `event_payload` example

The `event_payload` field is a JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — the same forge-neutral struct that GitHub and GitLab input drivers produce. An example for a `/fs-triage` comment on `PROJ-101`:

```json
{
  "repo": "acme/platform",
  "entity": {
    "kind": "work_item",
    "id": 10042,
    "key": "PROJ-101",
    "url": "https://myteam.atlassian.net/browse/PROJ-101"
  },
  "transition": {
    "kind": "comment_added",
    "comment": {
      "body": "/fs-triage please triage this bug",
      "command": "/fs-triage",
      "instruction": "please triage this bug"
    }
  },
  "actor": {
    "id": "5f7c012e0b2a4c001f3d9876",
    "kind": "human",
    "role": "write",
    "is_entity_author": false
  },
  "state": {
    "labels": ["bug", "fullsend"]
  },
  "source": {
    "system": "jira",
    "raw_type": "comment",
    "raw_action": "created"
  }
}
```

The `entity.key` field is required when `source.system` is `"jira"` — it carries the human-readable Jira key (e.g., `PROJ-101`). See the [NormalizedEvent spec](../../normative/normalized-event/v1/) for the full schema.

## Actor role resolution

> **Known limitation.** Roles are resolved by Jira project **role name**, not by actual granted permissions. This is intentional for the MVP — see below.

The poller maps each event actor to a dispatch authorization role (`read`, `write`, `admin`) by looking up their Jira project role membership and matching on the role's **name**:

| Jira project role name (case-insensitive) | Dispatch role |
|---|---|
| `Administrators` | `admin` |
| `Developers` | `write` |
| anything else (including custom role names) | `read` |

This does **not** check the project's permission scheme, so it can be wrong in both directions:

- An org with a custom role literally named `Developers` that has *not* been granted edit permissions will be over-privileged for write-gated dispatch (e.g. `/fs-code`).
- An org using differently-named roles (e.g. `Contributors`, `Engineering`) for people who *do* have edit access will be silently downgraded to `read`, and their slash commands will be ignored.

If your project uses Jira's default role names ("Administrators"/"Developers") with their default permissions, this works as expected. If you use custom role names, expect actors to resolve to `read` regardless of their real permissions until real permission-scheme resolution is implemented (tracked as future work, not planned for the MVP).

### Jira membership is not GitHub membership

> **Known limitation.** No cross-system identity check is performed between Jira and GitHub. The Jira project is the entire authorization boundary for Jira-sourced events.

The role resolved above feeds directly into the dispatch authorization gate (see [Architecture](../../architecture.md#agent-dispatch-and-coordination-layer)) — there is no separate check against the target GitHub repo's actual collaborators. This means anyone holding a Jira project role that maps to `write` (Jira's default "Developers" role, by default) can trigger write-gated slash commands like `/fs-code` against your repo, **even if that person has no GitHub access to it at all**.

If your Jira project's membership is broader than your GitHub repo's collaborator list — which is common, since the two are usually administered separately — treat that gap as real: anyone in that gap can use their Jira membership alone to induce agent-proposed changes to your repository. Before enabling this driver, check who holds `write`-mapped roles in the target Jira project and make sure you're comfortable with each of them being able to do that.

## Poll coordination

The poller uses Jira entity properties for distributed lock coordination and checkpoint tracking, following a write-then-verify protocol. Each cycle randomly selects up to N (default 5) issues from the candidate set for processing, spreading load across cycles. Two properties are stored on each processed issue:

- **Lock** (`fullsend.poll.{owner}.{repo}.lock`) — prevents concurrent pollers from *detecting changes on* the same issue simultaneously. The lock covers the change-detection window only: it is released when an issue finishes processing, before the dispatch records are written and consumed by the downstream dispatch step, and it is not renewed while an issue is being processed — a cycle that stalls longer than the stale threshold (15 minutes) can have its lock reclaimed by a concurrent poller, which may produce duplicate dispatches. Lock ownership through dispatch scheduling is part of the same tracked follow-up as dispatch confirmation.
- **Last check** (`fullsend.poll.{owner}.{repo}.lastCheck`) — tracks the timestamp of the most recent processed change. Only changes newer than this timestamp trigger agent dispatch.

These properties are namespaced per target repo, so multiple repos can poll the same Jira project without interference. The properties are visible in Jira's issue properties API but do not appear in the issue UI.

> **Security note — coordination state is tamperable by any project editor.** Writing an issue entity property requires only Jira's **Edit Issues** permission, which is typically far broader than the project roles this driver maps to `write`. That means anyone who can edit a polled issue can write its `lastCheck` and lock properties. The driver treats both as untrusted: a `lastCheck` dated in the future is ignored (it would otherwise suppress all detection on the issue), and one rewound before the backfill window is clamped to the window's start, so a rewind can replay at most one backfill window of history rather than the whole issue — but within that window it can still re-surface old comments. Per-issue dispatch is capped to bound the blast radius. Even so, **treat Edit-Issues on a polled project as part of the trust boundary**: prefer scoping the poll to a project whose editors you trust, and remember that the authorization gate keys on the *comment's* Jira role, so who can edit issues (and comments) matters as much as who holds `write`.

Three edge cases of checkpoint tracking are worth knowing:

- **First enable on an existing backlog.** Every issue starts with an unset `lastCheck`, so on the first poll of an issue the poller has no checkpoint to filter by. Activity is instead bounded by a fixed 24-hour backfill window (not currently configurable): an `opened` event is emitted only for issues created within the window, and only comments and changelog entries from within the window produce events. Backlog issues with no activity (including comment edits) inside the window are silently checkpointed without dispatching. If you want the initial rollout scoped even tighter, narrow the candidate set with a custom `--jql`.
- **Resuming after a gap longer than 24 hours permanently drops events in the gap.** The same 24-hour backfill window that bounds a first poll also bounds resumption. When the poller reads a stored `lastCheck` older than `now − 24h`, it clamps the checkpoint forward to `now − 24h` — the clamping is a security control (it prevents a rewound `lastCheck` from replaying an issue's entire history), but it cannot distinguish a malicious rewind from a legitimate long stop. Any Jira activity between the true checkpoint and the clamped value is silently skipped. The cycle succeeds, the only evidence is a `WARNING: lastCheck for <KEY> ("<timestamp>") predates the backfill window; clamping to <floor>` line in the run log — treat this as an alert, not noise. Because detection is checkpoint-based, ordinary cron jitter and occasional skipped ticks are harmless (schedule accuracy affects latency, not correctness); the guarantee simply ends at 24 hours. A realistic trigger: **GitHub disables scheduled workflows after 60 days of repository inactivity**, which stops the poller with no error anywhere. After a long gap, the starvation limit (M = 50, see below) compounds the problem — many issues may have accumulated new activity simultaneously, and there is no rotation across cycles to reach issues beyond the top M. If your deployment cannot tolerate the 24-hour bound, [ADR 0063](../../ADRs/0063-polling-based-work-discovery.md) treats the scheduler as pluggable: an external cron or Kubernetes CronJob that runs independently of GitHub Actions avoids the auto-disable risk.
- **Comment edits count as new activity, and are attributed to the editor.** The poller filters comments on the later of their created and updated timestamps, so editing a comment (for example, adding a slash command to an old comment) is detected on the next cycle. Jira bumps a comment's updated timestamp on modifications other than body edits too (such as visibility changes), and any such bump counts as activity. The flip side: modifying a comment whose slash command was already dispatched makes it look new again and can re-dispatch it. An edit-detected comment is attributed to the account that last modified it (Jira's `updateAuthor`), not the original author — so a user who edits someone else's comment to inject a command is authorized on *their own* role, not the original author's.

Because each cron run is a fresh process, per-cycle Jira API cost is worth sizing for: every cycle that selects at least one issue makes one call to load the project's role structure (a role-list call plus one detail call per role), then one additional call for each distinct actor below the highest role priority the first time they're seen that cycle, to check their group memberships (cached per actor for the rest of the cycle, but not across cron runs). Cost scales with the number of distinct commenting actors per cycle rather than the size of the groups backing a role.

Two more scaling limits:

- **Issues beyond the top M candidates can be starved.** Each cycle's JQL search returns at most M (default 50) candidates, ordered by `updated DESC`. If a project has more than M issues with in-scope activity at once, whichever issues aren't in that top-M window this cycle are simply never selected — there's no rotation across cycles to eventually reach them. This is expected to self-resolve for most projects (an issue with new activity moves toward the front of `updated DESC` on its own), but a project that consistently has more than M simultaneously-active issues will have some starved indefinitely. Narrow the default `--jql` (e.g. scope to a label or a subset of issue types) if this applies to you.
- **Comments are re-listed in full, oldest-first, every cycle.** The Jira REST API has no way to filter comments server-side by date, so `ListComments` always paginates from the start of an issue's comment history; the poller then filters client-side by timestamp. For issues with very long comment histories, this means re-fetching (and re-decoding) old comments every cycle just to discard them. Listings are capped at 10,000 entries per issue; an issue beyond that cap has its newest activity invisible to the poller (a WARNING is logged when the cap is hit). Each API response is bounded to 10MB, but the accumulated per-issue comment history is bounded only by the page cap — a per-issue byte budget is a tracked follow-up; against real Jira Cloud (which caps individual comments) the realistic worst case is tens of MB per issue.
- **Sustained Jira errors can stall a cycle.** Each Jira call retries up to 5 times with exponential backoff, honoring `Retry-After` (capped at 5 minutes). Under a sustained 429 or outage a single cycle can therefore run long; the workflow's concurrency group queues subsequent cron runs rather than stacking them, and a failed cycle simply retries from its checkpoints on the next run.

## Multi-repo polling

Multiple GitHub repos can poll the same Jira project. Each repo runs its own poll workflow and gets an isolated view of coordination state — no cross-repo configuration is needed. Before adding pollers, use Jira Components or labels to segment your project so each repo's `--jql` filter only matches issues relevant to it — this avoids redundant API calls and keeps each poller's candidate window focused.

### Should each repo have its own poll workflow?

Yes. Each repo should contain its own `.github/workflows/fullsend-poll-jira.yml`. The `--target-repo` flag passed to `fullsend poll` (defaulting to `${{ github.repository }}`) controls two things:

1. **Entity property namespace.** Lock and checkpoint properties are keyed as `fullsend.poll.{owner}.{repo}.*`, so repos `acme/frontend` and `acme/backend` polling the same Jira project write to separate property keys on each issue and never interfere with each other.
2. **GHA concurrency group.** The workflow's `concurrency.group` is scoped to the workflow file, which lives in the repo — so each repo's poll cycles queue independently.

This means each repo discovers and dispatches changes on its own schedule, with its own credentials and its own `--jql` filter. There is no shared state between repos.

### How entity property locks namespace across repos

The poller stores two properties per issue, both namespaced by the target repo's `{owner}/{repo}` slug:

| Property | Key pattern |
|---|---|
| Lock | `fullsend.poll.{owner}.{repo}.lock` |
| Checkpoint | `fullsend.poll.{owner}.{repo}.lastCheck` |

When `acme/frontend` and `acme/backend` both poll issue `PROJ-42`:

- `acme/frontend` reads and writes `fullsend.poll.acme.frontend.lock` and `fullsend.poll.acme.frontend.lastCheck`.
- `acme/backend` reads and writes `fullsend.poll.acme.backend.lock` and `fullsend.poll.acme.backend.lastCheck`.

The two repos lock independently, checkpoint independently, and can process the same Jira comment — each dispatching to its own agent workflows. This is intentional: the same `/fs-triage` comment on a Jira issue may be relevant to multiple repos if their harness triggers match.

### 50-candidate cap with multiple repos

Each repo's poller runs its own JQL query and gets its own top-M (default 50) candidate window. The cap is per-poller, not global — three repos polling the same project each see up to 50 candidates per cycle, not 50 shared across all three.

However, more pollers against the same Jira project means more API calls per cycle. Each poller independently reads entity properties for lock filtering, fetches changelogs, and paginates comments. For large multi-repo deployments, consider:

- **Narrowing `--jql` per repo** to reduce candidate overlap. Use Jira Components or labels to segment the project (see the recommendation above), then scope each repo's query — e.g., `component = Frontend` or `labels = frontend` — so it skips issues that belong to other repos.
- **Staggering cron schedules** so pollers from different repos don't hit the Jira API simultaneously. For example, offset each repo's schedule by one or two minutes.
- **Monitoring Jira rate limits.** Jira Cloud uses points-based quotas, and multiple pollers multiply the cost. Watch for `429` responses in workflow logs — the poller retries with backoff, but sustained rate limiting slows all repos' cycles.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| 401 on all Jira API calls | Invalid token | Regenerate the API token and update the `JIRA_TOKEN` secret |
| 200 on `/myself` but 403 on issue search | Org restricts personal API tokens for project data | Ask your Atlassian org admin to allow API token access for project data |
| No dispatches produced | No changes since last poll | Check the `lastCheck` entity property on the issue — the poller only dispatches for changes newer than this timestamp |
| Slash command ignored | Actor lacks `write` role in Jira project | The actor must be a member of a Jira project role named exactly "Developers" or "Administrators" — see [Actor role resolution](#actor-role-resolution) if you use custom role names |
| Slash commands silently ignored when using `--jql` | `--jira-project` not provided — all actors resolve to `external` and fail the role gate | Add `--jira-project PROJ` alongside `--jql` in your workflow file |
| Duplicate dispatches | `lastCheck` was cleared or missing | The poller treats a missing `lastCheck` as "never polled" and processes all recent changes. This is self-correcting — the next cycle advances `lastCheck` past the duplicates |
| Old comment or label change on a newly polled issue never dispatches | Activity predates the first-poll backfill window | On an issue's first poll, activity older than the 24-hour backfill window is permanently skipped (the checkpoint advances past it). To pick up older activity, scope the initial `--jql` to recently updated issues and widen it gradually, or have someone re-trigger by commenting again |
| Activity during a long outage or disabled workflow is missing after resume | Poller gap exceeded the 24-hour backfill window | The same 24-hour window that bounds a first poll also bounds resumption — any `lastCheck` older than 24 hours is clamped forward. Check the run log for `predates the backfill window` — if present, events in the gap are permanently lost. Have affected users re-trigger by commenting again. See [Poll coordination](#poll-coordination) |
| Dispatched agent workflow fails immediately | Agent pre/post scripts don't understand Jira-keyed payloads yet | Known limitation, tracked in [#2264](https://github.com/fullsend-ai/fullsend/issues/2264). The dispatch step above still runs the workflow and produces a `NormalizedEvent`, but built-in agent scripts expect a GitHub issue number, not a Jira key |

## See also

- [CEL Triggers Reference](cel-triggers-reference.md) — NormalizedEvent fields and routing rules
- [Bring Your Own Agent](bring-your-own-agent.md) — adding custom agents
- [Configuring agent behavior](customizing-agents.md) — harness configuration
