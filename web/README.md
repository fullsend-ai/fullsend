# Web

Browser-delivered static assets for the public site: the landing page, the interactive
document graph, and the custom 404 page under [`public/`](public/) (`index.html`,
`graph.html`, and `404.html`).

The **admin installation SPA** that used to live under `web/admin/` has been removed — see
[ADR 0019](../docs/ADRs/0019-web-source-and-cloudflare-site-layout.md) for the removal note.
There is no Vite build in this directory any more.

The documentation site is built separately by VitePress from [`../docs/`](../docs/)
(`npm run docs:build`). Wrangler configuration and the site Worker live only under
[`../cloudflare_site/`](../cloudflare_site/); that Worker is now a static-asset passthrough.
