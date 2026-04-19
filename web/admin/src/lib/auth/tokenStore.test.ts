import { beforeEach, describe, expect, it } from "vitest";
import { clearSession, loadToken, saveToken } from "./tokenStore";

beforeEach(() => {
  localStorage.clear();
  clearSession();
});

describe("tokenStore", () => {
  it("saveToken and loadToken round-trip with future expiresAt", () => {
    const future = Date.now() + 60_000;
    saveToken({ accessToken: "abc", tokenType: "bearer", expiresAt: future });
    expect(loadToken()).toEqual({
      accessToken: "abc",
      tokenType: "bearer",
      expiresAt: future,
    });
  });

  it("saveToken and loadToken round-trip with null expiresAt", () => {
    saveToken({ accessToken: "abc", tokenType: "bearer", expiresAt: null });
    expect(loadToken()).toEqual({
      accessToken: "abc",
      tokenType: "bearer",
      expiresAt: null,
    });
  });

  it("loadToken clears session and returns null when token is expired", () => {
    saveToken({
      accessToken: "abc",
      tokenType: "bearer",
      expiresAt: Date.now() - 1,
    });
    expect(loadToken()).toBeNull();
    expect(localStorage.getItem("fullsend_admin_github_token")).toBeNull();
  });

  it("loadToken normalizes legacy expiresAt 0 to null", () => {
    localStorage.setItem(
      "fullsend_admin_github_token",
      JSON.stringify({
        accessToken: "legacy",
        tokenType: "bearer",
        expiresAt: 0,
      }),
    );
    expect(loadToken()).toEqual({
      accessToken: "legacy",
      tokenType: "bearer",
      expiresAt: null,
    });
  });

  it("clearSession removes token", () => {
    saveToken({
      accessToken: "x",
      tokenType: "bearer",
      expiresAt: Date.now() + 60_000,
    });
    clearSession();
    expect(loadToken()).toBeNull();
  });

  it("loadToken returns null for invalid JSON", () => {
    localStorage.setItem("fullsend_admin_github_token", "not-json{");
    expect(loadToken()).toBeNull();
  });
});
