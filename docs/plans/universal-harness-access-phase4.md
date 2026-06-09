# Implementation Plan: Phase 4 — Runtime Dependency Loading

## Context

Phases 1-3 of ADR-0038 are fully shipped. Phase 1 (8 PRs) added URL detection, SSRF-hardened fetching, content-addressed caching, audit logging, schema extensions, resource resolution, and CLI integration. Phase 2 (3 PRs) added skill frontmatter parsing, recursive transitive dependency resolution with cycle/depth/breadth limits, and relative URL resolution. Phase 3 (2 PRs) added the `internal/lock/` package, `fullsend lock` CLI subcommand, and lock-aware `fullsend run`.

All three phases resolve dependencies **statically** — before the sandbox is created and the agent starts execution. Phase 4 adds **runtime dependency loading**: agents running inside the sandbox can request additional skills mid-run. The runner validates, fetches, and uploads the skill while the agent is executing.

Design details are in `docs/plans/universal-harness-access.md` (sections "Runtime Dependency Loading" and "Access Policy Model — Phase 2: Runtime fetch with policy"). The ADR is at `docs/ADRs/0038-universal-harness-access.md`.

> **Scope note:** The ADR originally specified "Unix socket to runner" for IPC. Investigation of the OpenShell sandbox model shows this is not feasible — there is no shared filesystem or socket path between host and container. This plan uses file-based IPC via `openshell sandbox exec` and `sandbox upload`, which is the same communication mechanism used by the OIDC token refresh goroutine (`runOIDCRefresh` in `internal/cli/run.go`).

## PR Dependency Graph

```
PR 1 (schema + ResolveSkillURL) ──> PR 2 (in-sandbox script + bootstrap) ──> PR 3 (handler + CLI wiring)
```

PRs are strictly sequential. Each is independently reviewable and safe to merge alone — earlier PRs introduce new code with no callers until the subsequent PR wires them in.

---

## IPC Architecture: File-Based Polling via OpenShell

The OpenShell sandbox model communicates exclusively via CLI commands: `sandbox exec`, `sandbox upload`, `sandbox download`. There is no shared filesystem, no port forwarding, and no socket path between host and container. Communication is always **runner-initiated** — the sandbox cannot push to the host.

The runtime fetch IPC works as follows:

```
┌─────────────────────────────────────────────────────────┐
│ Sandbox                                                 │
│                                                         │
│  Agent calls:                                          │
│  $ fullsend-fetch-skill https://...#sha256=abc123      │
│                                                         │
│  fullsend-fetch-skill (shell script):                  │
│    1. Write request JSON to                            │
│       /tmp/fullsend-fetch-requests/<uuid>.json         │
│    2. Poll for response at                             │
│       /tmp/fullsend-fetch-responses/<uuid>.json        │
│    3. Output skill path to stdout on success           │
└─────────────────────────────────────────────────────────┘
                    ↕ openshell sandbox exec / upload
┌─────────────────────────────────────────────────────────┐
│ Runner (host)                                           │
│                                                         │
│  runtimefetch.Run() goroutine:                         │
│    1. Poll: sandbox.Exec("ls /tmp/.../requests/")      │
│    2. Read: sandbox.Exec("cat /tmp/.../req.json")      │
│    3. Validate URL, hash, allowlist, rate limit         │
│    4. resolve.ResolveSkillURL() → fetch/cache/verify   │
│    5. sandbox.UploadFile() → skill into sandbox        │
│    6. sandbox.Exec("echo '...' > .../resp.json")       │
└─────────────────────────────────────────────────────────┘
```

This mirrors the proven **OIDC token refresh** pattern (`runOIDCRefresh` at `internal/cli/run.go:1221`), which calls `sandbox.Upload` concurrently while the agent runs via `ExecStreamReader`. The runner poll interval is 500ms, giving ~1s average latency for request detection plus fetch/upload time.

---

