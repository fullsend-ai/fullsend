# CLI Internals

This guide provides implementation details for fullsend CLI internals: command structure, installation pipeline, sandbox runtime, and key source files. For local development setup, see [Local Development](local-dev.md).

## CLI Command Tree

```
fullsend
├── admin
│   ├── install     # Per-org or per-repo infrastructure setup
│   ├── uninstall   # Tear down infrastructure (reverse layer order)
│   ├── analyze     # Health check: inspect installed state
│   ├── enable
│   │   └── repos   # Enable agent on repos (per-org mode)
│   └── disable
│       └── repos   # Disable agent on repos (per-org mode)
├── run             # Execute an agent in a sandbox
├── scan            # Run security scanner on input/output
│   ├── input       # Scan event payload for prompt injection
│   ├── output      # Scan agent output for secrets
│   ├── context     # Scan context files for injection
│   └── url         # Validate URLs for SSRF
├── post-review     # Post PR review comments to GitHub
└── post-comment    # Post issue/PR comments to GitHub
```

### Token Resolution Chain

All commands that interact with GitHub resolve authentication in this order:

```
GH_TOKEN env var  →  GITHUB_TOKEN env var  →  `gh auth token` CLI
```

### Install Mode Detection

The `install` command auto-detects mode from the positional argument:

```
fullsend admin install <org>              → Per-org mode (full infrastructure)
fullsend admin install <owner>/<repo>     → Per-repo mode (single repo bootstrap)
```

---

## Unified Installation Flow

Both per-org and per-repo modes share the same core pipeline. The code follows the same phases in the same order — the only differences are *where* artifacts land and *scope* of WIF/enrollment.

### Shared Pipeline

