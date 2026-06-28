import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkRehype from "remark-rehype";
import rehypeStringify from "rehype-stringify";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import rehypeSlug from "rehype-slug";
import { visit } from "unist-util-visit";
import fs from "node:fs";
import path from "node:path";
import { toString } from "mdast-util-to-string";
import type { Root as MdastRoot } from "mdast";
import type { Element, Root as HastRoot } from "hast";
import matter from "gray-matter";
import GitHubSlugger from "github-slugger";
import { formatDocDirHash } from "./hashFormat";
import { filePathToRouteKey, type DocsFilePath } from "./paths";

const SLUG_FRAGMENT_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

/** Derive display title: first heading text, else route last segment. */
export function extractTitle(mdast: MdastRoot, routeKey: string): string {
  let heading: string | null = null;
  visit(mdast, "heading", (node) => {
    if (heading === null && node.depth === 1) {
      heading = toString(node).trim();
    }
  });
  if (heading) return heading;
  const base = routeKey.split("/").pop() ?? routeKey;
  return base === "README"
    ? (routeKey.split("/").slice(-2, -1)[0] ?? "README")
    : base;
}

function decodeFragment(encoded: string): string {
  try {
    return decodeURIComponent(encoded.replace(/\+/g, "%20"));
  } catch {
    return encoded;
  }
}

function normalizeFragmentToSlug(fragmentEncoded: string): string {
  const raw = decodeFragment(fragmentEncoded);
  let slug: string;
  if (SLUG_FRAGMENT_PATTERN.test(raw)) {
    slug = raw;
  } else {
    slug = new GitHubSlugger().slug(raw);
  }
  return slug.replace(/::/g, "-");
}

function isDocLinkPath(resolvedPosix: string): boolean {
  if (resolvedPosix.endsWith(".md") || resolvedPosix.endsWith(".markdown")) {
    return true;
  }
  const base = path.posix.basename(resolvedPosix);
  if (!base.includes(".")) return true;
  return false;
}

function resolvedPathToRouteKey(resolvedPosix: string): string {
  if (resolvedPosix.endsWith(".md")) {
    return resolvedPosix.slice(0, -".md".length);
  }
  if (resolvedPosix.endsWith(".markdown")) {
    return resolvedPosix.replace(/\.markdown$/, "");
  }
  return resolvedPosix;
}

const GITHUB_BLOB_BASE =
  "https://github.com/fullsend-ai/fullsend/blob/main/";

function remarkRewriteMdLinks(repoRoot: string, sourceFile: DocsFilePath) {
  return (tree: MdastRoot) => {
    visit(tree, "link", (node) => {
      const url = node.url;
      if (!url || /^(https?:|mailto:|#)/i.test(url)) return;
      if (/^[a-z][a-z0-9+.-]*:/i.test(url)) return;
      if (url.startsWith("/")) return;

      const [pathPart, fragEncoded] = url.split("#", 2);
      const routeKeyDir = path.posix.dirname(filePathToRouteKey(sourceFile));
      const baseDir = routeKeyDir === "." ? "" : routeKeyDir;
      // resolvedPosix is relative to docs/ — may start with ../ when the link escapes docs/
      const resolvedPosix = path.posix.normalize(
        path.posix.join(baseDir, pathPart),
      );

      // Link escapes docs/ — rewrite to a GitHub blob URL if within repo root
      if (resolvedPosix.startsWith("../")) {
        const repoRelPosix = path.posix.normalize(
          path.posix.join("docs", resolvedPosix),
        );
        if (!repoRelPosix.startsWith("../")) {
          const frag = fragEncoded ? `#${fragEncoded}` : "";
          node.url = `${GITHUB_BLOB_BASE}${repoRelPosix}${frag}`;
        }
        return;
      }

      /** Paths are repo-relative to `docs/`; strip accidental `docs/` prefix from links. */
      const docRel = resolvedPosix.replace(/^docs\//, "");

      if (!isDocLinkPath(docRel)) return;

      const absUnderDocs = path.join(repoRoot, "docs", docRel);
      let linkTargetIsDir = false;
      try {
        linkTargetIsDir =
          fs.existsSync(absUnderDocs) && fs.statSync(absUnderDocs).isDirectory();
      } catch {
        linkTargetIsDir = false;
      }

      if (linkTargetIsDir) {
        const dirKey = docRel.replace(/\\/g, "/");
        node.url = formatDocDirHash(dirKey);
        return;
      }

      const routeKey = resolvedPathToRouteKey(docRel);
      const frag = fragEncoded ?? "";
      if (!frag) {
        node.url = `#/${routeKey}`;
      } else {
        const slug = normalizeFragmentToSlug(frag);
        node.url = `#/${routeKey}::${slug}`;
      }
    });
    return tree;
  };
}

/** Strip `::` from element ids so they never contain the hash-route delimiter. */
function rehypeStripColonIds() {
  return (tree: HastRoot) => {
    visit(tree, "element", (node: Element) => {
      const id = node.properties?.id;
      if (typeof id === "string") {
        node.properties.id = id.replace(/::/g, "-");
      }
    });
    return tree;
  };
}

/** After remark-rehype: turn `pre > code.language-mermaid` into Mermaid-friendly structure. */
function rehypeMermaidClass() {
  return (tree: HastRoot) => {
    visit(tree, "element", (node: Element) => {
      if (node.tagName !== "pre") return;
      const child = node.children[0] as Element | undefined;
      if (!child || child.tagName !== "code") return;
      const cls = Array.isArray(child.properties.className)
        ? child.properties.className
        : [];
      const lang = cls.find((c) => String(c).startsWith("language-"));
      if (lang !== "language-mermaid") return;
      node.properties.className = ["mermaid-doc"];
      const text = child.children[0];
      if (text && text.type === "text" && typeof text.value === "string") {
        node.children = [{ type: "text", value: text.value }];
      }
    });
    return tree;
  };
}

const headingLevels = ["h1", "h2", "h3", "h4", "h5", "h6"] as const;
const headingAttributes = Object.fromEntries(
  headingLevels.map((tag) => [
    tag,
    [...(defaultSchema.attributes?.[tag] ?? []), "id"],
  ]),
) as Record<string, string[]>;

const sanitizeSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    ...headingAttributes,
    code: [
      ...(defaultSchema.attributes?.code ?? []),
      "className",
      "class",
    ],
    pre: [...(defaultSchema.attributes?.pre ?? []), "className", "class"],
    span: [...(defaultSchema.attributes?.span ?? []), "className", "class"],
  },
};

export async function markdownToHtml(
  markdown: string,
  sourceFile: DocsFilePath,
  repoRoot: string,
): Promise<{
  title: string;
  html: string;
  frontmatter: Record<string, unknown>;
}> {
  const routeKey = filePathToRouteKey(sourceFile);
  const { data, content } = matter(markdown);
  const frontmatter = data as Record<string, unknown>;

  const processor = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkRewriteMdLinks, repoRoot, sourceFile)
    .use(remarkRehype, { allowDangerousHtml: false })
    .use(rehypeSlug)
    .use(rehypeMermaidClass)
    .use(rehypeStripColonIds)
    .use(rehypeSanitize, sanitizeSchema)
    .use(rehypeStringify);

  const file = await processor.process(content);
  const html = String(file);
  const mdast = unified().use(remarkParse).use(remarkGfm).parse(content) as MdastRoot;
  const title = extractTitle(mdast, routeKey);
  return { title, html, frontmatter };
}