## PR 1: Schema extensions + exported runtime resolver

**Scope:** Backward-compatible harness schema additions and a new exported function in `internal/resolve/` for single-URL resolution. No callers. Zero risk to existing behavior.

### Changes to `internal/harness/harness.go`

**Add to `Harness` struct** (after `AllowedRemoteResources`):

```go
AllowRuntimeFetch  bool `yaml:"allow_runtime_fetch,omitempty"`
MaxRuntimeFetches  int  `yaml:"max_runtime_fetches,omitempty"`
```

**Add to `Validate()`:**

```go
if h.MaxRuntimeFetches < 0 {
    return fmt.Errorf("max_runtime_fetches must be non-negative, got %d", h.MaxRuntimeFetches)
}
if !h.AllowRuntimeFetch && h.MaxRuntimeFetches != 0 {
    return fmt.Errorf("max_runtime_fetches requires allow_runtime_fetch: true")
}
```

When `AllowRuntimeFetch` is true and `MaxRuntimeFetches` is 0 (the zero value), the default of 10 is applied at point of use (in PR 3), not in Validate — the zero value must remain distinguishable from "explicitly set to 0 to disable."

### Changes to `internal/resolve/resolve.go`

Add new exported function:

```go
// ResolveSkillURL resolves a single URL-referenced skill for runtime fetch.
// It validates the URL against the harness allowlist, checks/fetches from
// cache, verifies the integrity hash, and logs an audit entry with
// FetchType "runtime". No transitive resolution — runtime-fetched skills
// are leaf nodes.
//
// Returns the resolved Dependency and the local cache path containing the
// fetched content. The caller is responsible for uploading the content to
// the sandbox.
func ResolveSkillURL(ctx context.Context, rawURL string, h *harness.Harness, opts ResolveOpts) (Dependency, string, error)
```

Implementation follows the same validation/fetch/cache/audit sequence as the existing unexported `resolveURL` (lines 150-253), but without:
- `resolveState` — no cycle/diamond tracking (single URL, not a graph)
- `resolveTransitiveDeps` — runtime skills are leaf nodes
- Depth/breadth counters — not applicable to single-URL resolution

The `FetchType` on the audit entry is `"runtime"` instead of `"static"`. The `Field` on the returned `Dependency` is `"runtime"`.

### Changes to `internal/harness/harness_test.go`

- Load harness with `allow_runtime_fetch: true` + `max_runtime_fetches: 5`: valid.
- Load harness with `allow_runtime_fetch: false` + `max_runtime_fetches: 5`: validation error.
- Load harness with `allow_runtime_fetch: true` + `max_runtime_fetches: -1`: validation error.
- Load harness with `allow_runtime_fetch: true` + no `max_runtime_fetches`: valid (zero value).
- Backward compatibility: harness without these fields parses and validates identically.

### Changes to `internal/resolve/resolve_test.go`

Tests using `httptest.NewTLSServer` (same pattern as existing `ResolveHarness` tests):

- **Valid fetch:** URL with hash in allowlist → fetch, cache, return path.
- **Cache hit:** Second call with same hash → return cached path, no HTTP request.
- **Hash mismatch:** Server returns content with wrong hash → error contains "integrity check failed".
- **URL not in allowlist:** URL outside `allowed_remote_resources` → error contains "not in allowed_remote_resources".
- **Missing hash:** URL without `#sha256=...` → error contains "must include #sha256=".
- **Audit entry:** Verify `FetchType` is `"runtime"` and `AllowedBy` is set.
- **Offline + cache miss:** `FetchPolicy.Offline = true`, no cache entry → error.
- **Offline + cache hit:** `FetchPolicy.Offline = true`, content in cache → success.

**Depends on:** Nothing

**After merge:** New harness fields accepted. `ResolveSkillURL` available. No callers. All existing tests pass.

---

## PR 2: In-sandbox `fullsend-fetch-skill` script + bootstrap wiring

