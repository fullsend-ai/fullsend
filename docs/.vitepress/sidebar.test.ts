import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getMarkdownFiles } from "./sidebar";

describe("getMarkdownFiles", () => {
  let docsRoot: string;

  beforeEach(() => {
    docsRoot = fs.mkdtempSync(path.join(os.tmpdir(), "sidebar-"));
  });

  afterEach(() => {
    fs.rmSync(docsRoot, { recursive: true, force: true });
  });

  function write(rel: string, content: string) {
    const full = path.join(docsRoot, rel);
    fs.mkdirSync(path.dirname(full), { recursive: true });
    fs.writeFileSync(full, content);
  }

  it("returns an empty list when the section directory is missing", () => {
    expect(getMarkdownFiles("missing", "missing", docsRoot)).toEqual([]);
  });

  it("lists top-level markdown files with H1 titles and skips README.md", () => {
    write("section/README.md", "# Section Index\n");
    write("section/alpha.md", "# Alpha Page\n");
    write("section/beta.md", "# Beta Page\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      { text: "Alpha Page", link: "/section/alpha" },
      { text: "Beta Page", link: "/section/beta" },
    ]);
  });

  it("prefers a frontmatter title over the H1", () => {
    write("section/page.md", "---\ntitle: Frontmatter Title\n---\n# Heading Title\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      { text: "Frontmatter Title", link: "/section/page" },
    ]);
  });

  it("falls back to the slug or directory name when no title is present", () => {
    write("section/untitled.md", "plain text\n");
    write("section/leaf/README.md", "plain text\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      { text: "leaf", link: "/section/leaf/" },
      { text: "untitled", link: "/section/untitled" },
    ]);
  });

  it("emits a leaf link for a subdirectory that only has a README", () => {
    write("section/applied/README.md", "# Applied Problem Considerations\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      { text: "Applied Problem Considerations", link: "/section/applied/" },
    ]);
  });

  it("nests subdirectory pages under a linked, collapsed group", () => {
    write("section/applied/README.md", "# Applied Problem Considerations\n");
    write("section/applied/konflux-ci/README.md", "# Applied: konflux-ci\n");
    write("section/applied/agent-eval-tools/README.md", "# Agent Setup Evaluation Tools\n");
    write("section/applied/extra.md", "# Extra Nested Page\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      {
        text: "Applied Problem Considerations",
        link: "/section/applied/",
        collapsed: true,
        items: [
          { text: "Agent Setup Evaluation Tools", link: "/section/applied/agent-eval-tools/" },
          { text: "Extra Nested Page", link: "/section/applied/extra" },
          { text: "Applied: konflux-ci", link: "/section/applied/konflux-ci/" },
        ],
      },
    ]);
  });

  it("nests pages under a directory that has no README of its own", () => {
    write("section/nested/page.md", "# Nested Page\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      {
        text: "nested",
        collapsed: true,
        items: [{ text: "Nested Page", link: "/section/nested/page" }],
      },
    ]);
  });

  it("walks more than two directory levels", () => {
    write("section/a/README.md", "# A\n");
    write("section/a/b/README.md", "# B\n");
    write("section/a/b/c.md", "# C\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      {
        text: "A",
        link: "/section/a/",
        collapsed: true,
        items: [
          {
            text: "B",
            link: "/section/a/b/",
            collapsed: true,
            items: [{ text: "C", link: "/section/a/b/c" }],
          },
        ],
      },
    ]);
  });

  it("skips hidden directories and non-content template or ALL-CAPS names", () => {
    write("section/.hidden/secret.md", "# Secret\n");
    write("section/0000-adr-template.md", "# Template\n");
    write("section/RESULTS/notes.md", "# Notes\n");
    write("section/visible.md", "# Visible\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      { text: "Visible", link: "/section/visible" },
    ]);
  });

  it("omits empty directories that have neither a README nor nested pages", () => {
    fs.mkdirSync(path.join(docsRoot, "section", "empty"), { recursive: true });
    write("section/kept.md", "# Kept\n");

    expect(getMarkdownFiles("section", "section", docsRoot)).toEqual([
      { text: "Kept", link: "/section/kept" },
    ]);
  });

  it("includes nested applied problem docs from the real problems tree", () => {
    const realDocsDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
    const items = getMarkdownFiles("problems", "problems", realDocsDir);
    const applied = items.find((item) => item.link === "/problems/applied/");
    expect(applied?.items?.map((item) => item.link)).toEqual(
      expect.arrayContaining([
        "/problems/applied/agent-eval-tools/",
        "/problems/applied/konflux-ci/",
      ]),
    );
  });

  it("resolves nested applied problem docs using the default docsRoot", () => {
    // Production call sites (docs/.vitepress/config.ts) use the 2-argument
    // form and rely on the default `docsRoot` derived from import.meta.url.
    // Exercise that default against the real docs tree so a regression there
    // (which would silently empty production sidebars) is caught.
    const items = getMarkdownFiles("problems", "problems");
    const applied = items.find((item) => item.link === "/problems/applied/");
    expect(applied?.items?.map((item) => item.link)).toEqual(
      expect.arrayContaining([
        "/problems/applied/agent-eval-tools/",
        "/problems/applied/konflux-ci/",
      ]),
    );
  });

  it("does not recurse infinitely through a directory symlink cycle", () => {
    write("section/real/README.md", "# Real\n");
    fs.symlinkSync(
      path.join(docsRoot, "section", "real"),
      path.join(docsRoot, "section", "real", "cycle"),
      "dir",
    );

    expect(() => getMarkdownFiles("section", "section", docsRoot)).not.toThrow();
  });
});
