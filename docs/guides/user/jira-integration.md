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

For the architectural details of the polling protocol, see [ADR 0063 — Polling-based work discovery](../../ADRs/0063-polling-based-work-discovery.md).

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

Currently, agents process Jira-sourced dispatches but write all results — comments, labels, status updates — back to **GitHub only**. No output is posted to Jira. The agent runs against the target GitHub repo exactly as it would for a GitHub-sourced event: triage output becomes a GitHub issue comment, code agent output becomes a pull request, and so on. The Jira issue that triggered the dispatch receives no automatic update.

This means the person who commented `/fs-triage` on a Jira issue will not see the triage result in Jira — they need to check the linked GitHub repo. Two follow-ups track closing this gap:

- [#2264](https://github.com/fullsend-ai/fullsend/issues/2264) — agent pre/post scripts do not understand Jira work-item payloads (they expect a GitHub issue number, not a Jira key)
- [#5989](https://github.com/fullsend-ai/fullsend/issues/5989) — Jira comment write support and a `tracker.Client` for Jira, which would allow agents to post results back to the originating Jira issue

Until both land, treat Jira as a **trigger source only**: it can start agent work, but all output appears in GitHub.

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

## Repo configuration

No special harness or config changes are needed to *receive* Jira-sourced dispatches: the Jira poller produces the same [NormalizedEvents](../../normative/normalized-event/v1/) that GitHub and GitLab do, so routing and triggers work unchanged. However, agent output is currently written to GitHub only — no results are posted back to Jira. See [Event semantics — input only](#event-semantics--input-only) for details and tracking issues. Additionally, the built-in agents' pre/post scripts do not yet understand Jira work-item payloads (they expect a GitHub issue number, not a Jira key — [#2264](https://github.com/fullsend-ai/fullsend/issues/2264)), so dispatched agent runs will not complete successfully until that follow-up lands. See the Troubleshooting section below.

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
  contents: read

jobs:
  poll:
    runs-on: ubuntu-24.04
    concurrency:
      group: fullsend-jira-poll
      cancel-in-progress: false
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
        run: |
          fullsend poll \
            --input-driver jira-poll \
            --jira-url "${JIRA_BASE_URL}" \
            --jira-project PROJ \
            --target-repo "${{ github.repository }}" \
            --output dispatches.json \
            --fullsend-dir .fullsend

      - name: Dispatch agent workflows
        env:
          GH_TOKEN: ${{ github.token }}
          JIRA_BASE_URL: ${{ vars.JIRA_BASE_URL }}
        run: |
          set -euo pipefail

          if ! jq -e 'length > 0' dispatches.json > /dev/null 2>&1; then
            echo "No dispatches to process."
            exit 0
          fi

          dispatched=0
          count=$(jq 'length' dispatches.json)

          for i in $(seq 0 $((count - 1))); do
            record=$(jq -c ".[$i]" dispatches.json)
            STAGE=$(echo "$record" | jq -r '.stage')
            RESOURCE_KEY=$(echo "$record" | jq -r '.resource_key')
            EVENT_TYPE=$(echo "$record" | jq -r '.event_type')
            ISSUE_ID=$(echo "$record" | jq -r '.iid // 0')

            # Extract the Jira issue key from the resource key (e.g. "issue-PROJ-101" → "PROJ-101").
            ISSUE_KEY="${RESOURCE_KEY#issue-}"
            ISSUE_URL="${JIRA_BASE_URL}/browse/${ISSUE_KEY}"

            # Build a minimal event payload compatible with the scaffold agent workflows.
            # The concurrency group uses fromJSON(event_payload).issue.number, so it must
            # stay a number — per the adapter spec, that's entity.id (here, the record's
            # own numeric .iid), not the Jira key string. This is a stopgap projection,
            # not the spec's full FULLSEND_WORK_ITEM_* execution-ref projection (deferred
            # to the tracked "real output driver" follow-up — see the KNOWN LIMITATION
            # note in internal/jirapoll/poller.go).
            EVENT_PAYLOAD=$(jq -nc \
              --argjson number "$ISSUE_ID" \
              --arg url "$ISSUE_URL" \
              '{issue: {number: $number, html_url: $url}}')

            # Find the workflow file for this stage by scanning for the
            # "# fullsend-stage: <stage>" marker in workflow files.
            WORKFLOW_NAME=""
            for wf in .github/workflows/*.yml .github/workflows/*.yaml; do
              [[ -f "$wf" ]] || continue
              if grep -qxF "# fullsend-stage: ${STAGE}" "$wf"; then
                WORKFLOW_NAME=$(basename "$wf")
                break
              fi
            done
            if [[ -z "$WORKFLOW_NAME" ]]; then
              echo "::warning::No workflow found for stage ${STAGE}, skipping ${RESOURCE_KEY}"
              continue
            fi

            echo "Dispatching ${WORKFLOW_NAME} for ${ISSUE_KEY} (${STAGE})"
            gh workflow run "$WORKFLOW_NAME" \
              -f event_type="$EVENT_TYPE" \
              -f source_repo="${{ github.repository }}" \
              -f event_payload="$EVENT_PAYLOAD"

            dispatched=$((dispatched + 1))
          done

          echo "::notice::Dispatched ${dispatched} agent workflow(s)"
```

2. Replace `PROJ` with your Jira project key.
3. Commit and push the workflow file.

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

# Show stage and resource key for each dispatch
jq '.[] | {stage, resource_key, event_type}' dispatches.json
```

Key fields in each dispatch record:

| Field | Description |
|---|---|
| `stage` | Agent stage to dispatch (e.g., `triage`, `code`) |
| `event_type` | What triggered the dispatch (e.g., `comment_added`, `label_changed`) |
| `resource_key` | Identifies the Jira issue (e.g., `issue-PROJ-101`) |
| `iid` | Numeric issue ID used for concurrency grouping |
| `event_payload_b64` | Base64-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — decode with `jq -r '.event_payload_b64' | base64 -d | jq '.'` |

### Dry-run tip

To inspect what the poller *would* dispatch without triggering any agent workflows, run the `fullsend poll` command above and stop there — do not run the "Dispatch agent workflows" step from the [scheduled workflow](#scheduled-workflow). The poll step writes `dispatches.json` and advances Jira checkpoints, but no workflows are dispatched until the separate dispatch script reads that file and calls `gh workflow run`.

If you want to avoid advancing Jira checkpoints during testing, poll against a dedicated test project or use a narrowly scoped `--jql` that selects only test issues.

## Dispatch record format

> **Implementation detail.** Once Jira support is stabilized, users will not need to know this dispatch format — it will be an internal detail between the poll job and the `fullsend run` workflows. This section is documented now because the integration is pre-alpha and operators may need to debug or inspect dispatch records directly.

The poll step writes an array of dispatch records to `dispatches.json`. Each record has the following fields:

| Field | Type | Description |
|---|---|---|
| `stage` | string | Agent stage to run (e.g., `"triage"`, `"code"`). Determined by routing rules. |
| `event_type` | string | Jira event type that triggered the dispatch (e.g., `"comment_added"`, `"label_changed"`, `"opened"`, `"reopened"`, `"edited"`, `"closed"`). |
| `event_payload_b64` | string | Base64-encoded JSON [NormalizedEvent](../../normative/normalized-event/v1/). Decoded example below. |
| `resource_key` | string | Stable entity key for concurrency control, in the format `issue-{JIRA_KEY}` (e.g., `"issue-PROJ-101"` for Jira key `PROJ-101`). |
| `is_fork` | boolean | Whether the source is a fork. Always `false` for Jira dispatches (Jira issues are work items, not change proposals). |
| `iid` | integer | Jira's internal numeric issue ID (not the human-readable key). Used by the downstream dispatch step to set `issue.number` in the compatibility payload. |

### Decoded `event_payload_b64` example

The `event_payload_b64` field is a base64-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — the same forge-neutral struct that GitHub and GitLab input drivers produce. A decoded example for a `/fs-triage` comment on `PROJ-101`:

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

The `entity.key` field is required when `source.system` is `"jira"` — it carries the human-readable Jira key (e.g., `PROJ-101`) that the `resource_key` is derived from. See the [NormalizedEvent spec](../../normative/normalized-event/v1/) for the full schema.

## Actor role resolution

> **Known limitation.** Roles are resolved by Jira project **role name**, not by actual granted permissions. This is intentional for the MVP — see below.

The poller maps each event actor to an [ADR 0054](../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md) role (`read`, `write`, `admin`) by looking up their Jira project role membership and matching on the role's **name**:

| Jira project role name (case-insensitive) | ADR 0054 role |
|---|---|
| `Administrators` | `admin` |
| `Developers` | `write` |
| anything else (including custom role names) | `read` |

This does **not** check the project's permission scheme, so it can be wrong in both directions:

- An org with a custom role literally named `Developers` that has *not* been granted edit permissions will be over-privileged for write-gated dispatch (e.g. `/fs-code`).
- An org using differently-named roles (e.g. `Contributors`, `Engineering`) for people who *do* have edit access will be silently downgraded to `read`, and their slash commands will be ignored.

If your project uses Jira's default role names ("Administrators"/"Developers") with their default permissions, this works as expected. If you use custom role names, expect actors to resolve to `read` regardless of their real permissions until real permission-scheme resolution is implemented (tracked as future work, not planned for the MVP).

### Group member pagination cap

When a project role (e.g. Developers) is backed by a group, role resolution must determine whether a given actor belongs to that group. The Jira REST API's group/member endpoint paginates results and the poller enforces a **100-page volume guard** on enumeration. In earlier versions (before [#6041](https://github.com/fullsend-ai/fullsend/issues/6041)), the poller enumerated all members of each role-assigned group to build an `accountId → role` map. Groups larger than 100 pages — common with org-wide groups like `jira-users` — were silently truncated, causing unlisted members to fall back to the `external` role. Because `/fs-*` slash commands require `write` or higher, those actors' dispatches were silently dropped.

Since [#6041](https://github.com/fullsend-ai/fullsend/issues/6041), the poller uses **per-actor role resolution** instead: it loads the project's role structure (which groups are assigned to which roles) without enumerating group members, then checks each actor's own group memberships individually via the `/user/groups` endpoint. This endpoint returns the user's full group list in a single unpaginated response, so the 100-page cap no longer applies. The per-actor approach also reduces Jira API usage — cost scales with the number of distinct commenting actors per cycle rather than the size of the groups backing a role.

If you see `WARNING: member listing for group <groupId> truncated at 100 pages` in your poll logs, you are running a version that predates this fix. Upgrade to a version that includes [#6041](https://github.com/fullsend-ai/fullsend/issues/6041) to eliminate the cap.

### Jira membership is not GitHub membership

> **Known limitation.** No cross-system identity check is performed between Jira and GitHub. The Jira project is the entire authorization boundary for Jira-sourced events.

The role resolved above feeds directly into ADR 0054's dispatch authorization gate — there is no separate check against the target GitHub repo's actual collaborators. This means anyone holding a Jira project role that maps to `write` (Jira's default "Developers" role, by default) can trigger write-gated slash commands like `/fs-code` against your repo, **even if that person has no GitHub access to it at all**.

If your Jira project's membership is broader than your GitHub repo's collaborator list — which is common, since the two are usually administered separately — treat that gap as real: anyone in that gap can use their Jira membership alone to induce agent-proposed changes to your repository. Before enabling this driver, check who holds `write`-mapped roles in the target Jira project and make sure you're comfortable with each of them being able to do that.

## Poll coordination

The poller uses Jira entity properties for distributed lock coordination and checkpoint tracking, following the write-then-verify protocol defined in [ADR 0063](../../ADRs/0063-polling-based-work-discovery.md). Two properties are stored on each processed issue:

- **Lock** (`fullsend.poll.{owner}.{repo}.lock`) — prevents concurrent pollers from *detecting changes on* the same issue simultaneously. The lock covers the change-detection window only: it is released when an issue finishes processing, before the dispatch records are written and consumed by the downstream dispatch step, and it is not renewed while an issue is being processed — a cycle that stalls longer than the stale threshold (15 minutes) can have its lock reclaimed by a concurrent poller, which may produce duplicate dispatches. Lock ownership through dispatch scheduling is part of the same tracked follow-up as dispatch confirmation.
- **Last check** (`fullsend.poll.{owner}.{repo}.lastCheck`) — tracks the timestamp of the most recent processed change. Only changes newer than this timestamp trigger agent dispatch.

These properties are namespaced per target repo, so multiple repos can poll the same Jira project without interference. The properties are visible in Jira's issue properties API but do not appear in the issue UI.

> **Security note — coordination state is tamperable by any project editor.** Writing an issue entity property requires only Jira's **Edit Issues** permission, which is typically far broader than the project roles this driver maps to `write`. That means anyone who can edit a polled issue can write its `lastCheck` and lock properties. The driver treats both as untrusted: a `lastCheck` dated in the future is ignored (it would otherwise suppress all detection on the issue), and one rewound before the backfill window is clamped to the window's start, so a rewind can replay at most one backfill window of history rather than the whole issue — but within that window it can still re-surface old comments. Per-issue dispatch is capped to bound the blast radius. Even so, **treat Edit-Issues on a polled project as part of the trust boundary**: prefer scoping the poll to a project whose editors you trust, and remember that the authorization gate keys on the *comment's* Jira role, so who can edit issues (and comments) matters as much as who holds `write`.

Two edge cases of checkpoint tracking are worth knowing:

- **First enable on an existing backlog.** Every issue starts with an unset `lastCheck`, so on the first poll of an issue the poller has no checkpoint to filter by. Activity is instead bounded by a fixed 24-hour backfill window (not currently configurable): an `opened` event is emitted only for issues created within the window, and only comments and changelog entries from within the window produce events. Backlog issues with no activity (including comment edits) inside the window are silently checkpointed without dispatching. If you want the initial rollout scoped even tighter, narrow the candidate set with a custom `--jql`.
- **Comment edits count as new activity, and are attributed to the editor.** The poller filters comments on the later of their created and updated timestamps, so editing a comment (for example, adding a slash command to an old comment) is detected on the next cycle. Jira bumps a comment's updated timestamp on modifications other than body edits too (such as visibility changes), and any such bump counts as activity. The flip side: modifying a comment whose slash command was already dispatched makes it look new again and can re-dispatch it. An edit-detected comment is attributed to the account that last modified it (Jira's `updateAuthor`), not the original author — so a user who edits someone else's comment to inject a command is authorized on *their own* role, not the original author's.

Because each cron run is a fresh process, per-cycle Jira API cost is worth sizing for: every cycle that selects at least one issue makes one call to load the project's role structure (a role-list call plus one detail call per role), then one additional call for each distinct actor below the highest role priority the first time they're seen that cycle, to check their group memberships (cached per actor for the rest of the cycle, but not across cron runs). Cost scales with the number of distinct commenting actors per cycle rather than the size of the groups backing a role.

Two more scaling limits, following [ADR 0063](../../ADRs/0063-polling-based-work-discovery.md):

- **Issues beyond the top M candidates can be starved.** Each cycle's JQL search returns at most M (default 50) candidates, ordered by `updated DESC`. If a project has more than M issues with in-scope activity at once, whichever issues aren't in that top-M window this cycle are simply never selected — there's no rotation across cycles to eventually reach them. This is expected to self-resolve for most projects (an issue with new activity moves toward the front of `updated DESC` on its own), but a project that consistently has more than M simultaneously-active issues will have some starved indefinitely. Narrow the default `--jql` (e.g. scope to a label or a subset of issue types) if this applies to you.
- **Comments are re-listed in full, oldest-first, every cycle.** The Jira REST API has no way to filter comments server-side by date, so `ListComments` always paginates from the start of an issue's comment history; the poller then filters client-side by timestamp. For issues with very long comment histories, this means re-fetching (and re-decoding) old comments every cycle just to discard them. Listings are capped at 10,000 entries per issue; an issue beyond that cap has its newest activity invisible to the poller (a WARNING is logged when the cap is hit). Each API response is bounded to 10MB, but the accumulated per-issue comment history is bounded only by the page cap — a per-issue byte budget is a tracked follow-up; against real Jira Cloud (which caps individual comments) the realistic worst case is tens of MB per issue.
- **Sustained Jira errors can stall a cycle.** Each Jira call retries up to 5 times with exponential backoff, honoring `Retry-After` (capped at 5 minutes). Under a sustained 429 or outage a single cycle can therefore run long; the workflow's concurrency group queues subsequent cron runs rather than stacking them, and a failed cycle simply retries from its checkpoints on the next run.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| 401 on all Jira API calls | Invalid token | Regenerate the API token and update the `JIRA_TOKEN` secret |
| 200 on `/myself` but 403 on issue search | Org restricts personal API tokens for project data | Ask your Atlassian org admin to allow API token access for project data |
| No dispatches produced | No changes since last poll | Check the `lastCheck` entity property on the issue — the poller only dispatches for changes newer than this timestamp |
| Slash command ignored | Actor lacks `write` role in Jira project | The actor must be a member of a Jira project role named exactly "Developers" or "Administrators" — see [Actor role resolution](#actor-role-resolution) if you use custom role names |
| Slash commands silently ignored when using `--jql` | `--jira-project` not provided — all actors resolve to `external` and fail the role gate | Add `--jira-project PROJ` alongside `--jql` in your workflow file |
| `WARNING: member listing for group <groupId> truncated at 100 pages` | Role-assigned group exceeds the 100-page volume guard on the group/member endpoint | Upgrade to a version that includes [#6041](https://github.com/fullsend-ai/fullsend/issues/6041), which uses per-actor role resolution and is not subject to the cap. As a workaround on older versions, narrow the role's backing group to fewer members or use the Administrators role if it has a smaller group. See [Group member pagination cap](#group-member-pagination-cap) |
| Duplicate dispatches | `lastCheck` was cleared or missing | The poller treats a missing `lastCheck` as "never polled" and processes all recent changes. This is self-correcting — the next cycle advances `lastCheck` past the duplicates |
| Old comment or label change on a newly polled issue never dispatches | Activity predates the first-poll backfill window | On an issue's first poll, activity older than the 24-hour backfill window is permanently skipped (the checkpoint advances past it). To pick up older activity, scope the initial `--jql` to recently updated issues and widen it gradually, or have someone re-trigger by commenting again |
| Dispatched agent workflow fails immediately | Agent pre/post scripts don't understand Jira-keyed payloads yet | Known limitation, tracked in [#2264](https://github.com/fullsend-ai/fullsend/issues/2264). The dispatch step above still runs the workflow and produces a `NormalizedEvent`, but built-in agent scripts expect a GitHub issue number, not a Jira key |

## See also

- [CEL Triggers Reference](cel-triggers-reference.md) — NormalizedEvent fields and routing rules
- [Bring Your Own Agent](bring-your-own-agent.md) — adding custom agents
- [Configuring agent behavior](customizing-agents.md) — harness configuration