**Scope:** A shell script embedded via `go:embed` and conditionally deployed at bootstrap time. The script is inert until PR 3 wires the runner-side handler.

### Create `internal/scaffold/fullsend-repo/scripts/fullsend-fetch-skill`

A Bash script following the `fullsend-check-output` precedent (shell script deployed to `/sandbox/bin/`):

```bash
#!/usr/bin/env bash
set -euo pipefail

# fullsend-fetch-skill — Request a skill fetch from the runner.
#
# Usage: fullsend-fetch-skill <url>#sha256=<hash>
#
# Communicates with the runner via file-based IPC:
#   1. Writes request JSON to /tmp/fullsend-fetch-requests/<id>.json
#   2. Polls for response at /tmp/fullsend-fetch-responses/<id>.json
#   3. Outputs the local skill path on success, error on failure
#
# Exit codes: 0 = success (path on stdout), 1 = failure (error on stderr)

REQUEST_DIR="/tmp/fullsend-fetch-requests"
RESPONSE_DIR="/tmp/fullsend-fetch-responses"
MAX_WAIT_SECONDS=120

if [[ $# -ne 1 ]]; then
  echo "Usage: fullsend-fetch-skill <url>#sha256=<hash>" >&2
  exit 1
fi

URL="$1"

if [[ ! "$URL" =~ \#sha256= ]]; then
  echo "ERROR: URL must include #sha256=... integrity hash" >&2
  exit 1
fi

# Generate request ID.
if [[ -f /proc/sys/kernel/random/uuid ]]; then
  REQUEST_ID=$(cat /proc/sys/kernel/random/uuid)
else
  REQUEST_ID="req-$(date +%s%N)-$$"
fi

mkdir -p "$REQUEST_DIR" "$RESPONSE_DIR"

# Write request — use printf to avoid shell expansion of URL contents.
printf '{"request_id":"%s","url":"%s"}\n' "$REQUEST_ID" "$URL" \
  > "$REQUEST_DIR/$REQUEST_ID.json"

# Poll for response.
ELAPSED=0
while [[ $ELAPSED -lt $MAX_WAIT_SECONDS ]]; do
  if [[ -f "$RESPONSE_DIR/$REQUEST_ID.json" ]]; then
    RESPONSE=$(cat "$RESPONSE_DIR/$REQUEST_ID.json")
    rm -f "$REQUEST_DIR/$REQUEST_ID.json" "$RESPONSE_DIR/$REQUEST_ID.json"

    ERROR=$(printf '%s' "$RESPONSE" \
      | grep -o '"error":"[^"]*"' | head -1 \
      | sed 's/"error":"//;s/"$//' || true)
    if [[ -n "$ERROR" ]]; then
      echo "ERROR: $ERROR" >&2
      exit 1
    fi

    SKILL_PATH=$(printf '%s' "$RESPONSE" \
      | grep -o '"skill_path":"[^"]*"' | head -1 \
      | sed 's/"skill_path":"//;s/"$//')
    if [[ -z "$SKILL_PATH" ]]; then
      echo "ERROR: malformed response from runner" >&2
      exit 1
    fi

    echo "$SKILL_PATH"
    exit 0
  fi

  sleep 0.2
  ELAPSED=$((ELAPSED + 1))
done

rm -f "$REQUEST_DIR/$REQUEST_ID.json"
echo "ERROR: timeout waiting for runner response after ${MAX_WAIT_SECONDS}s" >&2
exit 1
```

Design notes:
- Uses only basic shell utilities (`grep`, `sed`, `cat`, `mkdir`, `sleep`, `date`, `printf`) available in all sandbox images.
- JSON parsing is intentionally minimal (`grep -o` for field extraction). Avoids requiring `jq`.
- 120-second timeout is generous. Cached fetches complete in under 1 second; network fetches in under 35 seconds.
- Cleanup removes both request and response files to prevent stale files accumulating.

