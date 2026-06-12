# Triage Agent

<img src="icons/triage.png" alt="Triage agent icon" width="80">

Inspects an issue from any supported source (GitHub, Jira), assesses information sufficiency, asks clarifying questions when needed, and produces a structured triage decision that determines whether the issue is ready for implementation.

## How the agent works

The triage agent is source-agnostic. It supports GitHub issues (triggered on issue open/update) and Jira issues (triggered via Jira Automation webhook). The `TRIAGE_SOURCE` environment variable (`github` or `jira`) selects the data source; the scoring logic, questioning guidelines, and output schema are identical across sources.

For GitHub issues, the agent fetches issue content via the `gh` CLI at runtime. For Jira issues, a pre-script fetches all data from Jira's REST API on the host (Jira credentials never enter the sandbox) and mounts it as a JSON file.

The agent runs in a read-only sandbox. It cannot modify issues, push code, or interact with external services. Its only output is a structured JSON triage result consumed by the source-specific post-script, which applies labels and posts a summary comment (markdown for GitHub, ADF for Jira).

## How it helps

- New issues get a response within minutes instead of waiting for a human to notice them.
- Issues missing critical information get a clarification request immediately, shortening the feedback loop with the reporter.
- Well-specified issues are labeled and ready for the [code agent](code.md) without human intervention.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-triage` | GitHub issue comment | Runs triage on the issue |
| `/fs-triage` | Jira issue comment | Runs triage on the Jira issue (via Jira Automation webhook) |

The `/fs-triage` command does not accept arguments — it re-evaluates the issue
using current content, comments, and any prior triage analysis.

### GitHub triggers

Triage also runs automatically when a new issue is opened, when an issue is
edited, and when someone comments on an issue labeled `needs-info` (to
re-evaluate after the reporter provides clarification).

### Jira triggers

Triage runs automatically when a new issue is created in an enrolled Jira
project. The automatic trigger fires once at creation time; subsequent edits do
not re-trigger automatically. Use `/fs-triage` in a Jira comment to re-evaluate
after the reporter provides clarification or the issue is updated.

## Control labels

These labels are managed by the triage agent. It decides the triage
outcome and the post-script applies the corresponding label.

| Label | Meaning |
|-------|---------|
| `needs-info` | The issue lacks sufficient information. The agent posted clarifying questions. |
| `ready-to-code` | The issue is fully specified and low-risk (bug, documentation, performance). Triggers the [code agent](code.md). |
| `triaged` | The issue is fully specified but is a feature or other category that requires human prioritization before coding. |
| `duplicate` | The issue duplicates an existing one. The agent identified the original and the post-script closes the issue. |
| `blocked` | The issue depends on another issue or external condition. The agent identified the blocker. |
| `question` | The issue is a support request or question, not an actionable bug or feature. The agent attempted to answer it. |

The `issue-labels` skill may also apply contextual labels (e.g., `area/api`,
`kind/bug`) but these are informational — they do not control agent behavior.

## Configuration and extension

### Skill: `issue-labels`

The triage agent includes a built-in `issue-labels` skill that discovers your
repo's labels and applies them opportunistically during triage. You can replace
it with your own version to encode your team's labeling knowledge directly in
the skill, keeping it out of `AGENTS.md` (where it would bloat context for
every agent).

To overload the built-in skill, create your own `issue-labels` skill in
`.agents/skills/issue-labels/SKILL.md` and symlink `.claude/skills` to
`.agents/skills` so it's discoverable by both fullsend and local agent tooling.
You can also overload it at the org level in your `.fullsend` config repo at
`customized/skills/issue-labels/SKILL.md`. At runtime, your version replaces
the upstream default — no other configuration needed.

Here's an example that encodes domain-specific labeling rules:

```markdown
---
name: issue-labels
description: >-
  Apply contextual labels to triaged issues using team labeling conventions.
---

# Issue Labels

Apply labels to the issue being triaged. Use the conventions below — do not
invent labels or apply labels not listed here.

## Control labels (never recommend these)

These are managed by the triage pipeline. Never include them in `label_actions`:
`needs-info`, `ready-to-code`, `duplicate`, `blocked`, `triaged`, `question`.

## Area labels

- `area/api` — REST or gRPC surface in `pkg/api/`.
- `area/operator` — Kubernetes controller-runtime code in `internal/controller/`.
  Apply this even if the issue doesn't say "operator" — if it mentions
  reconciliation, finalizers, or CRDs, it belongs here.
- `area/ci` — GitHub Actions workflows, Tekton pipelines, build scripts.

## Kind labels

- `kind/bug` — confirmed defect in existing behavior.
- `kind/flaky-test` — use this instead of `kind/bug` for intermittent test
  failures. These route to a different team.
- `kind/feature` — new capability request.

## Priority labels

- `priority/critical` — production outages or data loss only. Do not apply
  based on user frustration alone.

## Special labels

- `needs/design` — the issue describes a desired outcome but the approach is
  unclear. When applying this label, do NOT also label `ready-to-code`.

## Output

Include recommendations in `label_actions`:

    "label_actions": {
      "reason": "Single sentence explaining the label choices.",
      "actions": [
        { "action": "add", "label": "area/api" }
      ]
    }
```

This gives the triage agent the subtlety it needs to distinguish between
`kind/bug` and `kind/flaky-test`, or to know that `area/operator` applies to
controller-runtime code, without adding label documentation to `AGENTS.md`
where every agent would pay the context cost.

## Jira integration

### Enrolling a Jira project

To enable triage for a Jira project, run:

```
fullsend jira enroll <PROJECT-KEY> --host <jira-host>
```

This registers the project key in your `.fullsend` config repo and creates the
two Jira Automation rules that fire the agent. See the
[Jira Automation Webhook runbook](../runbooks/jira-automation-webhook.md) for
the manual setup procedure.

### Jira control labels

Jira control labels are namespaced with `fullsend:` to avoid conflicts with
your team's existing labeling scheme:

| Label | Meaning |
|-------|---------|
| `fullsend:needs-info` | Same as `needs-info` for GitHub issues |
| `fullsend:ready-to-code` | Same as `ready-to-code` for GitHub issues |
| `fullsend:triaged` | Same as `triaged` for GitHub issues |
| `fullsend:duplicate` | Same as `duplicate` for GitHub issues |
| `fullsend:blocked` | Same as `blocked` for GitHub issues |
| `fullsend:question` | Same as `question` for GitHub issues |
| `fullsend:feature` | Feature request awaiting human prioritization |

### Jira comment format

Triage comments on Jira issues are posted in Atlassian Document Format (ADF).
The post-script converts the agent's markdown output to ADF automatically via
`scripts/markdown-to-adf.py`.

## Source

[`internal/scaffold/fullsend-repo/harness/triage.yaml`](../../internal/scaffold/fullsend-repo/harness/triage.yaml)
