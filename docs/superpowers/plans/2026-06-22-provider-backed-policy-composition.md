# Provider-Backed Policy Composition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace duplicated network rules across harness policy files with composable OpenShell provider profiles, so network access is defined once per service and composed automatically at sandbox fetch time.

**Architecture:** Five custom provider profile YAMLs define network rules per service (vertex-ai, github, package-registries, gitleaks, github-artifacts). During `fullsend run`, profiles are imported into the gateway and providers are created with types that map to these profiles. OpenShell's fetch-time composition merges provider network rules into the effective policy under `_provider_*` keys. All harnesses share a single `policies/base.yaml` for non-composable sandbox restrictions (filesystem, landlock, process). Note: only `network_policies` are composable via providers — filesystem_policy, landlock, and process must remain in the policy file.

**Tech Stack:** Go, OpenShell CLI, YAML

**Spec:** `docs/superpowers/specs/2026-06-17-provider-backed-policy-composition-design.md`

---

### Task 1: Create provider profile YAML files

Create four OpenShell provider profile YAMLs in the scaffold. These define
the network rules that were previously duplicated across policy files.
Endpoints and binaries are extracted from the current policy files — the
profiles use the superset across all harnesses (single profile per service).

**Files:**
- Create: `internal/scaffold/fullsend-repo/profiles/fullsend-vertex-ai.yaml`
- Create: `internal/scaffold/fullsend-repo/profiles/fullsend-github.yaml`
- Create: `internal/scaffold/fullsend-repo/profiles/fullsend-package-registries.yaml`
- Create: `internal/scaffold/fullsend-repo/profiles/fullsend-gitleaks.yaml`
- Create: `internal/scaffold/fullsend-repo/profiles/fullsend-github-artifacts.yaml`

- [ ] **Step 1: Create fullsend-vertex-ai profile**

```yaml
# profiles/fullsend-vertex-ai.yaml
id: fullsend-vertex-ai
display_name: Fullsend Vertex AI
description: Anthropic API and Google Cloud APIs for inference
category: inference
endpoints:
  - host: api.anthropic.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
  - host: "*.googleapis.com"
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
binaries:
  - "**/claude"
  - "**/node"
```

- [ ] **Step 2: Create fullsend-github profile**

Uses read-write access (superset — review/retro currently use read-only but
we chose single profile per service). Binaries include `**/git` and
`**/pre-commit` which code/fix agents need.

```yaml
# profiles/fullsend-github.yaml
id: fullsend-github
display_name: Fullsend GitHub
description: GitHub API and Git operations for fullsend agents
category: source_control
endpoints:
  - host: api.github.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
  - host: github.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
binaries:
  - "**/gh"
  - "**/git"
  - "**/node"
  - "**/pre-commit"
```

- [ ] **Step 3: Create fullsend-package-registries profile**

Covers npm, yarn, pnpm, PyPI, and Go module registries. All read-only.

```yaml
# profiles/fullsend-package-registries.yaml
id: fullsend-package-registries
display_name: Fullsend Package Registries
description: npm, PyPI, and Go module registries for dependency installation
category: data
endpoints:
  - host: registry.npmjs.org
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: registry.yarnpkg.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: pypi.org
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: files.pythonhosted.org
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: proxy.golang.org
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: sum.golang.org
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: storage.googleapis.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
binaries:
  - "**/npm"
  - "**/npx"
  - "**/yarn"
  - "**/yarnpkg"
  - "**/pnpm"
  - "**/node"
  - "**/pip"
  - "**/pip3"
  - "**/python"
  - "**/python3"
  - "**/python3.*"
  - "**/go"
  - "**/pre-commit"
```

- [ ] **Step 4: Create fullsend-gitleaks profile**

Covers GitHub releases download for gitleaks binary (used by pre-commit).

