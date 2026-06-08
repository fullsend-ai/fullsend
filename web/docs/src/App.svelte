<script lang="ts">
  import { onMount, tick } from "svelte";
  import type { DocPagePayload } from "virtual:fullsend-docs";
  import { manifest, loadPage } from "virtual:fullsend-docs";
  import { collectRouteKeys } from "./lib/manifestRouteKeys";
  import { collectDirPaths } from "./lib/manifestDirs";
  import { formatDocHash, parseDocHash } from "./lib/hashRoute";
  import {
    defaultRouteKeyFromKeys,
    legacyPathnameDocRest,
    navigateToRouteKey,
    persistLastDocRouteKey,
  } from "./lib/routing";
  import { bfsFirstRouteKeyUnderDir } from "./lib/manifestBfsDefault";
  import { persistExpandedPathForRouteKey } from "./lib/treeSession";
  import { DocTreeNav } from "./lib/tree";
  import { filterTree } from "./lib/filterTree";

  const NAV_COLLAPSED_KEY = "fullsend-docs-nav-collapsed";
  const WIDTH_STORAGE_KEY = "fullsend-docs-sidebar-width-px";

  const routeKeys = new Set(collectRouteKeys(manifest));
  const dirPaths = collectDirPaths(manifest);

  let shellEl: HTMLDivElement | null = $state(null);
  let mainEl: HTMLElement | null = $state(null);
  let pageRouteKey = $state("");
  let slug = $state<string | undefined>(undefined);
  /** Next file-branch outline sync should open the sidebar (directory URL or in-doc directory link). */
  let directoryOutlineReveal = $state(false);
  let page = $state<DocPagePayload | null>(null);
  let loading = $state(false);
  let navCollapsed = $state(false);
  let mobileNavOpen = $state(false);
  let narrowViewport = $state(false);
  /** Bumps when outline session keys change outside the tree (e.g. directory hash); keeps DocTreeNav in sync with sessionStorage. */
  let outlineSessionEpoch = $state(0);
  let filterQuery = $state("");
  let filteredManifest = $derived(filterTree(manifest, filterQuery));

  let outlineExpanded = $derived(
    narrowViewport ? mobileNavOpen : !navCollapsed,
  );

  let hamburgerLabel = $derived(
    !narrowViewport && !navCollapsed
      ? "Outline open"
      : narrowViewport && mobileNavOpen
        ? "Close documentation outline"
        : "Open documentation outline",
  );

  function getRemPx(): number {
    if (typeof window === "undefined") return 16;
    const n = parseFloat(getComputedStyle(document.documentElement).fontSize);
    return Number.isFinite(n) ? n : 16;
  }

  function clampSidebarWidthPx(px: number): number {
    const vw = typeof window !== "undefined" ? window.innerWidth : 1024;
    const minPx = getRemPx() * 15;
    const maxPx = Math.floor(vw * 0.5);
    return Math.min(maxPx, Math.max(minPx, Math.round(px)));
  }

  function applySidebarWidthPx(px: number): void {
    if (!shellEl) return;
    const w = clampSidebarWidthPx(px);
    shellEl.style.setProperty("--docs-sidebar-width", `${w}px`);
  }

  function syncOutlineForActiveRoute(routeKey: string, revealChrome: boolean): void {
    persistExpandedPathForRouteKey(routeKey, dirPaths);
    outlineSessionEpoch++;
    if (revealChrome) {
      persistNavCollapsed(false);
      if (narrowViewport) {
        mobileNavOpen = true;
      }
    }
    const sel = `[data-doc-tree-route="${CSS.escape(routeKey)}"]`;
    const tryScroll = (): boolean => {
      const el = document.querySelector(sel);
      if (el) {
        el.scrollIntoView({ block: "nearest" });
        return true;
      }
      return false;
    };
    void tick().then(() =>
      requestAnimationFrame(() => {
        if (!tryScroll()) {
          requestAnimationFrame(() => {
            tryScroll();
          });
        }
      }),
    );
  }

  /** Directory hash links in the article: go to BFS default page and sync outline (hash may not change). */
  function onDocMainClick(e: MouseEvent): void {
    if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
    if (e.button !== 0) return;
    const el = e.target;
    if (!(el instanceof Element)) return;
    if (!mainEl) return;
    const a = el.closest("a[href]");
    if (!a || !mainEl.contains(a)) return;
    const hrefAttr = a.getAttribute("href");
    if (hrefAttr === null || !hrefAttr.startsWith("#/")) return;
    const parsed = parseDocHash(hrefAttr);
    if (parsed?.kind !== "dir" || !dirPaths.has(parsed.dirPath)) return;

    e.preventDefault();
    const defaultKey = defaultRouteKeyFromKeys([...routeKeys]);
    let targetKey = bfsFirstRouteKeyUnderDir(manifest, parsed.dirPath);
    if (targetKey === null || !routeKeys.has(targetKey)) {
      targetKey = defaultKey;
    }
    if (targetKey === null) return;

    const loc = parseDocHash(window.location.hash);
    const already =
      loc?.kind === "file" &&
      loc.routeKey === targetKey &&
      !loc.slug;

    if (already) {
      syncOutlineForActiveRoute(targetKey, true);
      return;
    }
    directoryOutlineReveal = true;
    navigateToRouteKey(targetKey);
  }

  function syncRouteFromLocation(): void {
    const legacy = legacyPathnameDocRest();
    if (legacy !== null) {
      const u = new URL(window.location.href);
      u.pathname = "/docs/";
      u.hash = formatDocHash(legacy);
      location.replace(u.toString());
      return;
    }

    const parsed = parseDocHash(window.location.hash);
    const defaultKey = defaultRouteKeyFromKeys([...routeKeys]);

    if (routeKeys.size === 0) {
      pageRouteKey = "";
      slug = undefined;
      return;
    }

    if (parsed === null) {
      if (defaultKey !== null) {
        navigateToRouteKey(defaultKey, { replace: true });
        pageRouteKey = defaultKey;
        slug = undefined;
        persistLastDocRouteKey(defaultKey);
      }
      return;
    }

    if (parsed.kind === "dir") {
      if (!dirPaths.has(parsed.dirPath)) {
        if (defaultKey !== null) {
          navigateToRouteKey(defaultKey, { replace: true });
          pageRouteKey = defaultKey;
          slug = undefined;
          persistLastDocRouteKey(defaultKey);
        }
        return;
      }
      directoryOutlineReveal = true;
      let resolved = bfsFirstRouteKeyUnderDir(manifest, parsed.dirPath);
      if (resolved === null || !routeKeys.has(resolved)) {
        resolved = defaultKey;
      }
      if (resolved === null) {
        pageRouteKey = "";
        slug = undefined;
        return;
      }
      persistLastDocRouteKey(resolved);
      pageRouteKey = resolved;
      slug = undefined;
      navigateToRouteKey(resolved, { replace: true });
      return;
    }

    if (!routeKeys.has(parsed.routeKey)) {
      if (defaultKey !== null) {
        navigateToRouteKey(defaultKey, { replace: true });
        pageRouteKey = defaultKey;
        slug = undefined;
        persistLastDocRouteKey(defaultKey);
      }
      return;
    }

    pageRouteKey = parsed.routeKey;
    slug = parsed.slug;
    persistLastDocRouteKey(parsed.routeKey);
    const reveal = directoryOutlineReveal;
    directoryOutlineReveal = false;
    syncOutlineForActiveRoute(parsed.routeKey, reveal);
  }

  async function runMermaid(): Promise<void> {
    await tick();
    try {
      if (!document.querySelector(".doc-body pre.mermaid-doc")) return;
      const m = await import("mermaid");
      m.default.initialize({ startOnLoad: false, securityLevel: "strict" });
      await m.default.run({ querySelector: ".doc-body pre.mermaid-doc" });
    } catch {
      /* empty graph or mermaid internal error — ignore */
    }
  }

  let resizeActive = false;
  let resizeStartX = 0;
  let resizeStartWidth = 0;

  function readSidebarWidthPx(): number {
    if (!shellEl) return clampSidebarWidthPx(getRemPx() * 15);
    const v = getComputedStyle(shellEl).getPropertyValue("--docs-sidebar-width");
    const m = /^([\d.]+)px$/.exec(v.trim());
    if (m) return parseFloat(m[1]!);
    const m2 = /^([\d.]+)rem$/.exec(v.trim());
    if (m2) return parseFloat(m2[1]!) * getRemPx();
    return clampSidebarWidthPx(getRemPx() * 15);
  }

  function onResizeHandleDown(e: MouseEvent): void {
    if (narrowViewport || navCollapsed) return;
    e.preventDefault();
    resizeActive = true;
    resizeStartX = e.clientX;
    resizeStartWidth = readSidebarWidthPx();
    window.addEventListener("mousemove", onResizeMove);
    window.addEventListener("mouseup", onResizeUp);
  }

  function onResizeMove(e: MouseEvent): void {
    if (!resizeActive || !shellEl) return;
    const delta = e.clientX - resizeStartX;
    applySidebarWidthPx(resizeStartWidth + delta);
  }

  function onResizeUp(): void {
    if (!resizeActive) return;
    resizeActive = false;
    window.removeEventListener("mousemove", onResizeMove);
    window.removeEventListener("mouseup", onResizeUp);
    try {
      localStorage.setItem(WIDTH_STORAGE_KEY, String(readSidebarWidthPx()));
    } catch {
      /* ignore */
    }
  }

  onMount(() => {
    navCollapsed = localStorage.getItem(NAV_COLLAPSED_KEY) === "1";

    const mq = window.matchMedia("(max-width: 768px)");
    const syncNarrow = () => {
      narrowViewport = mq.matches;
    };
    syncNarrow();
    mq.addEventListener("change", syncNarrow);

    const rawW = localStorage.getItem(WIDTH_STORAGE_KEY);
    let initial: number;
    if (rawW) {
      const n = parseInt(rawW, 10);
      initial = Number.isFinite(n)
        ? n
        : Math.max(window.innerWidth * 0.2, getRemPx() * 15);
    } else {
      initial = Math.max(window.innerWidth * 0.2, getRemPx() * 15);
    }
    tick().then(() => applySidebarWidthPx(initial));

    const onReclampWidth = () => applySidebarWidthPx(readSidebarWidthPx());
    window.addEventListener("resize", onReclampWidth);

    syncRouteFromLocation();

    const onHashOrPop = () => syncRouteFromLocation();
    window.addEventListener("hashchange", onHashOrPop);
    window.addEventListener("popstate", onHashOrPop);
    return () => {
      mq.removeEventListener("change", syncNarrow);
      window.removeEventListener("resize", onReclampWidth);
      window.removeEventListener("hashchange", onHashOrPop);
      window.removeEventListener("popstate", onHashOrPop);
      window.removeEventListener("mousemove", onResizeMove);
      window.removeEventListener("mouseup", onResizeUp);
    };
  });

  $effect(() => {
    const m = mainEl;
    if (!m) return;
    const handler = (e: MouseEvent) => onDocMainClick(e);
    m.addEventListener("click", handler);
    return () => m.removeEventListener("click", handler);
  });

  $effect(() => {
    const key = pageRouteKey;
    if (!key || !routeKeys.has(key)) {
      page = null;
      loading = false;
      return;
    }

    loading = true;

    void loadPage(key)
      .then((p) => {
        if (pageRouteKey === key) {
          page = p;
        }
      })
      .catch(() => {
        if (pageRouteKey === key) {
          page = null;
          const dk = defaultRouteKeyFromKeys([...routeKeys]);
          if (dk !== null) {
            navigateToRouteKey(dk, { replace: true });
          }
        }
      })
      .finally(() => {
        if (pageRouteKey === key) {
          loading = false;
        }
      });
  });

  $effect(() => {
    if (!page?.html) return;
    void runMermaid();
  });

  $effect(() => {
    const s = slug;
    const html = page?.html;
    if (!s || !html) return;
    void tick().then(() => {
      const body = document.querySelector(".doc-body");
      if (!body) return;
      const el = document.getElementById(s);
      if (el && body.contains(el)) {
        el.scrollIntoView();
      }
    });
  });

  function persistNavCollapsed(collapsed: boolean): void {
    navCollapsed = collapsed;
    localStorage.setItem(NAV_COLLAPSED_KEY, collapsed ? "1" : "0");
  }

  function onHamburgerClick(): void {
    if (narrowViewport) {
      mobileNavOpen = !mobileNavOpen;
      if (mobileNavOpen) {
        persistNavCollapsed(false);
      }
      return;
    }
    if (navCollapsed) {
      persistNavCollapsed(false);
    }
  }

  function closeOutlineDesktop(): void {
    persistNavCollapsed(true);
  }

  function closeOutlineMobile(): void {
    mobileNavOpen = false;
  }