### Modify `internal/scaffold/scaffold.go`

Add to `executableFiles` map:

```go
"scripts/fullsend-fetch-skill": {},
```

### Modify `internal/cli/run.go` (`bootstrapCommon`)

After the `fullsend-check-output` deployment block (line ~972), add conditional deployment of `fullsend-fetch-skill`. Only deploy when the harness has `AllowRuntimeFetch: true`:

```go
if h.AllowRuntimeFetch {
    fetchScript, err := scaffold.FullsendRepoFile("scripts/fullsend-fetch-skill")
    if err != nil {
        fmt.Fprintf(os.Stderr, "WARNING: could not load fetch-skill script: %v\n", err)
    } else if err := func() error {
        tmpFetch, err := os.CreateTemp("", "fullsend-fetch-skill-*")
        if err != nil {
            return fmt.Errorf("creating temp file: %w", err)
        }
        defer os.Remove(tmpFetch.Name())
        if _, err := tmpFetch.Write(fetchScript); err != nil {
            tmpFetch.Close()
            return fmt.Errorf("writing temp file: %w", err)
        }
        tmpFetch.Close()
        remoteBin := fmt.Sprintf("%s/bin/fullsend-fetch-skill", sandbox.SandboxWorkspace)
        if err := sandbox.UploadFile(sandboxName, tmpFetch.Name(), remoteBin); err != nil {
            return fmt.Errorf("uploading to sandbox: %w", err)
        }
        if _, _, _, err := sandbox.Exec(sandboxName, fmt.Sprintf("chmod +x %s", remoteBin), 10*time.Second); err != nil {
            return fmt.Errorf("chmod: %w", err)
        }
        return nil
    }(); err != nil {
        fmt.Fprintf(os.Stderr, "WARNING: could not install fetch-skill script: %v\n", err)
    }

    // Create request/response directories.
    mkdirCmd := "mkdir -p /tmp/fullsend-fetch-requests /tmp/fullsend-fetch-responses"
    if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
        fmt.Fprintf(os.Stderr, "WARNING: could not create fetch request dirs: %v\n", err)
    }
}
```

### Tests

**`internal/scaffold/scaffold_test.go`:**
- The existing `TestFileModeMatchesFilesystem` test will automatically verify the new entry in `executableFiles`.

**`internal/cli/run_test.go`:**
- Verify `fullsend-fetch-skill` is listed in bootstrap steps when `AllowRuntimeFetch: true`.
- Verify it is skipped when `AllowRuntimeFetch: false` or absent.

**Depends on:** PR 1 (for `AllowRuntimeFetch` field on `Harness`)

**After merge:** Script embedded and deployed when enabled. Not yet functional — no runner-side handler.

---

## PR 3: Runtime fetch handler goroutine + CLI wiring

**Scope:** New `internal/runtimefetch/` package and goroutine wiring in `runAgent`. This is the only PR that enables the feature end-to-end.

### Create `internal/runtimefetch/handler.go`

New package to keep the handler logic isolated from the main CLI flow. Follows the separation-of-concerns pattern: `internal/fetch/` for HTTP, `internal/resolve/` for resolution, `internal/runtimefetch/` for runtime handling.

```go
package runtimefetch

const (
    SandboxRequestDir   = "/tmp/fullsend-fetch-requests"
    SandboxResponseDir  = "/tmp/fullsend-fetch-responses"
    DefaultPollInterval = 500 * time.Millisecond
    DefaultMaxFetches   = 10
)

// Request is the JSON structure written by fullsend-fetch-skill in the sandbox.
type Request struct {
    RequestID string `json:"request_id"`
    URL       string `json:"url"`
}

// Response is the JSON structure written back to the sandbox by the runner.
type Response struct {
    RequestID string `json:"request_id"`
    SkillPath string `json:"skill_path,omitempty"`
    Error     string `json:"error,omitempty"`
}

// HandlerOpts configures the runtime fetch handler.
type HandlerOpts struct {
    SandboxName  string
    Harness      *harness.Harness
    ResolveOpts  resolve.ResolveOpts
    MaxFetches   int
    PollInterval time.Duration
    Printer      *ui.Printer
}

// Run starts the runtime fetch handler. It polls the sandbox for fetch
// requests, processes them, and writes responses. It blocks until ctx is
// cancelled. Call in a goroutine.
func Run(ctx context.Context, opts HandlerOpts)
```

