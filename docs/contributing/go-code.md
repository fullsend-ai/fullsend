# Go Code

**Mint function:** The mint Cloud Function source lives in two places that must stay in sync:
- `internal/mint/` — the source of truth (has its own `go.mod`, tests run from `internal/mint/`)
- `internal/dispatch/gcf/mintsrc/` — the embedded copies (`.embed` suffix) deployed as a GCP Cloud Function

When changing **any** non-test `.go` file in `internal/mint/`, copy it to the corresponding `.embed` file in `internal/dispatch/gcf/mintsrc/`. If `go.mod` or `go.sum` changed, sync those to `go.mod.embed` and `go.sum.embed` too. The `lint-mint-embed-sync` pre-commit hook checks all files — not just `main.go`.

**Standalone mint:** `cmd/mint/` is a standalone HTTP server variant of the token mint that serves the same purpose as the GCF mint (`internal/mint/`) but runs without GCP infrastructure. Both use the shared `internal/mintcore/` library for token minting logic; they differ only in deployment model (filesystem PEM vs Secret Manager, JWKS vs STS verification). It supports custom role permissions via `CUSTOM_ROLE_PERMISSIONS` and a fallback proxy to an upstream mint. It has its own `go.mod` and tests run from `cmd/mint/`.

**CF Worker adapter:** `internal/dispatch/cf/workersrc/` is a thin TypeScript Cloudflare Worker adapter that consumes mintcore via WASM (`cmd/mint-wasm`). The adapter handles I/O only (Worker secrets, host fetch, Fetch Request/Response mapping); all mint logic stays in Go. The Go WASM bridge registers `mintcoreInitMint` and `mintcoreHandleFetch` on `globalThis` via `syscall/js`; changes to these entry points in `cmd/mint-wasm` or to the contracts they consume in `internal/mintcore/` require updating `workersrc/src/index.ts` to match.

**Mint client:** `internal/mintclient/` is the Go client for calling the mint service at runtime. It exchanges a GitHub Actions OIDC JWT for a role-scoped installation token. Unlike `internal/mint/` and `internal/mintcore/`, it has no embedded copies or sync requirements.

The `internal/mintcore/` module is shared between the mint and devmint. Its files are also embedded for Cloud Function deployment at `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any file in `internal/mintcore/`, sync it to the corresponding `.embed` file under `mintsrc/mintcore/`. Note: the mint's `go.mod.embed` uses `replace mintcore => ./mintcore` (not `../mintcore`), because `provisioner.go` rewrites the replace directive at bundle time to match the deployed directory layout.

**When adding a new file to `internal/mintcore/`:**
1. **Create the `.embed` copy:** Place it in `internal/dispatch/gcf/mintsrc/mintcore/` (required for all files — `lint-mint-embed-sync` enforces this).
2. **Register in `embeddedMintFiles`:** If the file will be included in the GCF bundle — either no build tag (e.g., `config.go`) or `//go:build !js` (e.g., `sts_verifier.go`, `gcp_pem.go`, `wif.go`) — add it to `embeddedMintFiles` in `internal/dispatch/gcf/provisioner.go` and to the `go:embed` directive.
3. **Add to `gcfSkip`:** If the file should NOT be in the GCF bundle — Worker-only files (`//go:build js`) or standalone-mint-only files — add it to the `gcfSkip` map in `TestEmbeddedMintSource_MatchesOriginal` in `provisioner_test.go` instead of `embeddedMintFiles`. The five current entries are `env_js.go`, `fetch_js.go`, `http_client_js.go`, and `pem_js.go` (Worker-only, `//go:build js`) and `file_pem.go` (standalone-mint-only, `//go:build !js`).

**Verifying embed sync:** After modifying any file under `internal/mint/` or `internal/mintcore/`, run the lint script to verify all copies are in sync:

```bash
./hack/lint-mint-embed-sync
```

You can also run the embed test to catch desyncs:

```bash
go test -race -count=1 -run TestEmbeddedMintSource ./internal/dispatch/gcf/
```

Both checks run in CI, but running them locally before committing catches desyncs early and avoids wasted CI iterations.

**Dispatch workflows:** See [Workflow Contracts](workflow-contracts.md) for dispatch sync rules, secret/input threading across installation-mode chains, and review instructions.

