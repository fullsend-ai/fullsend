# CEL Triggers Reference

This reference documents how fullsend dispatches custom agents and how to write CEL trigger expressions. For the step-by-step guide to building and registering a custom agent, see [Bring Your Own Agent](bring-your-own-agent.md).

## How custom agents are dispatched

When you register a custom agent and give it a `trigger` expression, fullsend handles the rest — no per-agent workflow file required. Here is how an event reaches your agent on GitHub:

### The dispatch flow

1. **Event arrives.** A GitHub webhook fires (issue opened, label added, comment posted, PR submitted, etc.). The installed shim workflow forwards the event to the centralized dispatch workflow in `.fullsend/`.

2. **Normalize.** The `gha-event` input driver converts the raw GitHub event into a [`NormalizedEvent`](../../normative/normalized-event/v1/) — a forge-neutral struct with fields like `event.entity.kind`, `event.transition.kind`, and `event.actor.role`.

3. **Authorize.** `fullsend dispatch` enforces the platform authorization gate before any agent is considered. Authorization is a platform-level decision — your CEL trigger does not need to implement permission checks (though you can add guards like `event.actor.role` if your agent has stricter requirements).

4. **Enumerate.** Dispatch loads all registered agents from the merged config (`agents:` list in org and per-repo `config.yaml`, plus scaffold discovery). Each harness with a non-empty `trigger` field is a candidate.

5. **Evaluate.** Each candidate's CEL `trigger` expression is evaluated with `event` bound to the `NormalizedEvent`. Every harness whose trigger returns `true` is selected. Multiple agents can match the same event (parallel fan-out).

6. **Launch.** Matched agents are launched via `fullsend run` using the existing sandbox and execution infrastructure. The dispatch workflow passes the event payload, source repo, and any trigger-specific metadata to the agent workflow.

### What you configure vs. what dispatch handles

| You provide | Dispatch handles |
|---|---|
| Harness file with `trigger` expression | Normalizing the raw GitHub event |
| Agent definition (prompt, tools, model) | Authorizing the actor |
| Registration in `config.yaml` | Enumerating and evaluating all registered triggers |
| Pre/post scripts | Launching matched agents in the sandbox |

### Coexistence with built-in agents

Built-in agents (triage, code, review, fix, retro, prioritize) are routed by the dispatch workflow's stage-based routing logic. Custom agents with CEL triggers run alongside them — the two mechanisms coexist. A single event can trigger both a built-in agent via stage routing and one or more custom agents via CEL matching.

You can also keep a hand-written workflow that invokes `fullsend run` with a fixed harness path. CEL-based dispatch and explicit harness invocation may run side by side in the same installation.

## Writing CEL triggers

