import path from "node:path";
import { describe, expect, it } from "vitest";
import { copyMarkdownSources } from "./markdown-sources";

describe("copyMarkdownSources", () => {
  it("copies a content page to the same relative path in outDir", () => {
    const copied: Array<[string, string]> = [];
    const mkdirs: string[] = [];
    copyMarkdownSources({
      pages: ["agents/triage.md"],
      srcDir: "/src",
      outDir: "/out",
      rewrites: {},
      exists: () => true,
      mkdir: (dir) => mkdirs.push(dir),
      copyFile: (src, dest) => copied.push([src, dest]),
    });
    expect(mkdirs).toEqual([path.join("/out", "agents")]);
    expect(copied).toEqual([
      [path.join("/src", "agents/triage.md"), path.join("/out", "agents/triage.md")],
    ]);
  });

  it("applies README.md → index.md rewrites so the .md sits next to index.html", () => {
    const copied: Array<[string, string]> = [];
    copyMarkdownSources({
      pages: ["README.md", "agents/README.md"],
      srcDir: "/src",
      outDir: "/out",
      rewrites: {
        "README.md": "index.md",
        "agents/README.md": "agents/index.md",
      },
      exists: () => true,
      mkdir: () => {},
      copyFile: (src, dest) => copied.push([src, dest]),
    });
    expect(copied).toEqual([
      [path.join("/src", "README.md"), path.join("/out", "index.md")],
      [path.join("/src", "agents/README.md"), path.join("/out", "agents/index.md")],
    ]);
  });

  it("skips pages whose source file is missing", () => {
    const copied: Array<[string, string]> = [];
    copyMarkdownSources({
      pages: ["404.md", "glossary.md"],
      srcDir: "/src",
      outDir: "/out",
      rewrites: {},
      exists: (file) => file.endsWith("glossary.md"),
      mkdir: () => {},
      copyFile: (src, dest) => copied.push([src, dest]),
    });
    expect(copied).toEqual([[path.join("/src", "glossary.md"), path.join("/out", "glossary.md")]]);
  });
});
