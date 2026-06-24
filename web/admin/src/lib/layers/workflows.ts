import type { LayerReport } from "../status/types";
import { CONFIG_REPO_NAME, WORKFLOWS_MANAGED_FILES } from "./constants";
import type { LayerGithub } from "./githubClient";

/**
 * Read-only port of `WorkflowsLayer.Analyze` (`internal/layers/workflows.go`).
 */
export async function analyzeWorkflowsLayer(org: string, gh: LayerGithub): Promise<LayerReport> {
  const report: LayerReport = {
    name: "workflows",
    status: "unknown",
    details: [],
    wouldInstall: [],
    wouldFix: [],
  };

  const present: string[] = [];
  const missing: string[] = [];

  for (const path of WORKFLOWS_MANAGED_FILES) {
    const body = await gh.getRepoFileUtf8(org, CONFIG_REPO_NAME, path);
    if (body === null) {
      missing.push(path);
    } else {
      present.push(path);
    }
  }

  if (missing.length === 0) {
    report.status = "installed";
    for (const p of present) {
      report.details.push(`${p} exists`);
    }
  } else if (present.length === 0) {
    report.status = "not_installed";
    for (const m of missing) {
      report.wouldInstall.push(`write ${m}`);
    }
  } else {
    report.status = "degraded";
    for (const p of present) {
      report.details.push(`${p} exists`);
    }
    for (const m of missing) {
      report.wouldFix.push(`write ${m}`);
    }
  }

  return report;
}