**`processRequests`** function:
1. List pending request files via `sandbox.Exec("ls /tmp/fullsend-fetch-requests/ 2>/dev/null || true")`.
2. For each `.json` file, call `processOneRequest`.

**`processOneRequest`** function:
1. Read request file via `sandbox.Exec("cat ...")`.
2. Delete request file immediately via `sandbox.Exec("rm -f ...")` to prevent reprocessing.
3. Parse request JSON. If malformed, write error response and return.
4. Rate limit check: if `fetchCount >= maxFetches`, write error response `"rate limit exceeded"`.
5. Call `resolve.ResolveSkillURL(ctx, req.URL, opts.Harness, opts.ResolveOpts)`.
6. On error, write error response and return.
7. Increment fetch counter.
8. Derive skill name from URL via `extractSkillName(req.URL)`.
9. Create skill directory in sandbox: `sandbox.Exec("mkdir -p $CLAUDE_CONFIG_DIR/skills/<name>")`.
10. Upload fetched content: `sandbox.UploadFile(sandboxName, localPath, remotePath)`.
11. Write success response with `skill_path`.

**`writeResponse`** function:
- Marshal `Response` to JSON.
- Write via `sandbox.Exec("printf '%s' '...' > /tmp/fullsend-fetch-responses/<id>.json")`.

**`extractSkillName`** function:
- Parse URL, strip `#sha256=...` fragment.
- If last path component is `SKILL.md` (case-insensitive), use its parent directory name.
- Otherwise, use a hash prefix: `"runtime-" + sha256(url)[:12]`.
- Sanitize: replace non-alphanumeric (except `-_`) characters.

### Modify `internal/cli/run.go` (`runAgent`)

Wire the handler goroutine after the OIDC refresh setup (line ~678), using the same `context + WaitGroup` pattern:

```go
runtimeFetchCtx, runtimeFetchCancel := context.WithCancel(context.Background())
var runtimeFetchWg sync.WaitGroup
if h.AllowRuntimeFetch {
    maxFetches := h.MaxRuntimeFetches
    if maxFetches == 0 {
        maxFetches = runtimefetch.DefaultMaxFetches
    }
    runtimeFetchWg.Add(1)
    go func() {
        defer runtimeFetchWg.Done()
        runtimefetch.Run(runtimeFetchCtx, runtimefetch.HandlerOpts{
            SandboxName: sandboxName,
            Harness:     h,
            ResolveOpts: resolve.ResolveOpts{
                WorkspaceRoot: workspaceRoot,
                FetchPolicy:   fetchPolicy,
                TraceID:       traceID,
                AuditLogPath:  auditLogPath,
            },
            MaxFetches:   maxFetches,
            PollInterval: runtimefetch.DefaultPollInterval,
            Printer:      printer,
        })
    }()
    printer.StepDone(fmt.Sprintf("Runtime skill fetch enabled (max %d)", maxFetches))
}
defer func() {
    runtimeFetchCancel()
    runtimeFetchWg.Wait()
}()
```

### Security scanning of runtime-fetched skills

Runtime-fetched skills arrive **after** the pre-agent security scan has run. The same security guarantees apply through different mechanisms:

1. URL must match `allowed_remote_resources` (org-controlled allowlist).
2. Content must match the `#sha256=...` hash (integrity pinning — attacker cannot silently change content).
3. Content fetched via SSRF-hardened `fetch.FetchURL` (no internal IPs, no redirects, HTTPS-only).
4. All fetches cached and audited.

