export type GitHubUser = {
  login: string;
  name: string | null;
};

export class GitHubUserRequestError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "GitHubUserRequestError";
    this.status = status;
  }
}

/** Same-origin BFF (Vite → Wrangler) — GitHub REST does not allow browser CORS for /user. */
export async function fetchGitHubUser(
  accessToken: string,
): Promise<GitHubUser> {
  const res = await fetch("/api/github/user", {
    headers: {
      Accept: "application/vnd.github+json",
      Authorization: `Bearer ${accessToken}`,
      "X-GitHub-Api-Version": "2022-11-28",
    },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new GitHubUserRequestError(
      res.status,
      `GitHub /user failed: ${res.status} ${text.slice(0, 200)}`,
    );
  }
  const data = (await res.json()) as Record<string, unknown>;
  const login = typeof data.login === "string" ? data.login : "";
  if (!login) {
    throw new Error("GitHub /user: missing login");
  }
  const name = typeof data.name === "string" ? data.name : null;
  return { login, name };
}
