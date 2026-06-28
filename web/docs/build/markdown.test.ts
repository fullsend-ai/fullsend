import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { markdownToHtml } from "./markdown";

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../..",
);

describe("markdownToHtml", () => {
  it("renders GFM table", async () => {
    const md = "|a|b|\n|-|-|\n|1|2|\n";
    const { html } = await markdownToHtml(md, "docs/test.md", repoRoot);
    expect(html).toContain("<table");
    expect(html).toContain("1");
  });

  it("rewrites relative md link to hash doc URL", async () => {
    const md = "[x](./other.md)";
    const { html } = await markdownToHtml(
      md,
      "docs/guides/getting-started/installation.md",
      repoRoot,
    );
    expect(html).toContain('href="#/guides/getting-started/other"');
  });

  it("strips front matter from HTML body", async () => {
    const md = "---\ntitle: Hello\n---\n\n# Body\n";
    const { html, frontmatter } = await markdownToHtml(md, "docs/x.md", repoRoot);
    expect(frontmatter.title).toBe("Hello");
    expect(html).toContain("Body");
    expect(html).not.toContain("Hello");
  });

  it("rewrites link with heading fragment to :: slug form", async () => {
    const md = "[z](./other.md#Section-One)";
    const { html } = await markdownToHtml(
      md,
      "docs/guides/getting-started/installation.md",
      repoRoot,
    );
    expect(html).toMatch(/href="#\/guides\/getting-started\/other::section-one"/);
  });

  it("marks mermaid fence for client render", async () => {
    const md = "```mermaid\nflowchart LR\n  A-->B\n```\n";
    const { html } = await markdownToHtml(md, "docs/a.md", repoRoot);
    expect(html).toContain('class="mermaid-doc"');
    expect(html).toContain("flowchart");
  });

  it("rewrites link to an existing docs directory as trailing-slash hash", async () => {
    const md = "[p](../../problems)";
    const { html } = await markdownToHtml(
      md,
      "docs/guides/getting-started/installation.md",
      repoRoot,
    );
    expect(html).toContain('href="#/problems/"');
  });

  it("strips accidental docs/ prefix so directory links resolve", async () => {
    const md = "[x](docs/problems/applied/)";
    const { html } = await markdownToHtml(md, "docs/vision.md", repoRoot);
    expect(html).toContain('href="#/problems/applied/"');
  });

  it("rewrites link escaping docs/ to GitHub blob URL", async () => {
    // docs/ADRs/ → routeKeyDir=ADRs → join(ADRs, ../../README.md) = ../README.md → escapes docs/
    const md = "[readme](../../README.md)";
    const { html } = await markdownToHtml(
      md,
      "docs/ADRs/0002-initial-fullsend-design.md",
      repoRoot,
    );
    expect(html).toContain(
      'href="https://github.com/fullsend-ai/fullsend/blob/main/README.md"',
    );
  });

  it("rewrites non-.md link escaping docs/ to GitHub blob URL", async () => {
    // docs/site-deployment.md → routeKeyDir="" → join("", ../.github/workflows/site-build.yml)
    const md = "[workflow](../.github/workflows/site-build.yml)";
    const { html } = await markdownToHtml(
      md,
      "docs/site-deployment.md",
      repoRoot,
    );
    expect(html).toContain(
      'href="https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/site-build.yml"',
    );
  });

  it("preserves fragment when rewriting outside-docs link", async () => {
    const md = "[workflow](../.github/workflows/site-build.yml#L10)";
    const { html } = await markdownToHtml(
      md,
      "docs/site-deployment.md",
      repoRoot,
    );
    expect(html).toContain(
      'href="https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/site-build.yml#L10"',
    );
  });

  it("does not rewrite link that escapes the repo root", async () => {
    // ../../../../etc/passwd from docs/vision.md escapes repo root — no rewrite
    const md = "[outside](../../../../etc/passwd)";
    const { html } = await markdownToHtml(md, "docs/vision.md", repoRoot);
    expect(html).not.toContain("github.com/fullsend-ai/fullsend");
  });
});
