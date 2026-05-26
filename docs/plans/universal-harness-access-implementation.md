# Implementation Plan: ADR-0038 Universal Harness Access via URLs

## Context

ADR-0038 makes harness declarative resources (agents, skills, policies) referenceable via HTTPS URLs with mandatory SHA256 integrity hashes, enabling community sharing and cross-org composition. Executable resources (scripts) remain local-only for security. The ADR is at `docs/ADRs/0038-universal-harness-access.md` with a detailed design at `docs/plans/universal-harness-access.md`.

This plan covers **Phase 1 (MVP)**: read-only, single-level URL support. No transitive resolution, no lock files, no runtime fetching. Existing harnesses with only local paths continue to work identically — zero behavioral change.

## PR Dependency Graph

```
PR 1 (URL utils) ────┬──> PR 5 (schema) ──> PR 7 (resolver) ──> PR 8 (CLI integration)
                      │                        ↑
PR 2 (fetcher) ──> PR 3 (cache) ──────────────┘
                                               ↑
PR 4 (audit) ─────────────────────────────────┘

PR 6 (gitignore)  [no dependencies, nothing depends on it]
```

PRs 1, 2, 4, and 6 have no dependencies and can be developed/merged in parallel.

---

## PR 1: URL detection and integrity hash parsing

**Scope:** Pure utility functions with no callers. Zero risk to existing behavior.

**Create `internal/harness/url.go`:**
- `IsURL(s string) bool` — true for valid HTTPS URLs. Rejects empty host, userinfo, non-HTTPS schemes. Uses `net/url.Parse` with additional guards.
- `IsAbsPath(s string) bool` — delegates to `filepath.IsAbs`.
- `IsRelPath(s string) bool` — `!IsURL(s) && !IsAbsPath(s)`.
- `ParseIntegrityHash(rawURL string) (cleanURL, hash string, hasHash bool)` — extracts `#sha256=...` fragment. Validates hash is exactly 64 lowercase hex chars (prevents path traversal via crafted hashes).

**Create `internal/harness/url_test.go`:**
- `IsURL`: valid HTTPS, HTTP rejected, `file://` rejected, empty host, userinfo, malformed URLs
- `ParseIntegrityHash`: valid extraction, missing fragment, wrong length, uppercase hex rejected, URL reconstruction

**After merge:** Utility functions available. No callers. All existing tests pass.

---

## PR 2: SSRF-hardened HTTP fetcher

**Scope:** New standalone package. No callers. Zero risk.

**Create `internal/fetch/fetch.go`:**
- `FetchPolicy` struct: `AllowedDomains`, `MaxSizeBytes` (10MB), `Timeout` (30s), `Offline`
- `FetchURL(ctx, rawURL, policy) ([]byte, error)` — HTTPS-only, domain allowlist, pre-request DNS resolution, internal IP rejection, DNS rebinding protection via custom `DialContext` that pins to pre-validated IPs, no-redirect policy, size limiting, double-encoding rejection (`%25`)
- `isAllowedDomain(hostname, allowed) bool` — exact match + explicit wildcard (`*.example.com`)
- Reuse IP-checking logic from `internal/security/ssrf.go:checkIP()` — either import directly or extract to a shared internal package (`internal/netutil/`) to avoid circular dependencies. The existing `checkIP` already covers loopback, private, link-local, multicast, unspecified, CGNAT. Add benchmark testing range (`198.18.0.0/15`) and IPv4-mapped IPv6 normalization.
- `ComputeSHA256(data []byte) string`

**Create `internal/fetch/fetch_test.go`:**
- Tests using `httptest.NewServer` (TLS): domain allowlist, internal IP rejection, no redirects, size limits, timeouts, offline mode, double-encoding rejection

**Key reuse:** `internal/security/ssrf.go` has `checkIP()` with CGNAT, documentation ranges, etc. Extract shared IP-checking to avoid duplication.

**After merge:** Standalone SSRF-protected HTTP fetcher. No callers.

---

## PR 3: Content-addressed cache

**Scope:** Adds cache to `internal/fetch/`. No callers. Zero risk.

**Create `internal/fetch/cache.go`:**
- `CacheEntry` struct: URL, FetchTime, SHA256 (JSON-serializable)
- `CachePath(workspaceRoot, hash) string` — `.fullsend-cache/resources/sha256/<hash>/`
- `CacheGet(workspaceRoot, hash) ([]byte, *CacheEntry, error)` — returns `(nil, nil, nil)` on miss. **Re-verifies integrity** on every read: `SHA256(content) == entry.SHA256`.
- `CachePut(workspaceRoot, url, content) error` — **atomic writes** (write to temp file, `os.Rename`). Restrictive permissions (0700 dirs, 0600 files).

