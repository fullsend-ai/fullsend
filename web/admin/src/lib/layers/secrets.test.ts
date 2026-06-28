import { describe, expect, it } from "vitest";
import type { LayerGithub } from "./githubClient";
import { analyzeSecretsLayer, secretNameForRole, variableNameForRole } from "./secrets";

function mockGh(opts: { secrets?: Set<string>; variables?: Set<string> }): LayerGithub {
  const secrets = opts.secrets ?? new Set();
  const variables = opts.variables ?? new Set();
  return {
    getRepoExists: async () => true,
    getRepoFileUtf8: async () => null,
    repoSecretExists: async (_o, _r, name) => secrets.has(name),
    repoVariableExists: async (_o, _r, name) => variables.has(name),
    orgSecretExists: async () => ({ kind: "ok", exists: false }),
  };
}

describe("analyzeSecretsLayer", () => {
  it("naming matches Go helpers", () => {
    expect(secretNameForRole("coder")).toBe("FULLSEND_CODER_APP_PRIVATE_KEY");
    expect(variableNameForRole("coder")).toBe("FULLSEND_CODER_APP_ID");
  });

  it("installed when all secrets and variables exist", async () => {
    const r = await analyzeSecretsLayer(
      "acme",
      mockGh({
        secrets: new Set(["FULLSEND_TRIAGE_APP_PRIVATE_KEY"]),
        variables: new Set(["FULLSEND_TRIAGE_APP_ID"]),
      }),
      [{ role: "triage" }],
    );
    expect(r.status).toBe("installed");
    expect(r.details).toContain("FULLSEND_TRIAGE_APP_PRIVATE_KEY exists");
    expect(r.details).toContain("FULLSEND_TRIAGE_APP_ID exists");
  });

  it("not_installed when nothing present", async () => {
    const r = await analyzeSecretsLayer("acme", mockGh({}), [{ role: "fullsend" }]);
    expect(r.status).toBe("not_installed");
    expect(r.wouldInstall).toEqual([
      "create FULLSEND_FULLSEND_APP_PRIVATE_KEY",
      "create FULLSEND_FULLSEND_APP_ID",
    ]);
  });

  it("degraded when partially present", async () => {
    const r = await analyzeSecretsLayer(
      "acme",
      mockGh({
        secrets: new Set(["FULLSEND_FULLSEND_APP_PRIVATE_KEY"]),
        variables: new Set(),
      }),
      [{ role: "fullsend" }],
    );
    expect(r.status).toBe("degraded");
    expect(r.wouldFix).toContain("create missing FULLSEND_FULLSEND_APP_ID");
  });

  it("installed with no agents configured", async () => {
    const r = await analyzeSecretsLayer("acme", mockGh({}), []);
    expect(r.status).toBe("installed");
    expect(r.details).toEqual([]);
  });
});
