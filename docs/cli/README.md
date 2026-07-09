---
sidebar_label: Overview
---

# Fullsend CLI

The `fullsend` CLI manages the complete fullsend lifecycle: provisioning GCP infrastructure, configuring GitHub, enrolling repositories, and running agents locally.

## Installation

Download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases). For detailed setup instructions, see [Getting Started](../guides/getting-started/).

## Command groups

| Command group | Description |
|--------------|-------------|
| [`fullsend github`](github.md) | Configure GitHub orgs and repos — setup, enrollment, day-2 operations |
| [`fullsend inference`](inference.md) | Manage GCP Workload Identity Federation for Agent Platform access |
| [`fullsend mint`](mint.md) | Deploy and manage the OIDC token mint service |

## Additional commands

| Command | Description |
|---------|-------------|
| `fullsend run` | Execute an agent locally in a sandbox. See [running agents locally](../guides/user/running-agents-locally.md). |
| `fullsend agent` | Manage agent registrations in config. Subcommands: `add`, `list`, `update`, `remove` (CRUD) and `migrate-customizations` (one-time migration of `customized/` overrides to config-driven agents per ADR-0064) |
| `fullsend lock [agent-name]` | Pin remote dependencies to `lock.yaml` |
| `fullsend scan` | Run security scanners on agent input/output |

## Global flags

All commands that interact with GitHub resolve authentication via `gh` CLI or `GH_TOKEN` environment variable. The CLI runs preflight checks and tells you exactly which OAuth scopes are missing before making any changes.

For the complete command tree with implementation details, see [CLI internals](../guides/dev/cli-internals.md).
