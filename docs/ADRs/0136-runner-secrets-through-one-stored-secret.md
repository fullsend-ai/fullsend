---
title: "136. Pass user secrets to host-side scripts through one stored secret"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - secrets
  - github-actions
---

# 136. Pass user secrets to host-side scripts through one stored secret

Date: 2026-10-07

## Status

Accepted

## Context

A custom harness's `pre_script`, `post_script` and `validation_loop` can read
`${NAME}` through `env.runner` ([ADR 0055](0055-unified-env-var-delivery.md)),
but on GitHub a user secret such as a Jira or CodeRabbit token never reaches
`fullsend run`. The reusable workflow only receives the secrets it declares,
and the shim is a generated file that setup and `repos install` overwrite.
GitLab already works, because each CI/CD variable is its own masked secret in
the job environment (#7689).

## Options

1. **Caller-side list in the shim** (`format(...)` with `toJSON(secrets.X)`).
   Users would edit a generated file that setup and `repos install` overwrite.
2. **`secrets: inherit` with `toJSON(secrets)`.** Every repository secret
   would reach the runner, whether or not a harness uses it.
3. **One named secret per integration.** Every new integration would need a
   fullsend release before a user could adopt it.

## Decision

On GitHub, user secrets reach host-side scripts through one stored secret,
`FULLSEND_RUNNER_SECRETS`, a JSON object of name → value, filtered by the
resolved `env.runner`. The reusable workflow declares it as optional and
passes it to each stage's `fullsend run` step only. The shim forwards it with
one fixed line. `fullsend run` removes it from its environment before any
child starts, masks every value and registers it for redaction, then resolves
`${NAME}` in the resolved `env.runner` (after overlays) from the object. Keys
the harness does not reference are dropped.

Runner-owned names are refused: `oidcDenyKeys`, `providerOnlyKeys`, the
`FULLSEND_`, `GITHUB_`, `ACTIONS_`, `RUNNER_`, `CI_` and `LD_` families, the
minted role tokens and `PATH`. A passthrough value stays on the host: a
reference from `env.sandbox`, a provider credential or an expanded
`host_files` entry is a validation error, and so is a reference in an overlay
whose `when:` reads anything other than `runtime.forge` or `config`.

GitLab is unchanged: a JSON bundle does not fit GitLab's per-variable masking.

## Consequences

- Adding an integration is one `gh secret set` and one `env.runner` line, with
  no edits to the generated shim and no fullsend release.
- A run receives only the secrets the stored object holds, and a host-side
  script receives only the ones its harness references.
- `GH_WORKFLOW_TOKEN` and the minted role tokens stay unforgeable, because
  their names are refused as keys.
- The reusable workflow must declare the secret before a shim from the same
  release passes it, so both ship together.
- Updating one value means rewriting the whole object.
