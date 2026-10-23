// Smoke tests for the CF Worker mint bridge.
//
// These tests run inside @cloudflare/vitest-pool-workers (Miniflare)
// with the real Go WASM binary. They verify that the bridge boots —
// not full mint OIDC coverage. Run after `make wasm-stage` so that
// mintcore.wasm and wasm_exec.js are present.
import { env, exports } from "cloudflare:workers";
import {
  createExecutionContext,
  waitOnExecutionContext,
} from "cloudflare:test";
import { afterEach, describe, expect, it, vi } from "vitest";
import worker, { type Env } from "./index";

/** Mock RateLimit that always denies. */
const denyRateLimit: Env["MINT_TOKEN_RATE_LIMITER"] = {
  limit: async () => ({ success: false }),
};

/** Mock RateLimit that always allows. */
const allowRateLimit: Env["MINT_TOKEN_RATE_LIMITER"] = {
  limit: async () => ({ success: true }),
};

describe("mint worker bridge smoke", () => {
  // The vitest config intentionally omits ALLOWED_ORGS from bindings
  // to verify the Worker boots for per-repo-only deployments (parity
  // with Go mintcore which allows empty ALLOWED_ORGS since #5856).
  it("boots and serves /health without ALLOWED_ORGS", async () => {
    const resp = await exports.default.fetch("https://worker.test/health");
    expect(resp.status).toBe(200);

    const body = await resp.text();
    expect(body).toContain("ok");
  });

  it("returns 404 for unknown paths", async () => {
    const resp = await exports.default.fetch("https://worker.test/nonexistent");
    // Go's ServeHTTP routes this; unmatched paths return 404.
    expect(resp.status).toBe(404);
  });

  it("returns 405 for non-POST on /v1/token", async () => {
    const resp = await exports.default.fetch("https://worker.test/v1/token", {
      method: "GET",
    });
    // The mint handler rejects non-POST on the token endpoint.
    expect(resp.status).toBe(405);
  });
});

describe("mint worker rate limiting", () => {
  it("returns 429 rate_limited when MINT_TOKEN_RATE_LIMITER denies POST /v1/token", async () => {
    const url = new URL("https://worker.test/v1/token");
    const req = new Request(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: denyRateLimit,
    } as Env;
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(429);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("rate_limited");
  });

  it("returns 429 rate_limited when MINT_TOKEN_RATE_LIMITER denies POST /v1/bot-role", async () => {
    const req = new Request("https://worker.test/v1/bot-role", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: denyRateLimit,
    } as Env;
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(429);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("rate_limited");
  });

  it("rejects an encoded slash in the path instead of skipping the limiter", async () => {
    const req = new Request("https://worker.test/v1%2Fbot-role", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: denyRateLimit,
    } as Env;
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
  });

  it("rate-limits an otherwise-encoded /v1/bot-role path on its decoded form", async () => {
    const req = new Request("https://worker.test/v1/bot%2Drole", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: denyRateLimit,
    } as Env;
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(429);
  });

  it("rejects an invalid percent-escape in the path", async () => {
    const req = new Request("https://worker.test/v1/token%zz", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env as Env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
  });

  it("returns 429 rate_limited when the limiter denies the POST / token alias", async () => {
    const req = new Request("https://worker.test/", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: denyRateLimit,
    } as Env;
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(429);
  });

  it("allows POST /v1/bot-role when rate limiter permits", async () => {
    const req = new Request("https://worker.test/v1/bot-role", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const allowedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: allowRateLimit,
    } as Env;
    const res = await worker.fetch(req, allowedEnv, ctx);
    await waitOnExecutionContext(ctx);
    // Passes through to the WASM handler (400/401), not 429.
    expect(res.status).not.toBe(429);
  });

  it("allows POST /v1/token when rate limiter permits", async () => {
    const url = new URL("https://worker.test/v1/token");
    const req = new Request(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const allowedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: allowRateLimit,
    } as Env;
    const res = await worker.fetch(req, allowedEnv, ctx);
    await waitOnExecutionContext(ctx);
    // Request should pass through to the WASM handler (which will
    // reject with 401 due to missing OIDC token — not 429).
    expect(res.status).not.toBe(429);
  });

  it("does not rate-limit GET /health", async () => {
    const url = new URL("https://worker.test/health");
    const req = new Request(url, { method: "GET" });
    const ctx = createExecutionContext();
    // Even with a deny rate limiter, /health should not be affected
    // because rate limiting only applies to POST /v1/token.
    const limitedEnv: Env = {
      ...env,
      MINT_TOKEN_RATE_LIMITER: denyRateLimit,
    } as Env;
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(200);
    const body = await res.text();
    expect(body).toContain("ok");
  });

  it("rate limit key incorporates hostname for preview isolation", async () => {
    // Verify that different hostnames (simulating different preview
    // aliases) produce different rate limit keys by checking that a
    // request to one hostname can be rate-limited independently.
    const keys: string[] = [];
    const capturingRateLimit: Env["MINT_TOKEN_RATE_LIMITER"] = {
      limit: async (opts: { key: string }) => {
        keys.push(opts.key);
        return { success: true };
      },
    };

    // Request with first hostname (simulating a preview alias).
    const req1 = new Request("https://bt-run-42-mint.sub.workers.dev/v1/token", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "CF-Connecting-IP": "1.2.3.4",
      },
      body: "{}",
    });
    const ctx1 = createExecutionContext();
    await worker.fetch(
      req1,
      { ...env, MINT_TOKEN_RATE_LIMITER: capturingRateLimit } as Env,
      ctx1,
    );
    await waitOnExecutionContext(ctx1);

    // Request with second hostname (different preview alias).
    const req2 = new Request("https://bt-run-99-mint.sub.workers.dev/v1/token", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "CF-Connecting-IP": "1.2.3.4",
      },
      body: "{}",
    });
    const ctx2 = createExecutionContext();
    await worker.fetch(
      req2,
      { ...env, MINT_TOKEN_RATE_LIMITER: capturingRateLimit } as Env,
      ctx2,
    );
    await waitOnExecutionContext(ctx2);

    // Both requests should have been rate-limit-checked.
    expect(keys).toHaveLength(2);
    // Keys should differ because hostnames differ.
    expect(keys[0]).not.toBe(keys[1]);
    // Both keys should contain the respective hostnames.
    expect(keys[0]).toContain("bt-run-42-mint.sub.workers.dev");
    expect(keys[1]).toContain("bt-run-99-mint.sub.workers.dev");
    // Both keys should contain the client IP.
    expect(keys[0]).toContain("1.2.3.4");
    expect(keys[1]).toContain("1.2.3.4");
  });
});

describe("mint worker tracing disabled", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // This test file runs against the WASM built without a stamped traces
  // endpoint (index.tracing.test.ts covers the stamped build). A leftover
  // collector secret must not enable export on its own.
  it("makes no collector request without a stamped endpoint", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL): Promise<Response> => {
        urls.push(typeof input === "string" ? input : String(input));
        return new Response("{}", { status: 200 });
      },
    );
    const ctx = createExecutionContext();
    const res = await worker.fetch(
      new Request("https://worker.test/v1/token", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      }),
      { ...env, MINT_OTLP_HEADERS: "Authorization=Bearer unused" } as Env,
      ctx,
    );
    await waitOnExecutionContext(ctx);
    // Rejected by the mint (4xx), never rate-limited or failed.
    expect(res.status).toBeGreaterThanOrEqual(400);
    expect(res.status).toBeLessThan(500);
    expect(res.status).not.toBe(429);
    expect(urls).toEqual([]);
  });
});
