import { describe, expect, it } from "vitest";
import { buildTree, type ManifestNode } from "./vitePluginDocs";

describe("buildTree", () => {
  it("sorts files alphabetically when no order is specified", () => {
    const result = buildTree([
      { routeKey: "guides/b", title: "B", segments: ["guides", "b"] },
      { routeKey: "guides/a", title: "A", segments: ["guides", "a"] },
      { routeKey: "guides/c", title: "C", segments: ["guides", "c"] },
    ]);

    const dir = result[0] as Extract<ManifestNode, { type: "dir" }>;
    expect(dir.type).toBe("dir");
    expect(dir.name).toBe("guides");
    const names = dir.children.map((c) => c.name);
    expect(names).toEqual(["a", "b", "c"]);
  });

  it("sorts files by order when frontmatter order is specified", () => {
    const result = buildTree([
      {
        routeKey: "guides/getting-started/configuring-github",
        title: "Configuring GitHub",
        segments: ["guides", "getting-started", "configuring-github"],
        order: 3,
      },
      {
        routeKey: "guides/getting-started/README",
        title: "Getting Started",
        segments: ["guides", "getting-started", "README"],
        order: 1,
      },
      {
        routeKey: "guides/getting-started/getting-inference",
        title: "Getting Inference",
        segments: ["guides", "getting-started", "getting-inference"],
        order: 2,
      },
      {
        routeKey: "guides/getting-started/org-mode",
        title: "Organization Mode",
        segments: ["guides", "getting-started", "org-mode"],
        order: 4,
      },
    ]);

    const guidesDir = result[0] as Extract<ManifestNode, { type: "dir" }>;
    expect(guidesDir.name).toBe("guides");
    const gsDir = guidesDir.children[0] as Extract<
      ManifestNode,
      { type: "dir" }
    >;
    expect(gsDir.name).toBe("getting-started");

    const names = gsDir.children.map((c) => c.name);
    expect(names).toEqual([
      "README",
      "getting-inference",
      "configuring-github",
      "org-mode",
    ]);
  });

  it("sorts ordered files before unordered files", () => {
    const result = buildTree([
      { routeKey: "d/z", title: "Z", segments: ["d", "z"] },
      { routeKey: "d/a", title: "A", segments: ["d", "a"], order: 2 },
      { routeKey: "d/m", title: "M", segments: ["d", "m"], order: 1 },
      { routeKey: "d/b", title: "B", segments: ["d", "b"] },
    ]);

    const dir = result[0] as Extract<ManifestNode, { type: "dir" }>;
    const names = dir.children.map((c) => c.name);
    // Ordered files first (by order), then unordered (alphabetically)
    expect(names).toEqual(["m", "a", "b", "z"]);
  });
});