**Interface documentation:** When extending a Go interface with new methods (e.g., adding methods to `ci.Driver` in `pkg/behaviourtest/drivers/ci/driver.go`), check `docs/guides/dev/` for documentation that lists or enumerates the interface's methods (e.g., `behaviour-drivers.md`). If found, update the method list to include all current methods, not just the newly added one. The `lint-interface-doc-sync` pre-commit hook enforces this for `ci.Driver`.

## WASM binary size constraints

Files in `internal/mintcore/` and `cmd/mint-wasm/` are compiled to a WASM binary (`GOOS=js GOARCH=wasm`) for the Cloudflare Worker adapter. The compiled binary must stay within CF Workers size limits:

| Tier | Gzip limit | Makefile behavior |
|------|-----------|-------------------|
| Workers Free | 3 MB | Warning |
| Workers Paid | 10 MB | Hard fail (`exit 1`) |

The `make wasm-build` target enforces these limits automatically — run it after any change to `internal/mintcore/` or `cmd/mint-wasm/` to verify the binary stays within bounds.

**Keep the WASM dependency graph minimal.** Because the Go WASM compiler includes the transitive closure of all referenced packages, small-looking changes can cause large binary size increases:

- **Do not pass closures or function values** (`func(string) string`, `func() error`, etc.) into structs that are compiled into the WASM binary. Closure capture pulls the entire dependency graph of the captured variables into the binary. Prefer passing resolved values (strings, ints, config structs with only data fields) instead.
- **Avoid importing heavy packages** in `internal/mintcore/` files that are WASM-compiled. Packages like `net/http`, `crypto/x509`, or cloud SDKs carry large dependency trees. Use build tags (`//go:build js` / `//go:build !js`) to isolate platform-specific implementations.
- **Construct concrete verifiers at the load site** (see `cmd/mint/main.go`, `internal/mint/main.go`, `cmd/mint-wasm/main.go`). Each load site creates the appropriate `OIDCVerifier` — `NewJWKSVerifier` for standalone/Worker/devmint, `NewSTSVerifier` for the Cloud Function — and passes it directly into `NewHandler`. Runtime constants such as the OIDC audience live in `mintconsts.OIDCAudience` and are applied inside the verifier constructors, so load sites do not need to thread configuration through closures or factories.
- **`mintEnv` and `mintHTTP` are package-internal accessors** in `internal/mintcore/`. `NewHandler` reads configuration via `mintEnv(key)` and all HTTP calls go through `mintHTTP(req)`. On native platforms (`//go:build !js`), `mintEnv` delegates to `os.Getenv` and `mintHTTP` uses a cached `*http.Client` with 30-second timeout. On WASM (`//go:build js`), the CF Worker calls `RegisterEnv` and `RegisterHTTP` once during `mintcoreInitMint` to supply JS callbacks. **Do not** pass HTTP clients from entrypoints into verifier configs or handler constructors — call `mintHTTP(req)` directly at use sites inside `internal/mintcore`. Tests override the HTTP function with `SetMintHTTPForTest(t, fake)` and use `t.Setenv` for environment variables.

When making changes to Go code under `cmd/`, `internal/`, or `pkg/`:

