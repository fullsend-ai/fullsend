import { Octokit } from "@octokit/rest";
import type { OctokitResponse } from "@octokit/types";
import { notifyGitHubUserUnauthorized } from "../auth/githubUnauthorized";

/**
 * GitHub REST client for **user** tokens. Some routes (e.g. org memberships) work from the
 * browser against `api.github.com`; `/user` still uses the same-origin Worker in [`user.ts`](./user.ts).
 *
 * A `request` hook dispatches {@link notifyGitHubUserUnauthorized} on HTTP **401** so `App.svelte` can sign out.
 *
 * Note: `new Octokit({ request: { hook } })` is ignored — `@octokit/core` replaces `request.hook`
 * with its own collection. We register via `octokit.hook.wrap("request", …)` instead.
 */
export function createUserOctokit(accessToken: string): Octokit {
  const octokit = new Octokit({ auth: accessToken });
  octokit.hook.wrap("request", async (request, options) => {
    try {
      const response = await request(options as never);
      // Octokit throws on 401 before returning; the branch below handles that.
      return response as OctokitResponse<unknown>;
    } catch (e: unknown) {
      const rec = e && typeof e === "object" ? (e as Record<string, unknown>) : null;
      const status =
        typeof rec?.status === "number"
          ? rec.status
          : rec?.response &&
              typeof rec.response === "object" &&
              typeof (rec.response as { status?: unknown }).status === "number"
            ? (rec.response as { status: number }).status
            : undefined;
      if (status === 401) {
        notifyGitHubUserUnauthorized();
      }
      throw e;
    }
  });
  return octokit;
}
