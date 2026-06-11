# Design: Directory links resolve to BFS default document

Date: 2026-05-05
Status: Approved (supersedes conflicting behavior in [2026-05-05-docs-nav-layout-and-directory-hash-design.md](./2026-05-05-docs-nav-layout-and-directory-hash-design.md))

## Relationship to prior specs

- [2026-05-04-docs-browser-design.md](./2026-05-04-docs-browser-design.md) — SPA boundaries unchanged.
- [2026-05-05-docs-browser-enhancements-design.md](./2026-05-05-docs-browser-enhancements-design.md) — hash routing baseline; this doc refines **folder / directory** behavior.
- [2026-05-05-docs-nav-layout-and-directory-hash-design.md](./2026-05-05-docs-nav-layout-and-directory-hash-design.md) — layout, resize, tree sort, and **`#/<dirPath>/` syntax** remain in force. **Article behavior for directory targets changes:** the earlier “option C” rule (*directory hash does not change the article; keep last document*) is **replaced** by this document. Implementation and tests that assumed option C should be updated to match here.

## Problem

Directory targets (`#/<dirPath>/`) were easy to treat as a separate “location” from the article, which led to confusing UX (e.g. outline state and hash getting out of sync with user expectations, or a second activation doing nothing useful). We want one clear rule for **folder links** and **cold-open directory URLs**.

## Goals

1. **Single meaning for directory navigation:** Any activation that targets a **directory** (valid `#/<dirPath>/` from in-doc links or cold load) **navigates to a concrete default document** under that directory and updates the visible article and **document** hash accordingly.
2. **Default document selection:** Use **breadth-first** ordering over the manifest subtree so **shallower** pages win over **deeper** ones (avoid DFS/outline order preferring a nested file over a sibling file at a higher level).
3. **Outline sync:** After directory resolution (including when the resolved route is **already** the current page), **expand** ancestors of the active document, **scroll** the active entry into view in the tree, and **open** the outline if it was collapsed (desktop) or closed (mobile drawer).
4. **Sidebar folders:** Remain **expand/collapse only**; they do **not** navigate to the default document. Only **in-document links** to `#/<dirPath>/` and **cold** loads with that hash perform “go to default.”

## Non-goals

- Synthetic **index pages** listing directory contents in the article pane.
- **Slug fragments** on directory-only URLs (`#/<dir>/::...`) — remain invalid; normalize or fall back like other bad hashes.
- Changing **authentication**, **search**, or **SSR**.

## Default document (BFS)

**Input:** Valid directory path `dirPath` present in the manifest tree.

**Child ordering** at each node matches the nav: **subdirectories first**, then **files**, each group ordered consistently with the existing tree (locale-aware name sort, same as manifest build).

**Algorithm:**

1. Initialize a queue with the **direct children** of `dirPath` in that order.
2. While the queue is non-empty, dequeue the front node:
   - If it is a **file**, its `routeKey` is the default; stop.
   - If it is a **directory**, enqueue its **children** (same ordering) at the **back** of the queue.

**No file found** (empty or odd tree): use the same **global default document** policy as for other invalid or empty entry URLs.

## URL and routing

- **Incoming `#/<dirPath>/`:** Resolve default route, set `pageRouteKey`, persist last-doc key, then normalize the hash to **`#/<routeKey>`** (or `::slug` only when applicable to **file** routes — never for directory-only entry). Prefer **`replaceState`**-style navigation for this normalization so the history stack does not retain a useless “directory-only” step unless product reasons dictate otherwise.
- **Stable in-doc links:** Markdown may continue to emit **`#/<dirPath>/`** for folder targets; resolution happens at runtime (avoids duplicating BFS logic in the build).

## Outline sync (after directory resolution)

Whenever directory resolution completes (including **idempotent** cases where the user was already on the default document):

- Persist expanded state for the **ancestor path** of the active document so the tree can show it (same family of behavior as today’s `persistExpandedPathInSession`, possibly keyed by **file** path segments rather than only “directory focus”).
- Scroll the **active file** row into view; if the implementation scrolls to a folder row for directory entry, align with showing the **document** row after normalization.
- Open the outline if hidden (desktop collapsed sidebar or mobile drawer closed).

**Implementation note:** If the hash does not change on second activation, the app must still run this sync (do not rely solely on `hashchange`).

## Sidebar interaction

- **Folder rows:** Toggling expand/collapse is unchanged; **no** navigation to the default document from the folder label or row (except any future explicit control not covered here).
- **File rows:** Continue to navigate to the selected document; outline sync should leave the destination visible as today or better.

## Edge cases

| Case | Behavior |
|------|----------|
| User follows directory link while already on that folder’s BFS default | Normalize hash if needed; **always** run outline sync (expand, scroll, open nav). |
| User on `child.md` follows link to **parent** directory | Navigate to parent’s BFS default (may differ from `child.md`). |
| Invalid / unknown `dirPath` | Same recovery as unknown file key (e.g. default document), without wedging the app. |
| Cold load `#/<dir>/` | Expand tree toward destination document after resolve; article shows default file; URL becomes file hash. |

## Testing

- **Unit:** BFS default helper — sibling file shallower than nested file wins; empty directory; single nested file; ordering with multiple subdirs.
- **Unit:** Hash parse/format unchanged for directory fragments; normalization path tested if factored.
- **Manual:** In-doc directory link from article; collapse folder and activate link again; cold load `#/<dir>/`; sidebar folder still only toggles; mobile drawer opens when resolving from hidden state.

## Open questions (implementation)

- Whether **every** file navigation (e.g. sidebar file click) should call the same **outline sync** helper as directory resolution for consistency (desirable; exact deduplication with existing `activeRouteKey` expansion logic TBD).
- Exact **scroll** target (file row vs folder) once URL is always a file route after resolve.

## References

- App shell: [`web/docs/src/App.svelte`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/App.svelte)
- Tree: [`web/docs/src/lib/DocTreeNav.svelte`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/lib/DocTreeNav.svelte)
- Hash helpers: [`web/docs/src/lib/hashRoute.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/lib/hashRoute.ts)
- Tree session: [`web/docs/src/lib/treeSession.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/lib/treeSession.ts)
