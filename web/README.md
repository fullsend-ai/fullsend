# Web

Browser-delivered assets for the public site: static files under `public/` today (document graph as `index.html`), with a future single Vite app expected to build into output that CI stages under **`_bundle/`** for the **Build Site** artifact. **`package.json` / `npm run dev` stay at the repository root** (Vite can still use this tree as its source root). Wrangler configuration and the Worker live only under [`../cloudflare_site/`](../cloudflare_site/).
