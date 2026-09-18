import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { copyMarkdownSources, relativeAssetHrefs } from "./markdown-sources";

const emptyRead = () => "";

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
      readFile: emptyRead,
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
      readFile: emptyRead,
    });
    expect(copied).toEqual([
      [path.join("/src", "README.md"), path.join("/out", "index.md")],
      [path.join("/src", "README.md"), path.join("/out", "README.md")],
      [path.join("/src", "agents/README.md"), path.join("/out", "agents/index.md")],
      [path.join("/src", "agents/README.md"), path.join("/out", "agents/README.md")],
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
      readFile: emptyRead,
    });
    expect(copied).toEqual([[path.join("/src", "glossary.md"), path.join("/out", "glossary.md")]]);
  });

  it("throws when no page source exists, so a broken build cannot ship empty .md output", () => {
    expect(() =>
      copyMarkdownSources({
        pages: ["404.md", "glossary.md"],
        srcDir: "/src",
        outDir: "/out",
        rewrites: {},
        exists: () => false,
        mkdir: () => {},
        copyFile: () => {},
        readFile: emptyRead,
      }),
    ).toThrow(/copied 0 markdown files/);
  });

  it("lets copyFile errors propagate instead of counting a failed copy", () => {
    expect(() =>
      copyMarkdownSources({
        pages: ["glossary.md"],
        srcDir: "/src",
        outDir: "/out",
        rewrites: {},
        exists: () => true,
        mkdir: () => {},
        copyFile: () => {
          throw new Error("disk full");
        },
        readFile: emptyRead,
      }),
    ).toThrow(/disk full/);
  });

  it("copies relative images referenced by a page, including srcExclude assets", () => {
    const copied: Array<[string, string]> = [];
    copyMarkdownSources({
      pages: ["agents/triage.md"],
      srcDir: "/src",
      outDir: "/out",
      rewrites: {},
      exists: () => true,
      mkdir: () => {},
      copyFile: (src, dest) => copied.push([src, dest]),
      readFile: () => "![Triage agent icon](icons/triage.png)\n",
    });
    expect(copied).toEqual([
      [path.join("/src", "agents/triage.md"), path.join("/out", "agents/triage.md")],
      [path.join("/src", "agents/icons/triage.png"), path.join("/out", "agents/icons/triage.png")],
    ]);
  });

  it("copies a shared image once when multiple pages reference it", () => {
    const copied: Array<[string, string]> = [];
    copyMarkdownSources({
      pages: ["agents/code.md", "agents/fix.md"],
      srcDir: "/src",
      outDir: "/out",
      rewrites: {},
      exists: () => true,
      mkdir: () => {},
      copyFile: (src, dest) => copied.push([src, dest]),
      readFile: () => "![Code agent icon](icons/coder.png)\n",
    });
    expect(copied.filter(([, dest]) => dest.endsWith("coder.png"))).toEqual([
      [path.join("/src", "agents/icons/coder.png"), path.join("/out", "agents/icons/coder.png")],
    ]);
  });

  it("skips remote, site-absolute, missing, and path-escaping image hrefs", () => {
    const copied: Array<[string, string]> = [];
    const srcDir = "/src";
    copyMarkdownSources({
      pages: ["agents/triage.md"],
      srcDir,
      outDir: "/out",
      rewrites: {},
      exists: (file) => file.endsWith("triage.md"),
      mkdir: () => {},
      copyFile: (src, dest) => copied.push([src, dest]),
      readFile: () =>
        [
          "![remote](https://example.com/triage.png)",
          "![site](/img/logo.png)",
          "![missing](icons/missing.png)",
          "![escape](../../secret.png)",
        ].join("\n"),
    });
    expect(copied).toEqual([
      [path.join(srcDir, "agents/triage.md"), path.join("/out", "agents/triage.md")],
    ]);
  });

  it("emits README.md alongside index.md so in-document README.md links resolve", () => {
    const srcDir = fs.mkdtempSync(path.join(os.tmpdir(), "md-src-"));
    const outDir = fs.mkdtempSync(path.join(os.tmpdir(), "md-out-"));
    try {
      fs.mkdirSync(path.join(srcDir, "guides", "getting-started"), { recursive: true });
      fs.mkdirSync(path.join(srcDir, "guides", "getting-started", "icons"), { recursive: true });
      fs.writeFileSync(
        path.join(srcDir, "guides", "README.md"),
        "- [Mint](getting-started/README.md)\n- [Hash](getting-started/README.md#setup)\n",
      );
      fs.writeFileSync(
        path.join(srcDir, "guides", "getting-started", "README.md"),
        "# Getting started\nSee [guides](../README.md).\n![Icon](icons/setup.png)\n",
      );
      fs.writeFileSync(path.join(srcDir, "guides", "getting-started", "icons", "setup.png"), "png");
      copyMarkdownSources({
        pages: ["guides/README.md", "guides/getting-started/README.md"],
        srcDir,
        outDir,
        rewrites: {
          "guides/README.md": "guides/index.md",
          "guides/getting-started/README.md": "guides/getting-started/index.md",
        },
      });

      const emitted = new Set(
        collectFiles(outDir).map((file) => path.relative(outDir, file).split(path.sep).join("/")),
      );
      expect(emitted).toEqual(
        new Set([
          "guides/index.md",
          "guides/README.md",
          "guides/getting-started/index.md",
          "guides/getting-started/README.md",
          "guides/getting-started/icons/setup.png",
        ]),
      );

      for (const rel of emitted) {
        if (!rel.endsWith(".md")) continue;
        const fromDir = path.dirname(path.join(outDir, rel));
        for (const href of markdownHrefs(fs.readFileSync(path.join(outDir, rel), "utf8"))) {
          const target = path.resolve(fromDir, href.split("#", 1)[0]);
          expect(fs.existsSync(target), `${rel} links to missing ${href}`).toBe(true);
        }
      }
    } finally {
      fs.rmSync(srcDir, { recursive: true, force: true });
      fs.rmSync(outDir, { recursive: true, force: true });
    }
  });
});

describe("relativeAssetHrefs", () => {
  it("collects markdown images and html img src, ignoring URLs and site-absolute paths", () => {
    expect(
      relativeAssetHrefs(`
![Triage](icons/triage.png)
![Titled](icons/coder.png "Coder")
![Bracketed](<icons/retro.png>)
<img src="icons/review.png">
<img src='icons/prioritize.png' alt="x">
![remote](https://example.com/x.png)
![site](/img/logo.png)
![hash](#anchor)
      `),
    ).toEqual([
      "icons/triage.png",
      "icons/coder.png",
      "icons/retro.png",
      "icons/review.png",
      "icons/prioritize.png",
    ]);
  });
});

function collectFiles(dir: string): string[] {
  const files: string[] = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) files.push(...collectFiles(full));
    else files.push(full);
  }
  return files;
}

function markdownHrefs(src: string): string[] {
  return [...src.matchAll(/\[[^\]]*\]\(([^)]+)\)/g)]
    .map((match) => match[1])
    .filter((href) => !/^(https?:|#|mailto:)/.test(href));
}