**Create `internal/fetch/cache_test.go`:**
- Round-trip, cache miss, partial entry handling, integrity re-verification, concurrent writes, same-content dedup

**Depends on:** PR 2 (same package, uses `ComputeSHA256`)

**After merge:** Content-addressed cache. No callers.

---

## PR 4: Fetch audit logging

**Scope:** Audit log utilities following the existing `security.AppendFinding` pattern. No callers. Zero risk.

**Create `internal/fetch/audit.go`** (diverges from design doc's `internal/audit/fetch_log.go` — co-locating audit with fetch avoids a single-file package; `internal/audit/` can be introduced later if audit logging grows beyond fetch):
- `FetchAuditEntry` struct: TraceID, FetchTime, URL, SHA256, FetchType (static/cache_hit), AllowedBy, CacheHit
- `AppendFetchAudit(logPath string, entry FetchAuditEntry) error` — appends JSONL line, mirrors `security.AppendFinding` in `internal/security/trace.go`

**Create `internal/fetch/audit_test.go`:**
- Append, read back, JSONL format, directory creation

**Depends on:** Nothing (can merge in parallel with PRs 1-3)

**After merge:** Audit log utilities. No callers.

---

## PR 5: Schema extensions — harness + org config allowlists

**Scope:** Backward-compatible additions. New optional `omitempty` fields. Existing harnesses work identically.

**Modify `internal/harness/harness.go`:**
- Add `AllowedRemoteResources []string` with `yaml:"allowed_remote_resources,omitempty"` to `Harness` struct (after existing fields)
- Add `ValidateAllowedRemoteResources(orgAllowlist []string) error` — new method (does NOT modify existing `Validate()` to preserve `Load()` behavior). Validates entries are HTTPS URLs with trailing `/`, validates harness entries are subset of org allowlist.
- Add `ValidateResourceTypes() error` — new method. Rejects URLs in executable fields (PreScript, PostScript, ValidationLoop.Script, HostFiles[].Src, APIServers). Requires integrity hash on URLs in declarative fields (Agent, Policy, Skills). Uses `IsURL`/`ParseIntegrityHash` from PR 1.
- Add `MatchesAllowedPrefix(rawURL string) bool` — URL canonicalization, double-encoding rejection, prefix matching against `AllowedRemoteResources`

**Modify `internal/config/config.go`:**
- Add `AllowedRemoteResources []string` with `yaml:"allowed_remote_resources,omitempty"` to `OrgConfig` struct

**Update tests in `internal/harness/harness_test.go`:**
- Load harness with/without `allowed_remote_resources` (backward compat)
- `ValidateAllowedRemoteResources`: valid entries, non-HTTPS rejected, missing trailing `/`, not in org allowlist
- `ValidateResourceTypes`: URLs in script fields rejected, URLs in declarative fields accepted, missing hash rejected
- `MatchesAllowedPrefix`: matching/non-matching URLs, double-encoding, normalization

**Update tests in `internal/config/config_test.go`:**
- Parse/marshal org config with `allowed_remote_resources`, omitempty when empty

**Depends on:** PR 1

**After merge:** Schema accepts `allowed_remote_resources`. Validation methods exist but aren't called from the run flow yet.

---

## PR 6: `.fullsend-cache` gitignore entry

**Scope:** Trivial. Prevents cache artifacts from being committed.

**Modify default `.gitignore`** template (wherever `.fullsend` repo creation generates the gitignore):
- Add `.fullsend-cache/`

**Depends on:** Nothing

**After merge:** Cache directory excluded from version control.

---

## PR 7: Resource resolver

**Scope:** New package that orchestrates fetch + cache + validation + audit for URL-referenced resources. This is the core logic.

**Create `internal/resolve/resolve.go`:**
- `ResolvedHarness` struct: wraps `*harness.Harness` + resolved paths (AgentPath, PolicyPath, SkillPaths, Dependencies)
- `Dependency` struct: URL, LocalPath (cache path), SHA256, FetchedAt
- `ResolveOpts` struct: WorkspaceRoot, FetchPolicy, OrgAllowlist, TraceID, AuditLogPath
- `ResolveHarness(ctx, h *harness.Harness, opts) (*ResolvedHarness, error)`:
  - For each declarative field (Agent, Policy, Skills):
    - Local path: return as-is
    - URL: validate against `AllowedRemoteResources` → extract/require integrity hash → check cache (with re-verification) → if miss and not offline: `fetch.FetchURL` → verify hash → security scan (InputPipeline, remote threshold) → `CachePut` → `AppendFetchAudit` → return cache content path
  - Phase 1: single-level only (no transitive deps)

**Create `internal/resolve/resolve_test.go`:**
- Tests using `httptest.NewTLSServer`: local pass-through, URL fetch+cache, cache hit, hash mismatch, URL not in allowlist, missing hash, offline+miss, offline+hit, security scan failure, mixed harness, audit entries

**Depends on:** PR 1, PR 2, PR 3, PR 4, PR 5

**After merge:** Complete resolution logic. Not wired into CLI yet.

---

## PR 8: CLI integration — wire into `fullsend run`

**Scope:** The only PR modifying existing code flow. Minimal diff.

**Modify `internal/cli/run.go`:**
- Add `--offline` flag to run command
- In `runAgent()`, **between** `h.ResolveRelativeTo(absFullsendDir)` and `h.ValidateFilesExist()`:
  1. `h.ValidateResourceTypes()` — reject URLs in script fields, require hashes (no-op for local-only harnesses)
  2. If harness has any URL references: load org config, call `h.ValidateAllowedRemoteResources(orgCfg.AllowedRemoteResources)`
  3. `resolve.ResolveHarness(ctx, h, opts)` — fetch/cache URLs (no-op if all local)
  4. Replace harness fields with resolved paths: `h.Agent = resolved.AgentPath`, etc.
  5. `h.ValidateFilesExist()` then validates resolved paths (cache files or local files)

**Key design:** For local-only harnesses, steps 1-3 are no-ops (no URLs detected, no fetches). Zero behavioral change for existing users.

**Modify `internal/cli/run_test.go`:**
- `--offline` flag registration test

**Depends on:** PR 5, PR 7

**After merge:** `fullsend run` supports URL-referenced declarative resources end-to-end. Example working harness:
```yaml
agent: https://raw.githubusercontent.com/fullsend-ai/library/8cd3799.../agents/code.md#sha256=abc123...
policy: policies/local-policy.yaml
skills:
  - skills/local-skill
  - https://raw.githubusercontent.com/fullsend-ai/library/8cd3799.../skills/rust/SKILL.md#sha256=def456...
allowed_remote_resources:
  - https://raw.githubusercontent.com/fullsend-ai/library/
```

---

## Future Phases (high-level)

### Phase 2: Transitive dependency resolution (2-3 PRs)
- Parse `dependencies:` field from SKILL.md YAML frontmatter
- Recursive resolution with cycle detection (visited set), depth limit (10), breadth limit (50)
- Relative URL resolution for URL-fetched resources (RFC 3986 base URL semantics)

### Phase 3: Lock files (2 PRs)
- `internal/lock/` package: LockFile struct, parse/generate/write
- `fullsend lock <harness>` CLI subcommand; prefer lock file entries in resolver

### Phase 4: Runtime dependency loading (2 PRs)
- `allow_runtime_fetch` + `max_runtime_fetches` harness fields
- `fullsend-fetch-skill` binary in sandbox, Unix socket to runner, rate limiting

---

## Verification

After PR 8 merges, verify Phase 1 end-to-end:

1. **Unit tests:** `make go-test` — all new and existing tests pass
2. **Lint:** `make lint` passes
3. **Local-only harness (regression):** Run an existing harness with only local paths — no behavioral change
4. **URL harness:** Create a test harness referencing a URL-hosted agent/skill with `#sha256=...` hash and matching `allowed_remote_resources` — verify fetch, cache, and execution
5. **Hash mismatch:** Modify the hash — verify rejection with clear error
6. **Missing hash:** Remove `#sha256=...` — verify rejection
7. **Domain not in allowlist:** Use a URL from an unallowed domain — verify rejection
8. **Script URL rejection:** Set `pre_script: https://...` — verify rejection with "must be local" error
9. **Offline mode:** Run `fullsend run --offline` with a URL-referencing harness — verify cache miss fails, cache hit succeeds
10. **Audit log:** Verify `.fullsend-cache/` populated and fetch audit JSONL entries written
