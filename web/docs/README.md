# Fullsend docs browser (Svelte SPA)

This directory is the **in-repo documentation browser**: a **Svelte 5 + Vite** app built as part of the shared root Vite config and served under **`/docs/`** in production.

## Local dev

From the repository root, **`npm run dev`** serves:

- **`/admin/`** — admin installation UI ([`../admin/README.md`](../admin/README.md))
- **`/docs/`** — this app

Design and behavior are described in the [docs browser design spec](../../docs/superpowers/specs/2026-05-04-docs-browser-design.md).

**`/api/*`** routes on the site Worker exist for the **admin** flow (OAuth, GitHub API proxy, etc.); the docs browser does not rely on them.

## Deep links (hash URLs)

Shared links should use the **`#/`** fragment so navigation stays in the docs SPA without path segments under `/docs/`.

- **Document:** `/docs/#/<routeKey>`
  Example: `/docs/#/guides/getting-started/installation` — `routeKey` matches the manifest path to the Markdown file (POSIX-style segments, no leading slash in the fragment).

- **Heading / in-page target:** `/docs/#/<routeKey>::<slug>`
  The **`::`** delimiter separates the document key from the heading **slug** (the `id` of the target element in the rendered page). It is not part of the slug itself; it only appears in the URL fragment so the app can load the right page and scroll to the right anchor.
