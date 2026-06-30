import { describe, expect, it } from "vitest";
import type { LayerGithub } from "./githubClient";
import { DISPATCH_TOKEN_SECRET_NAME } from "./constants";
import { analyzeDispatchTokenLayer } from "./dispatch";

function mockGh(orgSecret: Awaited<ReturnType<LayerGithub["orgSecretExists"]>>): LayerGithub {
  return {
    getRepoExists: async () => true,
    getRepoFileUtf8: async () => null,
    repoSecretExists: async () => false,
    repoVariableExists: async () => false,
    orgSecretExists: async () => orgSecret,
  };
}

describe("analyzeDispatchTokenLayer", () => {
  it("installed when org secret exists", async () => {
    const r = await analyzeDispatchTokenLayer("acme", mockGh({ kind: "ok", exists: true }));
    expect(r.status).toBe("installed");
    expect(r.details[0]).toContain(DISPATCH_TOKEN_SECRET_NAME);
  });

  it("not_installed when org secret missing", async () => {
    const r = await analyzeDispatchTokenLayer("acme", mockGh({ kind: "ok", exists: false }));
    expect(r.status).toBe("not_installed");
    expect(r.wouldInstall[0]).toContain("create");
  });

  it("unknown when forbidden", async () => {
    const r = await analyzeDispatchTokenLayer("acme", mockGh({ kind: "forbidden" }));
    expect(r.status).toBe("unknown");
    expect(r.details[0]).toContain("insufficient permissions");
  });
});
