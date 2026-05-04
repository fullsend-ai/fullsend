import { env } from "cloudflare:workers";
import {
  createExecutionContext,
  waitOnExecutionContext,
} from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import worker, { handleAdminApi, type Env } from "./index";

const IncomingRequest = Request;

const denyRateLimit: Env["OAUTH_TOKEN_RATE_LIMITER"] = {
  limit: async () => ({ success: false }),
};

const allowRateLimit: Env["OAUTH_TOKEN_RATE_LIMITER"] = {
  limit: async () => ({ success: true }),
};

function authorizeUrl(redirect: string): URL {
  const sp = new URLSearchParams({
    redirect_uri: redirect,
    state: "12345678",
    code_challenge: "E9Melhoaq2wv2FgFCGTrimrGQ6bc6oVe3gtPKSmlVSs",
    code_challenge_method: "S256",
  });
  return new URL(`https://worker.test/api/oauth/authorize?${sp}`);
}

describe("site worker admin API", () => {
  let originalFetch: typeof fetch;

  beforeEach(() => {
    originalFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("OPTIONS returns 204 with CORS headers when Origin is allowed (localhost)", async () => {
    const url = new URL("https://worker.test/api/oauth/token");
    const req = new IncomingRequest(url, {
      method: "OPTIONS",
      headers: { Origin: "http://127.0.0.1:5173" },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(204);
    expect(res.headers.get("Access-Control-Allow-Origin")).toBe(
      "http://127.0.0.1:5173",
    );
  });

  it("OPTIONS returns 403 without usable CORS origin for admin API", async () => {
    const site = "https://worker.test";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "OPTIONS",
      headers: {
        Origin: "https://attacker.example",
      },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(403);
  });

  it("returns 403 forbidden_origin for token POST when Origin does not match redirect_uri", async () => {
    const site = "https://worker.test";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: "http://localhost:9999",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        code: "abc",
        redirect_uri: "http://localhost:5173/admin/",
        code_verifier: "verifierverifierverifierverifier01",
        turnstile_token: "dummy",
      }),
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("forbidden_origin");
  });

  it("returns 503 missing_turnstile_keys when Turnstile vars are empty", async () => {
    const site = "https://worker.test";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "OPTIONS",
      headers: { Origin: "http://localhost:5173" },
    });
    const ctx = createExecutionContext();
    const strippedEnv = {
      ...env,
      TURNSTILE_SITE_KEY: "",
      TURNSTILE_SECRET_KEY: "",
    };
    const res = await worker.fetch(req, strippedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(503);
    const body = (await res.json()) as { detail?: string };
    expect(body.detail).toBe("missing_turnstile_keys");
  });

  it("exchanges token with mocked GitHub and Turnstile (no real network)", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.includes("challenges.cloudflare.com/turnstile")) {
        return new Response(JSON.stringify({ success: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      if (url.includes("github.com/login/oauth/access_token")) {
        return new Response(
          JSON.stringify({
            access_token: "gho_test_token",
            token_type: "bearer",
            expires_in: 3600,
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        );
      }
      return new Response(`unexpected fetch: ${url}`, { status: 500 });
    }) as typeof fetch;

    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        code: "exchange-code",
        redirect_uri: redirect,
        code_verifier: "verifierverifierverifierverifier01",
        turnstile_token: "dummy-turnstile",
      }),
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { access_token?: string };
    expect(body.access_token).toBe("gho_test_token");
  });

  it("returns 400 with GitHub OAuth error when token exchange fails (e.g. bad PKCE)", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.includes("challenges.cloudflare.com/turnstile")) {
        return new Response(JSON.stringify({ success: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      if (url.includes("github.com/login/oauth/access_token")) {
        return new Response(
          JSON.stringify({
            error: "bad_verification_code",
            error_description:
              "The code passed is incorrect or expired, or the PKCE verification failed.",
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        );
      }
      return new Response(`unexpected fetch: ${url}`, { status: 500 });
    }) as typeof fetch;

    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        code: "exchange-code",
        redirect_uri: redirect,
        code_verifier: "wrong-verifier-wrong-verifier-wrong01",
        turnstile_token: "dummy-turnstile",
      }),
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
    const body = (await res.json()) as {
      error?: string;
      error_description?: string | null;
    };
    expect(body.error).toBe("bad_verification_code");
    expect(body.error_description).toContain("PKCE");
  });

  it("proxies /api/github/user with mocked api.github.com", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.startsWith("https://api.github.com/user")) {
        return new Response(
          JSON.stringify({
            login: "octocat",
            name: "Octo Cat",
            avatar_url: "https://avatars.githubusercontent.com/u/1?v=4",
            email: "octocat@example.com",
            two_factor_authentication: true,
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        );
      }
      return new Response(`unexpected fetch: ${url}`, { status: 500 });
    }) as typeof fetch;

    const site = "https://worker.test";
    const url = new URL(`${site}/api/github/user`);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: {
        Origin: "http://localhost:5173",
        Authorization: "Bearer gho_fake",
        Accept: "application/vnd.github+json",
      },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(200);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body).toEqual({
      login: "octocat",
      name: "Octo Cat",
      avatar_url: "https://avatars.githubusercontent.com/u/1?v=4",
    });
    expect(body).not.toHaveProperty("email");
    expect(body).not.toHaveProperty("two_factor_authentication");
  });

  it("returns 429 rate_limited when OAUTH_TOKEN_RATE_LIMITER denies", async () => {
    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        code: "c",
        redirect_uri: redirect,
        code_verifier: "verifierverifierverifierverifier01",
        turnstile_token: "t",
      }),
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      OAUTH_TOKEN_RATE_LIMITER: denyRateLimit,
    };
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(429);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("rate_limited");
  });

  it("returns 429 rate_limited when GITHUB_USER_RATE_LIMITER denies", async () => {
    const site = "https://worker.test";
    const url = new URL(`${site}/api/github/user`);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: {
        Origin: "http://localhost:5173",
        Authorization: "Bearer gho_fake",
      },
    });
    const ctx = createExecutionContext();
    const limitedEnv: Env = {
      ...env,
      GITHUB_USER_RATE_LIMITER: denyRateLimit,
    };
    const res = await worker.fetch(req, limitedEnv, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(429);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("rate_limited");
  });

  it("returns 400 turnstile_required when turnstile_token is missing on token POST", async () => {
    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        code: "exchange-code",
        redirect_uri: redirect,
        code_verifier: "verifierverifierverifierverifier01",
      }),
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("turnstile_required");
  });

  it("returns 403 turnstile_failed when Turnstile siteverify returns success: false", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.includes("challenges.cloudflare.com/turnstile")) {
        return new Response(JSON.stringify({ success: false }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return new Response(`unexpected fetch: ${url}`, { status: 500 });
    }) as typeof fetch;

    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        code: "exchange-code",
        redirect_uri: redirect,
        code_verifier: "verifierverifierverifierverifier01",
        turnstile_token: "bad-token",
      }),
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("turnstile_failed");
  });

  it("returns 403 forbidden_origin on authorize when Origin does not match redirect_uri", async () => {
    const redirect = "http://localhost:5173/admin/";
    const url = authorizeUrl(redirect);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: { Origin: "http://localhost:8888" },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("forbidden_origin");
  });

  it("returns 403 forbidden_origin on authorize when navigation cannot bind CORS (no Origin)", async () => {
    const redirect = "https://evil.example/admin/";
    const url = authorizeUrl(redirect);
    const req = new IncomingRequest(url, { method: "GET" });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("forbidden_origin");
  });

  it("returns 400 param_too_long on authorize for oversized PKCE challenge", async () => {
    const redirect = "http://localhost:5173/admin/";
    const sp = new URLSearchParams({
      redirect_uri: redirect,
      state: "12345678",
      code_challenge: "a".repeat(257),
      code_challenge_method: "S256",
    });
    const url = new URL(`https://worker.test/api/oauth/authorize?${sp}`);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: { Origin: new URL(redirect).origin },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("param_too_long");
  });

  it("returns 400 param_too_long on authorize for oversized client state nonce", async () => {
    const redirect = "http://localhost:5173/admin/";
    const sp = new URLSearchParams({
      redirect_uri: redirect,
      state: "a".repeat(129),
      code_challenge: "E9Melhoaq2wv2FgFCGTrimrGQ6bc6oVe3gtPKSmlVSs",
      code_challenge_method: "S256",
    });
    const url = new URL(`https://worker.test/api/oauth/authorize?${sp}`);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: { Origin: new URL(redirect).origin },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("param_too_long");
  });

  it("returns 400 missing_or_invalid_oauth_params on authorize when code_challenge_method is not S256", async () => {
    const redirect = "http://localhost:5173/admin/";
    const sp = new URLSearchParams({
      redirect_uri: redirect,
      state: "12345678",
      code_challenge: "E9Melhoaq2wv2FgFCGTrimrGQ6bc6oVe3gtPKSmlVSs",
      code_challenge_method: "plain",
    });
    const url = new URL(`https://worker.test/api/oauth/authorize?${sp}`);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: { Origin: new URL(redirect).origin },
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(req, env, ctx);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("missing_or_invalid_oauth_params");
  });

  it("returns 415 unsupported_media_type for token POST without JSON content-type", async () => {
    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "text/plain",
      },
      body: "{}",
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(
      req,
      { ...env, OAUTH_TOKEN_RATE_LIMITER: allowRateLimit },
      ctx,
    );
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(415);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("unsupported_media_type");
  });

  it("returns 400 invalid_json for token POST with malformed JSON body", async () => {
    const site = "https://worker.test";
    const redirect = "http://localhost:5173/admin/";
    const url = new URL(`${site}/api/oauth/token`);
    const req = new IncomingRequest(url, {
      method: "POST",
      headers: {
        Origin: new URL(redirect).origin,
        "Content-Type": "application/json",
      },
      body: "{not-json",
    });
    const ctx = createExecutionContext();
    const res = await worker.fetch(
      req,
      { ...env, OAUTH_TOKEN_RATE_LIMITER: allowRateLimit },
      ctx,
    );
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("invalid_json");
  });

  it("handleAdminApi fallthrough returns 404 with CORS (no out-of-scope cors reference)", async () => {
    const site = "https://worker.test";
    const url = new URL(`${site}/api/oauth/future-slot`);
    const req = new IncomingRequest(url, {
      method: "GET",
      headers: { Origin: "http://localhost:5173" },
    });
    const ctx = createExecutionContext();
    const res = await handleAdminApi(req, env, url);
    await waitOnExecutionContext(ctx);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe("not_found");
    expect(res.headers.get("Access-Control-Allow-Origin")).toBe(
      "http://localhost:5173",
    );
  });
});
