# Guides

Practical how-to documentation for fullsend, organized by audience. For design documents and architectural context, see [docs/problems/](../problems/), [docs/ADRs/](../ADRs/), and [docs/architecture.md](../architecture.md).

## Getting started

Guides for onboarding organizations and configuring GitHub — the first thing most users need.

- [Getting Inference](getting-started/getting-inference.md)
- [Configuring GitHub](getting-started/configuring-github.md)
- [Organization Mode](getting-started/org-mode.md)

## Infrastructure

Advanced guides for platform operators who deploy and manage the GCP-side infrastructure (token mint, WIF, secrets).

- [Mint service administration](infrastructure/mint-administration.md) — Deploying and managing the token mint Cloud Function
- [Infrastructure reference](infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [Enabling fullsend on private repositories](infrastructure/private-repositories.md) — Additional guardrails and configuration for private repos

## User guides

Guides for developers working in repositories where fullsend is active.

- [Bugfix workflow](user/bugfix-workflow.md) — End-to-end guide to how fullsend handles a bug report from issue to merge
- [Running agents locally](user/running-agents-locally.md) — Run fullsend agents on your machine using released binaries (macOS + Linux)
- [Customizing agents](user/customizing-agents.md) — Harness configurations and layered content resolution for your org and repos
- [Customizing with AGENTS.md](user/customizing-with-agents-md.md) — Guide agents using your repo's AGENTS.md file
- [Customizing with skills](user/customizing-with-skills.md) — Extend or replace built-in agent skills with custom skill documents
- [Building custom agents](user/building-custom-agents.md) — Create a new agent from scratch on a per-repo fullsend installation

## Development

Guides for contributors developing and testing fullsend itself.

- [E2E testing](dev/e2e-testing.md) — Local and CI e2e runs, including PR authorization and `ok-to-test`
- [CLI internals](dev/cli-internals.md) — Command structure, installation pipeline, and sandbox runtime
- [Testing workflow changes](dev/testing-workflows.md) — Point a live GitHub org at a branch to test workflow, action, and agent changes before release
