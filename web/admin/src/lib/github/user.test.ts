import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchGitHubUser, GitHubUserRequestError } from "./user";

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn());
});

describe("fetchGitHubUser", () => {
  it("returns login and name when response is ok", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ login: "u", name: "User Name" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(fetchGitHubUser("secret-token")).resolves.toEqual({
      login: "u",
      name: "User Name",
    });
    const [url, init] = vi.mocked(fetch).mock.calls[0]!;
    expect(url).toBe("/api/github/user");
    expect(init?.headers).toMatchObject({
      Authorization: "Bearer secret-token",
    });
  });

  it("uses null name when response omits name", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ login: "onlylogin" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(fetchGitHubUser("t")).resolves.toEqual({
      login: "onlylogin",
      name: null,
    });
  });

  it("accepts Worker-narrowed /user JSON including avatar_url (field reserved for follow-on UI)", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          login: "u",
          name: null,
          avatar_url: "https://avatars.githubusercontent.com/u/1?v=4",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(fetchGitHubUser("t")).resolves.toEqual({
      login: "u",
      name: null,
    });
  });

  it("throws GitHubUserRequestError when response is not ok", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response("forbidden body", { status: 403 }),
    );

    const err = await fetchGitHubUser("t").catch((e) => e);
    expect(err).toBeInstanceOf(GitHubUserRequestError);
    expect((err as GitHubUserRequestError).status).toBe(403);
    expect((err as GitHubUserRequestError).message).toMatch(/403/);
  });

  it("throws when login is missing", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ id: 1 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(fetchGitHubUser("t")).rejects.toThrow(/missing login/);
  });
});