```
┌─────────────────────────────────────────────────────────────────┐
│              Unified Install Pipeline (both modes)               │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  fullsend admin install <target>                                │
│  ┌──────────────────────┐                                       │
│  │ Parse target          │                                      │
│  │  "acme"      → org   │                                      │
│  │  "acme/repo" → repo  │                                      │
│  └──────────┬───────────┘                                       │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 1: Discover (read-only)                              │ │
│  │                                                            │ │
│  │  a. Discover mint   --mint-url / --mint-project / default  │ │
│  │     └─ DiscoverMint() → check if GCF exists, get URL      │ │
│  │  b. Resolve existing app IDs from mint env vars            │ │
│  │     └─ ROLE_APP_IDS → skip app creation if all present     │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 2: App setup (shared: runAppSetup)                   │ │
│  │                                                            │ │
│  │  For each role in --agents:                                │ │
│  │    - Create/reuse GitHub App ({appSet}-{role} via --app-set)│ │
│  │    - Download PEM key from App creation flow               │ │
│  │    - Store PEM in GCP Secret Manager                       │ │
│  │    - Record App ID + Client ID                             │ │
│  │                                                            │ │
│  │  Shared code: runAppSetup() → []AgentCredentials           │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 3: Mint provisioning                                 │ │
│  │                                                            │ │
│  │  If mint not found → deploy GCF (Provision)                │ │
│  │  If mint exists    → register org (EnsureOrgInMint)        │ │
│  │                    → store PEMs in Secret Manager          │ │
│  │                                                            │ │
│  │  Both modes use gcf.NewProvisioner with same Config{}      │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-repo adds: RegisterPerRepoWIF()      │              │ │
│  │  │ (adds repo to PER_REPO_WIF_REPOS env)    │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 4: WIF provisioning (inference auth)                 │ │
│  │                                                            │ │
│  │  Both modes: ProvisionWIF() → create pool, provider, IAM   │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-org:  org-wide WIF provider           │              │ │
│  │  │ Per-repo: repo-scoped (BuildRepoProviderID)│             │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 5: Write scaffold + config files                     │ │
│  │                                                            │ │
│  │  Both modes: write workflow files + customized/ dirs       │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-org:  create .fullsend config repo    │              │ │
│  │  │           push reusable workflows         │              │ │
│  │  │           vendor fullsend binary (opt)    │              │ │
│  │  │                                           │              │ │
│  │  │ Per-repo: write .fullsend/ dir in repo    │              │ │
│  │  │           push shim workflow template     │              │ │
│  │  │           vendor fullsend binary (opt)    │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 6: Set secrets & variables                           │ │
│  │                                                            │ │
│  │  Both modes write the same credential set:                 │ │
│  │    Secrets:   FULLSEND_GCP_PROJECT_ID                      │ │
│  │              FULLSEND_GCP_WIF_PROVIDER                     │ │
│  │    Variables: FULLSEND_GCP_REGION                           │ │
│  │              FULLSEND_MINT_URL                              │ │
│  │                                                            │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-org:  secrets → .fullsend config repo │              │ │
│  │  │           MINT_URL → org variable         │              │ │
│  │  │           + repo var (dot-prefix fix)      │              │ │
│  │  │           + PEM keys as repo secrets       │              │ │
│  │  │           + client IDs as repo variables   │              │ │
│  │  │                                           │              │ │
│  │  │ Per-repo: secrets → target repo            │              │ │
│  │  │           + FULLSEND_PER_REPO_GUARD=true   │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 7: Enrollment (per-org only)                         │ │
│  │                                                            │ │
│  │  Per-org:  enable agent workflows on target repos          │ │
│  │  Per-repo: no-op (single repo, self-contained)             │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Mode Differences

Both modes call the same functions (`runAppSetup`, `gcf.NewProvisioner`, `ProvisionWIF`). The differences are narrow:

| Phase | Shared Code | Per-Org Variation | Per-Repo Variation |
|-------|-------------|-------------------|-------------------|
| **1. Discover** | `DiscoverMint()`, resolve app IDs | Discovers all org repos | Single repo validation |
| **2. App setup** | `runAppSetup()` → PEMs + App IDs | All 7 roles by default | Excludes "fullsend" role |
| **3. Mint** | `gcf.Provision()` or `EnsureOrgInMint()` | — | + `RegisterPerRepoWIF()` |
| **4. WIF** | `ProvisionWIF()` | Org-wide provider ID | `BuildRepoProviderID()` (repo-scoped) |
| **5. Scaffold** | `scaffold.PerRepoCustomizedDirs()` / `WalkFullsendRepo()` | Creates `.fullsend` repo, pushes workflows + optional binary | Writes `.fullsend/` dir + shim workflow + optional binary in target repo |
| **6. Secrets** | Same secret names, same API calls | Config repo + org variable | Target repo + `PER_REPO_GUARD` |
| **7. Enrollment** | — | `EnrollmentLayer` enables repos | No-op (self-contained) |

### Per-Org Layer Stack

Per-org mode wraps phases 5-7 in a `Layer` interface for composability (install forward, uninstall reverse):

```go
type Layer interface {
    Name() string
    RequiredScopes(op Operation) []string
    Install(ctx context.Context) error
    Uninstall(ctx context.Context) error
    Analyze(ctx context.Context) (LayerStatus, string, error)
}
```

```
Stack order:  ConfigRepo → Workflows → VendorBinary → Secrets → Inference → Dispatch → Enrollment
Install:      process 1→7 (forward)
Uninstall:    process 7→1 (reverse)
```

Per-repo mode does not use the layer stack — it runs the same phases inline in `runPerRepoInstall()` since there's no need for composable uninstall ordering with a single repo. Binary vendoring (when `--vendor-fullsend-binary` is set) and stale binary cleanup are handled inline rather than through `VendorBinaryLayer`.

---

## OpenShell Sandbox Runtime

### Sandbox Lifecycle

```
┌─────────────────────────────────────────────────────────────────┐
│                   Sandbox Lifecycle (run.go)                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐                                                │
│  │ Load harness │ Parse YAML config for agent                   │
│  └──────┬──────┘                                                │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ EnsureAvailable() │ Verify openshell binary exists           │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ EnsureGateway()   │ Start/verify gateway service             │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ EnsureProvider()  │ Register inference provider              │
│  │                   │ (bare-key credential form)               │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Pre-script        │ Run harness.pre_script (host-side)       │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Create()          │ openshell sandbox create                  │
│  │                   │ --image {harness.image}                   │
│  │                   │ Returns sandbox ID                        │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────────────────────────────┐                   │
│  │ bootstrapSandbox()                       │                   │
│  │                                          │                   │
│  │  Upload to /tmp/workspace:               │                   │
│  │  ├── fullsend binary (cross-compiled)    │                   │
│  │  ├── agent definition file               │                   │
│  │  ├── skills/ directory                   │                   │
│  │  ├── plugins/ directory                  │                   │
│  │  ├── host_files (expanded ${VAR} paths)  │                   │
│  │  ├── .env file (bootstrapEnv)            │                   │
│  │  └── security hooks                      │                   │
│  │                                          │                   │
│  │  bootstrapEnv() writes:                  │                   │
│  │  ├── PATH=/tmp/workspace/bin:$PATH       │                   │
│  │  ├── CLAUDE_CONFIG_DIR=/tmp/claude-config│                   │
│  │  ├── FULLSEND_OUTPUT_DIR=...             │                   │
│  │  └── sources .env.d/*.env files          │                   │
│  └──────────┬───────────────────────────────┘                   │
│             ▼                                                   │
│  ┌──────────────────┐                                           │
│  │ Copy source code  │ Upload target repo to sandbox            │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Security scan     │ Run host-side scanners on input          │
│  │ (input)           │ (injection detection, SSRF, etc.)        │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────────────────────────────┐                   │
│  │ Exec() — Run agent in sandbox            │                   │
│  │                                          │                   │
│  │ Command built by buildClaudeCommand():   │                   │
│  │  cd {repoDir} &&                         │                   │
│  │  . {envFile} &&                          │                   │
│  │  claude --print --verbose                │                   │
│  │    --output-format stream-json           │                   │
│  │    --model {model}                       │                   │
│  │    --agent {agent}                       │                   │
│  │    --dangerously-skip-permissions        │                   │
│  │    'Run the agent task'                  │                   │
│  │                                          │                   │
│  │ Background: OIDC token refresh every 4m  │                   │
│  └──────────┬───────────────────────────────┘                   │
│             ▼                                                   │
│  ┌──────────────────┐                                           │
│  │ Extract output    │ SafeDownload() with sanitization:        │
│  │                   │ - Remove dangerous symlinks (sandbox escape) │
│  │                   │ - Remove .git/hooks/ (hook injection)    │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────────────────────────────┐                   │
│  │ Validation loop (if configured)          │                   │
│  │                                          │                   │
│  │ for i := 0; i < max_iterations; i++ {    │                   │
│  │   run validation script                  │                   │
│  │   if pass → break                        │                   │
│  │   feed feedback → re-run agent           │                   │
│  │ }                                        │                   │
│  └──────────┬───────────────────────────────┘                   │
│             ▼                                                   │
│  ┌──────────────────┐                                           │
│  │ Post-script       │ Run harness.post_script (host-side)      │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Delete()          │ openshell sandbox delete                  │
│  │                   │ Cleanup sandbox resources                │
│  └──────────────────┘                                           │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Sandbox Constants

```go
SandboxWorkspace    = "/tmp/workspace"
SandboxClaudeConfig = "/tmp/claude-config"
```

### Key Sandbox Operations

| Operation | CLI Command | Purpose |
|-----------|------------|---------|
| `EnsureAvailable()` | Check `openshell` binary | Verify runtime available |
| `EnsureGateway()` | `openshell gateway ...` | Start inference gateway |
| `EnsureProvider()` | `openshell provider ...` | Register model provider (bare-key form) |
| `Create()` | `openshell sandbox create --image ...` | Spin up container |
| `Exec()` | `openshell sandbox exec ...` | Run command in sandbox |
| `ExecStreamReader()` | `openshell sandbox exec ...` | Streaming stdout reader |
| `Upload()` | `openshell sandbox upload ...` | Copy files into sandbox |
| `Download()` | `openshell sandbox download ...` | Copy files out of sandbox |
| `SafeDownload()` | Download + sanitize | Remove dangerous symlinks (absolute or repo-escaping), .git/hooks |
| `CollectLogs()` | Download logs dir | Extract sandbox logs |
| `ExtractTranscripts()` | Download transcripts | Extract conversation transcripts |
| `Delete()` | `openshell sandbox delete` | Destroy container |

### Security: sanitizeDownload()

After downloading files from the sandbox, `sanitizeDownload()` removes:
- **Dangerous symlinks** (absolute targets or targets that escape the repo) — Prevents sandbox escape via symlink-to-host-path attacks; relative in-repo symlinks are kept
- **.git/hooks/** — Prevents hook injection that would execute on the host

---

## Workflow Deployment & Scaffold System

### Scaffold Architecture

The fullsend binary embeds a complete `.fullsend` repo template using Go's `embed.FS`:

```go
//go:embed all:fullsend-repo
var content embed.FS
```

### File Categories

```
fullsend-repo/                      (embedded template)
├── .github/
│   ├── workflows/                  → Pushed to config repo
│   ├── actions/                    → Upstream-only (not installed)
│   └── scripts/                    → Upstream-only (not installed)
├── agents/                         → Layered (runtime, not installed)
├── skills/                         → Layered (runtime, not installed)
├── schemas/                        → Layered (runtime, not installed)
├── harness/                        → Layered (runtime, not installed)
├── policies/                       → Layered (runtime, not installed)
├── scripts/                        → Layered (runtime, not installed)
├── env/                            → Layered (runtime, not installed)
├── templates/
│   └── shim-per-repo.yaml          → Per-repo shim workflow template
└── (other files)                   → Installed to config repo
```

**Three categories:**

| Category | Installed? | Source | Purpose |
|----------|-----------|--------|---------|
| **Installed** | Yes | Scaffold → `.fullsend` repo | Workflows, configs, static files |
| **Layered** | No (runtime) | Upstream reusable workflows | agents/, skills/, harness/, plugins/, policies/, scripts/, schemas/, env/ |
| **Upstream-only** | No | Referenced directly | .github/actions/, .github/scripts/ |

### File Mode Tracking

Since `embed.FS` doesn't preserve Unix permissions, executable files are tracked in a static map:

```go
var executableFiles = map[string]struct{}{
    "scripts/post-code.sh":       {},
    "scripts/pre-triage.sh":      {},
    "scripts/scan-secrets":       {},
    // ... 20+ entries
}
```

`FileMode()` returns `"100755"` for scripts, `"100644"` for everything else. A test (`TestFileModeMatchesFilesystem`) validates this map stays in sync with the actual filesystem.

---

## Complete End-to-End Flow: Issue → Agent Run → PR

```
┌─────────────────────────────────────────────────────────────────┐
│           End-to-End: Issue Triage → Code → Review               │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. Issue created on target repo                                │
│     │                                                           │
│     ▼                                                           │
│  2. GitHub webhook → triage workflow dispatched                 │
│     │                                                           │
│     ▼                                                           │
│  3. Triage workflow calls .fullsend reusable workflow           │
│     │                                                           │
│     ▼                                                           │
│  4. Workflow requests OIDC token (id-token: write)              │
│     │                                                           │
│     ▼                                                           │
│  5. POST /v1/token → Mint validates, returns scoped token       │
│     │                                                           │
│     ▼                                                           │
│  6. fullsend run --agent triage                                 │
│     ├── Load harness/triage.yaml                                │
│     ├── Create sandbox                                          │
│     ├── Bootstrap (binary, agent, skills, env)                  │
│     ├── Run claude in sandbox                                   │
│     ├── Extract output                                          │
│     └── Cleanup sandbox                                         │
│     │                                                           │
│     ▼                                                           │
│  7. Triage agent labels issue, assigns priority                 │
│     │                                                           │
│     ▼                                                           │
│  8. Coder workflow dispatched (label trigger)                   │
│     │                                                           │
│     ▼                                                           │
│  9. Repeat steps 4-6 with role=coder                            │
│     ├── Coder agent creates branch, writes code                 │
│     └── Opens PR via GitHub App bot                             │
│     │                                                           │
│     ▼                                                           │
│  10. Review workflow dispatched (PR trigger)                    │
│     │                                                           │
│     ▼                                                           │
│  11. Repeat steps 4-6 with role=review                          │
│      ├── Review agent examines diff                             │
│      └── Posts review comments via GitHub App bot               │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## Key Source Files Reference

> **Note:** Line counts are approximate and may drift as the codebase evolves.

| File | Lines | Purpose |
|------|-------|---------|
| `internal/cli/root.go` | ~34 | CLI entry point, command registration |
| `internal/cli/admin.go` | ~2415 | Install/uninstall/analyze/enable/disable |
| `internal/cli/run.go` | ~1923 | Agent execution lifecycle |
| `internal/mint/main.go` | ~906 | GCF token mint service |
| `internal/dispatch/gcf/provisioner.go` | ~1350 | GCP infrastructure provisioner |
| `internal/sandbox/sandbox.go` | ~459 | OpenShell sandbox operations |
| `internal/harness/harness.go` | ~486 | Harness YAML parsing |
| `internal/layers/layers.go` | ~159 | Layer interface and stack |
| `internal/layers/secrets.go` | ~200 | PEM key deployment layer |
| `internal/layers/inference.go` | ~150 | Inference credential layer |
| `internal/layers/dispatch.go` | ~364 | Mint URL deployment layer |
| `internal/scaffold/scaffold.go` | ~146 | Embedded template system |
| `internal/inference/inference.go` | ~26 | Provider interface |
| `internal/inference/vertex/vertex.go` | ~80 | Vertex AI implementation |
| `internal/config/config.go` | ~264 | Org/repo config structures |

## See Also

- [Local Development](local-dev.md) - Development environment setup
- [Infrastructure Reference](../admin/infrastructure-reference.md) - Admin infrastructure details
- [Customizing Agents](../user/customizing-agents.md) - User customization guide
