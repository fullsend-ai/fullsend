---
title: "33. Per-repo installation mode"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
  - security-threat-model
topics:
  - installation
  - per-repo
  - reusable-workflows
  - distribution
  - github-apps
---

# 33. Per-repo installation mode

Date: 2026-05-06

## Status

Accepted

## Context

Fullsend's installation model is per-org: `fullsend admin install` creates a dedicated `.fullsend` config repo, per-role GitHub Apps ([ADR 0007](0007-per-role-github-apps.md)), shim workflows in enrolled repos, and a central token mint for OIDC-based credential issuance (ADR 0029). This requires org admin access and assumes all enrolled repos share agent configuration, credentials, and policies.

Some users cannot or do not want to use the per-org model:

1. **No org-wide setup desired** — teams who want fullsend on specific repos without the full `fullsend admin install` org setup (org admin is still needed to approve GitHub App installation on the repo).
2. **Private repos** — the `.fullsend` config repo defaults to public so that all enrolled repos can call its workflows via `workflow_call`. A private repo _can_ call into a public `.fullsend`, but event payloads and workflow run logs flow through the public repo's context, creating content exposure risk. Setting `.fullsend` to private restricts callers to private repos within the same org (public enrolled repos can no longer reach it). Per-repo sidesteps this entirely: the shim calls upstream `fullsend-ai/fullsend` reusable workflows directly, and stage workflows run in the private repo's own context — no cross-repo payload exposure. **Per-repo is the recommended default for private repos.**
3. **No sharing desired** — teams who want isolated agent configs, credentials, and billing for a single repo.
4. **Quick evaluation** — users who want to try fullsend on one repo without committing to org-wide setup.
5. **Personal repos** — individual developers on personal GitHub accounts (no org at all).

Three ADRs and the implementation in PR 792 create the building blocks that make per-repo possible:

- ADR 0029 replaces PEM secrets and dispatch PATs with OIDC-based credential issuance via a central token mint. The `mint-token` composite action takes a role name (triage, coder, review, fix) and returns a scoped GitHub App installation token — no PEMs or client IDs in the calling repo.
- [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md) publishes five reusable workflows (`reusable-triage.yml`, `reusable-code.yml`, `reusable-review.yml`, `reusable-fix.yml`, `reusable-retro.yml`) and four composite actions (`fullsend`, `mint-token`, `validate-enrollment`, `setup-gcp`) from `fullsend-ai/fullsend`, enabling any repo to call fullsend infrastructure via `workflow_call` without copying workflow files. Scaffold stage workflows in `.fullsend` are now thin callers (41–66 lines) that delegate to these reusable workflows.
- [ADR 0034](0034-centralized-shim-routing-via-dispatch.md) centralizes event-to-stage routing in `dispatch.yml` within the `.fullsend` config repo. The enrolled-repo shim (~70 lines) forwards raw event context to `dispatch.yml` via `workflow_call`; `dispatch.yml` (~370 lines) determines the stage, mints an OIDC dispatch token, validates the stage, checks the kill switch, and dispatches to the matching thin caller via `workflow_call`. Adding a new stage requires only a case branch in `dispatch.yml` — zero changes to enrolled repos.
- ADR 0035 introduces layered content resolution: upstream defaults (agents, skills, schemas, harness, policies, scripts) are sparse-checked from `fullsend-ai/fullsend` at runtime, then org overrides from `customized/` are copied on top. The scaffold installs only org-specific files (~23 files instead of ~68).

The per-org flow after PR 792:

```
enrolled repo (shim, ~70 lines)
  └─ workflow_call ─→ .fullsend/dispatch.yml (routing, ~370 lines)
                         └─ workflow_call ───→ .fullsend/code.yml (thin caller, ~43 lines)
                                                  └─ uses: fullsend-ai/fullsend/reusable-code.yml@v1
                                                            (workspace prep, mint-token, agent run)
```

Combined, these make per-repo installation viable: a workflow file in the target repo calling upstream reusable workflows, with credentials issued by the token mint and routing handled by a reusable dispatch workflow.

## Options

### Alternative 1: Per-repo via scaffold copy

