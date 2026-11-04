---
title: "158. Overlays may grant runtime fetch within the config-level allowlist"
status: Accepted
relates_to:
  - security-threat-model
topics:
  - harness
  - overlays
  - security
  - remote-resources
---

# 158. Overlays may grant runtime fetch within the config-level allowlist

Date: 2026-10-27

Amends: [ADR 0112](0112-overlays-may-set-any-harness-field.md)

## Status

Accepted

## Context

[ADR 0112](0112-overlays-may-set-any-harness-field.md) Decision 1 lets an
overlay ([ADR 0088](0088-cel-guarded-overlays.md)) set any harness field and
keeps field merge rules unchanged. The three fetch fields of
[ADR 0024](0024-harness-definitions.md), `allowed_remote_resources`,
`allow_runtime_fetch` and `max_runtime_fetches`, have no merge rule to keep:
`base:` composition drops them so that a base cannot widen the child's fetch
grants ([harness reference](../reference/harness-reference.md)).
Once Decision 1 is implemented, an overlay could set them and nothing would
apply them.

[ADR 0038](0038-universal-harness-access.md) makes a harness's own list a subset
of the config-level `allowed_remote_resources`, which the commands test by
prefix after normalization. Fetched content is an injection surface ([security
threat model](../problems/security-threat-model.md#persistent-injection-via-externally-editable-resources)),
so an overlay that could add any prefix would widen what the sandbox may fetch.

This ADR aims to let one harness turn on runtime fetch for one context without
widening what the sandbox may fetch past the repository's configuration.

## Options

**Keep the fetch fields out of overlays** (an exception to ADR 0112's "any
field"; fetch for some runs needs a second harness), or **let a matching
overlay set them within the config-level allowlist** (chosen).

## Decision

We let a matching overlay grant runtime fetch, bounded by the config-level
allowlist, so that fetch can be turned on per context.

A matching overlay applies `allowed_remote_resources` (appended to the
top-level list), `allow_runtime_fetch` (turned on) and `max_runtime_fetches`
(replaced, so raised or lowered within ADR 0024's range). Matching overlays
apply in file order, so the last cap set wins. They are guarded (ADR 0112
Decision 2) and overlay-only: a `forge:` block cannot set them.

Every prefix an overlay adds must pass the same prefix test against the
config-level list, and no overlay may set `allow_runtime_fetch: false` or an
empty `allowed_remote_resources` (it would turn nothing off); either fails the
load, naming the overlay's index. Both checks run on every overlay entry,
matching or not, with or without URL references, so that `lint`, and `lock`
when it composes the harness, check without an event what any run could apply.
When a matching overlay sets `allow_runtime_fetch`, every prefix of the
composed harness's merged list, top level included, must also pass on the same
terms, or the load fails. The config-level list is the one each command
resolves; an explicit empty list admits no overlay prefix. A base's overlays
resolve inside the base, so their fetch fields never reach the child, yet their
entries get the per-entry checks when the base loads.

## Forge scope

Forge-neutral: the bound is the same on GitHub, GitLab and Forgejo. The
runtime fetch service fetches only GitHub-hosted URLs today, on any forge.

## Consequences

- A harness with no top-level list can turn on runtime fetch for one
  context, such as one forge, without turning it on everywhere.
- A harness whose overlay grants a prefix outside the config-level list, inert
  before, now fails to load in `run`, `lint`, `lock` and dispatch, even for
  another forge's overlay or a base's.
- An overlay's merged-list or cross-field failure shows only where it matches:
  everywhere if keyed on `config`; in a run, and in `lint` or `lock` given the
  forge, if on `runtime.forge`; otherwise only in a run.
- Unless a matching overlay sets `allow_runtime_fetch`, a top-level list keeps
  today's checks (`run`, `lint`, `lock`, with URL references): separate work.
