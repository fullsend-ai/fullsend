import type { LayerReport, LayerStatus } from "./types";

/** Higher = worse for rollup UX (matches CLI-style pessimism). */
const severity: Record<LayerStatus, number> = {
  installed: 0,
  not_installed: 1,
  degraded: 2,
  unknown: 3,
};

/** Picks the more severe of two layer statuses. */
export function mergeLayerStatuses(a: LayerStatus, b: LayerStatus): LayerStatus {
  return severity[a] >= severity[b] ? a : b;
}

/**
 * Single org-level rollup across layer reports (for Pane A — Fullsend status).
 * Empty input is treated as fully installed (no layers to disagree).
 */
export function rollupOrgLayerStatus(reports: LayerReport[]): LayerStatus {
  if (reports.length === 0) return "installed";
  return reports.reduce((acc, r) => mergeLayerStatuses(acc, r.status), "installed" as LayerStatus);
}