1. **Unit tests:** Run `make go-test` (or `go test ./...`) and fix any failures before committing.
2. **Coverage:** CI enforces thresholds via [Codecov](https://about.codecov.io/) (see [`.codecov.yml`](../../.codecov.yml)). **Patch coverage** on changed lines must meet **80%** (with a 5% tolerance). **Project coverage** must not drop more than **1%** below the base branch. `make go-test` alone does **not** enforce these thresholds — you must verify coverage locally before committing. See [Verifying patch coverage locally](#verifying-patch-coverage-locally) below for the exact commands.
3. **Vet:** Run `make go-vet` to catch common issues.
4. **E2E tests:** Run `make e2e-test` if your changes touch `internal/appsetup/`, `internal/forge/`, `internal/cli/`, or `internal/layers/`. These tests exercise the full admin install/uninstall flow against live GitHub pool orgs using mint/OIDC authentication.

## Verifying patch coverage locally

`make go-test` runs tests with `-cover` but does not check whether your
changed lines meet the **80% patch coverage** threshold from
[`.codecov.yml`](../../.codecov.yml). You must approximate this check
yourself before committing. Skipping this step is the most common cause
of `codecov/patch` failures on first push.

### Step-by-step

1. **Identify changed Go files** (excluding tests and generated code).
   **Stage new files first** (`git add`) — `git diff --name-only` only
   sees tracked or staged files, so an unstaged new file would be
   invisible and the check would silently skip it.

   ```bash
   git diff --name-only main -- '*.go' | grep -v '_test.go'
   ```

2. **Determine affected packages** from those files:

   ```bash
   git diff --name-only main -- '*.go' | grep -v '_test.go' \
     | xargs -I{} dirname {} | sort -u \
     | sed 's|^|./|'
   ```

3. **Run tests with a cover profile** for the affected packages:

   ```bash
   go test -coverprofile=coverage.out ./path/to/changed/pkg/...
   ```

   If changes span multiple packages, list them all or use `./...`
   (slower but comprehensive).

4. **Inspect per-function coverage** for your changed files:

   ```bash
   go tool cover -func=coverage.out | grep 'changed_file.go'
   ```

   Each line shows `file:line: function  coverage%`. Look at functions
   you added or modified — these approximate Codecov's line-level patch
   metric.

5. **Assess against the threshold.** If the functions you changed or
   added show coverage well below 80%, add or extend `_test.go` files
   to cover the missing lines. Then re-run from step 3.

### What counts as covered

Codecov measures line-level coverage on the diff. Locally, `go tool
cover -func` reports function-level coverage, which is a coarser
approximation. Target **≥ 80%** on the functions you touched. If a
function has complex branching, use `go tool cover -html=coverage.out`
to visually inspect which lines are covered.

### When to skip

- **Test-only changes** (no production `.go` files modified) — Codecov
  patch coverage applies to production code, not test files.
- **Generated code, docs, or config-only changes** — no Go coverage
  applies.
- **Files listed in `.codecov.yml` `ignore:`** — these are excluded from
  coverage enforcement. Check the ignore list if your file is there.

## Concurrency testing (race detection)

`make go-test` runs all tests with `-race`. Every test must pass under the race detector.

### When to write a race test

When a type is shared across goroutines — for example, via `World.Clone` in the behaviourtest framework — write a dedicated `race_test.go` in the type's own package to verify thread-safety. The race detector can only catch bugs if the test exercises real concurrent access on mutable state.

### Pattern: real types with `forge.NewFakeClient()`

Construct the **real driver type** backed by `forge.NewFakeClient()`, not a synthetic stub. Seed the `FakeClient` so all methods return immediately (no network, no polling). Then launch concurrent goroutines exercising representative methods and rely on `-race` to detect unsynchronized access.

```go
func TestConcurrentAccess(t *testing.T) {
    t.Parallel()

    fc := forge.NewFakeClient()
    // Seed FakeClient so methods return without errors.
    fc.FileContents = map[string][]byte{
        "org/repo/dummy.yaml": []byte("content"),
    }

    d := New(fc) // construct the real driver type
    ctx := context.Background()

    const goroutines = 12

    var wg sync.WaitGroup
    for range goroutines {
        wg.Add(1)
        go func() {
            defer wg.Done()
            // Exercise representative methods concurrently.
            _, _ = d.GetFileContent(ctx, "org", "repo", "dummy.yaml")
            _ = d.CommitFile(ctx, "org", "repo", "path.txt", "msg", []byte("data"))
        }()
    }
    wg.Wait()
}
```

Convention: use 12 goroutines. This is high enough to trigger races reliably but low enough to avoid resource exhaustion in CI. See [`pkg/behaviourtest/drivers/scm/github/race_test.go`](../../pkg/behaviourtest/drivers/scm/github/race_test.go) for the canonical example.

### Assertions inside goroutines: `assert` not `require`

Inside goroutines spawned by a test, use `assert.XXX` (`testify/assert`), **not** `require.XXX` (`testify/require`). `require` calls `t.FailNow()`, which calls `runtime.Goexit()`. Go's `testing` package [documents](https://pkg.go.dev/testing#T.FailNow) that `FailNow` must be called from the goroutine running the test function, not from other goroutines — calling it from a spawned goroutine violates this contract and can silently mispass the test or crash the process. `assert` calls `t.Errorf()`, which is safe from any goroutine.

```go
go func() {
    defer wg.Done()
    tok, err := d.AccessToken(ctx)
    assert.NoError(t, err)   // safe from any goroutine
    assert.Equal(t, "expected", tok)
    // require.NoError(t, err) — never use require inside a goroutine
}()
```

This applies to all `require` functions (`require.NoError`, `require.Equal`, `require.NotNil`, etc.) — the entire `require` package uses `FailNow` internally. The test goroutine itself (the function passed to `t.Run` or the `Test*` function) can use `require` normally.

### Why synthetic stubs don't work

Stubs that implement an interface with no-ops or stateless pass-throughs hold no mutable state, so the race detector has nothing to detect. Even stubs that use `atomic.Int64` counters are invisible to `-race` because atomics are correctly synchronized by definition. The point of a race test is to exercise the **real type's fields** — only a real constructor backed by a thread-safe fake can trigger the detector on unsynchronized production code.

## Concurrent error handling

When goroutines fan out to perform **independent** operations that can each
fail on their own (e.g., a `sync.WaitGroup` + `go func` loop creating
providers), collect **every** failure and surface them together with
`errors.Join`. Do not keep a single error variable (e.g., `firstErr`) that
discards all failures after the first — that forces the user into
fix-and-rerun cycles: they fix the one error shown, rerun, and only then
discover the next.

The canonical pattern — a mutex-guarded `[]error` accumulated across
goroutines and joined after `wg.Wait()` — is in `internal/cli/run.go`
(provider fan-out):

```go
var (
    mu   sync.Mutex
    wg   sync.WaitGroup
    errs []error
)
for _, pd := range allDefs {
    wg.Add(1)
    go func(pd harness.ProviderDef) {
        defer wg.Done()
        if err := sandbox.EnsureProvider(ctx, /* … */); err != nil {
            mu.Lock()
            errs = append(errs, fmt.Errorf("ensuring provider %q: %w", pd.Name, err))
            mu.Unlock()
            return
        }
    }(pd)
}
wg.Wait()
if err := errors.Join(errs...); err != nil {
    return err
}
```

The `sync.Mutex` is the idiom used here; writing each goroutine's result into
its own pre-sized slice slot (one index per goroutine, no lock) is equally
acceptable. The requirement is that no failure is dropped — not the specific
synchronization mechanism.

**This applies only to independent fan-out.** When goroutines are *not*
independent — you deliberately want the first failure to cancel the rest
(e.g., an `errgroup.Group` sharing a `context.Context`) — fail-fast is
correct and must not be forced into error collection.

**When reviewing PRs:** Flag a fan-out that captures only the first error (a
single `firstErr`/`err` variable, or break-on-first) across independent
goroutines as a **medium-severity** finding, and recommend collecting a
`[]error` and returning `errors.Join`. Do not flag intentional fail-fast
cancellation patterns.

## Context-aware blocking

Functions that accept `context.Context` must not use `time.Sleep` or other
unconditionally-blocking calls. Use `select` to respect cancellation:

```go
// Good — respects context cancellation.
select {
case <-ctx.Done():
    return ctx.Err()
case <-time.After(backoff):
}

// Bad — blocks unconditionally, ignores cancellation.
time.Sleep(backoff)
```

This applies to retry loops, polling intervals, and any deliberate delay.
For retry patterns specifically, check whether the package already provides
an injectable sleep function (e.g., `sandbox.RetrySleepFn`) for testability.

For blocking syscalls like `syscall.Flock(LOCK_EX)` that cannot be
interrupted, add a comment documenting the worst-case blocking duration
and why it is acceptable.

## Error handling and naming conventions

### Use typed constants over string literals

When the codebase defines constants for a value, use them instead of repeating string literals. For example, `repos.ForgeGitHub` and `repos.ForgeGitLab` (defined in `internal/repos/manifest.go`) should be used wherever a forge type is compared or assigned — not the raw strings `"github"` or `"gitlab"`.

Before introducing a string literal for a domain value, search for existing constants:

```bash
grep -rn 'const.*Forge' internal/
```

The same applies to credential modes (`repos.CredModeWIF`), tracker types, and other enumerated values. Using typed constants avoids silent breakage when a value is renamed and makes it clear which values are valid.

### Prefer sentinel errors for programmatic error checking

When callers need to distinguish error conditions (e.g., "token not found" vs "unsupported forge"), define a package-level sentinel error and check it with `errors.Is`:

```go
// Package-level sentinel — unexported, starts with "err".
var errGitLabTokenMissing = errors.New("no GitLab token found: set GITLAB_TOKEN or pass --gitlab-token")

func resolveGitLabToken() (string, error) {
    if token := os.Getenv("GITLAB_TOKEN"); token != "" {
        return token, nil
    }
    return "", errGitLabTokenMissing
}

// Caller checks the sentinel:
token, err := resolveGitLabToken()
if errors.Is(err, errGitLabTokenMissing) {
    // handle missing token specifically
}
```

**Do not** match errors by substring: `strings.Contains(err.Error(), "token")` couples error handling to message wording and breaks when messages change. Use `errors.Is` or `errors.As` for all programmatic error checks.

See `internal/cli/forge_client.go` (`errGitLabTokenMissing`), `internal/cli/admin.go` (`errMintNotFound`), and `internal/cli/lock.go` (`errHarnessNotFound`) for examples of this pattern in the codebase.

### Use `%q` for values in error messages

Format user-provided or enumerated values with `%q` so they are consistently quoted in output. This makes error messages unambiguous when values contain spaces or are empty:

```go
// Good — %q adds quotes automatically.
return fmt.Errorf("unsupported forge %q", forgeName)

// Bad — manual escaping is inconsistent and easy to forget.
return fmt.Errorf("unsupported forge \"%s\"", forgeName)
```

This matches the pattern in `internal/cli/tracker_client.go` and `internal/cli/forge_client.go`.

### Consistent error message content

When multiple code paths produce errors for the same condition across different forges or providers, ensure they mention the same remediation options. For example, if one "no token found" error suggests both the environment variable and the `--token` flag, other forge-specific token errors should do the same — so users see consistent guidance regardless of which code path triggers.

## Go pitfalls

### `Timeout() bool` interface and `context.DeadlineExceeded`

`context.DeadlineExceeded` implements `interface{ Timeout() bool }` and returns `true`. This means any timeout detection that uses an interface type assertion will incorrectly classify context deadline errors as timeouts:

```go
// WRONG — matches context.DeadlineExceeded, which is not a transient
// network timeout but an intentional cancellation by the caller.
var te interface{ Timeout() bool }
if errors.As(err, &te) && te.Timeout() {
    return true // retries context deadlines — incorrect
}
```

Context deadline and cancellation errors represent intentional cancellation by the caller (e.g., a request timeout set by the application, a user-initiated cancel). They should never be classified as transient or retried — the caller chose to stop waiting, and retrying re-creates the same deadline.

**Always guard against context errors before checking `Timeout()`:**

```go
// CORRECT — context errors are excluded before the Timeout() check.
if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
    return false
}
var te interface{ Timeout() bool }
if errors.As(err, &te) && te.Timeout() {
    return true // only matches genuine network timeouts (e.g. net/http.Client.Timeout)
}
```

See [`forge.IsTransient`](../../internal/forge/forge.go) for the canonical example of the correct pattern.

**When reviewing PRs:** Flag any `Timeout() bool` interface assertion without a preceding `errors.Is(err, context.DeadlineExceeded)` guard as a medium-severity finding. The fix is to add the context-error check before the `Timeout()` check.

### Template map iteration

Go's `text/template` `range` action visits map keys of basic types (string,
int, uint, float) in **sorted order** — unlike bare `range` over a map in Go code.
Do **not** flag `{{ range $k, $v := .SomeMap }}` in templates as
non-deterministic when the key type is a basic type. See
[text/template documentation](https://pkg.go.dev/text/template) (search
"sorted key order").

