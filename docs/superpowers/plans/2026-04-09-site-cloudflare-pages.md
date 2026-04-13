# Documentation site → Cloudflare Workers (fork-safe CI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace GitHub Pages with **Cloudflare Workers static assets** (production + per-PR previews), using a secretless **Build Site** workflow plus a **`workflow_run` Deploy Site** workflow with Cloudflare + GitHub Deployment credentials, **`site-preview` / `site-production`**, and an upserted PR comment. Naming uses **site** throughout (workflows, artifact); the mindmap is the current `index.html` source only.

**Architecture:** **`Build Site`** runs on `pull_request` and `push` to `main`, checks out the PR head on PRs, produces `_site/`, uploads artifact **`site`**. **`Deploy Site`** checks out the repo (for `site/wrangler.toml`), downloads the artifact into **`site/public/`**, runs **`wrangler deploy`** on **`push`** and **`wrangler versions upload --preview-alias pr-<run-id>`** on **`pull_request`** (Wrangler **4.30.0** via `cloudflare/wrangler-action@v3.14.1` + `wranglerVersion`), resolves a **`workers.dev`** URL for GitHub, then `actions/github-script` records Deployments and comments.

**Tech Stack:** GitHub Actions, Cloudflare **Workers** (static assets), Wrangler **4.x**, `cloudflare/wrangler-action@v3.14.1`, `actions/github-script@v8`, REST Deployments API.

**Spec:** [2026-04-09-site-cloudflare-pages-design.md](../specs/2026-04-09-site-cloudflare-pages-design.md)

---

## File map

| File | Role |
|------|------|
| `.github/workflows/site-build.yml` | Secretless build + artifact `site` |
| `.github/workflows/site-deploy.yml` | Checkout + artifact → `site/public/`, `wrangler deploy` / `versions upload`, GitHub Deployment + PR comment |
| `site/wrangler.toml` | Worker name placeholder, `assets.directory = public`, SPA `not_found_handling`, `preview_urls` |
| `site/public/.gitkeep` | Keeps `public/` in git; CI overwrites with artifact contents |
| `.github/workflows/site-github-pages.yml` | **Removed** (replaced by `site-build.yml` / `site-deploy.yml`) |
| `docs/site-deployment.md` | Operator runbook: Worker, token scopes (Workers Edit), secrets/variables, fork policy, troubleshooting |

---

### Task 1: Add build workflow

**Files:**

- Create: `.github/workflows/site-build.yml`

- [ ] **Step 1: Create the workflow file**

Use this exact content (pin `actions/checkout` to `v6.0.2` to match other workflows in this repo):

```yaml
name: Build Site

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

concurrency:
  group: site-build-${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6.0.2
        with:
          ref: ${{ github.event_name == 'pull_request' && github.event.pull_request.head.sha || github.sha }}

      - name: Prepare site
        run: |
          mkdir -p _site
          cp docs/mindmap.html _site/index.html

      - uses: actions/upload-artifact@v4
        with:
          name: site
          path: _site/
          retention-days: 5
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/site-build.yml
git commit -m "ci: add site build workflow for Cloudflare handoff"
```

---

### Task 2: Add deploy workflow

**Files:**

- Create or update: `.github/workflows/site-deploy.yml`

- [ ] **Step 1: Implement Deploy Site (canonical copy in repo)**

The job must only run for successful runs of **this repository’s** **Build Site** workflow, and only for `pull_request` or `push` events.

**Behavior (Workers, not Pages):**

1. `actions/checkout` (so `site/wrangler.toml` exists).
2. Download artifact **`site`** into **`site/public/`**.
3. **`push`:** `cloudflare/wrangler-action` with `wranglerVersion: "4.30.0"`, `workingDirectory: site`, `command: deploy --name=<CLOUDFLARE_PROJECT_NAME>`.
4. **`pull_request`:** same action with `command: versions upload --name=<CLOUDFLARE_PROJECT_NAME> --assets public --preview-alias pr-<workflow_run.id>`.
5. **Resolve URL:** `deployment-url` output, else parse stdout/stderr for `workers.dev`.
6. **`actions/github-script`:** GitHub Deployments + PR comment; `description: Cloudflare Workers (static assets)`.

Copy the full YAML from the repository file [`.github/workflows/site-deploy.yml`](../../../.github/workflows/site-deploy.yml) when implementing in another clone.

