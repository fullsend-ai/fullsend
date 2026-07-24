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

## Documentation versioning (investigation)

Users on older fullsend releases may encounter docs that describe features or
behaviors not present in their version. Versioned docs would let users view
documentation matching their installed release. This section captures the
feasibility investigation for future implementation.

### Options evaluated

| Approach | How it works | Effort | Trade-offs |
|----------|-------------|--------|------------|
| **VitePress multi-version** | Build docs from each release tag into a versioned path (e.g., `/docs/v0.21/`, `/docs/v0.22/`). Add a version switcher dropdown in the nav bar. | Medium–high | Requires CI changes to build and deploy per-tag. Storage grows linearly with releases. VitePress does not have built-in versioning — it must be implemented via custom config and multi-build CI. |
| **Branch-based versioning** | Maintain a `docs-vN` branch per major/minor release. Deploy each branch to a path prefix. | Medium | Backport burden — fixes to docs must be cherry-picked to each active branch. Works well for projects with long-lived release branches. |
| **Git tag snapshots** | At release time, snapshot `docs/` into a versioned archive or static build. Serve from a `/docs/archive/vN.N/` path. | Low–medium | Read-only archives — no live editing of old versions. Simple to implement but less polished than a version switcher. |
| **Deprecation notices only** (current approach) | Label deprecated features inline; do not version the docs. Users read one set of docs with deprecation markers. | Low (done) | Sufficient when the deprecation surface is small and migration paths are clear. Does not help users find docs for removed features. |

### Recommendation

The current approach — deprecation notices with migration guidance — is
sufficient for the near term. The project has a small number of deprecated
features, all with clear replacements and migration tooling
(`fullsend agent migrate-customizations`, `env.runner` migration). Versioned
docs add ongoing maintenance cost (per-release builds, backport burden) that
is not yet justified.

**When to revisit:** If a future release removes deprecated features entirely
(e.g., `runner_env` removal in ADR-0055 Phase 3, `customized/` removal), users
on older versions will lose reference material. At that point, the git tag
snapshot approach offers the best effort-to-value ratio: snapshot the docs at
the last release before removal and serve them as a read-only archive.

See [#4886](https://github.com/fullsend-ai/fullsend/issues/4886) for the
original discussion.
