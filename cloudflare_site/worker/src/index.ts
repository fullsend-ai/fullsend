/// <reference types="@cloudflare/workers-types" />

/**
 * Minimal Worker: serve static assets from `[assets]` only.
 * Future OAuth or API routes extend this file; Wrangler project layout stays stable.
 */
export interface Env {
  ASSETS?: Fetcher;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    if (env.ASSETS != null) {
      return env.ASSETS.fetch(request);
    }
    return new Response("Worker misconfigured: ASSETS binding missing", {
      status: 503,
    });
  },
};
