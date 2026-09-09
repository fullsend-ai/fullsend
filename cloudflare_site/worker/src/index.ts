/// <reference types="@cloudflare/workers-types" />

/**
 * Site Worker: serves the static documentation site via the `[assets]` binding.
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
