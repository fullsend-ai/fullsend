---
description: How the fullsend fix agent reads PR review feedback, implements targeted fixes, runs tests and linters, and commits the result.
---

# Fix Agent

![Fix agent icon](icons/coder.png)

Review-feedback specialist that reads review comments on open PRs, implements targeted fixes, runs tests and linters, and commits the result.

## How the agent works

The fix agent is triggered when the [review agent](review.md) requests changes or when a human issues a `/fs-fix` command on a PR. It follows the same sandboxed pipeline as the [code agent](code.md).

1. **Pre-script** validates inputs and checks the iteration cap (preventing infinite fix loops).
2. **Sandbox** — the agent reads each review finding, implements targeted fixes, and verifies them against tests and linters.
3. **Validation loop** — the output is checked against a schema, with up to 2 retry iterations if the output is malformed.
4. **Post-script** pushes the commit and posts a summary comment on the PR.

### What the agent reads

The fix agent has two operating modes with different primary inputs:

**Bot-triggered** (review agent requests changes):

| Input | Source | How it gets there |
|-------|--------|-------------------|
| Review body | Latest `CHANGES_REQUESTED` review from a review bot | Pre-fetched on the runner before the sandbox starts, injected as `review-body.txt` |
| PR diff | `gh pr diff` inside the sandbox | Agent calls this to understand what code changed |
| Repository checkout | Full repo at PR HEAD | Checked out on the runner, mounted into the sandbox |
| Repo conventions | `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md` | Read from the checkout inside the sandbox |

**Human-triggered** (`/fs-fix [instruction]`):

| Input | Source | How it gets there |
|-------|--------|-------------------|
| Human instruction | Free text after `/fs-fix` in the comment | Extracted by the workflow, passed as `HUMAN_INSTRUCTION` env var (up to 10,000 bytes) |
| PR diff | `gh pr diff` inside the sandbox | Same as bot-triggered |
| Repository checkout | Full repo at PR HEAD | Same as bot-triggered |
| Repo conventions | `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md` | Same as bot-triggered |
| Review body (if any) | Prior `CHANGES_REQUESTED` review from a review bot | Still injected as `review-body.txt`, but human instruction takes precedence |

When a human instruction is present, it supersedes the review body as the
primary directive.

### What the agent does not read

This is worth being explicit about, because the fix agent's scope is narrower
than you might expect:

- **Inline PR review comments.** The agent reads the consolidated review body,
  not individual line-level comments. If you need the agent to act on a
  specific inline comment, copy the relevant text into a `/fs-fix` instruction.
- **Other PR comments.** General discussion comments on the PR are not part of
  the agent's context. Only the review body and the `/fs-fix` instruction are
  read.
- **CI logs and check status.** The fix agent does not read GitHub Actions logs,
  check run output, or merge readiness indicators. It addresses review
  feedback, not CI failures. (The [code agent](code.md) handles CI failures
  during implementation.)
- **Issue body.** The fix agent does not read the linked issue. It operates
  purely on the PR and review context.

### Links and URLs in instructions

The `/fs-fix` instruction text can contain URLs. Whether the agent can use them
depends on where the URL points:

| URL type | Works? | Why |
|----------|--------|-----|
| Same-repo issue or PR (`#123` or full GitHub URL) | Yes | Agent resolves via `gh` CLI through the GitHub API |
| Same-repo file or commit | Yes | Same mechanism — GitHub API via minted token |
| Cross-repo GitHub URL | No | Minted token is scoped to the target repo only |
| GitHub Gist | No | `gist.github.com` is not routable through the sandbox proxy |
| External URL (docs, pastebins, etc.) | No | Sandbox proxy blocks all non-API HTTP egress (403 Forbidden) |

GitHub may auto-shorten same-repo URLs in rendered comments (e.g.,
`https://github.com/org/repo/issues/2` becomes `#2`), but the dispatch
pipeline reads the raw comment body, so the full URL is preserved in the
instruction text either way.

**If you need the agent to act on external context**, paste the relevant
content directly into the `/fs-fix` comment rather than linking to it. The
instruction supports multi-line text (up to 10,000 bytes).

### Iteration limits

The fix agent enforces iteration caps to prevent infinite review-fix loops:

- **Bot-triggered:** up to 5 iterations per PR (configurable).
- **Human-triggered:** up to 10 total iterations per PR (configurable), shared
  across bot and human triggers.
- When a bot-triggered run is approaching the bot cap, the agent applies the
  `needs-human` label.
- Each `/fs-fix` comment cancels any in-flight fix run for the same PR and
  starts a new one.

## How it helps

