---
title: Docs Anchors
---

# Docs Anchors

In-page markdown fragment links (`[text](#heading-id)` and
`[text](other.md#heading-id)`) under `docs/` are checked against **VitePress
heading ids**, not lychee's independent slug guess.

## Why this matters

lychee's `--include-fragments` mode slugifies headings with a different
algorithm than VitePress's `markdown-it-anchor` plugin (which uses
`@mdit-vue/shared` slugify). A heading such as
`config.base.yaml (vendor preset)` is:

| Tool | Fragment |
|------|----------|
| VitePress (the live docs site) | `#config-base-yaml-vendor-preset` |
| lychee `--include-fragments` | `#configbaseyaml-vendor-preset` |

Both guesses look plausible. Review and fix agents flip-flopped between them
across multiple rounds on a docs PR, even though the guide content was
correct. The docs site is built by VitePress, so VitePress is authoritative.

## Which tool is authoritative

| Check | Hook | What it validates |
|-------|------|-------------------|
| Linked file exists | `lint-md-links` (lychee, **without** `--include-fragments`) | The path in a markdown link resolves on disk, for every `.md` file in the repo |
| `#fragment` matches a heading, source under `docs/` | `lint-docs-anchors` | The fragment is a VitePress heading id, a `{#explicit-id}` attr, or an HTML `id=` in the target `docs/` page |
| `#fragment` matches a heading, source outside `docs/` | `lint-md-link-fragments` (lychee, **with** `--include-fragments`, `docs/` excluded) | The fragment matches lychee's own slug guess for the target heading |

`hack/lint-docs-anchors` reimplements the `@mdit-vue/shared` slugify function
used by the pinned VitePress version in `package.json`. If the two ever
diverge after a VitePress upgrade, update the reimplementation and its tests
in `hack/lint-docs-anchors-test.py`.

Do **not** change a fragment to match lychee when `lint-docs-anchors` rejects
it. The hook prints the VitePress id to use.

### The authority boundary

The split above is by the **linking file's** location, not the target's:

- A `docs/**/*.md` file linking to any `#fragment` (in itself or another
  `docs/` page) is checked by `lint-docs-anchors` against the VitePress id.
- A file outside `docs/` (root `.md` files, `images/`, `skills/`, etc.)
  linking to a `#fragment` is checked by `lint-md-link-fragments` using
  lychee's own slug guess — even when the link's target is a `docs/` page.
  For headings made only of plain words this agrees with the VitePress id
  (for example `## Running the fullsend CLI` → `#running-the-fullsend-cli`
  either way), but for a heading with punctuation the two guesses can
  diverge (see the table above). Prefer linking to plain-word headings, or
  an explicit `{#id}`, from outside `docs/`.
- Neither hook currently re-validates a `docs/` page's own fragment when it
  is targeted from outside `docs/` using the VitePress algorithm; that
  cross-boundary case relies on the heading being simple enough that both
  slugifiers agree.

## Writing heading links

1. Use the slug VitePress generates. Punctuation becomes a hyphen, and
   consecutive punctuation collapses to one hyphen:
   `config.base.yaml (vendor preset)` → `config-base-yaml-vendor-preset`.
   This only replaces ASCII punctuation and curly quotes — other Unicode
   punctuation (em dash `—`, en dash `–`, etc.) is **not** replaced and is
   preserved literally in the id: `Event semantics — input only` →
   `event-semantics-—-input-only`. When in doubt, run
   `./hack/lint-docs-anchors` and use the id it prints rather than guessing.
2. Headings that start with a digit get a `_` prefix:
   `1. Webhook + dispatch service` → `_1-webhook-dispatch-service`.
3. To pin an id (for example when a heading is likely to be renamed, or when
   you want a stable target that is not the generated slug), add
   `{#my-stable-id}` at the end of the heading or an HTML
   `<a id="my-stable-id"></a>` immediately before it.

## Running locally

```bash
./hack/lint-docs-anchors
# or, file links + fragments together:
make lint-md-links
```

The pre-commit hook `lint-docs-anchors` scans the whole `docs/` tree whenever
any `docs/**/*.md` file is staged, so a heading rename is still caught if
another page still points at the old slug. `make lint-all` does the same.
