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
synchronized (new commits pushed), or moved out of draft by a user with triage-level repository permission or higher —
subject to the skips below.
On GitLab, automatic review fires when the cron poller sees an MR whose `created_at` is newer than the watermark
(up to one poll interval of delay). Native `merge_request_event` dispatch was removed; all GitLab events route
through the poller. Push-to-open-MR (GitHub `synchronize`) is not auto-detected;
comment `/fs-review` to re-review after new commits.

### Automatic skips

The review agent does not run automatically — though `/fs-review` always works — when:

- **The PR is a draft.** Opening or pushing to a draft does not trigger a review; it runs once the PR is marked ready for review. (Applying the `ready-for-review` label to a draft lifts the draft skip, but the label is an automatic trigger like any other and is still subject to the `fullsend-no-review` and documentation-prose skips below. Only `/fs-review` is an unconditional override.)
- **The PR carries the `fullsend-no-review` label.** See [Control labels](#control-labels) below.
- **The diff is documentation prose only.** A PR whose changed files are all markdown under `docs/guides/`, `docs/problems/`, `docs/agents/` or `docs/glossary.md` — and whose pages carry no executable markup — is skipped with a notice in the job summary. Prose is an allowlist: markdown anywhere else under `docs/` (ADRs, `normative/`, `contributing/`, `reference/`, `cli/`, `architecture.md`, `.vitepress/`, …), markdown elsewhere in the tree (`skills/*/SKILL.md`, `AGENTS.md`, `CLAUDE.md` are executable agent instruction) and lockfiles (a lockfile-only diff can repoint a dependency) are all still reviewed. Because VitePress compiles every page under `docs/` into a Vue component, each allowlisted page is also read at the PR head and keeps its review if `<script`, `<style`, `{{`, an `<!-- @include -->` directive, or a bound attribute or directive on a raw HTML tag appears anywhere on the page — code examples included, so a page that shows one of these in a fenced block stays reviewed — or if its frontmatter has any key other than `title`, `description`, `sidebar_position` and `sidebar_label`, or cannot be parsed as plain YAML. A file renamed into `docs/` is judged on the path it came from, and a page or file listing that could not be read, or a listing that was truncated, never skips.

A push that is skipped for any of these reasons still clears `ready-for-merge` and `ready-for-review`, exactly as a review round would at start — the labels never describe commits nobody reviewed. Installs still on the deprecated per-org mode ([ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md)) get the draft and label skips without this clearing.

See [ADR 0096](../ADRs/0096-skip-provably-unnecessary-review-dispatch.md) for the rationale.

## Control labels

Most of these labels are applied by the review post-script based on the review outcome; `fullsend-no-review` is the exception — see its row below.

| Label | Meaning |
|-------|---------|
| `ready-for-review` | Workflow state marker on the PR. Applied by the [code agent](code.md) post-script after pushing. In per-repo installs, triggers review when applied to a PR. |
| `ready-for-merge` | The review agent approved the PR. No blocking findings. |
| `requires-manual-review` | The review agent found issues that require human judgment — it could not confidently approve or reject. |
| `rejected` | The review agent rejected the PR and the post-script closed it. |
| `fullsend-no-review` | Prevents automatic (bot-triggered) review runs on this PR. Mirrors the [fix agent](fix.md)'s `fullsend-no-fix` label. Explicit `/fs-review` commands are unaffected. Currently applied manually (no `/fs-review-stop` command yet — see [ADR 0096](../ADRs/0096-skip-provably-unnecessary-review-dispatch.md)). |

When the review agent requests changes (without rejecting), no outcome label is
applied. On GitHub, the native `pull_request_review` event triggers the
[fix agent](fix.md) directly. On GitLab, which has no equivalent native review
event, the review bot posts an MR note with the hidden
`<!-- fullsend:changes-requested -->` marker; the scheduled poller retains that
bot-authored note and the dispatch router routes it to fix. Fork merge requests
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
