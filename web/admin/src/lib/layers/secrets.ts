import type { LayerReport } from "../status/types";
import { CONFIG_REPO_NAME } from "./constants";
import type { LayerGithub } from "./githubClient";

export function secretNameForRole(role: string): string {
  return `FULLSEND_${role.toUpperCase()}_APP_PRIVATE_KEY`;
}

export function variableNameForRole(role: string): string {
  return `FULLSEND_${role.toUpperCase()}_CLIENT_ID`;
}

/**
 * Read-only port of `SecretsLayer.Analyze` (`internal/layers/secrets.go`).
 */
export async function analyzeSecretsLayer(
  org: string,
  gh: LayerGithub,
  agents: { role: string }[],
): Promise<LayerReport> {
  const report: LayerReport = {
    name: "secrets",
    status: "unknown",
    details: [],
    wouldInstall: [],
    wouldFix: [],
  };

  const present: string[] = [];
  const missing: string[] = [];

  for (const agent of agents) {
    const sName = secretNameForRole(agent.role);
    if (await gh.repoSecretExists(org, CONFIG_REPO_NAME, sName)) {
      present.push(sName);
    } else {
      missing.push(sName);
    }

    const vName = variableNameForRole(agent.role);
    if (await gh.repoVariableExists(org, CONFIG_REPO_NAME, vName)) {
      present.push(vName);
    } else {
      missing.push(vName);
    }
  }

  if (missing.length === 0) {
    report.status = "installed";
    for (const name of present) {
      report.details.push(`${name} exists`);
    }
  } else if (present.length === 0) {
    report.status = "not_installed";
    for (const name of missing) {
      report.wouldInstall.push(`create ${name}`);
    }
  } else {
    report.status = "degraded";
    for (const name of present) {
      report.details.push(`${name} exists`);
    }
    for (const name of missing) {
      report.wouldFix.push(`create missing ${name}`);
    }
  }

  return report;
}
