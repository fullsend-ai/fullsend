import type { Octokit } from "@octokit/rest";
import { RequestError } from "@octokit/request-error";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { computePreflight } from "../layers/preflight";
import type { LayerReport } from "../status/types";
import { deployRequiredOAuthScopes } from "./deployOAuthScopes";
import {
  clearInstallReadinessProbeCache,
  probeGitHubAppInstallReadiness,
  resolveOrgListDeployRowCluster,
} from "./installReadinessProbes";
import type { OrgListAnalysisOk } from "./orgListRow";

function rep(name: string, status: LayerReport["status"]): LayerReport {
  return {
    name,
    status,
    details: [],
    wouldInstall: [],
    wouldFix: [],
  };
}

const notInstalledOk: OrgListAnalysisOk = {
  kind: "ok",
  rollup: "not_installed",
  reports: [
    rep("config-repo", "not_installed"),
    rep("workflows", "not_installed"),
    rep("secrets", "not_installed"),
    rep("enrollment", "installed"),
    rep("dispatch-token", "not_installed"),
  ],
};

describe("probeGitHubAppInstallReadiness", () => {
  beforeEach(() => {
    clearInstallReadinessProbeCache();
  });

  it("returns ok when listForOrg, workflows, and org secrets public-key succeed", async () => {
    const octokit = {
      rest: {
        repos: {
          listForOrg: vi.fn().mockResolvedValue({ data: [{ name: "r1" }] }),
        },
        actions: {
          listRepoWorkflows: vi.fn().mockResolvedValue({ data: [] }),
        },
      },
      request: vi.fn().mockResolvedValue({ data: { key: "k", key_id: "kid" } }),
    } as unknown as Octokit;
    await expect(probeGitHubAppInstallReadiness(octokit, "acme", {})).resolves.toEqual({
      ok: true,
      missing: [],
    });
  });

  it("returns not ok when listForOrg is 403", async () => {
    const octokit = {
      rest: {
        repos: {
          listForOrg: vi.fn().mockRejectedValue(
            new RequestError("Forbidden", 403, {
              request: {
                method: "GET",
                headers: {},
                url: "https://api.github.com/orgs/acme/repos",
              },
            }),
          ),
        },
      },
    } as unknown as Octokit;
    const r = await probeGitHubAppInstallReadiness(octokit, "acme", {});
    expect(r.ok).toBe(false);
    expect(r.missing.length).toBeGreaterThan(0);
  });
});

describe("resolveOrgListDeployRowCluster", () => {
  beforeEach(() => {
    clearInstallReadinessProbeCache();
  });

  it("runs probes when preflight skipped and config not installed", async () => {
    const granted = computePreflight([...deployRequiredOAuthScopes()], null);
    expect(granted.skipped).toBe(true);

    const octokit = {
      rest: {
        repos: {
          listForOrg: vi.fn().mockResolvedValue({ data: [{ name: "r1" }] }),
        },
        actions: {
          listRepoWorkflows: vi.fn().mockResolvedValue({ data: [] }),
        },
      },
      request: vi.fn().mockResolvedValue({ data: { key: "k", key_id: "kid" } }),
    } as unknown as Octokit;

    const row = await resolveOrgListDeployRowCluster(
      notInstalledOk,
      granted,
      octokit,
      "alice",
      "acme",
    );
    expect(row).toEqual({ kind: "deploy" });
  });
});
