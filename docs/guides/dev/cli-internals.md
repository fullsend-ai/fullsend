# CLI Internals

This guide provides implementation details for fullsend CLI internals: command structure, installation pipeline, sandbox runtime, and key source files. For running agents locally, see [Running agents locally](../user/running-agents-locally.md).

## CLI Command Tree

```
fullsend
├── admin                                    # All-in-one setup (GCP + GitHub)
│   ├── install      <org|owner/repo>        # Full infrastructure setup
│   ├── uninstall    <org>                   # Tear down (reverse layer order)
│   ├── analyze      <org>                   # Health check installed state
│   ├── enable
│   │   └── repos    <org> [repo...]         # Enable agent on repos
│   └── disable
│       └── repos    <org> [repo...]         # Disable agent on repos
├── mint                                     # Token mint management
│   ├── deploy                               # Deploy/update mint Cloud Function
│   ├── add-role       <role>                # Register role PEM + ROLE_APP_IDS entry
│   ├── remove-role    <role>                # Remove role from mint
│   ├── enroll       <org|owner/repo>        # Register org/repo in mint
│   ├── unenroll     <org|owner/repo>        # Remove org/repo from mint
│   ├── status       [org]                   # Inspect mint state and PEM health
│   └── token                                # Mint a short-lived token via OIDC
│       ├── --role <name>                    #   Agent role (triage, coder, review)
│       ├── --repos <list>                   #   Comma-separated repo names
│       ├── --mint-url <url>                 #   Mint service URL ($FULLSEND_MINT_URL)
│       └── --audience <string>              #   OIDC audience (default: fullsend-mint)
├── inference                                # GCP: inference WIF management
│   ├── provision    <org|owner/repo>        # Create WIF pool/provider for Agent Platform
│   ├── deprovision  <org|owner/repo>        # Remove WIF access for org or repo
│   └── status       <org|owner/repo>        # Check WIF health, print config
├── github                                   # GitHub-only configuration
│   ├── setup        <org|owner/repo>        # Configure fullsend (no GCP needed)
│   ├── enroll       <org> [repo...]         # Enable repos for agent workflows
│   ├── unenroll     <org> [repo...]         # Disable repos from agent workflows
│   ├── set          <target> <key> <value>  # Update a config value
│   ├── status       <org>                   # Analyze GitHub-side state
│   ├── uninstall    <org>                   # Remove fullsend GitHub configuration
│   └── sync-scaffold <org>                  # Update workflow templates
├── agent                                    # Manage agent registrations in config
│   ├── add          <url-or-path>            # Register an agent (URL auto-pinned)
│   ├── list                                  # List registered agents
│   ├── update       <name> [sha]             # Re-pin URL agent to new commit SHA
│   ├── remove       <name>                   # Unregister agent from config
│   └── migrate-customizations               # Migrate customized/ → config agents
│       ├── --fullsend-dir <dir>             #   Base directory with .fullsend layout
│       ├── --repo <owner/repo>              #   Target repo for migration PR
│       └── --dry-run                        #   Preview changes without PR
├── lock             [agent-name]              # Pin remote deps to lock.yaml
│   ├── --all                                #   Lock all harnesses in the harness directory
│   ├── --fullsend-dir <path>                #   Base directory with .fullsend layout
│   ├── --forge <platform>                   #   Lock only this forge variant; omit for all
│   ├── --update                             #   Force re-resolve even if current
│   ├── --offline                            #   Reject network fetches
│   ├── --max-depth <int>                    #   Max transitive dependency depth
│   └── --max-resources <int>                #   Max total remote resources
├── run                                      # Execute an agent in a sandbox
│   ├── --fullsend-dir <path>                #   Base directory with .fullsend layout
│   ├── --target-repo <path>                 #   Path to the target repository
│   ├── --output-dir <path>                  #   Base directory for run output
│   ├── --env-file <path>                    #   Load env vars from dotenv file (repeatable)
│   ├── --forge <platform>                   #   Forge platform (github, gitlab); auto-detected from CI env
│   ├── --no-post-script                     #   Skip post-script execution
│   ├── --debug [filter]                     #   Enable Claude Code debug logging
│   ├── --offline                            #   Reject network fetches
│   ├── --max-depth <int>                    #   Max transitive dependency depth (0 disables)
│   ├── --max-resources <int>                #   Max total remote resources per harness
│   ├── --run-url <url>                      #   CI/CD run URL for status comments
│   ├── --status-repo <owner/repo>           #   Repository for status comments
│   ├── --status-number <int>                #   Issue/PR number for status comments
│   └── --mint-url <url>                     #   Mint service URL for on-demand status tokens
├── fetch-skill      <url>                    # Fetch a skill at runtime (in-sandbox)
├── scan                                     # Run security scanner on input/output
│   ├── input                                # Scan event payload for prompt injection
│   ├── output                               # Scan agent output for leaked secrets
│   ├── context                              # Scan context files for prompt injection
│   └── url                                  # Validate URLs against SSRF attacks
├── post-review                              # Post PR review comments to GitHub
├── post-comment                             # Post issue/PR comments to GitHub
└── reconcile-status                         # Finalize orphaned status comments
    ├── --repo <owner/repo>                  #   Repository in owner/repo format
    ├── --number <int>                       #   Issue/PR number
    ├── --run-id <string>                    #   Workflow run ID (marker key)
    ├── --run-url <url>                      #   Workflow run URL (optional)
    ├── --sha <string>                       #   Commit SHA (optional)
    ├── --reason <string>                    #   Termination reason: terminated or cancelled (default: terminated)
    ├── --mint-url <url>                     #   Mint service URL for on-demand token (default: $FULLSEND_MINT_URL)
    └── --role <string>                      #   Agent role for minting (required with --mint-url)
```