**When reviewing PRs:** Do not flag `range` over a basic-type-keyed map
inside a `text/template` as non-deterministic output. The `text/template`
package guarantees sorted iteration for string, int, uint, and float keys. This
is a well-documented exception to Go's general rule that map iteration
order is unspecified.

## Injectable function variables (test seams)

Package-level variables that hold function values for test overriding must:

- Use an `XxxFn` suffix (e.g., `BuildWASMFn`, `RetrySleepFn`, `WranglerWhoamiFn`). Both exported (`XxxFn`) and unexported (`xxxFn`) variables follow this suffix pattern
- Default to the real implementation
- Include a doc comment following Go convention (starting with the variable name) that contains an "Override in tests to..." sentence describing the override behavior
- Be restored in a `t.Cleanup` callback when overridden

Examples: `internal/sandbox/sandbox.go` (`RetrySleepFn`), `internal/dispatch/cf/provisioner.go` (`BuildWASMFn`, `CopyWASMExecFn`).

## Secure HTTP clients

**Scope.** These requirements apply to any client whose target URL is derived from **untrusted configuration, user input, or remote content** — that is where SSRF lives. A client that talks to a **fixed first-party endpoint supplied by the sandbox/runner bootstrap** is out of scope for the SSRF hardening below. For example, `internal/cli/fetchskill.go` POSTs to the runner-side fetch service on the host, which is bound to `0.0.0.0` per ADR 0046, bearer-token authenticated, and reached over a private address. The runner injects `FULLSEND_FETCH_URL` and `FULLSEND_FETCH_TOKEN` as reserved keys that harness YAML cannot shadow, so this is a deliberate trusted channel rather than an arbitrary configuration URL. Such clients should still set a timeout and bound the response body, but do not need IP filtering, proxy disabling, or an HTTPS-only rule. The current `fetchskill.go` client sets a timeout but does not yet bound its decoded response body; that gap is not an example to copy.

