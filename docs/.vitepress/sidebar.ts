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
 *
 * `visitedRealDirs` tracks the real (symlink-resolved) paths of directories
 * on the current walk stack, so a directory symlink that points back to an
 * ancestor can't recurse without bound (e.g. a submodule symlink like
 * `docs/experiments -> ../experiments`). Broken or unreadable entries are
 * skipped rather than failing the whole config build.
 */
export function getMarkdownFiles(
  dir: string,
  base: string,
  docsRoot: string = defaultDocsDir,
  visitedRealDirs: ReadonlySet<string> = new Set(),
): DefaultTheme.SidebarItem[] {
  const fullDir = path.resolve(docsRoot, dir);
  if (!fs.existsSync(fullDir)) return [];

  let realDir: string;
  try {
    realDir = fs.realpathSync(fullDir);
  } catch {
    return [];
  }
  if (visitedRealDirs.has(realDir)) return [];
  const nextVisited = new Set(visitedRealDirs);
  nextVisited.add(realDir);

  let entries: string[];
  try {
    entries = fs.readdirSync(fullDir).sort();
  } catch {
    return [];
  }

  const items: DefaultTheme.SidebarItem[] = [];
  for (const entry of entries) {
    if (isNonContentPath(entry)) continue;

    const entryPath = path.resolve(fullDir, entry);
    if (entry.endsWith(".md") && entry !== "README.md") {
      const slug = entry.replace(/\.md$/, "");
      let content: string;
      try {
        content = fs.readFileSync(entryPath, "utf-8");
      } catch {
        continue;
      }
      items.push({ text: pageTitle(content, slug), link: `/${base}/${slug}` });
      continue;
    }

    let isDirectory: boolean;
    try {
      isDirectory = fs.statSync(entryPath).isDirectory();
    } catch {
      continue;
    }
    if (entry.startsWith(".") || !isDirectory) continue;

    let realEntryPath: string;
    try {
      realEntryPath = fs.realpathSync(entryPath);
    } catch {
      continue;
    }
    // A directory symlink that resolves back to an ancestor already on the
    // walk stack is a cycle. Skip it entirely here (rather than recursing
    // and inspecting the empty result) so it isn't mistaken for a
    // README-only leaf and published as a duplicate sidebar entry.
    if (nextVisited.has(realEntryPath)) continue;

    const childItems = getMarkdownFiles(
      path.join(dir, entry),
      `${base}/${entry}`,
      docsRoot,
      nextVisited,
    );
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
