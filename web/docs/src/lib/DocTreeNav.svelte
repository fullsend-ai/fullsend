<script lang="ts">
  import type { ManifestNode } from "virtual:fullsend-docs";
  import DocTreeNav from "./DocTreeNav.svelte";
  import { highlightSegments } from "./filterTree";
  import { formatDocHash } from "./hashRoute";
  import { navigateToRouteKey } from "./routing";

  interface Props {
    nodes: ManifestNode[];
    activeRouteKey: string;
    /**
     * Parent bumps when `sessionStorage` outline keys change (e.g. directory-link navigation).
     * Without this, `pageRouteKey` can stay the same and the tree would skip updates while session changes.
     */
    outlineSessionEpoch?: number;
    /** POSIX path segments for this level’s parent (e.g. `guides/admin`). */
    parentDirPath?: string;
    forceExpandAll?: boolean;
    filterQuery?: string;
  }

  let {
    nodes,
    activeRouteKey,
    outlineSessionEpoch = 0,
    parentDirPath = "",
    forceExpandAll = false,
    filterQuery = "",
  }: Props = $props();

  /** Bumps when a folder is toggled so `isExpanded` re-reads sessionStorage. */
  let treeEpoch = $state(0);

  function descendantMatchesActive(dirPath: string, active: string): boolean {
    if (!active) return false;
    return active === dirPath || active.startsWith(`${dirPath}/`);
  }

  function isExpanded(dirPath: string): boolean {
    void treeEpoch;
    void outlineSessionEpoch;
    const key = `fullsend-docs-tree:${dirPath}`;
    const raw = sessionStorage.getItem(key);
    if (raw === "1") return true;
    if (raw === "0") return false;
    return descendantMatchesActive(dirPath, activeRouteKey);
  }

  function toggleDir(dirPath: string): void {
    const cur = isExpanded(dirPath);
    sessionStorage.setItem(`fullsend-docs-tree:${dirPath}`, cur ? "0" : "1");
    treeEpoch++;
  }

  function childListId(dirPath: string): string {
    const safe = dirPath.replace(/[^a-zA-Z0-9_-]/g, "-");
    return `doc-tree-children-${safe}`;
  }
</script>

<ul class="doc-tree-list" role="list">
  {#each nodes as node (node.type === "file" ? node.routeKey : `dir:${parentDirPath}/${node.name}`)}
    <li class="doc-tree-item">
      {#if node.type === "dir"}
        {@const dirPath = parentDirPath ? `${parentDirPath}/${node.name}` : node.name}
        {@const expanded = forceExpandAll || isExpanded(dirPath)}
        {@const subId = childListId(dirPath)}
        <div class="doc-tree-folder" data-doc-tree-dir={dirPath}>
          <button
            type="button"
            class="doc-tree-folder-toggle"
            aria-expanded={expanded}
            aria-controls={subId}
            onclick={() => toggleDir(dirPath)}
          >
            <span
              class="doc-tree-chevron"
              class:doc-tree-chevron--open={expanded}
              aria-hidden="true"
            >
              <svg width="12" height="12" viewBox="0 0 12 12" focusable="false">
                <path
                  d="M4 1.5 L8.5 6 L4 10.5"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            </span>
            <span class="doc-tree-folder-glyph" aria-hidden="true">
              {#if expanded}
                <svg width="14" height="14" viewBox="0 0 16 16" focusable="false">
                  <path
                    fill="currentColor"
                    d="M2 4a2 2 0 0 1 2-2h3l1 1h5a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V4zm3 0v1h7V5h1V4a1 1 0 0 0-1-1H7.5l-1-1H4a1 1 0 0 0-1 1z"
                  />
                </svg>
              {:else}
                <svg width="14" height="14" viewBox="0 0 16 16" focusable="false">
                  <path
                    fill="currentColor"
                    d="M2 4a2 2 0 0 1 2-2h3l1 1h5a2 2 0 0 1 2 2v1H3V4a1 1 0 0 1 1-1zm0 3v6a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V7H2z"
                  />
                </svg>
              {/if}
            </span>
            <span class="doc-tree-folder-label">{#each highlightSegments(node.name, filterQuery) as seg, i (i)}{#if seg.highlight}<mark class="doc-tree-match">{seg.text}</mark>{:else}{seg.text}{/if}{/each}</span>
          </button>
          {#if expanded}
            <div id={subId} class="doc-tree-folder-children">
              <DocTreeNav
                nodes={node.children}
                {activeRouteKey}
                {outlineSessionEpoch}
                parentDirPath={dirPath}
                {forceExpandAll}
                {filterQuery}
              />
            </div>
          {/if}
        </div>
      {:else}
        <a
          href={formatDocHash(node.routeKey)}
          class="doc-tree-link"
          class:doc-tree-link--active={node.routeKey === activeRouteKey}
          data-doc-tree-route={node.routeKey}
          onclick={(e: MouseEvent) => { e.preventDefault(); navigateToRouteKey(node.routeKey); }}
        >
          <span class="doc-tree-chevron-slot" aria-hidden="true"></span>
          <span class="doc-tree-doc-glyph" aria-hidden="true">
            <svg width="14" height="14" viewBox="0 0 16 16" focusable="false">
              <path
                fill="currentColor"
                d="M4 1.5A1.5 1.5 0 0 0 2.5 3v10A1.5 1.5 0 0 0 4 14.5h8a1.5 1.5 0 0 0 1.5-1.5V5.207a1 1 0 0 0-.293-.707l-3.207-3.207A1 1 0 0 0 9.793 1H4zm0 1h5v3a1 1 0 0 0 1 1h3v7.5a.5.5 0 0 1-.5.5h-8a.5.5 0 0 1-.5-.5V3a.5.5 0 0 1 .5-.5z"
              />
            </svg>
          </span>
          <span class="doc-tree-link-text">{#each highlightSegments(node.title, filterQuery) as seg, i (i)}{#if seg.highlight}<mark class="doc-tree-match">{seg.text}</mark>{:else}{seg.text}{/if}{/each}</span>
        </a>
      {/if}
    </li>
  {/each}
</ul>
