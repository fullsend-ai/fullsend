# AGENTS.md

Fullsend is a platform for fully autonomous agentic development for Git-hosted organizations (GitHub, GitLab, Forgejo). This repo holds design documents (`docs/`) and a Go CLI (`cmd/fullsend/`). See [fullsend.sh](https://fullsend.sh).

## Always-on

These apply to every change. Topic-specific rules live in the index below — read only the file for the current task.

- Stage before `make lint`. Pre-commit only checks staged files.
- Invoke this checkout's CLI as `go run ./cmd/fullsend …` from the repo root — never a `fullsend` binary from mise, `$PATH`, `go install`, or another tree. Details: [Go Code](docs/contributing/go-code.md#running-the-fullsend-cli).
- Follow [COMMITS.md](COMMITS.md) for commit messages and PR titles. Breaking changes need the `!` suffix on both.
- [DCO](https://developercertificate.org/): human-proposed commits (including human-driven agent sessions) use `git commit -s`. Autonomous agent commits must never add `Signed-off-by`.
- Changing Go production code under `cmd/` or `internal/` (not `_test.go`): verify patch coverage against the 80% target / 75% floor in [`.codecov.yml`](.codecov.yml). `make go-test` does not enforce this. Commands: [Go Code](docs/contributing/go-code.md#verifying-patch-coverage-locally).
- Forge operations go through `forge.Client`. Never shell out to `gh` or call forge APIs outside `internal/forge/github/`. See [Forge Abstraction](docs/contributing/forge-abstraction.md).
- Never commit secrets or sensitive identifiers (GCP projects, service accounts, Model Armor templates, internal hostnames). Sensitive values are environment variables with no defaults.
- Per-org installation mode is deprecated ([ADR 0044](docs/ADRs/0044-deprecate-per-org-installation-mode.md)). Do not extend it; flag remaining org-mode content as deprecated. Per-repo is the only supported model.

## Topic-specific guidance

| File | When to read |
|------|-------------|
| [Problem Documents](docs/contributing/problem-docs.md) | Adding or editing `docs/problems/` |
| [Go Code](docs/contributing/go-code.md) | Changing Go under `cmd/`, `internal/`, or `pkg/` |
| [Mintcore Architecture](docs/contributing/mintcore.md) | Changing `internal/mintcore/`, `cmd/mint-wasm/`, `cmd/mint/`, `internal/mint/`, or mint deploy provisioners |
| [Runtime Implementation](docs/contributing/runtime-implementation.md) | Adding or changing a `runtime.Runtime` backend |
| [Forge Abstraction](docs/contributing/forge-abstraction.md) | Adding forge operations |
| [Documentation](docs/contributing/documentation.md) | Changing CLI commands, flags, config/env vars, docs-site pages, or skills |
| [Config Reference](docs/reference/config-reference.md) | Adding or modifying `config.yaml` fields |
| [Harness Composition](docs/contributing/harness-composition.md) | Changing merge functions in `internal/harness/` or `AgentEntry` fields |
| [Harness Field Reference](docs/contributing/harness-fields.md) | Adding or modifying `Harness` or `ForgeConfig` fields |
| [Normative Event Specification](docs/normative/normalized-event/v1/README.md) | Changing `event_payload` or `internal/normevent/` |
| [CEL Triggers](docs/contributing/cel-triggers.md) | Writing or reviewing harness `trigger` CEL or `.feature` CEL filters |
| [ADRs](docs/contributing/adrs.md) | Touching `docs/ADRs/` |
| [Workflow Contracts](docs/contributing/workflow-contracts.md) | Changing GHA reusable workflows |
| [CI Workflows](docs/contributing/ci-workflows.md) | Changing `.github/workflows/` or `pull_request_target` secrets |
| [Behaviour Testing](docs/guides/dev/behaviour-testing.md) | Behaviour-test repo provisioning, forks, or workflow dispatch |
| [Sandbox Topology](docs/contributing/sandbox-topology.md) | Sandbox images, CI image pulling, or agent harness configs |
| [Shell Scripting](docs/contributing/shell-scripting.md) | Writing or reviewing shell scripts |
| [Bot Identities](docs/contributing/bot-identities.md) | Referencing bot identities in code |
| [GitLab Role Credentials](docs/contributing/gitlab-role-credentials.md) | GitLab registered-role credentials |
| [Vouch System](docs/contributing/vouch-system.md) | Contributor vouch gate or PR workflows |
| [Tier Conventions](docs/contributing/tier-conventions.md) | Using the term "tier" in code or docs |
| [Design Decisions](docs/contributing/design-decisions.md) | Architectural principles |
| [Experiments issues](CONTRIBUTING.md#where-to-file-experiments-related-issues) | Work in the `experiments/` submodule vs this tracker |
