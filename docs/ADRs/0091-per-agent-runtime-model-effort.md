---
title: "91. Per-agent runtime, model and effort on agents: entries"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - config
  - harness
  - runtime
---

# 91. Per-agent runtime, model and effort on agents: entries

Date: 2026-08-25

## Status

Accepted

## Context

`.fullsend/config.yaml` could not express per-agent `model`, `runtime`, or
`effort`. A repo that needed different agents on different models or
runtimes had to set GitHub repository variables (`TRIAGE_FULLSEND_MODEL`,
`CODE_FULLSEND_RUNTIME`, ...), which live outside the repository: no review,
no git history, no diff, and nothing in the repo records the intent.

The per-role `model:` field in the harness is per-agent by construction,
but repos consuming remote harnesses cannot reach it. The `validModelName`
regex (`^[a-zA-Z0-9_.@-]+$`) also prevented provider-qualified model
identifiers (e.g. `xai-vertex/xai/grok-4.6`) in harness files, forcing
repos on pi to use repository variables as the only path.

`runtime:` was a single repo-wide key (ADR 0033), yet the case that
motivates this record is precisely a repo whose agents run on different
runtimes: fullsend-ai/pi-xai-vertex runs triage and review on pi (Grok)
and code, fix and retro on Claude Code.

Related issues: #6529 (per-role model + effort, scoped out runtime),
#6570 (validModelName `/` restriction), #6577 (alias resolution bug,
independent). #6529 and #6570 are folded into this record deliberately:
a per-agent model field that cannot name `provider/id` would not serve
the pi case that motivates it, so the schema and the validation rule are
one decision, not two.

## Options

**A separate `role_overrides:` map** (the first cut of the implementing
PR, #6583) — keyed by agent name, with `runtime`/`model`/`effort` per key.
Rejected before merge: `config.yaml` already has a per-agent place — the
`agents:` list (ADR 0058) — keyed by the same names with the same layered
merge, so a second per-agent section would have split "what runs" from
"how it runs" and carried a misleading name (it was keyed by agent name,
not harness role, because `code` and `fix` share `role: coder`).

**Fields on the `agents:` entry** — chosen; see Decision.

## Decision

### 1. `runtime`, `model`, `effort` on `agents:` entries

An `agents:` entry may set the three fields. A built-in agent is tuned
with a **name-only entry** (no `source:`); a custom agent carries them on
its `source:` entry:

```yaml
runtime: pi                    # repo default for agents that set none
agents:
  - name: triage
    model: xai-vertex/xai/grok-4.6
  - name: code
    runtime: claude
    model: sonnet
    effort: high
  - source: https://raw.githubusercontent.com/acme/agents/<sha>/harness/lint.yaml#sha256=<hash>
    model: haiku               # custom agent "lint" (name derived from the file, ADR 0058)
```

Names are **agent names** as passed to `fullsend run <agent>` (triage,
code, review, fix, retro, prioritize) or a custom entry's name, matched
case-insensitively. They are NOT harness `role:` values — `code` and
`fix` both carry `role: coder`, while the existing role-prefixed
repository variables (`CODE_FULLSEND_MODEL`, `FIX_FULLSEND_MODEL`) already
key on agent name.

An enabled entry without `source:` must name a built-in agent and set at
least one field; anything else is rejected (`coder` gets a "did you mean
`code`?" hint). Such an *override-only* entry registers no harness: the
built-in keeps resolving through the agents-repo fallback, and it is not
enumerated as a custom harness, locked, or listed as one.

This narrows ADR 0045's "config.yaml does not gain deep-merge
capabilities or per-agent override entries": that decision kept an
agent's *definition* out of `config.yaml`, and it still holds — these
fields tune three operational knobs of an already resolved harness; they
do not define or compose one. Anything beyond runtime/model/effort still
belongs in a harness (`base` composition).

`fullsend agent set <name> [--runtime] [--model] [--effort]` writes the
entry so the file need not be edited by hand.

### 2. Precedence

Config-layer addition, slotting in below per-run overrides:

```
--runtime/--model/--effort flag
  > FULLSEND_* env (including role-prefixed repository variables)
  > the agent's agents: entry
  > repo-wide runtime: / harness model: effort:
  > default
```

The repo-wide `runtime:` key is kept as the default for agents that set
none — deliberately not removed: existing configs, `fullsend github setup
--runtime` and the behaviour-test installs depend on it, and a default is
still the right shape for a repo where every agent runs the same way.
Per-agent is now the primary place to select a runtime; the docs steer
there.

Entries merge per field across the layered config (ADR 0069): the
overlay's non-empty value wins, an empty value inherits the base's; there
is no tombstone to unset a base value short of restating the entry.

Plan output, stderr `runtime: selected ...` lines, and `metrics.json`
(`runtime_source`, `override_source`) name the source as
`<config path> agents.<name>` (the effective config file).

`fullsend run` does not call `Validate()` on the config it loads, so it
validates the effective `agents:` list itself on every run — names,
runtime against `ValidRuntimes()` (a stub runtime cannot be activated
through an entry any more than through `runtime:`), effort against the
shared levels, model against the shared model-reference syntax — in the
overlay and in `config.base.yaml` alike, and fails the run with an error
naming the file and entry rather than silently running without the
settings. Runtime selection and the model/effort application read one
loaded config, so an entry's three fields always agree on their source.

The effective entries, with these fields, are exposed to overlay CEL
expressions as `config.agents` (ADR 0088).

### 3. Segment-based model validation

A shared `ValidModelRef` regex (`^[a-zA-Z0-9_.@-]+(/[a-zA-Z0-9_.@-]+)*$`)
replaces the harness-local `validModelName`. This is a superset of the
previous rule: existing single-segment model names continue to validate,
and `provider/id` forms are now accepted in both harness `model:` fields
and `agents:` entry `model:` values. Malformed forms (`/leading`,
`trailing/`, `a//b`) are rejected.

The effort level list moves to `config.ValidEffortLevels()` for the same
reason: `harness` imports `config`, so the shared lists live in `config`
and both validators read one source of truth.

### 4. Deferred: `models.aliases`

Provider-qualified per-agent models remove most of the need for the
`models.aliases` map proposed in #6529. Alias resolution is tracked
separately in #6577. This ADR does not introduce an alias system.

### 5. Scope: per-repo first

The fields are read wherever `agents:` entries are (org-mode configs
included, since the list is shared), but the CLI (`agent set`), the setup
PR text and the docs target per-repo installs; per-org installation mode
is deprecated (ADR 0044).

## Consequences

- Repos express per-agent runtime, model and effort in one reviewable,
  version-controlled list that also says which harness runs — one place
  per agent.
- Repository variables remain valid for one-off experiments and for
  repos that prefer out-of-band configuration.
- The harness `model:` field now accepts `/` in model identifiers,
  unblocking provider-qualified models in harness files.
- Because `config.yaml` now carries durable per-agent intent, a
  `fullsend github setup` re-run keeps an existing per-repo `config.yaml`
  untouched unless a flag targets a config key, and then changes only
  that key; managed workflow files still refresh. The converge
  full-rescaffold repair (missing workflow) and the GitLab setup path keep
  regenerating the file as before.
- The reusable workflow's `config.yaml` guard allows an enabled entry
  without `source:` when it sets a field. A repo must bump its pin to a
  version that carries this change before adding such an entry: an older
  pinned workflow rejects it for every agent, whereas a separate unknown
  key would have been ignored. The docs say so.
- `review` and `retro` can be put on another runtime through their
  entries but are documented to stay on Claude Code today; this is not
  enforced in validation.
