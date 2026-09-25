# Chaining Follow-up Workflows After an Agent Run

Run your own GitHub Actions workflow after a fullsend agent finishes, using
what the run already publishes. The follow-up lives in your repository, runs
with the job token, and holds only the permissions you give it. No new agent
role, no change to fullsend.

Typical follow-ups: re-run CI jobs an agent classified as flaky, open a
deployment, notify a channel, or feed the result into another workflow.

This is one option, not a requirement: how you automate your own repository
is up to you. It is the option that works today for actions no built-in role
grants, because fullsend keeps control-plane write access out of agent roles
([ADR 0124](../../ADRs/0124-criteria-for-adding-a-built-in-agent-role.md)).

## Prerequisites

- A repository with fullsend installed in per-repo mode (the `fullsend`
  shim workflow lives in your `.github/workflows/`).
- An agent whose [harness](../../glossary.md#harness) declares a
  `validation_loop` with a `schema`, so the run ends with a validated result
  file ([harness reference](../../reference/harness-reference.md)). See
  [Bring Your Own Agent](bring-your-own-agent.md#minimum-viable-agent).
- Write access to the repository, to add a workflow on the default branch.

## Where each action belongs

| You want to… | Put it in | Token |
|---|---|---|
| Comment, label, review, open a PR | the agent's post-script | the role's minted token |
| Re-run or cancel CI jobs, dispatch a workflow, approve a run, deploy | a follow-up workflow | the job token, with permissions you declare |
| Keep the LLM sandbox weaker than your scripts | the harness `privilege_levels` field (`runtime: read`) | the same role's token at a lower level |

For a permission no built-in role grants, a follow-up workflow is the
simplest place: its permissions are declared in a file you own. `privilege_levels` only narrows a
token within a role, so it does not replace a follow-up workflow. See
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

A run whose output never validates fails, so filter on
`workflow_run.conclusion == 'success'` first. On a successful run, the
validated result is usually in the highest-numbered `iteration-N/output/`.
It is not always there. When no iteration passes inline, a final sweep can
accept an earlier iteration and leave a later, invalid one in the artifact.
Walk the iterations from newest to oldest and use the first result that
matches your schema, as Step 3 does.

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
Actions job ids, run ids, a verdict enum. Keep confidence scores if the follow-up
thresholds on them. Example result for a CI diagnosis agent:

```json
{
  "status": "diagnosed",
  "recommended_action": "retry",
  "retry_targets": [
    { "job_name": "flaky-unit", "job_id": 105264044037 }
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
          {
          echo '```text'
          for art in result/fullsend-*; do
            echo "### agent: ${art#result/fullsend-}"
            for m in "$art"/*/metrics.json; do
              if [ -f "$m" ]; then
                jq -r '"model=\(.model) runtime=\(.runtime) iterations=\(.iterations) turns=\(.num_turns) cost_usd=\(.total_cost_usd)"' "$m"
              fi
            done
            last=$(printf '%s\n' "$art"/*/iteration-*/output | sort -V | tail -1)
            for r in "$last"/*.json; do
              if [ -f "$r" ]; then
                # one JSON line: newlines and control characters stay escaped
                jq -c '{action, pr_number, head_sha, findings: (.findings|length?)}' "$r"
              fi
            done
          done
          echo '```'
          } | tee -a "$GITHUB_STEP_SUMMARY"
```

The summary prints the newest iteration's output for reading, not for acting
on. `jq -c` keeps each result on one line with newlines and control characters
escaped, so agent text cannot start a workflow command or break out of the
code fence in the job summary; Step 3 shows how to select a validated result. A run
without `metrics.json` or without a result file prints only the agent line.

Output from a run of the default review agent:

```text
fullsend-review
### agent: review
model=claude-opus-4-6 runtime=claude iterations=1 turns=33 cost_usd=2.16544465
{"action":"approve","pr_number":76,"head_sha":"f86f6fccf3cda777e85131ce46f6736ee8f585bf","findings":0}
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
      AGENT: ci-retry                # only this agent's runs matter here
      CI_WORKFLOW: ci                # only this workflow's jobs may be re-run
      CI_JOBS: "flaky-unit"          # only these job names (space-separated)
    steps:
      - name: Check this run is a ${{ env.AGENT }} run
        id: find
        env:
          GH_TOKEN: ${{ github.token }}
          RUN_ID: ${{ github.event.workflow_run.id }}
        run: |
          names=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$RUN_ID/artifacts" --jq '.artifacts[].name')
          name=$(grep -Fx "fullsend-$AGENT" <<<"$names" || true)
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
          HEAD_SHA: ${{ github.event.workflow_run.head_sha }}   # the commit the agent ran on
        run: |
          f=""
          for d in $(printf '%s\n' result/*/iteration-*/output | sort -Vr); do   # newest first
            if jq -e '(.recommended_action | IN("retry", "none"))
                      and (.retry_targets | type == "array" and all(.[]; (.job_id | type == "number") and (.job_name | type == "string")))' \
                 "$d/ci-retry-result.json" >/dev/null 2>&1; then
              f="$d/ci-retry-result.json"; break
            fi
          done
          [ -n "$f" ] || { echo "no result matches the schema; nothing to do"; exit 0; }
          echo "using $f"
          [ "$(jq -r .recommended_action "$f")" = retry ] || exit 0
          for id in $(jq -r '.retry_targets[].job_id | numbers' "$f"); do
            job=$(gh api "repos/$GITHUB_REPOSITORY/actions/jobs/$id") || continue        # confirm against the API
            [ "$(jq -r .conclusion <<<"$job")" = failure ] || continue
            [ "$(jq -r .workflow_name <<<"$job")" = "$CI_WORKFLOW" ] || continue
            [ "$(jq -r .head_sha <<<"$job")" = "$HEAD_SHA" ] || continue
            case " $CI_JOBS " in *" $(jq -r .name <<<"$job") "*) ;; *) continue ;; esac
            run=$(jq -r .run_id <<<"$job")
            attempt=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$run" --jq .run_attempt) || continue
            [ "$attempt" -le 2 ] || continue                                             # budget
            gh run rerun "$run" --job "$id" --repo "$GITHUB_REPOSITORY"
          done
