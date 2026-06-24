import type { Octokit } from "@octokit/rest";
import { RequestError } from "@octokit/request-error";
import { analyzeOrgLayers } from "../layers/analyzeOrg";
import {
  forbidden403HintsFromRequestError,
  isLikelyGitHubRateLimit403,
  userGitHubRestRateLimitShortMessage,
} from "./githubPermissionHints";
import { CONFIG_FILE_PATH, CONFIG_REPO_NAME } from "../layers/constants";
import { createLayerGithub } from "../layers/githubClient";
import {
  agentsFromConfig,
  enabledReposFromConfig,
  OrgConfigYamlLimitError,
  parseOrgConfigYaml,
  validateOrgConfig,
} from "../layers/orgConfigParse";
import { computePreflight, type PreflightResult } from "../layers/preflight";
import type { LayerReport, LayerStatus } from "../status/types";
import { deployRequiredOAuthScopes } from "./deployOAuthScopes";

/** Result of GitHub App install probes when classic `X-OAuth-Scopes` is absent. */
export type GitHubAppInstallReadiness = {
  ok: boolean;
  missing: string[];
};

export type OrgListAnalysisOk = {
  kind: "ok";
  rollup: LayerStatus;
  reports: LayerReport[];
};

export type OrgListAnalysisErr = {
  kind: "error";
  message: string;
  /** True when GitHub returned 403 (token cannot read this org’s installation state). */
  forbidden: boolean;
  /**
   * Actionable bullets for org owners / the signed-in user (browser-visible OAuth scope hints,
   * then defaults — never relies on `X-Accepted-GitHub-Permissions`, which CORS hides from JS).
   */
  missingPermissionLines?: string[];
  /** GitHub JSON `message` on the failing response, when available. */
  githubApiMessage?: string;
};

/**
 * Runs the same read-only layer stack as the org dashboard **analyze** path
 * (`analyzeOrgLayers`) so permission failures on workflows, Actions, enrollment, or
 * org secrets surface as actionable permission errors — not only the config repo.
 *
 * Deploy vs Configure uses **config-repo** plus install readiness: classic tokens compare
 * `X-OAuth-Scopes` to {@link deployRequiredOAuthScopes}; GitHub App user tokens use
 * read-only API probes (see `installReadinessProbes.ts`) instead of assuming OAuth scopes exist.
 */
export type AnalyzeOrgForOrgListOptions = {
  /**
   * When not `null`/`undefined`, skips REST `GET /repos/{org}/.fullsend` and uses this
   * as the config-repo existence hint (from GraphQL batching on the org list).
   */
  fullsendRepoExistsHint?: boolean | null;
};

export async function analyzeOrgForOrgList(
  org: string,
  octokit: Octokit,
  options?: AnalyzeOrgForOrgListOptions,
): Promise<OrgListAnalysisOk | OrgListAnalysisErr> {
  const gh = createLayerGithub(octokit);
  try {
    let exists: boolean;
    const hint = options?.fullsendRepoExistsHint;
    if (hint === true) {
      exists = true;
    } else if (hint === false) {
      exists = false;
    } else {
      exists = await gh.getRepoExists(org, CONFIG_REPO_NAME);
    }
    let agents: { role: string }[] = [];
    let enabledRepos: string[] = [];
    if (exists) {
      const raw = await gh.getRepoFileUtf8(org, CONFIG_REPO_NAME, CONFIG_FILE_PATH);
      if (raw) {
        try {
          const cfg = parseOrgConfigYaml(raw);
          if (validateOrgConfig(cfg) === null) {
            agents = agentsFromConfig(cfg);
            enabledRepos = enabledReposFromConfig(cfg);
          }
        } catch (e) {
          if (e instanceof OrgConfigYamlLimitError) {
            return {
              kind: "error",
              message: e.message,
              forbidden: false,
            };
          }
          /* invalid YAML — still analyze other layers with empty agents/repos */
        }
      }
    }
    const { reports, rollup } = await analyzeOrgLayers({
      org,
      gh,
      agents,
      enabledRepos,
    });
    return { kind: "ok", reports, rollup };
  } catch (e) {
    if (e instanceof RequestError && e.status === 403) {
      if (isLikelyGitHubRateLimit403(e)) {
        return {
          kind: "error",
          message: userGitHubRestRateLimitShortMessage(e),
          forbidden: false,
        };
      }
      const hints = forbidden403HintsFromRequestError(e);
      const lines =
        hints.missingPermissionLines.length > 0
          ? [...hints.missingPermissionLines]
          : [...DEFAULT_FORBIDDEN_ACTION_LINES];
      return {
        kind: "error",
        message: "Insufficient permissions to evaluate Fullsend state for this organisation.",
        forbidden: true,
        missingPermissionLines: lines,
        githubApiMessage: hints.githubApiMessage,
      };
    }
    return {
      kind: "error",
      message: e instanceof Error ? e.message : String(e),
      forbidden: false,
    };
  }
}

