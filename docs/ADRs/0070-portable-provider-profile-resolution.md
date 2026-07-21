---
title: "70. Portable provider and profile resolution"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - providers
  - profiles
  - portability
  - remote-resources
---

# 70. Portable provider and profile resolution

Date: 2026-07-03

## Status

Accepted

## Context

[ADR 0038](0038-universal-harness-access.md) introduced URL-based resource resolution
for harnesses, enabling agents, openshell policies, skills, and other declarative resources to be
referenced from remote sources with integrity hashing. However, provider and profile
definitions remain resolved from hardcoded local directories only.

Currently:
- Provider definitions are loaded from `.fullsend/providers/` by `LoadProviderDefs`
- Profile definitions are imported from `.fullsend/profiles/` by `ImportProfiles`

When a harness is referenced via `base:` (ADR 0045) and that base harness lives in a
remote repository, its bundled provider and profile definitions cannot be discovered.
The base harness may declare providers needed for the agent to run, but those providers
are inaccessible to child harnesses that use the base as a composition anchor.

This blocks full harness portability. A shared base harness should be able to bundle
everything an agent needs: agent definitions, openshell policies, skills, **and** providers and
profiles. Today, providers and profiles are an exception.

### Related work

- [ADR 0038](0038-universal-harness-access.md): Established URL-based resource
  resolution with integrity hashing for agents, openshell policies, skills, and other
  declarative resources.
- [ADR 0045](0045-forge-portable-harness-schema.md): Introduced harness `base:` for
  composition; allows a child harness to inherit and extend a base harness.
- [ADR 0065](0065-provider-backed-policy-composition.md): Defines the provider
  model and composition semantics for openshell policy binding and credential
  delivery.

## Decision

Extend the harness schema with two new fields:

1. **`openshell.profiles`** — A new list field accepting only HTTPS URLs with `#sha256=...`
   integrity hashes. Profiles define openshell-level credential type schemas and
   belong in shared repositories, not locally. No local-path form.

2. **`providers`** — Extend the existing list field to accept both local provider
   names (existing behavior) and remote HTTPS URLs with `#sha256=...` hashes.
   Mixed forms allowed in the same list.

### Schema

```yaml
# New openshell.profiles field (URL-only)
openshell:
  profiles:
  - "https://github.com/org/profiles/tree/main/claude-code.yaml#sha256=abc..."
  - "https://github.com/org/profiles/tree/main/google-vertex-ai.yaml#sha256=def..."

# Extended providers field (mixed local names and URLs)
providers:
  - "my-local-provider"  # Local name (existing)
  - "https://github.com/org/repo/tree/main/providers/my-provider.yaml#sha256=789..."  # Remote
```

### Resolution flow

**Phase 1 — Base composition (`compose.go`)**