Run `fullsend admin install` targeting a single repo instead of an org. Copy all scaffold files (agent workflows, composite action, dispatcher, scripts) into the target repo.

**Rejected**: Same maintenance burden as per-org — the repo must re-run install to pick up upstream patches. Contradicts ADR 0031's motivation to eliminate workflow drift. ADR 0035's layered content resolution already solves the upstream-defaults problem for per-org; per-repo should reuse the same mechanism.

### Alternative 2: Single GitHub App for all roles

Use one GitHub App for triage, code, review, and fix roles to simplify per-repo setup.

**Rejected**: GitHub suppresses events triggered by pushes made with any `GITHUB_TOKEN` or GitHub App installation token, to prevent infinite loops. Two separate Apps work because a push made with App-A's token _does_ generate events that trigger workflows authenticated as App-B. The fix→review loop requires the coder/fix agent to push commits that trigger review — if both roles share one App, the push token matches the workflow's App and the event is silently suppressed, breaking the feedback cycle. At minimum, coder and review must be separate Apps.

### Alternative 3: Per-repo as a separate codebase

Build a standalone per-repo tool or action that does not share infrastructure with per-org fullsend.

**Rejected**: Duplicates agent logic, composite action, and security controls. Per-repo should reuse the same reusable workflows as per-org, with mode detection to adapt behavior.

### Alternative 4: Two-app minimum (coder + review)

Reduce per-repo to two Apps instead of matching the full per-org app set.

**Rejected**: Dropping the triage App forces triage to share one of the other App identities, which conflates permissions (triage only needs `issues:write`, while coder has `contents:write`). The full per-role model (ADR 0007) provides least-privilege isolation. CLI automation (`fullsend admin install`) makes creating the Apps straightforward.

## Decision

### Overview

Add a **per-repo installation mode** where fullsend runs entirely within a single repository — no `.fullsend` config repo, no cross-repo dispatch, no org-level secrets. The target repo IS the config repo.

Per-repo reuses the reusable workflows from ADR 0031, adding one new artifact: `reusable-dispatch.yml`, a reusable version of the per-org `dispatch.yml` (ADR 0034) that combines event-to-stage routing with per-stage dispatch into a single `workflow_call` entry point, published in `fullsend-ai/fullsend`. This eliminates the need for a `.fullsend` config repo — the target repo's shim calls `reusable-dispatch.yml` directly.

### 1. Architecture

```
Per-org (current, after ADR 0031/0034/0035):

ENROLLED REPO                         .FULLSEND CONFIG REPO
─────────────                         ─────────────────────
fullsend.yml (shim, ~70 lines)        dispatch.yml (routing, ~370 lines)
  │ workflow_call                        │ determines stage
  └──────────────────────────────────────┘ workflow_call
                                         │
                                     code.yml / review.yml / ... (thin callers, ~43 lines)
                                         │ uses: (workflow_call)
                                         └──> fullsend-ai/fullsend/reusable-code.yml@v1
                                                ├── prepare workspace (ADR 0035 layering)
                                                ├── validate-enrollment
                                                ├── mint-token (OIDC → scoped token)
                                                ├── setup-gcp
                                                └── fullsend action (run agent)

Per-repo (proposed):

TARGET REPO (self-contained)
────────────────────────────
.github/workflows/fullsend.yml (~70 lines, shim)
  │
  │ workflow_call
  └──> fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1
         ├── routes event to stage (same logic as .fullsend/dispatch.yml)
         ├── dispatches to per-stage reusable workflows:
         ├──> reusable-triage.yml  ─┐
         ├──> reusable-code.yml    ─┤── reusable workflows (ADR 0031)
         ├──> reusable-review.yml  ─┤
         ├──> reusable-fix.yml     ─┤
         └──> reusable-retro.yml   ─┘
                   │
         uses: fullsend-ai/fullsend@v1          (run agent)
         uses: fullsend-ai/fullsend/mint-token   (OIDC → scoped token)
         uses: fullsend-ai/fullsend/setup-gcp    (GCP auth)
         skips enrollment validation (per-repo mode)
         config: .fullsend/ directory in target repo
```

