---
description: How the fullsend review agent evaluates pull requests for correctness, security, intent alignment, style, and documentation currency.
---

# Review Agent

![Review agent icon](icons/review.png)

Code review specialist that evaluates pull requests for correctness, security, intent alignment, style, and documentation currency.

## How the agent works

The review agent is triggered when a PR is opened or updated. It follows the same pre-script / sandbox / post-script pipeline as the other agents.

1. **Pre-script** validates inputs and fetches PR metadata.
2. **Sandbox** — the agent runs the `pr-review` orchestrator skill. The orchestrator triages the change, then dispatches specialized sub-agents in parallel — each covering a distinct review dimension (correctness, security, intent & coherence, style & conventions, docs currency, and optionally cross-repo contracts). Sub-agents run concurrently and return structured findings. The orchestrator collects, deduplicates, and synthesizes findings across dimensions, runs PR-level checks (scope authorization, protected paths), and produces a structured JSON review result. The agent cannot push files, edit code, or push — it is strictly read-only.
3. **Validation loop** — the output is checked against a schema, with up to 2 retry iterations if the output is malformed.
4. **Post-script** posts the review on the PR. Findings that include a file path and a line in the change diff are also posted as inline comments on that line (GitHub review comments; GitLab merge-request discussions). Findings that cannot be positioned — file-level notes, lines outside the diff, or a GitLab diff version whose `head_sha` no longer matches the reviewed commit — stay in the sticky review comment, and on GitLab as general MR notes. Once the sticky review comment is posted, a failure to submit the forge's native review (GitHub PR review or GitLab approval) is logged as a warning and does not fail the run — the sticky comment is the authoritative record of the verdict.

If a prior review exists (e.g., re-review after fixes), it is injected into the sandbox so the agent can assess whether previous findings were addressed.

## How it helps