```yaml
# profiles/fullsend-gitleaks.yaml
id: fullsend-gitleaks
display_name: Fullsend Gitleaks
description: GitHub releases access for gitleaks binary download
category: data
endpoints:
  - host: github.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: objects.githubusercontent.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: release-assets.githubusercontent.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
binaries:
  - "**/pre-commit"
```

- [ ] **Step 5: Create fullsend-github-artifacts profile**

Retro-specific: Azure blob + GitHub Actions artifact endpoints for
downloading workflow run logs.

```yaml
# profiles/fullsend-github-artifacts.yaml
id: fullsend-github-artifacts
display_name: Fullsend GitHub Artifacts
description: GitHub Actions artifact download endpoints
category: data
endpoints:
  - host: "*.blob.core.windows.net"
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: "*.actions.githubusercontent.com"
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
binaries:
  - "**/gh"
```

- [ ] **Step 6: Commit**

```bash
git add internal/scaffold/fullsend-repo/profiles/
git commit -s -m "feat(sandbox): add provider profile definitions for policy composition

Define five OpenShell provider profiles (vertex-ai, github,
package-registries, gitleaks, github-artifacts) that capture the
network rules previously duplicated across harness policy files.

Part of #776."
```

---

### Task 2: Create provider definition YAML files

Create ProviderDef YAML files in the scaffold. These define the providers
that `fullsend run` will create on the gateway. Each provider's `type`
maps to a custom profile from Task 1, which contributes network rules.

No credentials are defined — these providers exist purely for network
policy composition. Credential delivery (GH_TOKEN, GCP credentials)
continues via host_files unchanged.

**Files:**
- Create: `internal/scaffold/fullsend-repo/providers/vertex-ai.yaml`
- Create: `internal/scaffold/fullsend-repo/providers/github.yaml`
- Create: `internal/scaffold/fullsend-repo/providers/package-registries.yaml`
- Create: `internal/scaffold/fullsend-repo/providers/gitleaks.yaml`
- Create: `internal/scaffold/fullsend-repo/providers/github-artifacts.yaml`

- [ ] **Step 1: Create provider definition files**

```yaml
# providers/vertex-ai.yaml
name: vertex-ai
type: fullsend-vertex-ai
```

```yaml
# providers/github.yaml
name: github
type: fullsend-github
```

```yaml
# providers/package-registries.yaml
name: package-registries
type: fullsend-package-registries
```

```yaml
# providers/gitleaks.yaml
name: gitleaks
type: fullsend-gitleaks
```

```yaml
# providers/github-artifacts.yaml
name: github-artifacts
type: fullsend-github-artifacts
```

- [ ] **Step 2: Commit**

```bash
git add internal/scaffold/fullsend-repo/providers/
git commit -s -m "feat(sandbox): add provider definitions for policy composition

Each provider references a custom profile type (fullsend-*) that
contributes network rules via OpenShell's provider-backed composition.
No credentials — credential delivery remains via host_files.

Part of #776."
```

---

### Task 3: Register profiles/ and providers/ as layered directories

Add `profiles/` and `providers/` to the `layeredDirs` list in
`internal/scaffold/scaffold.go`. This ensures they are:
- Skipped during scaffold installation (not committed to `.fullsend`)
- Provided at runtime by reusable workflows (like harness/, policies/, etc.)

**Files:**
- Modify: `internal/scaffold/scaffold.go:62-71`

- [ ] **Step 1: Add to layeredDirs**

In `internal/scaffold/scaffold.go`, add `"profiles/"` and `"providers/"`
to the `layeredDirs` slice (line 62-71):

```go
var layeredDirs = []string{
	"agents/",
	"skills/",
	"schemas/",
	"harness/",
	"plugins/",
	"policies/",
	"profiles/",
	"providers/",
	"scripts/",
	"env/",
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/scaffold/ -v -run TestWalk`

