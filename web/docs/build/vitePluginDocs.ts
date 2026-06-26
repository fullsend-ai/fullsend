import type { ModuleNode, Plugin } from "vite";
import fs from "node:fs";
import path from "node:path";
import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import matter from "gray-matter";
import type { Root as MdastRoot } from "mdast";
import {
  listDocMarkdownFiles,
  filePathToRouteKey,
  type DocsFilePath,
} from "./paths";
import { markdownToHtml, extractTitle } from "./markdown";

const VIRTUAL_BOOTSTRAP = "\0virtual:fullsend-docs";
const VIRTUAL_BOOTSTRAP_PUBLIC = "virtual:fullsend-docs";
const PAGE_PREFIX = "virtual:fullsend-docs/page/";
const PAGE_INTERNAL_PREFIX = "\0fullsend-docs-page:";

export type ManifestNode =
  | { type: "dir"; name: string; children: ManifestNode[] }
  | { type: "file"; name: string; routeKey: string; title: string; order?: number };

type FileNode = Extract<ManifestNode, { type: "file" }>;

/** Internal tree nodes use a `Map` for dirs; manifest output uses arrays. */
type DirNode = {
  children: Map<string, DirNode | FileNode>;
};

export function buildTree(
  paths: { routeKey: string; title: string; segments: string[]; order?: number }[],
): ManifestNode[] {
  const root: DirNode = { children: new Map() };

  function ensureDir(d: DirNode, name: string): DirNode {
    const existing = d.children.get(name);
    if (existing) {
      if ("routeKey" in existing) {
        throw new Error(`path conflict: ${name} is both file and directory`);
      }
      return existing;
    }
    const next: DirNode = { children: new Map() };
    d.children.set(name, next);
    return next;
  }

  for (const p of paths) {
    let d = root;
    const segs = p.segments;
    for (let i = 0; i < segs.length - 1; i++) {
      d = ensureDir(d, segs[i]!);
    }
    const leafName = segs[segs.length - 1]!;
    const fileNode: FileNode = {
      type: "file",
      name: leafName,
      routeKey: p.routeKey,
      title: p.title,
      ...(p.order != null ? { order: p.order } : {}),
    };
    if (d.children.has(leafName)) {
      const existing = d.children.get(leafName);
      if (existing && !("routeKey" in existing)) {
        throw new Error(`path conflict: ${leafName} is both file and directory`);
      }
    }
    d.children.set(leafName, fileNode);
  }

  function toManifest(dir: DirNode): ManifestNode[] {
    const entries = [...dir.children.entries()];
    const dirNodes: ManifestNode[] = [];
    const fileNodes: FileNode[] = [];
    for (const [name, ch] of entries) {
      if ("routeKey" in ch) {
        fileNodes.push(ch);
      } else {
        dirNodes.push({
          type: "dir",
          name,
          children: toManifest(ch),
        });
      }
    }
    dirNodes.sort((a, b) => a.name.localeCompare(b.name));
    fileNodes.sort((a, b) => {
      const oa = a.order ?? Infinity;
      const ob = b.order ?? Infinity;
      if (oa !== ob) return oa - ob;
      return a.name.localeCompare(b.name);
    });
    return [...dirNodes, ...fileNodes];
  }

  return toManifest(root);
}

function isRepoDocMarkdownFile(repoRoot: string, filePath: string): boolean {
  const docsDir = path.join(repoRoot, "docs");
  const normalized = path.normalize(filePath);
  const prefix = path.normalize(`${docsDir}${path.sep}`);
  if (!normalized.startsWith(prefix)) return false;
  const ext = path.extname(normalized);
  return ext === ".md" || ext === ".markdown";
}

function manifestMetaForFile(
  md: string,
  f: DocsFilePath,
): { routeKey: string; title: string; segments: string[]; order?: number } {
  const routeKey = filePathToRouteKey(f);
  const { data, content } = matter(md);
  const mdast = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .parse(content) as MdastRoot;
  const title = extractTitle(mdast, routeKey);
  const order = typeof data.order === "number" ? data.order : undefined;
  return {
    routeKey,
    title,
    segments: routeKey.split("/").filter(Boolean),
    ...(order != null ? { order } : {}),
  };
}

