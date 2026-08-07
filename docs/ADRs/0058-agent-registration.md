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

Which agents fullsend knows about was originally compiled into the binary.
Scaffold-embedded harnesses (formerly `internal/scaffold/fullsend-repo/harness/`,
now in `fullsend-ai/agents`) defined the complete agent set, and
`HarnessNames()` enumerated them from the embed. There was no mechanism
for registering agents that lived outside the scaffold — adding or
extracting an agent required a code change to the fullsend binary.

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
is a URL or local path, with an optional name override. Entries may
also set `enabled: false` to disable an agent (including scaffold
defaults) without removing it from configuration:

```yaml
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=<hash>
  - name: lint
    source: harness/my-linter.yaml
  - name: retro
    enabled: false   # suppression-only — disables scaffold default
```

`fullsend run <name>` resolves agents from config at runtime, loading
harnesses directly from URLs or local paths. No intermediate wrapper
files are generated on disk — role and slug come from the harness
content itself. Config entries are looked up directly via the agent name (explicit
`name` if set, otherwise derived from source filename). When an agent is
not in config, a runtime fallback resolves it from the `fullsend-ai/agents`
repository for known first-party agents. *(Originally, config entries
merged additively with scaffold-discovered agents on disk; the additive
merge and disk fallback were removed once all first-party agents were
extracted — see PR #5425.)*

A `fullsend agent` CLI subcommand (`add`, `list`, `update`, `remove`)
manages entries (single-user CLI operations; no concurrency guard on
config read/write) and auto-pins URLs to a commit SHA with an
integrity hash. Per-repo config gains `allowed_remote_resources` so per-repo
installs can validate base composition without an org config repo.
Per-repo config is read from the **base branch**, not the PR branch,
so a PR cannot inject an attacker-controlled `allowed_remote_resources`
entry or agent source.

Phasing, schema details, CLI behavior, and migration mechanics are covered
in the sections below.

## Consequences

- Anyone can add an agent to a fullsend installation via `fullsend agent add` — no code change required.
- First-party and third-party agents follow the same registration path.
- Config-driven registration allows agents to be added, updated, or removed without code changes.
- Per-repo installs no longer need org config for remote resource validation.
- Empty config falls back to agents-repo resolution for known first-party agents. *(The original scaffold-discovery disk fallback was removed once all customers migrated to config-driven agents — see PR #5425.)*
- **Transitional agents-repo fallback:** During the agent extraction, a runtime fallback resolves known first-party agents from `fullsend-ai/agents` when not in config. This avoids requiring config changes from existing users during extraction. The fallback will be removed once all users have migrated to config-driven registration (Phase 5).
- The `agents` YAML key was previously used in `OrgConfig` with a different schema (role/name/slug identity tuples, removed by ADR 0045 Phase 4). The new schema (URL/path source entries) is incompatible; a custom unmarshaler detects and rejects old-format entries with a clear error message.

## References

- [ADR 0033](0033-per-repo-installation-mode.md) -- per-repo installation mode
- [ADR 0038](0038-universal-harness-access.md) -- URL-based resource references and integrity hashes
- [ADR 0045](0045-forge-portable-harness-schema.md) -- harness composition via `base:` URLs
- [ADR 0057](0057-repos-management.md) -- repos management for per-repo installations
- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) -- user-facing guide for agent registration