When a harness declares `base:`, the base YAML is fetched and parsed. The
`mergeBaseIntoChild()` function merges `openshell.profiles` and `providers` lists:
- Base entries come first, child entries append
- Deduplication by profile `id` (from profile YAML) / provider `name` (from provider YAML)
- Child wins in dedup conflicts
- Concatenation with last-writer-wins dedup (note: skills in ADR 0045 now use basename-aware dedup per [#5408](https://github.com/fullsend-ai/fullsend/pull/5408); profiles and providers use simpler id/name dedup at resolution time)

**Phase 2 — Resource resolution (`resolve.go`)**

After base composition produces the final merged harness, `ResolveHarness()` adds two
new loops:

1. **Profiles:** For each entry in `openshell.profiles`:
   - Fetch the URL (cache-aware, HTTPS-only, SSRF-hardened)
   - Verify SHA-256 integrity hash
   - Cache the resource (content-addressed)
   - Parse as openshell profile YAML
   - Validate that `id` field is non-empty
   - Store resolved profile (id + local cache path) for later import

2. **Providers:** For each entry in `providers`:
   - Check `IsURL()` to distinguish local names from URLs
   - If URL: fetch, verify hash, cache, parse as ProviderDef YAML
     - Validate `name` and `type` fields are non-empty
   - If not URL: leave as local name (resolved later from `providers/` dir in `run.go`)

After resolution, all remote URLs are replaced with local cache paths.
Downstream code sees only local paths and names.

### Integration in `run.go`

The provider import flow expands to:

1. Check gateway availability (`CheckGateway`)
2. **Validate referential integrity** (fail-fast, before any gateway mutations):
   deduplicate URL-resolved providers, then verify each URL-resolved provider's
   `type` matches a URL-resolved profile `id`. Local providers are validated
   by the gateway at creation time (step 5).
3. Enable providers v2 and import resolved profiles to the gateway
   (`openshell provider profile import`)
4. Load local provider defs from `providers/` dir (existing `LoadProviderDefs`),
   merge with URL-resolved provider defs from resolution phase
5. Create/ensure each provider on the gateway (existing `EnsureProvider`)

Referential integrity failure is a hard error:
```
"provider 'my-claude' references profile type 'claude-code',
but no profile with that id was declared"
```

### Base harness inheritance

When a harness uses `base:`, the base can declare its own `openshell.profiles` and `providers`.
This is the key portability scenario — a shared base harness bundles everything an
agent needs.

**Merge semantics:**
- `openshell.profiles`: concatenate base + child lists, deduplicate by profile `id` (child wins)
- `providers`: concatenate base + child lists. Precedence at runtime (highest
  first): local directory defs > URL-resolved child defs > URL-resolved base defs.
  URL-resolved providers deduplicate by resolved `name` (child wins). Local names
  shadow URL-resolved names of the same `name`.

**Example:**

```yaml
# Base harness at https://github.com/org/shared-harness/harness.yaml
agent: agents/code.md
policy: policies/default.yaml  # openshell policy
openshell:
  profiles:
  - "https://github.com/org/profiles/tree/main/claude-code.yaml#sha256=aaa..."
  - "https://github.com/org/profiles/tree/main/github.yaml#sha256=bbb..."
providers:
  - "https://github.com/org/providers/tree/main/claude.yaml#sha256=ccc..."
```

```yaml
# Child harness in .fullsend/harness/code.yaml
base: "https://github.com/org/shared-harness/harness.yaml#sha256=ddd..."
providers:
  - "my-local-provider"
```

Result after merge and resolution:
- Both profiles (two from base URL)
- Two providers: base's remote `claude` provider + child's local `my-local-provider`
- If child declared a remote provider with same `name` as base's, child's wins

## Validation

### Schema validation (`ValidateResourceTypes`)

- `openshell.profiles[]`: every entry must pass `IsURL()` and have a valid `#sha256=...`
  integrity hash. Profiles are always remote.
- `providers[]`: if `IsURL()`, require `#sha256=...` integrity hash. If not URL,
  accept as local provider name (no change).

### Content validation (after resolution)

- Each resolved profile YAML must have non-empty `id` field
- Each URL-resolved provider YAML must have non-empty `name` and `type` fields
- Credential values must be `${VAR}` references, never literal secrets. If a
  credential value doesn't match the `${...}` pattern, emit a warning.

### Referential integrity (in `run.go`)

- For each **URL-resolved** provider, verify its `type` matches a URL-resolved
  profile `id`. Mismatch is a hard error; abort harness execution.
- **Local providers** are not checked here — their `type` references
  gateway-resident profiles, and the gateway itself rejects unknown types at
  `openshell provider create` time.
- When URL-resolved providers exist but no URL-resolved profiles are declared,
  a warning is emitted (referential integrity cannot be verified ahead of time).
- Runs after profile import but before provider creation.

## Security

No new attack surface. Same controls as ADR 0038:

- All profile/provider URLs go through `fetch.FetchURL` — SSRF-hardened, HTTPS-only,
  DNS pre-resolution with IP validation
- `#sha256=...` integrity hash required and verified on every fetch
- Profile/provider URLs must match `AllowedRemoteResources` prefixes
- URL-fetched provider YAMLs contain `${VAR}` references, never resolved secrets
- Integrity hash covers the template YAML, protecting against substitution
- Content validation warns if a credential value doesn't look like `${...}`
- Audit logging via `AppendFetchAudit` for every fetch

## Backwards compatibility

Fully backwards-compatible:

- `openshell.profiles` is a new optional field. Omitting it changes nothing.
- `providers` keeps its type (`[]string`). Existing local-name entries work
  unchanged — `IsURL()` returns false, they pass through to existing
  `LoadProviderDefs` + `EnsureProvider` flow.
- No schema version bump. Older harnesses without `openshell.profiles` or URL-referenced
  providers behave identically.

## Consequences

- Users can now share harnesses that bundle providers and profiles with remote
  repositories, enabling full harness portability.
- A base harness can declare both its own providers and profiles, and child harnesses
  inherit them, simplifying composition and reducing duplication.
- Provider and profile definitions can be maintained in shared repositories and
  referenced by multiple organizations, reducing copy-paste and improving
  maintainability.
- The validation layer ensures referential integrity: every provider's type must
  match a declared profile, preventing broken harnesses.
- No new attack surface — same fetch + cache + audit pipeline as ADR 0038.
