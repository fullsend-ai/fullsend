# Go Developer Cookbook

A practical guide for Go developers contributing to the fullsend codebase. Covers project conventions, key patterns, and deep-dives into the mint, inference, GitHub forge, and reusable workflow systems.

For local environment setup, see [Local Development](local-dev.md). For CLI architecture, see [CLI Internals](cli-internals.md).

---

## Table of Contents

1. [Project Layout](#project-layout)
2. [Go Conventions](#go-conventions)
3. [Testing Patterns](#testing-patterns)
4. [The Mint System](#the-mint-system)
5. [The Inference System](#the-inference-system)
6. [The Forge Abstraction (GitHub Code Guide)](#the-forge-abstraction)
7. [GitHub Reusable Workflows & Actions](#github-reusable-workflows--actions)

---

## Project Layout

```
fullsend/
├── cmd/fullsend/main.go          # Entry point — delegates to internal/cli
├── internal/                     # All business logic (Go internal visibility)
│   ├── cli/                      # Cobra commands (admin, mint, inference, github, run, scan)
│   ├── config/                   # YAML config parsing and validation
│   ├── appsetup/                 # GitHub App manifest creation flow
│   ├── forge/                    # Git forge abstraction (interface)
│   │   └── github/               # GitHub REST API implementation
│   ├── mintcore/                 # Shared token mint library (verifiers, handler, GitHub JWT)
│   ├── mint/                     # Cloud Function entry point (wires mintcore)
│   ├── inference/                # Inference provider interface
│   │   └── vertex/               # Vertex AI provider implementation
│   ├── layers/                   # Composable install/uninstall stack
│   ├── dispatch/                 # Dispatch infrastructure interface
│   │   └── gcf/                  # GCP Cloud Function provisioner
│   ├── runtime/                  # Agent runtime (Claude process management)
│   ├── sandbox/                  # OpenShell container orchestration
│   ├── security/                 # Input/output scanner pipeline
│   ├── harness/                  # Harness YAML parsing
│   ├── resolve/                  # URL resolution with integrity hashes
│   ├── skill/                    # SKILL.md frontmatter parser
│   ├── envfile/                  # .env file loader
│   ├── fetch/                    # HTTP fetch with caching and audit
│   ├── scaffold/                 # Embedded .fullsend repo template
│   ├── sticky/                   # Sticky comment management
│   ├── gcp/                      # GCP API helpers
│   ├── netutil/                  # Network utilities
│   ├── sentencetoken/            # Sentence tokenization
│   └── ui/                       # Terminal output formatting
├── hack/                         # One-off utility scripts
├── e2e/                          # End-to-end Playwright tests
├── .github/
│   ├── workflows/                # Reusable dispatch + stage workflows
│   └── actions/                  # Composite actions (mint-token, setup-gcp)
├── action.yml                    # Top-level composite action for external use
├── Makefile                      # Build, test, lint targets
└── go.mod                        # Module: github.com/fullsend-ai/fullsend
```

Key rules:

- **Everything is `internal/`**. No public Go API — the CLI binary is the only consumer.
- **`cmd/` is a thin shell**. `main.go` calls `cli.Execute()` and prints errors to stderr.
- **`hack/` is throwaway**. Utility scripts that aren't part of the product.
- **`e2e/` is optional**. Run with `make e2e-test` when touching appsetup, forge, cli, or layers.

---

## Go Conventions

### Error Handling

Every layer adds context with `%w` for error chain traversal:

```go
if err := doSomething(); err != nil {
    return fmt.Errorf("doing something: %w", err)
}
```

Sentinel errors are package-level variables, not strings:

```go
// In forge package
var ErrNotFound = errors.New("not found")

// In consumer
if errors.Is(err, forge.ErrNotFound) {
    // handle 404
}
```

Never `panic` for recoverable errors. CLI commands use `RunE` (not `Run`) to return errors up to `main`.

### Interface Design

Interfaces are small, behavior-focused, and defined where they're consumed:

```go
// internal/mintcore/interfaces.go

type HTTPDoer interface {
    Do(req *http.Request) (*http.Response, error)
}

type OIDCVerifier interface {
    Verify(ctx context.Context, rawToken string) (*Claims, error)
}

type PEMAccessor interface {
    AccessPEM(ctx context.Context, org, role string) ([]byte, error)
}
```

This follows the Go proverb: "Accept interfaces, return structs." Interfaces live next to the code that depends on them, not next to implementations.

### Dependency Injection

Dependencies are injected through constructors — no global state, no service locators:

```go
func NewHandler(pemAccessor PEMAccessor, oidcVerifier OIDCVerifier) (*Handler, error) {
    // read env vars, validate, return configured handler
}
```

For optional configuration, use the fluent builder pattern:

```go
func NewSetup(client forge.Client, prompter Prompter, browser BrowserOpener, printer *ui.Printer) *Setup {
    return &Setup{
        client:   client,
        prompter: prompter,
        browser:  browser,
        ui:       printer,
        appSet:   DefaultAppSet,
    }
}

func (s *Setup) WithKnownSlugs(slugs map[string]string) *Setup {
    s.knownSlugs = slugs
    return s
}
```

### CLI Framework (Cobra)

Commands use the builder pattern with `RunE` for error propagation:

```go
func newInstallCmd() *cobra.Command {
    var agents string
    var dryRun bool

    cmd := &cobra.Command{
        Use:   "install <org-or-owner/repo>",
        Short: "Install fullsend for an organization or repository",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            target := args[0]
            // validation and business logic
            return nil
        },
    }

    cmd.Flags().StringVar(&agents, "agents", "", "comma-separated agent roles")
    cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without applying")
    return cmd
}
```

Token resolution follows a fixed chain: `GH_TOKEN` → `GITHUB_TOKEN` → `gh auth token`.

### Immutability for Shared Data

When a package exposes data that shouldn't be mutated, return deep copies:

```go
// internal/mintcore/github.go

// unexported canonical data
var canonicalRolePermissions = map[string]map[string]string{
    "triage": {"contents": "read", "issues": "write"},
    "coder":  {"contents": "write", "pull_requests": "write"},
    // ...
}

// exported accessor returns a copy
func RolePermissions() map[string]map[string]string {
    result := make(map[string]map[string]string, len(canonicalRolePermissions))
    for role, perms := range canonicalRolePermissions {
        cp := make(map[string]string, len(perms))
        for k, v := range perms {
            cp[k] = v
        }
        result[role] = cp
    }
    return result
}
```

### Context Propagation

All long-running or cancellable operations accept `context.Context` as first parameter:

```go
func (v *JWKSVerifier) Verify(ctx context.Context, rawToken string) (*Claims, error)
func (p *Provider) Provision(ctx context.Context) (map[string]string, error)
func (c *LiveClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error
```

### Validation

Validation functions are standalone, reusable, and separated from business logic:

```go
// internal/mintcore/patterns.go
var (
    GitHubOrgPattern = `^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`
    RepoNamePattern  = `^[a-zA-Z0-9_.][a-zA-Z0-9._-]{0,99}$`
    RolePattern      = `^[a-z][a-z0-9_-]*$`
)

func ValidateOrgName(org string) error {
    if !regexp.MustCompile(GitHubOrgPattern).MatchString(org) {
        return fmt.Errorf("invalid org name %q", org)
    }
    if strings.Contains(org, "--") {
        return fmt.Errorf("org name %q must not contain double-hyphens", org)
    }
    return nil
}
```

Validate early in CLI commands (before irreversible operations) and at system boundaries (HTTP handlers, config parsing).

### Build & Lint

```bash
make go-build         # Build binary to ./bin/fullsend
make go-test          # go test -race -cover ./...
make go-vet           # go vet
make go-lint          # golangci-lint run
make go-fmt           # gofmt -l -w
make go-tidy          # go mod tidy
```

The linter config (`.golangci.yml`) enables a focused set: `errcheck`, `govet`, `staticcheck`, `unused`, `gosimple`, `ineffassign`. Tests always run with `-race` to catch concurrency bugs.

---

## Testing Patterns

### Assertions: testify

The project uses `stretchr/testify` throughout. Use `require` for setup that must succeed (fails immediately) and `assert` for the actual assertions under test (continues to report all failures):

```go
func TestProvision(t *testing.T) {
    p := vertex.New(vertex.Config{
        ProjectID:   "my-project",
        Region:      "global",
        WIFProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/prov",
    })

    secrets, err := p.Provision(context.Background())
    require.NoError(t, err)                                    // setup must pass
    assert.Equal(t, "my-project", secrets["FULLSEND_GCP_PROJECT_ID"])  // assertion
    assert.Equal(t, 2, len(secrets))                           // assertion
}
```

### Table-Driven Tests

Use `t.Run` with table structs for testing multiple scenarios through the same code path:

```go
func TestValidateOrgName(t *testing.T) {
    tests := []struct {
        name    string
        org     string
        wantErr bool
    }{
        {"valid simple", "acme", false},
        {"valid with hyphens", "my-org", false},
        {"double hyphen rejected", "my--org", true},
        {"too long", strings.Repeat("a", 40), true},
        {"empty", "", true},
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            err := mintcore.ValidateOrgName(tc.org)
            if tc.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

### Test Helpers

Mark helper functions with `t.Helper()` so test failure messages reference the caller, not the helper:

```go
func writeEnvFile(t *testing.T, content string) string {
    t.Helper()
    path := filepath.Join(t.TempDir(), ".env")
    require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
    return path
}
```

Use `t.TempDir()` for automatic cleanup and `t.Setenv()` for test-scoped environment variables.

### Test Doubles (Fakes)

Test doubles implement the interface with configurable behavior — no mocking frameworks:

```go
type fakeOIDCVerifier struct {
    claims *mintcore.Claims
    err    error
}

func (f *fakeOIDCVerifier) Verify(_ context.Context, _ string) (*mintcore.Claims, error) {
    return f.claims, f.err
}

type fakePEMAccessor struct {
    pems map[string][]byte
    err  error
}

func (f *fakePEMAccessor) AccessPEM(_ context.Context, org, role string) ([]byte, error) {
    if f.err != nil {
        return nil, f.err
    }
    key := org + "/" + role
    pem, ok := f.pems[key]
    if !ok {
        return nil, fmt.Errorf("no PEM for %s", key)
    }
    return pem, nil
}
```

For the forge layer, `forge.FakeClient` provides a full in-memory fake with maps for tracking created secrets, variables, files, and injecting errors:

```go
fake := &forge.FakeClient{
    Secrets:        map[string]string{},
    Variables:      map[string]string{},
    CreatedSecrets: map[string]string{},
    Errors:         map[string]error{},
}
layer := layers.NewInferenceLayer("acme", fake, provider, printer)
```

### Integration Tests with httptest

For HTTP-based components, use `httptest.NewServer` to create local test servers:

```go
func newTestOIDCEnv(t *testing.T) *testOIDCEnv {
    t.Helper()
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    require.NoError(t, err)

    env := &testOIDCEnv{key: key, kid: "test-key-1"}
    mux := http.NewServeMux()
    mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(map[string]interface{}{
            "issuer":   env.server.URL,
            "jwks_uri": env.server.URL + "/.well-known/jwks",
        })
    })
    mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
        // return JWKS with the test key
    })
    env.server = httptest.NewServer(mux)
    t.Cleanup(env.server.Close)
    return env
}
```

---

## The Mint System

The token mint is fullsend's authentication backbone. It exchanges GitHub Actions OIDC tokens for short-lived, role-scoped GitHub App installation tokens.

### Architecture Overview

```
GitHub Actions Workflow
    │
    │  1. Request OIDC token (ACTIONS_ID_TOKEN_REQUEST_URL)
    ▼
┌──────────────────────────────┐
│  POST /v1/token              │
│  Bearer: <oidc-jwt>          │
│  Body: {role, repos}         │
│                              │
│  Token Mint (GCP Cloud Fn)   │
│  ┌────────────────────────┐  │
│  │ OIDCVerifier.Verify()  │──┼── Validate JWT (STS or JWKS)
│  │                        │  │
│  │ PEMAccessor.AccessPEM()│──┼── Fetch App PEM from Secret Manager
│  │                        │  │
│  │ GenerateAppJWT()       │──┼── Sign JWT with App's RSA key
│  │                        │  │
│  │ FindInstallation()     │──┼── Lookup GitHub App installation
│  │                        │  │
│  │ CreateInstallToken()   │──┼── Create scoped installation token
│  └────────────────────────┘  │
│                              │
│  Response: {token,           │
│    expires_at,               │
│    granted_repos,            │
│    granted_permissions}      │
└──────────────────────────────┘
```

### Package Structure

The mint is split into three layers:

| Package | Role | Depends on |
|---------|------|-----------|
| `internal/mintcore/` | Shared library: interfaces, handler, verifiers, GitHub API, validation | stdlib only |
| `internal/mint/` | Cloud Function entry point: wires mintcore components | mintcore |
| `internal/cli/mint.go` | CLI commands: `deploy`, `enroll`, `unenroll`, `status` | mintcore (types/validation only) |

**mintcore is a pure library** — it has no dependency on mint or cli. This makes it independently testable.

### Core Interfaces

```go
// internal/mintcore/interfaces.go

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
    Do(req *http.Request) (*http.Response, error)
}

// OIDCVerifier validates OIDC tokens and returns parsed claims.
// Two implementations: JWKSVerifier (direct) and STSVerifier (via GCP).
type OIDCVerifier interface {
    Verify(ctx context.Context, rawToken string) (*Claims, error)
}

// PEMAccessor retrieves agent PEM keys by org and role.
// Implementation: GCPSecretPEMAccessor (Secret Manager).
type PEMAccessor interface {
    AccessPEM(ctx context.Context, org, role string) ([]byte, error)
}
```

### Strategy Pattern: OIDC Verification

Two interchangeable verifiers satisfy the same interface:

**JWKSVerifier** — validates JWTs directly using cached JWKS keys:

```go
verifier := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
    IssuerURL:            "https://token.actions.githubusercontent.com",
    Audience:             "fullsend-mint",
    HTTPClient:           http.DefaultClient,
    AllowedOrgs:          []string{"acme"},
    AllowedWorkflowFiles: []string{"agent.yml"},
})
```

Notable implementation details:
- JWKS keys cached for 1 hour with `sync/singleflight` to prevent thundering herd
- Missing key ID triggers refresh only if >30s since last miss
- Maximum staleness of 24 hours before keys are rejected

**STSVerifier** — delegates cryptographic validation to GCP Security Token Service:

```go
verifier := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
    HTTPClient:         http.DefaultClient,
    GCPProjectNum:      "123456",
    WIFPoolName:        "fullsend-pool",
    DefaultWIFProvider: "gh-actions",
    AllowedOrgs:        []string{"acme"},
})
```

Defense in depth: prevalidates claims (issuer, audience, expiry, org, workflow ref) before the STS exchange. This catches malformed tokens early without an API call.

### Handler: HTTP Request Processing

The `Handler` is the HTTP entry point for the mint Cloud Function:

```go
handler, err := mintcore.NewHandler(pemAccessor, oidcVerifier)
```

Routes:

| Path | Method | Auth | Purpose |
|------|--------|------|---------|
| `/health` | GET | none | Liveness probe |
| `/v1/status` | GET | Bearer OIDC | Diagnostic: org name + available roles |
| `/v1/token` | POST | Bearer OIDC | Mint a scoped token |

The `/v1/token` request body:

```json
{
  "role": "coder",
  "repos": ["my-repo", "another-repo"]
}
```

Validation enforces: role must be in the allowed set, repos array 1-500 entries, each repo name matches `RepoNamePattern`, no `..` in names.

### Role Permissions

Each role maps to a fixed set of GitHub permissions. Tokens are always downscoped to these — the App's full permissions are never exposed:

```go
var canonicalRolePermissions = map[string]map[string]string{
    "triage":     {"contents": "read", "issues": "write", "pull_requests": "read"},
    "coder":      {"contents": "write", "pull_requests": "write", "issues": "write"},
    "review":     {"contents": "read", "pull_requests": "write"},
    "fix":        {"contents": "write", "pull_requests": "write", "issues": "write"},
    "retro":      {"actions": "read", "contents": "read", "issues": "write"},
    "prioritize": {"contents": "read", "issues": "write"},
    "fullsend":   {"actions": "write", "contents": "write", "workflows": "write"},
}
```

### GitHub App JWT Generation

The mint signs JWTs with the App's RSA private key:

```go
func GenerateAppJWT(appID string, pemData []byte) (string, error)
```

- Parses PKCS1 or PKCS8 PEM
- Creates RS256 JWT: `{iss: appID, iat: now-60s, exp: now+10m}`
- Signs with SHA256+RSA

### Claims Validation

```go
type Claims struct {
    Issuer          string
    Audience        Audience  // custom unmarshaler handles string or []string
    IssuedAt        int64
    Expiry          int64
    Repository      string    // "acme/widget"
    RepositoryOwner string    // "acme"
    JobWorkflowRef  string    // "fullsend-ai/fullsend/.github/workflows/agent.yml@main"
}
```

Validation checks:
1. **Org allowed** — case-insensitive membership in allowlist
2. **Workflow ref** — must originate from org's `.fullsend` repo, upstream `fullsend-ai/fullsend`, or a registered per-repo WIF entry
3. **Time bounds** — expiry and issued-at with 30s clock skew tolerance

### Security Patterns

- **Cross-org check**: `FindInstallation()` verifies `account.login` matches expected org
- **PEM zeroing**: Memory zeroed on `defer` after use to prevent key leakage
- **Error typing**: `mintError` wraps HTTP status codes for clean response mapping
- **Environment validation at startup**: Cloud Function `init()` validates all env vars, fails fast

### Cloud Function Wiring

```go
// internal/mint/main.go

