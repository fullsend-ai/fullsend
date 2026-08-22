# Documentation site

The documentation site is built with **[VitePress](https://vitepress.dev/)** and **[VitePress Theme +](https://vitepress-theme-default-plus.lando.dev/)**. Markdown source and site configuration both live in `docs/` (config in `docs/.vitepress/config.ts`), and build output goes to `docs/.vitepress/dist/`.

## Local development

```bash
npm ci
npm run docs:dev
```

The dev server starts on `http://localhost:5173/docs/`. Submodules (e.g. `experiments/`) are initialized automatically before the dev server starts -- no manual `git submodule` step needed.

## Building

```bash
npm run docs:build
```

The `docs:build` script runs `git submodule update --init` and then `mvb docs`, which builds versioned documentation for each qualifying git tag plus a `dev` build from the current tree. The `mvb` CLI (from `@lando/vitepress-theme-default-plus`) sets `VPL_MVB_VERSION` per sub-build; `config.ts` reads this to label the sidebar version switcher (falling back to `"dev"` for local development). CI also sets `VPL_MVB_BRANCH` to the current commit SHA so `mvb` knows which ref to treat as the development head.

## How it works

- `docs/` contains all markdown content, organized by section (agents, guides, ADRs, etc.)
- `docs/.vitepress/config.ts` defines the sidebar navigation and markdown processing
- `getMarkdownFiles()` auto-discovers markdown files and subdirectory READMEs for dynamic sidebar sections (ADRs, experiments, design docs, specs, plans)
- Symlinks connect submodule content into `docs/` (e.g. `docs/experiments` -> `../experiments`)
- The `search.options.scopes` array in `config.ts` defines the scope pills shown in the search modal. Each scope has a `label` and a list of `prefixes` (path prefixes like `/docs/guides/`). When a user activates a scope, search results are filtered to pages whose path starts with one of the scope's prefixes. Every `docs/` subfolder that produces rendered pages must appear in at least one scope; otherwise its pages become unreachable when any scope pill is active.
- `multiVersionBuild` at `docs/.vitepress/config.ts` controls which versions are to be built. `sidebarEnder` sets up the version switcher with a few versions and the page `/v/index.md` contains a more comprehensive list of versions.

## Submodules

Some doc content lives in separate repositories linked as git submodules:

| Submodule | Path | Docs symlink |
|-----------|------|-------------|
| [fullsend-ai/experiments](https://github.com/fullsend-ai/experiments) | `experiments/` | `docs/experiments` -> `../experiments` |

The `docs:dev` and `docs:build` scripts in the root `package.json` handle submodule initialization automatically. CI uses `submodules: true` on `actions/checkout` in `.github/workflows/site-build.yml`.

## CI/CD

- **`.github/workflows/site-build.yml`** — builds the VitePress site on PRs and pushes to `main`, uploads the artifact
- **`.github/workflows/site-deploy.yml`** — deploys the built artifact to Cloudflare Workers on `main` pushes, uploads preview versions on PRs

For Cloudflare Worker setup and troubleshooting, see [`site-deployment.md`](site-deployment.md).
