# Mintcore Architecture

This guide describes the **shipped** wiring of `internal/mintcore/` — the
shared token-minting library used by three deploy targets. It covers
platform accessors, load-site construction, WASM constraints, and the
patterns that keep the Cloudflare Worker binary within size limits.

## Deploy targets

Mintcore is shared by three deploy targets. Each target has its own
**load site** (entrypoint) that constructs the verifier and PEM accessor
and passes them into `NewHandler`.

| Target | Load site | Verifier | PEM accessor |
|--------|-----------|----------|--------------|
| **GCF** (Cloud Function) | `internal/mint` | `NewSTSVerifier` | `NewGCPSecretPEMAccessor` |
| **Cloudflare Worker** | `cmd/mint-wasm` (`mintcoreInitMint`) | `NewJWKSVerifier` | `NewHostPEMAccessor` |
| **Standalone** | `cmd/mint` | `NewJWKSVerifier` | `NewFilesystemPEMAccessor` |

A fourth consumer, **devmint** (used in e2e tests), also constructs
`NewJWKSVerifier` and passes its own PEM accessor via the same
`NewHandler` interface.

## Layering

```
internal/mintcore/        shared library — all mint logic
  mintconsts/             compile-time constants (OIDCAudience)
internal/mint/            GCF load site (has its own go.mod)
cmd/mint/                 standalone HTTP server load site (has its own go.mod)
cmd/mint-wasm/            WASM entrypoint for CF Worker (imports mintcore)
internal/dispatch/cf/
  workersrc/              CF Worker TypeScript adapter (I/O only)
internal/dispatch/gcf/
  mintsrc/                embedded copies for GCF deployment (.embed files)
```

