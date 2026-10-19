/// <reference types="@cloudflare/workers-types" />

/**
 * Site Worker: serves the static documentation site via the `[assets]` binding.
 */
export interface Env {
  ASSETS?: Fetcher;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (url.protocol === "http:") {
      url.protocol = "https:";
      return Response.redirect(url.toString(), 301);
    }

    if (env.ASSETS != null) {
      return env.ASSETS.fetch(request);
    }
    return new Response("Worker misconfigured: ASSETS binding missing", {
      status: 503,
    });
  },
};