Per-repo requirements: repo admin + org admin to install GitHub Apps on the repo, GCP project for inference. No dedicated config repo, no cross-repo dispatch.

### 2. Repo layout

```
target-repo/
├── .github/workflows/fullsend.yml    ← single workflow file (~70 lines, shim)
├── .fullsend/                        ← in-repo config workspace (optional)
│   ├── customized/                  ← user overrides (same convention as per-org)
│   │   ├── agents/                 ← agent prompt overrides
│   │   ├── harness/                ← harness config overrides
│   │   ├── policies/               ← sandbox policies
│   │   ├── skills/                 ← repo-specific skills
│   │   └── scripts/                ← pre/post scripts
│   └── config.yaml                  ← repo-level config
├── AGENTS.md
└── ... (source code)
```

The `.fullsend/` directory mirrors the `.fullsend` config repo structure. It acts as a self-contained config workspace — analogous to the `.fullsend` repo root in per-org mode. At runtime, workspace prep populates `.fullsend/agents/`, `.fullsend/skills/`, etc. from upstream defaults, then copies `.fullsend/customized/*` on top. This is the same `customized/` convention from ADR 0035, rooted at `.fullsend/` instead of `.`.

The `.fullsend/` directory is optional. Without it, upstream defaults apply. Users add override files to `.fullsend/customized/` only to customize agent behavior — the top-level dirs inside `.fullsend/` are runtime-populated and should not be committed.

### 3. Config layering

ADR 0035 introduces layered content resolution for per-org: upstream defaults are sparse-checked at runtime, then org overrides from `customized/` are copied on top. Per-repo uses the same `customized/` convention, rooted inside `.fullsend/`:

```
fullsend-ai/fullsend defaults  <  .fullsend/customized/  <  AGENTS.md
(base, sparse-checked)           (overrides)               (instructions)
```

The org-level `.fullsend` config repo tier is skipped — the in-repo `.fullsend/` directory serves as the config workspace. The reusable workflows' "Prepare workspace" step is parameterized by root directory: `.` for per-org (the `.fullsend` repo checkout), `.fullsend/` for per-repo. In both modes, it sparse-checkouts upstream defaults into `{root}/agents/`, `{root}/skills/`, etc., then copies `{root}/customized/*` on top — identical code path, different root.

**Git ref for config reads**: In per-repo mode, `.fullsend/`, `AGENTS.md`, and `.github/workflows/fullsend.yml` are always read from the **base branch** (the default branch of the repository), not the PR head branch. This is enforced by `pull_request_target`, which checks out the base branch by default. The reusable workflows do not check out the PR head ref for config or agent instructions — only the target repo's source code is checked out from the PR head for the agent to operate on. This prevents PR authors from injecting modified agent instructions, policies, or workflow files via their PR — the project's #1 threat category (external prompt injection).

### 4. The `reusable-dispatch.yml` workflow

This is the key new artifact, published in `fullsend-ai/fullsend/.github/workflows/`. It is a reusable version of the per-org `dispatch.yml` (ADR 0034), accepting event context via `workflow_call` inputs and performing the same routing and dispatch logic.

The routing logic (identical to per-org `dispatch.yml`) maps:
- `issues` + `labeled` → stage based on label name (`ready-to-code` → code, `ready-for-review` → review)
- `issue_comment` + slash commands → `/triage`, `/code`, `/review`, `/fix`, `/retro`
- `issue_comment` + `needs-info` label (non-command) → auto-triage
- `pull_request_target` + `opened`/`synchronize`/`ready_for_review` → review
- `pull_request_target` + `closed` → retro
- `pull_request_review` + `changes_requested` from review bot → fix (same-repo PRs only)

In per-org mode, `dispatch.yml` routes events and dispatches to thin callers via `workflow_call`. In per-repo mode, `reusable-dispatch.yml` routes events and dispatches to per-stage reusable workflows directly via conditional `workflow_call` jobs, keeping the entire pipeline within a single `workflow_call` chain.

**Dispatch mechanism**: Per-org uses `workflow_call` to fan out to thin callers in `.fullsend`, which in turn call upstream reusable workflows via `workflow_call`. Per-repo uses conditional `workflow_call` jobs inside `reusable-dispatch.yml` to call `reusable-code.yml` etc. directly, eliminating the need for thin callers.