func init() {
    // 1. Parse and validate env vars (ALLOWED_ORGS, ROLE_APP_IDS, etc.)
    // 2. Create STSVerifier with config
    // 3. Create GCPSecretPEMAccessor
    // 4. Create Handler with verifier + pemAccessor
    // 5. Register: functions.HTTP("ServeHTTP", handler.ServeHTTP)
}
```

> **Sync requirement**: Changes to `internal/mint/main.go` must be copied to `internal/dispatch/gcf/mintsrc/main.go.embed`. Changes to `internal/mintcore/` files must be synced to `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. See `CLAUDE.md` for details.

---

## The Inference System

The inference system manages AI model credentials. It provisions Workload Identity Federation (WIF) so GitHub Actions workflows can authenticate to Vertex AI without storing long-lived keys.

### Provider Interface

```go
// internal/inference/inference.go

type Provider interface {
    Name() string                                     // e.g. "vertex"
    Provision(ctx context.Context) (map[string]string, error)  // returns secrets
    SecretNames() []string                            // names of secrets this provider manages
    Variables() map[string]string                     // non-secret name/value pairs
}
```

Key contract: `Provision()` must be idempotent — the install command calls it on every run.

### Vertex AI Implementation

```go
// internal/inference/vertex/vertex.go

type Config struct {
    ProjectID   string  // GCP project ID (required)
    Region      string  // e.g. "global" (required)
    WIFProvider string  // full WIF provider resource name
}

type Provider struct {
    cfg Config
}

func New(cfg Config) *Provider
func NewAnalyzeOnly() *Provider  // read-only mode for status checks
```

