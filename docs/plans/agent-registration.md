# Implementation plan: Agent registration

Implements [ADR 0058](../ADRs/0058-agent-registration.md).

## Phase 1: Config schema and per-repo `allowed_remote_resources`

**Goal:** Both org and per-repo config can express agent entries (URLs
or local paths) and remote resource allowlists. No behavioral changes
yet.

### 1a. Add `agents` field to both config structs

**File:** `internal/config/config.go`

Add an `AgentEntry` type that supports both string shorthand and
object form via a custom YAML unmarshaler:

```go
type AgentEntry struct {
    Name    string `yaml:"name,omitempty"`
    Source  string `yaml:"source"`
    Enabled *bool  `yaml:"enabled,omitempty"`
}
```

When `Enabled` is nil (omitted), the entry defaults to enabled.
Setting `enabled: false` suppresses the agent from the merged set
and blocks resolution. Disabled entries must have an explicit `Name`.

`AgentEntry` implements `yaml.Unmarshaler`: if the YAML node is a
scalar string, it populates `Source` and leaves `Name` empty (derived
from the source filename at usage time). If the node is a mapping, it
decodes `name` and `source` fields normally.

Add `Agents` to `OrgConfig` (top-level, alongside
`allowed_remote_resources`):

```go
type OrgConfig struct {
    // ... existing fields ...
    Agents                 []AgentEntry          `yaml:"agents,omitempty"`
    AllowedRemoteResources []string              `yaml:"allowed_remote_resources,omitempty"`
    // ...
}
```

Add `Agents` and `AllowedRemoteResources` to `PerRepoConfig`:

```go
type PerRepoConfig struct {
    Version                string              `yaml:"version"`
    KillSwitch             bool                `yaml:"kill_switch,omitempty"`
    Roles                  []string            `yaml:"roles,omitempty"`
    Agents                 []AgentEntry        `yaml:"agents,omitempty"`
    AllowedRemoteResources []string            `yaml:"allowed_remote_resources,omitempty"`
    CreateIssues           *CreateIssuesConfig `yaml:"create_issues,omitempty"`
}
```

Add a `AgentEntry.DerivedName()` helper that returns `Name` if set,
otherwise derives it from the `Source` filename.

### 1b. Validation

**File:** `internal/config/config.go` — `Validate()` for both
`OrgConfig` and `PerRepoConfig`

- Each entry is classified as a URL (starts with `https://`) or a
  local path.
- URL entries must include a `#sha256=` fragment (64-char hex) and
  must be prefixed by an entry in `allowed_remote_resources`.
- Local path entries must not contain path traversal (`..`).
- Agent names (derived from filename) must be unique across all
  entries.

### 1c. Per-repo `allowed_remote_resources`

**File:** `internal/config/config.go`

Add `AllowedRemoteResources` to `PerRepoConfig` (shown above). The
call sites that construct `ComposeOpts.OrgAllowlist` (in
`internal/harness/compose.go`) must merge both org and per-repo
allowlists before passing them to `matchingAllowedPrefix`.

### 1d. Seed defaults during install

**Files:** `internal/cli/admin.go`, `internal/cli/github.go`,
`internal/config/config.go`

A shared helper computes default agent URLs:

```go
func DefaultAgentEntries(commitSHA string) ([]AgentEntry, error)
```

This calls `scaffold.HarnessBaseURLWithHash()` for each default
harness name. The `--agents` flag filters which roles (and therefore
which harness URLs) are included.

**Org-mode install** (`runInstall`): `NewOrgConfig()` populates
`Agents` with default URLs.

**Per-repo install** (`runPerRepoInstall` / `runGitHubSetupPerRepo`):
`NewPerRepoConfig()` populates `Agents` with default URLs.

Both also seed default `AllowedRemoteResources`:
```go
[]string{
    "https://raw.githubusercontent.com/fullsend-ai/fullsend/",
    "https://raw.githubusercontent.com/fullsend-ai/agents/",
}
```

### 1e. Tests

**File:** `internal/config/config_test.go`

- Parse/marshal round-trip with `agents` and `allowed_remote_resources`
  for both `OrgConfig` and `PerRepoConfig`.
