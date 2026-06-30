---
title: "55. Unified environment variable delivery for harness runner and sandbox"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - configuration
  - environment
---

# 55. Unified environment variable delivery for harness runner and sandbox

Date: 2026-06-23

Amends: [ADR 0024](0024-harness-definitions.md), [ADR 0049](0049-agent-configuration-env-var-convention.md)

## Status

Accepted

## Context

Setting an environment variable that needs to reach both the runner (pre/post
scripts) and the sandbox (agent inference) requires specifying it in two
independent mechanisms with different formats:

1. `runner_env:` in the harness YAML — a key-value map for host-side scripts.
2. A `.env` file under `env/` — shell `export` syntax, delivered via
   `host_files` with `expand: true`.

ADR 0049 acknowledges this explicitly: "A config var needed by both must
appear in both places."

The `.env` file is especially painful to customize. It contains all
passthrough context vars (`GITHUB_PR_URL`, `GH_TOKEN`, `PR_NUMBER`, etc.).
Adding a single custom var like `REVIEW_FINDING_SEVERITY_THRESHOLD` forces
forking the entire file and maintaining all those passthroughs — see
[fullsend-ai/.fullsend#84](https://github.com/fullsend-ai/.fullsend/pull/84).

This separation was not an intentional design choice. It fell out of the
original `fullsend run` implementation (PR #231), which solved two different
runtime problems at different execution points and was later codified into
ADR 0024 without anyone asking whether a user should have to specify the same
var in two places.

## Decision

Add a new `env:` top-level field to the harness schema with `runner` and
`sandbox` sub-maps. Deprecate `runner_env` in favor of `env.runner`.

`host_files` env delivery (`.env` files with `expand: true`) remains
permanently supported alongside `env.sandbox`. The two mechanisms are
complementary: `env.sandbox` is convenient for simple per-harness vars,
while `host_files` provides file-level composability that `env.sandbox`
cannot match (e.g. one `.env` file per tool, mix-and-matched across
harnesses without duplication).

### Schema

```yaml
env:
  runner:
    FULLSEND_OUTPUT_SCHEMA: "${FULLSEND_DIR}/schemas/review-result.schema.json"
  sandbox:
    GITHUB_PR_URL: "${GITHUB_PR_URL}"
    GH_TOKEN: "${GH_TOKEN}"
    REVIEW_FINDING_SEVERITY_THRESHOLD: "medium"
```

- `env.runner` — key-value pairs set in the host process environment for
  pre/post scripts and the validation loop. Replaces `runner_env`.
- `env.sandbox` — key-value pairs the runner writes into a generated `.env`
  file and copies into the sandbox at bootstrap. Complements (does not
  replace) `.env` files delivered via `host_files`.
- Values in both sub-maps support `${VAR}` expansion from the host
  environment, same as `runner_env` and `expand: true` host_files today.

The `env:` field can appear at the top level and inside `forge.<platform>`
blocks, replacing `runner_env` at both levels
([ADR 0045](0045-forge-portable-harness-schema.md)).

Go struct:

```go
type EnvConfig struct {
    Runner  map[string]string `yaml:"runner,omitempty"`
    Sandbox map[string]string `yaml:"sandbox,omitempty"`
}
```

Added to both `Harness` and `ForgeConfig`:

```go
Env *EnvConfig `yaml:"env,omitempty"`
```

### Merge semantics

`env:` follows the same per-variable additive merge rules established by
ADR 0045 for `runner_env`:

- **`base:` composition** — parent map merged with child map; child keys win
  on collision. Each sub-map (`runner`, `sandbox`) merges independently. A
  child that declares only one sub-map inherits the other from the parent.
- **`forge.<platform>` resolution** — identical rules. Forge sub-maps merge
  with top-level sub-maps; forge keys win.

**Limitation:** merge is strictly additive — there is no mechanism for a
child to remove a key inherited from its base. A child that inherits
`GITHUB_ISSUE_URL` from a base cannot suppress it; it can only override
the value. If removal semantics are needed in the future, a YAML `null`
/ `~` sentinel could be added.

### Runner behavior

When `env.sandbox` is present (after all merges), the runner:

1. Expands `${VAR}` references from the host environment using Go's
   `os.Expand`, which supports `$VAR` and `${VAR}` syntax only — no
   default values, substring operations, or other shell parameter
   expansion features.
2. Writes the result as `KEY=value` lines to a generated `.env` file inside
   the sandbox (e.g. `/sandbox/workspace/.env.d/generated.env`).
3. The sandbox's `envfile.Load` picks it up normally.

`env.runner` sets key-value pairs in the host process environment before
executing pre/post scripts and the validation loop — identical to current
`runner_env` behavior.

### Precedence

When both `env.sandbox` and `host_files` `.env` entries define the same
key, `env.sandbox` takes precedence. This is enforced by bootstrap
ordering: `.env.d/` files are sourced first, then `env.sandbox` exports
are emitted, so `env.sandbox` wins on collision. This matches the
expected use case: a harness inherits a shared `.env` file via
`host_files` and overrides a single var with `env.sandbox`.

### Deprecation

`runner_env` **always** emits a deprecation warning when present, regardless
of whether `env:` also exists:

- When `env:` is also present: `env.runner` wins; warning says so.
- When `env:` is absent: `runner_env` still works; warning says
  "migrate to env.runner."
- Same rules apply to `forge.<platform>.runner_env`.

`host_files` env delivery is **not deprecated**. It provides file-level
composability (one `.env` file per tool, mixed across harnesses) that
`env.sandbox` cannot structurally replicate. The two mechanisms coexist
permanently.

### Migration phases

**Phase 1 — Schema extension (this ADR):** Add `env:` to `Harness` and
`ForgeConfig`. `runner_env` emits deprecation warnings whenever present. When
both exist, `env.runner` wins. Runner generates `.env` from `env.sandbox`.

**Phase 2 — Migrate scaffold harnesses:** Update all scaffold harnesses to
use `env:` instead of `runner_env`. Move simple passthrough vars from manual
`.env` files into `env.sandbox` where appropriate. Harnesses that use
modular per-tool `.env` files via `host_files` keep them.

**Phase 3 — Remove `runner_env`:** Remove `runner_env` from the Go structs.
`yaml.Unmarshal` silently ignores it in old files. `Lint()` emits an error
for harnesses that still reference it.

## Consequences

- Adding a config var that both runner and sandbox need is a change to one
  file (the harness YAML), not a fork of an entire `.env` file.
- `base:` composition works naturally — adding one config knob to a
  customized harness is a few lines, not a full env file fork.
- No runner changes are needed for Phase 1 beyond generating the `.env` file
  from `env.sandbox` and emitting deprecation warnings for `runner_env`.
- Existing harnesses continue to work unchanged; they just get noisier about
  `runner_env` deprecation.
- ADR 0049's env var naming convention applies unchanged — the delivery
  mechanism changes but the `{AGENT}_{SETTING_NAME}` convention does not.
- Modular `.env` files via `host_files` remain the right choice for
  per-tool env groups shared across multiple harnesses.
- This change extends the harness schema; runners older than Phase 1 will
  silently ignore `env:` and fall back to `runner_env` / `host_files` only.
  Harness schema versioning ([#235](https://github.com/fullsend-ai/fullsend/issues/235))
  would make this evolution explicit.
- Env merge is strictly additive. A child cannot remove a key inherited from
  its base — it can only override the value.