Secret and variable names:

| Name | Type | Value |
|------|------|-------|
| `FULLSEND_GCP_PROJECT_ID` | Secret | GCP project ID |
| `FULLSEND_GCP_WIF_PROVIDER` | Secret | Full WIF provider resource name |
| `FULLSEND_GCP_REGION` | Variable | GCP region (e.g. "global") |

### Layer Integration

The inference provider is wrapped in a `Layer` for composability with the install stack:

```go
// internal/layers/inference.go

type InferenceLayer struct {
    org      string
    client   forge.Client
    provider inference.Provider
    ui       *ui.Printer
}
```

**Install flow**:
1. `provider.Provision(ctx)` → acquires credential map
2. `client.CreateRepoSecret()` for each secret (unconditional upsert)
3. `client.CreateOrUpdateRepoVariable()` for non-secret values

**Analyze flow**:
1. Checks all secrets exist via `client.RepoSecretExists()`
2. Checks all variables exist via `client.RepoVariableExists()`
3. Returns `StatusInstalled` / `StatusNotInstalled` / `StatusDegraded`

Nil provider is handled gracefully — returns `StatusInstalled` with "no inference provider configured".

### CLI Wiring

```go
// internal/cli/admin.go (simplified)

var inferenceProvider inference.Provider
if inferenceProject != "" {
    vcfg := vertex.Config{
        ProjectID:   inferenceProject,
        Region:      inferenceRegion,
        WIFProvider: inferenceWIFProvider,
    }
    inferenceProvider = vertex.New(vcfg)
}

stack.Add(layers.NewInferenceLayer(org, client, inferenceProvider, printer))
```