- Validation: duplicate agent names, missing hash, non-HTTPS, URL not
  in allowlist, path traversal rejected.
- Local path entries accepted without hash.
- String shorthand and object form both parse correctly.
- Explicit `name` overrides filename-derived name.
- `NewOrgConfig` and `NewPerRepoConfig` include default agent URLs.

---

## Phase 2: `fullsend agent` CLI subcommand

**Goal:** Users can manage agents in config from the CLI.

### 2a. Command structure

**File:** `internal/cli/agent.go` (new)

```
fullsend agent add <url-or-path> [--name <name>]
fullsend agent list [--fullsend-dir <path>]
fullsend agent update <name> [<sha>] [--fullsend-dir <path>]
fullsend agent remove <name> [--fullsend-dir <path>]
```

Register in `internal/cli/root.go`:
```go
cmd.AddCommand(newAgentCmd())
```

### 2b. `agent add`

1. Classify input as URL or local path.
2. **URL path:**
   a. **Pin commit SHA** — if the URL lacks a pinned commit (e.g. a
      `github.com` browse URL or a `raw.githubusercontent.com` URL
      with `main`/`HEAD` instead of a 40-char SHA), resolve the
      default branch HEAD via `forge.Client` and rewrite the URL to
      the pinned `raw.githubusercontent.com` form.
   b. Fetch harness YAML content.
   c. **Compute integrity hash** — if the URL lacks a `#sha256=`
      fragment, compute SHA-256 of the fetched content and append it.
   d. If a `#sha256=` fragment was already present, verify it matches.
   e. Add URL prefix to `allowed_remote_resources` if not present.
3. **Local path:**
   a. Validate path exists and has no traversal (`..`).
   b. Read harness YAML from disk.
4. Determine agent name: use `--name` if provided, otherwise derive
   from filename.
5. Parse harness to validate structure and extract role/slug.
6. Check for duplicate name in existing config.
7. Append entry to `agents` in config (string shorthand if no
   `--name`, object form if `--name` provided).
8. Write updated config.

### 2c. `agent list`

Build the merged agent set (scaffold base + config overlay) and print
a table: name, role, source (scaffold, URL, or path). Agents
overridden by config entries are shown with their config source, not
the scaffold.

### 2d. `agent update`

1. Look up agent by name in config. Error if not found or if entry
   is a local path (nothing to pin).
2. Parse the existing URL to extract the repo owner/name and harness
   path.
3. **If a SHA argument is provided:** use it directly.
   **If no SHA argument:** resolve the default branch HEAD via
   `forge.Client`.
4. Rewrite the URL with the new commit SHA.
5. Fetch the harness at the new URL, compute SHA-256, update the
   `#sha256=` fragment.
6. Write updated config.

### 2e. `agent remove`

Match by name (derived from filename), remove from `agents` list,
write config. Optionally clean up `allowed_remote_resources` if no
remaining URL agents use that prefix.

### 2f. Tests

**File:** `internal/cli/agent_test.go` (new)

- Add/list/update/remove round-trip.
- Add duplicate rejected.
- Add with bad URL rejected.
- Add with unpinned URL resolves SHA and computes hash.
- Add with pinned URL verifies existing hash.
- Add with local path works.
- Add with path traversal rejected.
- Update re-pins SHA and recomputes hash.
- Update with explicit SHA uses it directly.
- Update on local path entry returns error.
- Remove nonexistent name returns error.

---

## Phase 3: Runtime agent resolution in `fullsend run`

**Goal:** `fullsend run <name>` resolves agents from config at
runtime, loading harnesses directly from URLs or local paths. No
wrapper files are generated.

### 3a. Add merge helper

**File:** `internal/config/config.go` (or new `internal/config/agents.go`)

```go
func MergedAgents(scaffoldNames []string, commitSHA string, configAgents []AgentEntry) ([]MergedAgent, error)
```

1. Build base set from `scaffoldNames` using
   `scaffold.HarnessBaseURLWithHash()`.
