import { describe, expect, it } from "vitest";
import { layerStatusLabel, type LayerStatus } from "./types";

describe("layerStatusLabel", () => {
  it("maps not_installed", () => {
    const s: LayerStatus = "not_installed";
    expect(layerStatusLabel(s)).toBe("not installed");
  });
});
