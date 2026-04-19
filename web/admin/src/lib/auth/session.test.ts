import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

vi.mock("../github/user", () => ({
  fetchGitHubUser: vi.fn(),
}));

import { fetchGitHubUser } from "../github/user";
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
      expiresAt: 9999999999999,
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

  it("clears githubUser when fetchGitHubUser throws", async () => {
    saveToken({
      accessToken: "bad",
      tokenType: "bearer",
      expiresAt: 9999999999999,
    });
    githubUser.set({ login: "stale", name: null });
    vi.mocked(fetchGitHubUser).mockRejectedValue(new Error("network"));

    await refreshSession();

    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
  });
});

describe("signOut", () => {
  it("clears token and githubUser", () => {
    saveToken({
      accessToken: "x",
      tokenType: "bearer",
      expiresAt: 1,
    });
    githubUser.set({ login: "bob", name: null });

    signOut();

    expect(localStorage.getItem("fullsend_admin_github_token")).toBeNull();
    expect(get(githubUser)).toBeNull();
    expect(get(githubLogin)).toBeNull();
  });
});
