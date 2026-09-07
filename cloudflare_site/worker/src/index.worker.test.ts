import { env } from "cloudflare:workers";
import { createExecutionContext, SELF, waitOnExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import worker, { type Env } from "./index";

/** ASSETS stub that records what the Worker forwarded to it. */
function assetsStub(body = "asset") {
  const seen: string[] = [];
  const fetcher = {
    fetch: async (request: Request) => {
      seen.push(new URL(request.url).pathname);
      return new Response(body, { status: 200 });
    },
  } as unknown as Fetcher;
  return { fetcher, seen };
}

describe("site worker", () => {
  it("passes requests through to the ASSETS binding", async () => {
    const { fetcher, seen } = assetsStub("<!doctype html>");
    const ctx = createExecutionContext();

    const res = await worker.fetch(
      new Request("https://example.test/docs/"),
      { ASSETS: fetcher },
      ctx,
    );
    await waitOnExecutionContext(ctx);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<!doctype html>");
    expect(seen).toEqual(["/docs/"]);
  });

  it("returns 503 when the ASSETS binding is missing", async () => {
    const ctx = createExecutionContext();

    const res = await worker.fetch(
      new Request("https://example.test/"),
      {} as Env,
      ctx,
    );
    await waitOnExecutionContext(ctx);

    expect(res.status).toBe(503);
    expect(await res.text()).toContain("ASSETS binding missing");
  });

  it("no longer handles the retired admin OAuth routes", async () => {
    // Before removal these three were intercepted by the Worker's OAuth BFF and
    // never reached ASSETS. Asserting the forwarded paths (not just the body)
    // is what proves no stray handler survives: a stub that always returns the
    // same body would pass a body-only assertion even if a handler still ran.
    const retired = [
      "/api/oauth/authorize",
      "/api/oauth/token",
      "/api/github/user",
    ];
    const { fetcher, seen } = assetsStub();

    for (const path of retired) {
      const ctx = createExecutionContext();
      const res = await worker.fetch(
        new Request(`https://example.test${path}`),
        { ASSETS: fetcher },
        ctx,
      );
      await waitOnExecutionContext(ctx);
      expect(res.status).toBe(200);
    }

    expect(seen).toEqual(retired);
  });
});

describe("wrangler bindings", () => {
  it("provides env.ASSETS from wrangler.toml", () => {
    expect((env as Env).ASSETS).toBeDefined();
  });
});

async function fetchPath(path: string, navigate: boolean): Promise<Response> {
  const headers = navigate ? { "Sec-Fetch-Mode": "navigate" } : undefined;
  return SELF.fetch(`https://example.test${path}`, { headers });
}

describe("asset 404-page routing", () => {
  it.each(["/does-not-exist", "/admin/foo"])(
    "returns the root 404 page for %s",
    async (path) => {
      for (const navigate of [true, false]) {
        const res = await fetchPath(path, navigate);
        expect(res.status).toBe(404);
        expect(await res.text()).toContain("Page not found");
      }
    },
  );

  it("returns the docs 404 page for misses under /docs/", async () => {
    for (const navigate of [true, false]) {
      const res = await fetchPath("/docs/this-does-not-exist", navigate);
      expect(res.status).toBe(404);
      expect(await res.text()).toContain("docs not found");
    }
  });

  it("serves the landing page at /", async () => {
    const res = await fetchPath("/", false);
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("landing");
  });
});