```

What each guard does:

- **`conclusion == 'success'`** skips failed agent runs; a run whose output
  never validated fails.
- **The gate step** checks the run's artifact list for `fullsend-<agent>`
  before anything is downloaded; other agents' runs and routing-only runs stop
  there. An API error fails the step instead of reading as "nothing to do".
- **The iteration walk** uses the newest result that matches the schema, so
  an invalid later iteration is skipped. Mirror your harness schema in the
  `jq -e` check. The schema is a filter, not a trust boundary: every guard
  below still applies to a result that passes it.
- **`numbers`** drops any id that is not a number before it reaches a URL.
- **The API lookup** confirms each id is a failed job in this repository, in
  the workflow named by `CI_WORKFLOW`, with a name listed in `CI_JOBS`.
- **`HEAD_SHA`** comes from the event, not the agent. A job on any other
  commit is skipped. For pull request events (`pull_request_target`) the shim
  run's `head_sha` is the pull request head, the same SHA a `pull_request` CI
  job reports; the job's own `GITHUB_SHA` (the base branch) is a different
  value. For comment and issue events it is the default branch, so the
  follow-up skips those runs rather than guessing. The agent can only choose
  among failed, allowlisted jobs on the commit it ran on.
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
gh run list --limit 12 --json name,event,conclusion,databaseId \
  --jq '.[] | select(.conclusion != "skipped") | "\(.name) [\(.event)] \(.conclusion // "running") id=\(.databaseId)"' | head -4
```

```text
fullsend-run-info [workflow_run] success id=36060241831
ci-rerun [workflow_run] success id=36060241805
fullsend [workflow_run] success id=36060218326
ci [push] success id=36060203210
```

Then confirm the re-run happened and who triggered it:

```bash
gh api repos/OWNER/REPO/actions/runs/36060203210 --jq '"attempt=\(.run_attempt) conclusion=\(.conclusion)"'
gh api repos/OWNER/REPO/actions/runs/36060203210/attempts/2 --jq .triggering_actor.login
```

```text
attempt=2 conclusion=success
github-actions[bot]
```

`ci` shows `success` because the list reports the latest attempt. The
follow-up log shows `using result/fs-cir-probe/iteration-1/output/ci-retry-result.json`:
the probe's iteration-2 held an invalid result and was skipped. A second
target with a job id that does not exist returned 404 and was skipped too.

The re-run attempt is attributed to `github-actions[bot]` and to the follow-up
run, not to the agent's App. On a pull request, the re-run shows only as the
new check result; the actor is on the run's attempt page. Comments the agent posted still carry the App
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
      head-sha:
        required: true
        type: string
permissions: {}
jobs:
  rerun:
    runs-on: ubuntu-24.04
    permissions:
      actions: write
    steps:
      # the gate, download and re-run steps from Step 3, with
      # RUN_ID: inputs.run-id and HEAD_SHA: inputs.head-sha
```

```yaml
# In each adopting repository: .github/workflows/ci-rerun.yml
on:
  workflow_run:
    workflows: ["fullsend"]
    types: [completed]
permissions: {}
jobs:
  rerun:
    if: github.event.workflow_run.conclusion == 'success'
    permissions:
      actions: write
    uses: OWNER/AGENT-REPO/.github/workflows/rerun.yml@<commit-sha>
    with:
      run-id: ${{ github.event.workflow_run.id }}
      head-sha: ${{ github.event.workflow_run.head_sha }}
```

`github.event.workflow_run` is not available inside a `workflow_call`
callee, so the caller passes the run id and head SHA as inputs. Keep the
head-SHA check in the callee; it is what ties a re-run to the agent's commit.

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
| Re-run step runs but re-runs nothing | The agent run was triggered by a comment or issue event, so its head SHA is the default branch; or the job name is not in `CI_JOBS` | Expected for those runs; add the job name to `CI_JOBS` if it should be re-run |

## See also

- [ADR 0124](../../ADRs/0124-criteria-for-adding-a-built-in-agent-role.md) — the criteria for adding a built-in agent role
- [Bring Your Own Agent](bring-your-own-agent.md) — building the agent whose result you consume
- [Custom Agent Identity](custom-agent-identity.md) — when a role change is and is not needed
- [GitHub Docs: `workflow_run`](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run)
