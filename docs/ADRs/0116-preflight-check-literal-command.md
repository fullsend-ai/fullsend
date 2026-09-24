---
title: "116. preflight_check is a literal host command, not a script resource"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - security
  - validation
---

# 116. preflight_check is a literal host command, not a script resource

Date: 2026-09-24

## Status

Accepted

Extends [ADR 0038](0038-universal-harness-access.md): adds `preflight_check` to the set of harness fields with explicit execution semantics.

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

`validation_loop.preflight_check` was added by [#5192](https://github.com/fullsend-ai/fullsend/pull/5192) to run a host-dependency probe (e.g. `python3 -c "import jsonschema"`) before sandbox creation, so a missing dependency fails fast rather than after the agent completes ([#5074](https://github.com/fullsend-ai/fullsend/issues/5074)). It is executed as a literal `sh -c` command with no working directory set (`internal/cli/run.go:1279`), and it is deliberately not resolved through the resource-fetch pipeline that serves `pre_script` and `post_script`.

That distinction was never documented. On 2026-09-21 a harness set `preflight_check: "scripts/common-preflight.sh"`, expecting the same script-path semantics as `pre_script`; fullsend executed the string as a command, the relative path resolved against the wrong directory, and at least nine runs failed before revert ([#7600](https://github.com/fullsend-ai/fullsend/issues/7600)). The fix under review ([agents#1418](https://github.com/fullsend-ai/agents/pull/1418)) inlines the probe as a self-contained command, which is correct for the current semantics.

This ADR makes the semantics explicit and machine-checked.

## Options

- **Treat `preflight_check` as a fetched script path.** Rejected: it would need a second resolution path distinct from `pre_script`, and the field has shipped as a command since #5192; changing it now is a breaking semantic change with no migration benefit.

- **Split into `command` and `script` subfields.** Rejected: YAGNI for a single host-side probe; a resource-resolved script variant is out of scope here and can be proposed separately.

## Decision

1. **`preflight_check` is a literal command.** It is executed via `sh -c` with no working directory, is not a script path, and is not resource-resolved.

2. **Flag path-like values at load.** Extend `Harness.Lint()` (per [ADR 0115](0115-harness-schema-versioning-and-field-types.md)) to flag `preflight_check` values matching `^[./]?[\w./-]+\.(sh|py|rb|js)$` at SeverityError, guiding authors toward a self-contained inline command. This regex is an authoring-mistake heuristic, not a security or sanitization control — it has known gaps (it misses a `.bash` extension, `sh scripts/foo.sh`, or a script name with trailing arguments) and does **not** constrain shell metacharacters in a value that is executed verbatim via `sh -c`.

3. **Resource-resolved preflight is future work.** A script-based variant (e.g. `preflight_script`) is explicitly out of scope; if pursued, it must follow `pre_script` delivery semantics under [ADR 0038](0038-universal-harness-access.md).

## Consequences

- The semantic type of `preflight_check` is now documented. The path-pattern Lint rule in Decision 2 will flag the 2026-09-21 failure mode at load time once it lands; until then a path-like value fails only at runtime when `sh -c` runs it.
- Authors must inline self-contained dependency probes. The `Harness.Lint()` path-pattern flag is not yet implemented; when it lands, `fullsend lock` and `run` emit a non-fatal diagnostic (print and continue), and making SeverityError fail-fast in `lock`/`run` is a tracked follow-up.
- agents#1418 (inline `python3 -c "import jsonschema"`) is the correct authoring pattern and needs no change.
- Backward compatible: existing inline commands are unaffected.
- Follow-ups out of scope here: extending coverage to `pre_script`/`post_script` ([ADR 0117](0117-extend-preflight-coverage-to-pre-and-post-scripts.md)), a resource-resolved `preflight_script` field, and making the SeverityError diagnostic fail-fast in `fullsend lock`/`run`.
