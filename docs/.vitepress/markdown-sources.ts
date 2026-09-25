import fs from "node:fs";
import path from "node:path";

/**
 * Copy each VitePress page's markdown source into the build output next to the
 * HTML VitePress already wrote. Rewrites (README.md → index.md) are applied so
 * the .md URL matches the HTML URL plus `.md`. When a rewrite changes the
 * relative path, the source is also written at the original path so in-document
 * `README.md` links still resolve (the copy is verbatim; links are not rewritten).
 *
 * Relative image files those pages reference (`![](...)` and `<img src>`) are
 * copied to the same location under outDir, including assets VitePress excludes
 * from `siteConfig.pages` (for example `docs/agents/icons/`).
 *
 * Pages whose source file is missing (for example a generated 404) are skipped.
 * The build fails if every page is skipped, so a broken srcDir/outDir/rewrites
 * wiring cannot ship a site with no .md files.
 */
export function copyMarkdownSources(opts: {
  pages: string[];
  srcDir: string;
  outDir: string;
  rewrites: Record<string, string | undefined>;
  copyFile?: (src: string, dest: string) => void;
  mkdir?: (dir: string) => void;
  exists?: (file: string) => boolean;
  readFile?: (file: string) => string;
}): void {
  const copyFile = opts.copyFile ?? ((src, dest) => fs.copyFileSync(src, dest));
  const mkdir = opts.mkdir ?? ((dir) => fs.mkdirSync(dir, { recursive: true }));
  const exists = opts.exists ?? ((file) => fs.existsSync(file));
  const readFile = opts.readFile ?? ((file) => fs.readFileSync(file, "utf8"));

  const srcRoot = path.resolve(opts.srcDir);
  const copiedAssets = new Set<string>();
  let copied = 0;
  for (const page of opts.pages) {
    const destRel = opts.rewrites[page] ?? page;
    const src = path.join(opts.srcDir, page);
    if (!exists(src)) continue;
    const dest = path.join(opts.outDir, destRel);
    mkdir(path.dirname(dest));
    copyFile(src, dest);
    copied++;
    if (destRel !== page) {
      const originalDest = path.join(opts.outDir, page);
      mkdir(path.dirname(originalDest));
      copyFile(src, originalDest);
      copied++;
    }
    copyRelativeAssets({
      content: readFile(src),
      pageDir: path.dirname(src),
      srcRoot,
      outDir: opts.outDir,
      copiedAssets,
      copyFile,
      mkdir,
      exists,
    });
  }
  if (copied === 0) {
    throw new Error(
      `copyMarkdownSources copied 0 markdown files from ${opts.srcDir} to ${opts.outDir} (${opts.pages.length} pages)`,
    );
  }
  console.log(
    `copyMarkdownSources copied ${copied} markdown files and ${copiedAssets.size} assets from ${opts.srcDir} to ${opts.outDir} (${opts.pages.length} pages)`,
  );
}

function copyRelativeAssets(opts: {
  content: string;
  pageDir: string;
  srcRoot: string;
  outDir: string;
  copiedAssets: Set<string>;
  copyFile: (src: string, dest: string) => void;
  mkdir: (dir: string) => void;
  exists: (file: string) => boolean;
}): void {
  for (const href of relativeAssetHrefs(opts.content)) {
    const assetSrc = path.resolve(opts.pageDir, href);
    if (!isInside(opts.srcRoot, assetSrc) || !opts.exists(assetSrc)) continue;
    const assetDest = path.join(opts.outDir, path.relative(opts.srcRoot, assetSrc));
    if (opts.copiedAssets.has(assetDest)) continue;
    opts.copiedAssets.add(assetDest);
    opts.mkdir(path.dirname(assetDest));
    opts.copyFile(assetSrc, assetDest);
  }
}

/** Local `![](...)` and `<img src>` targets, excluding URLs and site-absolute paths. */
export function relativeAssetHrefs(src: string): string[] {
  const hrefs: string[] = [];
  const markdownImage =
    /!\[[^\]]*\]\(\s*(?:<([^>\n]+)>|(\S+?))(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?\s*\)/g;
  for (const match of src.matchAll(markdownImage)) {
    hrefs.push(match[1] ?? match[2] ?? "");
  }
  const htmlImage = /<img\b[^>]*?\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/gi;
  for (const match of src.matchAll(htmlImage)) {
    hrefs.push(match[1] ?? match[2] ?? match[3] ?? "");
  }
  return hrefs
    .map((href) => href.trim().replace(/[?#].*$/, ""))
    .filter((href) => href !== "" && isRelativeAssetHref(href));
}

function isRelativeAssetHref(href: string): boolean {
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) return false;
  if (href.startsWith("//") || href.startsWith("#") || href.startsWith("/")) return false;
  return true;
}

function isInside(root: string, file: string): boolean {
  const rel = path.relative(root, file);
  return rel !== "" && !rel.startsWith("..") && !path.isAbsolute(rel);
}
