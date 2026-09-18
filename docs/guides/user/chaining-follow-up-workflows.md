# Chaining Follow-up Workflows After an Agent Run

Run your own GitHub Actions workflow after a fullsend agent finishes, using
what the run already publishes. The follow-up lives in your repository, runs
with the job token, and holds only the permissions you give it. No new agent
role, no change to fullsend.

Typical follow-ups: re-run CI jobs an agent classified as flaky, open a
deployment, notify a channel, or feed the result into another workflow.

Decided in [ADR 0115](../../ADRs/0115-user-owned-follow-up-workflows-after-agent-runs.md).

## Prerequisites

- A repository with fullsend installed in per-repo mode (the `fullsend`
  shim workflow lives in your `.github/workflows/`).
- An agent whose harness declares a `validation_loop` with a `schema`, so the
  run ends with a validated result file. See
  [Bring Your Own Agent](bring-your-own-agent.md#minimum-viable-agent).
- Write access to the repository, to add a workflow on the default branch.

## Where each action belongs

| You want to… | Put it in | Token |
|---|---|---|
| Comment, label, review, open a PR | the agent's post-script | the role's minted token |
| Re-run or cancel CI jobs, dispatch a workflow, approve a run, deploy | a follow-up workflow | the job token, with permissions you declare |
| Keep the LLM sandbox weaker than your scripts | the harness `privilege_levels` field (`runtime: read`) | the same role's token at a lower level |

The rule: anything a fullsend role does not already grant is a follow-up
workflow, not a wider role. Roles are shared trust boundaries; your workflow's
permissions are yours alone. `privilege_levels` only narrows a token within
the role, so it complements a follow-up workflow rather than replacing it. See
[`privilege_levels` in the harness reference](../../reference/harness-reference.md).

## What a run publishes

Every agent run uploads the artifact **`fullsend-<agent>`**, where `<agent>`
is the agent's name in `.fullsend/config.yaml`. It is uploaded even when the
run fails, so check the run's conclusion before trusting its contents.

```text
fullsend-<agent>/
└── <run-dir>/                       # generated per run, e.g. fs-rev-09c9f0671390
    ├── iteration-1/
    │   ├── output/
    │   │   └── agent-result.json    # the validated result (name set by the agent)
    │   ├── output.jsonl             # raw agent stream, not for consumers
    │   └── transcripts/             # agent transcripts, not for consumers
    ├── logs/                        # sandbox and gateway logs
    ├── metrics.json                 # model, runtime, turns, tool calls, cost
    ├── run-telemetry.jsonl          # OpenTelemetry spans
    ├── eval-measurements.jsonl
    └── eval-measure-ledger.txt
```

On a successful run the highest-numbered `iteration-N/output/` holds the
result that passed validation. A run whose output never validates fails, so
filtering on `workflow_run.conclusion == 'success'` is enough to skip it.

The shim workflow completing raises a `workflow_run` event in your repository.
That event is your trigger.

> **Planned:** a runner-written `fullsend-summary-<agent>` artifact
> ([#7413](https://github.com/fullsend-ai/fullsend/issues/7413)) will carry the
> agent name, the subject (PR number, head SHA), the run status and the
> validated result in one small file. Until it lands, consumers gate on the
> artifacts API and read the result out of `fullsend-<agent>` as shown below.

## Steps

### 1. Make the result carry what the follow-up needs

The follow-up sees only the result file. Put identifiers in it, not prose:
job ids, run ids, a verdict enum. Keep confidence scores if the follow-up
thresholds on them. Example result for a CI diagnosis agent:

```json
{
  "status": "diagnosed",
  "recommended_action": "retry",
  "retry_targets": [
    { "check_name": "flaky-unit", "check_run_id": 105264044037 }
  ]
}
```

Add every field to the harness schema so the validation loop rejects a result
that lacks them.

### 2. Start read-only: summarize every run

Before granting any write permission, chain a read-only follow-up that shows
what each agent run published. It needs `actions: read` and works with every
agent, fleet or custom. Create `.github/workflows/fullsend-run-info.yml` on the
default branch (`workflow_run` only fires for workflows that exist there):

```yaml
name: fullsend-run-info
on:
  workflow_run:
    workflows: ["fullsend"]          # the shim workflow's name
    types: [completed]

permissions: {}

jobs:
  info:
    if: github.event.workflow_run.conclusion == 'success'
    runs-on: ubuntu-24.04
    permissions:
      actions: read
    steps:
      - name: Find agent results on this run
        id: find
        env:
          GH_TOKEN: ${{ github.token }}
          RUN_ID: ${{ github.event.workflow_run.id }}
        run: |
          names=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$RUN_ID/artifacts" \
            --jq '[.artifacts[].name | select(startswith("fullsend-"))] | join(" ")')
          echo "artifacts=$names" >> "$GITHUB_OUTPUT"
          echo "${names:-no agent ran on run $RUN_ID; nothing to do}"

      - uses: actions/download-artifact@v4
        if: steps.find.outputs.artifacts != ''
        with:
          pattern: fullsend-*
          run-id: ${{ github.event.workflow_run.id }}
          github-token: ${{ github.token }}
          path: result

      - name: Summarize agent run
        if: steps.find.outputs.artifacts != ''
        run: |
          set -euo pipefail
          for art in result/fullsend-*; do
            echo "### agent: ${art#result/fullsend-}"
            m=$(ls "$art"/*/metrics.json 2>/dev/null | head -1)
            [ -n "$m" ] && jq -r '"model=\(.model) runtime=\(.runtime) iterations=\(.iterations) turns=\(.num_turns) cost_usd=\(.total_cost_usd)"' "$m"
            r=$(ls "$art"/*/iteration-*/output/*.json 2>/dev/null | sort -V | tail -1)
            [ -n "$r" ] && jq '{action, pr_number, head_sha, findings: (.findings|length?)}' "$r"
          done | tee -a "$GITHUB_STEP_SUMMARY"
```

Output from a run of the default review agent:

```text
fullsend-review
### agent: review
model=claude-opus-4-6 runtime=claude iterations=1 turns=33 cost_usd=2.16544465
{
  "action": "approve",
  "pr_number": 76,
  "head_sha": "f86f6fccf3cda777e85131ce46f6736ee8f585bf",
  "findings": 0
}
```

The same pull request produced three more shim completions (a review-submitted
event and two comment events) within a minute. Each triggered the follow-up,
and each stopped at the gate:

```text
no agent ran on run 35241411949; nothing to do
```

`workflow_run` cannot filter by agent: every shim completion triggers the
follow-up, including runs where dispatch routed nothing (an unauthorized
comment, an event no agent matches) and runs of other agents. The gate step is
one API call; when no `fullsend-*` artifact exists, every later step is
skipped and nothing is downloaded. Budget for one short job per shim run.
With the planned summary artifact
([#7413](https://github.com/fullsend-ai/fullsend/issues/7413)) the gate and the
glob become a single download of `fullsend-summary-*` and a `jq` filter on
`.agent`.

### 3. Act on the result

Now add the follow-up that needs a write permission. Create
`.github/workflows/ci-rerun.yml` on the default branch:

```yaml
name: ci-rerun
on:
  workflow_run:
    workflows: ["fullsend"]          # the shim workflow's name
    types: [completed]

permissions: {}

jobs:
  rerun:
    if: github.event.workflow_run.conclusion == 'success'
    runs-on: ubuntu-24.04
    permissions:
      actions: write                 # read the run, download, re-run
    env:
      AGENT: ci-diagnose             # only this agent's runs matter here
    steps:
      - name: Check this run is a ${{ env.AGENT }} run
        id: find
        env:
          GH_TOKEN: ${{ github.token }}
          RUN_ID: ${{ github.event.workflow_run.id }}
        run: |
          name=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$RUN_ID/artifacts" \
            --jq '.artifacts[].name' | grep -Fx "fullsend-$AGENT" || true)
          echo "artifact=$name" >> "$GITHUB_OUTPUT"
          echo "${name:-no $AGENT result on run $RUN_ID; nothing to do}"

      - uses: actions/download-artifact@v4
        if: steps.find.outputs.artifact != ''
        with:
          name: ${{ steps.find.outputs.artifact }}
          run-id: ${{ github.event.workflow_run.id }}
          github-token: ${{ github.token }}
          path: result

      - name: Re-run jobs the agent classified as flaky
        if: steps.find.outputs.artifact != ''
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          f=$(ls result/*/iteration-*/output/ci-diagnose-result.json 2>/dev/null | sort -V | tail -1) || exit 0
          [ "$(jq -r .recommended_action "$f")" = retry ] || exit 0
          for id in $(jq -r '.retry_targets[].check_run_id' "$f"); do
            job=$(gh api "repos/$GITHUB_REPOSITORY/actions/jobs/$id")
            [ "$(jq -r .conclusion <<<"$job")" = failure ] || continue     # confirm against the API
            run=$(jq -r .run_id <<<"$job")
            [ "$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$run" --jq .run_attempt)" -le 2 ] || continue  # budget
            gh run rerun "$run" --job "$id" --repo "$GITHUB_REPOSITORY"
          done
```

What each guard does:

- **`conclusion == 'success'`** skips failed agent runs; a run whose output
  never validated fails.
- **The gate step** checks the run's artifact list for `fullsend-<agent>`
  before anything is downloaded; other agents' runs and routing-only runs stop
  there.
- **The API lookup** confirms each id is a failed job in this repository. The
  agent's list only selects among facts the API confirms.
- **`run_attempt`** bounds repeats without any bookkeeping of your own.
- **`--repo`** is required: the job has no checkout, so `gh` cannot infer the
  repository.

### 4. Remove the action from the post-script

If the post-script used to perform the same action with the minted token,
delete that code. The role then needs nothing beyond commenting, and one
identity never holds two tokens.

### 5. Verify with a real run

Trigger the agent and follow the chain. Output below is from a probe
repository where `ci` fails on its first attempt:

```bash
gh run list --limit 3 --json name,event,conclusion,databaseId \
  --jq '.[] | "\(.name) [\(.event)] \(.conclusion // "running") id=\(.databaseId)"'
```

```text
ci-rerun [workflow_run] success id=35241406132
fullsend [workflow_run] success id=35241374378
ci [push] failure id=35241360345
```

Then confirm the re-run happened and who triggered it:

```bash
gh api repos/OWNER/REPO/actions/runs/35241360345 --jq '"attempt=\(.run_attempt) conclusion=\(.conclusion)"'
gh api repos/OWNER/REPO/actions/runs/35241360345/attempts/2 --jq .triggering_actor.login
```

```text
attempt=2 conclusion=success
github-actions[bot]
```

The re-run attempt is attributed to `github-actions[bot]` and to the follow-up
run, not to the agent's App. Comments the agent posted still carry the App
identity.

## Rules of the road

- **Treat the result as a filter, never as a command.** Confirm ids and
  states against the API before acting on them.
- **Per-repo installation only.** In the deprecated per-org mode the shim runs
  in the org's host repository, so neither the event nor the job token reaches
  yours.
- **Default branch only.** Edits to the follow-up take effect after they merge.
- **Grant one permission per need.** `actions: write` for re-runs and
  dispatches; nothing else unless the follow-up uses it.
- **Do not add the workflow token to the post-script.** The runner keeps it out
  of pre- and post-script children on purpose.

## Sharing a follow-up with other repositories

GitHub has no installer for workflows. Publish the follow-up as a reusable
workflow next to the agent, and let adopters add a thin caller:

```yaml
# In the agent repository: .github/workflows/rerun.yml
on:
  workflow_call:
    inputs:
      run-id:
        required: true
        type: string
jobs:
  rerun:
    runs-on: ubuntu-24.04
    steps:
      # the download and re-run steps from Step 2, using inputs.run-id
```

```yaml
# In each adopting repository: .github/workflows/ci-rerun.yml
on:
  workflow_run:
    workflows: ["fullsend"]
    types: [completed]
jobs:
  rerun:
    if: github.event.workflow_run.conclusion == 'success'
    permissions:
      actions: write
    uses: OWNER/AGENT-REPO/.github/workflows/rerun.yml@<commit-sha>
    with:
      run-id: ${{ github.event.workflow_run.id }}
```

Adopters pin the caller to a commit, the same way `fullsend agent add` pins
the harness. Move both pins together when releasing. If the organization
restricts which actions may run, the agent repository must be on the allowlist.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Follow-up never runs | Workflow not on the default branch, or `workflows:` does not match the shim's `name:` | Merge it; copy the exact name from the shim |
| Follow-up runs and stops at the gate | The completed run was another agent's, or dispatch routed nothing | Expected; one short job per shim run |
| `Resource not accessible by integration` on re-run | Job lacks `actions: write` | Add it to the job's `permissions` |
| `failed to determine base repo` | `gh` run without a checkout | Pass `--repo "$GITHUB_REPOSITORY"` |
| Re-run refused | The target run is still in progress | Only completed runs can be re-run; wait or skip |

## See also

- [ADR 0115](../../ADRs/0115-user-owned-follow-up-workflows-after-agent-runs.md) — the decision and its rationale
- [Bring Your Own Agent](bring-your-own-agent.md) — building the agent whose result you consume
- [Custom Agent Identity](custom-agent-identity.md) — when a role change is and is not needed
- [GitHub Docs: `workflow_run`](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run)
