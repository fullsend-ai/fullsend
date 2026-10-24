---
title: "151. Use harness human-role minimums for actor admission"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - cel
  - data-security
---

# 151. Use harness human-role minimums for actor admission

Date: 2026-10-23

## Status

Accepted

This decision partially supersedes the human permission thresholds in
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md),
[ADR 0098](0098-entity-first-harness-evaluation.md),
[ADR 0133](0133-actor-attributed-data-security-boundary.md), and
[ADR 0141](0141-deterministic-filtered-entity-context-staging.md) for the
target CEL and model-bound paths. It also refines [ADR
0061](0061-harness-cel-dispatch.md)'s authorization-before-harness-
enumeration ordering for harness-specific thresholds and narrows the human
label-transition exception in
[ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md) for a
harness requiring more than `triage`.
Their other provenance, bot, freshness, and filtering requirements remain.

## Context

[ADR 0061](0061-harness-cel-dispatch.md) puts platform authorization before
harness CEL. A harness's `role` selects its run identity: GitHub requests a
mint token for it, while GitLab selects registered role credentials
([`run.go` at `fe68925b`](https://github.com/fullsend-ai/fullsend/blob/fe68925b53066c27ba0c0272e2e8eb1ce73ebe40/internal/cli/run.go#L903-L915)).
[Authorization v1](../normative/authorization/v1/README.md#default-thresholds)
already sets `triage` for observation and `write` for mutation, including its
target entity-fact and model-record rules. The GitHub workflow applies those
thresholds to human slash commands; its other event routes have distinct
guards, including an ungated retro-close route
([`reusable-dispatch.yml` at `fe68925b`](https://github.com/fullsend-ai/fullsend/blob/fe68925b53066c27ba0c0272e2e8eb1ce73ebe40/.github/workflows/reusable-dispatch.yml#L305-L420)).
Since [#8727](https://github.com/fullsend-ai/fullsend/pull/8727), Go CEL
dispatch also applies an event-level `triage` floor for ordinary human events
and filters candidate harnesses by the v1 `role`-derived default before CEL:
`triage` and `review`
require `triage`; every other role requires `write`
([`auth.go` at `33dc5e1`](https://github.com/fullsend-ai/fullsend/blob/33dc5e1e27f2c732c49603ed93fb405fb728d160/internal/harnessdispatch/auth.go#L78-L157),
[`core.go` at `33dc5e1`](https://github.com/fullsend-ai/fullsend/blob/33dc5e1e27f2c732c49603ed93fb405fb728d160/internal/harnessdispatch/core.go#L68-L82)).
Its harness schema has no explicit human minimum
([`harness.go` at `33dc5e1`](https://github.com/fullsend-ai/fullsend/blob/33dc5e1e27f2c732c49603ed93fb405fb728d160/internal/harness/harness.go#L364-L400)),
and its label-added exception bypasses the per-harness role threshold
([`auth.go` at `33dc5e1`](https://github.com/fullsend-ai/fullsend/blob/33dc5e1e27f2c732c49603ed93fb405fb728d160/internal/harnessdispatch/auth.go#L53-L58)).
The current GitHub dispatch and run jobs each check out the PR base commit for
PR events ([`reusable-dispatch.yml` at `fe68925b`](https://github.com/fullsend-ai/fullsend/blob/fe68925b53066c27ba0c0272e2e8eb1ce73ebe40/.github/workflows/reusable-dispatch.yml#L1519-L1532),
[`run checkout` at `fe68925b`](https://github.com/fullsend-ai/fullsend/blob/fe68925b53066c27ba0c0272e2e8eb1ce73ebe40/.github/workflows/reusable-dispatch.yml#L1681-L1686)).
The matrix execution ref carries agent and role but no composed policy digest
([`ExecutionRef` at `fe68925b`](https://github.com/fullsend-ai/fullsend/blob/fe68925b53066c27ba0c0272e2e8eb1ce73ebe40/internal/harnessdispatch/ref.go#L3-L13)),
and `run` resolves the harness by name again
([`run.go` at `fe68925b`](https://github.com/fullsend-ai/fullsend/blob/fe68925b53066c27ba0c0272e2e8eb1ce73ebe40/internal/cli/run.go#L641-L674)).

A common `triage`-only admission rule would allow `triage`-authored model
records to guide a write-capable harness even if its trigger CEL required a
`write` prompting actor. A mint role can provide a compatibility default for
human authorization, but the minted token's permission set does not itself
define which human actors may prompt or supply data to the harness.
[ADR 0112](0112-overlays-may-set-any-harness-field.md) specifies that overlays
guarded by trusted facts may set security-relevant harness fields. Excluding
`human_min_role` would prevent the same reviewed harness from choosing a
minimum by source or other trusted metadata. The minimum must,
however, be selected before it controls actor and content admission.

## Options

Keep one `triage` threshold for every harness and rely on CEL for stricter
routing; derive a threshold from token permissions; or use an explicit default
mapping from mint role with a configurable, platform-enforced harness minimum.

## Decision

For ordinary human actors, Fullsend retains a non-waivable `triage`-level
authorization gate. The narrow verified label-transition grant below can
satisfy that gate only for the exact transition and its proven projection;
it does not establish a normalized human role. For each trusted harness,
Fullsend resolves `human_min_role` from an explicit top-level harness field
and any matching guarded overlays or, when no explicit value survives
resolution, from the harness's `role`: `triage` and `review` default to
`triage`; every other role, including custom roles, defaults to `write`.
An explicit value may be `triage`, `write`, `maintain`, or `admin`. It can
lower a `write` default to `triage` but cannot admit `read`, `none`, or
`external` actors. An invalid explicit value makes that harness ineligible;
it never falls back to the default. Validate explicit values in top-level,
base, and overlay entries, including overlays that do not match. Within each
base layer, matching overlays apply in order before that layer merges into the
child; an explicit child value overrides the inherited value, and the child's
matching overlays then apply in order. Resolve the final value before the
harness's actor gate or trigger CEL. A deprecated `forge:` block cannot set it.

An overlay setting `human_min_role` is a guarded overlay under ADR 0112:
its `when:` may depend only on that decision's closed set of trusted facts or
the versioned verified effective-role input, verified from the provider or
the pinned trusted configuration before harness-specific admission. It cannot
depend on the trigger CEL, actor-filtered entity facts, model records, or any
value whose availability depends on the minimum being selected. A role
comparison must use the versioned verified effective-role input, never raw
`event.actor.role` or the narrow label grant. The raw role field is excluded
for this purpose even though normalized-event v1 lists it as trusted; the
versioned CEL schema must define the verified role path and provenance before
role-based guards can be used. A known absent event or optional key is a
nonmatch under ADR 0112; an evaluation error or required guard evidence that
cannot be verified fails evaluation rather than silently selecting a fallback.
Trusted entity-source identity needed for a guard must be obtained from
provider-verified structural metadata or a platform discovery credential,
not from the still-unpinned harness run credential.
Trusted-field validation alone does not prove that a guard's matching branch
has the intended security meaning: for example, a change author may choose a
fork, while only an author with write access can make `is_fork` false. A
conditional relaxation should positively prove the trusted condition for
which the lower minimum is intended. The selected value gates other actors'
entity facts and model records as well as the prompting actor.

The default is an authorization policy, not a deduction from mint token scopes
or the harness's `privilege_levels`. The trusted composed harness revision,
ordered overlay outcome, trusted guard evidence, and effective minimum stay
fixed for one evaluation and run cycle; a changed policy or guard result
requires a new cycle or abort, not a silent threshold change.
The registry and local harness and `base` files that set this policy must come
from one immutable trusted checkout revision selected independently of the
prompting actor and entity content. A change proposal uses its target/base
commit, never its proposed head; other paths must identify an equivalent
trusted canonical revision. Every remote harness or base layer must pass its
allowlist and content-hash checks, and its digest belongs in the composed
policy identity. Dispatch must bind that identity, the ordered overlay
resolution and verified guard inputs, and the effective minimum to its run
handoff. A run or later Fullsend-controlled retrieval that cannot verify the
same pin must stop and may restart authorization from the first gate before
using the content.

Fullsend applies the same effective minimum separately to the ordinary human
prompting event before that harness's CEL predicate; to each actor-originated
entity fact needed for an actionable CEL result, including aggregates and
absence claims; and to each Fullsend-controlled model-bound source record at
ADR 0133's fresh exposure checkpoints. The record check uses its own current
revision actor, never the event sender or discovery credential. A fact below
the selected harness's minimum is excluded from its authorized CEL view; an
unresolved decisive fact leaves the result unknown rather than proving false
or empty. An event or model record below the minimum is denied or redacted.
Trusted Fullsend provenance can authorize eventless discovery but does not
authorize actor-originated entity facts or model records. CEL may narrow
selection beyond the platform gate, never relax it.

A verified human label addition supplies a narrow `triage`-equivalent grant
for that exact accepted transition and its proven projection. It neither
sets `actor.role` nor makes `actor.role_verified` true. If the selected
harness requires `write`, `maintain`, or `admin`, the human labeler must
also have a verified effective role meeting that minimum. The
provider-classified bot label handoff and registered-bot rules in ADR 0107
remain separate from human
thresholds; neither label grant admits unrelated history or model content.
The `human_min_role` field does not constrain bots; harness CEL may narrow
their routing.
The ungated GitHub retro-close route remains legacy compatibility, not a
general CEL-path exception.

Effective human roles come from the source provider or a trusted configured
permission source such as OWNERS. A failed forge-role lookup does not negate
an independently verified OWNERS grant for the same actor and target.
Fullsend exposes a verified effective role and its provenance to CEL when
present, and exposes an exact human label grant as separate provenance without
synthesizing a role. [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md)
may cache a bounded provider-verified entity-CEL role, but each harness
reapplies its own minimum; no harness-specific authorization decision or
filtered view is shared as a role verdict. Event and model-bound checks remain
fresh. The [Authorization Contract
v2](../normative/authorization/v2/README.md) specifies these gates and their
audit outcomes.

## Forge scope

The human minimum applies to GitHub, GitLab, and Jira through verified
normalized role mappings. On Jira, Developer and Administrator satisfy
`triage` because its mapping has no separate triage level. Forgejo is a
target, but its adapter must establish a verified effective role before
admitting a human actor through the role-based path; unsupported or
unverifiable mappings fail closed.
Source-specific bot and label grants require their own provider evidence.

## Consequences

- Current Go CEL dispatch enforces the v1 role-derived default for ordinary
  human events, and bash routes retain their stage guards. Explicit or guarded
  overlay `human_min_role` values, selected-minimum checks for human label
  additions and actor-originated entity and model records, and the pinned
  policy handoff remain target behavior. The field cannot become effective
  policy until the loader validates and enforces it.
- A harness author may deliberately set `human_min_role: triage` for a
  write-capable role; that permits `triage` actors and records to guide that
  harness without changing its minted capabilities.
- Authorized entity views, aggregate evidence, and audits are bound to the
  selected harness's minimum; verified actor roles may be reused, but pass
  decisions and filtered views cannot be reused across different minima.
- [Entity-context-snapshot v1](../normative/entity-context-snapshot/v1/README.md)
  retains its category-derived `triage`/`write` threshold. Snapshot admission
  with a differing override or a higher role requires a versioned contract
  before migration.
