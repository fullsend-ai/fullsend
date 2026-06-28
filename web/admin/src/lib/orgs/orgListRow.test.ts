import { describe, expect, it } from "vitest";
import { computePreflight } from "../layers/preflight";
import type { LayerReport } from "../status/types";
import { deployRequiredOAuthScopes } from "./deployOAuthScopes";
import {
  buildDeployPreflight,
  orgListRowFromAnalysis,
  type GitHubAppInstallReadiness,
  type OrgListAnalysisErr,
  type OrgListAnalysisOk,
} from "./orgListRow";

function preflightAllGranted() {
  return computePreflight([...deployRequiredOAuthScopes()], ["repo", "workflow", "admin:org"]);
}

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

describe("orgListRowFromAnalysis", () => {
  it("cannot_deploy on forbidden error", () => {
    const err: OrgListAnalysisErr = {
      kind: "error",
      message: "no access",
      forbidden: true,
    };
    const row = orgListRowFromAnalysis(err, preflightAllGranted());
    expect(row.kind).toBe("cannot_deploy");
    if (row.kind === "cannot_deploy") {
      expect(row.reason).toContain("cannot reach everything");
      expect(row.missingInstallRequirements?.length).toBeGreaterThanOrEqual(3);
      expect(row.helpBullets?.length).toBeGreaterThanOrEqual(2);
    }
  });

  it("cannot_deploy on forbidden includes analysis lines and GitHub API message", () => {
    const err: OrgListAnalysisErr = {
      kind: "error",
      message: "Insufficient permissions",
      forbidden: true,
      missingPermissionLines: [
        "GitHub reports this API call would be allowed with these OAuth scopes: repo.",
      ],
      githubApiMessage: "Resource not accessible by integration",
    };
    const row = orgListRowFromAnalysis(err, preflightAllGranted());
    expect(row.kind).toBe("cannot_deploy");
    if (row.kind === "cannot_deploy") {
      expect(row.missingInstallRequirements?.[0]).toContain("OAuth scopes");
      expect(row.missingInstallRequirements).toContain("Resource not accessible by integration");
    }
  });

  it("error on non-forbidden failure", () => {
    const err: OrgListAnalysisErr = {
      kind: "error",
      message: "network",
      forbidden: false,
    };
    expect(orgListRowFromAnalysis(err, preflightAllGranted())).toEqual({
      kind: "error",
      message: "network",
    });
  });

  it("deploy when config repo not installed and OAuth preflight ok", () => {
    expect(orgListRowFromAnalysis(notInstalledOk, preflightAllGranted())).toEqual({
      kind: "deploy",
    });
  });

  it("error when preflight skipped and GitHub App readiness not supplied", () => {
    const pf = buildDeployPreflight(null);
    expect(pf.skipped).toBe(true);
    const row = orgListRowFromAnalysis(notInstalledOk, pf);
    expect(row.kind).toBe("error");
    if (row.kind === "error") {
      expect(row.message).toContain("Try Refresh");
    }
  });

  it("deploy when preflight skipped and GitHub App probes pass", () => {
    const pf = buildDeployPreflight(null);
    const ready: GitHubAppInstallReadiness = { ok: true, missing: [] };
    expect(orgListRowFromAnalysis(notInstalledOk, pf, ready)).toEqual({ kind: "deploy" });
  });

  it("cannot_deploy when preflight skipped and GitHub App probes fail", () => {
    const pf = buildDeployPreflight(null);
    const ready: GitHubAppInstallReadiness = {
      ok: false,
      missing: ["Organisation-level GitHub Actions secrets"],
    };
    const row = orgListRowFromAnalysis(notInstalledOk, pf, ready);
    expect(row.kind).toBe("cannot_deploy");
    if (row.kind === "cannot_deploy") {
      expect(row.missingInstallRequirements).toEqual(["Organisation-level GitHub Actions secrets"]);
      expect(row.helpBullets?.length).toBeGreaterThan(0);
    }
  });

  it("cannot_deploy when config not installed and classic OAuth scopes missing", () => {
    const pf = computePreflight([...deployRequiredOAuthScopes()], ["repo"]);
    const row = orgListRowFromAnalysis(notInstalledOk, pf);
    expect(row.kind).toBe("cannot_deploy");
    if (row.kind === "cannot_deploy") {
      expect(row.missingInstallRequirements?.some((s) => s.includes("GitHub Actions"))).toBe(true);
      expect(row.helpBullets?.length).toBeGreaterThanOrEqual(2);
    }
  });

  it("configure when config repo exists (installed)", () => {
    const ok: OrgListAnalysisOk = {
      kind: "ok",
      rollup: "degraded",
      reports: [
        rep("config-repo", "installed"),
        rep("workflows", "degraded"),
        rep("secrets", "not_installed"),
        rep("enrollment", "installed"),
        rep("dispatch-token", "not_installed"),
      ],
    };
    expect(orgListRowFromAnalysis(ok, preflightAllGranted())).toEqual({
      kind: "configure",
    });
  });

  it("configure when config repo degraded", () => {
    const ok: OrgListAnalysisOk = {
      kind: "ok",
      rollup: "degraded",
      reports: [
        rep("config-repo", "degraded"),
        rep("workflows", "not_installed"),
        rep("secrets", "not_installed"),
        rep("enrollment", "installed"),
        rep("dispatch-token", "not_installed"),
      ],
    };
    expect(orgListRowFromAnalysis(ok, preflightAllGranted())).toEqual({
      kind: "configure",
    });
  });

  it("configure when scopes missing but config already present (no deploy gate)", () => {
    const ok: OrgListAnalysisOk = {
      kind: "ok",
      rollup: "degraded",
      reports: [
        rep("config-repo", "installed"),
        rep("workflows", "not_installed"),
        rep("secrets", "not_installed"),
        rep("enrollment", "installed"),
        rep("dispatch-token", "not_installed"),
      ],
    };
    const pf = computePreflight([...deployRequiredOAuthScopes()], ["repo"]);
    expect(orgListRowFromAnalysis(ok, pf)).toEqual({ kind: "configure" });
  });

  it("configure when preflight skipped but config installed (ignores readiness)", () => {
    const ok: OrgListAnalysisOk = {
      kind: "ok",
      rollup: "degraded",
      reports: [
        rep("config-repo", "installed"),
        rep("workflows", "not_installed"),
        rep("secrets", "not_installed"),
        rep("enrollment", "installed"),
        rep("dispatch-token", "not_installed"),
      ],
    };
    const pf = buildDeployPreflight(null);
    expect(orgListRowFromAnalysis(ok, pf, { ok: false, missing: ["should be ignored"] })).toEqual({
      kind: "configure",
    });
  });
});
