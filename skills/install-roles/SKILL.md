---
name: install-roles
description: >
  Who runs which fullsend CLI command, in what order, and with what
  credentials. Covers the three installation steps (inference provisioning,
  mint enrollment, GitHub setup), per-org vs per-repo modes, and
  mint/inference resource separation. Use when working on install,
  provisioning, enrollment, or WIF-related code.
allowed-tools: Bash
---

# Install Roles & Phases

Quick-reference for agents working on install, provisioning, enrollment, or
WIF-related code. The most common sources of agent confusion are:
1. Who runs which command and in what phase
2. Confusing per-org vs per-repo install semantics
3. Mixing up mint vs inference WIF resources

## Roles & Phases

A typical install has three steps across different roles. The public mint
is already deployed and its URL is locked as `DefaultMintURL` — new users
do not deploy their own mint, but the mint SRE must still enroll each
org/repo before it can exchange tokens.

```
Step 1 (if needed) — GCP admin:    fullsend inference provision
Step 2 — Mint SRE:                 fullsend mint enroll
Step 3 — GitHub admin:             fullsend github setup
```

Most users interact only with Step 3. Steps 1–2 are pre-requisites handled
by infrastructure teams.

### Step 1: Inference provisioning (GCP admin, if needed)

**Who:** Someone with GCP project access and Agent Platform enabled.
**When:** Before GitHub setup, if the org/repo needs AI inference.
**Credentials:** GCP credentials (gcloud auth).

```
fullsend inference provision <org|owner/repo>   -> create inference WIF
fullsend inference status <org|owner/repo>       -> check inference WIF health
fullsend inference deprovision <org|owner/repo>  -> remove inference WIF
```

Outputs: WIF provider resource name, passed to Step 3 via
`--inference-wif-provider`.

### Step 2: Mint enrollment (mint SRE)

**Who:** SRE with access to the mint's GCP project.
**When:** Before GitHub setup — each org or repo must be enrolled in the mint
so the mint function will accept its OIDC tokens.
**Credentials:** GCP credentials for the mint project.

```
fullsend mint enroll <org|owner/repo>   -> register org/repo in mint
fullsend mint unenroll <org|owner/repo> -> remove org/repo from mint
fullsend mint status                    -> check mint health
```

The `mint deploy` command is only needed when standing up a new private mint
on a new GCP project or updating the mint function itself — not during
normal onboarding.

### Step 3: GitHub setup (GitHub admin / most users)

**Who:** Anyone with GitHub org admin access.
**When:** After Steps 1–2 are done (or skipped where not needed).
**Credentials:** GitHub token only — no GCP credentials needed.

```
fullsend github setup <org|owner/repo>   -> configure fullsend for an org or repo
fullsend github enroll <org> [repos...]  -> enroll repos in an existing org
fullsend github unenroll <org> [repos..] -> remove repos from enrollment
fullsend github set <org|owner/repo>     -> update config variables
fullsend github status <org|owner/repo>  -> check configuration health
fullsend github uninstall <org|owner/repo> -> remove fullsend
fullsend github sync-scaffold <org>      -> sync workflow scaffolding
```

The setup command accepts `--inference-project` and
`--inference-wif-provider` to wire up infrastructure from Step 1.
`--mint-url` defaults to the hosted public mint and should not be changed
for new installations.

### fullsend admin (legacy)

Monolithic command that combines all three phases in a single invocation.
Preserved for backward compatibility — do not recommend to new users.

```
fullsend admin install <org|owner/repo>  -> does github setup + mint + inference
fullsend admin uninstall                 -> tears down everything
```

---

## Install Modes (Per-org vs Per-repo)

The CLI argument format determines the mode. The code branches on
`strings.Contains(arg, "/")`.

```
fullsend github setup acme          -> per-org mode  (no slash)
fullsend github setup acme/widget   -> per-repo mode (has slash)
```

### Per-org mode

Creates centralized infrastructure for an entire GitHub organization:

- Creates a `.fullsend` config repo (public, holds config.yaml, shim workflows)
- Creates per-role GitHub Apps via manifest flow (browser-based)
- Enrolls repos by updating config.yaml (enabled: true/false)
- Enrollment triggers repo-maintenance workflow to create PRs in target repos

Key flags: `--enroll-all`, `--enroll-none` (per-org ONLY -- rejected in per-repo)
Default roles: fullsend, triage, coder, review, retro, prioritize

### Per-repo mode

Bootstraps a single repository without requiring a config repo:

- Writes `.github/workflows/fullsend.yaml` and `.fullsend/config.yaml` directly
- Sets repo-level variables (`FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`) and
  secrets (`FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER`)
