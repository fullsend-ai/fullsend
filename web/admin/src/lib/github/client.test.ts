import { afterEach, describe, expect, it, vi } from "vitest";
import { createUserOctokit } from "./client";

describe("createUserOctokit", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("dispatches fullsend:github-unauthorized when GitHub returns 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ message: "Bad credentials" }), {
            status: 401,
            headers: { "content-type": "application/json" },
          }),
        ),
      ),
    );
    const listener = vi.fn();
    window.addEventListener("fullsend:github-unauthorized", listener);
    const o = createUserOctokit("tok");
    await expect(o.request("GET /user")).rejects.toThrow();
    expect(listener).toHaveBeenCalledOnce();
    window.removeEventListener("fullsend:github-unauthorized", listener);
  });
});
