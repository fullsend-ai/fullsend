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
│   ├── enroll       <owner/repo>            # Register repo in mint
│   ├── unenroll     <owner/repo>            # Remove repo from mint
│   ├── status       [org]                   # Inspect mint state and PEM health
│   │   ├── --mint-url <url>                 #   Mint service URL ($FULLSEND_MINT_URL)
│   │   ├── --project <id>                   #   GCP project ID (direct infra queries)
│   │   └── --region <region>                #   GCP region (default: us-central1)
│   └── token                                # Mint a short-lived token via OIDC
│       ├── --role <name>                    #   Agent role (triage, coder, review)
│       ├── --repos <list>                   #   Comma-separated repo names
│       ├── --level <name>                   #   Privilege level (read or write; default: write)
│       ├── --mint-url <url>                 #   Mint service URL ($FULLSEND_MINT_URL)
│       └── --audience <string>              #   OIDC audience (default: fullsend-mint)
├── inference                                # Inference credentials (GCP Vertex, OpenAI)
│   ├── provision    <org|owner/repo>        # Create WIF pool/provider for Agent Platform
│   ├── deprovision  <org|owner/repo>        # Remove WIF access for org or repo
│   ├── status       <org|owner/repo>        # Check WIF health, print config
│   └── openai                               # OpenAI WIF enrolment (GPT on pi or codex)
│       ├── request  <owner/repo>[,...]      # Generate the provider/mapping request for an admin
│       │   ├── --audience <string>          #   Provider audience (default: fullsend://<owner>)
│       │   ├── --project <name|id>          #   OpenAI project to bill the runs to
│       │   ├── --service-account <id>       #   Map an existing service account
│       │   ├── --ref <ref>                  #   Tighten to a ref: emits this ref + refs/pull/*
│       │   ├── --format <json|md>           #   Document format
│       │   └── --out <file>                 #   Write to a file instead of stdout
│       ├── import   [reply.json]            # Record the admin's reply in config.yaml
│       │   ├── --audience/--identity-provider-id/--service-account-id
│       │   ├── --variables                  #   Set FULLSEND_OPENAI_* repo variables instead
│       │   └── --repo <owner/repo>          #   Select from a multi-repo reply; target for --variables
│       └── status   <owner/repo>            # Resolved identifiers, and the exchange inside Actions
├── github                                   # GitHub-only configuration
│   ├── setup        <owner/repo>            # Configure fullsend (no GCP needed)
│   └── set          <owner/repo> <key> <value> # Update a config value
├── repos                                    # Manage per-repo installations via manifest
│   ├── --gitlab-token <token>               #   GitLab access token (overrides GITLAB_TOKEN)
│   ├── install      [repos...]              # Converge repos to desired state (provision, repair drift, upgrade)
│   │   ├── -f, --manifest <path>            #   Path or URL to repos.yaml (default: repos.yaml)
│   │   ├── --dry-run                        #   Preview without making changes
│   │   ├── --concurrency <int>              #   Max parallel operations (1-32, default: 4)
│   │   ├── --roles <list>                   #   Agent roles (default: triage,coder,review,fix,retro,prioritize)
│   │   ├── --direct                         #   Push scaffold to default branch (skip PR)
│   │   ├── --vertex-project <id>            #   GCP project ID for Vertex inference (install-time only)
│   │   ├── --vertex-wif-provider <path>     #   Full WIF provider resource name (uses verbatim; skips per-repo derivation)
│   │   ├── --openai-api-key <key>           #   FULLSEND_OPENAI_API_KEY for openai-api-key repos (CLI-only; never in repos.yaml)
│   │   ├── --forge <type>                   #   Forge type for new repos (github or gitlab)
│   │   ├── --vertex-region <region>         #   Per-repo GCP inference region override
│   │   ├── --fullsend-ref <ref>             #   Per-repo fullsend workflow ref override
│   │   ├── --mint-url <url>                 #   Per-repo mint URL override
│   │   ├── --app-set <prefix>               #   GitHub App set prefix override ($FULLSEND_APP_SET); GitHub-only
│   │   ├── --allowed-remote-resources <list> #  Per-repo allowed remote resources override
│   │   ├── --inference-auth <method>        #   vertex-wif, openai-api-key or openai-wif (GitHub only); persisted as inference.auth on each selected manifest entry (a repo covered only by a glob gets its own copied entry); never changes defaults or forge sections
│   │   ├── --vendor                         #   Vendor binary and content into each repo for offline CI
│   │   ├── --gitlab-url <url>               #   GitLab instance URL; sets gitlab.url in the manifest
│   │   ├── --gitlab-role-registry <path>    #   Administrator GitLab role registry JSON
│   │   ├── --gitlab-role-token role=token   #   Administrator-provided GitLab role PAT (repeatable)
│   │   ├── --rotate-gitlab-roles            #   Force-rotate GitLab role credentials
│   │   ├── --rotate-gitlab-role <name>      #   Rotate a specific GitLab role (repeatable)
│   │   ├── --rotate-gitlab-trigger-token    #   Force-rotate the GitLab webhook fast-path trigger token
│   ├── uninstall    <repos...>              # Tear down fullsend from repos and remove from manifest
│   │   ├── -f, --manifest <path>            #   Path to repos.yaml (default: repos.yaml)
│   │   ├── --dry-run                        #   Preview without making changes
│   │   ├── --yes                            #   Skip confirmation for glob patterns
│   │   ├── --direct                         #   Push file deletions to default branch (skip PR)
│   │   ├── --concurrency <int>              #   Max parallel operations (1-32, default: 4)
│   │   ├── --manifest-only                  #   Remove from manifest without tearing down
│   │   └── --uninstall-only                 #   Tear down without removing from manifest
│   ├── status                               # Compare manifest against actual repo state (includes declared config-preset and managed configuration drift)
│   │   ├── -f, --manifest <path>            #   Path or URL to repos.yaml (default: repos.yaml)
│   │   ├── --json                           #   Emit JSON output instead of table
│   │   ├── --repo <owner/repo>              #   Filter to specific repos (repeatable)
│   │   └── --concurrency <int>              #   Max parallel API calls (default: 8)
├── agent                                    # Generate and manage agents in config (--fullsend-dir default: .fullsend)
│   ├── new          <name>                   # Generate a complete custom agent and register it
│   │   ├── --role <name>                    #   Mint role: triage|review|coder|retro|prioritize
│   │   ├── --on <preset>                    #   Trigger preset (command:/label:/issue-opened/pr-opened)
│   │   ├── --trigger <cel>                  #   Raw CEL trigger (mutually exclusive with --on)
│   │   ├── --runtime <claude|pi|codex>      #   Runtime in config.yaml; shapes Vertex vs OpenAI harness fields
│   │   ├── --model <alias|id>               #   Model (opus default; OpenAI id required for codex)
│   │   ├── -f, --file <spec.yaml>           #   Read the agent definition from a spec file
│   │   ├── --validation-loop                #   Add a schema validation_loop
│   │   ├── --no-register                    #   Write files without touching config.yaml
│   │   ├── --force                          #   Overwrite generated files (never shared assets)
│   │   └── --dry-run                        #   Validate and print, writing nothing
│   ├── add          <url-or-path>            # Register an agent (URL auto-pinned)
│   ├── list                                  # List registered agents
│   ├── set          <name>                   # Set an agent's runtime, model or effort (per-repo)
│   │   ├── --runtime <claude|pi>            #   Runtime for this agent
│   │   ├── --model <alias|id|provider/id>   #   Model for this agent
│   │   └── --effort <level>                 #   Effort level for this agent
│   ├── update       <name> [sha]             # Re-pin URL agent or local harness base
│   └── remove       <name>                   # Unregister agent from config
├── lock             [agent-name]              # Pin remote deps to lock.yaml
│   ├── --all                                #   Lock all harnesses in the harness directory
│   ├── --fullsend-dir <path>                #   .fullsend configuration directory (default: .fullsend)
│   ├── --forge <platform>                   #   Lock only this forge variant; omit for all
│   ├── --update                             #   Force re-resolve even if current
│   ├── --offline                            #   Reject network fetches
│   ├── --max-depth <int>                    #   Max transitive dependency depth
│   └── --max-resources <int>                #   Max total remote resources
├── run                                      # Execute an agent in a sandbox
│   ├── --fullsend-dir <path>                #   .fullsend configuration directory (default: .fullsend)
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
│       ├── --marker <string>                #     Sticky marker for idempotent updates (HTML comment or Jira property)
│       ├── --keep-history                   #     Append previous content as collapsed history (default true)
│       ├── --only-if-exists                 #     Update an existing marked comment, never create one
│       └── --fullsend-dir <path>            #     .fullsend config directory (resolves keep_history default)
├── post-review                              # Post sticky PR/MR review comments (formal review is best-effort)
│   ├── --forge <forge>                      #   Forge backend: github (default) or gitlab
│   ├── --base-url <url>                     #   Forge instance URL (e.g. https://gitlab.example.com)
│   ├── --repo <owner/repo>                  #   Repository in owner/repo format
│   ├── --pr <int>                           #   Pull request / merge request number
│   ├── --result <path>                      #   Path to review result file, or '-' for stdin
│   ├── --token <string>                     #   Forge token (default: $GH_TOKEN / $GITHUB_TOKEN / gh auth token, or $GITLAB_TOKEN)
│   ├── --head-sha <sha>                     #   Expected PR HEAD SHA (skips review if HEAD moved)
│   ├── --dry-run                            #   Print what would be posted without API calls
│   ├── --keep-history                       #   Append previous content as collapsed history (default true)
│   └── --fullsend-dir <path>                #   .fullsend config directory (default: $FULLSEND_DIR; resolves keep_history default)
├── fetch-review-threads                      # Fetch PR/MR review threads as JSON
│   ├── --repo <owner/repo>                   #   Repository in owner/repo format (required)
│   ├── --pr <int>                            #   Pull request / merge request number (required)
│   ├── --forge <forge>                       #   Forge backend: github (default) or gitlab
│   ├── --token <string>                      #   Forge token (default: forge environment token)
│   └── --base-url <url>                      #   Forge API base URL
├── post-comment                             # Post issue/PR comments to GitHub (deprecated)
│   └── --token <string>                     #   GitHub token (default: $GH_TOKEN / $GITHUB_TOKEN / gh auth token)
├── eval-measure                             # Score wild-run traces (eval measurements)
│   ├── --telemetry <path>                   #   Path to run-telemetry.jsonl (or --output-dir)
│   ├── --output-dir <path>                  #   CI output base or runDir (managed-job form)
│   ├── --registry <path>                    #   Agents measurement manifest YAML (or --agent)
│   ├── --agent <name>                       #   Agent name for manifest resolution (managed-job form)
│   ├── --fullsend-dir <path>                #   .fullsend dir (local manifest override + fetch cache)
│   ├── --offline                            #   Reject network fetches (local manifest only)
│   └── --out-dir <path>                     #   Output dir (default: telemetry directory)
├── resolve-mr-source                        # Resolve a GitLab MR's source branch, SHA, and project path
│   ├── --project <path>                     #   GitLab project path (default: $CI_PROJECT_PATH)
│   ├── --mr-iid <int>                       #   Merge request IID (required)
│   ├── --gitlab-url <url>                   #   GitLab instance URL (default: https://gitlab.com)
│   └── --token <string>                     #   GitLab token (default: $GITLAB_TOKEN)
├── check-protected-branch                   # Fail closed unless a GitLab branch is confirmed not protected
│   ├── --project <path>                     #   GitLab project path to check (required)
│   ├── --branch <string>                    #   Branch name to check (required)
│   ├── --gitlab-url <url>                   #   GitLab instance URL (default: https://gitlab.com)
│   └── --token <string>                     #   GitLab token (default: $GITLAB_TOKEN)
└── reconcile-status                         # Finalize orphaned status comments
    ├── --repo <owner/repo>                  #   Repository in owner/repo format (required for GitHub/GitLab)
    ├── --number <int>                       #   Issue/PR number (required for GitHub/GitLab; derived from entity.key for Jira)
    ├── --run-id <string>                    #   Workflow run ID (marker key)
    ├── --run-url <url>                      #   Workflow run URL (optional)
    ├── --sha <string>                       #   Commit SHA (optional)
    ├── --reason <string>                    #   Termination reason: terminated or cancelled (default: terminated)
    ├── --mint-url <url>                     #   Mint service URL for on-demand token (default: $FULLSEND_MINT_URL)
    ├── --role <string>                      #   Agent role for minting (required with --mint-url)
    ├── --forge <platform>                   #   Forge platform (github, gitlab); auto-detected from CI env
    ├── --fullsend-dir <path>                #   Path to fullsend config directory (completion mode detection and tracker routing)
    ├── --job-status <string>                #   Job outcome from CI runner (e.g. success, failure, cancelled)
    └── --was-skipped                        #   Pre-script decided to skip the run; forces synthesis under on_failure
