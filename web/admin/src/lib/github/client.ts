import { Octokit } from "@octokit/rest";
import type {
  OctokitResponse,
  RequestInterface,
  RequestParameters,
} from "@octokit/types";

/**
 * GitHub REST client for **user** tokens. Browser calls to `api.github.com` are blocked by
 * CORS for most routes; prefer same-origin [`fetchGitHubUser`](./user.ts) until a Worker
 * proxy exists for each endpoint you need.
 */
export function createUserOctokit(accessToken: string): Octokit {
  return new Octokit({
    auth: accessToken,
    request: {
      hook: async (
        request: RequestInterface,
        options: RequestParameters,
      ): Promise<OctokitResponse<unknown>> => {
        // Octokit supplies resolved endpoint options; `RequestInterface` overloads are stricter.
        const response = await request(
          options as RequestParameters & { url: string },
        );
        if (response.status === 401) {
          window.dispatchEvent(new CustomEvent("fullsend:github-unauthorized"));
        }
        return response;
      },
    },
  });
}