Verify existing scaffold tests still pass — adding directories to
`layeredDirs` only affects which files are skipped during installation.

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/scaffold.go
git commit -s -m "refactor(scaffold): register profiles/ and providers/ as layered directories

These directories contain upstream defaults provided at runtime by
reusable workflows, like harness/ and policies/. Adding them to
layeredDirs ensures they are not committed to .fullsend during
scaffold installation.

Part of #776."
```

---

### Task 4: Add sandbox helper functions (TDD)

Add `ImportProfiles` and `EnableProvidersV2` functions to the sandbox
package. These wrap `openshell` CLI calls and follow the same patterns
as the existing `EnsureProvider` and `CheckGateway` functions.

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Modify: `internal/sandbox/sandbox_test.go`

- [ ] **Step 1: Write failing tests**

Add to `internal/sandbox/sandbox_test.go`:

```go
func TestImportProfiles_DirNotExist(t *testing.T) {
	err := ImportProfiles("/nonexistent/path/that/does/not/exist")
	assert.NoError(t, err, "should return nil when directory does not exist")
}

func TestImportProfiles_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	err := ImportProfiles(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider profile import")
}

func TestEnableProvidersV2_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	err := EnableProvidersV2()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "providers_v2")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/sandbox/ -v -run "TestImportProfiles|TestEnableProvidersV2"`

Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement ImportProfiles**

Add to `internal/sandbox/sandbox.go`:

```go
// ImportProfiles imports provider profile YAML files from a directory into
// the gateway. Idempotent — re-importing unchanged profiles is a no-op.
// Returns nil if the directory does not exist.
func ImportProfiles(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	out, err := exec.Command("openshell", "provider", "profile", "import", "--from", dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("provider profile import from %s failed: %s", dir, strings.TrimSpace(string(out)))
	}
	return nil
}
```

- [ ] **Step 4: Implement EnableProvidersV2**

Add to `internal/sandbox/sandbox.go`:

```go
// EnableProvidersV2 sets the providers_v2_enabled gateway setting to true,
// enabling provider-backed policy composition. Idempotent.
func EnableProvidersV2() error {
	out, err := exec.Command("openshell", "settings", "set", "providers_v2_enabled", "true", "--global").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to enable providers_v2: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/sandbox/ -v -run "TestImportProfiles|TestEnableProvidersV2"`

Expected: PASS

- [ ] **Step 6: Run full sandbox test suite**

Run: `go test ./internal/sandbox/ -v`

Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go
git commit -s -m "feat(sandbox): add ImportProfiles and EnableProvidersV2 functions

ImportProfiles imports provider profile YAMLs into the gateway via
openshell provider profile import. EnableProvidersV2 sets the
providers_v2_enabled gateway setting. Both are idempotent.

Part of #776."
```

---

### Task 5: Wire profile import into fullsend run

Insert two new steps into the `fullsend run` flow in `internal/cli/run.go`:
enable providers_v2, then import profiles. Both go inside the existing
`if len(h.Providers) > 0` block, before the provider creation loop.

**Files:**
- Modify: `internal/cli/run.go:413-430`

- [ ] **Step 1: Add profile import and v2 setting to run.go**

In `internal/cli/run.go`, find the block starting at line 413:

```go
// 2b. Ensure providers exist on the gateway (if any declared).
if len(h.Providers) > 0 {
	providersDir := filepath.Join(absFullsendDir, "providers")
```

Replace with:

```go
// 2b. Ensure providers exist on the gateway (if any declared).
if len(h.Providers) > 0 {
	// Enable provider-backed policy composition on the gateway.
	provV2Start := time.Now()
	printer.StepStart("Enabling providers v2")
	if err := sandbox.EnableProvidersV2(); err != nil {
		printer.StepFail("Failed to enable providers v2")
		return fmt.Errorf("enabling providers v2: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Providers v2 enabled (%.1fs)", time.Since(provV2Start).Seconds()))

	// Import provider profiles (if profiles/ directory exists).
	profilesDir := filepath.Join(absFullsendDir, "profiles")
	profileStart := time.Now()
	printer.StepStart("Importing provider profiles")
	if err := sandbox.ImportProfiles(profilesDir); err != nil {
		printer.StepFail("Failed to import provider profiles")
		return fmt.Errorf("importing provider profiles: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Provider profiles imported (%.1fs)", time.Since(profileStart).Seconds()))

	providersDir := filepath.Join(absFullsendDir, "providers")
```

The rest of the block (loading provider defs, ensuring providers) stays
unchanged.

- [ ] **Step 2: Run go vet**

Run: `make go-vet`

Expected: No issues.

- [ ] **Step 3: Run unit tests**

Run: `go test ./internal/cli/ -v -run TestRun -count=1`

If no unit tests cover `runAgent` directly (it requires openshell),
verify with: `go build ./cmd/fullsend/`

Expected: Compiles without errors.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/run.go
git commit -s -m "feat(sandbox): wire provider profile import into fullsend run

When a harness declares providers, fullsend run now:
1. Enables providers_v2_enabled on the gateway
2. Imports provider profiles from the profiles/ directory
3. Creates providers (existing behavior)

Profile import is idempotent and skipped if the directory does not
exist. The v2 setting is a one-time no-op after first enablement.

Part of #776."
```

---

### Task 6: Update harness files to declare providers

Add the `providers` field to all six scaffold harness files, listing
which providers each agent needs. This tells `fullsend run` to create
the providers and attach them to the sandbox.

**Files:**
- Modify: `internal/scaffold/fullsend-repo/harness/triage.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/code.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/review.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/fix.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/prioritize.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/retro.yaml`

- [ ] **Step 1: Add providers to triage.yaml**

Add after `policy: policies/triage.yaml`:

```yaml
providers:
  - vertex-ai
  - github
```

- [ ] **Step 2: Add providers to code.yaml**

Add after `policy: policies/code.yaml`:

```yaml
providers:
  - vertex-ai
  - github
  - package-registries
  - gitleaks
```

- [ ] **Step 3: Add providers to review.yaml**

Add after `policy: policies/review.yaml`:

```yaml
providers:
  - vertex-ai
  - github
```

- [ ] **Step 4: Add providers to fix.yaml**

Add after `policy: policies/fix.yaml`:

```yaml
providers:
  - vertex-ai
  - github
  - package-registries
  - gitleaks
```

- [ ] **Step 5: Add providers to prioritize.yaml**

Add after `policy: policies/prioritize.yaml`:

```yaml
providers:
  - vertex-ai
  - github
```

- [ ] **Step 6: Add providers to retro.yaml**

Add after `policy: policies/retro.yaml`:

```yaml
providers:
  - vertex-ai
  - github
  - github-artifacts
```

- [ ] **Step 7: Commit**

```bash
git add internal/scaffold/fullsend-repo/harness/
git commit -s -m "feat(sandbox): declare providers in harness files

Each harness now lists the providers it needs. This triggers
provider creation and attachment during fullsend run, enabling
provider-backed policy composition.

- triage, review, prioritize: vertex-ai, github
- code, fix: vertex-ai, github, package-registries, gitleaks
- retro: vertex-ai, github, github-artifacts

Part of #776."
```

---

### Task 7: Replace per-agent policy files with a single base.yaml

All six policy files have identical filesystem/landlock/process sections
and no longer need per-agent network_policies (all network rules are now
provided by provider profiles). Replace them with a single
`policies/base.yaml` and update all harness files to reference it.

**Files:**
- Create: `internal/scaffold/fullsend-repo/policies/base.yaml`
- Delete: `internal/scaffold/fullsend-repo/policies/triage.yaml`
- Delete: `internal/scaffold/fullsend-repo/policies/code.yaml`
- Delete: `internal/scaffold/fullsend-repo/policies/review.yaml`
- Delete: `internal/scaffold/fullsend-repo/policies/fix.yaml`
- Delete: `internal/scaffold/fullsend-repo/policies/prioritize.yaml`
- Delete: `internal/scaffold/fullsend-repo/policies/retro.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/triage.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/code.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/review.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/fix.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/prioritize.yaml`
- Modify: `internal/scaffold/fullsend-repo/harness/retro.yaml`

- [ ] **Step 1: Create policies/base.yaml**

```yaml
---
version: 1

# Base sandbox policy shared by all agents.
#
# Defines non-composable sandbox restrictions: filesystem access,
# landlock, and process identity. Network access is provided
# entirely by provider profiles via provider-backed policy
# composition (ADR 0046).

filesystem_policy:
  include_workdir: true
  read_only: [/usr, /lib, /proc, /dev/urandom, /app, /etc, /var/log]
  read_write: [/sandbox, /tmp, /dev/null]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
  run_as_group: sandbox
```

- [ ] **Step 2: Delete per-agent policy files**

```bash
git rm internal/scaffold/fullsend-repo/policies/triage.yaml \
      internal/scaffold/fullsend-repo/policies/code.yaml \
      internal/scaffold/fullsend-repo/policies/review.yaml \
      internal/scaffold/fullsend-repo/policies/fix.yaml \
      internal/scaffold/fullsend-repo/policies/prioritize.yaml \
      internal/scaffold/fullsend-repo/policies/retro.yaml
```

- [ ] **Step 3: Update harness files to reference base.yaml**

In all six harness files, change `policy: policies/<agent>.yaml` to
`policy: policies/base.yaml`:

- `harness/triage.yaml`: `policy: policies/triage.yaml` → `policy: policies/base.yaml`
- `harness/code.yaml`: `policy: policies/code.yaml` → `policy: policies/base.yaml`
- `harness/review.yaml`: `policy: policies/review.yaml` → `policy: policies/base.yaml`
- `harness/fix.yaml`: `policy: policies/fix.yaml` → `policy: policies/base.yaml`
- `harness/prioritize.yaml`: `policy: policies/prioritize.yaml` → `policy: policies/base.yaml`
- `harness/retro.yaml`: `policy: policies/retro.yaml` → `policy: policies/base.yaml`

- [ ] **Step 4: Run lint**

Run: `make lint`

Expected: No failures.

- [ ] **Step 5: Commit**

```bash
git add internal/scaffold/fullsend-repo/policies/ internal/scaffold/fullsend-repo/harness/
git commit -s -m "refactor(sandbox): replace per-agent policies with shared base.yaml

All network rules are now provided by provider profiles via
composition. The per-agent policy files had identical
filesystem/landlock/process sections, so they collapse into a
single base.yaml.

Harness files updated to reference policies/base.yaml.

Part of #776."
```

---

### Task 8: Write ADR 0046

Formalize the design decision as an ADR. The content is derived from
the design spec at `docs/superpowers/specs/2026-06-17-provider-backed-policy-composition-design.md`.

**Files:**
- Create: `docs/ADRs/0046-provider-backed-policy-composition.md`

- [ ] **Step 1: Write the ADR**

Follow the format of existing ADRs (e.g., `0045-forge-portable-harness-schema.md`).
Use YAML frontmatter with `title`, `status`, `relates_to`, `topics`.

```markdown
---
title: "46. Provider-backed policy composition"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - policy
  - providers
  - composition
  - sandbox
---

# 46. Provider-backed policy composition

Date: 2026-06-22

## Status

Accepted

## Context

ADR 0024 established per-agent harness files with a `policy` field
pointing to an OpenShell policy YAML. Each policy file contains
filesystem, process, and network restrictions for one agent.

In practice, every policy file duplicates the same network rule blocks
for shared services: Vertex AI inference endpoints, GitHub API access,
package registries (npm, PyPI, Go modules), and gitleaks binary
downloads. Six agents × four service groups = the same endpoints and
binaries repeated in every combination.

This duplication creates maintenance burden: when a service adds or
changes endpoints, every policy file must be updated independently.
The fix.yaml policy comments "Identical to the code agent policy,"
making the redundancy explicit.

OpenShell v0.0.37 introduced provider-backed policy composition
(NVIDIA/OpenShell#1037). When a provider is attached to a sandbox and
has a registered profile, the gateway merges the profile's network
rules into the effective policy at fetch time under reserved
`_provider_*` keys. This is additive-only and keeps provider rules
isolated from user/agent rules.

## Decision

**Policy files must not contain network_policies.** All network access
is provided through provider profiles — this is the single mechanism
for granting network access to sandboxed agents. Policy files define
only non-composable sandbox restrictions: filesystem access, landlock,
and process identity.

Define custom provider profiles for each service and import them into
the gateway during `fullsend run`. Harnesses declare which providers
they need — each contributes network rules automatically via
composition. Because all agents share identical non-network policy
sections, a single `policies/base.yaml` replaces the per-agent
policy files.

Five custom profiles:

| Profile ID | Service | Access |
|---|---|---|
| `fullsend-vertex-ai` | Anthropic API, Google Cloud APIs | read-write |
| `fullsend-github` | GitHub API, Git transport | read-write |
| `fullsend-package-registries` | npm, PyPI, Go modules | read-only |
| `fullsend-gitleaks` | GitHub releases for gitleaks | read-only |
| `fullsend-github-artifacts` | GitHub Actions artifact download | read-only |

Custom profiles are used instead of OpenShell built-ins because our
profiles bundle endpoint combinations specific to fullsend (e.g.,
fullsend-vertex-ai combines Anthropic API + GCP APIs into one profile).

Provider definitions reference these profiles via the `type` field.
No credentials are defined — providers exist for network policy
contribution only. Credential delivery continues via host_files
(ADR 0025 tier 4).

The `providers_v2_enabled` gateway setting and profile imports are
managed automatically by `fullsend run`, consistent with how it
already manages provider creation.

## Consequences

- **Network access is exclusively provider-managed.** Policy files
  never contain `network_policies` — network rules live in provider
  profiles and are composed at fetch time. This eliminates the
  duplication that motivated this change and prevents it from
  recurring.
- Network rules for each service are defined once in a profile YAML.
  Adding or changing endpoints updates one file.
- All agents share a single `policies/base.yaml` for non-composable
  restrictions (filesystem, landlock, process). Per-agent policy
  files are eliminated.
- No schema changes to harness YAML or ProviderDef. Existing custom
  agents with inline network rules keep working — duplicated rules are
  redundant but harmless (composition is additive), but should be
  migrated to providers over time.
- Requires OpenShell >= v0.0.37 and the `providers_v2_enabled` gateway
  setting (set automatically).
- Single profile per service means all agents get the broadest access
  level (read-write for GitHub). If per-agent access differentiation
  is needed later, split into separate profiles (e.g.,
  fullsend-github-rw, fullsend-github-ro).
```

- [ ] **Step 2: Commit**

```bash
git add docs/ADRs/0046-provider-backed-policy-composition.md
git commit -s -m "docs(adr): add ADR 0046 for provider-backed policy composition

Formalizes the decision to adopt OpenShell provider-backed policy
composition. Policy files must not contain network_policies — all
network access is provided through five composable provider profiles.

Closes #776."
```

---

### Task 9: Final verification

- [ ] **Step 1: Run full test suite**

Run: `make go-test`

Expected: All tests pass.

- [ ] **Step 2: Run vet and lint**

Run: `make go-vet && make lint`

Expected: No issues.

- [ ] **Step 3: Review the full diff**

Run: `git log --oneline main..HEAD`

Verify the commit history is clean and each commit is self-contained.