**Prefer the shared, SSRF-hardened fetcher `internal/fetch.FetchURL` over constructing a raw `http.Client`.** Pass it a `fetch.FetchPolicy` (see `fetch.DefaultPolicy` for the GitHub-content defaults) rather than re-implementing the protections. `internal/fetch/fetch.go` is the canonical reference.

`FetchURL` has a deliberately narrow envelope — reach for it only when all of these hold, otherwise it will reject the request or can't express what you need:

- **The legitimate host set is known up front.** `FetchURL` requires a non-empty `AllowedDomains` allowlist — an empty allowlist rejects *every* URL (`isAllowedDomain` returns false), so the allowlist is mandatory, not optional.
- **GET, HTTP 200, no custom headers.** It issues a `GET` and sends no request headers — so it cannot carry authentication or use another method. Transient HTTP status codes (429, 502, 503) are retried with exponential backoff, but the function ultimately requires a 200 response.
- **Whole body buffered, port 443.** It reads the entire body into memory and defaults `AllowedPorts` to `{"443"}`.

When the fetch falls outside that envelope you must build a custom client. Common reasons: **the legitimate host set is not knowable up front** (e.g. `internal/repos/manifest.go`'s `LoadManifest` accepts any user-supplied `https://` host, so no allowlist covers it — this is why `fetchManifestURL`/`safeDialContext` exist and deliberately do *not* use `FetchURL`), authenticated requests, non-GET methods, non-200 handling, streaming, or a client reused across many calls. The complete worked pattern is `LoadManifest`'s initial HTTPS-only gate together with `fetchManifestURL`/`safeDialContext`; the fetch functions enforce the remaining controls and HTTPS-only redirects, but do not independently reject a non-HTTPS initial URL. A custom in-scope client **must** apply all of these properties:

- **HTTPS only.** Reject `http://` inputs, and validate the scheme on redirects too — a `CheckRedirect` that lets an `https://` origin bounce to `http://` reopens the hole.
- **Reject internal/reserved IPs, on every connection.** Inside a custom `DialContext`, resolve the host the transport actually asks for (split the `addr` it passes you, resolve it, check every IP with `netutil.CheckIP` / `netutil.IsInternal`), then dial one of those validated IPs — never re-dial the hostname. Validating and dialing the exact same IP is the DNS-rebinding defense. Do this **per connection**, the way `safeDialContext` does, so a redirect to a different host is resolved and validated before that connection. `fetch.FetchURL`'s single up-front resolution and pinning is safe because it blocks redirects outright (its `CheckRedirect` returns `http.ErrUseLastResponse`); it cannot validate a redirect to a different host, so never copy that pattern into a client that follows redirects.
- **Keep the original hostname on the request.** Only the dial *address* changes to the validated IP — the `http.Request` URL, and therefore SNI and certificate verification, must still carry the original hostname, or TLS verification breaks. Build the request from the original URL and let `DialContext` swap the address, exactly as `fetch.FetchURL` does (`http.NewRequestWithContext(ctx, …, rawURL, …)` while its `DialContext` dials the IP). **Never** rewrite the request URL to the IP, and never reach for `InsecureSkipVerify` to paper over the resulting cert failure.
- **Disable proxies.** Set `Transport.Proxy = nil` so `HTTP(S)_PROXY` env vars can't redirect the request.
- **Bound the time.** Set an explicit timeout (30s is the repo default) via `context.WithTimeout` and/or `http.Client.Timeout`.
- **Bound the size.** Wrap the response body in `io.LimitReader(body, max+1)` and error if the read exceeds `max` (1 MB is the manifest default; pick a limit appropriate to the payload). Never `io.ReadAll` — or `json.NewDecoder` — an unbounded body.
- **Constrain redirects.** Block them (as `fetch.FetchURL` does) or cap the hop count; and if you allow any hop, re-run the checks above against each redirect target — both the scheme check and, via per-connection dialing, the internal-IP check — not just the scheme.

When the set of legitimate hosts *is* known, add a domain allowlist too, even on the custom path.


## Credential redaction for external content

Any runner feature that processes external content — validation script
output, CI logs, script stdout/stderr — for injection into LLM prompts,
logging, or file storage **must** redact credentials before that content
leaves the runner boundary. The validation loop's `redactFeedback`
function in `internal/cli/run.go` is the canonical implementation.

### Invariants

1. **Scan `RunnerEnv` for credential literal values.** Iterate the
   runner environment map and replace every value whose key is
   classified as sensitive by `sensitiveEnvKey` (explicit names like
   `PUSH_TOKEN`, `GH_TOKEN`, plus suffix matches on `_TOKEN`,
   `_SECRET`, `_PASSWORD`, `_KEY`, `_CREDENTIALS`) with
   `[REDACTED:<key>]`. Skip values shorter than
   `minRedactableSecretLen` (currently 8) — short values like `"main"`
   or `"true"` cause false-positive mangling.

2. **Apply `security.SecretRedactor` as a second-pass fallback.**
   The `RunnerEnv` scan only catches credentials the harness declared.
   A `security.NewSecretRedactor().Scan(content)` call catches
   credentials with recognizable shapes (known-prefix tokens such as
   `ghp_`, `sk-ant-`, `AKIA`, PEM blocks, connection strings) that
   never passed through the runner
   environment — for example, a key baked into a test fixture or a
   pre-commit hook printing its own secrets.

3. **Use `truncateUTF8` when enforcing size limits on external
   content.** Naive byte slicing (`s[:max]`) can split a multi-byte
   UTF-8 rune, producing invalid text that breaks downstream JSON
   serialization or LLM tokenization. Use `truncateUTF8(s, max)`
   (defined in `internal/cli/run.go`), which backs up to the last
   valid rune boundary before appending a `[truncated]` marker.

4. **Write files containing potential secrets with mode `0600`.**
   Feedback files, redacted logs, and any file derived from external
   content must use `os.WriteFile(path, data, 0o600)` — not `0644`.
   The run directory is uploaded as a CI artifact; restrictive
   permissions limit exposure if the artifact is downloaded to a
   shared filesystem.

### Why both passes are needed

Neither pass alone is sufficient. Opaque tokens (e.g., a GitHub
installation token with no recognizable prefix) have no pattern for the
`SecretRedactor` to match — only the literal `RunnerEnv` scan catches
those. Conversely, credentials that never entered the runner environment
(a PEM key printed by a repo hook, a fixture secret) are invisible to
the env scan — only the pattern-based `SecretRedactor` catches those.

### When this applies

Apply these invariants whenever external content crosses a trust
boundary in the runner:

- Validation script output injected into the next iteration's LLM
  prompt (`feedback_mode`)
- Pre-commit or post-script output routed back to the agent
- CI log fragments stored in the run directory
- Any new feature that captures subprocess output for prompt injection,
  storage, or logging

See also [#2107](https://github.com/fullsend-ai/fullsend/issues/2107)
(replicate existing security patterns) and
[#2872](https://github.com/fullsend-ai/fullsend/issues/2872)
(post-script security invariants) for related guidance in other layers.

## Running the fullsend CLI

**Audience:** contributors and agents working from a **repo checkout**. Do not
change end-user or operator guides under `docs/guides/getting-started/`,
`docs/guides/user/`, or `docs/guides/infrastructure/` to require `go run` —
those audiences install a released `fullsend` binary.

When agents (or humans working from this checkout) need the fullsend CLI,
invoke it from the **repo root** with:

```bash
go run ./cmd/fullsend <subcommand> …
```

**Do not** use a preinstalled `fullsend` from mise, `$PATH`, `GOBIN`, `go install`, or another clone. Those binaries often lag the branch you are on. Enrollment and other mint-mutating commands rewrite Cloud Run env vars; a stale CLI can apply obsolete merge logic against the hosted mint.

This already happened: an enrollment for `crc-org/crc` used a June-era mise `fullsend` that still wrote org-scoped `ROLE_APP_IDS` keys and re-derived `ALLOWED_ROLES` from slash-keyed entries only. Shared role-only keys such as `e2e` and `fix` were ignored, so the mint dropped those roles from `ALLOWED_ROLES` and e2e broke until the mint was restored. Current tree enroll is safer, but the durable fix for agents is to always run the CLI from source.

`make go-build` / `bin/fullsend` is fine when you intentionally build **this** checkout first. Prefer `go run` unless you have a reason to keep a built binary.

## Running e2e tests

The e2e tests mint short-lived GitHub App installation tokens via the central token mint. Pool-org admin operations use mint/OIDC in CI and do not require a dedicated mint URL secret.

- **CI (mint):** Uses the hosted public mint (same default as `fullsend admin --mint-url`) with the workflow's OIDC identity. The e2e workflow exchanges the OIDC JWT for an `e2e`-role installation token on the pool org. Override with `FULLSEND_MINT_URL` if needed.
- **Local:** Run `gh auth login` (or set `GH_TOKEN` / `GITHUB_TOKEN` with pool-org admin access). Mint uses `FULLSEND_MINT_URL` or the hosted default.

**Do not** increase e2e or behaviour **suite** timeouts without **explicit human authorization** in the current session (or an issue/PR comment that clearly authorizes that bump). Suite ceilings are job `timeout-minutes` in `.github/workflows/e2e.yml` for the `e2e` / `behaviour` jobs, the matching `go test -timeout` values in the `e2e-test` / `behaviour-test` Make targets, and the default `E2E_LOCK_TIMEOUT`. Scenario-level wait or assertion windows (for example dispatch detection) are out of scope when an issue asks for them — those are not suite ceilings. On timeout failures, diagnose and fix the root cause (slow scenarios, unnecessary waits, lock contention); do not open a PR whose primary change is raising the suite timeout.

**When reviewing PRs:** Flag unauthorized suite-timeout increases as an **important-severity** finding (policy violation). Explicit human authorization in the linked issue or a PR comment is the only exception.

See [`docs/guides/dev/e2e-testing.md`](../guides/dev/e2e-testing.md) and `make help` for pool org setup and troubleshooting.

## Per-repo config field checklist

When adding a new field to `perRepoConfig` (`internal/config/config.go`)
that needs validation, you must wire validation into **both** the write
path and the run path. The two paths use different validation entry
points, and missing either one creates a gap.

### Why two validation paths exist

`perRepoConfig.Validate()` runs on **write paths** — for example, when
`fullsend config set` persists a config file. However, `fullsend run`
loads config via `loadPerRepoLayers()` (in `internal/config/interfaces.go`),
which **does not call `Validate()`**. This means validation logic that
only lives in `Validate()` never fires when the config is consumed at
runtime. Invalid entries (unknown keys, bad references) are silently
accepted.

The codebase solves this with a dual-validation pattern: `Validate()`
covers write paths, and inline validation in `runAgent()`
(`internal/cli/run.go`) covers the run path.

### Canonical examples

- **`agentSettings()`** (`internal/cli/run.go`) — validates agent
  entries loaded from config before applying them. The function's doc
  comment explicitly states: "`fullsend run` never calls `Validate()` on
  the config it loads, so this is where those values get checked."
- **`ValidateModelAliases()`** (`internal/config/config.go`) — exported
  validation function called both in `Validate()` (write path) and
  directly in `runAgent()` (run path) to reject unknown alias keys and
  invalid model references before the sandbox is created.
- **`run_models_aliases_test.go`** (`internal/cli/`) — run-path
  integration test verifying that invalid `models.aliases` values are
  rejected by `runAgent()` before sandbox creation.

### Checklist

When adding a new validated field to `perRepoConfig`:

1. **Add validation in `Validate()`** — this covers write paths (e.g.,
   `fullsend config set`).
2. **Add inline validation in `runAgent()`** — follow the
   `agentSettings()` / `ValidateModelAliases()` pattern: validate the
   effective (merged) value before the sandbox is created, not after.
   If the validation logic is non-trivial, export it as a standalone
   function (like `ValidateModelAliases`) so both call sites use the
   same logic.
3. **Add a run-path integration test** — follow the pattern in
   `run_models_aliases_test.go`: write an invalid config, call
   `runAgent()`, and assert that it fails with the expected error
   before any sandbox is created.

### When reviewing PRs

When reviewing a PR that adds a new validated field to `perRepoConfig`,
check that validation fires on the run path — not only in `Validate()`.
Flag a missing run-path validation call as a **medium-severity** finding.
The fix is to add inline validation in `runAgent()` and a corresponding
integration test.