Flags: `--inference-project`, `--inference-region`, `--inference-wif-provider`. The WIF provider is auto-provisioned if omitted (using `gcf.Provisioner`).

### Adding a New Inference Provider

1. Create `internal/inference/newprovider/newprovider.go` implementing `inference.Provider`
2. Add the provider name to `config.ValidProviders()`
3. Add CLI flags and wiring in `internal/cli/admin.go`
4. Write tests (see `internal/inference/vertex/vertex_test.go` for reference)

---

## The Forge Abstraction

All GitHub API operations go through `forge.Client` — direct `exec.Command("gh")` or raw HTTP calls are forbidden outside `internal/forge/github/`.

### Interface

The `forge.Client` interface is defined in `internal/forge/forge.go` and covers:

| Category | Methods |
|----------|---------|
| **Repositories** | `ListOrgRepos`, `GetRepo`, `CreateRepo`, `DeleteRepo` |
| **Files** | `CreateFile`, `CreateOrUpdateFile`, `GetFileContent`, `DeleteFile`, `CommitFiles` |
| **Branches** | `CreateBranch`, `CreateFileOnBranch`, `CreateOrUpdateFileOnBranch` |
| **Pull requests** | `CreateChangeProposal`, `ListRepoPullRequests`, `MergeChangeProposal` |
| **Reviews** | `CreatePullRequestReview`, `ListPullRequestReviews`, `DismissPullRequestReview` |
| **Issues** | `CreateIssue`, `CloseIssue`, `ListOpenIssues`, `ListIssueComments`, `CreateIssueComment` |
| **Workflows** | `DispatchWorkflow`, `GetLatestWorkflowRun`, `ListWorkflowRuns` |
| **Secrets & Variables** | `CreateRepoSecret`, `RepoSecretExists`, `CreateOrUpdateRepoVariable`, `RepoVariableExists` |
| **Auth** | `GetAuthenticatedUser`, `GetTokenScopes` |

