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
machine-checked: the `reservedSandboxKeys` sync note in `internal/cli/run.go`,
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

The lockstep with the fleet harnesses fullsend loads ([ADR 0058](0058-agent-registration.md))
is enforced only by tribal knowledge; the topology this needs — released tag
at PR time, `main` nightly ([ADR 0139](0139-agent-functional-test-suite-lives-in-agents-repo.md))
— already exists for functional tests.

## Options

- **Hand-maintained manifest** — rejected: it recreates the drift
  (#1677 passed review the same way).
- **Behavior-level golden over `bootstrapEnv`** — strongest check,
  but needs fixtures for every emission gate; hardening follow-up.

## Decision

Publish the sandbox environment contract as a generated, committed manifest
at [`sandbox-env.json`](../normative/sandbox-env/v1/sandbox-env.json), with
schema and category semantics in its [README](../normative/sandbox-env/v1/README.md).
A build-time collector generates the manifest by running every code
path that injects environment variables into the sandbox — the
`bootstrapEnv` `.env` emission, the per-iteration env, trace-ID injection,
the security-hook env, each production runtime's `EnvExports()`, the
`fullsend agent new` Vertex scaffold, and the effective reserved-key set
after the init-time merges — so the contract cannot disagree with the code
that builds the environment. An always-on golden test byte-compares the
committed file against collector output (`-update` regenerates), and fails
when a runtime is newly registered without a manifest decision.

The manifest is fetchable at any tag via raw.githubusercontent.com — the pinned-URL
mechanism of [ADR 0038](0038-universal-harness-access.md) — and the tag is the
version — the tag itself is the version identifier, so the manifest
carries no embedded `fullsend_version` field. Consumers validate env-var
reachability
against the latest released tag at PR time and `main` nightly, following the
[ADR 0139](0139-agent-functional-test-suite-lives-in-agents-repo.md) bridge.

## Consequences

- Adding, changing, or reserving a runner env var requires a same-PR manifest
  bump, visible in the diff.
- Content repos gain a fail-closed gate: a failed or unparseable manifest
  fetch fails the check rather than skipping it.
- The [ADR 0058](0058-agent-registration.md) transitional lockstep between
  runner and fleet harnesses becomes verifiable instead of tribal knowledge.
- The manifest is derived contract data — no runtime cost or behavior change.
- `harness_delivered` variables are labeled not-runner-guaranteed: their
  provenance is the stock scaffold and may lag the fleet's `gcp-vertex.env`.

## Forge scope

The manifest is forge-agnostic: it describes the variables the runner
injects into the sandbox regardless of which forge hosts the repository.
The fetch URL uses raw.githubusercontent.com because fullsend is hosted on
GitHub; a GitLab or Forgejo deployment would use the equivalent raw-file
endpoint. Consumer validation logic (parse, fail closed) is
forge-independent.

## References

- [ADR 0038](0038-universal-harness-access.md) — URL-based resource access and tag pinning
- [ADR 0058](0058-agent-registration.md) — agent registration and the transitional fleet lockstep
- [ADR 0139](0139-agent-functional-test-suite-lives-in-agents-repo.md) — cross-repo validation topology
- [fullsend-ai/agents#1677](https://github.com/fullsend-ai/agents/issues/1677) — the drift incident
- [fullsend-ai/agents#1672](https://github.com/fullsend-ai/agents/issues/1672) — proposed `FULLSEND_SKILLS_DIR` follow-up