function generateLoadPageSource(sortedRouteKeys: string[]): string {
  const cases = sortedRouteKeys
    .map((k) => {
      // Each import() must use a string literal so Vite/Rollup can analyze it
      // (see dynamic-import-vars limitations).
      const specifier = PAGE_PREFIX + encodeURIComponent(k);
      return `    case ${JSON.stringify(k)}:\n      return (await import(${JSON.stringify(specifier)})).default;`;
    })
    .join("\n");

  return `export async function loadPage(routeKey) {
  switch (routeKey) {
${cases}
    default:
      throw new Error("Unknown doc route: " + routeKey);
  }
}
`;
}

async function loadBootstrapModule(repoRoot: string): Promise<string> {
  const files = listDocMarkdownFiles(repoRoot);
  const meta: { routeKey: string; title: string; segments: string[] }[] = [];

  for (const f of files) {
    const abs = path.join(repoRoot, f);
    const md = fs.readFileSync(abs, "utf8");
    meta.push(manifestMetaForFile(md, f));
  }

  meta.sort((a, b) => a.routeKey.localeCompare(b.routeKey));
  const manifest = buildTree(meta);
  const sortedKeys = meta.map((m) => m.routeKey);

  return `export const manifest = ${JSON.stringify(manifest)};
${generateLoadPageSource(sortedKeys)}
`;
}

export function fullsendDocsPlugin(repoRoot: string): Plugin {
  return {
    name: "fullsend-docs",
    resolveId(id) {
      if (id === VIRTUAL_BOOTSTRAP_PUBLIC) return VIRTUAL_BOOTSTRAP;
      if (id.startsWith(PAGE_PREFIX)) {
        const encoded = id.slice(PAGE_PREFIX.length);
        let routeKey: string;
        try {
          routeKey = decodeURIComponent(encoded);
        } catch {
          return undefined;
        }
        return `${PAGE_INTERNAL_PREFIX}${routeKey}`;
      }
      return undefined;
    },
    async load(id) {
      if (id === VIRTUAL_BOOTSTRAP) {
        return loadBootstrapModule(repoRoot);
      }
      if (id.startsWith(PAGE_INTERNAL_PREFIX)) {
        const routeKey = id.slice(PAGE_INTERNAL_PREFIX.length);
        const rel = `docs/${routeKey}.md` as DocsFilePath;
        const abs = path.join(repoRoot, rel);
        if (!fs.existsSync(abs)) {
          return null;
        }
        const md = fs.readFileSync(abs, "utf8");
        const { title, html, frontmatter } = await markdownToHtml(
          md,
          rel,
          repoRoot,
        );
        const payload = { title, html, frontmatter };
        return `export default ${JSON.stringify(payload)};\n`;
      }
      return undefined;
    },
    configureServer(server) {
      const docsDir = path.join(repoRoot, "docs");
      if (fs.existsSync(docsDir)) {
        server.watcher.add(docsDir);
      }
    },
    handleHotUpdate({ file, server }) {
      if (!isRepoDocMarkdownFile(repoRoot, file)) return;

      const graph = server.moduleGraph;
      const hmrMods: ModuleNode[] = [];

      const bootstrap = graph.getModuleById(VIRTUAL_BOOTSTRAP);
      if (bootstrap) {
        graph.invalidateModule(bootstrap, undefined, undefined, true);
        hmrMods.push(bootstrap);
      }

      const pageIds = [...graph.idToModuleMap.keys()].filter((id) =>
        id.startsWith(PAGE_INTERNAL_PREFIX),
      );
      for (const id of pageIds) {
        const mod = graph.getModuleById(id);
        if (mod) {
          graph.invalidateModule(mod, undefined, undefined, true);
          hmrMods.push(mod);
        }
      }

      return hmrMods;
    },
  };
}