- Every PR gets a thorough review within minutes, regardless of team availability.
- Reviews cover security, correctness, intent & coherence, style, and docs currency — dimensions humans sometimes skip under time pressure.
- The structured output format makes it easy to see what was flagged and why.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-review` | PR comment | Triggers a review on the PR (per-repo installs only; standalone issues are ignored) |

Requires triage-level repository permission or higher (triage, write,
maintain, or admin). Mutation stages such as `/fs-fix` still require
write or higher.

The `/fs-review` command does not accept arguments. The review agent also runs automatically when a PR is opened,
synchronized (new commits pushed), or moved out of draft by a user with triage-level repository permission or higher.
If a built-in GitHub review run is cancelled while its commit is still the
pull request HEAD, its GitHub status comment identifies that commit and directs
maintainers to review or retry with `/fs-review` before merging. If a newer
commit superseded the cancelled review, the comment directs maintainers to
check the new HEAD's review status without suggesting `/fs-review`, which could
cancel an in-progress review for that HEAD. If the current HEAD cannot be
verified, the comment omits retry guidance. Status comments routed to Jira omit
this GitHub-only guidance.

The reviewed commit comes from the explicit workflow input or the PR event's
`pull_request.head.sha`; GitHub's generic `GITHUB_SHA` can identify a merge or
base commit and is not used to decide whether a cancelled review was superseded.

GitHub review submissions, including `commented` reviews with an empty summary,
continue through normal routing. An empty summary can still accompany
substantive inline review comments, so custom triggers receive the complete
review event.

On GitLab, opening an MR triggers automatic review through the
[ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md) webhook fast
path. `fullsend repos install` provisions a project webhook with merge-request
events enabled. Each event starts a dispatcher pipeline on the protected default
branch, which dispatches review without waiting for a poll. Provisioning is
deferred until the dispatcher and its scripts are on the default branch, the
default branch is protected, and `no_one_allowed` is verified. A repository
whose scaffold merge request is still open therefore relies on the poller alone
once the scaffold is merged to a protected default branch. Until then neither
path dispatches, because the poll jobs also require a protected ref. The
[ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md) cron poller stays
in place as the reconciliation backstop. When a webhook delivery is missed or
the fast path is not provisioned, review fires once the poller sees an MR whose
`created_at` is newer than the watermark (up to one poll interval of delay).
Native `merge_request_event` pipeline dispatch was removed.
A push to an open MR (GitHub `synchronize`) also triggers automatic review,
through the poller only, so expect up to one poll interval of delay. The
webhook dispatcher rejects merge request updates. GitLab records a new diff
version on each push. When the newest version moves the MR head and was created
after the watermark, the poller dispatches review for that head. This includes
fix-agent pushes to bot-authored MRs, and the fix stage's iteration cap bounds
the loop. Dispatch is keyed on the head SHA, so later polls that see the same
head do not review it again. Diff versions do not record who pushed, so the
poller cannot authorize the pusher. It therefore reviews pushes only to
same-project MRs, where any pusher already has project push access. A push to
a fork MR is not reviewed automatically, and re-review of fork MR pushes is
currently unsupported (the router also denies slash commands on fork MRs).

## Control labels

These labels are applied by the review post-script based on the review outcome.

| Label | Meaning |
|-------|---------|
| `ready-for-review` | Workflow state marker on the PR. Applied by the [code agent](code.md) post-script after pushing. In per-repo installs, triggers review when applied to a PR. |
| `ready-for-merge` | The review agent approved the PR. No blocking findings. |
| `requires-manual-review` | The review agent found issues that require human judgment — it could not confidently approve or reject. |
| `rejected` | The review agent rejected the PR and the post-script closed it. |

When the review agent requests changes (without rejecting), no outcome label is
applied. On GitHub, the native `pull_request_review` event triggers the
[fix agent](fix.md) directly. On GitLab, which has no equivalent native review
event, the review bot posts an MR note with the hidden
`<!-- fullsend:changes-requested -->` marker; the webhook fast path, with the
scheduled poller as reconciliation backstop, retains that bot-authored note and
the dispatch router routes it to fix. Both paths retain the note only when its
author is the Poller bot user, so with distinct Analyst and Poller identities
it does not dispatch automatically and `/fs-fix` is needed. Fork merge requests
are blocked from automatic fix runs, matching GitHub. Comment-only reviews do
not carry the marker and do not dispatch fix.

Stale outcome labels from prior review runs are removed before the new one is
applied.

The `issue-labels` skill may also apply contextual labels (e.g., `area/api`,
`priority/high`) but these are informational -- they do not control agent
behavior.

## Configuration and extension

### Skill: `issue-labels`

The review agent includes the `issue-labels` skill to discover your repo's
labels and apply them to PRs during review. This is the same built-in skill
used by the [triage agent](triage.md). Unique-named repo skills extend both
agents; overriding the built-in skill is per-agent via `base:` composition.

To **extend**, add a uniquely named skill in `.agents/skills/` and symlink
`.claude/skills` to `.agents/skills` so it is discoverable by both fullsend
and local agent tooling. A same-named `issue-labels` skill in that directory
is shadowed by the built-in version and is ignored.

To **override** the built-in skill, register the review agent with a harness
that uses `base:` composition and include your replacement `issue-labels`
skill in the `skills:` list -- see
[Configuring with Skills](../guides/user/customizing-with-skills.md#overriding-built-in-skills)
and [Bring Your Own Agent](../guides/user/bring-your-own-agent.md).

See [Configuring with AGENTS.md](../guides/user/customizing-with-agents-md.md).

### Variables

| Variable | Description | Default | Valid values |
|----------|-------------|---------|--------------|
| `REVIEW_FINDING_SEVERITY_THRESHOLD` | Minimum severity for findings to include in the review. Findings below this level are omitted from both the narrative body and the posted inline comments. | `low` | `info`, `low`, `medium`, `high`, `critical` |

Set this in the harness's `env.sandbox` (the upstream default lives in
`harness/review.yaml`). To override per repo or org, use `base:`
composition rather than the CI workflow `env:` block — workflow `env:`
is reserved for infrastructure plumbing (see [Architecture](../architecture.md#agent-harness)
for details on harness composition and workflow-env conventions).
The post-script reads the value from the runner environment directly —
no separate configuration is needed.

The review agent omits findings below the threshold from its output. The
post-script also filters the structured `findings` array as
defense-in-depth. When filtering removes all findings from a
`request-changes` or `reject` verdict, the post-script downgrades the
verdict to `comment` (applying the `requires-manual-review` label).

## Source

[`fullsend-ai/agents` — `harness/review.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/review.yaml)