### GitHub Implementation

`internal/forge/github/github.go` implements `forge.Client` using the GitHub REST API:

```go
type LiveClient struct {
    httpClient *http.Client
    token      string
    baseURL    string  // default: "https://api.github.com"
}
```

Key behaviors:

- **API version**: Uses `X-GitHub-Api-Version: 2022-11-28`
- **Retry logic**: Max 3 retries on rate limit (HTTP 429) with exponential backoff
- **Error typing**: `APIError` with `StatusCode`, `Message`, `Details`
- **404 handling**: Returns `forge.ErrNotFound` for consistent sentinel error checking

### Atomic Multi-File Commits

`CommitFiles` uses the Git Trees API for atomic operations:

```go
type TreeFile struct {
    Path    string  // "scripts/post-code.sh"
    Content string
    Mode    string  // "100644" (regular) or "100755" (executable)
}

func (c *LiveClient) CommitFiles(ctx context.Context, owner, repo, branch, message string, files []TreeFile) (bool, error)
```

The method is idempotent — returns `(false, nil)` if the tree already matches HEAD (no-op).

### FakeClient for Testing

`forge.FakeClient` provides an in-memory implementation for unit tests:

```go
fake := &forge.FakeClient{
    Secrets:        map[string]string{"EXISTING_SECRET": "val"},
    Variables:      map[string]string{},
    CreatedSecrets: map[string]string{},           // tracks what was created
    VariablesExist: map[string]bool{},
    Errors:         map[string]error{"BadOp": fmt.Errorf("boom")},  // inject errors
}
```