- `--mint-url` defaults to the hosted public mint, so most installs can omit it
- Sets guard variable `FULLSEND_PER_REPO_INSTALL=true` to prevent per-org
  enrollment from overwriting it
- Creates a dedicated WIF provider per repo (`gh-{owner}-{repo}`)
- No config repo, no cross-repo dispatch
- Requires `--inference-project` on first-time setup (skipped on re-runs if
  the `FULLSEND_GCP_PROJECT_ID` secret already exists)

Default roles: triage, coder, review, fix, retro, prioritize
Note: the "fullsend" role is EXCLUDED -- per-repo uses the target repo's own
shim for dispatch instead of a separate fullsend app.
Note: "fix" is included in per-repo but NOT per-org because per-org reuses
the coder app for fix operations.

### Flag scope

Unified flags (work in both modes):
  `--agents`, `--dry-run`, `--vendor-fullsend-binary`, `--inference-project`,
  `--inference-region`, `--inference-wif-provider`,
  `--public`, `--app-set`, `--mint-url`, `--skip-app-setup`

Admin install ONLY flags (not available on `fullsend github setup`):
  `--mint-provider`, `--mint-project`, `--mint-region`, `--skip-mint-check`

Per-org ONLY flags (rejected in per-repo with an error):
  `--enroll-all`, `--enroll-none`

Per-repo specifics:
  `--inference-project` is REQUIRED (optional in per-org).
  `--mint-project` defaults to `--inference-project` if not set.
  Guard variable prevents per-org from overwriting per-repo installs.

### Shared app sets

When `--public` is used, GitHub Apps are created as public (unlisted) and can
be installed by multiple orgs. The `--app-set` flag controls the app name
prefix:

```
--app-set=fullsend-ai  ->  apps named fullsend-ai-coder, fullsend-ai-review, etc.
Default app set: "fullsend-ai"
Legacy app sets checked during uninstall: ["fullsend"]
```

For shared apps, PEMs use role-only naming shared across all orgs on a mint:
`fullsend-{role}-app-pem` (e.g., `fullsend-coder-app-pem`). A single secret
per role serves all orgs enrolled on that mint instance.

The `ROLE_APP_IDS` env var on the mint function maps org-scoped keys to app IDs:
```json
{"acme/coder": "12345", "acme/review": "12346", "beta/coder": "12345"}
```
Multiple orgs can share the same app ID when using public apps.

---

## Mint vs Inference Resource Separation

The mint and inference subsystems use SEPARATE WIF pools. This prevents
lifecycle operations on one from interfering with the other.

| Resource          | Mint                      | Inference                  |
|-------------------|---------------------------|----------------------------|
| WIF pool          | `fullsend-pool`           | `fullsend-inference`       |
| WIF provider (org)| `github-oidc` in mint pool| `github-oidc` in inference pool |
| WIF provider (repo)| registered in `PER_REPO_WIF_REPOS` | `gh-{owner}-{repo}` in inference pool |
| Service account   | `fullsend-mint@{project}` | None (direct WIF)          |
| Cloud Function    | `fullsend-mint`           | None                       |
| IAM role          | invoker on the function   | `roles/aiplatform.user`    |

### Mint infrastructure (token minting)

Purpose: Exchange GitHub OIDC JWTs for scoped GitHub App installation tokens.
Managed by: `fullsend mint deploy/enroll/unenroll` (rare — public mint is the default)

GCP resources:
- Service account: `fullsend-mint@{project}.iam.gserviceaccount.com`
- WIF pool: `fullsend-pool` (const `defaultPool` in provisioner.go)
- WIF provider: `github-oidc` (org-scoped, shared provider with CEL condition)
- Cloud Function: `fullsend-mint` (deployed as GCF gen2 / Cloud Run)
- Secrets: `fullsend-{role}-app-pem` (one per role, shared across orgs on the mint)

Env vars on the Cloud Function:
  `ALLOWED_ORGS`, `ROLE_APP_IDS`, `ALLOWED_ROLES`, `ALLOWED_WORKFLOW_FILES`,
  `GCP_PROJECT_NUMBER`, `WIF_POOL_NAME`, `WIF_PROVIDER_NAME`, `OIDC_AUDIENCE`,
  `PER_REPO_WIF_REPOS`, `FULLSEND_SOURCE_HASH`

The mint pool's WIF provider attribute condition scopes to `repository_owner`:
```
assertion.repository_owner == 'acme'
assertion.repository_owner in ['acme', 'beta']
```

### Inference infrastructure (AI model access)

Purpose: Allow GitHub Actions workflows to authenticate to Vertex AI Agent
Platform for LLM inference via WIF (no service account key needed).
Managed by: `fullsend inference provision/status/deprovision`
Pre-requisite: User must have access to a GCP project with Agent Platform enabled.

