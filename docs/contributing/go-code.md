# Go Code

**Mint function:** The mint Cloud Function source lives in two places that must stay in sync:
- `internal/mint/main.go` — the source of truth (has its own `go.mod`, tests run from `internal/mint/`)
- `internal/dispatch/gcf/mintsrc/main.go.embed` — the embedded copy deployed as a GCP Cloud Function

When changing `internal/mint/main.go`, always copy it to `internal/dispatch/gcf/mintsrc/main.go.embed`. If `go.mod` or `go.sum` changed, sync those to `go.mod.embed` and `go.sum.embed` too.

**Standalone mint:** `cmd/mint/` is a standalone HTTP server variant of the token mint that serves the same purpose as the GCF mint (`internal/mint/`) but runs without GCP infrastructure. Both use the shared `internal/mintcore/` library for token minting logic; they differ only in deployment model (filesystem PEM vs Secret Manager, JWKS vs STS verification). It supports custom role permissions via `CUSTOM_ROLE_PERMISSIONS` and a fallback proxy to an upstream mint. It has its own `go.mod` and tests run from `cmd/mint/`.

**CF Worker adapter:** `internal/dispatch/cf/workersrc/` is a thin TypeScript Cloudflare Worker adapter that consumes mintcore via WASM (`cmd/mint-wasm`). The adapter handles I/O only (Worker secrets, host fetch, Fetch Request/Response mapping); all mint logic stays in Go. The Go WASM bridge registers `mintcoreInitMint` and `mintcoreHandleFetch` on `globalThis` via `syscall/js`; changes to these entry points in `cmd/mint-wasm` or to the contracts they consume in `internal/mintcore/` require updating `workersrc/src/index.ts` to match.

**Mint client:** `internal/mintclient/` is the Go client for calling the mint service at runtime. It exchanges a GitHub Actions OIDC JWT for a role-scoped installation token. Unlike `internal/mint/` and `internal/mintcore/`, it has no embedded copies or sync requirements.

The `internal/mintcore/` module is shared between the mint and devmint. Its files are also embedded for Cloud Function deployment at `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any file in `internal/mintcore/`, sync it to the corresponding `.embed` file under `mintsrc/mintcore/`. Note: the mint's `go.mod.embed` uses `replace mintcore => ./mintcore` (not `../mintcore`), because `provisioner.go` rewrites the replace directive at bundle time to match the deployed directory layout.

**When adding a new file to `internal/mintcore/`:**
1. **Create the `.embed` copy:** Place it in `internal/dispatch/gcf/mintsrc/mintcore/` (required for all files — `lint-mint-embed-sync` enforces this).
2. **Register in `embeddedMintFiles`:** If the file will be included in the GCF bundle — either no build tag (e.g., `config.go`) or `//go:build !js` (e.g., `sts_verifier.go`, `gcp_pem.go`, `wif.go`) — add it to `embeddedMintFiles` in `internal/dispatch/gcf/provisioner.go` and to the `go:embed` directive.
3. **Add to `gcfSkip`:** If the file should NOT be in the GCF bundle — Worker-only files (`//go:build js`) or standalone-mint-only files — add it to the `gcfSkip` map in `TestEmbeddedMintSource_MatchesOriginal` in `provisioner_test.go` instead of `embeddedMintFiles`. The five current entries are `env_js.go`, `fetch_js.go`, `http_client_js.go`, and `pem_js.go` (Worker-only, `//go:build js`) and `file_pem.go` (standalone-mint-only, `//go:build !js`).

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

1. **Identify changed Go files** (excluding tests and generated code):

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
