---
title: "128. preflight_check is a literal host command, not a script resource"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - security
  - validation
---

# 128. preflight_check is a literal host command, not a script resource

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

`validation_loop.preflight_check` was added by [#5192](https://github.com/fullsend-ai/fullsend/pull/5192) to run a host-dependency probe (e.g. `python3 -c "import jsonschema"`) before sandbox creation, so a missing dependency fails fast rather than after the agent completes ([#5074](https://github.com/fullsend-ai/fullsend/issues/5074)). It is executed as a literal `sh -c` command with no working directory set (in the preflight execution block in `internal/cli/run.go`), and it is deliberately not resolved through the resource-fetch pipeline that serves `pre_script` and `post_script`.

That distinction was never documented. On 2026-09-21 a harness set `preflight_check: "scripts/common-preflight.sh"`, expecting the same script-path semantics as `pre_script`; fullsend executed the string as a command, the relative path resolved against the wrong directory, and at least nine runs failed before revert ([#7600](https://github.com/fullsend-ai/fullsend/issues/7600)). The fix under review ([agents#1418](https://github.com/fullsend-ai/agents/pull/1418)) inlines the probe as a self-contained command, which is correct for the current semantics.

This ADR documents the semantics and specifies a future machine check.

## Options

- **Treat `preflight_check` as a fetched script path.** Rejected: it would need a second resolution path distinct from `pre_script`, and the field has shipped as a command since #5192; changing it now is a breaking semantic change with no migration benefit.

- **Split into `command` and `script` subfields.** Rejected: YAGNI for a single host-side probe; a resource-resolved script variant is out of scope here and can be proposed separately.

## Decision

1. **`preflight_check` is a literal command, not a resource path.** It is executed via `sh -c` without setting a working directory and is not resource-resolved. The existing nested check expands `${VAR}` references from the permitted host environment before shell parsing; the top-level check should retain command semantics when implemented. Authors must not interpolate untrusted values or credentials, even inside shell quotes: the current failure and timeout diagnostics include the expanded command. Before releasing the top-level field, use the same explicitly allowlisted, minimal host environment for both `${VAR}` expansion and the `sh -c` process; reject references to variables outside that allowlist *before* expansion rather than consulting the runner's environment. Exclude forge, mint, and provider tokens by default. Any necessary credential must be individually justified and scoped to that probe. Redact sensitive command/output diagnostics and test rejected `${GH_TOKEN}` and other excluded references, child-environment token exclusion, and redaction on failure and timeout.

2. **Advise on bare path-like values at load.** Extend `Harness.Lint()` (per [ADR 0127](0127-harness-schema-versioning-and-field-types.md)) to flag `preflight_check` values matching `^[./]?[\w./-]+\.(sh|py|rb|js)$` at SeverityError. This is a non-fatal authoring diagnostic, not a load or execution prohibition. Once implemented, harness publication CI must treat this diagnostic as a blocking error before publishing a harness that uses the field; `fullsend lock`/`run` do not currently fail on SeverityError. The regex is not a security or sanitization control: it misses a `.bash` extension, `sh scripts/foo.sh`, and script names with trailing arguments, and does **not** constrain shell metacharacters in a value passed to `sh -c` after expansion.

3. **Resource-resolved preflight is future work.** A script-based variant (e.g. `preflight_script`) is explicitly out of scope; if pursued, it must follow `pre_script` delivery semantics under [ADR 0038](0038-universal-harness-access.md).

## Consequences

- The semantic type of `preflight_check` is now documented. The planned path-pattern Lint rule in Decision 2 can prevent publication of the 2026-09-21 bare-path mistake only when harness publication CI blocks on it; it remains non-fatal in `fullsend lock`/`run`. Until then a bare path-like value is attempted at runtime by `sh -c`, where it may fail if the file is absent from the host working directory.
- Authors should prefer self-contained dependency probes. An intentional `sh scripts/a.sh` command is allowed and does not match the bare-path heuristic: it runs if the script exists relative to the host process's working directory, but the file is not fetched or delivered with the harness. If that directory is an untrusted checkout, this command can execute checkout-controlled code on the host; only use such a command when the referenced file and harness author are trusted. The `Harness.Lint()` path-pattern flag is not yet implemented; when it lands, `fullsend lock` and `run` print and continue rather than refusing to load or execute. Making SeverityError fail-fast is a separate policy decision.
- agents#1418 (inline `python3 -c "import jsonschema"`) is the correct authoring pattern and needs no change.
- Backward compatible: existing inline commands are unaffected.
- Follow-ups out of scope here: extending coverage to `pre_script`/`post_script` ([ADR 0129](0129-extend-preflight-coverage-to-pre-and-post-scripts.md)), a separately named resource-resolved `preflight_script` field for complex probes, and deciding separately whether SeverityError diagnostics should ever be fail-fast in `fullsend lock`/`run`.
