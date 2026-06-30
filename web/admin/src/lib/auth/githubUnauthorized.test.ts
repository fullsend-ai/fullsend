import { describe, expect, it, vi } from "vitest";
import { GITHUB_USER_UNAUTHORIZED_EVENT, notifyGitHubUserUnauthorized } from "./githubUnauthorized";

describe("notifyGitHubUserUnauthorized", () => {
  it("dispatches the shared event name", () => {
    const listener = vi.fn();
    window.addEventListener(GITHUB_USER_UNAUTHORIZED_EVENT, listener);
    notifyGitHubUserUnauthorized();
    expect(listener).toHaveBeenCalledOnce();
    window.removeEventListener(GITHUB_USER_UNAUTHORIZED_EVENT, listener);
  });
});