A future enhancement could add host-side `security.InputPipeline().Scan()` of fetched content before uploading to the sandbox. This is not included in Phase 4 to keep PR scope manageable.

### Create `internal/runtimefetch/handler_test.go`

Test cases:

- **Successful fetch:** Mock `sandbox.Exec` to return a valid request JSON; mock `sandbox.UploadFile`; verify response written with `skill_path`.
- **Rate limit enforcement:** Set `maxFetches=2`, send 3 requests. Verify third gets `"rate limit exceeded"` error.
- **URL not in allowlist:** Request with URL outside `allowed_remote_resources` → error response.
- **Missing hash:** Request with URL without `#sha256=...` → error response.
- **Hash mismatch:** TLS server returns wrong content → error response with "integrity check failed".
- **Malformed request JSON:** Garbage content in request file → error response with "malformed".
- **Context cancellation:** Cancel context → goroutine exits cleanly, no panics.
- **Empty request directory:** `ls` returns empty → no action.
- **Audit entry:** Verify `FetchType: "runtime"` in audit entries via `ResolveSkillURL`.
- **`extractSkillName`:** URL with `/SKILL.md` → parent dir name; URL without → hash prefix; special characters → sanitized.
- **Cache hit:** Skill already in cache → no HTTP request, success response.
- **Concurrent requests:** Multiple request files in same poll cycle → all processed sequentially, rate limit applied across all.

**Depends on:** PR 1 (`AllowRuntimeFetch`, `MaxRuntimeFetches`, `ResolveSkillURL`), PR 2 (`fullsend-fetch-skill` script in sandbox)

**After merge:** Runtime skill fetching works end-to-end.

---

## Security Considerations

### Rate limiting

Rate limiting is enforced exclusively on the runner side. The in-sandbox script cannot circumvent the limit because:
- The sandbox has no network access to fetch skills directly (sandbox network policy).
- The script can only write request files. The runner decides whether to honor them.
- The runner maintains an atomic counter. After `max_runtime_fetches`, all requests get error responses.
- Even if an attacker floods the request directory with many files, the runner processes them sequentially and stops at the limit.

### Request forgery

An attacker-controlled process in the sandbox could craft request files. Mitigations:
- All URLs validated against `allowed_remote_resources` — same as static resolution.
- All URLs require `#sha256=...` integrity hashes — same as static resolution.
- Request files must be valid JSON with `request_id` and `url` fields. Malformed requests are rejected.
- Audit logging makes all runtime fetches visible in post-run analysis.

### Agent-directed fetch of malicious content

An attacker who controls the issue body (or any agent input) could trick the agent into calling `fullsend-fetch-skill` with a URL that serves adversarial content.

Mitigations:
- The URL must match `allowed_remote_resources` prefixes. The attacker cannot fetch from arbitrary domains.
- The URL must include a `#sha256=...` hash that matches the content. The attacker must know the hash of the content they want the agent to fetch.
- Combined: exploiting this requires compromising an allowed source AND knowing its content hash.

### Denial of service via request flooding

An attacker-controlled process floods `/tmp/fullsend-fetch-requests/` with thousands of request files.

Mitigations:
- `max_runtime_fetches` caps total successful fetches. After the limit, all requests get error responses (minimal processing).
- The runner processes requests sequentially within each poll cycle. No parallelism explosion.
- Each request is deleted immediately after being read, preventing reprocessing.

### Audit trail

All runtime fetches produce audit entries with `fetch_type: "runtime"`, making them distinguishable from static fetches in post-run analysis. The audit entry includes URL, hash, cache hit status, and the `allowed_by` prefix that matched.

---

## Files Summary

