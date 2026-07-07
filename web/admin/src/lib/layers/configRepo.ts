import type { LayerReport } from "../status/types";
import { CONFIG_FILE_PATH, CONFIG_REPO_NAME } from "./constants";
import type { LayerGithub } from "./githubClient";
import { parseOrgConfigYaml, validateOrgConfig } from "./orgConfigParse";

/**
 * Read-only port of `ConfigRepoLayer.Analyze` (`internal/layers/configrepo.go`).
 */
export async function analyzeConfigRepoLayer(org: string, gh: LayerGithub): Promise<LayerReport> {
  const report: LayerReport = {
    name: "config-repo",
    status: "unknown",
    details: [],
    wouldInstall: [],
    wouldFix: [],
  };

  const exists = await gh.getRepoExists(org, CONFIG_REPO_NAME);
  if (!exists) {
    report.status = "not_installed";
    report.wouldInstall = [`create ${CONFIG_REPO_NAME} repository`, `write ${CONFIG_FILE_PATH}`];
    return report;
  }

  const content = await gh.getRepoFileUtf8(org, CONFIG_REPO_NAME, CONFIG_FILE_PATH);
  if (content === null) {
    report.status = "degraded";
    report.details = [`repo exists but ${CONFIG_FILE_PATH} is missing`];
    report.wouldFix = [`write ${CONFIG_FILE_PATH}`];
    return report;
  }

  let parsed;
  try {
    parsed = parseOrgConfigYaml(content);
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    report.status = "degraded";
    report.details = [`${CONFIG_FILE_PATH} exists but is invalid: ${msg}`];
    report.wouldFix = [`rewrite ${CONFIG_FILE_PATH}`];
    return report;
  }

  const validateErr = validateOrgConfig(parsed);
  if (validateErr) {
    report.status = "degraded";
    report.details = [`${CONFIG_FILE_PATH} exists but is invalid: ${validateErr}`];
    report.wouldFix = [`rewrite ${CONFIG_FILE_PATH}`];
    return report;
  }

  report.status = "installed";
  report.details = [`${CONFIG_FILE_PATH} exists and is valid`];
  return report;
}
