import { describe, expect, it } from "vitest";
import type { LayerGithub } from "./githubClient";
import { analyzeWorkflowsLayer } from "./workflows";
import { AGENT_WORKFLOW_PATH, CODEOWNERS_PATH, ONBOARD_WORKFLOW_PATH } from "./constants";

function mockGh(map: Record<string, string | null>): LayerGithub {
  return {
    getRepoExists: async () => true,
    getRepoFileUtf8: async (_o, _r, path) => map[path] ?? null,
    repoSecretExists: async () => false,
    repoVariableExists: async () => false,
    orgSecretExists: async () => ({ kind: "ok", exists: false }),
  };
}

describe("analyzeWorkflowsLayer", () => {
  it("installed when all managed files exist", async () => {
    const stub = "x";
    const r = await analyzeWorkflowsLayer(
      "acme",
      mockGh({
        [AGENT_WORKFLOW_PATH]: stub,
        [ONBOARD_WORKFLOW_PATH]: stub,
        [CODEOWNERS_PATH]: stub,
      }),
    );
    expect(r.status).toBe("installed");
    expect(r.details).toHaveLength(3);
    expect(r.wouldInstall).toEqual([]);
    expect(r.wouldFix).toEqual([]);
  });

  it("not_installed when all missing", async () => {
    const r = await analyzeWorkflowsLayer("acme", mockGh({}));
    expect(r.status).toBe("not_installed");
    expect(r.wouldInstall).toEqual([
      `write ${AGENT_WORKFLOW_PATH}`,
      `write ${ONBOARD_WORKFLOW_PATH}`,
      `write ${CODEOWNERS_PATH}`,
    ]);
  });

  it("degraded when partially present", async () => {
    const r = await analyzeWorkflowsLayer("acme", mockGh({ [AGENT_WORKFLOW_PATH]: "a" }));
    expect(r.status).toBe("degraded");
    expect(r.details).toEqual([`${AGENT_WORKFLOW_PATH} exists`]);
    expect(r.wouldFix).toEqual([`write ${ONBOARD_WORKFLOW_PATH}`, `write ${CODEOWNERS_PATH}`]);
  });
});
