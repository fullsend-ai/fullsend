---
title: "57. Repos management for per-repo installations"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - installation
  - per-repo
  - repos
  - multi-org
  - management
---

# 57. Repos management for per-repo installations

Date: 2026-06-17

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

Builds on [ADR 0033](0033-per-repo-installation-mode.md) (per-repo
installation mode) and [ADR 0048](0048-automatic-updates.md) (automatic
updates). Core commands work independently of ADR 0048;
version-management commands require its `--upstream-ref` mechanism.

## Context

Per-repo installation ([ADR 0033](0033-per-repo-installation-mode.md))
is fullsend's target model — each repo is self-contained with its own
`.fullsend/` directory, shim workflow, WIF provider, variables, and
secrets. However, the per-repo install path (`runPerRepoInstall()` in
`admin.go`) handles one repo at a time. Organizations managing tens or
hundreds of repos across multiple GitHub orgs face three gaps:

1. **No bulk operations.** Installing on 50 repos means 50 manual runs.
2. **No enrollment inventory.** Discovering which repos have fullsend
   requires scanning for guard variables across all repos — an O(n) API
   operation with no centralized record.
3. **No drift detection.** No reference state to compare against:
   "which repos have a stale mint URL?" or "which repos are still on
   v2.1.0?"

Per-org's enrollment system provides these operationally but at the cost
of a dedicated config repo, org-level variables, enrollment PRs,
reconciliation scripts, and three-level workflow nesting. A thin
orchestration layer over existing per-repo machinery can provide the same
value without that infrastructure.

## Decision

Add a `fullsend repos` subcommand group that manages per-repo
installations at scale via a declarative `repos.yaml` manifest.

**Target persona:** platform administrators (SRE/DevOps) managing
fullsend across an organization. Individual repo owners continue using
`fullsend github` for single-repo setup. The relationship is analogous
to Terraform vs cloud provider CLIs.

**Subcommands:**

| Command | Purpose |
|---------|---------|
| `repos init` | Discover existing installations, generate manifest |
| `repos status` | Read-only comparison of manifest vs actual state |
| `repos install` | Provision fullsend on uninstalled manifest repos |
| `repos sync` / `repos diff` | Reconcile configuration drift |
| `repos upgrade` | Upgrade scaffold shim ref across repos |
| `repos upgrade-mint` | Verify token mint deployment against manifest |
| `repos remove` | Remove fullsend from specific repos |

**Manifest:** a YAML file declaring desired state — mint config, default
field values, and a list of repos (strings for defaults, objects for
overrides). Supports glob patterns (`acme-corp/*`) and multi-org repos
in a single file. Lives wherever the operator chooses (not in a
`.fullsend` config repo). All read commands accept `--manifest` as a
local path or URL per [ADR 0038](0038-universal-harness-access.md).

**Key design constraints:**

- WIF provisioning and mint registration are serialized
  (read-modify-write on Cloud Run env vars). Install uses three phases:
  parallel discovery → sequential WIF → parallel scaffold.
- Version changes (`repos upgrade`) are separated from config
  reconciliation (`repos sync`) to prevent accidental upgrades.
- Works alongside per-org installations during a migration period;
  serves as the migration path for ADR 0044 (pending) deprecation.

Manifest schema, field resolution semantics, subcommand specifications,
and implementation details are in the
[repos management plan](../plans/repos-management.md) and
[repos init plan](../plans/repos-init.md).

## Consequences

- **Operational parity with per-org** — bulk install, inventory, and
  drift detection without per-org's infrastructure complexity (config
  repo, org-level variables, enrollment workflows).
- **Cross-org management** — a single manifest manages repos across
  multiple GitHub orgs, unlike per-org's single-org model.
- **Operator discipline required** — the manifest must be maintained
  manually; adding repos to the org doesn't auto-add them (unless
  matched by a glob pattern).
- **API rate scaling** — discovery is O(n) API calls per repo; large
  deployments may require rate limiting and `--repo` filtering.
- **Migration path** — if per-org is deprecated, the repos tool provides
  the migration mechanism before the old model is removed.

## References

- [ADR 0033](0033-per-repo-installation-mode.md) — per-repo installation model
- [ADR 0038](0038-universal-harness-access.md) — URL-based resource references
- ADR 0044 (pending) — per-org deprecation; repos tool replaces its deferred Option C
- [ADR 0045](0045-forge-portable-harness-schema.md) — harness composition via `base` URLs
- [ADR 0048](0048-automatic-updates.md) — `--upstream-ref` version pinning
- [Implementation plan: repos management](../plans/repos-management.md)
- [Implementation plan: repos init](../plans/repos-init.md)

## Implementation status

Implemented subcommands:

- `repos init` — PR #3033
- `repos install` — PR #3033
- `repos status` — PR #3031
- `repos add`, `repos remove`, `repos uninstall` — PR #4081
- `repos upgrade`, `repos upgrade-mint` — PR #4080
- `repos diff`, `repos sync` — PR #4079
