# Design: Documentation site on Cloudflare Workers (static assets, PR previews, fork-safe CI)

Date: 2026-04-09
Status: Draft (brainstorm consolidated)

> **Note:** Filename retains `cloudflare-pages` for link stability; the implementation uses **Workers + static assets** ([migration guide](https://developers.cloudflare.com/workers/static-assets/migration-guides/migrate-from-pages/)), not `wrangler pages deploy`.

## Context

The repository publishes a **static documentation site**. Today the primary surface is the interactive document graph in `docs/mindmap.html`; the site will likely **grow** (more pages or assets under `docs/` or a dedicated static tree). CI treats this as **one deployable site**: produce a directory (today `_site/` with `index.html` from the mindmap), upload it as artifact **`site`**, then deploy from **`site/public/`** using Wrangler and [`site/wrangler.toml`](../../../site/wrangler.toml).

**Implemented:** [`.github/workflows/site-build.yml`](../../../.github/workflows/site-build.yml) and [`.github/workflows/site-deploy.yml`](../../../.github/workflows/site-deploy.yml) use the build → artifact → `workflow_run` deploy split. **Production** uses **`wrangler deploy`** (Worker + static assets). **Pull requests** use **`wrangler versions upload --preview-alias …`** so previews get a stable **`*.workers.dev`** URL without promoting a new production version. The previous GitHub Pages workflow has been **removed**.

**Operator setup:** Cloudflare **Worker**, API token with **Workers** permissions, and GitHub Actions secrets/variables are required; see [`docs/site-deployment.md`](../../site-deployment.md).

## Goals

- Deploy this **documentation site** to **Cloudflare Workers** (static assets binding), not GitHub Pages and not the legacy Pages-only upload path.
- **Per-PR previews**, including **fork PRs**, using the **two-workflow** pattern: unprivileged build + artifact, privileged deploy.
- Integrate with **GitHub Deployments** using **`site-preview`** and **`site-production`**, with `environment_url` pointing at the Worker URL (`*.workers.dev` or custom domain).
- Surface preview links via **Deployments** and a **single upserted PR comment**.
- Use **stable workflow and artifact names** centered on **site** as content grows beyond the mindmap.
- Roll out in **two phases** (fork validation, then upstream). Custom domains (e.g. **`konflux.sh`**) attach to the **Worker** when DNS is ready.

## Non-goals

- Rewriting site **application** code beyond packaging.
- **Workers Builds** (Cloudflare-hosted CI) as the source of truth—**GitHub Actions** remains the deploy driver unless the project later opts in.
- OIDC to Cloudflare in the initial design; **API token** in secrets is sufficient.

## Approach comparison (condensed)

| Approach | Idea | Verdict |
|----------|------|--------|
| **A — `workflow_run` + artifact** | Secretless build uploads artifact; privileged workflow deploys. | **Chosen.** Fork-safe. |
| **B — `pull_request_target`** | Deploy with base-repo secrets on PR. | **Rejected** for untrusted build steps. |
| **C — External bot** | Webhook-driven deploy. | **Rejected** for this static site. |

**Deploy tooling:** **Wrangler 4.x** via `cloudflare/wrangler-action`, **`wrangler deploy`** for **`push` to `main`**, **`wrangler versions upload --preview-alias`** for **`pull_request`** previews ([preview URLs](https://developers.cloudflare.com/workers/configuration/previews/)).

## Architecture

### Workflow split

1. **Build (`site-build.yml`):** `pull_request` + `push` to `main` (no `paths` filter in current fork—runs on every PR/push; may be narrowed later). Produces **`site`** artifact (`_site/`).
2. **Deploy (`site-deploy.yml`):** On successful **Build Site**, checkout (for `site/wrangler.toml`), download artifact into **`site/public/`**, then:
   - **`push`:** `wrangler deploy --name=<var>` → production.
   - **`pull_request`:** `wrangler versions upload --assets public --preview-alias pr-<workflow_run.id>` → preview only.

**Permissions:** Build: `contents: read` only. Deploy: `actions: read`, `deployments: write`, `pull-requests: write`. No `pages: write` for this site.

### `site/wrangler.toml`

- **`[assets].directory`:** `./public` (filled in CI).
- **`not_found_handling = "single-page-application"`** for the single-page mindmap.
- **`workers_dev = true`**, **`preview_urls = true`**.
- **No `main`** (assets-only Worker).

### GitHub environment names

- **`site-preview`** — PR uploads; `transient_environment: true`.
- **`site-production`** — production deploy; `production_environment: true`.

### Cloudflare

- One **Worker**; GitHub variable **`CLOUDFLARE_PROJECT_NAME`** holds the Worker name (name kept for backward compatibility).
- API token: **Workers** (and Account Read if needed), not Pages-only.

### Domains

- Default **`*.workers.dev`**; custom domains on the Worker when ready (**`konflux.sh`** upstream).

### Security (fork PRs)

Same as before: minimal auditable build; deploy trusts artifacts from the known build workflow.

### Removal of GitHub Pages

**Done.** Disable **Settings → Pages** if unused.

## Rollout phases

### Phase 1 — Fork

- Configure Worker + token + secrets; validate production and fork PR preview + comment.

### Phase 2 — Upstream

- Land workflows + runbook; org secrets; Worker + optional **`konflux.sh`** on Worker routes.

## Operator documentation

See [`docs/site-deployment.md`](../../site-deployment.md).

## Testing and acceptance

- **`main` push:** production Worker updates; **`site-production`** deployment with correct URL.
- **PR:** preview URL on `workers.dev`, **`site-preview`**, PR comment; fork build has no secrets.
- **Wrangler:** pinned **4.30.0** in workflow; preview alias requires **≥ 4.21.0**.

## Spec self-review

- **Consistency:** `site-preview` / `site-production`; Worker + static assets; artifact **`site`**.
- **Scope:** CI and packaging only.
- **Workers vs Pages:** Implementation is Workers; filename is historical.