### Adding a New Forge Operation

1. Add the method signature to `forge.Client` in `internal/forge/forge.go`
2. Implement it in `internal/forge/github/github.go`
3. Add it to `forge.FakeClient` (return zero value or use the `Errors` map)
4. Write tests using `FakeClient`

---

## GitHub Reusable Workflows & Actions

The workflow system is how fullsend runs agents in response to GitHub events. It's built from three layers: thin shims in enrolled repos, a central dispatch router, and per-stage reusable workflows.

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Enrolled Repo                                                   │
│  .github/workflows/fullsend.yaml  (thin shim)                  │
│    ↓ workflow_call                                               │
├──────────────────────────────────────────────────────────────────┤
│  Central Dispatch                                                │
│  reusable-dispatch.yml  (routing logic)                         │
│    ↓ determines stage from event type + payload                  │
│    ↓ conditional jobs per stage                                  │
├──────────────────────────────────────────────────────────────────┤
│  Stage Workflows                                                 │
│  reusable-{triage,code,review,fix,retro,prioritize}.yml         │
│    ↓ checkout config repo + upstream defaults                    │
│    ↓ layer workspace (defaults → org → per-repo overrides)       │
│    ↓ mint role-scoped token                                      │
│    ↓ checkout target repo                                        │
│    ↓ invoke action.yml composite                                 │
│    ↓ run post-script                                             │
└─────────────────────────────────────────────────────────────────┘
```

### Shim Workflow

Deployed to enrolled repos. Thin event forwarder — all logic lives upstream:

**Triggers:**

| Event | Actions | Routes to |
|-------|---------|-----------|
| `issues` | opened, edited, labeled | triage / code / review |
| `issue_comment` | created | Slash commands: `/fs-triage`, `/fs-code`, `/fs-review`, `/fs-fix`, `/fs-retro`, `/fs-prioritize` |
| `pull_request_target` | opened, synchronize, ready_for_review | review |
| `pull_request_target` | closed | retro |
| `pull_request_review` | submitted (changes_requested from review bot) | fix |

Uses `pull_request_target` (not `pull_request`) to run the BASE branch version, preventing PR authors from tampering with workflow code.

### Dispatch Router

`reusable-dispatch.yml` determines which stage to run:

```
SLASH COMMAND       CONDITION                              STAGE
/fs-triage          always                                 triage
/fs-code            issue has no PR                        code
/fs-review          always                                 review
/fs-fix             issue is a PR, user authorized         fix
/fs-retro           user authorized                        retro
/fs-prioritize      user authorized                        prioritize

