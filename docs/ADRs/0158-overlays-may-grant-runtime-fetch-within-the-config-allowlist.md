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

**Keep the fetch fields out of overlays:** an exception to ADR 0112's "any
field", and a harness needing fetch for some runs composes a second one. **Let
a matching overlay set them, bounded by the config-level allowlist** (chosen):
the grant is per context, and the configuration still bounds it.

## Decision

We let a matching overlay grant runtime fetch, bounded by the config-level
allowlist, so that fetch can be turned on per context.

A matching overlay applies `allowed_remote_resources` (appended to the
top-level list), `allow_runtime_fetch` (turned on) and `max_runtime_fetches`
(replaced, within ADR 0024's range), and validation's cross-field rules apply
to the merged harness. They are guarded fields under ADR 0112 Decision 2, and
overlay fields only: a `forge:` block cannot set them.

Every prefix an overlay adds must pass the same prefix test against the
config-level list, or the harness fails to load, naming the overlay's index
and the entry. The check runs on every overlay entry, matching or not, and
whether or not the harness has URL references, so that `lint`, and `lock` when
it composes the harness, check without an event what any run could apply. The
config-level list is the one each command resolves; an explicit empty list
admits no overlay prefix.

A base's overlays resolve inside the base before the merge, so the fetch
fields they set do not reach the child. Their entries are still checked when
the base loads, since a base's overlays are validated like any harness's.

## Forge scope

Forge-neutral: the bound is the same on GitHub, GitLab and Forgejo. The
runtime fetch service fetches only GitHub-hosted URLs today, on any forge.

## Consequences

- A harness with no top-level list can turn on runtime fetch for one
  context, such as one forge, without turning it on everywhere.
- An overlay cannot turn fetch off or remove a prefix.
- A harness whose overlay grants a prefix outside the config-level list, inert
  before, now fails to load in `run`, `lint`, `lock` and dispatch, even when
  that overlay is for another forge's runs.
- This ADR bounds what an overlay adds, not a harness's top-level list, which
  is still checked only by `run`, `lint` and `lock` when the harness has URL
  references; closing that gap is separate work.