### Migrate Customizations

The `fullsend agent migrate-customizations` command converts `customized/` directory overlays (deprecated by [ADR-0064](../../ADRs/0064-deprecate-customized-directory-overlay.md)) into config-driven agents with `base:` composition harnesses. It scans the local `customized/` directory, classifies each override, and delivers changes via PR:

```bash
# Preview what would change (no PR created)
fullsend agent migrate-customizations --fullsend-dir .fullsend --dry-run

# Create a migration PR
fullsend agent migrate-customizations --fullsend-dir .fullsend --repo owner/repo
```

Migration actions per agent:

| Override type | Detection | Action |
|---------------|-----------|--------|
| Dead | Agent already registered in config | Delete customized files |
| Custom | Not in upstream scaffold | Move files, register local path in config |
| Modified | Standard scaffold agent, not in config | Compute `base:` composition harness via `DiffHarness`, register in config |

The diff engine (`internal/harness/diff.go`) computes the minimal child harness that reproduces the customized version when composed with the upstream base. It mirrors `mergeBaseIntoChild` semantics: scalar overrides, slice concatenation extras, map merge deltas, and security fields always included.

### Command Decomposition

The `mint`, `inference`, and `github` subcommands decompose setup into role-specific operations for organizations that separate GCP and GitHub responsibilities:

| Install Phase | Standalone Command | Required Access |
|---------------|--------------------|-----------------|
| Phases 1-3: Mint deployment | `fullsend mint deploy` | GCP project (mint): `roles/iam.serviceAccountAdmin`, `roles/iam.workloadIdentityPoolAdmin`, `roles/cloudfunctions.developer`, `roles/run.admin`; with `--pem-dir` also `roles/secretmanager.admin`, `roles/resourcemanager.projectIamAdmin` |
| Phases 1-3: Mint enrollment | `fullsend mint enroll` | GCP project (mint): `roles/cloudfunctions.viewer`, `roles/run.admin`, `roles/iam.workloadIdentityPoolAdmin`; per-repo mode also needs `roles/resourcemanager.projectIamAdmin` |
| Phase 4: WIF provisioning | `fullsend inference provision` | GCP project (inference): `roles/iam.workloadIdentityPoolAdmin`, `roles/resourcemanager.projectIamAdmin` |
| Phases 5-7: GitHub setup + enrollment | `fullsend github setup` | GitHub only |

The typical handoff: a GCP admin runs `mint deploy`, `mint enroll`, and `inference provision`, then passes the mint URL and WIF provider resource name to a GitHub maintainer who runs `github setup --mint-url=... --inference-wif-provider=...`. See [Advanced setup](../infrastructure/advanced-setup.md).