**Nesting depth**: target-repo shim → `reusable-dispatch.yml` → `reusable-code.yml` = 2 levels of `workflow_call` (GitHub limit is 4).

### 5. Per-repo mode detection

Reusable workflows accept an `install_mode` input (`per-org` default; `per-repo` switches behavior). The shim passes `install_mode: per-repo` to `reusable-dispatch.yml`, which propagates it to per-stage reusable workflows. The `validate-enrollment` action also accepts `install_mode` and skips `config.yaml` enrollment checks in per-repo mode.

In per-repo mode:
- Enrollment validation is skipped (always self-enrolled).
- A single checkout retrieves both config (`.fullsend/` subdirectory) and code (repo root).
- The "Prepare workspace" step runs with root=`.fullsend/` — populates `.fullsend/agents/`, `.fullsend/skills/`, etc. from upstream, then copies `.fullsend/customized/*` on top.
- `fullsend run` receives `--fullsend-dir=.fullsend` and `--target-repo=.`.

In per-org mode:
- Enrollment is validated against `config.yaml`.
- Two checkouts: `.fullsend` repo (config), then target repo into `target-repo/`.
- The "Prepare workspace" step runs with root=`.` — populates `agents/`, `skills/`, etc. from upstream, then copies `customized/*` on top.
- `fullsend run` receives `--fullsend-dir=.` and `--target-repo=target-repo`.

### 6. Credential models

ADR 0029 defines three
installation profiles based on who owns the GitHub Apps and the token mint.
Role-only PEM naming (`fullsend-{role}-app-pem`, no org prefix) and
`--public` Apps enable shared Apps across orgs — onboarding a new org to
a shared mint requires zero Secret Manager work since the PEM is already
stored from when the App was created.

Per-repo maps to these profiles:

| Profile | Who manages Apps + mint | Per-repo user does |
|---------|------------------------|--------------------|
| **SaaS** (default) | Platform operator (fullsend-ai) pre-provisions shared public Apps and mint | Install shared Apps on repo, set `FULLSEND_MINT_URL` |
| **Bundled** | Enterprise admin runs one mint + shared `--public` Apps for multiple orgs | Install shared Apps on repo, point at enterprise mint URL |
| **Self-managed** | Per-repo user deploys own mint + own Apps | `fullsend admin install owner/repo --mint-project=my-proj` creates everything |

**SaaS profile (default)**: The simplest path. Shared public Apps
(`fullsend-triage`, `fullsend-coder`, `fullsend-review`) are pre-created
by the platform operator and installed on the per-repo user's repo (requires
org admin approval). The `mint-token` composite action exchanges a GitHub
OIDC token for a scoped installation token — no PEMs, client IDs, or App
secrets in the repo. The mint looks up the PEM via role-only naming
(`fullsend-{role}-app-pem`) in Secret Manager.

**Self-managed profile**: For users who want full control — same per-role
Apps ([ADR 0007](0007-per-role-github-apps.md)), but user-owned. The user
deploys their own mint and creates their own Apps via `fullsend admin
install owner/repo --mint-project=my-proj`.

The mint's `job_workflow_ref` validation accepts three patterns:
- `{org}/.fullsend/.github/workflows/*.yml@*` (per-org shim workflows)
- `fullsend-ai/fullsend/.github/workflows/reusable-*.yml@*` (upstream reusable workflows called via `workflow_call`)
- `{owner}/{repo}/.github/workflows/*.yml@*` where `{owner}/{repo}` is registered in `PER_REPO_WIF_REPOS` (per-repo workflows running directly via `workflow_dispatch`)

The `repository_owner` claim scopes tokens to the calling org/user.
`ALLOWED_ORGS` on the mint controls which orgs may request tokens.

### 7. CLI support: `fullsend admin install <owner/repo>`

The existing `fullsend admin install` command handles both per-org and per-repo modes. The argument format determines the mode:

```
fullsend admin install <org>            # per-org installation
fullsend admin install <owner/repo>     # per-repo installation
```

