/**
 * When GitHub returns **401** for a request made with the signed-in user’s token, the admin SPA
 * must **not** treat it as an ordinary retryable screen error. The shell clears the session and
 * shows the global **Re-authenticate** banner (see UX spec: global banners).
 *
 * `createUserOctokit` dispatches this event from its request hook. Call
 * {@link notifyGitHubUserUnauthorized} from any other user-token GitHub path that can return 401
 * so behaviour stays consistent.
 */
export const GITHUB_USER_UNAUTHORIZED_EVENT = "fullsend:github-unauthorized" as const;

export function notifyGitHubUserUnauthorized(): void {
  window.dispatchEvent(new CustomEvent(GITHUB_USER_UNAUTHORIZED_EVENT));
}
