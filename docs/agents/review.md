# Review Agent

<img src="icons/review.png" alt="Review agent icon" width="80">

Code review specialist that evaluates pull requests for correctness, security, intent alignment, style, and documentation currency.

## How the agent works

The review agent is triggered when a PR is opened or updated. It follows the same pre-script / sandbox / post-script pipeline as the other agents.

1. **Pre-script** validates inputs and fetches PR metadata.
2. **Sandbox** — the agent runs the `pr-review` orchestrator skill. The orchestrator triages the change, then dispatches specialized sub-agents in parallel — each covering a distinct review dimension (correctness, security, intent & coherence, style & conventions, docs currency, and optionally cross-repo contracts). Sub-agents run concurrently and return structured findings. The orchestrator collects, deduplicates, and synthesizes findings across dimensions, runs PR-level checks (scope authorization, protected paths), and produces a structured JSON review result. The agent cannot push files, edit code, or push — it is strictly read-only.
3. **Validation loop** — the output is checked against a schema, with up to 2 retry iterations if the output is malformed.
4. **Post-script** posts the review on the PR.

If a prior review exists (e.g., re-review after fixes), it is injected into the sandbox so the agent can assess whether previous findings were addressed.

## How it helps

- Every PR gets a thorough review within minutes, regardless of team availability.
- Reviews cover security, correctness, intent & coherence, style, and docs currency — dimensions humans sometimes skip under time pressure.
- The structured output format makes it easy to see what was flagged and why.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-review` | Issue or PR comment | Triggers a review |

The `/fs-review` command does not accept arguments. The review agent also runs
automatically when a PR is opened, synchronized (new commits pushed), or moved
out of draft.

## Control labels

These labels are applied by the review post-script based on the review outcome.

| Label | Meaning |
|-------|---------|
| `ready-for-review` | Signals the review agent to evaluate the PR. Applied by the [code agent](code.md) post-script. |
| `ready-for-merge` | The review agent approved the PR. No blocking findings. |
| `requires-manual-review` | The review agent found issues that require human judgment — it could not confidently approve or reject. |
| `rejected` | The review agent rejected the PR and the post-script closed it. |

When the review agent requests changes (without rejecting), no outcome label is
applied — the `pull_request_review` event triggers the [fix agent](fix.md) directly.

Stale outcome labels from prior review runs are removed before the new one is
applied.

## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

## Source

[`internal/scaffold/fullsend-repo/harness/review.yaml`](../../internal/scaffold/fullsend-repo/harness/review.yaml)
