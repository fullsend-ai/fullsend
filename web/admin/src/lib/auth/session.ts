import { derived, writable } from "svelte/store";
import { clearSession, loadToken } from "./tokenStore";
import {
  fetchGitHubUser,
  GitHubUserRequestError,
  type GitHubUser,
} from "../github/user";

/** Cached GitHub profile from `refreshSession()` (single `/api/github/user` source). */
export const githubUser = writable<GitHubUser | null>(null);

/** GitHub login for signed-in user, or null (derived from `githubUser`). */
export const githubLogin = derived(githubUser, ($u) => $u?.login ?? null);

/** True until initial auth boot (OAuth handoff or `refreshSession`) finishes. */
export const authBootPending = writable(true);

export async function refreshSession(): Promise<void> {
  const t = loadToken();
  if (!t?.accessToken) {
    githubUser.set(null);
    return;
  }
  try {
    const u = await fetchGitHubUser(t.accessToken);
    githubUser.set(u);
  } catch (e) {
    if (e instanceof GitHubUserRequestError && e.status === 401) {
      signOut();
      return;
    }
    githubUser.set(null);
  }
}

export function signOut(): void {
  clearSession();
  githubUser.set(null);
}
