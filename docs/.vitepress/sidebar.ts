import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { DefaultTheme } from "vitepress";
import { isNonContentPath } from "./seo";

const defaultDocsDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

function pageTitle(content: string, fallback: string): string {
  const fmTitleMatch = content.match(/^title:\s*["']?(.+?)["']?\s*$/m);
  const titleMatch = content.match(/^#\s+(.+)$/m);
  return fmTitleMatch?.[1] || titleMatch?.[1] || fallback;
}

/**
 * Auto-discover markdown pages under `dir` as a VitePress sidebar tree.
 *
 * Top-level `.md` files (except `README.md`) become leaf items. Directories
 * that contain a `README.md` become a linked group; nested pages and folders
 * are emitted as `items` so content below the first level appears in the
 * sidebar rather than being omitted.
 */
export function getMarkdownFiles(
  dir: string,
  base: string,
  docsRoot: string = defaultDocsDir,
): DefaultTheme.SidebarItem[] {
  const fullDir = path.resolve(docsRoot, dir);
  if (!fs.existsSync(fullDir)) return [];

  const items: DefaultTheme.SidebarItem[] = [];
  for (const entry of fs.readdirSync(fullDir).sort()) {
    if (isNonContentPath(entry)) continue;

    const entryPath = path.resolve(fullDir, entry);
    if (entry.endsWith(".md") && entry !== "README.md") {
      const slug = entry.replace(/\.md$/, "");
      const content = fs.readFileSync(entryPath, "utf-8");
      items.push({ text: pageTitle(content, slug), link: `/${base}/${slug}` });
      continue;
    }

    if (entry.startsWith(".") || !fs.statSync(entryPath).isDirectory()) continue;

    const childItems = getMarkdownFiles(path.join(dir, entry), `${base}/${entry}`, docsRoot);
    const readmePath = path.resolve(entryPath, "README.md");
    if (!fs.existsSync(readmePath)) {
      if (childItems.length === 0) continue;
      items.push({ text: entry, collapsed: true, items: childItems });
      continue;
    }

    const text = pageTitle(fs.readFileSync(readmePath, "utf-8"), entry);
    const link = `/${base}/${entry}/`;
    if (childItems.length === 0) {
      items.push({ text, link });
      continue;
    }
    items.push({ text, link, collapsed: true, items: childItems });
  }
  return items;
}
