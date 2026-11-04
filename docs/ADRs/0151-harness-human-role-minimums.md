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

This decision applies only to CEL/entity routes and migrated v2 model-bound
admission. Legacy non-CEL, non-entity workflows remain under Authorization v1
and their existing workflow guards.

| Earlier ADR | Effect of this decision |
| --- | --- |
| [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) | Apply the harness minimum before CEL on migrated routes. |
| [ADR 0098](0098-entity-first-harness-evaluation.md) | Apply it to decisive actor-attributed entity facts. |
| [ADR 0133](0133-actor-attributed-data-security-boundary.md) and [ADR 0141](0141-deterministic-filtered-entity-context-staging.md) | Apply it at model-record admission and exposure gates. |
| [ADR 0061](0061-harness-cel-dispatch.md) | Resolve the minimum before the harness actor gate and CEL. |
| [ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md) | A human label grant meets `triage` only; bots remain separate. |

## Context

[ADR 0061](0061-harness-cel-dispatch.md) puts platform authorization before
harness CEL. [Authorization v1](../normative/authorization/v1/README.md)
sets different human thresholds for observation and mutation routes, while a
harness's `role` selects its run credentials. Credential scope does not decide
which people may trigger a harness or contribute actor-authored data to it.
A single `triage` threshold could let lower-privilege actors influence a
write-capable harness. A per-harness minimum addresses that gap; its trusted
value must be resolved before it gates admission. [ADR 0112](0112-overlays-may-set-any-harness-field.md)
allows trusted-fact-guarded overlays to set such fields.

## Options

Keep one `triage` threshold for every harness and rely on CEL for stricter
routing; derive a threshold from token permissions; or use an explicit default
mapping from mint role with a configurable, platform-enforced harness minimum.

## Decision

1. Fullsend enforces a non-waivable `triage` floor for ordinary human actors.
   Bots use their separate registered-role and label-handoff rules.
2. Each harness has an effective `human_min_role`. It defaults to `triage` for
   `role: triage` or `role: review`, and to `write` for every other role,
   including custom roles. An explicit value may be `triage`, `write`,
   `maintain`, or `admin`; an invalid value makes the harness ineligible.
3. Trusted top-level values and guarded overlays may set the minimum. An
   overlay may preserve or lower its layer's non-overlay baseline, never raise
   it. A missing optional guard keeps the baseline; unverifiable required
   evidence or an evaluation error fails closed. CEL and deprecated `forge:`
   blocks cannot set or relax the minimum.
4. The same effective minimum gates the human prompting event, decisive
   actor-originated entity facts, and Fullsend-controlled model-bound records.
   CEL may narrow admission further but cannot relax these gates.
5. A verified human label addition grants `triage` only for its exact accepted
   transition and proven projection. It does not create a verified role; a
   stronger harness minimum requires the labeler's verified effective role.

[Authorization Contract v2](../normative/authorization/v2/README.md) defines
the policy sources, overlay-resolution and provenance rules, per-record gates,
auditing, and run-time rechecks. [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
defines the bound-run lifecycle. These v2 rules do not extend to legacy
non-CEL, non-entity workflows.

## Forge scope

GitHub, GitLab, and Jira use their verified normalized-role mappings. Jira
Developer and Administrator satisfy `triage` because Jira has no separate
triage level. Forgejo must provide a verified effective role or deny the
role-based path. Bot and label grants require separate provider evidence.

## Consequences

- The `human_min_role` field is target behavior until the loader validates and
  enforces it. Existing legacy routes keep their Authorization v1 guards.
- A harness author may deliberately set `human_min_role: triage` for a
  write-capable role; that permits `triage` actors and records to guide that
  harness without changing its minted capabilities.
- Each harness applies its own minimum to shared verified role evidence;
  authorization decisions and filtered views are not shared across minima.
- [Entity-context-snapshot v1](../normative/entity-context-snapshot/v1/README.md)
  retains its observation/mutation-derived `triage`/`write` threshold. A
  different snapshot threshold requires a versioned contract.
