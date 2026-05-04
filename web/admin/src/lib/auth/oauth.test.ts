import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./turnstile", () => ({
  obtainTurnstileToken: vi.fn(),
}));

vi.mock("./session", () => ({
  refreshSession: vi.fn(),
}));

import { obtainTurnstileToken } from "./turnstile";
import { refreshSession } from "./session";
import { challengeS256 } from "./pkce";
import {
  completeGithubOAuthFromHandoff,
  consumeOAuthParamsFromDocumentUrl,
  getOAuthRedirectUri,
  SIGNING_IN_CANCELLED_MESSAGE,
  startGithubSignIn,
  tryParseWorkerExpandedOauthState,
} from "./oauth";
import { loadToken } from "./tokenStore";

const originalWindowLocation = window.location;

function installLocationStub(stub: Record<string, unknown>) {
  Reflect.deleteProperty(window, "location");
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: stub as unknown as Location,
  });
}

function restoreWindowLocation() {
  Reflect.deleteProperty(window, "location");
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: originalWindowLocation,
  });
}

const OAUTH_DOC_HANDOFF_KEY = "fullsend_admin_oauth_doc_handoff";
const OAUTH_STATE_KEY = "fullsend_admin_oauth_state";
const PKCE_VERIFIER_KEY = "fullsend_admin_pkce_verifier";

