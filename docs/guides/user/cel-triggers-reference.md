# CEL Triggers Reference

This reference documents how fullsend dispatches custom agents and how to write CEL trigger expressions. For the step-by-step guide to building and registering a custom agent, see [Bring Your Own Agent](bring-your-own-agent.md).

## How custom agents are dispatched

When you register a custom agent and give it a `trigger` expression, fullsend handles the rest — no per-agent workflow file required. Here is how an event reaches your agent on GitHub:

### The dispatch flow

1. **Event arrives.** A GitHub webhook fires (issue opened, label added, comment posted, PR submitted, etc.). The repository's installed shim workflow calls the upstream `reusable-dispatch.yml` workflow.

2. **Normalize.** The `gha-event` input driver converts the raw GitHub event into a [`NormalizedEvent`](../../normative/normalized-event/v1/) — a forge-neutral struct with fields like `event.entity.kind`, `event.transition.kind`, and `event.actor.role`.

3. **Authorize.** `fullsend dispatch` enforces the platform authorization gate before any agent is considered. Authorization is a platform-level decision — your CEL trigger does not need to implement permission checks (though you can add guards like `event.actor.role` if your agent has stricter requirements).

4. **Enumerate.** Dispatch loads all registered agents from the merged repository-local config (the `agents:` list in `.fullsend/config.yaml`, optionally layered over `.fullsend/config.base.yaml`, plus scaffold discovery). Each harness with a non-empty `trigger` field is a candidate. If a registered agent's harness cannot be resolved or loaded, dispatch logs a GitHub Actions `::error::` annotation and skips that agent so other agents can still run. If every registered agent fails to load, `fullsend dispatch` exits non-zero instead of emitting an empty matrix — a fully unreadable harness set is a configuration error, not a "no trigger matched" result.

5. **Evaluate.** Each candidate's CEL `trigger` expression is evaluated with `event` bound to the `NormalizedEvent`. Every harness whose trigger returns `true` is selected. Multiple agents can match the same event (parallel fan-out).

6. **Launch.** Matched agents are launched via `fullsend run` in the inline `harness-run` job of `reusable-dispatch.yml`, using the existing sandbox and execution infrastructure. The dispatch workflow passes the event payload, source repo, and any trigger-specific metadata to that job.

### What you configure vs. what dispatch handles

| You provide | Dispatch handles |
|---|---|
| Harness file with `trigger` expression | Normalizing the raw GitHub event |
| Agent definition (prompt, tools, model) | Authorizing the actor |
| Registration in `config.yaml` | Enumerating and evaluating all registered triggers |
| Pre/post scripts | Launching matched agents in the sandbox |

### Coexistence with built-in agents

Built-in agents (triage, code, review, fix, retro, prioritize) are routed by the dispatch workflow's stage-based routing logic. Custom agents with CEL triggers run alongside them — the two mechanisms coexist. A single event can trigger both a built-in agent via stage routing and one or more custom agents via CEL matching.

On GitHub, CEL dispatch ignores the `trigger` of any agent registered under a
built-in name (matched case-insensitively), including an override whose harness
has one: that agent runs only where stage routing sends it, so it never runs
twice. Jira polling, the only poll source that evaluates CEL triggers today, has
no stage routing, so built-in names whose trigger matches are still dispatched.
GitLab routes agents through its own built-in router and does not evaluate
`trigger` expressions.

