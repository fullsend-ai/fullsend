import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

vi.mock("../github/user", async (importOriginal) => {
  const mod = await importOriginal<typeof import("../github/user")>();
  return { ...mod, fetchGitHubUser: vi.fn() };
});

import { fetchGitHubUser, GitHubUserRequestError } from "../github/user";
import { githubLogin, githubUser, refreshSession, signOut } from "./session";
import { saveToken } from "./tokenStore";

beforeEach(() => {
  localStorage.clear();
  vi.mocked(fetchGitHubUser).mockReset();
  githubUser.set(null);
});

describe("refreshSession", () => {
  it("clears githubUser when there is no stored token", async () => {
    githubUser.set({ login: "ghost", name: null });
    await refreshSession();
    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
    expect(fetchGitHubUser).not.toHaveBeenCalled();
  });

  it("sets githubUser from fetchGitHubUser when token exists", async () => {
    saveToken({
      accessToken: "tok",
      tokenType: "bearer",
      expiresAt: Date.now() + 60_000,
    });
    vi.mocked(fetchGitHubUser).mockResolvedValue({
      login: "alice",
      name: "Alice L",
    });

    await refreshSession();

    expect(fetchGitHubUser).toHaveBeenCalledWith("tok");
    expect(get(githubUser)).toEqual({
      login: "alice",
      name: "Alice L",
    });
    expect(get(githubLogin)).toBe("alice");
  });

  it("clears githubUser but keeps stored token when fetchGitHubUser throws generically", async () => {
    saveToken({
      accessToken: "bad",
      tokenType: "bearer",
      expiresAt: Date.now() + 60_000,
    });
    githubUser.set({ login: "stale", name: null });
    vi.mocked(fetchGitHubUser).mockRejectedValue(new Error("network"));

    await refreshSession();

    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
    expect(localStorage.getItem("fullsend_admin_github_token")).not.toBeNull();
  });

  it("clears stored token and githubUser when fetchGitHubUser rejects with 401", async () => {
    saveToken({
      accessToken: "revoked",
      tokenType: "bearer",
      expiresAt: Date.now() + 60_000,
    });
    githubUser.set({ login: "stale", name: null });
    vi.mocked(fetchGitHubUser).mockRejectedValue(
      new GitHubUserRequestError(401, "GitHub /user failed: 401 "),
    );

    await refreshSession();

    expect(localStorage.getItem("fullsend_admin_github_token")).toBeNull();
    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
  });

  it("does not call fetchGitHubUser when stored token is already expired", async () => {
    saveToken({
      accessToken: "tok",
      tokenType: "bearer",
      expiresAt: Date.now() - 1,
    });
    githubUser.set({ login: "stale", name: null });

    await refreshSession();

    expect(fetchGitHubUser).not.toHaveBeenCalled();
    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
    expect(localStorage.getItem("fullsend_admin_github_token")).toBeNull();
  });
});

describe("signOut", () => {
  it("clears token and githubUser", () => {
    saveToken({
      accessToken: "x",
      tokenType: "bearer",
      expiresAt: Date.now() + 60_000,
    });
    githubUser.set({ login: "bob", name: null });

    signOut();

    expect(localStorage.getItem("fullsend_admin_github_token")).toBeNull();
    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
  });
});