2. Overlay `configAgents` — config entries with the same agent name
   replace scaffold entries; new names are appended. The agent name
   is the explicit `name` field if set, otherwise derived from the
   `Source` filename (e.g. `triage.yaml` → `triage`).
3. Return merged list sorted by name.

`MergedAgent` contains the resolved name, source (URL, path, or
scaffold), and whether it came from config or scaffold.

### 3b. Update `fullsend run` to resolve agents from config

**Files:** `internal/cli/run.go`, `internal/harness/` (loader)

When `fullsend run <name>` is invoked:

1. Build the merged agent set via `MergedAgents()` (scaffold base +
   config overlay from the target repo's `config.yaml`).
2. Look up the requested agent by name.
3. **URL source:** pass the URL directly to `LoadWithBase()`. The
   harness is fetched at runtime — no wrapper file is written to disk.
   Role and slug come from the harness content itself.
4. **Local path source:** resolve the path relative to `.fullsend/`
   and load the harness file directly.
5. **Agents repo fallback** (transitional): for known first-party
   agents not found in config, resolve the latest harness from
   `fullsend-ai/agents` at runtime. The fallback resolves the floating
   version tag (`v0`) SHA via the forge client, constructs a commit-pinned
   URL, checks the org allowlist, fetches via `fetch.FetchURL`, and
   caches content directly. Supply-chain integrity relies on the
   commit-pinned URL, TLS transport, and the org allowlist. This tier
   exists to support the [agent extraction](agent-extraction-to-agents-repo.md)
   without requiring config changes from existing users and will be
   removed once all users have migrated (Step 7).
6. **Scaffold source** (no config override): load the scaffold harness
   as today.

The `--harness` flag, when given a name instead of a file path, uses
this same resolution path.

### 3c. Remove wrapper generation for config agents

**File:** `internal/layers/harnesswrappers.go`

`HarnessWrappersLayer` continues to generate wrappers for
scaffold-based agents in org mode (these still need `role:` and
`slug:` from the GitHub App credentials). Config-driven agents bypass
wrapper generation entirely — `fullsend run` resolves them at runtime
(3b). As agents migrate from scaffold to config, the wrapper layer
shrinks.

### 3d. Tests

**File:** `internal/cli/run_test.go` (or new test file)

- Config agent resolved by name loads from URL at runtime.
- Config agent resolved by name loads from local path.
- Name collision: config entry wins over scaffold.
- Unknown agent name returns an error.
- Scaffold fallback works when agent is not in config.

---

## Phase 4: Remove hardcoded agent map and scaffold triage files

**Goal:** Delete compiled-in agent map and triage scaffold files.

**Prerequisite:** Dispatch routing must resolve agents from the merged
agent set (Phase 3) before scaffold files are deleted. Verify that
dispatch does not check for scaffold harness existence independently
— if it does, update it to use `MergedAgents()` so triage dispatch
continues to work after scaffold deletion.

### 4a. Clean up scaffold agent helpers

**File:** `internal/scaffold/baseurl.go`

Once agents are resolved from config (Phase 3), `HarnessBaseURL()`,
`HarnessContentHash()`, and `HarnessBaseURLWithHash()` are only used
for install-time default seeding (Phase 1d). Remove any branches or
helpers that are no longer needed. `HarnessNames()` continues to
return scaffold-embedded names for default URL computation.

### 4b. Delete triage scaffold files

**Directory:** `internal/scaffold/fullsend-repo/`

Delete:
- `agents/triage.md`
- `env/triage.env`
- `policies/triage.yaml`
- `schemas/triage-result.schema.json`
- `scripts/pre-triage.sh`
- `scripts/post-triage.sh`
- `scripts/post-triage-test.sh`
- `skills/issue-labels/SKILL.md`

### 4c. Update scaffold.go

**File:** `internal/scaffold/scaffold.go`

Remove deleted files from `executableFiles` map.

### 4d. Update Makefile

**File:** `Makefile`

Remove `post-triage-test.sh` from `script-test` target.

### 4e. Update validate-output-schema-test.sh

**File:** `internal/scaffold/fullsend-repo/scripts/validate-output-schema-test.sh`

Change default schema from `triage-result.schema.json` to
`prioritize-result.schema.json`. Update test data to match.

### 4f. Update docs/agents/triage.md

**File:** `docs/agents/triage.md`

Update source link to `fullsend-ai/agents` repo.

### 4g. Test updates

Multiple test files need updates for removed files and deleted
agent map:

- `internal/scaffold/scaffold_test.go` — remove triage from expected
  file lists, delete triage-specific tests, remove `IsExternalAgent`
  skip branches.
- `internal/scaffold/baseurl_test.go` — remove external agent test
  cases.
- `internal/harness/scaffold_integration_test.go` — remove triage
  from test tables, remove `IsExternalAgent` skips, update
  `TestDiscoverAgents` count.
- `internal/layers/harnesswrappers_test.go` — update base URL
  assertions.
- `internal/scaffold/vendormanifest_test.go` — change `agents/triage.md`
  references to `agents/code.md`.
- `internal/layers/workflows_test.go` — same.

---

## Phase 5: Transition to authoritative config

**Goal:** Remove the scaffold fallback so `agents` in config is the
sole source of agent discovery.

**Precondition:** All first-party agents (code, fix, review, retro,
prioritize, triage) have been extracted from the scaffold into
standalone repos and are registered via config in all active
installations.

### 5a. File tracking issue

Once the last first-party agent is extracted, file a GitHub issue to
track the transition. The issue should verify:

- All first-party agents are available in standalone repos.
- All active installations have `agents` populated in config (check
  via `fullsend repos status`).
- The deprecation notice from the additive merge is no longer being
  triggered in CI/logs.

### 5b. Remove scaffold fallback

**File:** `internal/config/agents.go` (or wherever `MergedAgents`
lives)

Change `MergedAgents()` to return only config entries — scaffold
names are no longer included as a base set. If `agents` is empty,
return an empty list (or error) instead of falling back.

### 5c. Remove scaffold harness files

**Directory:** `internal/scaffold/fullsend-repo/harness/`

Delete all remaining harness YAML files. The scaffold embed no longer
hosts agent harnesses. Related agent files (agents/, env/, policies/,
schemas/, scripts/, skills/) for each extracted agent should already
have been deleted in their respective extraction phases.

### 5d. Remove `HarnessNames()` and `HarnessWrappersLayer`

**Files:** `internal/scaffold/baseurl.go`,
`internal/layers/harnesswrappers.go`

`HarnessNames()` is no longer needed for agent discovery. Either
remove it or repurpose it for install-time defaults only.
`HarnessWrappersLayer` is no longer needed — all agents are resolved
at runtime from config. Remove the layer and its `harnessesForRole()`
helper.

### 5e. Update install seeding

**Files:** `internal/cli/admin.go`, `internal/cli/github.go`,
`internal/config/config.go`

`NewOrgConfig()` and `NewPerRepoConfig()` populate `agents` with
default URLs pointing to the standalone repos (not the scaffold).
This is the only place default agent URLs are defined.

### 5f. Tests

- Verify empty `agents` returns empty/error (no fallback).
- Verify `fullsend agent list` with empty config shows nothing.
- Remove all scaffold-harness-related test infrastructure.

---

## Phasing and PRs

| PR | Phase | Dependencies |
|----|-------|-------------|
| 1 | 1a-1e: config schema | None |
| 2 | 2a-2f: CLI commands | PR 1 |
| 3 | 3a-3d: runtime agent resolution | PR 1 |
| 4 | 4a-4g: remove agent map + triage files | PRs 1-3 |
| 5 | 5a-5f: authoritative config | All agents extracted |

PRs 2 and 3 can be developed in parallel after PR 1 merges. PR 4
is the cleanup that depends on everything else. Phase 5 is a
follow-up tracked by a GitHub issue, filed once all first-party
agents have been extracted from the scaffold.

**Related:** `fullsend agent migrate-customizations` (implemented in
ADR-0064 / PR #2932) migrates existing `customized/` overrides into
config-driven agents. It uses `DiffHarness` to compute minimal `base:`
composition harnesses and registers agents via the same config schema
defined in Phase 1.