AUTO TRIGGERS
issue opened/edited                                        triage
issue labeled ready-to-code                                code
issue labeled ready-for-review                             review
PR opened/sync/ready                                       review
PR closed                                                  retro
review changes_requested (from org review bot)             fix
```

The router also:
- Checks the `.fullsend/config.yaml` kill switch
- Validates role enablement (maps stages to roles: code/fix→coder, retro/prioritize→fullsend)
- Blocks fork PRs for the fix stage (security)
- Sanitizes event payloads (4096-char limit on comments)

### Stage Workflow Anatomy

Each `reusable-{stage}.yml` follows the same template:

```yaml
# 1. Checkout config repository (.fullsend)
- uses: actions/checkout@v4
  with:
    repository: ${{ inputs.source_repo }}

# 2. Checkout upstream defaults (sparse)
- uses: actions/checkout@v4
  with:
    repository: fullsend-ai/fullsend
    sparse-checkout: |
      .github/actions/
      internal/scaffold/

# 3. Layer workspace (upstream defaults + org customizations + per-repo overrides)

# 4. Validate enrollment
- uses: ./.github/actions/validate-enrollment

# 5. Mint role-scoped token
- uses: ./.github/actions/mint-token
  with:
    role: coder  # stage-specific
    repos: ${{ steps.enrollment.outputs.name }}
    mint_url: ${{ inputs.mint_url }}

