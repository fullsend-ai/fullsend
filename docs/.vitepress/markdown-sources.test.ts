import fs from "node:fs";
import os from "node:os";
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
      }),
    ).toThrow(/disk full/);
  });

  it("emits README.md alongside index.md so in-document README.md links resolve", () => {
    const srcDir = fs.mkdtempSync(path.join(os.tmpdir(), "md-src-"));
    const outDir = fs.mkdtempSync(path.join(os.tmpdir(), "md-out-"));
    try {
      fs.mkdirSync(path.join(srcDir, "guides", "getting-started"), { recursive: true });
      fs.writeFileSync(
        path.join(srcDir, "guides", "README.md"),
        "- [Mint](getting-started/README.md)\n- [Hash](getting-started/README.md#setup)\n",
      );
      fs.writeFileSync(
        path.join(srcDir, "guides", "getting-started", "README.md"),
        "# Getting started\nSee [guides](../README.md).\n",
      );
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
        ]),
      );

      for (const rel of emitted) {
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