```

### Command Decomposition

`fetch-review-threads` emits a JSON object containing `threads` and a
`truncated` flag. GitHub review threads are capped at 20 pages; consumers
must treat `truncated: true` as an incomplete result. GitLab merge-request
discussions are mapped to the same thread model, including resolution state,
comments, positions, and resolver identity when GitLab provides it. Each comment
also includes `author_role` and `author_role_verified`; resolved threads include
`resolved_by_role` and `resolved_by_role_verified`. A role is `none` with
`verified: true` for an authoritative non-member result, and `verified: false`
when the actor is a bot/unknown or the permission lookup fails.

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

The `install` command accepts only an `owner/repo` target. An org-only
argument is rejected before any forge call, because per-org installation
has been removed:

```
fullsend admin install <owner>/<repo>     → Per-repo mode (single repo bootstrap)
fullsend admin install <org>              → error: requires an owner/repo target
```

---

## Unified Installation Flow

`fullsend admin install` runs a single per-repo pipeline. Per-org installation (the `.fullsend` config repo, org-wide WIF, and org enrollment) has been removed from the CLI.

### Shared Pipeline

```
┌─────────────────────────────────────────────────────────────────┐
│                   Per-Repo Install Pipeline                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  fullsend admin install <owner/repo>                            │
│  ┌──────────────────────┐                                       │
│  │ Parse target          │                                      │
│  │  "acme/repo" → repo  │                                       │
│  │  "acme"      → error │                                       │
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
│  │  If mint exists    → verify mint URL (verifyMintURL)       │ │
│  │                    → store PEMs in Secret Manager          │ │
│  │                                                            │ │
│  │  Uses gcf.NewProvisioner with a shared Config{}            │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 4: WIF provisioning (inference auth)                 │ │
│  │                                                            │ │
│  │  ProvisionWIF() → create pool, provider, IAM               │ │
│  │  Repo-scoped WIF provider (mintcore.BuildRepoProviderID)   │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 5: Write scaffold + config files                     │ │
│  │                                                            │ │
│  │  CommitScaffoldFiles() delivery modes:                     │ │
│  │    Default (PR):  create feature branch → commit → open PR │ │
│  │    --direct:      try CommitFiles (default branch)         │ │
│  │      if ErrBranchProtected → fall back to PR mode          │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ Write .fullsend/ dir in the target repo  │              │ │
│  │  │ Push shim workflow template              │              │ │
│  │  │ Vendor fullsend binary (opt)             │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  └──────────┬─────────────────────────────────────────────────┘ │
│             ▼                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Phase 6: Set secrets & variables                           │ │
│  │                                                            │ │
│  │  Writes the credential set to the target repo:             │ │
│  │    Secrets (install-time only, not managed by sync):       │ │
│  │              FULLSEND_GCP_PROJECT_ID                       │ │
│  │              FULLSEND_GCP_WIF_PROVIDER                     │ │
│  │    Variables (managed by sync):                            │ │
│  │              FULLSEND_GCP_REGION                           │ │
│  │              FULLSEND_MINT_URL                             │ │
│  │              FULLSEND_REVIEW_CLIENT_ID (best-effort)       │ │
│  │              FULLSEND_APP_SET (GitHub only)                │ │
│  │                                                            │ │
│  │  ┌──────────────────────────────────────────┐              │ │
│  │  │ secrets → target repo                    │              │ │
│  │  │ + FULLSEND_PER_REPO_INSTALL=true         │              │ │
│  │  │                                          │              │ │
│  │  │ NOTE: Phase 6 runs before Phase 5        │              │ │
│  │  │ (vars/secrets before scaffold commit)    │              │ │
│  │  │ to prevent a race window (#6122)         │              │ │
│  │  └──────────────────────────────────────────┘              │ │
│  │                                                            │ │
│  │  No enrollment phase: the repo is self-contained.          │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Per-Repo Phase Details

| Phase | Code | Per-repo behavior |
|-------|------|-------------------|
| **1. Discover** | `DiscoverMint()`, resolve app IDs | Single repo validation |
| **2. App setup** | `runAppSetup()` → PEMs + App IDs | Excludes "fullsend" role |
| **3. Mint** | `gcf.Provision()` | Deploys the mint if absent, otherwise verifies the existing mint's URL; no org is registered (use `mint enroll <owner/repo>` separately to add repositories) |
| **4. WIF** | `ProvisionWIF()` | `mintcore.BuildRepoProviderID()` (repo-scoped, GitHub only; GitLab uses shared `gitlab-oidc` provider) |
| **5. Scaffold** | `repos.BuildScaffoldFiles()` (via `scaffold.CollectPerRepoInstallFiles()`) | Writes `.fullsend/` dir + shim workflow + thin caller workflows + optional binary in target repo (committed after secrets, see #6122) |
| **6. Secrets** | Repository secret and variable writes | Target repo + `FULLSEND_PER_REPO_INSTALL` (written before scaffold commit, see #6122) |

### Install orchestration

`runPerRepoInstall()` delegates to `repos.Install()` (from `internal/repos`) for the core install logic (multi-component installation check, WIF provisioning, scaffold commit, variable/secret writes), while `runGitHubSetupPerRepo()` handles GitHub-specific setup. The CLI no longer composes a layer stack for installation, and `internal/layers` no longer ships concrete `Layer` implementations; it keeps the `Layer` interface, `AgentCredentials`, and the vendoring helpers. Vendoring (when `--vendor` is set) and stale asset cleanup are handled inline or via shared helpers.

### Binary acquisition (`internal/binary`)

Linux binary resolution for `fullsend run` and vendoring lives in `internal/binary`:

| Function | Policy |
|----------|--------|
| `ResolveForRun` | Release download (released CLI only) → cross-compile → latest release |
| `ResolveForVendor` | Cross-compile → matching release (released CLI only) → fail (no latest) |
| `ResolveExplicit` | Validate linux/{arch} ELF for `--fullsend-binary` |

Vendoring commit messages use title + body (upload and stale delete). `admin install` and `github setup` remove stale vendored assets at `.fullsend/bin/fullsend` when `--vendor` is not set.

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
│  │ ImportProfileVerified() │ Import openshell provider profiles │
│  │                   │ (from resolved openshell.profiles;       │
│  │                   │  drops the os.TempDir() content cache    │
│  │                   │  and confirms the gateway lists each     │
│  │                   │  profile — see #7218. On GitLab, a       │
│  │                   │  fullsend-gitlab-forge profile is        │
│  │                   │  auto-generated from the forge host URL  │
│  │                   │  — see #6615)                            │
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
│  │  UploadDir (tar) to /sandbox/workspace:  │                   │
│  │  ├── fullsend binary (cross-compiled)    │                   │
│  │  ├── skills/ directory                   │                   │
│  │  └── plugins/ directory                  │                   │
│  │                                          │                   │
│  │  Upload (single file):                   │                   │
│  │  ├── agent definition file               │                   │
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
│  │  ├── sources .env.d/*.env files          │                   │
│  │  └── sources .fullsend/iteration.env     │                   │
│  │      (FULLSEND_TIMEOUT_MINUTES +         │                   │
│  │       FULLSEND_ITERATION_DEADLINE +      │                   │
│  │       TRACEPARENT, rewritten before      │                   │
│  │       every iteration)                   │                   │
│  └──────────┬───────────────────────────────┘                   │
│             ▼                                                   │
│  ┌──────────────────┐                                           │
│  │ Copy source code  │ UploadDir() tar of target repo           │
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
│  │   if i > 1: ClearIterationArtifacts      │                   │
│  │     (sweep stray processes, clear output)│                   │
│  │   write iteration.env incl. TRACEPARENT  │                   │
│  │   run agent                              │                   │
│  │   if killed at timeout: sweep stray      │                   │
│  │     processes (agent still runs, #7042)  │                   │
│  │   extract output                         │                   │
│  │   SafeDownload repo (non-fatal on fail)  │                   │
│  │   run validation script                  │                   │
│  │   if pass → break (early exit)           │                   │
│  │   if killed at timeout → break (#7042)   │                   │
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
│  │                   │ FULLSEND_VALIDATED_ITERATION_DIR is an   │
│  │                   │ absolute path to the validated           │
│  │                   │ iteration's output dir (independent of   │
│  │                   │ cwd / a relative --output-dir), for      │
│  │                   │ forward compatibility. The scaffold-     │
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
SandboxCodexConfig     = "/sandbox/codex-config"
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
| `ImportProfile()` | `openshell provider profile import ...` | Import openshell provider profile (hash-cached) |
| `ImportProfileVerified()` | Forget cache → import → `list-profiles` | Same, then confirm the gateway lists it (#7218) |
| `EnsureProvider()` | `openshell provider ...` | Register model provider (bare-key form) |
| `Create()` | `openshell sandbox create --image ...` | Spin up container |
| `Exec()` | `openshell sandbox exec ...` | Run command in sandbox |
| `ExecStreamReader()` | `openshell sandbox exec ...` | Streaming stdout reader |
| `Upload()` | `openshell sandbox upload ...` | Copy files into sandbox |
| `UploadDir()` | tar -czf + Upload + Exec extract | Copy directory preserving symlinks |
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

The fullsend binary embeds the installation scaffold using Go's `embed.FS`:

```go
//go:embed all:fullsend-repo
var content embed.FS
```

### File Categories

```
fullsend-repo/                      (embedded template)
├── .github/
│   ├── workflows/                  → Thin callers installed to target repo
│   ├── actions/                    → Upstream-only (not installed)
│   └── scripts/                    → Upstream-only (not installed)
├── agents/                         → Layered (runtime, not installed)
├── skills/                         → Layered (runtime, not installed)
├── schemas/                        → Layered (runtime, not installed)
├── harness/                        → Layered (runtime, not installed)
├── providers/                      → Built into the binary (not installed or layered)
├── profiles/                       → Built into the binary (not installed or layered)
├── scripts/                        → Layered (runtime, not installed)
├── env/                            → Layered (runtime, not installed)
├── templates/
│   └── shim-per-repo.yaml          → Rendered to .github/workflows/fullsend.yaml
└── (other files)                   → Not installed by per-repo installs
```

**Four categories:**

| Category | Installed? | Source | Purpose |
|----------|-----------|--------|---------|
| **Installed** | Yes | Scaffold → target repo | `fullsend.yaml` shim (from `templates/shim-per-repo.yaml`) and the `prioritize.yml` thin caller |
| **Layered** | No (runtime) or yes with `--vendor` | Upstream `@main` sparse checkout, or vendored at install | agents/, skills/, harness/, plugins/, scripts/, schemas/, env/ |
| **Built in** | No | Embedded in the `fullsend` binary; `fullsend run` resolves a bare provider name to it | providers/, profiles/ |
| **Upstream-only** | No (layered) or yes with `--vendor` | Referenced directly or vendored at install | .github/actions/, .github/scripts/ |

Runtime skips upstream fetch when `.defaults/action.yml` is present (vendored); layered installs sparse-checkout `fullsend-ai/fullsend@main` into `.defaults/`.

### File Mode Tracking

Since `embed.FS` doesn't preserve Unix permissions, executable files are tracked in a static map:

```go
var executableFiles = map[string]struct{}{
    "scripts/fullsend-check-output":          {},
    "scripts/install-precommit-tools.sh":     {},
    "scripts/prepare-sandbox-credentials.sh": {},
    "scripts/resolve-precommit-tools.py":     {},
    "scripts/setup-prioritize.sh":            {},
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
| `internal/cli/inference.go` | ~408 | Inference WIF provision/status (GCP) |
| `internal/cli/inference_openai.go` | ~900 | OpenAI WIF enrolment: request document, reply import, status/exchange |
| `internal/cli/github.go` | ~966 | GitHub setup/set/status/uninstall/sync-scaffold/enroll/unenroll |
| `internal/cli/github_client.go` | ~130 | GitHub token resolution and authenticated client construction |
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
| `internal/scaffold/scaffold.go` | ~146 | Embedded template system |
| `internal/inference/inference.go` | ~26 | Provider interface |
| `internal/inference/vertex/vertex.go` | ~80 | Agent Platform (Vertex AI) implementation |
| `internal/inference/openaiwif/openaiwif.go` | ~330 | OpenAI Workload Identity Federation token exchange (runner-side) |
| `internal/cli/run_openai.go` | ~550 | OpenAI credential resolution, run-scoped provider lifecycle and refresh |
| `internal/config/config.go` | ~264 | Per-repo config structures |

## See Also

- [Running agents locally](../user/running-agents-locally.md) — Run agents locally (binary download, GCP credentials, per-agent env vars)
- [Getting Started](../getting-started/) — Standard per-repo installation
- [Advanced setup](../infrastructure/advanced-setup.md) — Alternative installation paths and setup flags
- [Mint service administration](../infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../infrastructure/infrastructure-reference.md) — Infrastructure details
- [Configuring Agents](../user/customizing-agents.md) — User configuration guide
