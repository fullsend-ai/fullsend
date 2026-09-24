---
title: "117. Extend preflight coverage to pre_script and post_script"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - runtime
  - validation
---

# 117. Extend preflight coverage to pre_script and post_script

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

`validation_loop.preflight_check` runs a host-dependency probe only for the validation loop ([ADR 0116](0116-preflight-check-literal-command.md)). The original issue ([#5074](https://github.com/fullsend-ai/fullsend/issues/5074)) and its follow-up ([#5568](https://github.com/fullsend-ai/fullsend/issues/5568)) both noted that `pre_script` and `post_script` have the same host-dependency exposure. The cost differs between them: `pre_script` runs on the host *before* sandbox creation (`internal/cli/run.go:1613-1616`), so a missing dependency fails fast there; `post_script` runs after teardown, so its missing dependency is the higher-cost case this ADR closes. PR [#6009](https://github.com/fullsend-ai/fullsend/pull/6009) proposed a top-level `preflight_check` field covering all scripts, but it predates the schema-versioning work ([ADR 0115](0115-harness-schema-versioning-and-field-types.md)) and the 2026-09-21 semantics incident ([ADR 0116](0116-preflight-check-literal-command.md)) and is now stale and conflicting.

Extending preflight to `pre_script` and `post_script` closes that gap, and this decision builds on ADR 0115 and ADR 0116 for the field's type and semantics.

## Options

- **Per-script sibling fields (`pre_script.preflight_check`, etc.).** Rejected: `pre_script` and `post_script` are flat strings, not structs, so siblings would force a breaking refactor of those fields into objects.

- **Resource-resolved script field.** Rejected here: consistent with ADR 0116, a script variant is future work and follows `pre_script` delivery semantics under [ADR 0038](0038-universal-harness-access.md).

## Decision

Introduce a top-level `preflight_check` field on the harness that runs once, before sandbox creation, as a single host-dependency gate for `pre_script`, `post_script`, and `validation_loop`.

1. **Top-level field, not per-script siblings.** A single top-level field covers all scripts without a breaking refactor of the flat `pre_script`/`post_script` strings.

2. **Literal-command semantics.** Consistent with ADR 0116, the top-level field is a literal `sh -c` command (not a script path), subject to the same `Harness.Lint()` path-pattern guard.

3. **Execution order and precedence.** The top-level check runs before `pre_script` ([ADR 0072](0072-pre-script-output-protocol.md)) and before sandbox creation; `validation_loop.preflight_check` continues to run after it, unchanged.

4. **Composition.** The field follows the scalar merge rule from [ADR 0045](0045-forge-portable-harness-schema.md): a child harness overrides the inherited value.

## Consequences

- One host-dependency gate covers all scripts; failures are caught before sandbox creation rather than after the agent completes.
- Backward compatible: harnesses without a top-level field behave exactly as today.
- This decision supersedes the design in PR #6009; the PR's disposition is left to normal triage.
- This decision resolves the design question in #5568 (top-level field vs. per-script siblings); implementation is tracked separately.
- Follow-ups out of scope here: a resource-resolved script variant (see ADR 0116), and the rollout/version gate for consumers on pinned fullsend versions.