# 6. Checkout target repository with scoped token
- uses: actions/checkout@v4
  with:
    token: ${{ steps.mint.outputs.token }}

# 7. Setup GCP & OIDC (for Vertex AI inference)
- uses: ./.github/actions/setup-gcp

# 8. Setup agent environment (from harness/)

# 9. Invoke the composite action
- uses: fullsend-ai/fullsend@v0
  with:
    agent: coder
    target-repo: ${{ github.workspace }}/target-repo

# 10. Run post-script (create PRs, post reviews, merge, etc.)
```

### Composite Actions

Three reusable primitives in `.github/actions/`:

**`mint-token/action.yml`** — Exchanges OIDC for a role-scoped GitHub token:
1. Obtain GitHub OIDC token via `ACTIONS_ID_TOKEN_REQUEST_URL`
2. POST to mint with `{role, repos}`
3. Output: `token` (masked in logs)

**`setup-gcp/action.yml`** — Authenticates to GCP via WIF:
1. Exchange OIDC token for GCP access token
2. Authenticate `gcloud` CLI
3. Set `GOOGLE_APPLICATION_CREDENTIALS` for Vertex AI

**`validate-enrollment/action.yml`** — Confirms the repo is enrolled:
1. Per-org mode: check `config.yaml`
2. Per-repo mode: skip (self-enrolled)

### Top-Level Composite Action

`action.yml` at the repo root is the entry point for external repos:

```yaml
inputs:
  agent: # Agent name (triage, code, review, fix, retro, prioritize)
  version: # CLI release version (default: latest)
  fullsend-dir: # Path to .fullsend config
  target-repo: # Target repo checkout path
  github_token: # For authenticated API calls

steps:
  # 1. Install fullsend CLI (check vendored binary first, then download)
  # 2. Install OpenShell CLI & gateway
  # 3. Configure Podman & gateway (rootless containers)
  # 4. Pre-pull sandbox image
  # 5. Invoke: fullsend run <agent-name>
  # 6. Upload output artifacts
```

The CLI is installed with retry logic (3 attempts, exponential backoff). Vendored binaries at `.fullsend/bin/fullsend` take priority over downloads.

### Workspace Layering

Agent configuration is resolved through three layers (later overrides earlier):

```
Layer 1: Upstream defaults (fullsend-ai/fullsend)
    agents/, skills/, harness/, policies/, scripts/, env/
        ↓
Layer 2: Org-level customizations (.fullsend config repo)
    customized/agents/, customized/skills/, etc.
        ↓
Layer 3: Per-repo overrides (.fullsend/ directory in target repo)
    .fullsend/agents/, .fullsend/skills/, etc.
```

This allows organizations to maintain baseline configurations while individual repos can customize agent behavior.

### Adding a New Stage

1. Define the role permissions in `internal/mintcore/github.go` (`canonicalRolePermissions`)
2. Add routing logic in `reusable-dispatch.yml`
3. Create `reusable-{stage}.yml` following the existing template
4. Add a harness YAML in `harness/{stage}.yaml`
5. Create the post-script in `scripts/post-{stage}.sh`
6. Update the shim workflow to forward the new event pattern

### Key Design Decisions

- **Thin caller pattern**: Enrolled repos contain minimal config. All workflow logic lives upstream so fullsend updates don't require per-repo PRs.
- **OIDC-based authentication**: No PAT tokens stored in repos. Each role gets minimal scopes via ephemeral installation tokens.
- **`pull_request_target`**: Runs the base branch version of workflows, preventing PR authors from modifying security-critical dispatch logic.
- **Post-scripts are mandatory**: The `--no-post-script` flag is never exposed as a workflow input. All agent output goes through validation before being posted.

---

## See Also

- [Local Development](local-dev.md) — Development environment setup
- [CLI Internals](cli-internals.md) — Command structure, installation pipeline, sandbox runtime
- [Testing Workflow Changes](testing-workflows.md) — Point a live GitHub org at a branch to test changes
- [Architecture](../../architecture.md) — System design overview
- [CONTRIBUTING.md](../../../CONTRIBUTING.md) — Commit conventions, PR process
