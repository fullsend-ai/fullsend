---
title: "150. Separate execution workspace from optional target repository"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - runtime
  - harness
  - ci-cd
  - execution
---

# 150. Separate execution workspace from optional target repository

Date: 2026-10-21

## Status

Accepted

Builds on [ADR 0093](0093-tracker-routed-status-notifications.md), which
separates a status destination from a code repository. It amends [ADR
0098](0098-entity-first-harness-evaluation.md) and complements [ADR
0133](0133-actor-attributed-data-security-boundary.md) and [ADR
0141](0141-deterministic-filtered-entity-context-staging.md). A missing target
repository does not remove entity attribution or content filtering.

## Context

`fullsend run` currently requires a local target directory: the CLI declares
`--target-repo` and marks it required in [`internal/cli/run.go` at
5e05c5487](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/cli/run.go#L451-L471).
The CLI also auto-detects a forge from CI environment variables when `--forge`
is omitted ([`detectForgePlatform` in `run.go` at
5e05c5487](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/cli/run.go#L6331-L6361)),
which is execution-host metadata, not proof that the host checkout is the
target. Treating that checkout as a target would conflate configuration,
execution, code, status, and credential scope. Some Jira, tenant, and
service-level tasks have configuration and input but no target code repository.

The runner therefore needs a repository-less contract that works for direct
invocations and CI-hosted runs, while keeping repository-targeted callers
compatible. The decision supports later managed execution without making
that execution path part of this change. See
[agent-infrastructure.md](../problems/agent-infrastructure.md) and
[agent-architecture.md](../problems/agent-architecture.md) for the broader
problems.

This ADR aims to let configured agents run without a target code repository
while keeping configuration, execution, and target context separate and
preserving repository-targeted runs.

## Options

### Infer repository-less mode when no path is supplied

Rejected. An omitted path may be an operator or workflow error, so the caller
must choose repository-targeted or repository-less mode explicitly.

### Represent the scratch directory as a target repository

Rejected. A scratch directory must not acquire Git, repository identity,
target instructions, repository-scoped credentials, or target status
semantics merely because a runtime needs a working directory.

### Use the CI host checkout as the default target

Rejected. The workflow project is an execution host unless the caller
explicitly selects it as the target.

### Add a separate runner for repository-less execution

Rejected. A separate runner would duplicate the existing `runAgent`
orchestration: the entrypoint loads run configuration and starts harness
resolution ([`runAgent` at
5e05c5487](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/cli/run.go#L476-L527)),
then composes and resolves the harness
([`runAgent` at the same commit](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/cli/run.go#L627-L646)).
Those responsibilities should remain shared while target-specific behavior
becomes explicit.

## Decision

We adopt an explicit repository-less run mode to let harnesses execute without
a target code repository while preserving existing repository-targeted
behavior.

`fullsend run` requires exactly one of:

- `--target-repo PATH` for a local target directory; or
- `--no-target-repo` for repository-less execution.

The runner distinguishes three runtime concepts:

- **Sandbox workspace root** is the existing `WorkspaceDir()` runtime API,
  backed by `sandbox.SandboxWorkspace` (`/sandbox/workspace`). Its meaning
  remains unchanged: it is the root for runner-managed files and output (see
  [`runtime.Runtime.WorkspaceDir`](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/runtime/runtime.go#L176),
  [`ClaudeRuntime.WorkspaceDir`](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/runtime/claude.go#L85),
  and [`sandbox.SandboxWorkspace`](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/sandbox/sandbox.go#L29-L30)
  at 5e05c5487).
- **Execution directory** is a runner-owned directory that is always present
  for the agent process. A repository-targeted run may use the target checkout
  as this directory. A repository-less run uses an empty scratch directory
  under the sandbox workspace root. It is separate from `RunParams.RepoDir`,
  which remains the target-repository path and is empty without a target. The
  current Codex configuration renderer rejects an empty `RepoDir` ([`RunParams`
  and `renderCodexConfig` at
  5e05c5487](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/runtime/runtime.go#L75-L88),
  [same commit](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/runtime/codex_config.go#L214-L222)).
  The implementation must pass the execution directory separately to the
  runtime rather than redefining `RepoDir`.
- **Target repository** is optional target identity and content. It is absent
  in repository-less mode even though the execution directory is present.

Repository-less mode does not create or upload a target checkout, and it does
not assign target-repository semantics to the scratch execution directory. In
particular, `TARGET_REPO_DIR`, `FULLSEND_TARGET_REPO_DIR`, and `REPO_DIR` are
absent in this mode.

Harness compatibility is explicit:

- `target_repo_required` is a top-level scalar resolved through base
  composition. A child harness may set it to `false` to override an inherited
  `true`; legacy `forge:` blocks and `overlays:` cannot set it.
- If the field is absent, it defaults to `true`. A malformed value fails during
  harness loading.
- Fullsend checks the field after harness resolution and capability
  validation, but before environment validation, credential selection, script
  execution, or sandbox creation.

Only `false` enables `--no-target-repo`. All other values require a target
repository, and repository-dependent harnesses never receive a fabricated one.

`--fullsend-dir` supplies the harness, agent, skill, and policy configuration.
`runAgent` resolves it as the workspace root and uses it to load configuration
and resolve the harness ([`runAgent` at
5e05c5487](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/cli/run.go#L476-L527),
[`harness.ComposeOpts` and harness resolution at the same commit](https://github.com/fullsend-ai/fullsend/blob/5e05c54876ee6f6824d1baf10ce4929e4826a6b7/internal/cli/run.go#L627-L646)).
It remains independent of the execution directory and target repository.

In repository-less mode, no forge is selected unless the caller supplies
`--forge` or equivalent explicit configuration. CI variables may identify the
execution environment for diagnostics, but they do not select a forge, target,
repository credentials, or status destination. An explicit forge selects an
adapter only; external repository access and status or output destinations
still require explicit resource and destination data. Inference credentials
remain usable when authorized, because they are not repository credentials. A
runner-local output directory may retain a safe default.

Status routing still follows ADR 0093. For a repository-less run, entity-first
attribution records no target repository but retains the verified invocation
identity, effective policy, harness revision, and resolved entity. Run, cache,
and entity identity never use a sentinel repository.

ADR 0141 applies when a repository-less run has a handled entity that needs an
entity-context snapshot. It does not require a target checkout or define runs
without such an entity. Entity content remains subject to ADR 0133, and
supported Git-forge snapshots also follow ADR 0141.

The implementation must satisfy these repository-less acceptance criteria:

- preserve existing repository-targeted CLI and workflow behavior;
- make `--no-target-repo` explicit and mutually exclusive with
  `--target-repo`;
- load harness, agent, skill, and policy configuration from `--fullsend-dir`,
  independently of the target repository;
- use a scratch execution directory without copying the CI host checkout or
  injecting its target instructions, and fail clearly when a selected harness
  requires a repository;
- keep host metadata, task or event source, target context, status/output
  destination, and credential scope distinct;
- fail early with an actionable diagnostic when a non-existent
  `--target-repo` path resembles an `owner/repo` identifier;
- demonstrate direct runs and repository-less runs from both GitHub Actions
  and GitLab CI.

## Forge scope

The runtime contract is forge-neutral and applies to GitHub, GitLab, and
Forgejo: repository-less mode never selects a target from CI host metadata,
and a target is supplied only explicitly. This ADR does not add or change
forge-specific adapters, workflows, credentials, or status routing. GitHub
Actions and GitLab CI are the required demonstrations; Forgejo-specific
workflow integration is outside this ADR because no Forgejo adapter or
workflow is changed here.

## Consequences

- Repository-independent agents get a stable runtime contract without
  weakening repository-targeted execution.
- Existing and unannotated harnesses remain target-repository–required, while
  repository-less compatibility becomes an explicit, reviewable capability.
- Runtime adapters must distinguish a working directory from target-repo
  identity, which may require small interface and environment changes.
- CI hosts can provide configuration and execution without implicitly granting
  target-repository authority or repository-scoped credentials.
- Managed polling, OpenShift Jobs, tenant schema changes, and making every
  built-in agent repository-independent remain follow-up work.

## References

- [ADR 0123 — Tenant configuration from repos.yaml](0123-tenant-configuration-from-repos-yaml.md)
- [#7605 — Move run-context preparation into fullsend](https://github.com/fullsend-ai/fullsend/issues/7605)
- [#6632 — Diagnose owner/repo values passed to `--target-repo`](https://github.com/fullsend-ai/fullsend/issues/6632)
