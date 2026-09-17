---
title: "19. Web source under web/ and Cloudflare project under cloudflare_site/"
status: Accepted
relates_to:
  - contributor-guidance
topics:
  - repository-layout
  - ci
  - cloudflare
---

# 19. Web source under `web/` and Cloudflare project under `cloudflare_site/`

Date: 2026-04-15

> **Note (2026-08-20):** The admin installation SPA described in points 1 and 3 was
> removed. `web/` now holds only the static landing page and document graph under
> `web/public/`; there is no Vite build. Point 1's principle still holds — Node tooling
> stays at the repository root — but its example scripts are stale: `npm run dev` and
> `npm run build` no longer exist. The root scripts today are `npm test`,
> `npm run dev:worker`, and `npm run docs:*`. The site Worker
> under `cloudflare_site/worker/` is a static-asset passthrough — its OAuth BFF was
> removed with the SPA. The `web/` vs `cloudflare_site/` split this ADR decided is
> unchanged, as is the Build Site / Deploy Site artifact contract. The public mint
> Worker (`internal/dispatch/cf/workersrc/`, `mint.fullsend.sh`) is a separate
> deployment and was never covered by this ADR.
>
> **Note (2026-09-17):** Site CI is a single workflow (`.github/workflows/site.yml`)
> with `build` and `deploy` jobs. The `_bundle/` artifact contract, artifact name
> `site`, and environments `site-preview` / `site-production` are unchanged. See
> [`site-deployment.md`](../site-deployment.md).

## Status

Accepted

## Context

This repository mixes design documents, Go CLI code (`cmd/`), and a small **browser-delivered** surface (today the interactive document graph). That surface was previously rooted at `docs/mindmap.html` while Cloudflare Wrangler lived under `site/`, which read as a generic “website” folder rather than “Cloudflare deploy boundary,” and blurred documentation versus deployable HTML. Separating **`web/`** (browser source) from **`cloudflare_site/`** (Wrangler + deploy-time static) makes layout and CI obvious for contributors.

## Decision

1. **Browser-oriented source** (static HTML today; future Vite entrypoints and assets) lives under **`web/`**. The landing page is **`web/public/index.html`** (served at `/`); the interactive document graph is **`web/public/graph.html`** (served at `/graph.html`). **Node tooling** (`package.json`, lockfile, and scripts such as `npm run dev` / `npm run build`) stays at the **repository root** so day-to-day work does not require `cd web`; Vite config may still point `root` at `web/` (or a subdirectory) for resolution.
2. The **sole Wrangler project** in this repository lives under **`cloudflare_site/`** (`wrangler.toml` from the default-branch checkout on deploy, plus **`public/`** and **`worker/`** filled from the Build Site artifact). The GitHub Actions **Deploy Site** workflow continues to use **`workingDirectory: cloudflare_site`**, downloads the artifact to **`_bundle/`**, and copies only **`public/`** and **`worker/`** into **`cloudflare_site/`**; workflow **names** (`Build Site`, artifact `site`, environments `site-preview` / `site-production`) stay stable for existing automation and operators.
3. **Build Site** assembles **`_bundle/`** from `web/` (and later from JS build outputs) without changing that deploy contract.

## Consequences

- Contributors edit the landing page at **`web/public/index.html`** and the document graph at **`web/public/graph.html`** (formerly `docs/mindmap.html`, then `web/public/index.html`).
- Paths in runbooks and local Wrangler preview use **`cloudflare_site/`** instead of **`site/`**.
- Future SPA work adds **Vite** (and related devDependencies) using the **root** `package.json`, with source and config under **`web/`**, and extends **`site-build.yml`** to merge build output into **`_bundle/public/`** (and keep **`_bundle/worker/`** in sync) without renaming **`cloudflare_site/`** again.
- Links and docs that referred to `site/wrangler.toml` or `docs/mindmap.html` must be updated when backporting older instructions.