The harness `trigger` field is a [CEL](https://github.com/google/cel-spec) boolean expression evaluated against the incoming event. The expression has access to a single root variable, `event`, which is a [`NormalizedEvent`](../../normative/normalized-event/v1/) object.

A harness with no `trigger` field (or an empty trigger) is manual-only — it runs via `fullsend run` but is never selected by dispatch.

### NormalizedEvent fields

The `event` variable has the following top-level fields:

| Field | Type | Description |
|---|---|---|
| `event.repo` | string | Repository path (`owner/repo`) |
| `event.entity.kind` | string | `"work_item"` (issue), `"change_proposal"` (PR), or `"conversation"` (Discussion / Slack channel; [ADR 0086](../../ADRs/0086-conversation-surface-for-agent-participation.md)) |
| `event.entity.id` | int | Issue, PR, or conversation number |
| `event.transition.kind` | string | What happened — see [transition kinds](#transition-kinds) |
| `event.transition.label` | object | Present only when `kind == "label_changed"` |
| `event.transition.comment` | object | Present only when `kind == "comment_added"` (conversation comments carry `id` and `parent_id`, with `parent_id == id` for thread roots; [ADR 0086](../../ADRs/0086-conversation-surface-for-agent-participation.md)) |
| `event.transition.review` | object | Present only when `kind == "review_submitted"` |
| `event.actor.id` | string | Forge login of the user or bot that triggered the event |
| `event.actor.kind` | string | `"human"` or `"bot"` |
| `event.actor.role` | string | Repository permission: `admin`, `maintain`, `write`, `triage`, `read`, `none`, `external` |
| `event.actor.is_entity_author` | boolean | True when the actor is the author of the work item, change proposal, or conversation |
| `event.state.labels` | list | Label names on the entity at event time |
| `event.state.change_proposal` | object | Present when a change proposal is involved (includes `is_fork`, `head_ref`, `base_ref`) |
| `event.state.conversation` | object | Required when `entity.kind == "conversation"` (includes `category.name`; optional `category.id` / `slug` / `format`) |
| `event.source.system` | string | `"github"`, `"gitlab"`, `"jira"`, `"manual"`, or `"schedule"` |

This table covers the most common trigger fields. For the complete field list — including `event.entity.url`, `event.entity.key`, `event.source.raw_type`, and all `event.state.change_proposal` sub-fields — see the [NormalizedEvent v1 schema](../../normative/normalized-event/v1/normalized-event.schema.json).

### Transition kinds

| Kind | When it fires |
|---|---|
| `opened` | Issue or PR created |
| `reopened` | Issue or PR reopened after close |
| `edited` | Title/body/metadata edited (no new commits) |
| `synchronized` | PR head branch received new commits |
| `updated` | Legacy umbrella for any modification — prefer `edited` or `synchronized` for new triggers |
| `closed` | Issue or PR closed |
| `merged` | PR merged into target branch |
| `marked_ready` | Draft PR marked ready for review |
| `label_changed` | Label added or removed — check `event.transition.label.name` and `.action` (`"added"` or `"removed"`) |
| `comment_added` | Comment posted — check `event.transition.comment.command` for slash commands |
| `review_submitted` | PR review submitted — check `event.transition.review.state` (`"approved"`, `"changes_requested"`, `"commented"`, `"dismissed"`) |

### Common trigger patterns

**Run on new issues:**
```yaml
trigger: >
  event.entity.kind == "work_item"
    && event.transition.kind == "opened"
```

**Run when a specific label is added:**
```yaml
trigger: >
  event.transition.kind == "label_changed"
    && event.transition.label.name == "ready-for-my-agent"
    && event.transition.label.action == "added"
```

**Run on a slash command (issues and non-fork PRs):**
```yaml
trigger: >
  event.transition.kind == "comment_added"
    && has(event.transition.comment.command)
    && event.transition.comment.command == "/my-command"
    && (!has(event.state.change_proposal) || !event.state.change_proposal.is_fork)
```

This is exactly what [`fullsend agent new --on command:/my-command`](../../cli/agent.md#agent-new)
emits, so a generated trigger and a hand-written one stay the same expression.

Use `!has(...)` rather than `!= null` for the fork guard. `state.change_proposal`
is **absent** on a comment posted to a plain issue, not present-and-null — the
[NormalizedEvent schema](../../normative/normalized-event/v1/normalized-event.schema.json)
requires only `labels` under `state`. Comparing an absent key against `null`
raises a missing-key error, and dispatch reports that as
`::error:: harness dispatch: skipping agent <name>: trigger eval failed` on
**every** issue comment in the repository, so the agent looks permanently
broken. The `!has(...)` form evaluates cleanly on both surfaces.

**Run when a PR is opened or updated (non-fork):**
```yaml
trigger: >
  event.entity.kind == "change_proposal"
    && (event.transition.kind == "opened"
        || event.transition.kind == "synchronized"
        || event.transition.kind == "marked_ready")
    && !event.state.change_proposal.is_fork
```

**Run when review requests changes:**
```yaml
trigger: >
  event.transition.kind == "review_submitted"
    && event.transition.review.state == "changes_requested"
```

**Run only when the actor has write permission:**
```yaml
trigger: >
  event.entity.kind == "work_item"
    && event.transition.kind == "opened"
    && event.actor.role in ["admin", "maintain", "write"]
```

### Guarding optional fields with `has()`

Some NormalizedEvent fields are optional — they are present only for certain transition kinds. For example, `event.transition.comment.command` is set only when the comment contains a slash command. Accessing an absent field in CEL produces a missing-key error. Use `has()` to guard access:

```cel
has(event.transition.comment.command)
  && event.transition.comment.command == "/my-command"
```

Fields that require `has()` guards include `event.transition.comment.command` and any other field tagged as optional in the [NormalizedEvent v1 schema](../../normative/normalized-event/v1/normalized-event.schema.json). Fields like `event.transition.label` and `event.transition.comment` are inherently scoped by `event.transition.kind` checks and do not need `has()` when the trigger already filters on the correct transition kind.

### Checking a label on the entity

Use `event.state.labels` to check labels on the issue or PR at event time:

```yaml
trigger: >
  event.entity.kind == "work_item"
    && event.transition.kind == "comment_added"
    && has(event.transition.comment.command)
    && event.transition.comment.command == "/analyze"
    && "needs-analysis" in event.state.labels
```

### Fork safety

Write-capable agents that push commits or open PRs **must** guard against fork PRs. Use `!event.state.change_proposal.is_fork` in your trigger or rely on the platform authorization gate. Read-only agents (analysis, review) may run on fork PRs when policy allows.

### Verifying your trigger

Before deploying, verify your trigger expression is correct:

1. **Check field paths** against the [NormalizedEvent fields](#normalizedevent-fields) table and the [NormalizedEvent v1 schema](../../normative/normalized-event/v1/normalized-event.schema.json). Common mistakes include using `event.type` (does not exist) instead of `event.entity.kind`, or referencing `event.transition.label` on a non-`label_changed` transition.

2. **Walk through example events.** The [NormalizedEvent examples](../../normative/normalized-event/v1/examples/) directory contains fixtures for common GitHub events (issue opened, label added, PR opened, slash command, review submitted). Open the fixture that matches your intended trigger and manually evaluate your CEL expression against its fields to confirm the result is `true`.

3. **Test end-to-end** by applying the triggering action (e.g., adding a label, posting a slash command) in a test repository where your agent is registered. Check the dispatch workflow run in GitHub Actions to confirm your agent was selected.

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — step-by-step guide to building and registering custom agents
- [NormalizedEvent v1 spec](../../normative/normalized-event/v1/) — full schema and examples for CEL trigger input