</script>

<div
  bind:this={shellEl}
  class="docs-shell"
  class:docs-shell--nav-collapsed={navCollapsed}
  class:docs-shell--mobile-nav-open={mobileNavOpen}
>
  <div class="docs-shell-inner">
    <aside class="docs-sidebar" id="docs-sidebar" aria-label="Documentation outline">
      <div class="docs-chrome-row docs-sidebar-header">
        <span class="docs-sidebar-title">Outline</span>
        <button
          type="button"
          class="docs-icon-btn docs-sidebar-close docs-sidebar-close--desktop"
          aria-label="Close outline"
          onclick={closeOutlineDesktop}
        >
          <svg width="18" height="18" viewBox="0 0 24 24" focusable="false" aria-hidden="true">
            <path
              fill="currentColor"
              d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"
            />
          </svg>
        </button>
        <button
          type="button"
          class="docs-icon-btn docs-sidebar-close docs-sidebar-close--mobile"
          aria-label="Close outline"
          onclick={closeOutlineMobile}
        >
          <svg width="18" height="18" viewBox="0 0 24 24" focusable="false" aria-hidden="true">
            <path
              fill="currentColor"
              d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"
            />
          </svg>
        </button>
      </div>
      <div class="docs-tree-filter-wrap">
        <svg class="docs-tree-filter-icon" width="14" height="14" viewBox="0 0 16 16" focusable="false" aria-hidden="true">
          <path fill="currentColor" d="M11.5 7a4.5 4.5 0 1 1-9 0 4.5 4.5 0 0 1 9 0Zm-.82 4.74a6 6 0 1 1 1.06-1.06l3.04 3.04a.75.75 0 1 1-1.06 1.06l-3.04-3.04Z"/>
        </svg>
        <input
          class="docs-tree-filter"
          type="text"
          placeholder="Filter docs…"
          aria-label="Filter documents"
          bind:value={filterQuery}
        />
        {#if filterQuery}
          <button
            type="button"
            class="docs-tree-filter-clear"
            aria-label="Clear filter"
            onclick={() => filterQuery = ""}
          >
            <svg width="16" height="16" viewBox="0 0 16 16" focusable="false" aria-hidden="true">
              <path fill="currentColor" d="M3.72 3.72a.75.75 0 0 1 1.06 0L8 6.94l3.22-3.22a.75.75 0 1 1 1.06 1.06L9.06 8l3.22 3.22a.75.75 0 1 1-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 0 1-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 0 1 0-1.06Z"/>
            </svg>
          </button>
        {/if}
      </div>
      <nav class="docs-tree-wrap">
        <DocTreeNav
          nodes={filteredManifest}
          activeRouteKey={pageRouteKey}
          outlineSessionEpoch={outlineSessionEpoch}
          forceExpandAll={filterQuery.trim().length > 0}
          {filterQuery}
        />
      </nav>
    </aside>

    <button
      type="button"
      class="docs-sidebar-resize-handle"
      aria-label="Resize outline panel"
      onmousedown={onResizeHandleDown}
    ></button>

    <div class="docs-content-column">
      <header class="docs-chrome-row docs-topbar">
        <button
          type="button"
          class="docs-icon-btn docs-hamburger"
          aria-controls="docs-sidebar"
          aria-expanded={outlineExpanded}
          aria-label={hamburgerLabel}
          disabled={!narrowViewport && !navCollapsed}
          onclick={onHamburgerClick}
        >
          <svg width="20" height="20" viewBox="0 0 24 24" focusable="false" aria-hidden="true">
            <path
              fill="currentColor"
              d="M4 6h16v2H4V6zm0 5h16v2H4v-2zm0 5h16v2H4v-2z"
            />
          </svg>
        </button>
        <a class="docs-brand" href="/">Fullsend</a>
      </header>

      <div class="docs-main-wrap">
        <main class="docs-main" bind:this={mainEl}>
          {#if pageRouteKey && page}
            <article
              class="doc-body"
              data-frontmatter={JSON.stringify(page.frontmatter)}
            >
              {@html page.html}
            </article>
          {:else if pageRouteKey && loading}
            <article class="doc-body doc-body--empty">
              <p>Loading…</p>
            </article>
          {:else}
            <article class="doc-body doc-body--empty">
              <p>No documentation pages were found.</p>
            </article>
          {/if}
        </main>
      </div>
    </div>
  </div>
</div>