> **Note:** The legacy `admin install` command wraps all phases into a single invocation but is deprecated. The standalone commands above are the recommended path. See the [Unified Installation Flow](#unified-installation-flow) section below for how the phases are structured internally.

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
│  │     └─ ROLE_APP_IDS (role → app ID, shared) → skip app     │ │
│  │        creation when all roles are present                 │ │
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
│  │  │ Per-repo: repo-scoped (mintcore.BuildRepoProviderID)│     │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 5: Write scaffold + config files                     │ │
│  │                                                            │ │
│  │  Both modes: write workflow files + customized/ dirs       │ │
│  │  CommitScaffoldFiles() delivery modes:                      │ │
│  │    Default (PR):  create feature branch → commit → open PR │ │
│  │    --direct:      try CommitFiles (default branch)         │ │
│  │      if ErrBranchProtected → fall back to PR mode          │ │
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
| **4. WIF** | `ProvisionWIF()` | Org-wide provider ID | `mintcore.BuildRepoProviderID()` (repo-scoped) |
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
Stack order:  ConfigRepo → Workflows → HarnessWrappers → VendorBinary → Secrets → Inference → Dispatch → Enrollment
Install:      process 1→8 (forward)
Uninstall:    process 8→1 (reverse)
```

Per-repo mode does not use the layer stack — it runs the same phases inline in `runPerRepoInstall()` and `runGitHubSetupPerRepo()` since there's no need for composable uninstall ordering with a single repo. Vendoring (when `--vendor` is set) and stale asset cleanup are handled inline or via shared helpers; per-org mode uses `VendorBinaryLayer`.

### Binary acquisition (`internal/binary`)

Linux binary resolution for `fullsend run` and vendoring lives in `internal/binary`:

| Function | Policy |
|----------|--------|
| `ResolveForRun` | Release download (released CLI only) → cross-compile → latest release |
| `ResolveForVendor` | Cross-compile → matching release (released CLI only) → fail (no latest) |
| `ResolveExplicit` | Validate linux/{arch} ELF for `--fullsend-binary` |

Vendoring commit messages use title + body (upload and stale delete). `github status` reports stale vendored assets at `bin/fullsend` or `.fullsend/bin/fullsend` without install-intent flags.

---

## OpenShell Sandbox Runtime

### Sandbox Lifecycle

```
┌─────────────────────────────────────────────────────────────────┐
│                   Sandbox Lifecycle (run.go)                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐                                                │
│  │ Load harness │ LoadWithBase: unmarshal → compose base →       │
│  │              │ ResolveForge(--forge / env) → Validate        │
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
│  │  Upload to /sandbox/workspace:           │                   │
│  │  ├── fullsend binary (cross-compiled)    │                   │
│  │  ├── agent definition file               │                   │
│  │  ├── skills/ directory                   │                   │
│  │  ├── plugins/ directory                  │                   │
│  │  ├── host_files (expanded ${VAR} paths)  │                   │
│  │  ├── .env file (bootstrapEnv)            │                   │
│  │  └── security hooks                      │                   │
│  │                                          │                   │
│  │  bootstrapEnv() writes:                  │                   │
│  │  ├── PATH=/sandbox/workspace/bin:$PATH   │                   │
│  │  ├── CLAUDE_CONFIG_DIR=/sandbox/claude-config│               │
│  │  ├── FULLSEND_OUTPUT_DIR=...             │                   │
│  │  ├── FULLSEND_FETCH_URL=... (if allow_runtime_fetch)│        │
│  │  ├── FULLSEND_FETCH_TOKEN=<per-run token> (if above)│       │
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
SandboxWorkspace    = "/sandbox/workspace"
SandboxClaudeConfig = "/sandbox/claude-config"
```

For sandbox workspace layout, agent rule layering, and security scanning
details, see [Agent runtimes](../../runtimes.md).

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
| **Layered** | No (runtime) or yes with `--vendor` | Upstream `@v0` sparse checkout, or vendored at install | agents/, skills/, harness/, plugins/, policies/, scripts/, schemas/, env/ |
| **Upstream-only** | No (layered) or yes with `--vendor` | Referenced directly or vendored at install | .github/actions/, .github/scripts/ |

Runtime skips upstream fetch when `.defaults/action.yml` is present (vendored); layered installs sparse-checkout `fullsend-ai/fullsend@v0` into `.defaults/`.

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
| `internal/cli/migrate.go` | ~520 | Migrate customized/ overrides to config-driven agents |
| `internal/cli/mint.go` | ~1022 | Mint deploy/enroll/unenroll/status |
| `internal/cli/inference.go` | ~408 | Inference WIF provision/status |
| `internal/cli/github.go` | ~966 | GitHub setup/set/status/uninstall/sync-scaffold/enroll/unenroll |
| `internal/cli/run.go` | ~1923 | Agent execution lifecycle |
| `internal/mint/main.go` | ~95 | GCF token mint entry point (wiring only) |
| `cmd/mint/` | ~285 | Standalone mint server (no GCP dependency) |
| `internal/mintcore/` | ~1425 | Shared mint library (handler, OIDC verifiers, GitHub API) |
| `internal/dispatch/gcf/provisioner.go` | ~1959 | GCP infrastructure provisioner |
| `internal/sandbox/sandbox.go` | ~459 | OpenShell sandbox operations |
| `internal/harness/harness.go` | ~486 | Harness YAML parsing |
| `internal/layers/layers.go` | ~159 | Layer interface and stack |
| `internal/layers/secrets.go` | ~200 | PEM key deployment layer |
| `internal/layers/inference.go` | ~150 | Inference credential layer |
| `internal/layers/dispatch.go` | ~364 | Mint URL deployment layer |
| `internal/scaffold/scaffold.go` | ~146 | Embedded template system |
| `internal/inference/inference.go` | ~26 | Provider interface |
| `internal/inference/vertex/vertex.go` | ~80 | Agent Platform (Vertex AI) implementation |
| `internal/config/config.go` | ~264 | Org/repo config structures |

## See Also

- [Running agents locally](../user/running-agents-locally.md) — Run agents locally (binary download, GCP credentials, per-agent env vars)
- [Getting Started](../getting-started/) — Standard per-repo installation
- [Advanced setup](../infrastructure/advanced-setup.md) — Alternative installation paths and setup flags
- [Mint service administration](../infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../infrastructure/infrastructure-reference.md) — Infrastructure details
- [Customizing Agents](../user/customizing-agents.md) — User customization guide
