# Web

Browser-delivered static assets for the public site: the landing page and the
custom 404 page under [`public/`](public/) (`index.html` and `404.html`).

There is no Vite build in this directory.

The documentation site is built separately by VitePress from [`../docs/`](../docs/)
(`npm run docs:build`). Wrangler configuration and the site Worker live only under
[`../cloudflare_site/`](../cloudflare_site/); that Worker is now a static-asset passthrough.