Per-repo flags:
- `--inference-project` — GCP project for Vertex AI inference (required)
- `--inference-region` — GCP region for Vertex AI inference (default: `global`)
- `--inference-wif-provider` — full WIF provider resource name (`projects/{number}/locations/global/.../providers/{id}`); auto-provisioned if omitted

Shared flags (valid for both per-org and per-repo):
- `--mint-url` — token mint URL for OIDC token exchange (optional; auto-discovered from `--mint-project`/`--mint-region` if omitted)
- `--mint-project` — GCP project containing the mint function (defaults to `--inference-project` in per-repo)
- `--mint-region` — cloud region for the mint function (default: `us-central1`)
- `--agents` — comma-separated agent roles (default: `fullsend,triage,coder,review,retro,prioritize`)
- `--dry-run` — preview changes without making them
- `--skip-app-setup` — skip GitHub App creation (reuse existing apps)
- `--skip-mint-deploy` — skip Cloud Function deployment, reuse existing mint URL
- `--skip-mint-check` — skip mint validation, GCP provisioning, and app setup; requires `--mint-url`
- `--public` — create public unlisted GitHub Apps (for multi-org)
- `--mint-provider` — token mint provider backend (default: `gcf`)
- `--mint-source-dir` — path to mint function source directory
- `--app-set` — app set name prefix for GitHub Apps (default: `fullsend`)

Per-org-only flags (`--vendor-fullsend-binary`, `--enroll-all`, `--enroll-none`) are rejected when an `owner/repo` argument is given. All other flags are shared between per-org and per-repo modes — per-repo can create GitHub Apps, deploy a mint, and manage public apps when existing infrastructure is not found.

**Per-repo install steps**:

1. Discovers existing infrastructure: auto-discovers mint URL and app IDs from `--mint-project`/`--mint-region` in a single API call.
2. If apps are missing and `--skip-app-setup` is not set: creates GitHub Apps via the browser-based manifest flow (same as per-org). PEMs are stored in Secret Manager.
3. If no mint exists: deploys the token mint Cloud Function (same provisioner path as per-org).
4. If a mint already exists: validates PEMs, registers the org, and sets up per-repo WIF.
5. Auto-provisions inference WIF pool/provider if `--inference-wif-provider` is omitted.
6. Generates scaffold files (`.github/workflows/fullsend.yaml`, `.fullsend/config.yaml`, `.fullsend/customized/` directories).
7. Commits all scaffold files to the target repo via the GitHub API.
8. Sets repository variables (`FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`, `FULLSEND_PER_REPO_INSTALL`).
9. Sets repository secrets (`FULLSEND_GCP_PROJECT_ID`, WIF credentials).

Per-repo install requires only `repo` and `workflow` OAuth scopes when reusing existing infrastructure. When creating apps, scope escalation to `admin:org` is required (same as per-org).

### 8. Coexistence

Per-repo and per-org coexist within the same org. Some repos use the org `.fullsend` config repo (per-org), others run independently (per-repo). They use different dispatch paths, credential stores, and shim templates.

To prevent per-org enrollment from overriding a per-repo installation, per-repo install sets a repository Actions variable `FULLSEND_PER_REPO_INSTALL=true`. The per-org enrollment flow checks this guard at three points:

1. **CLI (`--enroll-all`)**: Skips repos with `FULLSEND_PER_REPO_INSTALL=true`. Prompts interactively if the guard exists but is not `true` (e.g., admin cleared it). Fails closed on API errors (skips the repo rather than risking overwrite).
2. **`reconcile-repos.sh`**: Checks the guard via the GitHub API before both enrollment and unenrollment. Skips repos with `true`. Fails closed on non-404 API errors.
3. **Enrollment `Analyze`**: Reports per-repo repos as "per-repo install, skipped" and excludes them from drift calculation.

A mixed-visibility org is a natural fit: public repos use per-org with a public `.fullsend`, while private repos use per-repo to avoid routing event payloads through a public config repo. Per-repo should be the default recommendation for any private repo.

Migration between models:
- **Per-repo → per-org**: Remove `FULLSEND_PER_REPO_INSTALL` variable from the repo, remove workflow file, add to `.fullsend/config.yaml` enrollment.
- **Per-org → per-repo**: Remove from enrollment, run `fullsend admin install owner/repo` (sets the guard variable and writes the per-repo shim).

