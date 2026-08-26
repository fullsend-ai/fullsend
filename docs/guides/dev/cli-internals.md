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
│   ├── delete                               # Tear down mint infrastructure
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
├── repos                                    # Manage per-repo installations via manifest
│   ├── --gitlab-token <token>               #   GitLab access token (overrides GITLAB_TOKEN)
│   ├── migrate      <org>                   # Migrate org from per-org to per-repo install
│   │   ├── --project <id>                   #   GCP project ID for inference (required)
│   │   ├── --repo <name>                    #   Filter to specific repos (repeatable, supports globs)
│   │   ├── --dry-run                        #   Preview only
│   │   ├── --direct                         #   Push scaffold to default branch (skip PR)
│   │   ├── --concurrency <int>              #   Parallel limit (1-32, default: 4)
│   │   └── -f, --manifest <path>            #   Output path for repos.yaml (default: repos.yaml)
│   ├── install      [repos...]              # Converge repos to desired state (provision, repair drift, upgrade)
│   │   ├── -f, --manifest <path>            #   Path or URL to repos.yaml (default: repos.yaml)
│   │   ├── --dry-run                        #   Preview without making changes
│   │   ├── --concurrency <int>              #   Max parallel operations (1-32, default: 4)
│   │   ├── --roles <list>                   #   Agent roles (default: triage,coder,review,fix,retro,prioritize)
│   │   ├── --direct                         #   Push scaffold to default branch (skip PR)
│   │   ├── --inference-project <id>         #   GCP project ID for inference (install-time only)
│   │   ├── --inference-project-number <num> #   Numeric GCP project number for WIF (auto-derived; install-time only)
│   │   ├── --forge <type>                   #   Forge type for new repos (github or gitlab)
│   │   ├── --inference-region <region>      #   Per-repo GCP inference region override
│   │   ├── --fullsend-ref <ref>             #   Per-repo fullsend workflow ref override
│   │   ├── --mint-url <url>                 #   Per-repo mint URL override
│   │   └── --allowed-remote-resources <list> #  Per-repo allowed remote resources override
│   ├── uninstall    <repos...>              # Tear down fullsend from repos and remove from manifest
│   │   ├── -f, --manifest <path>            #   Path to repos.yaml (default: repos.yaml)
│   │   ├── --dry-run                        #   Preview without making changes
│   │   ├── --yes                            #   Skip confirmation for glob patterns
│   │   ├── --concurrency <int>              #   Max parallel operations (1-32, default: 4)
│   │   ├── --manifest-only                  #   Remove from manifest without tearing down
│   │   └── --uninstall-only                 #   Tear down without removing from manifest
│   ├── status                               # Compare manifest against actual repo state
│   │   ├── -f, --manifest <path>            #   Path or URL to repos.yaml (default: repos.yaml)
│   │   ├── --json                           #   Emit JSON output instead of table
│   │   ├── --repo <owner/repo>              #   Filter to specific repos (repeatable)
│   │   └── --concurrency <int>              #   Max parallel API calls (default: 8)
├── agent                                    # Manage agent registrations in config
│   ├── add          <url-or-path>            # Register an agent (URL auto-pinned)
│   ├── list                                  # List registered agents
│   ├── set          <name>                   # Set an agent's runtime, model or effort (per-repo)
│   │   ├── --runtime <claude|pi>            #   Runtime for this agent
│   │   ├── --model <alias|id|provider/id>   #   Model for this agent
│   │   └── --effort <level>                 #   Effort level for this agent
│   ├── update       <name> [sha]             # Re-pin URL agent to new commit SHA
│   └── remove       <name>                   # Unregister agent from config
├── lock             [agent-name]              # Pin remote deps to lock.yaml
│   ├── --all                                #   Lock all harnesses in the harness directory
│   ├── --fullsend-dir <path>                #   .fullsend configuration directory
│   ├── --forge <platform>                   #   Lock only this forge variant; omit for all
│   ├── --update                             #   Force re-resolve even if current
│   ├── --offline                            #   Reject network fetches
│   ├── --max-depth <int>                    #   Max transitive dependency depth
│   └── --max-resources <int>                #   Max total remote resources
├── run                                      # Execute an agent in a sandbox
│   ├── --fullsend-dir <path>                #   .fullsend configuration directory
│   ├── --target-repo <path>                 #   Path to the target repository
│   ├── --output-dir <path>                  #   Base directory for run output
│   ├── --env-file <path>                    #   Load env vars from dotenv file (repeatable)
│   ├── --forge <platform>                   #   Forge platform (github, gitlab); auto-detected from CI env
│   ├── --no-post-script                     #   Skip post-script execution
│   ├── --debug [filter]                     #   Enable agent runtime debug logging
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
├── issues                                   # Read and write issue content across trackers
│   ├── get                                  #   Read issue content (title, body, comments, labels)
│   │   ├── --tracker <tracker>              #     Tracker backend: github, gitlab, or jira
│   │   ├── --project <project>              #     Project: owner/repo (GitHub/GitLab) or key (Jira)
│   │   └── --number <int>                   #     Issue number
│   └── post-comment                         #   Post or update a sticky comment on an issue
│       ├── --tracker <tracker>              #     Tracker backend: github, gitlab, or jira
│       ├── --project <project>              #     Project: owner/repo (GitHub/GitLab) or key (Jira)
│       ├── --number <int>                   #     Issue number
│       └── --marker <string>                #     Hidden HTML marker for idempotent updates
├── post-review                              # Post PR/MR review comments to GitHub or GitLab
│   ├── --forge <forge>                      #   Forge backend: github (default) or gitlab
│   ├── --base-url <url>                     #   Forge instance URL (e.g. https://gitlab.example.com)
│   ├── --repo <owner/repo>                  #   Repository in owner/repo format
│   ├── --pr <int>                           #   Pull request / merge request number
│   ├── --result <path>                      #   Path to review result file, or '-' for stdin
│   ├── --token <string>                     #   Forge token (default: $GH_TOKEN / $GITHUB_TOKEN or $GITLAB_TOKEN)
│   ├── --head-sha <sha>                     #   Expected PR HEAD SHA (skips review if HEAD moved)
│   └── --dry-run                            #   Print what would be posted without API calls
├── post-comment                             # Post issue/PR comments to GitHub (deprecated)
├── eval-measure                             # Score wild-run traces (eval measurements)
│   ├── --telemetry <path>                   #   Path to run-telemetry.jsonl (or --output-dir)
│   ├── --output-dir <path>                  #   CI output base or runDir (managed-job form)
│   ├── --registry <path>                    #   Agents measurement manifest YAML (or --agent)
│   ├── --agent <name>                       #   Agent name for manifest resolution (managed-job form)
│   ├── --fullsend-dir <path>                #   .fullsend dir (local manifest override + fetch cache)
│   ├── --offline                            #   Reject network fetches (local manifest only)
│   └── --out-dir <path>                     #   Output dir (default: telemetry directory)
└── reconcile-status                         # Finalize orphaned status comments
    ├── --repo <owner/repo>                  #   Repository in owner/repo format
    ├── --number <int>                       #   Issue/PR number
    ├── --run-id <string>                    #   Workflow run ID (marker key)
    ├── --run-url <url>                      #   Workflow run URL (optional)
    ├── --sha <string>                       #   Commit SHA (optional)
    ├── --reason <string>                    #   Termination reason: terminated or cancelled (default: terminated)
    ├── --mint-url <url>                     #   Mint service URL for on-demand token (default: $FULLSEND_MINT_URL)
    ├── --role <string>                      #   Agent role for minting (required with --mint-url)
    ├── --forge <platform>                   #   Forge platform (github, gitlab); auto-detected from CI env
    ├── --fullsend-dir <path>                #   Path to fullsend config directory (completion mode detection)
    ├── --job-status <string>                #   Job outcome from CI runner (e.g. success, failure, cancelled)
    └── --was-skipped                        #   Pre-script decided to skip the run; forces synthesis under on_failure
