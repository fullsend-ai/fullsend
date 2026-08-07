---
title: "80. config.yaml vs. agent env vars: where a config option belongs"
status: Accepted
relates_to:
  - agent-infrastructure
  - governance
topics:
  - configuration
  - harness
  - conventions
---

# 80. config.yaml vs. agent env vars: where a config option belongs

Date: 2026-07-31

Amends: [ADR 0049](0049-agent-configuration-env-var-convention.md)

## Status

Accepted

## Context

[fullsend-ai/agents#567](https://github.com/fullsend-ai/agents/pull/567)
added `TRIAGE_AUTO_CODE` as a harness `env.runner` default in
`harness/triage.yaml`. A reviewer flagged that fullsend-ai/fullsend#1754,
the issue that requested the knob, asked for it to "live in the per-repo/
per-org config surface" — i.e. `.fullsend/config.yaml` — not a harness
default (see the [review
discussion](https://github.com/fullsend-ai/agents/pull/567#discussion_r3686020058)).
`config.yaml` already carries fields like `create_issues.allow_targets`
that are unrelated to any single agent, per the [governance](../problems/governance.md)
and [agent infrastructure](../problems/agent-infrastructure.md) problem
docs. [ADR 0049](0049-agent-configuration-env-var-convention.md) defines
how agent config env vars are *named* (`{AGENT}_{SETTING_NAME}`) and
requires separate per-agent vars when the same concept is independently
tunable per agent (e.g. `CODE_MAX_FILE_SIZE` vs `REVIEW_MAX_FILE_SIZE`),
but it draws no line against `config.yaml` — nothing in it says when a
knob should live there instead of as an env var, so there was no rule to
check the PR against.

## Decision

A config option belongs in exactly one of the two surfaces, based on
whether its behavior is meaningful to more than one agent. This narrows
ADR 0049's per-agent-vars rule (a setting that applies to multiple agents
gets separate vars per agent) to the case where each agent needs its own
independently tunable value; a single value meant to apply the same way
across every agent is a different case, and belongs in `config.yaml`
instead:

- **Pipeline/dispatch policy — governs whether or how agents run, or
  applies the same way across every agent, rather than tuning one agent's
  own inference-time logic:** it is a `config.yaml` field. It gets a plain
  name with no `{AGENT}_` prefix, and it is not also settable via
  environment variable — `config.yaml` (`internal/config` accessors) is
  the single source of truth. `roles`, `kill_switch`, and
  `create_issues.allow_targets` are existing examples.
- **Single-agent behavior tuning — adjusts how one specific agent does its
  own job:** it is an `{AGENT}_`-prefixed env var per ADR 0049, delivered
  via that agent's `env.runner`/`env.sandbox`. The prefix matters even
  though the var lives in one agent's harness: `.env` files can be sourced
  together and `runner_env`/`env.sandbox` can share a host environment, so
  the agent name scopes the var and prevents collisions in those shared
  contexts (ADR 0049, Consequences). It is not also settable as
  a `config.yaml` field — overriding it per repo or org means overriding
  the harness (e.g. via `base:` composition, per ADR 0045), not adding a
  parallel field to `config.yaml`.

**Override convention:** `env.runner`/`env.sandbox` values are agent
defaults. A per-repo or per-org override edits the harness (`base:`
composition, per ADR 0045), not the CI workflow `env:` block.
[ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md) reserves that
block for infrastructure plumbing (credentials, project IDs, regions),
not agent behavior knobs — overriding ADR 0049's "CI workflow injection"
delivery mechanism for behavior knobs specifically; see that ADR for the
full rule and its exceptions. This also means behavior defaults in
`env.runner`/`env.sandbox` must be literals (e.g. `TRIAGE_AUTO_CODE:
"on"`), not shell-style passthrough expressions (e.g.
`${TRIAGE_AUTO_CODE:-on}`) — `env.runner`/`env.sandbox` support `${VAR}`
host-variable expansion (see [ADR 0055](0055-unified-env-var-delivery.md),
§ Runner behavior), not shell default-value syntax, so passthrough syntax
is rejected at harness load: env validation treats `TRIAGE_AUTO_CODE:-on`
as a host variable name and fails with `host variable … is not set`; even
absent validation, `os.Expand` would resolve the whole reference to an
empty string, not the intended default.

A knob only moves from one surface to the other by a deliberate migration,
not by adding a second way to set the same value. Applying this rule to
`TRIAGE_AUTO_CODE` is a boundary case: it decides whether the code agent
runs next, which sounds like dispatch policy, but that decision is made
inside triage's own post-script, as part of triage's inference-time
behavior — not by a shared dispatch/CLI layer gating multiple agents
uniformly. That keeps it single-agent behavior tuning today, so it
correctly belongs in `harness/triage.yaml` `env.runner`, not
`config.yaml`. If the check is ever lifted out of triage's post-script
into a shared dispatch layer, it becomes pipeline/dispatch policy and
should move to `config.yaml` as a deliberate migration, not before.
fullsend-ai/fullsend#1754's "per-repo/per-org config
surface" request is satisfied by documenting the existing harness override
path (`base:` composition or an org/repo harness copy), not by adding a
`config.yaml` field — the gap the reviewer found is a documentation gap,
not a placement gap.

## Consequences

- Resolves the fullsend-ai/agents#567 ambiguity: `TRIAGE_AUTO_CODE` stays
  an env var; each agent's doc file (in `fullsend-ai/agents`) Variables
  table must state how to override it (which harness layer to edit),
  matching the guidance ADR 0049 already expects.
- `config.yaml` cannot accumulate `{agent}_foo`-style fields — any such
  field is a signal the knob was misplaced.
- An env var cannot quietly gain a `config.yaml` mirror with its own
  precedence rules; there is one settable location per config option.
- A knob whose scope grows from one agent to several requires a new
  decision (an ADR update or explicit review), not a silent field
  addition to `config.yaml`.
