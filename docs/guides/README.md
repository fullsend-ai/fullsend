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
- [OpenAI Workload Identity](infrastructure/openai-workload-identity.md) — Run GPT models on the pi runtime without a stored OpenAI key: console setup, repository variables, local runs, troubleshooting

## Infrastructure

Advanced guides for platform operators who deploy and manage the GCP-side infrastructure (token mint, WIF, secrets).

- [Mint service administration](infrastructure/mint-administration.md) — Deploying and managing the token mint (GCP or Cloudflare)
- [Standalone mint](infrastructure/standalone-mint.md) — Running the token mint as a standalone HTTP server without GCP
- [Infrastructure reference](infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [Enabling fullsend on private repositories](infrastructure/private-repositories.md) — Additional guardrails and configuration for private repos
- [Tracing reference](infrastructure/distributed-tracing.md) — Telemetry levels, environment variables, span hierarchy, and attributes
- [Eval measurements](infrastructure/eval-measurements.md) — Online trace scoring with `eval-measurements.jsonl` and measurement manifests

## User guides

Guides for developers working in repositories where fullsend is active.

- [Bugfix workflow](user/bugfix-workflow.md) — End-to-end guide to how fullsend handles a bug report from issue to merge
- [Issue commands](user/issues-commands.md) — Slash commands and label triggers for interacting with agents
- [Running agents locally](user/running-agents-locally.md) — Run fullsend agents on your machine using released binaries (macOS + Linux)

### Customizing agents

Start with the [overview](user/customizing-overview.md) to pick the right approach, then follow the relevant guide:

- [Customizing agents overview](user/customizing-overview.md) — Quick decision guide for all customization approaches
- [Configuring with AGENTS.md](user/customizing-with-agents-md.md) — Guide agents using your repo's AGENTS.md file
- [Configuring with skills](user/customizing-with-skills.md) — Extend built-in agent skills; [authoring augmentations](user/customizing-with-skills.md#authoring-skills-that-augment-defaults)
- [Configuring agent behavior](user/customizing-agents.md) — Harness composition, status notifications, and disabling agents
- [Bring Your Own Agent](user/bring-your-own-agent.md) — Build and register a custom agent from scratch
- [Custom Agent Identity](user/custom-agent-identity.md) — Using a standalone mint for custom GitHub App identity
- [Harness Field Reference](../reference/harness-reference.md) — Complete harness YAML field reference, merge rules, and resource referencing
- [CEL Triggers Reference](user/cel-triggers-reference.md) — Dispatch flow, NormalizedEvent fields, transition kinds, and trigger patterns
- [Custom Poller Example](user/custom-poller-example.md) — Create a custom poller workflow that invokes fullsend harness agents with a pre-computed matrix
- [Building custom agents from scratch](user/building-custom-agents.md) — _(deprecated — see [Bring Your Own Agent](user/bring-your-own-agent.md))_
- [Default, derived, and custom agents](../agents/topics/default-vs-custom.md) — When configuration crosses into derived or custom agent territory

### Integrations & observability

- [Jira Integration](user/jira-integration.md) — Connect fullsend to a Jira project so that issue comments and label changes trigger agents
- [How to emit traces](user/how-to-emit-traces.md) — Configure a repository or organization to send OpenTelemetry traces to a remote backend
- [Tracing with MLflow](user/tracing-with-mlflow.md) — MLflow-specific setup: experiment routing, Basic auth encoding, org-level organization, and cost column caveats

## Development

Guides for contributors developing and testing fullsend itself.

- [E2E testing](dev/e2e-testing.md) — Local and CI e2e runs, including PR authorization and `ok-to-test`
- [CLI internals](dev/cli-internals.md) — Command structure, installation pipeline, and sandbox runtime
- [Behaviour testing](dev/behaviour-testing.md) — Write Gherkin scenarios for end-to-end agent behaviour
- [Behaviour test drivers](dev/behaviour-drivers.md) — Implement SCM and CI drivers for behaviour tests
- [Testing workflow changes](dev/testing-workflows.md) — Point a live GitHub org at a branch to test workflow, action, and agent changes before release
- [Tracing internals](dev/tracing.md) — How the distributed tracing implementation works and how to extend it
