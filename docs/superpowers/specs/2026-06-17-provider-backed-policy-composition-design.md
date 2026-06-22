# Provider-Backed Policy Composition

**Issue:** [#776](https://github.com/fullsend-ai/fullsend/issues/776)
**Date:** 2026-06-17
**Status:** Draft

## Problem

All 6 default harness policy files duplicate the same network rule
blocks (`vertex_ai`, `github_api`, `package_registries`,
`gitleaks_releases`). When a service changes domains or endpoints,
every policy file must be updated independently. The `fix.yaml` policy
even comments "Identical to the code agent policy."

## Solution

Replace duplicated network rules with OpenShell provider profiles.
When a provider is attached to a sandbox, its profile contributes
network policy rules that are merged into the effective policy at fetch
time under reserved `_provider_*` keys. Harness policy files shrink to
harness-specific restrictions only (filesystem, process, landlock).

## What Changes

### New: `profiles/` directory

A new `profiles/` directory in the scaffold
(`internal/scaffold/fullsend-repo/profiles/`) contains five OpenShell
provider profile YAML files:

| Profile ID | Endpoints | Binaries | Access |
|---|---|---|---|
| `fullsend-vertex-ai` | `api.anthropic.com:443`, `*.googleapis.com:443` | `**/claude`, `**/node` | read-write |
| `fullsend-github` | `api.github.com:443`, `github.com:443` | `**/gh`, `**/node`, `**/git` | read-write |
| `fullsend-package-registries` | `registry.npmjs.org:443`, `pypi.org:443`, `files.pythonhosted.org:443`, `proxy.golang.org:443`, `sum.golang.org:443`, `storage.googleapis.com:443` | `**/npm`, `**/npx`, `**/node`, `**/pip`, `**/python`, `**/go` | read-only |
| `fullsend-gitleaks` | `github.com:443` (path: `/gitleaks/gitleaks/releases/`) | `**/curl` | read-only |
| `fullsend-github-artifacts` | `*.blob.core.windows.net:443`, `*.actions.githubusercontent.com:443` | `**/gh` | read-only |

Each file follows the OpenShell provider profile YAML format (same
schema as `providers/github.yaml` in the OpenShell repo). Custom
profiles are used instead of built-in ones because our profiles bundle
the exact endpoint combinations our harnesses need (e.g.,
`fullsend-vertex-ai` combines Anthropic API + Google Cloud APIs into
one profile, which doesn't match any single built-in).

### Modified: `fullsend run` flow

Two new steps in `internal/cli/run.go`, inserted between the existing
gateway check (step 2a) and provider creation (step 2b):

```
fullsend run <agent>
  ├─ Check openshell available             (existing, step 2)
  ├─ Check gateway running                 (existing, step 2a)
  ├─ Set providers_v2_enabled              (NEW)
  │    openshell settings set providers_v2_enabled true --global
  │    Idempotent — no-op if already set.
  ├─ Import provider profiles              (NEW)
  │    openshell provider profile import --from <profilesDir>
  │    Idempotent — re-importing unchanged profiles is a no-op.
  │    profilesDir = filepath.Join(absFullsendDir, "profiles")
  │    Skipped if directory does not exist.
  ├─ Ensure providers exist                (existing, step 2b)
  │    openshell provider create --name X --type fullsend-github ...
  │    The type now maps to our custom profile → network rules.
  ├─ Create sandbox with --provider flags  (existing)
  └─ OpenShell fetch-time composition      (automatic)
       Gateway merges profile network rules into effective policy
       under _provider_* keys.
```

Error handling follows the existing pattern: if profile import fails
(invalid YAML, OpenShell too old), `fullsend run` fails early with a
clear error message. No fallback to fat policies.

### Replaced: per-agent policy files → single `base.yaml`

**Principle: policy files must not contain `network_policies`.** All
network access is provided exclusively through provider profiles. Policy
files define only non-composable sandbox restrictions: filesystem access,
landlock, and process identity.

The six per-agent policy files (`triage.yaml`, `code.yaml`,
`review.yaml`, `fix.yaml`, `prioritize.yaml`, `retro.yaml`) are deleted
and replaced by a single `policies/base.yaml`. This is possible because:

1. All six files have identical `filesystem_policy`, `landlock`, and
   `process` sections.
2. All `network_policies` entries are now covered by provider profiles
   (including retro's `github_artifacts`, now served by the
   `fullsend-github-artifacts` profile).

All harness files update their `policy` field to reference
`policies/base.yaml` instead of per-agent policy files.

### Modified: provider definition `type` values

Provider definition files in `.fullsend/providers/` have their `type`
fields updated to reference our custom profile IDs. The `ProviderDef`
struct is unchanged — only the YAML values change:

```yaml
# Before
name: work-github
type: github
credentials:
  GITHUB_TOKEN: ${GITHUB_TOKEN}

# After
name: work-github
type: fullsend-github
credentials:
  GITHUB_TOKEN: ${GITHUB_TOKEN}
```

The `type` is the link between the provider instance and its profile —
OpenShell looks up the profile by type to extract network rules for
composition.

## What Does Not Change

- **Harness YAML schema:** `Policy` (string) and `Providers`
  ([]string) fields stay the same. No new fields.
- **`ProviderDef` struct:** Same Go struct (name, type, credentials,
  config). Only the values in YAML files change.
- **Policy YAML format:** Same OpenShell format, just fewer entries.
- **Sandbox creation flow:** `--provider` flags are already passed.
- **`action.yml`:** No changes. OpenShell is installed and the gateway
  is started externally as before.

## How Composition Works (OpenShell Side)

For reference, the OpenShell composition mechanism
([NVIDIA/OpenShell#1037](https://github.com/NVIDIA/OpenShell/pull/1037)):

1. When a sandbox calls `GetSandboxConfig` RPC, the gateway checks if
   `providers_v2_enabled` is true.
2. For each attached provider, it looks up the profile by provider type
   and extracts a `NetworkPolicyRule` via `profile.network_policy_rule()`.
3. `compose_effective_policy()` merges these rules into the sandbox
   policy under `_provider_*` keys (e.g., `_provider_work_github`).
4. Provider rules are isolated from user/agent rules — they cannot be
   merged into each other, and agent-proposed rules for the same
   host:port land as separate entries.

Key properties:
- **Additive only** — provider profiles add network rules, never
  remove or modify existing ones.
- **Isolated** — provider rules (`_provider_*`) are kept separate from
  user/agent rules.
- **Gated** — requires `providers_v2_enabled` setting (set
  automatically by `fullsend run`).
- **Requires** OpenShell >= v0.0.37.

## Migration

**No migration required.** The design is backwards-compatible:

- **Default agents** come from upstream via runtime layering (ADR
  0035). Updating the scaffold updates all deployments automatically on
  their next dispatch.
- **Custom agents** with existing fat policy files keep working. If a
  custom agent's policy duplicates rules that a provider profile also
  contributes, the rules are redundant but harmless — the proxy permits
  a request if it matches any rule.
- **Gateway setting** (`providers_v2_enabled`) is set automatically by
  `fullsend run`.
- **Profile import** is idempotent and runs on every `fullsend run`.

## Prerequisites

- OpenShell >= v0.0.37 (provider-backed composition support)
- `providers_v2_enabled` gateway setting (set automatically)