## Consequences

### Positive

- **No org admin required**: Repo admins can adopt fullsend without org-level access or coordination (though org admin is still needed to install the GitHub Apps on the repo).
- **Self-contained**: Everything fullsend needs lives in one repo — simpler mental model, easier cleanup.
- **Reuses ADR 0031/0034/0035 infrastructure**: Per-repo adds one workflow (`reusable-dispatch.yml`); all stage reusable workflows, composite actions, and layered content resolution are shared with per-org.
- **Low entry barrier**: Install shared public Apps on repo, copy one workflow file, set mint URL — working fullsend in under 15 minutes. No Apps to create, no PEMs, no GCP project for credentials (SaaS profile).
- **Reduced blast radius**: Token mint scopes tokens to the requesting repo via the `repository` OIDC claim. Credential compromise affects only the single repo.
- **Private repo safe by default**: Per-repo workflows run in the repo's own context — event payloads and logs never transit a public `.fullsend` config repo. Per-org requires choosing between a public `.fullsend` (content exposure risk for private enrolled repos) or a private `.fullsend` (blocks public enrolled repos). Per-repo eliminates this tradeoff.
- **Same agent behavior**: Triage → Code → Review → Fix workflow is identical from the user's perspective.

### Negative

- **Org admin still needed for App installation**: While per-repo removes the need for org admin to run `fullsend admin install`, an org admin must still approve the GitHub App installation on the repo.
- **Config governance weaker**: In per-org, agent config lives in a separate repo with its own CODEOWNERS. In per-repo, `.fullsend/` config lives alongside code — a code contributor could modify agent behavior in a PR (mitigated by CODEOWNERS on `.fullsend/` and base-branch checkout).
- **No centralized policy**: Per-repo users set their own policies. An org cannot enforce uniform agent behavior across independently-installed repos.
- **Credential separation collapses**: In per-org mode (ADR 0008), the dispatch PAT only grants `actions:write` on `.fullsend` — enrolled repos can trigger dispatch but never access PEM secrets. In per-repo mode, the repo that triggers workflows IS the repo holding secrets (WIF provider, SA key, or GCP project ID), eliminating this credential separation. A compromised repo contributor with write access could potentially access secrets stored at the repo level. The token mint mitigates this partially — PEMs remain in Secret Manager, not in the repo — but repo-level secrets (`FULLSEND_GCP_WIF_PROVIDER`, `FULLSEND_GCP_PROJECT_ID`) are still co-located with the code.
- **Self-managed profile increases burden**: Users who opt for self-managed (own mint + own Apps) manage their own App PEM rotation and GCP Secret Manager project. The SaaS profile avoids this entirely.

### Risks

Ordered by the project's threat priority (external injection > insider > drift > supply chain):

- **External injection — `pull_request_target` misconfiguration**: Per-repo workflows MUST use `pull_request_target` (not `pull_request`) to prevent PR authors from modifying the workflow to exfiltrate secrets. In per-repo mode, the workflow file lives in the same repo as the code — unlike per-org where the shim is pushed by the orchestrator app. A contributor with write access could change `pull_request_target` to `pull_request` in a PR, and if that PR is merged, subsequent PRs could exfiltrate secrets. The workflow template enforces `pull_request_target`, but users could edit it.
- **External injection — untrusted code checkout**: `pull_request_target` triggers reusable workflows that check out and execute against the PR's head ref for source code — the classic "pwn request" surface. This is distinct from workflow modification risk: even with the correct trigger, the agent sandbox executes untrusted code from the PR. The agent sandbox, restricted tool permissions, and base-branch-only config reads (see Config layering) mitigate this surface.
- **Insider — workflow and config modification**: In per-repo mode, `.github/workflows/fullsend.yml` and `.fullsend/` live alongside code. A contributor with write access could modify agent behavior, sandbox policies, or the workflow trigger in a PR. Without CODEOWNERS protection, these changes could be merged by any approver.
- **`event_payload` size**: Per-org's `dispatch.yml` builds a minimal payload from `$GITHUB_EVENT_PATH` (extracting only `issue`, `pull_request`, and `comment` fields), avoiding the 65KB `workflow_call` input limit. Per-repo's shim forwards `event_action` via `workflow_call` and `reusable-dispatch.yml` reads remaining context from `github.event.*` expressions, following the same pattern. Large PR event payloads are unlikely to be an issue since the shim does not pass the full payload as an input.
- **App identity confusion**: Users unfamiliar with the fix→review loop requirement may attempt a single-App setup and get silent failures (no review triggered after fix pushes).

