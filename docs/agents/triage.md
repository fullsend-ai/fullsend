# Triage Agent

<img src="icons/triage.png" alt="Triage agent icon" width="80">

Inspects a GitHub issue, assesses information sufficiency, asks clarifying questions when needed, and produces a structured triage decision that determines whether the issue is ready for implementation.

## How the agent works

The triage agent is triggered when a new issue is opened or when an existing issue is updated. It fetches the issue content — title, body, labels, comments — and reads repository context (architecture docs, existing issues, PRs) to understand the landscape. It then decides whether the issue has enough information to act on, or whether clarification is needed.

The agent runs in a read-only sandbox. It cannot modify issues, push code, or interact with external services. Its only output is a structured JSON triage result consumed by the post-script, which applies labels and posts a summary comment.

## How it helps

- New issues get a response within minutes instead of waiting for a human to notice them.
- Issues missing critical information get a clarification request immediately, shortening the feedback loop with the reporter.
- Well-specified issues are labeled and ready for the [code agent](code.md) without human intervention.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-triage` | Issue comment | Runs triage on the issue |

The `/fs-triage` command does not accept arguments — it re-evaluates the issue
using current content, comments, and any prior triage analysis.

Triage also runs automatically when a new issue is opened, when an issue is
edited, and when someone comments on an issue labeled `needs-info` (to
re-evaluate after the reporter provides clarification).

## Control labels

These labels are managed by the triage agent. It decides the triage
outcome and the post-script applies the corresponding label.

| Label | Meaning |
|-------|---------|
| `needs-info` | The issue lacks sufficient information. The agent posted clarifying questions. |
| `ready-to-code` | The issue is fully specified and low-risk (bug, documentation, performance). Triggers the [code agent](code.md). |
| `triaged` | The issue is fully specified but is a feature or other category that requires human prioritization before coding. |
| `duplicate` | The issue duplicates an existing one. The agent identified the original and the post-script closes the issue. |
| `blocked` | The issue depends on prerequisites — existing issues/PRs or newly created upstream issues. The agent identified or created the blockers. |
| `question` | The issue is a support request or question, not an actionable bug or feature. The agent attempted to answer it. |

The `issue-labels` skill may also apply contextual labels (e.g., `area/api`,
`kind/bug`) but these are informational — they do not control agent behavior.

## Configuration and extension

### Cross-repo issue creation

The triage agent can create prerequisite issues in other repositories when it
identifies upstream dependencies that don't have tracking issues yet. This is
controlled by the `create_issues` section in `config.yaml`:

```yaml
create_issues:
  allow_targets:
    orgs:
      - my-org
    repos:
      - upstream-org/specific-repo
```

**Defaults:** At install time, fullsend populates this with your org (in org mode)
or your repo (in per-repo mode), plus `fullsend-ai/fullsend` as an upstream target.

**When to expand the allowlist:** If your project depends on libraries or services
in other GitHub orgs and you want the triage agent to automatically file
prerequisite issues there, add those orgs or repos to `allow_targets`.

**When to restrict the allowlist:** If you don't want agents creating issues
outside your org, remove entries. If `allow_targets` is empty, automatic
prerequisite creation is disabled entirely — the agent will still identify
the dependency and include a draft issue body in its comment for a human to
file manually.

The source repo (where triage is running) is always implicitly allowed
regardless of the allowlist.

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

## Source

[`internal/scaffold/fullsend-repo/harness/triage.yaml`](../../internal/scaffold/fullsend-repo/harness/triage.yaml)