- **`vars.CLOUDFLARE_PROJECT_NAME`:** Worker name (same variable name as before).

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/site-deploy.yml site/wrangler.toml site/public/.gitkeep
git commit -m "ci: deploy site with Workers static assets"
```

---

### Task 3: Remove GitHub Pages workflow

**Files:**

- Delete: `.github/workflows/site-github-pages.yml`

- [ ] **Step 1: Delete the file**

Remove `.github/workflows/site-github-pages.yml` entirely so the site is no longer deployed via `actions/deploy-pages`.

- [ ] **Step 2: Commit**

```bash
git rm .github/workflows/site-github-pages.yml
git commit -m "ci: drop GitHub Pages workflow for documentation site"
```

---

### Task 4: Operator runbook

**Files:**

- Create: `docs/site-deployment.md`

- [ ] **Step 1: Add the runbook**

Create `docs/site-deployment.md` with the following sections (adjust org/repo names when copying for upstream):

1. **Overview** — Link to the design spec `docs/superpowers/specs/2026-04-09-site-cloudflare-pages-design.md` and summarize **`Build Site`** / **`Deploy Site`** (Workers + static assets).
2. **Cloudflare setup**
   - Create a **Worker** (or let first `wrangler deploy` create it); enable **preview URLs**; optional **workers.dev** subdomain.
   - Create an **API Token** with **Cloudflare Workers → Edit** (and **Account Settings → Read** if needed). Pages-only tokens are **not** sufficient. Store as `CLOUDFLARE_API_TOKEN`.
   - Copy **Account ID** → `CLOUDFLARE_ACCOUNT_ID`.
   - Add **`CLOUDFLARE_PROJECT_NAME`** as a GitHub **Actions variable** = **Worker name**.
   - Optional **custom domain** (fork demos or later `konflux.sh`): attach routes on the **Worker**, not a Pages project.
3. **GitHub setup (fork — phase 1)**
   - Repository → **Settings → Secrets and variables → Actions**:
     - Secrets: `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`
     - Variables: `CLOUDFLARE_PROJECT_NAME`
   - **Settings → Actions → General → Fork pull request workflows**: allow workflows from contributors (so fork PRs can run the **build** workflow).
4. **GitHub setup (upstream — phase 2)**
   - Same secrets/variables at **org or repo** level as your governance prefers.
   - Confirm the deploy workflow’s `GITHUB_TOKEN` can comment on fork PRs (`pull-requests: write` is already declared in the workflow).
   - After cutover, **disable GitHub Pages** for this repo if it was only used for this site (**Settings → Pages**).
   - **Later:** attach **`konflux.sh`** (or a subdomain) to the Worker; production URLs in Deployments follow Wrangler output (often `*.workers.dev` until custom domain is primary).
5. **Troubleshooting**
   - **Deploy job skipped:** wrong triggering workflow name (must match **`Build Site`** exactly), or `workflow_run.repository` not equal to current repo.
   - **`Could not determine Workers deployment URL`:** check `wrangler-action` outputs and Wrangler **4.x** stdout/stderr; workflow pins **4.30.0**.
   - **Artifact download 404:** deploy job needs `actions: read` and correct `run-id` (already set); build must have uploaded artifact **`site`**.
   - **No PR comment:** `workflow_run.pull_requests` empty and `pulls.list` with `head=owner:branch` did not return exactly one open PR (document for draft PRs or unusual head branches).

- [ ] **Step 2: Commit**

```bash
git add docs/site-deployment.md
git commit -m "docs: add documentation site Cloudflare operator runbook"
```

---

### Task 5: Phase 1 validation (fork)

**Files:** none (manual)

- [ ] **Step 1: Configure Cloudflare + GitHub** per `docs/site-deployment.md` on your fork.

- [ ] **Step 2: Push a commit on `main` that touches `docs/mindmap.html`**

Expected: **`Build Site`** succeeds; **`Deploy Site`** runs; Cloudflare **production Worker** updates; GitHub shows **`site-production`** with `environment_url` on **`workers.dev`** (or your custom host).

- [ ] **Step 3: Open a PR (same repo)** (any change that triggers **Build Site**)

Expected: **`wrangler versions upload`** preview; **`site-preview`** deployment; one PR comment updated on reruns.

- [ ] **Step 4: Open a PR from a second GitHub user / fork** (or your own fork of your fork) changing `docs/mindmap.html`**

Expected: build succeeds on the base repo without Cloudflare secrets in fork logs; deploy + comment still occur from the base repo’s deploy workflow.

---

### Task 6: Phase 2 — upstream PR

**Files:** none (manual); branch should contain Tasks 1–4 commits.

- [ ] **Step 1: Push your branch to origin and open a PR** against `konflux-ci/fullsend` (or upstream default branch).

- [ ] **Step 2: In the PR description**, list maintainer follow-ups: add Actions secrets/variables, verify fork workflow policy, disable legacy GitHub Pages when ready, optional `konflux.sh` DNS later.

- [ ] **Step 3: After merge**, repeat a subset of Task 5 checks on upstream.

---

## Plan self-review

**1. Spec coverage**

| Spec requirement | Task |
|------------------|------|
| Cloudflare Workers instead of GitHub Pages | Tasks 2–3 |
| Two-phase build + `workflow_run` deploy | Tasks 1–2 |
| `site-preview` / `site-production` | Task 2 (`createDeployment`) |
| PR comment upsert + PR resolution fallback | Task 2 (github-script) |
| PR head checkout | Task 1 |
| Fork-safe (no secrets on build) | Tasks 1 vs 2 permissions |
| Operator docs + phases | Tasks 4–6 |
| Concurrency | Both workflows |
| `*.workers.dev` then `konflux.sh` on Worker | Task 4 runbook |

**2. Placeholder scan**

No TBD/TODO left in workflow YAML or task text; `cloudflare/wrangler-action@v3.14.1` with **`wranglerVersion: "4.30.0"`**, github-script `v8`.

**3. Type / naming consistency**

- Single artifact name **`site`** in build and deploy.
- Build workflow display name must stay **`Build Site`** — it is the `workflow_run.workflows` filter target.
- Environment names exactly `site-preview` and `site-production`.

**Known follow-up (optional hardening):** If `createDeployment` returns **409** for a rare duplicate ref/environment case, extend the github-script to locate the existing deployment and only create a status (not required for normal one-commit-per-deploy usage).

---

**Plan complete and saved to `docs/superpowers/plans/2026-04-09-site-cloudflare-pages.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — Dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach do you want?**
