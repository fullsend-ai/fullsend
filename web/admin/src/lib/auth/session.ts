import { derived, writable } from "svelte/store";
import { clearAllAdminSessionCaches } from "./adminSessionCaches";
import { loadToken } from "./tokenStore";
import { fetchGitHubUser, GitHubUserRequestError, type GitHubUser } from "../github/user";

/** Cached GitHub profile from `refreshSession()` (single `/api/github/user` source). */
export const githubUser = writable<GitHubUser | null>(null);

/** GitHub login for signed-in user, or null (derived from `githubUser`). */
export const githubLogin = derived(githubUser, ($u) => $u?.login ?? null);

/** True until initial auth boot (OAuth handoff or `refreshSession`) finishes. */
export const authBootPending = writable(true);

/** Set after 401 so the shell can show Re-authenticate (admin SPA UX spec: global banners). */
export const reauthenticateSuggested = writable(false);

export type SignOutOptions = {
  /** When true, show re-auth banner after clearing session (invalid/expired token). */
  suggestReauth?: boolean;
};

export async function refreshSession(): Promise<void> {
  const t = loadToken();
  if (!t?.accessToken) {
    githubUser.set(null);
    return;
  }
  try {
    const u = await fetchGitHubUser(t.accessToken);
    githubUser.set(u);
    reauthenticateSuggested.set(false);
  } catch (e) {
    if (e instanceof GitHubUserRequestError && e.status === 401) {
      signOut({ suggestReauth: true });
      return;
    }
    githubUser.set(null);
  }
}

export function signOut(options?: SignOutOptions): void {
  // Token, slug, OAuth scope + install-readiness caches, org list memory + analysis
  // (see `adminSessionCaches.ts` — extend there when adding user-scoped admin caches).
  clearAllAdminSessionCaches();
  githubUser.set(null);
  reauthenticateSuggested.set(Boolean(options?.suggestReauth));
}
