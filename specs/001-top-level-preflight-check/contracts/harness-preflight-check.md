# Contract: Top-Level `preflight_check` Harness Field

**Feature**: [spec.md](../spec.md) | **Branch**: `001-top-level-preflight-check` | **Date**: 2026-10-26

The harness YAML is a cross-repository API boundary (ADR 0127): fullsend owns
the interpretation of each field. This contract defines the top-level
`preflight_check` field as consumed by the fullsend runner. Design authority:
ADR 0128 (literal command semantics), ADR 0129 (coverage and migration).
Field classification and semantic-type tables of record:
[Harness Field Reference](../../../docs/contributing/harness-fields.md).

## 1. Field declaration

```yaml
# harness/<agent>.yaml
agent: agents/code.md
preflight_check: 'python3 -c "import jsonschema"'   # optional; literal sh -c command
pre_script: scripts/pre.sh                            # gated by the check above
validation_loop:
  script: scripts/validate.sh
  # preflight_check: ...                              # nested field: deprecated, still runs after the top-level check
```

| Property | Value |
|---|---|
| Key | `preflight_check` |
| Location | Harness top level **only** |
| Type | string (optional); absent/empty/whitespace-only ⇒ unset |
| Semantic type | inline command (executed via `sh -c` on the host; never resource-resolved, never fetched) |
| Schema version | `1` (backward-compatible addition; older loaders silently ignore the key — ADR 0127) |
| Conditional settable | No — a `preflight_check` key inside `forge:` or `overlays:` is ignored, never honored |

## 2. Execution contract

1. **Exactly once per run**, before `pre_script` (when present) and before
   sandbox creation.
2. Executed as a literal `sh -c` command with **no working directory set**;
   relative paths resolve against the runner's working directory. The value is
   never resolved through the harness resource-fetch pipeline.
3. When both the top-level and nested checks are configured, the top-level runs
   first; a failing top-level check prevents the nested check from running.
4. Execution bound: 30 seconds (shared with the nested check). Exceeding it is
   equivalent to failure.
5. The check runs before any pre-script skip decision: a `pre_script` that would
   skip the run does not skip the check (eager gate; ADR 0129 trade-off).

## 3. Environment contract (credential safety — release gate #8721)

The command's `${VAR}` references validate against, and its child process runs
with, exactly:

- the host `PATH`, `HOME`, and `TMPDIR` (when set in the host process);
- the runner-forced `FULLSEND_DIR` and `PYTHONDONTWRITEBYTECODE=1`;
- non-credential variables declared via `env.runner` (or deprecated
  `runner_env`).

Nothing else is inherited. Credential names — forge, mint, and provider tokens,
runner secrets, and OIDC/trigger names — are excluded **even when declared**.

## 4. Reference validation contract

- Plain `${VAR}` brace references are validated **before any shell executes**:
  a reference to a name outside the environment in §3 fails the run pre-execution.
  The error names the variable (never a value) and points to the runner-env
  declaration channel.
- Bare `$VAR` (no braces) is left to the child shell, where unset means empty.
- Non-plain brace forms (`${VAR:-default}`) are not validated and expand per
  shell semantics.
- References to runner-secret names are refused at harness validation (ADR 0136).

## 5. Failure and diagnostic contract

- Nonzero exit or timeout stops the run before `pre_script`, before sandbox
  creation, and before the nested check, reporting which check failed with
  field-qualified command text and output.
- Failure/timeout diagnostics carry only **redacted** command text and output:
  declared runner-env secret values never appear (ADR 0136 redaction).
- Error identity: the failing field is named — `preflight_check` for the
  top-level gate, `validation_loop.preflight_check` for the nested gate.

## 6. Composition contract (ADR 0045 / ADR 0129 Decision 4)

- `base:` composition merges the field as a **scalar**: a child's non-empty
  value replaces the base's; an absent child value inherits the base's; values
  never append or combine.
- The overriding author is responsible for the child command covering the
  dependencies of any inherited `pre_script`, `post_script`, and validation
  script.
- Overlays that select different stage scripts do not move or replace the gate:
  the single top-level check must cover the union of dependencies across
  possible matching overlays.

## 7. Lint contract (FR-007)

| Condition | Diagnostic |
|---|---|
| Top-level value matches a bare script-path pattern (`^[./]?[\w./-]+\.(sh|py|rb|js)$`) | Warning: value looks like a script path; the check runs verbatim as `sh -c` and is never fetched — use `pre_script` for a fetched script or inline the command |
| `validation_loop.preflight_check` is set (any harness, once this feature ships) | Warning (deprecation): names the top-level `preflight_check` replacement |

Both are non-fatal authoring diagnostics; `fullsend lock`/`run` print and
continue. Harness publication CI may treat them as blocking before publishing.

## 8. Migration contract (FR-008)

- The nested `validation_loop.preflight_check` continues to run during
  migration — after the top-level check when both are configured — and
  harnesses setting only the nested field retain their existing behavior (plus
  the deprecation warning).
- Nested-only harnesses' existing credential exposure is not changed by this
  feature; the top-level gate does not retrofit them.
- Removal of the nested field is out of scope (separately decided
  compatibility/version gate per ADR 0127).
- Consumers: an older pinned fullsend CLI silently ignores the added key.
  Publishing a harness whose safety depends on the check requires consumer pin
  verification first (fullsend-ai/agents#1753, out of scope here).

## 9. Backward compatibility

Harnesses that do not set the top-level field behave exactly as before this
feature, in 100% of scenarios (SC-002): no new subprocess, no new validation,
no changed ordering.