```

### Command Decomposition

The `mint`, `inference`, and `github` subcommands decompose setup into role-specific operations for organizations that separate GCP and GitHub responsibilities:

| Install Phase | Standalone Command | Required Access |
|---------------|--------------------|-----------------|
| Phases 1-3: Mint deployment | `fullsend mint deploy` | GCP project (mint): `roles/iam.serviceAccountAdmin`, `roles/iam.workloadIdentityPoolAdmin`, `roles/cloudfunctions.developer`, `roles/run.admin`; with `--pem-dir` also `roles/secretmanager.admin`, `roles/resourcemanager.projectIamAdmin` |
| Phases 1-3: Mint enrollment | `fullsend mint enroll` | GCP project (mint): `roles/cloudfunctions.viewer`, `roles/run.admin`, `roles/iam.workloadIdentityPoolAdmin` |
| Phase 4: WIF provisioning | `fullsend inference provision` | GCP project (inference): `roles/iam.workloadIdentityPoolAdmin`, `roles/resourcemanager.projectIamAdmin` |
| Phases 5-7: GitHub setup + enrollment | `fullsend github setup` | GitHub only |

The typical handoff: a GCP admin runs `mint deploy`, `mint enroll`, and `inference provision`, then passes the mint URL and WIF provider resource name to a GitHub maintainer who runs `github setup --mint-url=... --inference-wif-provider=...`. See [Advanced setup](../infrastructure/advanced-setup.md).

> **Deprecated:** The `admin install` command is deprecated. Use the
> standalone commands above instead. See the
> [Unified Installation Flow](#unified-installation-flow) section below for
> how the phases are structured internally.

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
│              Unified Install Pipeline (both modes)              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  fullsend admin install <target>                                │
│  ┌──────────────────────┐                                       │
│  │ Parse target          │                                      │
│  │  "acme"      → org   │                                       │
│  │  "acme/repo" → repo  │                                       │
│  └──────────┬───────────┘                                       │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 1: Discover (read-only)                              │ │
│  │                                                            │ │
│  │  a. Discover mint   --mint-url / --mint-project / default  │ │
│  │     └─ DiscoverMint() → check if GCF exists, get URL       │ │
│  │  b. Resolve existing app IDs from mint env vars            │ │
│  │     └─ ROLE_APP_IDS (role → app ID, shared) → skip app     │ │
│  │        creation when all roles are present                 │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 2: App setup (shared: runAppSetup)                   │ │
│  │                                                            │ │
│  │  For each role in --agents:                                │ │
│  │    - Create/reuse GitHub App ({appSet}-{role} --app-set)   │ │
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
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 4: WIF provisioning (inference auth)                 │ │
│  │                                                            │ │
│  │  Both modes: ProvisionWIF() → create pool, provider, IAM   │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-org:  org-wide WIF provider          │              │ │
│  │  │ Per-repo: repo-scoped WIF provider       │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 5: Write scaffold + config files                     │ │
│  │                                                            │ │
│  │  Both modes: write workflow files                           │ │
│  │  CommitScaffoldFiles() delivery modes:                     │ │
│  │    Default (PR):  create feature branch → commit → open PR │ │
│  │    --direct:      try CommitFiles (default branch)         │ │
│  │      if ErrBranchProtected → fall back to PR mode          │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-org:  create .fullsend config repo   │              │ │
│  │  │           push reusable workflows        │              │ │
│  │  │           vendor fullsend binary (opt)   │              │ │
│  │  │                                          │              │ │
│  │  │ Per-repo: write .fullsend/ dir in repo   │              │ │
│  │  │           push shim workflow template    │              │ │
│  │  │           vendor fullsend binary (opt)   │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 6: Set secrets & variables                           │ │
│  │                                                            │ │
│  │  Both modes write the same credential set:                 │ │
│  │    Secrets (install-time only, not managed by sync):       │ │
│  │              FULLSEND_GCP_PROJECT_ID                       │ │
│  │              FULLSEND_GCP_WIF_PROVIDER                     │ │
│  │    Variables (managed by sync):                            │ │
│  │              FULLSEND_GCP_REGION                           │ │
│  │              FULLSEND_MINT_URL                             │ │
│  │              FULLSEND_REVIEW_CLIENT_ID (best-effort)       │ │
│  │                                                            │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Per-org:  secrets → .fullsend config repo│              │ │
│  │  │           MINT_URL → org variable        │              │ │
│  │  │           + repo var (dot-prefix fix)    │              │ │
│  │  │           + PEM keys as repo secrets     │              │ │
│  │  │           + client IDs as repo variables │              │ │
│  │  │                                          │              │ │
│  │  │ Per-repo: secrets → target repo          │              │ │
│  │  │           + FULLSEND_PER_REPO_GUARD=true │              │ │
│  │  │                                          │              │ │
│  │  │ NOTE: Per-repo runs Phase 6 before       │              │ │
│  │  │ Phase 5 (vars/secrets before scaffold    │              │ │
│  │  │ commit) to prevent a race window (#6122) │              │ │
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
| **3. Mint** | `gcf.Provision()` or `EnsureOrgInMint()` | — | — (use `mint enroll` separately) |
| **4. WIF** | `ProvisionWIF()` | Org-wide provider ID | `mintcore.BuildRepoProviderID()` (repo-scoped, GitHub only; GitLab uses shared `gitlab-oidc` provider) |
| **5. Scaffold** | `repos.BuildScaffoldFiles()` (via `scaffold.CollectPerRepoInstallFiles()`) | Creates `.fullsend` repo, pushes workflows + optional binary | Writes `.fullsend/` dir + shim workflow + thin caller workflows + optional binary in target repo (committed after secrets in per-repo, see #6122) |
| **6. Secrets** | Same secret names, same API calls | Config repo + org variable | Target repo + `PER_REPO_GUARD` (written before scaffold commit in per-repo, see #6122) |
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

Per-repo mode does not use the layer stack — `runPerRepoInstall()` delegates to `repos.Install()` (from `internal/repos`) for the core install logic (multi-component installation check, WIF provisioning, scaffold commit, variable/secret writes), while `runGitHubSetupPerRepo()` handles GitHub-specific setup. There's no need for composable uninstall ordering with a single repo. Vendoring (when `--vendor` is set) and stale asset cleanup are handled inline or via shared helpers; per-org mode uses `VendorBinaryLayer`.

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
│                   Sandbox Lifecycle (run.go)                    │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐                                                │
│  │ Load harness │ LoadWithBase: unmarshal → compose base →      │
│  │              │ ResolveForge(--forge / env) → Validate        │
│  └──────┬──────┘                                                │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ EnsureAvailable() │ Verify openshell binary exists           │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ CheckGateway()    │ Start/verify gateway service             │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ ImportProfile()   │ Import openshell provider profiles       │
│  │                   │ (from resolved openshell.profiles)       │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ EnsureProvider()  │ Register inference provider              │
│  │                   │ (bare-key credential form)               │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Pre-script        │ Run harness.pre_script (host-side).      │
│  │                   │ skipped=true or exit 78 skips (#4718,582)│
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Create()          │ openshell sandbox create                 │
│  │                   │ --image {harness.image}                  │
│  │                   │ Returns sandbox ID                       │
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
│  │  ├── FULLSEND_FETCH_TOKEN=<run token> (if above)│            │
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
│  │ Command built by buildRunCommand():      │                   │
│  │  cd {repoDir} &&                         │                   │
│  │  . {envFile} &&                          │                   │
│  │  claude --print --verbose                │                   │
│  │    --output-format stream-json           │                   │
│  │    [--settings {hooksSettingsPath}]      │                   │
│  │    --model {model}                       │                   │
│  │    --effort {effort}                     │                   │
│  │    --agent {agent}                       │                   │
│  │    --dangerously-skip-permissions        │                   │
│  │    'Run the agent task'                  │                   │
│  │                                          │                   │
│  │ Background: OIDC token refresh every 4m  │                   │
│  └──────────┬───────────────────────────────┘                   │
│             ▼                                                   │
│  ┌──────────────────┐                                           │
│  │ Extract output    │ SafeDownload() with sanitization:        │
│  │                   │ - Remove dangerous symlinks (escape)     │
│  │                   │ - Remove .git/hooks/ (hook injection)    │
│  │                   │                                          │
│  │                   │ With validation_loop: SafeDownload       │
│  │                   │ failure is non-fatal — clean up repo dir │
│  │                   │ and continue to next iteration. Output   │
│  │                   │ files (extracted separately) are kept.   │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────────────────────────────┐                   │
│  │ Validation loop (if configured)          │                   │
│  │                                          │                   │
│  │ Phase 1 — inline validation:             │                   │
│  │ for i := 1; i <= max_iterations; i++ {   │                   │
│  │   run agent → extract output             │                   │
│  │   SafeDownload repo (non-fatal on fail)  │                   │
│  │   run validation script                  │                   │
│  │   if pass → break (early exit)           │                   │
│  │   feed feedback → next iteration         │                   │
│  │ }                                        │                   │
│  │                                          │                   │
│  │ Phase 2 — post-loop sweep (#5393):       │                   │
│  │ if no inline pass:                       │                   │
│  │   for i := latest..1 {                   │                   │
│  │     run validation on iteration-i dir    │                   │
│  │     TARGET_REPO_DIR="" (repo dir is      │                   │
│  │       unreliable across iterations)      │                   │
│  │     if pass → use this iteration; break  │                   │
│  │   }                                      │                   │
│  └──────────┬───────────────────────────────┘                   │
│             ▼                                                   │
│  ┌──────────────────┐                                           │
│  │ Post-script       │ Run harness.post_script (host-side)      │
│  │                   │ REPO_DIR set only when last SafeDownload │
│  │                   │ succeeded and validated iteration is the │
│  │                   │ latest; empty otherwise. post-fix.sh and │
│  │                   │ post-code.sh both fail closed on empty   │
│  │                   │ REPO_DIR in their own script logic; the  │
│  │                   │ other validation_loop post-scripts don't │
│  │                   │ reference REPO_DIR at all. code.yaml has │
│  │                   │ no validation_loop, so post-code.sh      │
│  │                   │ can't currently hit this path, but the   │
│  │                   │ check is real, not dead code. There is   │
│  │                   │ no per-iteration repo checkout, so post- │
│  │                   │ fix.sh cannot recover a sweep-validated  │
│  │                   │ non-final iteration; it fails closed     │
│  │                   │ instead of pushing (known limitation,    │
│  │                   │ see #5393).                              │
│  │                   │                                          │
│  │                   │ FULLSEND_VALIDATED_ITERATION_DIR points  │
│  │                   │ to the validated iteration's output dir, │
│  │                   │ for forward compatibility. The scaffold- │
│  │                   │ embedded post-scripts don't consume it   │
│  │                   │ yet (tracked in fullsend-ai/agents#411)  │
│  │                   │ — they still scan for the last iteration │
│  │                   │ blindly.                                 │
│  └──────┬───────────┘                                           │
│         ▼                                                       │
│  ┌──────────────────┐                                           │
│  │ Delete()          │ openshell sandbox delete                 │
│  │                   │ Cleanup sandbox resources                │
│  └──────────────────┘                                           │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Sandbox Constants

```go
SandboxWorkspace       = "/sandbox/workspace"
SandboxClaudeConfig    = "/sandbox/claude-config"
SandboxPiConfig        = "/sandbox/pi-config"
SandboxPiExtensionsDir = "/usr/local/share/pi-extensions"   // image-baked, read-only pi extensions (loaded only via -e)
```

For sandbox workspace layout, agent rule layering, and security scanning
details, see [Agent runtimes](../../runtimes.md).

### Key Sandbox Operations

| Operation | CLI Command | Purpose |
|-----------|------------|---------|
| `EnsureAvailable()` | Check `openshell` binary | Verify runtime available |
| `CheckGateway()` | `openshell gateway ...` | Start inference gateway |
| `ImportProfile()` | `openshell provider profile import ...` | Import openshell provider profile |
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
    "scripts/fullsend-check-output":          {},
    "scripts/install-precommit-tools.sh":     {},
    "scripts/prepare-sandbox-credentials.sh": {},
    "scripts/reconcile-repos.sh":             {},
    "scripts/resolve-precommit-tools.py":     {},
    "scripts/setup-prioritize.sh":            {},
    "scripts/validate-source-repo.sh":        {},
}
```

