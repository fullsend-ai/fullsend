import { describe, expect, it } from "vitest";
import { mergeLayerStatuses, rollupOrgLayerStatus } from "./engine";
import type { LayerReport } from "./types";

function rep(status: LayerReport["status"]): LayerReport {
  return {
    name: "x",
    status,
    details: [],
    wouldInstall: [],
    wouldFix: [],
  };
}

describe("rollupOrgLayerStatus", () => {
  it("returns installed for empty reports", () => {
    expect(rollupOrgLayerStatus([])).toBe("installed");
  });

  it("returns installed when all installed", () => {
    expect(rollupOrgLayerStatus([rep("installed"), rep("installed")])).toBe("installed");
  });

  it("picks worst status", () => {
    expect(rollupOrgLayerStatus([rep("installed"), rep("degraded")])).toBe("degraded");
    expect(rollupOrgLayerStatus([rep("not_installed"), rep("installed")])).toBe("not_installed");
    expect(rollupOrgLayerStatus([rep("unknown"), rep("degraded")])).toBe("unknown");
  });
});

describe("mergeLayerStatuses", () => {
  it("is commutative for severity", () => {
    expect(mergeLayerStatuses("installed", "degraded")).toBe("degraded");
    expect(mergeLayerStatuses("degraded", "installed")).toBe("degraded");
  });
});
