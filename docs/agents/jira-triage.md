# Jira Triage Agent

Inspects a Jira issue, assesses information sufficiency, asks clarifying questions when needed, and produces a structured triage decision that determines whether the issue is ready for implementation.

## How the agent works

The Jira triage agent is triggered when a new Jira issue is created in an enrolled project, or when someone comments `/fs-triage` on an existing issue. It fetches the issue content — summary, description, type, priority, existing comments — and reads repository context to understand the landscape. It then scores the issue across four dimensions (symptom, cause, reproduction steps, and impact) and decides whether the issue has enough information to act on, whether clarification is needed, or whether it matches another known issue.

The agent posts its triage result as an ADF-formatted comment on the Jira issue and applies namespaced control labels (prefixed `fullsend:`) to reflect the outcome. Unlike GitHub issues, Jira labels are plain strings — the `fullsend:` prefix prevents collisions with your team's existing labeling scheme.

## How it helps

- New issues get a response within minutes instead of waiting for a human to notice them.
- Issues missing critical information get a clarification request immediately, shortening the feedback loop with the reporter.
- Well-specified issues are labeled and ready for the code agent without human intervention.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-triage` | Jira issue comment | Runs triage on the issue |

The `/fs-triage` command does not accept arguments — it re-evaluates the issue
using current content, comments, and any prior triage analysis.

Triage also runs automatically when a new issue is created in an enrolled Jira
project. The automatic trigger fires once at creation time; subsequent edits do
not re-trigger automatically. Use `/fs-triage` to re-evaluate after the reporter
provides clarification or the issue is updated.

## Scoring

The agent scores each issue out of 100 across four dimensions before deciding on
an action. All four dimensions must be sufficiently addressed for an issue to be
considered `sufficient`.

| Dimension | Weight | What it checks |
|-----------|--------|----------------|
| Symptom | 35% | Is the observable problem clearly described? What is the actual vs. expected behavior? |
| Cause | 30% | Is there enough context to understand or hypothesize what is going wrong? |
| Reproduction | 20% | Are there steps, a test case, or a configuration that reproduces the problem? |
| Impact | 15% | Is the scope, frequency, or severity of the problem described? |

Feature requests are scored differently: reproduction steps are not required, and
the impact dimension receives higher weight. The agent detects the issue type
from the Jira `issuetype` field.

## Actions

| Action | Label applied | Meaning |
|--------|--------------|---------|
| `insufficient` | `fullsend:needs-info` | The issue lacks sufficient information. The agent posts clarifying questions. |
| `sufficient` (bug/task) | `fullsend:ready-to-code` | The issue is fully specified. Ready for the code agent. |
| `sufficient` (feature) | `fullsend:feature` | The issue is a well-specified feature request requiring human prioritization before coding. |
| `duplicate` | `fullsend:duplicate` | The issue duplicates an existing one. The agent identifies the original issue. |
| `blocked` | `fullsend:blocked` | The issue depends on another issue or external condition. The agent identifies the blocker. |
| `question` | `fullsend:question` | The issue is a support request or question, not an actionable bug or feature. The agent attempts to answer it. |

When the agent takes the `sufficient` action, it also applies `fullsend:triaged`
to mark that triage has completed. This label is not removed on re-triage;
subsequent runs update the other labels in place.

## Control labels

These labels are managed by the Jira triage agent. Do not add or remove them
manually — the agent may overwrite your changes on the next triage run.

| Label | Meaning |
|-------|---------|
| `fullsend:needs-info` | The issue lacks sufficient information. The agent posted clarifying questions. |
| `fullsend:ready-to-code` | The issue is fully specified and low-risk (bug, task, documentation, performance). |
| `fullsend:feature` | The issue is a well-specified feature request awaiting human prioritization. |
| `fullsend:duplicate` | The issue duplicates an existing one. The agent identified the original. |
| `fullsend:blocked` | The issue depends on another issue or external condition. |
| `fullsend:triaged` | Triage has completed at least once. Present alongside another outcome label. |
| `fullsend:question` | The issue is a support request or question, not an actionable item. |

The `issue-labels` skill may also apply contextual labels (e.g., `area/api`,
`kind/bug`) but these are informational — they do not control agent behavior.

## Configuration and extension

### Enrolling a Jira project

To enable the Jira triage agent for a project, run:

```
fullsend jira enroll <PROJECT-KEY> --host <jira-host>
```

For example:

```
fullsend jira enroll MYPROJ --host myorg.atlassian.net
```

This registers the project key in your `.fullsend` config repo and creates the
two Jira Automation rules that fire the agent. See the
[Jira Automation Webhook runbook](../runbooks/jira-automation-webhook.md) for
the manual setup procedure if you prefer to configure the rules yourself.

### Skill: `issue-labels`

The Jira triage agent includes a built-in `issue-labels` skill that discovers
your project's label conventions and applies them opportunistically during
triage. You can replace it with your own version to encode your team's labeling
knowledge directly in the skill, keeping it out of `AGENTS.md` (where it would
bloat context for every agent).

To overload the built-in skill, create your own `issue-labels` skill in
`.agents/skills/issue-labels/SKILL.md` and symlink `.claude/skills` to
`.agents/skills` so it's discoverable by both fullsend and local agent tooling.
You can also overload it at the org level in your `.fullsend` config repo at
`customized/skills/issue-labels/SKILL.md`. At runtime, your version replaces
the upstream default — no other configuration needed.

The skill applies only contextual labels (e.g., `area/api`, `kind/bug`). It
must never recommend control labels — those are reserved for the triage pipeline.

## Source

[`internal/scaffold/fullsend-repo/harness/jira-triage.yaml`](../../internal/scaffold/fullsend-repo/harness/jira-triage.yaml)
