---
title: "146. Sandbox environment contract manifest"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - runtime
  - security
  - drift
---

# 146. Sandbox environment contract manifest

Date: 2026-10-21

## Status

Accepted

## Context

The sandbox environment surface — what the runner injects into the sandbox and
reserves from harness override — was hand-synced in three descriptions, none
machine-checked: the sync note on the `reservedSandboxKeys` map in
`internal/cli/run.go`,
the agents repo's `SANDBOX_RUNTIME_VARS` allowlist in
`scripts/skill-env-reachability-test.sh`, and per-runtime `EnvExports()` in
prose only. The drift was realized in
[fullsend-ai/agents#1677](https://github.com/fullsend-ai/agents/issues/1677):
a skill migrating `CLAUDE_CONFIG_DIR` to a proposed `FULLSEND_SKILLS_DIR`
passed the agents repo's reachability gate because the same PR edited the
allowlist — no released runner exports it, and a bot reviewer, not CI, caught
it. Content repos cannot verify at PR time that env vars their skills
reference are exported by any released runner — a drift-control gap in the
[threat model's](../problems/security-threat-model.md) ranked priorities.

The lockstep with the fleet harnesses fullsend loads
([ADR 0058](0058-agent-registration.md)) is enforced only by tribal
knowledge; the topology this needs — released tag at PR time via
[ADR 0038](0038-universal-harness-access.md)'s pinned-URL mechanism,
`main` nightly
([ADR 0139](0139-agent-functional-test-suite-lives-in-agents-repo.md)) —
already exists for functional tests. The proposed `FULLSEND_SKILLS_DIR`
([fullsend-ai/agents#1672](https://github.com/fullsend-ai/agents/issues/1672))
is a follow-up to close the remaining drift surface.

## Options

- **Hand-maintained manifest** — rejected: it recreates the drift
  (#1677 passed review the same way).
- **Generated bespoke JSON document** — rejected in review: every other
  contract under `docs/normative/` is JSON Schema, and a schema lets any
  consumer validate with stock tooling instead of parsing a custom format.
- **Behavior-level golden over `bootstrapEnv`** — strongest check, but
  needs fixtures for every emission gate; hardening follow-up.

## Decision

Publish the sandbox environment contract as generated, committed JSON
Schemas (Draft 2020-12) under
[`docs/normative/sandbox-env/v1/`](../normative/sandbox-env/v1/README.md):

- [`harness-env.schema.json`](../normative/sandbox-env/v1/harness-env.schema.json)
  validates a harness YAML's `env.sandbox` blocks: no key may be a name
  the runner reserves.
- [`env-references.schema.json`](../normative/sandbox-env/v1/env-references.schema.json)
  validates that code running under a runtime reads only variables the
  contract makes reachable there, and carries the full inventory —
  always, conditional (with gates), per-runtime, harness-delivered,
  reserved, deprecated — in its `$defs`.

A build-time collector generates the schemas by running every code path
that injects environment variables into the sandbox: the
`.env` emission in `bootstrapEnv` (`internal/cli/run.go`), the
per-iteration env, trace-ID injection, the security-hook env, each
production runtime's `EnvExports()` and launch-command exports, the
`fullsend agent new` Vertex scaffold, and the effective reserved-key set
after the init-time merges. Commands are replayed in a scratch workspace
and parsed for real `export` bindings, and each runtime's declared launch
exports are checked against its built launch commands, so the contract
cannot disagree with the code that builds the environment. An always-on
golden test byte-compares the committed schemas against collector output
(`-update` regenerates); a companion runtime-enumeration test fails when
a runtime is newly registered without a contract decision.

The schemas are fetchable at any tag via raw.githubusercontent.com
(tag-pinned URLs complement the commit-SHA + integrity-hash mechanism of
[ADR 0038](0038-universal-harness-access.md) for read-only contract
data that does not execute). The `v1` path and the tag identify the
version. Consumers validate against the latest released tag at PR time
and `main` nightly, following the
[ADR 0139](0139-agent-functional-test-suite-lives-in-agents-repo.md)
bridge. `fullsend lint` keeps checking reserved keys in-process; the
schemas serve repos that validate without running the fullsend binary.

Scope is the sandbox only. The environment of host-side pre-scripts,
post-scripts, and validation scripts (`childScriptEnv`,
`validationScriptEnv`) is a different surface — the runner's own
environment plus `env.runner` — and is out of scope here.

## Consequences

- Adding, changing, or reserving a runner env var requires a same-PR
  schema regeneration, visible in the diff.
- Content repos gain a fail-closed gate: a failed or unparseable schema
  fetch fails the check rather than skipping it.
- The [ADR 0058](0058-agent-registration.md) transitional lockstep between
  runner and fleet harnesses becomes verifiable instead of tribal knowledge.
- The schemas are derived contract data — no runtime cost or behavior
  change.
- Harness-delivered variables are not runner guarantees: their
  provenance is the stock scaffold and may lag the fleet's
  `gcp-vertex.env`.
- A future per-version harness JSON Schema
  ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)
  follow-up) can `$ref` the env definitions here instead of restating them.

## Forge scope

The schemas are forge-agnostic: they describe the variables the runner
injects into the sandbox regardless of which forge hosts the repository.
The fetch URL uses raw.githubusercontent.com because fullsend is hosted on
GitHub; a GitLab or Forgejo deployment would use the equivalent raw-file
endpoint. Consumer validation logic (parse, fail closed) is
forge-independent.