| File | PR | Action | Description |
|------|----|--------|-------------|
| `internal/harness/harness.go` | 1 | **Modify** | Add `AllowRuntimeFetch`, `MaxRuntimeFetches` fields + validation |
| `internal/harness/harness_test.go` | 1 | **Modify** | Tests for new fields |
| `internal/resolve/resolve.go` | 1 | **Modify** | Add exported `ResolveSkillURL` function |
| `internal/resolve/resolve_test.go` | 1 | **Modify** | Tests for `ResolveSkillURL` |
| `internal/scaffold/fullsend-repo/scripts/fullsend-fetch-skill` | 2 | **Create** | In-sandbox shell script |
| `internal/scaffold/scaffold.go` | 2 | **Modify** | Add to `executableFiles` map |
| `internal/cli/run.go` | 2, 3 | **Modify** | Bootstrap deployment (PR 2), handler goroutine wiring (PR 3) |
| `internal/runtimefetch/handler.go` | 3 | **Create** | Runtime fetch handler goroutine |
| `internal/runtimefetch/handler_test.go` | 3 | **Create** | Handler tests |

---

## Verification

After PR 3 merges, verify Phase 4 end-to-end:

1. **Unit tests:** `make go-test` — all new and existing tests pass.
2. **Lint:** `make lint` passes.
3. **Local-only harness (regression):** Run an existing harness without `allow_runtime_fetch` — no polling goroutine, no behavioral change.
4. **Runtime fetch disabled by default:** Confirm `fullsend-fetch-skill` is not deployed and no goroutine starts when `allow_runtime_fetch` is absent or false.
5. **Runtime fetch end-to-end:** Create a harness with `allow_runtime_fetch: true`, `max_runtime_fetches: 5`, and `allowed_remote_resources`. Run an agent whose prompt instructs it to call `fullsend-fetch-skill <url>#sha256=...`. Verify: skill is fetched, uploaded to sandbox, path returned to agent, audit entry written with `fetch_type: "runtime"`.
6. **Rate limit:** Set `max_runtime_fetches: 2`. Have the agent attempt 3 runtime fetches. Verify the third returns "rate limit exceeded".
7. **URL not in allowlist:** Have the agent fetch a URL outside `allowed_remote_resources`. Verify rejection.
8. **Hash mismatch:** Have the agent fetch a URL with wrong hash. Verify rejection.
9. **Missing hash:** Have the agent fetch a URL without `#sha256=...`. Verify the in-sandbox script rejects it before writing a request.
10. **Audit log:** Verify runtime fetch entries appear alongside static fetch entries with `fetch_type: "runtime"`.
11. **Offline mode:** Run with `--offline` and `allow_runtime_fetch: true`. Verify runtime fetch returns cache hits but fails on cache misses.
12. **Timeout:** Verify the in-sandbox script times out cleanly after 120 seconds if the runner never responds.
13. **No network in sandbox:** Verify the agent cannot bypass `fullsend-fetch-skill` by directly fetching URLs from inside the sandbox (sandbox network policy blocks this).

---

## Future Enhancements (not in Phase 4)

1. **Host-side security scan of runtime-fetched skills:** Run `security.InputPipeline().Scan()` on fetched content before uploading to sandbox. Adds defense-in-depth for prompt injection detection.
2. **Transitive resolution of runtime-fetched skills:** If a runtime-fetched skill declares `dependencies:` in its frontmatter, resolve them recursively. Currently runtime skills are leaf nodes.
3. **Anomaly detection:** Alert when runtime fetch patterns deviate from baseline (e.g., fetches from a new domain or unusual volume).
4. **Pre-warming:** Pre-cache skills listed in `allowed_remote_resources` at bootstrap time so runtime fetches hit cache.

---

## Future Phases (unchanged from Phase 2 plan)

Phase 4 is the final phase of the original ADR-0038 roadmap. Additional enhancements (signature verification, structured VCS references, anomaly detection) are tracked as follow-up issues, not numbered phases.
