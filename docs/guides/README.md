# Guides

Practical how-to documentation for fullsend, organized by audience. For design documents and architectural context, see [docs/problems/](../problems/), [docs/ADRs/](../ADRs/), and [docs/architecture.md](../architecture.md).

## Getting started

Guides for onboarding organizations and configuring GitHub — the first thing most users need.

- [Mint enrollment](getting-started/README.md) — Enroll your org or repo in a token mint before configuring anything else
- [Getting Inference](getting-started/getting-inference.md) — Provision GCP inference access for your org or repo
- [Configuring GitHub](getting-started/configuring-github.md) — Install GitHub Apps and run the setup CLI
- [Organization Mode](getting-started/org-mode.md) — _(deprecated — see [per-repo Getting Started](getting-started/configuring-github.md))_ Org-wide setup with a shared `.fullsend` config repo

## Operations & Advanced Setup

Guides for organization owners and repository administrators who manage fullsend installations.

- [Operations](getting-started/operations.md) — Enrollment, configuration updates, status checks, uninstall, and standalone commands
- [Advanced setup](infrastructure/advanced-setup.md) — Alternative installation paths, setup flags, custom app sets, and manual WIF configuration

## Infrastructure

Advanced guides for platform operators who deploy and manage the GCP-side infrastructure (token mint, WIF, secrets).

- [Mint service administration](infrastructure/mint-administration.md) — Deploying and managing the token mint (GCP or Cloudflare)
- [Standalone mint](infrastructure/standalone-mint.md) — Running the token mint as a standalone HTTP server without GCP
- [Infrastructure reference](infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [Enabling fullsend on private repositories](infrastructure/private-repositories.md) — Additional guardrails and configuration for private repos
- [Tracing reference](infrastructure/distributed-tracing.md) — Telemetry levels, environment variables, span hierarchy, and attributes

## User guides

Guides for developers working in repositories where fullsend is active.

- [Bugfix workflow](user/bugfix-workflow.md) — End-to-end guide to how fullsend handles a bug report from issue to merge
- [Running agents locally](user/running-agents-locally.md) — Run fullsend agents on your machine using released binaries (macOS + Linux)
- [Configuring agent behavior](user/customizing-agents.md) — Harness configurations and `base:` composition for your org and repos
- [Configuring with AGENTS.md](user/customizing-with-agents-md.md) — Guide agents using your repo's AGENTS.md file
- [Configuring with skills](user/customizing-with-skills.md) — Extend or replace built-in agent skills
- [Bring Your Own Agent](user/bring-your-own-agent.md) — Add a custom agent or configure an existing one, from harness file to CI
- [CEL Triggers Reference](user/cel-triggers-reference.md) — Dispatch flow, NormalizedEvent fields, transition kinds, and trigger patterns
- [How to emit traces](user/how-to-emit-traces.md) — Configure a repository or organization to send OpenTelemetry traces to a remote backend
- [Tracing with MLflow](user/tracing-with-mlflow.md) — MLflow-specific setup: experiment routing, Basic auth encoding, org-level organization, and cost column caveats
- [Jira Integration](user/jira-integration.md) — Connect fullsend to a Jira project so that issue comments and label changes trigger agents
- [Building custom agents from scratch](user/building-custom-agents.md) — _(deprecated — see [Bring Your Own Agent](user/bring-your-own-agent.md))_
- [Default, derived, and custom agents](../agents/topics/default-vs-custom.md) — When configuration crosses into derived or custom agent territory

## Development

Guides for contributors developing and testing fullsend itself.

- [E2E testing](dev/e2e-testing.md) — Local and CI e2e runs, including PR authorization and `ok-to-test`
- [CLI internals](dev/cli-internals.md) — Command structure, installation pipeline, and sandbox runtime
- [Behaviour testing](dev/behaviour-testing.md) — Write Gherkin scenarios for end-to-end agent behaviour
- [Behaviour test drivers](dev/behaviour-drivers.md) — Implement SCM and CI drivers for behaviour tests
- [Testing workflow changes](dev/testing-workflows.md) — Point a live GitHub org at a branch to test workflow, action, and agent changes before release
- [Tracing internals](dev/tracing.md) — How the distributed tracing implementation works and how to extend it
