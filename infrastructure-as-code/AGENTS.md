# AGENTS.md

Instructions for agents working under `infrastructure-as-code/`. Extends the repository-root [AGENTS.md](../AGENTS.md); commit and DCO conventions live there.

## Overview

This directory manages Infrastructure as Code (IaC) for fullsend-ai using [OpenTofu](https://opentofu.org/).

## Conventions

- **Separation of Concerns**: Each platform or infrastructure domain has its own directory (e.g., `cloudflare/`). Do not create monolithic configurations across multiple providers.
- **State Isolation**: Each component directory maintains its own independent state file and provider configurations.
- **Provider-Scoped Variables**: Structure variables under platform-specific objects (e.g., `variable "cloudflare" { ... }`).
- **Path-Filtered CI/CD**: Each component directory has a dedicated GitHub Actions workflow in the repo-root `.github/workflows/<component>.yml` triggered only by changes in that directory.

## Working with OpenTofu

- Run commands against a component from the repo root with `tofu -chdir=infrastructure-as-code/<component> ...`.
- Always run `tofu fmt` and `tofu validate` on the affected component directory before proposing changes.
- Use `-compact-warnings` on plan commands to keep output clean.
- Never commit secrets, tokens, credentials, `*.tfstate`, or `*.tfvars` files.
