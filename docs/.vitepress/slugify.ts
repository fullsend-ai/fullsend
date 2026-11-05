import { slug } from "github-slugger";

/**
 * Heading-anchor slug function for `markdown.anchor.slugify`.
 *
 * Uses GitHub's slug rules, the same ones lychee (`make lint-md-links`) checks
 * fragments against, so a link that resolves on GitHub also resolves on the
 * site. VitePress's default slugify differs: it prefixes a leading digit with
 * `_`, turns `'` into `-`, and collapses runs of `-`.
 *
 * This is the non-stateful `slug` export: duplicate headings on a page are
 * still made unique by markdown-it-anchor (`-1`, `-2`, ...), as VitePress does
 * by default.
 */
export function githubSlugify(text: string): string {
  return slug(text);
}