GCP resources:
- WIF pool: `fullsend-inference` (const `DefaultInferencePool` in provisioner.go)
- WIF provider:
  - Org-scoped: `github-oidc` (in the inference pool, NOT the mint pool)
  - Repo-scoped: `gh-{owner}-{repo}` (dedicated provider per repo)
- IAM binding: `roles/aiplatform.user` granted to WIF principalSet
- NO service account needed -- uses direct WIF (principalSet to project IAM)
- NO Cloud Function -- workflows authenticate directly to Vertex AI

The inference pool's WIF condition follows the same pattern as mint but in a
DIFFERENT pool:
```
Org-scoped:  assertion.repository_owner == 'acme'
Repo-scoped: assertion.repository == 'acme/widget'
```

### Per-repo WIF setup

Per-repo mode creates a dedicated WIF provider in the inference pool and
registers the repo in the mint's `PER_REPO_WIF_REPOS` env var.

**Mint side (env var update, not a WIF provider):**
- Repo added to `PER_REPO_WIF_REPOS` env var on the Cloud Function
- Done by: `fullsend mint enroll <owner/repo>`
- The mint checks this list to accept tokens for the specific repo

**Inference side (WIF provider creation):**
- Provider ID: `gh-{owner}-{repo}` in the `fullsend-inference` pool
- Created by: `fullsend inference provision <owner/repo>`
- Condition: `assertion.repository == '{owner}/{repo}'`
- IAM: `roles/aiplatform.user` granted to the repo's principalSet

---

## Key Code Locations

```
cmd/fullsend/main.go                    -> CLI entry point
internal/cli/github.go                  -> github setup/enroll/unenroll/status
internal/cli/admin.go                   -> legacy install/uninstall/enable/disable
internal/appsetup/appsetup.go           -> GitHub App manifest flow, shared apps
internal/config/config.go               -> org/repo config model
internal/cli/mint.go                    -> mint deploy/enroll/unenroll/status
internal/cli/inference.go               -> inference provision/status/deprovision
internal/dispatch/gcf/provisioner.go    -> GCF mint provisioner (WIF, PEMs, deploy)
internal/mintcore/                      -> shared mint logic (PEM handling, token exchange)
internal/mint/main.go                   -> Cloud Function source (token exchange)
```

---

## Common Mistakes to Avoid

### CLI separation mistakes

WRONG: "use fullsend admin install to set up fullsend"
RIGHT: Most users run `fullsend github setup` (+ `fullsend inference provision`
if inference is needed). `admin install` is legacy.

WRONG: "fullsend github setup needs GCP credentials"
RIGHT: `fullsend github` requires only a GitHub token. GCP operations are
handled separately by `fullsend inference`.

WRONG: "users need to deploy their own mint or pass --mint-url"
RIGHT: The public mint URL is locked as the default (`DefaultMintURL`). New
users should not change it. However, the mint SRE must still run
`fullsend mint enroll` to register each org/repo before tokens can be minted.

### Install mode mistakes

WRONG: "per-org and per-repo are different commands"
RIGHT: Same command (`github setup` or `admin install`), mode determined by
whether the argument contains a slash.

WRONG: "per-repo mode doesn't need GCP infrastructure"
RIGHT: Per-repo still needs inference WIF (via `fullsend inference provision`)
with repo-scoped providers.

WRONG: "--enroll-all works for per-repo"
RIGHT: `--enroll-all` and `--enroll-none` are per-org ONLY flags.

WRONG: "per-repo uses the fullsend role"
RIGHT: Per-repo excludes the fullsend dispatch role; it uses the repo's own shim.

WRONG: "each org gets its own GitHub Apps"
RIGHT: With `--public`, apps are shared across orgs via the app set.

WRONG: "PEM secrets are scoped per org like fullsend-{org}--{role}-app-pem"
RIGHT: PEM secrets use role-only naming: `fullsend-{role}-app-pem`, shared across orgs.

### Resource separation mistakes

WRONG: "the WIF pool is shared between mint and inference"
RIGHT: Separate pools -- `fullsend-pool` (mint) and `fullsend-inference` (inference).

WRONG: "inference needs a service account"
RIGHT: Inference uses direct WIF -- principalSet binding to the project, no SA.

WRONG: "the mint pool name is fullsend-inference"
RIGHT: Mint uses `fullsend-pool`; inference uses `fullsend-inference`.

WRONG: "per-repo WIF providers are only needed for inference"
RIGHT: Per-repo mode creates providers in BOTH pools.

WRONG: "mint enroll also sets up inference"
RIGHT: `mint enroll` only handles the mint side; run `inference provision` separately.
