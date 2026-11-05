// @vitest-environment node
// (VitePress's markdown renderer loads esbuild, which does not run under jsdom.)
import { createMarkdownRenderer } from "vitepress";
import { describe, expect, it } from "vitest";
import { githubSlugify } from "./slugify";

describe("githubSlugify", () => {
  it.each([
    // VitePress default: `_4-...` (leading digit gets a `_` prefix).
    ["4. Tell fullsend the three identifiers", "4-tell-fullsend-the-three-identifiers"],
    // VitePress default: `if-you-don-t-...` (`'` becomes `-`).
    ["If you don't have OpenAI platform access", "if-you-dont-have-openai-platform-access"],
    // VitePress default: `_1-webhook-dispatch-service` (runs of `-` collapse).
    ["1. Webhook + dispatch service", "1-webhook--dispatch-service"],
    // Same under both slug functions.
    ["Accept a static key as well (`auth: api-key`)", "accept-a-static-key-as-well-auth-api-key"],
  ])("slugs %j as %j", (heading, expected) => {
    expect(githubSlugify(heading)).toBe(expected);
  });

  it("is not stateful", () => {
    expect(githubSlugify("Overview")).toBe("overview");
    expect(githubSlugify("Overview")).toBe("overview");
  });
});

describe("markdown.anchor.slugify wiring", () => {
  it("renders GitHub-style heading ids and keeps duplicate ids unique", async () => {
    const md = await createMarkdownRenderer(process.cwd(), {
      anchor: { slugify: githubSlugify },
    });
    const html = md.render(
      [
        "## 4. Tell fullsend the three identifiers",
        "### If you don't have OpenAI platform access",
        "## 1. Webhook + dispatch service",
        "## Overview",
        "## Overview",
      ].join("\n\n"),
      { relativePath: "test.md" },
    );
    const ids = [...html.matchAll(/<h\d id="([^"]*)"/g)].map((m) => m[1]);
    expect(ids).toEqual([
      "4-tell-fullsend-the-three-identifiers",
      "if-you-dont-have-openai-platform-access",
      "1-webhook--dispatch-service",
      "overview",
      "overview-1",
    ]);
  });
});
