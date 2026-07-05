# Documentation site

The documentation site is built with **[VitePress](https://vitepress.dev/)**. Markdown source lives in `docs/`, site configuration in `website/.vitepress/config.ts`, and build output in `website/dist/`.

## Local development

```bash
cd website
npm ci
npm run dev
```

The dev server starts on `http://localhost:5173/docs/`. Submodules (e.g. `experiments/`) are initialized automatically via a `predev` hook — no manual `git submodule` step needed.

## Building

```bash
cd website
npm run build
```

A `prebuild` hook runs `git submodule update --init` before the VitePress build, matching CI behavior.

## How it works

- `docs/` contains all markdown content, organized by section (agents, guides, ADRs, etc.)
- `website/.vitepress/config.ts` defines the sidebar navigation and markdown processing
- `getMarkdownFiles()` auto-discovers markdown files and subdirectory READMEs for dynamic sidebar sections (ADRs, experiments, design docs, specs, plans)
- Symlinks connect submodule content into `docs/` (e.g. `docs/experiments` → `../experiments`)

## Submodules

Some doc content lives in separate repositories linked as git submodules:

| Submodule | Path | Docs symlink |
|-----------|------|-------------|
| [fullsend-ai/experiments](https://github.com/fullsend-ai/experiments) | `experiments/` | `docs/experiments` → `../experiments` |

The `predev` and `prebuild` hooks in `website/package.json` handle initialization automatically for local dev. CI uses `submodules: true` on `actions/checkout` in `.github/workflows/site-build.yml`.

## CI/CD

- **`.github/workflows/site-build.yml`** — builds the VitePress site on PRs and pushes to `main`, uploads the artifact
- **`.github/workflows/site-deploy.yml`** — deploys the built artifact to Cloudflare Workers on `main` pushes, uploads preview versions on PRs

For Cloudflare Worker setup, secrets, and troubleshooting, see [`web-admin-deployment.md`](web-admin-deployment.md).
