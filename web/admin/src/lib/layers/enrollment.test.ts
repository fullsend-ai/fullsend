import { describe, expect, it } from "vitest";
import type { LayerGithub } from "./githubClient";
import { SHIM_WORKFLOW_PATH } from "./constants";
import { analyzeEnrollmentLayer } from "./enrollment";

function mockGh(shimByRepo: Record<string, string | null>): LayerGithub {
  return {
    getRepoExists: async () => true,
    getRepoFileUtf8: async (_org, repo, path) => {
      if (path !== SHIM_WORKFLOW_PATH) return null;
      return shimByRepo[repo] ?? null;
    },
    repoSecretExists: async () => false,
    repoVariableExists: async () => false,
    orgSecretExists: async () => ({ kind: "ok", exists: false }),
  };
}

describe("analyzeEnrollmentLayer", () => {
  it("installed when no enabled repos", async () => {
    const r = await analyzeEnrollmentLayer("acme", mockGh({}), []);
    expect(r.status).toBe("installed");
    expect(r.details).toEqual(["no repositories enrolled"]);
  });

  it("installed when all enabled repos have shim", async () => {
    const r = await analyzeEnrollmentLayer("acme", mockGh({ a: "yaml", b: "yaml" }), ["a", "b"]);
    expect(r.status).toBe("installed");
    expect(r.details).toEqual(["a enrolled", "b enrolled"]);
  });

  it("not_installed when none enrolled", async () => {
    const r = await analyzeEnrollmentLayer("acme", mockGh({}), ["x", "y"]);
    expect(r.status).toBe("not_installed");
    expect(r.wouldInstall).toEqual(["create enrollment PR for x", "create enrollment PR for y"]);
  });

  it("degraded when mixed", async () => {
    const r = await analyzeEnrollmentLayer("acme", mockGh({ a: "ok" }), ["a", "b"]);
    expect(r.status).toBe("degraded");
    expect(r.details).toContain("a enrolled");
    expect(r.wouldFix).toContain("create enrollment PR for b");
  });
});