### Mitigations

- **CODEOWNERS on workflow and config**: `fullsend admin install` should add CODEOWNERS entries protecting `.github/workflows/fullsend.yml` and `.fullsend/` so that changes to the workflow trigger, agent config, and sandbox policies require approval from designated owners. This is the primary defense against both workflow misconfiguration and insider modification of agent behavior. In per-org mode, the `.fullsend` config repo has its own CODEOWNERS; per-repo must replicate this governance at the file level.
- **Base-branch config reads**: Reusable workflows read `.fullsend/`, `AGENTS.md`, and workflow files from the base branch only (enforced by `pull_request_target`). PR authors cannot inject modified agent instructions or policies via their PR.
- **Template validation**: `fullsend admin install` generates the workflow file with `pull_request_target`. Users who modify it are warned in documentation.
- **Minimal payload**: Following per-org `dispatch.yml`, `reusable-dispatch.yml` reads event context from `github.event.*` expressions (available in `workflow_call` callee context) rather than passing the full payload as an input.
- **Clear error messages**: Credential auto-detection reports why coder and review Apps must be separate, with a link to setup documentation.
- **Migration path**: Per-repo users who outgrow the model can migrate to per-org without changing agent behavior — the same reusable workflows power both modes.

## Resolved Questions

### `reusable-dispatch.yml` dispatch mechanism

**Resolved (PR #799):** Option 1 — conditional `workflow_call` jobs. `reusable-dispatch.yml` defines one job per stage (`triage`, `code`, `review`, `fix`, `retro`), each with an `if:` condition based on the routing output. Only the matched stage job runs. This keeps the pipeline within `workflow_call` and stays within GitHub Actions' native capabilities. The nesting depth is 3 levels (shim → `reusable-dispatch.yml` → `reusable-{stage}.yml`), within GitHub's 4-level limit.

### Concurrency groups

**Resolved (PR #799):** Concurrency groups are set at the per-stage job level inside `reusable-dispatch.yml`, matching the per-org behavior.

### `stop-fix` job placement

**Resolved (PR #799):** The `stop-fix` job lives in the target repo's shim workflow (same location as per-org) since it only needs the default `GITHUB_TOKEN` — no mint or reusable workflow involvement.

### CLI command design

**Resolved (PR #799):** Per-repo uses the existing `fullsend admin install` command rather than a separate `fullsend init` subcommand. The argument format determines the mode: `fullsend admin install <org>` for per-org, `fullsend admin install <owner/repo>` for per-repo. Per-org-only and per-repo-only flags are validated and rejected when used with the wrong mode.

## References

- [ADR 0007: Per-role GitHub Apps](0007-per-role-github-apps.md) — authentication model replicated in per-repo
- [ADR 0008: workflow_dispatch for cross-repo dispatch](0008-workflow-dispatch-for-cross-repo-dispatch.md) — superseded by `workflow_call` (ADR 0034 centralizes routing)
- [ADR 0026: Stage-based dispatch](0026-stage-based-dispatch-for-agent-workflow-decoupling.md) — stage model preserved in reusable workflows
- ADR 0029: Central token mint — default credential model for per-repo
- [ADR 0031: Reusable workflows](0031-reusable-workflows-for-action-installed-distribution.md) — publishes stage reusable workflows and composite actions
- [ADR 0034: Centralized event routing](0034-centralized-shim-routing-via-dispatch.md) — routing logic in `dispatch.yml`, replicated as `reusable-dispatch.yml` for per-repo
- ADR 0035: Layered content resolution — upstream defaults sparse-checked at runtime, overrides via `customized/` (per-org) or `.fullsend/` (per-repo)
