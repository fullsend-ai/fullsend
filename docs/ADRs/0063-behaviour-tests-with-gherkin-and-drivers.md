---
title: "63. Behaviour tests with Gherkin and pluggable drivers"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - e2e
  - testing
  - behaviour-tests
---

# 63. Behaviour tests with Gherkin and pluggable drivers

Date: 2026-06-07

## Status

Accepted

## Context

Fullsend needs end-to-end tests that validate **deterministic platform behaviour** — dispatch routing, harness loading, schema validation, post-scripts, token scoping, sandbox policy, and SCM mutations — without depending on LLM output. This is distinct from admin install e2e ([ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)) and from LLM/instruction testing ([testing-agents.md](../problems/testing-agents.md)).

Runtime selection is shared with production via `defaults.runtime` in org `config.yaml` ([runtimes.md](../runtimes.md)). Harness definitions remain as in [ADR 0024](0024-harness-definitions.md). Per-repo install mode ([ADR 0033](0033-per-repo-installation-mode.md)) is the behaviour v1 default; per-org install is deferred.

## Decision

- Add **behaviour tests** under `e2e/behaviour/` using **godog** and portable Gherkin feature files.
- Exercise **real SCM + real CI** through **driver interfaces** (`scm.Driver`, `ci.Driver`, `install.Driver`); v1 implementations target GitHub and GitHub Actions.
- Substitute inference with a **dummy runtime** (`runtime: dummy` in per-repo config, or `defaults.runtime: dummy` for per-org) that executes scripted operations in the real OpenShell sandbox and emits `behaviour-results.json`.
- Select backends via **runner env** (`BEHAVIOUR_SCM`, `BEHAVIOUR_CI`, `BEHAVIOUR_INSTALL_MODE`); feature files stay install-mode agnostic. v1 runs **per-repo** against the halfsend org pool; the suite provisions fullsend via `fullsend github setup` rather than requiring pre-installed orgs.
- Use **compatibility tags** (`@skip:*`, `@requires:*`) to filter scenarios for future backends; tags do not select configuration.

## Consequences

- Behaviour tests can pass while prompt quality regresses; LLM evals remain necessary for instruction coverage.
- Behaviour orgs are provisioned at suite start with `--runtime dummy`; production orgs must not use dummy unintentionally.
- Adding GitLab or Tekton requires new drivers and runner env values, not feature file rewrites.
- Dummy runtime op vocabulary stays minimal; new ops require runtime + docs updates when scenarios need them.
