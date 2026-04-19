import { describe, expect, it } from "vitest";
import {
  effectiveCorsOrigin,
  hasNonEmptyTurnstileKeys,
  isAllowedOAuthRedirectUri,
  isLocalhostHttpOrigin,
} from "./oauthCors";

describe("isAllowedOAuthRedirectUri", () => {
  it("allows HTTPS redirect URIs under /admin/", () => {
    expect(
      isAllowedOAuthRedirectUri("https://preview.example.com/admin/"),
    ).toBe(true);
    expect(
      isAllowedOAuthRedirectUri("https://preview.example.com/admin/oauth/callback.html"),
    ).toBe(true);
  });

  it("allows loopback HTTP with /admin/ path", () => {
    expect(
      isAllowedOAuthRedirectUri("http://localhost:5173/admin/"),
    ).toBe(true);
    expect(
      isAllowedOAuthRedirectUri("http://127.0.0.1:8787/admin/oauth/callback.html"),
    ).toBe(true);
    expect(isAllowedOAuthRedirectUri("http://[::1]:3000/admin/")).toBe(true);
  });

  it("rejects wrong path, protocol, or host", () => {
    expect(isAllowedOAuthRedirectUri("https://evil.com/other/")).toBe(false);
    expect(isAllowedOAuthRedirectUri("https://evil.com/admin/extra")).toBe(
      false,
    );
    expect(
      isAllowedOAuthRedirectUri("http://evil.com/admin/"),
    ).toBe(false);
    expect(isAllowedOAuthRedirectUri("ftp://localhost/admin/")).toBe(false);
    expect(isAllowedOAuthRedirectUri("not-a-url")).toBe(false);
  });
});

describe("isLocalhostHttpOrigin", () => {
  it("matches loopback HTTP origins", () => {
    expect(isLocalhostHttpOrigin("http://localhost:5173")).toBe(true);
    expect(isLocalhostHttpOrigin("http://127.0.0.1:8787")).toBe(true);
  });

  it("rejects HTTPS loopback and empty", () => {
    expect(isLocalhostHttpOrigin("https://localhost:5173")).toBe(false);
    expect(isLocalhostHttpOrigin("")).toBe(false);
  });
});

describe("effectiveCorsOrigin", () => {
  it("returns loopback Origin for admin API paths", () => {
    const url = new URL("https://worker.example/api/oauth/token");
    const req = new Request(url, {
      method: "OPTIONS",
      headers: { Origin: "http://localhost:5173" },
    });
    expect(effectiveCorsOrigin(req, url)).toBe("http://localhost:5173");
  });

  it("returns deployed origin only when Origin matches the worker URL", () => {
    const site = "https://worker.example";
    const tokenUrl = new URL(`${site}/api/oauth/token`);
    const ok = new Request(tokenUrl, {
      method: "OPTIONS",
      headers: { Origin: site },
    });
    expect(effectiveCorsOrigin(ok, tokenUrl)).toBe(site);

    const bad = new Request(tokenUrl, {
      method: "OPTIONS",
      headers: { Origin: "https://evil.example" },
    });
    expect(effectiveCorsOrigin(bad, tokenUrl)).toBe(null);
  });

  it("for authorize without Origin, returns redirect_uri origin when navigation binding passes", () => {
    const site = "https://worker.example";
    const authorizeUrl = new URL(
      `${site}/api/oauth/authorize?redirect_uri=${encodeURIComponent(`${site}/admin/`)}&state=nonce12345&code_challenge=x&code_challenge_method=S256`,
    );
    const req = new Request(authorizeUrl, { method: "GET" });
    expect(effectiveCorsOrigin(req, authorizeUrl)).toBe(site);
  });

  it("for /api/github/user without Origin, infers from Sec-Fetch-Site + Referer", () => {
    const site = "https://worker.example";
    const userUrl = new URL(`${site}/api/github/user`);
    const req = new Request(userUrl, {
      method: "OPTIONS",
      headers: {
        "Sec-Fetch-Site": "same-origin",
        Referer: `${site}/admin/`,
      },
    });
    expect(effectiveCorsOrigin(req, userUrl)).toBe(site);
  });
});

describe("hasNonEmptyTurnstileKeys", () => {
  it("requires both keys non-empty after trim", () => {
    expect(
      hasNonEmptyTurnstileKeys({
        TURNSTILE_SITE_KEY: "a",
        TURNSTILE_SECRET_KEY: "b",
      }),
    ).toBe(true);
    expect(
      hasNonEmptyTurnstileKeys({
        TURNSTILE_SITE_KEY: "  ",
        TURNSTILE_SECRET_KEY: "b",
      }),
    ).toBe(false);
    expect(
      hasNonEmptyTurnstileKeys({
        TURNSTILE_SITE_KEY: "a",
        TURNSTILE_SECRET_KEY: "",
      }),
    ).toBe(false);
  });
});