The review, retro, triage and prioritize agents are the exception on
GitHub: a `trigger` you set on the agent's entry in `config.yaml` decides
whether the built-in `Review`, `Retro`, `Triage` or `Prioritize` job starts,
in place of the stage-based rules. A harness `trigger:` field never changes
built-in routing; copy it into `config.yaml` to opt in. See
[Routing built-in agents with triggers](#routing-built-in-agents-with-triggers).

You can also keep a hand-written workflow that invokes `fullsend run` with a fixed harness path. CEL-based dispatch and explicit harness invocation may run side by side in the same installation.

## Writing CEL triggers

The harness `trigger` field is a [CEL](https://github.com/google/cel-spec)
boolean expression evaluated against the incoming event. The expression has
access to a single root variable, `event`, which is a
[`NormalizedEvent`](../../normative/normalized-event/v1/) object. The same
expression language is used by `agents[].trigger` in `config.yaml`, but that
field decides routing for the built-in review, retro, triage and prioritize
agents only; CEL dispatch of custom agents and the Jira poller use the harness
`trigger:`.

A harness with no `trigger` field (or an empty trigger) is manual-only — it runs via `fullsend run` but is never selected by dispatch.

### NormalizedEvent fields

The `event` variable has the following top-level fields:

| Field | Type | Description |
|---|---|---|
| `event.repo` | string | Repository path (`owner/repo`) |
| `event.entity.kind` | string | `"work_item"` (issue), `"change_proposal"` (PR), or `"conversation"` (Discussion / Slack channel) |
| `event.entity.id` | int | Issue, PR, or conversation number |
| `event.transition.kind` | string | What happened — see [transition kinds](#transition-kinds) |
| `event.transition.label` | object | Present only when `kind == "label_changed"` |
| `event.transition.comment` | object | Present only when `kind == "comment_added"` (conversation comments carry `id` and `parent_id`, with `parent_id == id` for thread roots) |
| `event.transition.review` | object | Present only when `kind == "review_submitted"` |
| `event.actor.id` | string | Forge login of the user or bot that triggered the event |
| `event.actor.kind` | string | `"human"` or `"bot"` |
| `event.actor.role` | string | Human repository permission: `admin`, `maintain`, `write`, `triage`, `read`, `none`, `external`. Current adapters may populate a bot's forge/project permission when lookup succeeds, or `none` when it is unavailable; the planned behavior is `none` for bots. |
| `event.actor.bot_role` | string or null | Optional provider-resolved canonical bot role such as `review`; absent/null for humans and unrecognized bots |
| `event.actor.role_verified` | boolean | Optional lookup status. For humans, true means `actor.role` is a trusted forge permission; for bots, true means bot-role lookup completed, including a successful no-match result |
| `event.actor.is_entity_author` | boolean | True when the actor is the author of the work item, change proposal, or conversation |
| `event.state.labels` | list | Label names on the entity at event time |
| `event.state.change_proposal` | object | Present when a change proposal is involved (includes `is_fork`, `head_ref`, `base_ref`) |
| `event.state.conversation` | object | Required when `entity.kind == "conversation"` (includes `category.name`; optional `category.id` / `slug` / `format`) |
| `event.source.system` | string | `"github"`, `"gitlab"`, `"jira"`, `"manual"`, or `"schedule"` |

> `actor.bot_role` and `actor.role_verified` are planned fields and
> are not yet emitted by the production runtime. The target contract uses
> `actor.role: "none"` for bots, but current adapters may still populate a
> forge/project permission there during migration. `role_verified` is not
> authorization evidence by itself: for bots it only indicates lookup
> completion, not recognition. The permanent label-added exception
> does not require bot-role lookup; its provider-positive bot classification
> and forge-authorized label mutation remain sufficient platform evidence.
> CEL filters selecting a bot role must use `has(event.actor.bot_role)` and
> compare the resolved role.

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

On GitHub, empty-body `commented` review submissions remain available to custom
triggers. A reviewer may submit substantive inline comments without adding a
summary body.

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
    && event.entity.kind == "work_item"
    && has(event.transition.comment.command)
    && event.transition.comment.command == "/my-command"
    && (!has(event.state.change_proposal) || !event.state.change_proposal.is_fork)
```

This is exactly what [`fullsend agent new --on command:/my-command`](../../cli/agent.md#agent-new)
emits, so a generated trigger and a hand-written one stay the same expression.

`event.entity.kind == "work_item"` restricts this to issues and pull requests.
A comment on a GitHub Discussion arrives with `entity.kind` of `conversation`,
so without that clause the agent also fires on discussions.

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

2. **Lint the expression.** Run [`fullsend lint`](../../cli/lint.md) from the
   repository root. It fails when a `trigger` does not compile or does not
   return a boolean, whether the trigger is in a registered harness or on an
   `agents:` entry in `config.yaml`. It does not evaluate the trigger against
   events. For example output, see
   [Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

3. **Walk through example events.** The [NormalizedEvent examples](../../normative/normalized-event/v1/examples/) directory contains fixtures for common GitHub events (issue opened, label added, PR opened, slash command, review submitted). Open the fixture that matches your intended trigger and manually evaluate your CEL expression against its fields to confirm the result is `true`.

4. **Test end-to-end** by applying the triggering action (e.g., adding a label, posting a slash command) in a test repository where your agent is registered. Check the dispatch workflow run in GitHub Actions to confirm your agent was selected.

## Routing built-in agents with triggers

On GitHub, a `trigger` on the `review`, `retro`, `triage` or `prioritize`
entry in `.fullsend/config.yaml` decides when that built-in agent runs, in
place of the dispatch workflow's built-in rules. The trigger reads the same
`event` fields as a harness trigger. This section covers what differs for each
agent and how to check a trigger before you merge. For one short example per
agent, see
[Configuring Agent Behavior](customizing-agents.md#choosing-when-review-runs).

### Choosing when review runs

By default, the dispatch workflow decides when the review agent runs: when a
pull request is opened, pushed to or marked ready, when the
`ready-for-review` label is added, and on a `/fs-review` comment. To decide it
yourself, give the `review` entry in `.fullsend/config.yaml` a `trigger`: a
[CEL expression](#writing-cel-triggers) over the event. The review still
runs in the same `Review` job; the trigger only decides whether that job
starts.

#### Example: review only on `/fs-review`

To stop the automatic review on every pull request but keep `/fs-review`, add
this entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human"
```

`has(event.entity.linked_change_proposal)` keeps the trigger to comments on
pull requests. The `Route` job evaluates the review trigger for the events
the fullsend workflow receives: a pull request opened, pushed to
(`synchronize`), marked ready for review, labeled or unlabeled, and a `/fs-*`
comment on a pull request from a user who is not a bot. It does so only when
the default rules chose review or no stage, and not for a closed pull request
or a `/fs-retro`, `/fs-triage` or `/fs-prioritize` comment (see
[Which trigger decides](#which-trigger-decides)), so a broad trigger can start
reviews the default rules would not. It
never starts a review from a bot's comment.

The trigger is read from your default branch, so a pull request cannot change
its own routing. After the change merges, open a pull request: no review
starts. Comment `/fs-review` on it: one review starts.

#### What opts a repository in

Only a `trigger` on the `review` entry in `config.yaml` moves review routing
to CEL:

| Your configuration | Who decides |
|---|---|
| No `review` entry, or one with only `runtime`, `model`, `effort` or `subagents` | The dispatch workflow, as before |
| `trigger` on the `review` entry | The trigger |
| A `review` entry with `source:` whose harness sets or inherits `trigger:` | The dispatch workflow, as before |

A harness `trigger:` field never changes when a built-in agent runs, whether
your own harness writes it or inherits it through `base:`. To route review by
a trigger you keep in a harness, copy the expression into the `review` entry's
`trigger` in `config.yaml`. The kill switch and `enabled: false` on the review
entry keep working as before. The `Route`
job evaluates the trigger with the fullsend CLI built at the dispatch
workflow's commit; if your workflow call pins `fullsend_version`, the `Review`
job runs that version instead.

#### Known differences from the default rules

A trigger is checked by the same permission gate as other CEL-dispatched
agents: the review harness's `role: review` needs at least triage access, so
triage-role users can start a review as before. Some cases still differ from
the dispatch workflow's built-in rules:

| Case | Default rules | Trigger |
|---|---|---|
| Pull request opened or pushed by a bot without a registered fullsend role | Reviewed | Not reviewed |
| Pull request pushed by the fix agent | Reviewed | Not reviewed until [#8329](https://github.com/fullsend-ai/fullsend/issues/8329) ships, so the review → fix → re-review loop stops |
| Who is checked when a pull request is opened, pushed to or marked ready | The pull request author | The user who sent the event |
| A maintainer's push to an outside contributor's fork pull request | Not reviewed | Reviewed |

> [!WARNING]
> Opt in only after
> [#8329](https://github.com/fullsend-ai/fullsend/issues/8329) ships, or
> accept that pushes from the fix agent no longer start a re-review. Until
> then the trigger path does not look up a bot's role.

#### Checking the decision

When a trigger is set, the `Route` job's "Finalize stage" step logs the
decision for each pull request and pull request comment. To catch an invalid
trigger before it merges, see
[Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

```text
CEL route (review): bash=review cel=skip reason=trigger did not match harness=agents@3c3081741e3c
```

The job summary shows `review_route=cel` when the trigger decided, or
`review_route=bash` when the dispatch workflow did: the kill switch is on or
the review entry is disabled.

| Log text | Meaning | Action |
|---|---|---|
| `cel=skip reason=trigger did not match` | The trigger left this event out | Expected for events your trigger excludes; otherwise fix the trigger |
| `cel=skip reason=actor not authorized` | The actor failed the dispatch permission check | See [Known differences from the default rules](#known-differences-from-the-default-rules) for bots |
| `cel=skip reason=harness unavailable; actor below write` | The review harness could not be fetched or loaded, so only write access passes | Check the route step's warning for the harness error |
| `cel=skip reason=trigger evaluation failed: …` | The trigger compiles but failed on this event, for example it reads a field the event lacks without `has()`; this counts as no match | Guard optional fields with `has()` |
| `CEL route failed; keeping the bash stage` | A CEL route step failed or the configuration is invalid, so the dispatch workflow decided | Open the failed "… CEL route" step for the error, for example an invalid `trigger` or another invalid `agents:` entry |
| `invalid agent config: agents[N] (review): invalid trigger` in any agent job | The `trigger` in `config.yaml` does not compile; every agent run validates the whole `agents:` list, so all agents fail, not only review | Fix the expression in `.fullsend/config.yaml`; see [Checking a trigger before you merge](#checking-a-trigger-before-you-merge) |

### Choosing when retro runs

By default, the dispatch workflow starts the retro agent when a pull request
is closed, merged or not, and on a `/fs-retro` comment on an issue or a pull
request. To decide it yourself, give the `retro` entry in
`.fullsend/config.yaml` a `trigger`. The retro still runs in the same `Retro`
job; the trigger only decides whether that job starts. What opts a repository
in, and how to check the decision, work as for review: see
[What opts a repository in](#what-opts-a-repository-in) and
[Checking the decision](#checking-the-decision), with `retro` in place of
`review` (`CEL route (retro): ...`, `retro_route=cel`). To check a retro
trigger before you merge, see
[Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

#### Example: retro only on `/fs-retro`

To stop the retro on every closed pull request but keep `/fs-retro`, add this
entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: retro
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-retro"
      && event.actor.kind == "human"
```

After the change merges, close a pull request: no retro starts. Comment
`/fs-retro` on it: one retro starts.

The `Route` job asks the trigger only about closed pull requests and
`/fs-retro` comments; every other event keeps the dispatch workflow's rules,
and a bot's `/fs-retro` comment never starts a retro.

#### Who can start a retro

A trigger decides which events start a retro, not who may start one:

- **Closing a pull request:** anyone who closes or merges it, including its
  read-only author and bots without a registered fullsend role, as with the
  dispatch workflow. Only the retro agent gets this exception (the
  [lifecycle-close exception](../../normative/authorization/v1/README.md#lifecycle-close-pull_request_targetclosed)).
- **`/fs-retro`:** users with write access or above.

### Choosing when triage and prioritize run

The triage and prioritize agents opt in the same way as review: give the
`triage` or `prioritize` entry in `.fullsend/config.yaml` a `trigger`, and
that trigger, not the dispatch workflow's built-in rules, decides whether the
`Triage` or `Prioritize` job starts. The rules in
[What opts a repository in](#what-opts-a-repository-in) apply to both, with
`triage` or `prioritize` in place of `review`. Each agent opts in on its own: a
`triage` trigger does not change when prioritize runs. To check either trigger
before you merge, see
[Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

A trigger only sees the events that reach the `Route` job. The workflow that
`fullsend` installs in your repository forwards issues opened, edited and
labeled, and comments that start with `/fs-` from people, not bots. So a
prioritize trigger only decides `/fs-prioritize`, and on a pull request only
`/fs-triage` asks the triage trigger.

By default the dispatch workflow starts:

- **Triage** when an issue is opened or edited, when the `ready-for-triage`
  label is added to an issue, and on a `/fs-triage` comment on an issue or a
  pull request.
- **Prioritize** on a `/fs-prioritize` comment on an issue or a pull request.
  The scheduled scoring run (a scheduler workflow that dispatches
  `prioritize.yml`) does not go through this routing, so a trigger does not
  change it.

#### Example: triage only on `/fs-triage`

To stop triaging every new or edited issue but keep `/fs-triage`, add this
entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: triage
    trigger: >-
      event.entity.kind == "work_item"
      && event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-triage"
      && event.actor.kind == "human"
```

After the change merges, open an issue: no triage starts. Comment
`/fs-triage` on it: one triage starts. The `Route` job summary shows
`triage_route=cel`.

Leave out the `ready-for-triage` label at your own cost: when the triage
agent splits an issue, it labels each new sub-issue `ready-for-triage` so that
the sub-issue is triaged. Keep a label arm like this one if you rely on
splits:

```text
(event.entity.kind == "work_item"
  && event.transition.kind == "label_changed"
  && event.transition.label.name == "ready-for-triage"
  && event.transition.label.action == "added")
```

#### Which trigger decides

The `Route` job asks one agent's trigger per event:

| Event | Agent asked |
|---|---|
| An issue opened, edited or labeled | `triage` |
| A pull request closed, merged or not | `retro` |
| A pull request opened, pushed to, marked ready or labeled | `review` |
| A `/fs-` comment that the built-in rules route to `review`, `retro`, `triage` or `prioritize` | That agent |
| Any other `/fs-retro`, `/fs-triage` or `/fs-prioritize` comment | `retro`, `triage` or `prioritize` |
| Any other `/fs-` comment on a pull request | `review` |
| Any other `/fs-` comment on an issue | `triage` |

An event the built-in rules route to another stage, such as `/fs-code` or the
`ready-to-code` label, is never handed to a trigger.

#### Copying the built-in triage rules into a trigger

To start from the built-in rules and then change them, write them as a
trigger. Keep the `event.actor.kind == "human"` guard on opened and edited:
it stops the triage agent's own sub-issues and edits from being triaged
twice once bots are recognized
([#8329](https://github.com/fullsend-ai/fullsend/issues/8329)).

```yaml
agents:
  - name: triage
    trigger: >-
      (event.entity.kind == "work_item"
        && event.transition.kind in ["opened", "edited"]
        && event.actor.kind == "human")
      || (event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-triage"
        && event.transition.label.action == "added")
      || (event.entity.kind in ["work_item", "change_proposal"]
        && event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-triage"
        && event.actor.kind == "human")
```

It differs from the built-in rules in one case:

| Case | Built-in rules | This trigger |
|---|---|---|
| An issue opened or edited by a bot, for example a sub-issue the triage agent creates | Triaged when the bot holds a collaborator role | Not triaged; the sub-issue is triaged through its `ready-for-triage` label |

### Checking a trigger before you merge

A `trigger` on the `review`, `retro`, `triage` or `prioritize` entry that does
not compile, or does not return a boolean, still merges. After the merge,
every agent run in the repository fails with `invalid agent config:
agents[N] (<name>): invalid trigger`. To catch this before you merge, run
[`fullsend lint`](../../cli/lint.md) from the repository root:

```bash
fullsend lint
```

If the configuration directory is not `.fullsend` in the current directory,
pass its path:

```bash
fullsend lint --fullsend-dir <path>
```

`fullsend lint` loads `.fullsend/config.yaml`, layered over
`.fullsend/config.base.yaml` if you have one, with the same validation agent
runs use. It exits 1 when it finds an error.

`fullsend lint` caches the agents it fetches in `.fullsend/.fullsend-cache/`.
Add `.fullsend/.fullsend-cache/` to your `.gitignore`, or delete the directory
before you commit.

#### A valid trigger

This `.fullsend/config.yaml` holds only the `review` entry from
[Example: review only on `/fs-review`](#example-review-only-on-fs-review):

```yaml
version: "1"
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human"
```

```text
$ fullsend lint
  • Fetching agent review from fullsend-ai/agents@5144c1d561a0
  ✓ agent review resolved from fullsend-ai/agents@main
  ! review: warning: env.sandbox: env.sandbox coexists with host_files entry /home/user/repo/.fullsend/.fullsend-cache/resources/sha256/e2874d7d47a7a710f89ea6dcb99ae3b8c720ac06fccb7499dfddbe14f12b5ecb/content.env (dest: /sandbox/workspace/.env.d/gcp-vertex.env); env.sandbox values take precedence on key collision
  ! review: warning: forge: forge is deprecated; use overlays with CEL when expressions instead (see ADR 0088)
$ echo $?
0
```

There is no `error:` line, so the trigger is valid. The entry has no `source:`,
so `fullsend lint` fetches the `review` agent from the agents repository and
lints it too. The two `warning:` lines come from that agent, not from your
`config.yaml` (see [Reading the output](#reading-the-output)).

#### A syntax error

This `review` trigger ends with a stray `&&`:

```yaml
version: "1"
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human" &&
```

```text
$ fullsend lint
  ✗ config: error: config.yaml: agents[0] (review): invalid trigger: ERROR: <input>:1:212: Syntax error: mismatched input '<EOF>' expecting {'[', '{', '(', '.', '-', '!', 'true', 'false', 'null', NUM_FLOAT, NUM_INT, NUM_UINT, STRING, BYTES, IDENTIFIER}
 | event.transition.kind == "comment_added" && has(event.entity.linked_change_proposal) && has(event.transition.comment.command) && event.transition.comment.command == "/fs-review" && event.actor.kind == "human" &&
 | ...................................................................................................................................................................................................................^
Error: lint found 1 error-level diagnostic(s)
$ echo $?
1
```

`agents[0] (review)` names the entry. `<input>:1:212` is line 1, column 212
of the trigger as YAML reads it: the `>-` block joins the trigger into one
line. The caret marks where parsing failed, here the end of the expression.

#### A trigger that does not return a boolean

This `review` trigger is a string, not a condition:

```yaml
version: "1"
agents:
  - name: review
    trigger: '"opened"'
```

```text
$ fullsend lint
  ✗ config: error: config.yaml: agents[0] (review): invalid trigger: trigger expression must evaluate to bool, got string
Error: lint found 1 error-level diagnostic(s)
$ echo $?
1
```

#### Reading the output

| Output | Meaning | Action |
|---|---|---|
| `invalid trigger: ERROR: <input>:L:N: Syntax error: …` | The trigger does not compile; the caret marks line `L`, column `N` of the trigger as YAML reads it | Fix the expression at the caret in `.fullsend/config.yaml` |
| `invalid trigger: trigger expression must evaluate to bool, got string` | The trigger compiles but returns a string, not `true` or `false` | Write a condition, for example `event.transition.kind == "opened"` |
| `! Git token not available; private repo fetches may fail` | No `GH_TOKEN`, `GITHUB_TOKEN` or `gh` login was found | Nothing to fix for the public agents repository; set `GH_TOKEN` to remove the line |
| A `warning:` line prefixed with an agent name, for example `review: warning: forge: forge is deprecated` | The warning comes from the upstream harness that an entry without `source:` resolves from the agents repository | Nothing to fix in this repository; warnings do not fail the run unless you pass `--strict` |

`fullsend lint` does not evaluate triggers against events. A trigger that
compiles but reads a field the event lacks without `has()` evaluates to an
error at run time, which counts as no match (`cel=skip`); see the
`Guard optional fields with has()` row in
[Checking the decision](#checking-the-decision).

#### Running the check in CI

This GitHub Actions workflow runs `fullsend lint` on every pull request that
changes `.fullsend/config.yaml` or `.fullsend/config.base.yaml`. It installs
the CLI with the `fullsend-ai/fullsend` action and its
`agent: __install_only__` input:

```yaml
name: Lint fullsend config

on:
  pull_request:
    paths: ['.fullsend/config.yaml', '.fullsend/config.base.yaml']

permissions:
  contents: read

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7

      - name: Install fullsend CLI
        uses: fullsend-ai/fullsend@vX.Y.Z
        with:
          agent: __install_only__
          version: vX.Y.Z

      - name: Lint fullsend config
        env:
          GH_TOKEN: ${{ github.token }}
        run: fullsend lint --fullsend-dir .fullsend
```

`fullsend lint` first ships in the release after v0.50.0. Replace both
`vX.Y.Z` values with that release or a later one. The `version` input picks the CLI release
the step installs, and it defaults to `latest`. If the repository vendors the
CLI at `.fullsend/bin/fullsend`, the step uses that binary instead, so it
must also be that release or later.

An entry without `source:` makes `fullsend lint` fetch the agent from the
agents repository, so the job needs network access; `GH_TOKEN` authenticates
that request. With `--offline`, `fullsend lint` uses the agents revision that
an earlier online run cached in the same `.fullsend` directory. Run that online
run with the same forge, set by `--forge` or detected in the same CI
environment; otherwise the forge-specific files are not cached and the offline
run fails.

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — step-by-step guide to building and registering custom agents
- [NormalizedEvent v1 spec](../../normative/normalized-event/v1/) — full schema and examples for CEL trigger input