`FileMode()` returns `"100755"` for scripts, `"100644"` for everything else. A test (`TestFileModeMatchesFilesystem`) validates this map stays in sync with the actual filesystem.

---

## Complete End-to-End Flow: Issue → Agent Run → PR

```
┌─────────────────────────────────────────────────────────────────┐
│           End-to-End: Issue Triage → Code → Review              │
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
│      └── Posts review comments via forge API (GitHub App or GitLab token) │
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
| `internal/cli/mint.go` | ~1022 | Mint deploy/enroll/unenroll/status |
| `internal/cli/inference.go` | ~408 | Inference WIF provision/status |
| `internal/cli/github.go` | ~966 | GitHub setup/set/status/uninstall/sync-scaffold/enroll/unenroll |
| `internal/cli/issues.go` | ~430 | Issue read/write commands (`fullsend issues get`, `post-comment`) |
| `internal/cli/tracker_client.go` | ~122 | Tracker client factory (GitHub/GitLab/Jira) |
| `internal/cli/run.go` | ~1923 | Agent execution lifecycle |
| `internal/mint/main.go` | ~95 | GCF token mint entry point (wiring only) |
| `cmd/mint/` | ~285 | Standalone mint server (no GCP dependency) |
| `internal/mintcore/` | ~1425 | Shared mint library (handler, OIDC verifiers, GitHub API) |
| `internal/dispatch/gcf/provisioner.go` | ~1959 | GCP infrastructure provisioner |
| `internal/dispatch/cf/workersrc/` | ~800 | CF Worker adapter for mint (WASM bridge, I/O only) |
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
- [Configuring Agents](../user/customizing-agents.md) — User configuration guide
