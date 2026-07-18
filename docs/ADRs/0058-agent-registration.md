---
title: "58. Agent registration"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - agents
  - per-repo
  - configuration
  - extensibility
---

# 58. Agent registration

Date: 2026-06-29

## Status

Accepted

## Context

Which agents fullsend knows about is currently compiled into the binary.
Scaffold-embedded harnesses (`internal/scaffold/fullsend-repo/harness/`)
define the complete agent set, and `HarnessNames()` enumerates them
from the embed. There is no mechanism for registering agents that live
outside the scaffold — adding or extracting an agent requires a code
change to the fullsend binary.

The triage agent extraction to `fullsend-ai/agents`
([ADR 0045](0045-forge-portable-harness-schema.md) Phase 4) is the
first agent to move out of the scaffold. The registration mechanism
must support both first-party extractions and user-defined agents
without code changes.

## Decision

Make agent registration a **config-level concept**. Add an `agents`
list to both `OrgConfig` and `PerRepoConfig`. (Note: ADR 0045 Phase 4
previously removed the `agents` block from `OrgConfig`; this re-adds
a field with the same YAML key but different semantics — harness
source URLs rather than role/name/slug identity tuples.) Each entry
is a URL or local path, with an optional name override:

```yaml
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=<hash>
  - name: lint
    source: harness/my-linter.yaml
```

`fullsend run <name>` resolves agents from config at runtime, loading
harnesses directly from URLs or local paths. No intermediate wrapper
files are generated on disk — role and slug come from the harness
content itself. Config entries merge additively with
scaffold-discovered agents; collision is keyed by agent name (explicit
`name` if set, otherwise derived from source filename) and resolved in
favor of config, enabling gradual migration. Once all first-party agents are extracted, config
becomes authoritative and the scaffold fallback is removed.

A `fullsend agent` CLI subcommand (`add`, `list`, `update`, `remove`;
plus `migrate-customizations` per [ADR 0064](0064-deprecate-customized-directory-overlay.md))
manages entries (single-user CLI operations; no concurrency guard on
config read/write) and auto-pins URLs to a commit SHA with an
integrity hash. Per-repo config gains `allowed_remote_resources` so per-repo
installs can validate base composition without an org config repo.
Per-repo config is read from the **base branch**, not the PR branch,
so a PR cannot inject an attacker-controlled `allowed_remote_resources`
entry or agent source.

See the [implementation plan](../plans/agent-registration.md) for
phasing, schema details, CLI behavior, and migration mechanics.

## Consequences

- Anyone can add an agent to a fullsend installation via `fullsend agent add` — no code change required.
- First-party and third-party agents follow the same registration path.
- The additive merge model allows agents to be extracted from the scaffold one at a time without disrupting existing installations.
- Per-repo installs no longer need org config for remote resource validation.
- No forced migration — empty config falls back to scaffold discovery until populated.
- **Transitional agents-repo fallback:** During the [agent extraction](../plans/agent-extraction-to-agents-repo.md), a runtime fallback resolves known first-party agents from `fullsend-ai/agents` when not in config. This avoids requiring config changes from existing users during extraction. The fallback will be removed once all users have migrated to config-driven registration (Phase 5 / extraction plan Step 7).
- The `agents` YAML key was previously used in `OrgConfig` with a different schema (role/name/slug identity tuples, removed by ADR 0045 Phase 4). The new schema (URL/path source entries) is incompatible; a custom unmarshaler detects and rejects old-format entries with a clear error message.

## References

- [ADR 0033](0033-per-repo-installation-mode.md) -- per-repo installation mode
- [ADR 0038](0038-universal-harness-access.md) -- URL-based resource references and integrity hashes
- [ADR 0045](0045-forge-portable-harness-schema.md) -- harness composition via `base:` URLs
- [ADR 0057](0057-repos-management.md) -- repos management for per-repo installations
- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) -- user-facing guide for agent registration
- [Implementation plan](../plans/agent-registration.md)
