import fs from "node:fs";
import path from "node:path";

/**
 * Copy each VitePress page's markdown source into the build output next to the
 * HTML VitePress already wrote. Rewrites (README.md → index.md) are applied so
 * the .md URL matches the HTML URL plus `.md`.
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
}): void {
  const copyFile = opts.copyFile ?? ((src, dest) => fs.copyFileSync(src, dest));
  const mkdir = opts.mkdir ?? ((dir) => fs.mkdirSync(dir, { recursive: true }));
  const exists = opts.exists ?? ((file) => fs.existsSync(file));

  let copied = 0;
  for (const page of opts.pages) {
    const destRel = opts.rewrites[page] ?? page;
    const src = path.join(opts.srcDir, page);
    if (!exists(src)) continue;
    const dest = path.join(opts.outDir, destRel);
    mkdir(path.dirname(dest));
    copyFile(src, dest);
    copied++;
  }
  if (copied === 0) {
    throw new Error(
      `copyMarkdownSources copied 0 markdown files from ${opts.srcDir} to ${opts.outDir} (${opts.pages.length} pages)`,
    );
  }
  console.log(
    `copyMarkdownSources copied ${copied} markdown files from ${opts.srcDir} to ${opts.outDir} (${opts.pages.length} pages)`,
  );
}
