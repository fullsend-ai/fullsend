# Guides

Practical how-to documentation for fullsend, organized by audience. For design documents and architectural context, see [docs/problems/](../problems/), [docs/ADRs/](../ADRs/), and [docs/architecture.md](../architecture.md).

Structure decided in [ADR 0023](../ADRs/0023-user-documentation-structure.md).

## Administration

Guides for org administrators who install, configure, and manage fullsend.

- [Installing fullsend](admin/installation.md) — Set up fullsend in a GitHub organization from scratch (see [#328](https://github.com/fullsend-ai/fullsend/pull/328))
- [Enabling fullsend on private repositories](admin/private-repositories.md) — Additional guardrails and configuration for private repos

## User guides

Guides for developers working in repositories where fullsend is active.

- [Bugfix workflow](user/bugfix-workflow.md) — End-to-end guide to how fullsend handles a bug report from issue to merge
- [Running agents locally](user/running-agents-locally.md) — Run fullsend agents on your machine using released binaries (macOS + Linux)

## Development

Guides for contributors developing and testing fullsend itself.

- [Local development](dev/local-dev.md) — Run fullsend agents locally on macOS and Linux (amd64 + arm64)
