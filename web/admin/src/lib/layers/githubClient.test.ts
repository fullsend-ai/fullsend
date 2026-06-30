import { describe, expect, it, vi } from "vitest";
import type { Octokit } from "@octokit/rest";
import { createLayerGithub } from "./githubClient";

describe("createLayerGithub getRepoFileUtf8", () => {
  it("wraps invalid base64 from the Contents API in a normal Error", async () => {
    const octokit = {
      repos: {
        getContent: vi.fn().mockResolvedValue({
          data: {
            type: "file",
            content: "not-valid-base64!!!",
          },
        }),
      },
    } as unknown as Octokit;

    const gh = createLayerGithub(octokit);
    await expect(gh.getRepoFileUtf8("o", "r", "p")).rejects.toThrow(/not valid base64/i);
  });
});