**internal/mintcore/** contains all token-minting logic: request
parsing, OIDC verification, claims validation, authorization (org,
workflow-ref, repos scope), GitHub App token creation, and status
endpoints. It compiles to both native (`!js`) and WASM (`js`) targets.

**Entrypoints** (`internal/mint`, `cmd/mint`, `cmd/mint-wasm`) are thin.
They construct the appropriate `OIDCVerifier` + `PEMAccessor` for their
platform, call `NewHandler(pemAccessor, verifier)`, and serve HTTP.
No mint logic lives in entrypoints.

**The CF Worker TypeScript adapter** (`workersrc/src/index.ts`) handles
I/O only — Worker secrets, host fetch, Fetch Request/Response mapping.
It calls `mintcoreInitMint` and `mintcoreHandleFetch` registered on
`globalThis` by the Go WASM bridge in `cmd/mint-wasm`.

## Platform accessors

Two package-internal accessor functions abstract platform I/O inside
mintcore:

### `mintEnv(key string) string`

Reads environment/configuration values.

| Build tag | Implementation | File |
|-----------|----------------|------|
| `!js` (native) | `os.Getenv(key)` | `env.go` |
| `js` (WASM) | JS callback registered via `RegisterEnv` | `env_js.go` |

`NewHandler` reads all configuration variables (`ROLE_APP_IDS`,
`ALLOWED_ORGS`, `ALLOWED_WORKFLOW_FILES`, `PER_REPO_WIF_REPOS`,
`WORKFLOW_HOST_REPOS`, `CUSTOM_ROLE_PERMISSIONS`, `ALLOWED_ROLES`)
via `mintEnv` at construction time.

### `mintHTTP(req *http.Request) (*http.Response, error)`

Executes outbound HTTP requests (GitHub API calls).

| Build tag | Implementation | File |
|-----------|----------------|------|
| `!js` (native) | Cached `*http.Client` with 30s timeout | `http_client.go` |
| `js` (WASM) | JS fetch callback registered via `RegisterHTTP` | `http_client_js.go` |

`mintHTTP(req)` is called at **request time** inside handler and
verifier files (`github.go`, `jwks_verifier.go`, `sts_verifier.go`,
`gcp_pem.go`). It is never called during construction.

### Registration on WASM

The CF Worker calls `RegisterEnv` and `RegisterHTTP` once during
`mintcoreInitMint` — before `NewHandler` reads configuration. Only
`cmd/mint-wasm/main.go` calls these functions; native entrypoints do
not need to (and cannot — the functions are behind `//go:build js`).

### Who may call `RegisterEnv` / `RegisterHTTP`

Only the WASM entrypoint (`cmd/mint-wasm`). These are one-shot
registration functions, not something load sites or tests should call.
Tests use `t.Setenv` for environment variables and
`SetMintHTTPForTest(t, fake)` for HTTP overrides.

## Load-site construction

Each entrypoint builds the verifier and PEM accessor appropriate for
its platform, then passes them into `NewHandler`:

```go
// cmd/mint (standalone)
verifier, _ := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
    IssuerURL: "https://token.actions.githubusercontent.com",
})
pemAccessor, _ := mintcore.NewFilesystemPEMAccessor(os.Getenv("PEM_DIR"))
handler, _ := mintcore.NewHandler(pemAccessor, verifier)
```

```go
// internal/mint (GCF)
verifier, _ := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
    GCPProjectNum:      gcpProjectNum,
    WIFPoolName:        wifPoolName,
    DefaultWIFProvider: defaultWIFProvider,
    PerRepoWIFRepos:    perRepoWIFRepos,
})
pemAccessor := mintcore.NewGCPSecretPEMAccessor(gcpProjectNum)
handler, _ := mintcore.NewHandler(pemAccessor, verifier)
```

```go
// cmd/mint-wasm (CF Worker)
mintcore.RegisterEnv(getEnvFn)
mintcore.RegisterHTTP(fetchFn)
pemAccessor, _ := mintcore.NewHostPEMAccessor(pemFn)
verifier, _ := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
    IssuerURL: "https://token.actions.githubusercontent.com",
})
handler, _ := mintcore.NewHandler(pemAccessor, verifier)
```

Key properties:

- **No `getEnv` or `HTTPDoer` parameters** on `NewHandler`. The handler
  reads config via `mintEnv` and makes HTTP calls via `mintHTTP`
  internally.
- **No factories passed into `NewHandler`.** The handler receives
  constructed interfaces, not factory functions.
- **Verifier configs use plain data** (strings, maps, booleans). The
  OIDC audience is the compile-time constant `mintconsts.OIDCAudience`,
  applied inside verifier constructors — load sites do not pass it.

## WASM-unsafe patterns

These patterns increase the WASM binary size or cause deadlocks in the
Worker runtime. **Do not use them in `internal/mintcore/`.**

### Closures and function values in config structs

```go
// BAD: closure captures http.Client's entire dependency graph
type VerifierConfig struct {
    DoHTTP func(*http.Request) (*http.Response, error)
}
```

Closure capture pulls the entire dependency graph of the captured
variables into the WASM binary. A `func(string) string` for env lookups
or a `func(*http.Request) (*http.Response, error)` for HTTP inflates the
binary even if the function body is trivial, because the compiler must
include all transitively reachable types.

### `mintEnv` / `mintHTTP` inside verifier constructors

```go
// BAD: constructor-time env/HTTP pulls dependencies into the call
// graph at init, and on WASM the JS callbacks may not be registered yet
func NewJWKSVerifier() (*JWKSVerifier, error) {
    issuer := mintEnv("OIDC_ISSUER")  // constructor-time env read
    resp, _ := mintHTTP(req)           // constructor-time HTTP call
    // ...
}
```

Verifier constructors run during `mintcoreInitMint`. Using `mintHTTP`
in a constructor tries to make HTTP calls before the event loop is
available, risking deadlocks. Using `mintEnv` in a constructor is less
dangerous but couples construction to environment state rather than
explicit config.

The shipped pattern: constructors take plain config structs →
`mintHTTP(req)` is called at **request time** in verifier and handler
methods.

## WASM-safe patterns

These are the patterns used in shipped code. Follow them when modifying
mintcore.

### `Register*` once at bootstrap

On WASM, the CF Worker calls `RegisterEnv(fn)` and `RegisterHTTP(fn)`
once during `mintcoreInitMint`, before constructing the handler. Native
platforms do not call these — `mintEnv` delegates to `os.Getenv` and
`mintHTTP` uses a cached `*http.Client` automatically via build tags.

### Constructors take plain data

Verifier configs contain only strings, maps, and booleans — no function
values, no interfaces, no closures:

```go
type JWKSVerifierConfig struct {
    IssuerURL string
}

type STSVerifierConfig struct {
    GCPProjectNum      string
    WIFPoolName        string
    DefaultWIFProvider string
    PerRepoWIFRepos    map[string]bool
    // optional fields...
}
```

### Request-time `mintHTTP(req)`

HTTP calls happen at request time, not construction time. Files that
call `mintHTTP` include:

- `github.go` — GitHub App installation token creation
- `jwks_verifier.go` — JWKS key fetching
- `sts_verifier.go` — GCP STS token exchange
- `gcp_pem.go` — GCP Secret Manager access

This is intentional: on WASM, the JS event loop must be free for
Promises to settle. Constructor-time HTTP would deadlock.

### Handler reads config via `mintEnv`

`NewHandler` reads `ROLE_APP_IDS`, `ALLOWED_ORGS`, and other
configuration variables once via `mintEnv` at construction time.
This works because `RegisterEnv` has already been called by the
time `NewHandler` runs — the pattern is: register → construct →
serve.

## PEM remains injected

PEM access uses the `PEMAccessor` interface at the `NewHandler`
boundary.

| PEM implementation | Platform | Storage |
|-------------------|----------|---------|
| `GCPSecretPEMAccessor` | GCF | GCP Secret Manager |
| `FilesystemPEMAccessor` | Standalone | Local directory (`PEM_DIR`) |
| `HostPEMAccessor` | CF Worker (WASM) | Worker secrets via JS callback |

Each load site constructs the appropriate accessor and passes it to
`NewHandler`. Tests can pass any `PEMAccessor` implementation.

## Interfaces vs accessors

Mintcore uses two patterns for dependency injection:

**Package-internal accessors** (`mintEnv`, `mintHTTP`) for production
environment lookups and HTTP — these are implementation details of
mintcore, not part of its public API. They exist because every file in
mintcore needs env/HTTP access, and threading interfaces through every
function signature would be impractical. Tests override them with
`t.Setenv` and `SetMintHTTPForTest(t, fake)`.

**Interfaces** (`OIDCVerifier`, `PEMAccessor`) at the `NewHandler`
boundary — these are the public contract between load sites and
mintcore. Load sites choose which implementation to construct; mintcore
does not know or care which platform it is running on.

```go
type OIDCVerifier interface {
    Verify(ctx context.Context, rawToken string) (*Claims, error)
}

type PEMAccessor interface {
    AccessPEM(ctx context.Context, role string) ([]byte, error)
}
```

## Embed sync and `make wasm-build`

### Embedded copies for GCF

Mintcore files are embedded for Cloud Function deployment at
`internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any
file in `internal/mintcore/`, sync it to the corresponding `.embed`
file. The `lint-mint-embed-sync` pre-commit hook enforces this.

When **adding a new file** to `internal/mintcore/`:

1. Create the `.embed` copy in
   `internal/dispatch/gcf/mintsrc/mintcore/`.
2. If the file is included in the GCF bundle (no build tag, or
   `//go:build !js`), add it to `embeddedMintFiles` in
   `internal/dispatch/gcf/provisioner.go` and to the `go:embed`
   directive.
3. If the file is NOT included in the GCF bundle (Worker-only
   `//go:build js` files, or standalone-mint-only files), add it to
   `gcfSkip` in `TestEmbeddedMintSource_MatchesOriginal` in
   `provisioner_test.go`.

Current `gcfSkip` entries: `env_js.go`, `fetch_js.go`,
`http_client_js.go`, `pem_js.go` (all `//go:build js`), and
`file_pem.go` (standalone-mint-only, `//go:build !js`).

### WASM binary size gate

The compiled WASM binary must stay within Cloudflare Workers size
limits. Run `make wasm-build` after any change to `internal/mintcore/`
or `cmd/mint-wasm/` to verify:

| Tier | Gzip limit | Makefile behavior |
|------|-----------|-------------------|
| Workers Free | 3 MB | Warning |
| Workers Paid | 10 MB | Hard fail |

Keep the WASM dependency graph minimal. The Go WASM compiler includes
the transitive closure of all referenced packages — small-looking
changes can cause large binary size increases. Avoid importing heavy
packages (`net/http`, `crypto/x509`, cloud SDKs) in files that are
WASM-compiled. Use build tags (`//go:build js` / `//go:build !js`) to
isolate platform-specific implementations.