function workerExpandedStateB64(n: string, k = "0x4AAA_sitekey"): string {
  const payload = { v: 1, n, k };
  const bytes = new TextEncoder().encode(JSON.stringify(payload));
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

describe("tryParseWorkerExpandedOauthState", () => {
  it("returns null for raw UUID state", () => {
    expect(
      tryParseWorkerExpandedOauthState("550e8400-e29b-41d4-a716-446655440000"),
    ).toBeNull();
  });

  it("parses worker-expanded base64url JSON state", () => {
    const b64 = workerExpandedStateB64("nonce-value");
    expect(tryParseWorkerExpandedOauthState(b64)).toEqual({
      v: 1,
      n: "nonce-value",
      k: "0x4AAA_sitekey",
    });
  });
});

describe("startGithubSignIn", () => {
  let randomUUIDSpy: { mockRestore(): void };

  beforeEach(() => {
    sessionStorage.clear();
    randomUUIDSpy = vi.spyOn(crypto, "randomUUID").mockReturnValue(
      "00000000-0000-4000-8000-000000000001",
    );
    const assign = vi.fn();
    installLocationStub({
      origin: "https://oauth-start.test",
      search: "",
      href: "https://oauth-start.test/admin/",
      assign,
    });
  });

  afterEach(() => {
    randomUUIDSpy.mockRestore();
    restoreWindowLocation();
  });

  it("stores PKCE verifier and state, then assigns authorize URL with S256 challenge", async () => {
    const assign = window.location.assign as ReturnType<typeof vi.fn>;

    await startGithubSignIn();

    const verifier = sessionStorage.getItem(PKCE_VERIFIER_KEY);
    const state = sessionStorage.getItem(OAUTH_STATE_KEY);
    expect(verifier).toBeTruthy();
    expect(state).toBe("00000000-0000-4000-8000-000000000001");

    expect(assign).toHaveBeenCalledOnce();
    const target = assign.mock.calls[0]![0] as string;
    const u = new URL(target);
    expect(u.origin).toBe("https://oauth-start.test");
    expect(u.pathname).toBe("/api/oauth/authorize");
    expect(u.searchParams.get("code_challenge_method")).toBe("S256");
    expect(u.searchParams.get("state")).toBe(state);
    expect(u.searchParams.get("redirect_uri")).toBe(getOAuthRedirectUri());
    expect(await challengeS256(verifier!)).toBe(
      u.searchParams.get("code_challenge"),
    );
  });
});

describe("consumeOAuthParamsFromDocumentUrl", () => {
  beforeEach(() => {
    sessionStorage.clear();
    vi.spyOn(history, "replaceState").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.mocked(history.replaceState).mockRestore();
    restoreWindowLocation();
  });

  it("returns false and does not replaceState when there is no code param", () => {
    installLocationStub({
      origin: "https://consume.test",
      search: "?foo=1",
      href: "https://consume.test/admin/?foo=1",
      assign: vi.fn(),
    });

    expect(consumeOAuthParamsFromDocumentUrl()).toBe(false);
    expect(sessionStorage.getItem(OAUTH_DOC_HANDOFF_KEY)).toBeNull();
    expect(history.replaceState).not.toHaveBeenCalled();
  });

  it("stashes code/state, replaces URL to admin hash route, and returns true", () => {
    installLocationStub({
      origin: "https://consume.test",
      search: "?code=ghcode&state=rawstate",
      href: "https://consume.test/admin/?code=ghcode&state=rawstate",
      assign: vi.fn(),
    });

    expect(consumeOAuthParamsFromDocumentUrl()).toBe(true);
    expect(JSON.parse(sessionStorage.getItem(OAUTH_DOC_HANDOFF_KEY)!)).toEqual(
      { code: "ghcode", state: "rawstate" },
    );
    const expected = new URL("/admin/", "https://consume.test");
    expected.search = "";
    expected.hash = "#/";
    expect(history.replaceState).toHaveBeenCalledOnce();
    expect(history.replaceState).toHaveBeenCalledWith(
      null,
      "",
      expected.href,
    );
  });

  it("treats present-but-empty code as a handoff (key exists in query)", () => {
    installLocationStub({
      origin: "https://consume.test",
      search: "?code=",
      href: "https://consume.test/admin/?code=",
      assign: vi.fn(),
    });

    expect(consumeOAuthParamsFromDocumentUrl()).toBe(true);
    expect(JSON.parse(sessionStorage.getItem(OAUTH_DOC_HANDOFF_KEY)!)).toEqual(
      { code: "", state: "" },
    );
    expect(history.replaceState).toHaveBeenCalledOnce();
  });
});

describe("completeGithubOAuthFromHandoff", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
    vi.mocked(obtainTurnstileToken).mockReset().mockResolvedValue("ts-token");
    vi.mocked(refreshSession).mockReset().mockResolvedValue(undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            access_token: "access-xyz",
            token_type: "Bearer",
            expires_in: 3600,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );
  });

  it("returns error when document handoff is missing", async () => {
    const r = await completeGithubOAuthFromHandoff();
    expect(r).toEqual({
      ok: false,
      error: "Missing OAuth handoff — try signing in again.",
    });
    expect(obtainTurnstileToken).not.toHaveBeenCalled();
  });

  it("rejects non-Worker-expanded state and clears OAuth state", async () => {
    const nonce = "550e8400-e29b-41d4-a716-446655440000";
    sessionStorage.setItem(
      OAUTH_DOC_HANDOFF_KEY,
      JSON.stringify({ code: "gh-code", state: nonce }),
    );
    sessionStorage.setItem(OAUTH_STATE_KEY, nonce);
    sessionStorage.setItem(PKCE_VERIFIER_KEY, "verifier");

    const r = await completeGithubOAuthFromHandoff();

    expect(r.ok).toBe(false);
    expect(r).toMatchObject({ ok: false });
    if (!r.ok) {
      expect(r.error).toContain("Worker-expanded");
    }
    expect(sessionStorage.getItem(OAUTH_STATE_KEY)).toBeNull();
    expect(obtainTurnstileToken).not.toHaveBeenCalled();
  });

  it("rejects Worker-expanded nonce mismatch", async () => {
    const expanded = workerExpandedStateB64("expected-nonce");
    sessionStorage.setItem(
      OAUTH_DOC_HANDOFF_KEY,
      JSON.stringify({ code: "gh-code", state: expanded }),
    );
    sessionStorage.setItem(OAUTH_STATE_KEY, "other-nonce");
    sessionStorage.setItem(PKCE_VERIFIER_KEY, "verifier");

    const r = await completeGithubOAuthFromHandoff();

    expect(r).toEqual({
      ok: false,
      error: "OAuth state mismatch — try signing in again.",
    });
    expect(sessionStorage.getItem(OAUTH_STATE_KEY)).toBeNull();
    expect(obtainTurnstileToken).not.toHaveBeenCalled();
  });

  it("rejects missing PKCE verifier after valid handoff", async () => {
    const nonce = "11111111-1111-1111-1111-111111111111";
    const expanded = workerExpandedStateB64(nonce);
    sessionStorage.setItem(
      OAUTH_DOC_HANDOFF_KEY,
      JSON.stringify({ code: "gh-code", state: expanded }),
    );
    sessionStorage.setItem(OAUTH_STATE_KEY, nonce);

    const r = await completeGithubOAuthFromHandoff();

    expect(r).toEqual({
      ok: false,
      error:
        "Missing PKCE verifier (session expired or this tab did not start sign-in). Open the app from /admin/ and try again.",
    });
    expect(sessionStorage.getItem(OAUTH_STATE_KEY)).toBeNull();
    expect(obtainTurnstileToken).not.toHaveBeenCalled();
  });

  it("persists token and clears OAuth session storage on success", async () => {
    const nonce = "22222222-2222-2222-2222-222222222222";
    const expanded = workerExpandedStateB64(nonce, "site-key-1");
    sessionStorage.setItem(
      OAUTH_DOC_HANDOFF_KEY,
      JSON.stringify({ code: "exchange-code", state: expanded }),
    );
    sessionStorage.setItem(OAUTH_STATE_KEY, nonce);
    sessionStorage.setItem(PKCE_VERIFIER_KEY, "pkce-verifier-value");

    const r = await completeGithubOAuthFromHandoff();

    expect(r).toEqual({ ok: true });
    expect(obtainTurnstileToken).toHaveBeenCalledWith("site-key-1", undefined);
    expect(refreshSession).toHaveBeenCalledOnce();
    expect(loadToken()).toEqual({
      accessToken: "access-xyz",
      tokenType: "Bearer",
      expiresAt: expect.any(Number),
    });
    expect(sessionStorage.getItem(OAUTH_STATE_KEY)).toBeNull();
    expect(sessionStorage.getItem(PKCE_VERIFIER_KEY)).toBeNull();
    expect(sessionStorage.getItem(OAUTH_DOC_HANDOFF_KEY)).toBeNull();

    const fetchMock = vi.mocked(fetch);
    expect(fetchMock).toHaveBeenCalledOnce();
    const [, init] = fetchMock.mock.calls[0]!;
    expect(init?.method).toBe("POST");
    const body = JSON.parse(init?.body as string) as Record<string, unknown>;
    expect(body).toMatchObject({
      code: "exchange-code",
      code_verifier: "pkce-verifier-value",
      turnstile_token: "ts-token",
    });
    expect(typeof body.redirect_uri).toBe("string");
  });

  it("persists null expiresAt when token response omits expires_in", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          access_token: "no-expiry",
          token_type: "Bearer",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    const nonce = "33333333-3333-3333-3333-333333333333";
    const expanded = workerExpandedStateB64(nonce);
    sessionStorage.setItem(
      OAUTH_DOC_HANDOFF_KEY,
      JSON.stringify({ code: "code-no-exp", state: expanded }),
    );
    sessionStorage.setItem(OAUTH_STATE_KEY, nonce);
    sessionStorage.setItem(PKCE_VERIFIER_KEY, "verifier");

    const r = await completeGithubOAuthFromHandoff();

    expect(r).toEqual({ ok: true });
    expect(loadToken()).toEqual({
      accessToken: "no-expiry",
      tokenType: "Bearer",
      expiresAt: null,
    });
  });

  it("treats abort while reading token JSON as cancelled and clears OAuth state", async () => {
    const nonce = "44444444-4444-4444-4444-444444444444";
    const expanded = workerExpandedStateB64(nonce);
    sessionStorage.setItem(
      OAUTH_DOC_HANDOFF_KEY,
      JSON.stringify({ code: "exchange-code", state: expanded }),
    );
    sessionStorage.setItem(OAUTH_STATE_KEY, nonce);
    sessionStorage.setItem(PKCE_VERIFIER_KEY, "pkce-verifier-value");

    const jsonNever = new Promise<object>(() => {
      /* hang until aborted */
    });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: () => jsonNever,
      } as unknown as Response),
    );

    const ac = new AbortController();
    const p = completeGithubOAuthFromHandoff({ signal: ac.signal });
    await Promise.resolve();
    ac.abort();

    const r = await p;
    expect(r).toEqual({
      ok: false,
      error: SIGNING_IN_CANCELLED_MESSAGE,
    });
    expect(loadToken()).toBeNull();
    expect(sessionStorage.getItem(OAUTH_STATE_KEY)).toBeNull();
  });
});
