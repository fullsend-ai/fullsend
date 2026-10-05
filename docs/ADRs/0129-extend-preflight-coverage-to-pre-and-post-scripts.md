---
title: "129. Extend preflight coverage to pre_script and post_script"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - runtime
  - validation
---

# 129. Extend preflight coverage to pre_script and post_script

Date: 2026-09-24

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

`validation_loop.preflight_check` runs a host-dependency probe only for the validation loop ([ADR 0128](0128-preflight-check-literal-command.md)). The original issue ([#5074](https://github.com/fullsend-ai/fullsend/issues/5074)) and its follow-up ([#5568](https://github.com/fullsend-ai/fullsend/issues/5568)) both noted that `pre_script` and `post_script` have the same host-dependency exposure. The cost differs between them: `pre_script` runs on the host *before* sandbox creation (in the pre-script execution block in `internal/cli/run.go`), so a missing dependency fails fast there; `post_script` runs after teardown, so its missing dependency is the higher-cost case this ADR closes. PR [#6009](https://github.com/fullsend-ai/fullsend/pull/6009) proposed a top-level `preflight_check` field covering all scripts, but it predates the schema-versioning work ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)) and the 2026-09-21 semantics incident ([ADR 0128](0128-preflight-check-literal-command.md)) and is now stale and conflicting.

Extending preflight to `pre_script` and `post_script` closes that gap, and this decision builds on ADR 0127 and ADR 0128 for the field's type and semantics.

## Options

- **Per-script sibling fields (`pre_script.preflight_check`, etc.).** Rejected: `pre_script` and `post_script` are flat strings, not structs, so siblings would force a breaking refactor of those fields into objects.

- **Resource-resolved script field.** Deferred: a single `sh -c` command can combine dependency probes, but more involved checks may need a separately named `preflight_script`. Such a field would have to be delivered like `pre_script` under [ADR 0038](0038-universal-harness-access.md), with an explicit host execution and working-directory contract; it must not reinterpret the existing literal-command field ([ADR 0128](0128-preflight-check-literal-command.md)).

## Decision

Introduce a top-level `preflight_check` field on the harness that runs once, before sandbox creation, as a single host-dependency gate for `pre_script`, `post_script`, and `validation_loop`.

1. **Top-level field, not per-script siblings.** A single top-level field covers all scripts without a breaking refactor of the flat `pre_script`/`post_script` strings.

2. **Literal-command semantics.** Consistent with ADR 0128, the top-level field is a literal `sh -c` command (not a script path), subject to the same `Harness.Lint()` path-pattern guard. Its implementation must also enforce ADR 0128's restricted credential environment and diagnostic-redaction release gates.

3. **Execution order and migration.** The top-level check runs before `pre_script` ([ADR 0072](0072-pre-script-output-protocol.md)) and before sandbox creation. A nonzero exit or timeout reports the failed preflight check and stops the run before `pre_script`, sandbox creation, or any nested check. This eager gate also checks post-script dependencies on runs that `pre_script` would otherwise skip; authors must account for that trade-off. Implementation tests must verify ordering, failure, and skip paths with sentinel actions. Deprecate `validation_loop.preflight_check` in favor of the top-level field once that replacement is implemented. During migration, existing nested checks continue to run after the top-level check (if present), so harnesses are not broken before they can move their probe. Before releasing the top-level field, when both checks are configured, enforce [ADR 0128](0128-preflight-check-literal-command.md)'s shared expansion/process allowlist, rejection of excluded `${VAR}` references before expansion, and redaction of command/output diagnostics for **both** checks. Test excluded-token references and failure/timeout diagnostics on both paths. Nested-only harnesses retain their existing behavior until a separately planned compatibility migration; their current credential exposure is not fixed by the new top-level gate. Emit a deprecation warning only once the replacement is usable; removal requires a separately decided compatibility/version gate ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)).

4. **Composition.** The field follows the scalar merge rule from [ADR 0045](0045-forge-portable-harness-schema.md): a child harness overrides the inherited value. An overriding child check must include the dependencies of any inherited `pre_script`, `post_script`, and validation script still present in the composed harness; it does not append to the base check. Overlay-selected scripts can also replace those scripts while the check remains top-level: authors must cover the union of dependencies across possible matching overlays, even though an inactive overlay's probe can fail an otherwise eligible run. Implementation tests must cover a child override with an inherited script dependency and an overlay-selected script dependency.

5. **Consumer rollout.** Publishing or advancing a harness pin that relies on the top-level check requires verification that every pinned fullsend consumer has a version that executes the field; an older loader silently ignores the added YAML key even when `schema_version` remains `1` ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)). Release the supporting CLI first, exercise a run that proves the check precedes `pre_script` and sandbox creation on each consumer, and retain nested checks until that verification and migration finish. If a consumer fails verification, keep or restore its previous harness pin; do not publish a harness whose safety depends on an ignored check.

## Consequences

- One host-dependency gate covers all scripts; failures are caught before sandbox creation rather than after the agent completes.
- Backward compatible: harnesses without a top-level field behave exactly as today, including execution of the nested check during migration.
- This decision supersedes the design in PR #6009; the PR's disposition is left to normal triage.
- This decision resolves the design question in #5568 (top-level field vs. per-script siblings); implementation is tracked separately.
- Follow-ups out of scope here: a separately named, resource-delivered `preflight_script` for more complex probes (see ADR 0128), a deprecation-warning/removal timeline, and choosing a concrete minimum CLI release after the top-level field ships.
