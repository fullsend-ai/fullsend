import { describe, expect, it } from "vitest";
import {
  filePathToRouteKey,
  listDocMarkdownFiles,
  pathnameToRouteKey,
  routeKeyToUrl,
} from "./paths";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

describe("paths", () => {
  it("filePathToRouteKey strips docs/ and .md", () => {
    expect(filePathToRouteKey("docs/guides/getting-started/installation.md")).toBe(
      "guides/getting-started/installation",
    );
    expect(filePathToRouteKey("docs/README.md")).toBe("README");
  });

  it("routeKeyToUrl", () => {
    expect(routeKeyToUrl("guides/getting-started/installation")).toBe(
      "/docs/guides/getting-started/installation",
    );
  });

  it("pathnameToRouteKey", () => {
    expect(pathnameToRouteKey("/docs/guides/getting-started/installation")).toBe(
      "guides/getting-started/installation",
    );
    expect(pathnameToRouteKey("/docs/")).toBe("");
  });

  it("listDocMarkdownFiles respects filter", () => {
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "fullsend-docs-"));
    fs.mkdirSync(path.join(tmp, "docs", "a"), { recursive: true });
    fs.writeFileSync(path.join(tmp, "docs", "a", "x.md"), "# x\n");
    fs.writeFileSync(path.join(tmp, "docs", "skip.md"), "# s\n");

    const all = listDocMarkdownFiles(tmp);
    expect(all).toContain("docs/a/x.md");
    expect(all).toContain("docs/skip.md");

    const filtered = listDocMarkdownFiles(tmp, (p) => p !== "docs/skip.md");
    expect(filtered).toContain("docs/a/x.md");
    expect(filtered).not.toContain("docs/skip.md");
  });
});