export type OrgListRowCluster =
  | { kind: "checking" }
  | { kind: "configure" }
  | { kind: "deploy" }
  | {
      kind: "cannot_deploy";
      reason: string;
      /** Plain-language access gaps to request from an organisation owner when needed. */
      missingInstallRequirements?: string[];
      helpBullets?: string[];
    }
  | { kind: "error"; message: string };

const ORG_OWNER_HELP = [
  "If you are not an organisation owner, ask an owner to approve the Fullsend Admin application for this organisation and the access it needs. Organisations that use SAML may require an owner to authorize the app for your account afterward.",
  "If you are an owner, use your organisation’s settings on GitHub to approve the app and the permissions it requests.",
] as const;

/** When GitHub returns 403 without browser-visible OAuth scope hints on the response. */
const DEFAULT_FORBIDDEN_ACTION_LINES = [
  "An organisation owner may need to approve or install the Fullsend Admin GitHub App and accept the permissions it requests.",
  "If the organisation uses SAML single sign-on, an owner may need to authorize the app for your GitHub account after you sign in.",
  "Organisation policies may need to allow this app access to the org and its repositories (including the `.fullsend` configuration repository).",
] as const;

function userFacingPermissionForClassicScope(scope: string): string {
  switch (scope.trim()) {
    case "repo":
      return "Repositories in this organisation (read and manage contents)";
    case "workflow":
      return "GitHub Actions on organisation repositories";
    case "admin:org":
      return "Organisation-level GitHub Actions settings and secrets";
    default:
      return `Access related to: ${scope}`;
  }
}

function cannotDeployClusterForMissingClassicScopes(
  deployPreflight: PreflightResult,
): OrgListRowCluster {
  return {
    kind: "cannot_deploy",
    reason:
      "Your GitHub account does not have everything it needs to deploy Fullsend in this organisation yet.",
    missingInstallRequirements: deployPreflight.missing.map(userFacingPermissionForClassicScope),
    helpBullets: [...ORG_OWNER_HELP],
  };
}

/**
 * Maps layer analysis to the org list trailing cluster.
 * - **403 / rate limits / network** from `analyzeOrgForOrgList` → `cannot_deploy` or `error`.
 * - **Deploy vs Configure** (when analysis succeeds): **config-repo** status plus install
 *   readiness — classic `X-OAuth-Scopes` when present, else {@link GitHubAppInstallReadiness}
 *   from read-only API probes (`installReadinessProbes.ts` async wrapper supplies this).
 */
export function orgListRowFromAnalysis(
  result: OrgListAnalysisOk | OrgListAnalysisErr,
  deployPreflight: PreflightResult,
  githubAppReadiness?: GitHubAppInstallReadiness | null,
): OrgListRowCluster {
  if (result.kind === "error") {
    if (result.forbidden) {
      let req: string[] = [...(result.missingPermissionLines ?? [])];
      const api = result.githubApiMessage?.trim();
      if (api && !req.some((line) => line.includes(api))) {
        req.push(api);
      }
      if (req.length === 0) {
        req = [...DEFAULT_FORBIDDEN_ACTION_LINES];
      }
      return {
        kind: "cannot_deploy",
        reason:
          "Your account cannot reach everything in this organisation that Fullsend needs to deploy or manage here.",
        missingInstallRequirements: req,
        helpBullets: [...ORG_OWNER_HELP],
      };
    }
    return { kind: "error", message: result.message };
  }

  const configReport = result.reports.find((r: LayerReport) => r.name === "config-repo");
  if (!configReport) {
    return {
      kind: "error",
      message:
        "Could not determine configuration status for this organisation. Try Refresh, or ask an organisation owner to check app access.",
    };
  }

  if (configReport.status === "not_installed") {
    if (!deployPreflight.skipped) {
      if (deployPreflight.missing.length > 0) {
        return cannotDeployClusterForMissingClassicScopes(deployPreflight);
      }
      return { kind: "deploy" };
    }
    if (githubAppReadiness == null) {
      return {
        kind: "error",
        message: "Could not confirm deploy access for this organisation yet. Try Refresh.",
      };
    }
    if (!githubAppReadiness.ok) {
      return {
        kind: "cannot_deploy",
        reason:
          "Your GitHub account does not have everything it needs to deploy Fullsend in this organisation yet.",
        missingInstallRequirements: [...githubAppReadiness.missing],
        helpBullets: [...ORG_OWNER_HELP],
      };
    }
    return { kind: "deploy" };
  }

  return { kind: "configure" };
}

export function buildDeployPreflight(granted: string[] | null): PreflightResult {
  return computePreflight([...deployRequiredOAuthScopes()], granted);
}
