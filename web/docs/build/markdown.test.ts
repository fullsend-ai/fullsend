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
});