- Review feedback is addressed quickly — often before the reviewer checks back.
- Fixes are scoped to exactly what the review requested, reducing churn.
- The iteration cap prevents the fix and [review](review.md) agents from looping indefinitely.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-fix` | PR comment | Triggers the fix agent on the PR |
| `/fs-fix-stop` | PR comment | Disables the fix agent for this PR |

Requires write-level repository permission (admin, maintain, or write).

The `/fs-fix` command accepts optional free-text instructions after the
command. The text is passed to the agent as a human instruction, giving you
direct control over what to fix:

- `/fs-fix` — fix whatever the [review agent](review.md) flagged
- `/fs-fix you forgot to update the docs here`
- `/fs-fix the error handling in processItem needs to distinguish between retryable and fatal errors`
- `/fs-fix address the concern raised in #42` — same-repo references work
  ([details](#links-and-urls-in-instructions))

The fix agent also triggers automatically when the [review agent](review.md) submits a
"changes requested" review on a same-repo PR (fork PRs are blocked).

For **PRs authored by the fullsend code agent** (`fullsend-ai-coder[bot]`),
automatic fixing happens with no extra setup — the fix agent responds to review
feedback out of the box. PRs from other bots (e.g., Renovate) require the
`fullsend-fix` label; without it, the bot-triggered fix run is dispatched but
the eligibility check exits early with a warning. See
[Bot identities](../contributing/bot-identities.md) for how the eligibility
script identifies the coder bot across different API surfaces.

For **human-authored PRs**, the fix agent will not auto-fix review feedback
unless you opt in by adding the `fullsend-fix` label. This prevents the agent
from pushing commits to your branch without your consent. You can still use
`/fs-fix` at any time regardless of the label — the label only controls
automatic (bot-triggered) runs.

`/fs-fix-stop` adds the `fullsend-no-fix` label to the PR, preventing any
further bot-triggered fix runs. Human-triggered `/fs-fix` commands still work.
Remove the label or use `/fs-fix` to re-engage.

## Control labels

| Label | Meaning |
|-------|---------|
| `fullsend-fix` | Enables automatic bot-triggered fix runs on human-authored PRs and PRs from bots other than the fullsend code agent. Without this label, the fix agent only runs when explicitly invoked via `/fs-fix`. PRs authored by `fullsend-ai-coder[bot]` are always eligible without this label. |
| `fullsend-no-fix` | Prevents bot-triggered fix runs on this PR. Applied by `/fs-fix-stop`. Human `/fs-fix` commands are unaffected. Takes priority over `fullsend-fix`. |
| `needs-human` | The fix agent is approaching its iteration cap and needs human direction. Applied automatically when a bot-triggered fix iteration reaches the warning threshold. |

## Configuration and extension

See [Configuring with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Configuring with Skills](../guides/user/customizing-with-skills.md).

### Image and network policy synchronization

By default, the fix and [code agent](code.md) use the same upstream container
image and overlapping sandbox policies (`policies/code.yaml` and
`policies/fix.yaml` in [fullsend-ai/agents](https://github.com/fullsend-ai/agents)).
They are separate harnesses, though — you can override each independently when
their needs diverge. For example, you might keep Jira endpoints out of the code
agent's policy while allowing them on the fix agent when reviewers ask you to
verify something against a ticket during PR feedback.

> **Warning:** If you customize image, `policy:`, or `providers:` on only one
> agent by mistake, the other may fail with no obvious reason (for example, a
> package manager or registry endpoint allowed in code but not fix).

**Recommended configuration**

If you want both agents to share one behavior set — one place to edit image,
policy, providers, and runner scripts — put the same overrides in both harness
files in your repo's `.fullsend/` directory:

```yaml
# .fullsend/harness/code.yaml  (register as source: harness/code.yaml in config.yaml)
base: https://raw.githubusercontent.com/fullsend-ai/agents/<tag>/harness/code.yaml#sha256=…
image: ghcr.io/your-org/your-fullsend-image@sha256:…
policy: policies/base.yaml
providers:
  - vertex-ai
  - github
  - package-registries

# .fullsend/harness/fix.yaml  (register as source: harness/fix.yaml in config.yaml)
base: https://raw.githubusercontent.com/fullsend-ai/agents/<tag>/harness/fix.yaml#sha256=…
image: ghcr.io/your-org/your-fullsend-image@sha256:…
policy: policies/base.yaml   # same file — edit once, both agents use it
providers:
  - vertex-ai
  - github
  - package-registries
```

Keep the shared policy at `.fullsend/policies/base.yaml`. The policy file
defines non-network sandbox restrictions only — filesystem access, landlock, and
process identity. Network access is controlled through `providers:` profiles
listed in the harness (see [Architecture](../architecture.md#agent-sandbox) for details on
provider-backed policy composition) — when
unifying configuration, keep the `providers:` list the same on both harnesses
too. The same pattern applies to `pre_script` and `post_script` when you want a
single place to maintain runner-side behavior.

See [Customizing Agents](../guides/user/customizing-agents.md) for harness
composition.

### Variables

None.

## Source

[`fullsend-ai/agents` — `harness/fix.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/fix.yaml)
