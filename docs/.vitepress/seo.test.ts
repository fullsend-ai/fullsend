import { describe, expect, it } from "vitest";
import {
  DOCS_URL_BASE,
  canonicalUrl,
  globalSeoHead,
  isIndexablePage,
  isNonContentPath,
  isSitemapUrl,
  pageOutputPath,
  pageRobotsHead,
  pageSeoHead,
} from "./seo";

describe("pageOutputPath", () => {
  it("maps the root index to the empty (directory) path", () => {
    expect(pageOutputPath("index.md")).toBe("");
    expect(pageOutputPath("README.md")).toBe("");
  });

  it("maps a nested index/README to its directory", () => {
    expect(pageOutputPath("agents/index.md")).toBe("agents/");
    expect(pageOutputPath("guides/getting-started/README.md")).toBe("guides/getting-started/");
  });

  it("maps a content page to an .html path when cleanUrls is off", () => {
    expect(pageOutputPath("agents/triage.md")).toBe("agents/triage.html");
    expect(pageOutputPath("guides/user/jira-integration.md")).toBe(
      "guides/user/jira-integration.html",
    );
  });

  it("drops the extension entirely when cleanUrls is on", () => {
    expect(pageOutputPath("agents/triage.md", true)).toBe("agents/triage");
    expect(pageOutputPath("index.md", true)).toBe("");
  });
});

describe("canonicalUrl", () => {
  it("always includes the /docs/ base", () => {
    // Regression guard: the docs are served under /docs/, so a base-less
    // hostname (as in the original issue template) would drop that segment.
    for (const page of ["index.md", "agents/triage.md", "guides/getting-started/README.md"]) {
      expect(canonicalUrl(page).startsWith(DOCS_URL_BASE)).toBe(true);
    }
  });

  it("resolves the redirecting root to its content destination", () => {
    expect(canonicalUrl("index.md")).toBe("https://fullsend.sh/docs/guides/getting-started/");
  });

  it("resolves a directory index to a trailing-slash URL", () => {
    expect(canonicalUrl("agents/index.md")).toBe("https://fullsend.sh/docs/agents/");
  });

  it("resolves a content page to its .html URL when clean URLs are off", () => {
    expect(canonicalUrl("guides/user/jira-integration.md")).toBe(
      "https://fullsend.sh/docs/guides/user/jira-integration.html",
    );
  });

  it("resolves a content page to the URL shape served by Cloudflare", () => {
    expect(canonicalUrl("guides/user/jira-integration.md", true)).toBe(
      "https://fullsend.sh/docs/guides/user/jira-integration",
    );
  });
});

describe("pageSeoHead", () => {
  const head = pageSeoHead({
    page: "agents/triage.md",
    title: "Triage Agent | Fullsend",
    description: "How the triage agent works",
    cleanUrls: true,
  });

  it("emits a canonical link and og:url with the full docs URL", () => {
    const url = "https://fullsend.sh/docs/agents/triage";
    expect(head).toContainEqual(["link", { rel: "canonical", href: url }]);
    expect(head).toContainEqual(["meta", { property: "og:url", content: url }]);
  });

  it("emits page-specific og:title and og:description", () => {
    expect(head).toContainEqual([
      "meta",
      { property: "og:title", content: "Triage Agent | Fullsend" },
    ]);
    expect(head).toContainEqual([
      "meta",
      { property: "og:description", content: "How the triage agent works" },
    ]);
  });
});

describe("isIndexablePage", () => {
  it("excludes the 404 page from self-canonical / OG tags", () => {
    expect(isIndexablePage("404.md")).toBe(false);
  });

  it("treats content pages as indexable", () => {
    expect(isIndexablePage("index.md")).toBe(true);
    expect(isIndexablePage("agents/triage.md")).toBe(true);
  });

  it("excludes template and repo-metadata pages from canonical metadata", () => {
    expect(isIndexablePage("experiments/0000-experiment-template/index.md")).toBe(false);
    expect(isIndexablePage("experiments/example/RESULTS.md")).toBe(false);
    expect(isIndexablePage("experiments/example/skills/check/SKILL.md")).toBe(false);
  });
});

describe("isNonContentPath", () => {
  it("uses the same classification for source paths and clean URLs", () => {
    expect(isNonContentPath("ADRs/0000-adr-template.md")).toBe(true);
    expect(isNonContentPath("experiments/0000-experiment-template/")).toBe(true);
    expect(isNonContentPath("experiments/example/RESULTS")).toBe(true);
    expect(isNonContentPath("experiments/example/results")).toBe(false);
  });

  it("matches template names with suffixes, like the original isNonContent", () => {
    // The predicate is unanchored (no trailing `$`) so a suffixed template such
    // as `0000-foo-template-v2` classifies as non-content, matching the original
    // sidebar-discovery regex rather than treating it as a content page.
    expect(isNonContentPath("ADRs/0000-foo-template-v2.md")).toBe(true);
  });
});

describe("isSitemapUrl", () => {
  it("excludes the redirecting root and non-content URLs", () => {
    expect(isSitemapUrl("")).toBe(false);
    expect(isSitemapUrl("/")).toBe(false);
    expect(isSitemapUrl("experiments/0000-experiment-template/")).toBe(false);
    expect(isSitemapUrl("experiments/example/SKILL")).toBe(false);
  });

  it("keeps normal experiment pages in the public sitemap", () => {
    expect(isSitemapUrl("experiments/0026-eval-statistical-significance/")).toBe(true);
  });
});

describe("pageRobotsHead", () => {
  it("marks the directly reachable 404 asset noindex", () => {
    expect(pageRobotsHead("404.md")).toEqual([
      ["meta", { name: "robots", content: "noindex" }],
    ]);
    expect(pageRobotsHead("agents/triage.md")).toEqual([]);
  });
});

describe("globalSeoHead", () => {
  it("declares site-wide Open Graph and Twitter card metadata", () => {
    expect(globalSeoHead).toContainEqual(["meta", { property: "og:type", content: "website" }]);
    expect(globalSeoHead).toContainEqual([
      "meta",
      { property: "og:site_name", content: "Fullsend" },
    ]);
    expect(globalSeoHead).toContainEqual([
      "meta",
      { name: "twitter:card", content: "summary_large_image" },
    ]);
  });
});
