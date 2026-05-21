# Fix Agent

<img src="icons/coder.png" alt="Fix agent icon" width="80">

Review-feedback specialist that reads review comments on open PRs, implements targeted fixes, runs tests and linters, and commits the result.

## How the agent works

The fix agent is triggered when the [review agent](review.md) requests changes or when a human issues a `/fs-fix` command on a PR. It follows the same sandboxed pipeline as the [code agent](code.md).

1. **Pre-script** validates inputs and checks the iteration cap (preventing infinite fix loops).
2. **Sandbox** — the agent reads each review finding, implements targeted fixes, and verifies them against tests and linters.
3. **Validation loop** — the output is checked against a schema, with up to 2 retry iterations if the output is malformed.
4. **Post-script** pushes the commit and posts a summary comment on the PR.

## How it helps

- Review feedback is addressed quickly — often before the reviewer checks back.
- Fixes are scoped to exactly what the review requested, reducing churn.
- The iteration cap prevents the fix and [review](review.md) agents from looping indefinitely.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-fix` | PR comment | Triggers the fix agent on the PR |
| `/fs-fix-stop` | PR comment | Disables the fix agent for this PR |

The `/fs-fix` command accepts optional free-text instructions after the
command. The text is passed to the agent as a human instruction, giving you
direct control over what to fix:

- `/fs-fix` — fix whatever the [review agent](review.md) flagged
- `/fs-fix you forgot to update the docs here`
- `/fs-fix the error handling in processItem needs to distinguish between retryable and fatal errors`

The fix agent also triggers automatically when the [review agent](review.md) submits a
"changes requested" review on a same-repo PR (fork PRs are blocked).

`/fs-fix-stop` adds the `fullsend-no-fix` label to the PR, preventing any
further bot-triggered fix runs. Human-triggered `/fs-fix` commands still work.
Remove the label or use `/fs-fix` to re-engage.

## Control labels

| Label | Meaning |
|-------|---------|
| `fullsend-no-fix` | Prevents bot-triggered fix runs on this PR. Applied by `/fs-fix-stop`. Human `/fs-fix` commands are unaffected. |
| `needs-human` | The fix agent is approaching its iteration cap and needs human direction. Applied automatically when the fix iteration reaches the warning threshold. |

## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

## Source

[`internal/scaffold/fullsend-repo/harness/fix.yaml`](../../internal/scaffold/fullsend-repo/harness/fix.yaml)
