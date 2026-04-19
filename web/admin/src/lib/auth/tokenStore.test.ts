import { beforeEach, describe, expect, it } from "vitest";
import { clearSession, loadToken, saveToken } from "./tokenStore";

beforeEach(() => {
  localStorage.clear();
  clearSession();
});

describe("tokenStore", () => {
  it("saveToken and loadToken round-trip", () => {
    saveToken({ accessToken: "abc", tokenType: "bearer", expiresAt: 123 });
    expect(loadToken()).toEqual({
      accessToken: "abc",
      tokenType: "bearer",
      expiresAt: 123,
    });
  });

  it("clearSession removes token", () => {
    saveToken({ accessToken: "x", tokenType: "bearer", expiresAt: 1 });
    clearSession();
    expect(loadToken()).toBeNull();
  });

  it("loadToken returns null for invalid JSON", () => {
    localStorage.setItem("fullsend_admin_github_token", "not-json{");
    expect(loadToken()).toBeNull();
  });
});
