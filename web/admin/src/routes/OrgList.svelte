<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { githubUser } from "../lib/auth/session";
  import { loadGithubAppSlug, loadToken } from "../lib/auth/tokenStore";
  import { createUserOctokit } from "../lib/github/client";
  import { githubAppInstallationsNewUrl } from "../lib/github/githubAppInstallLink";
  import { readTokenScopesHeaderCached } from "../lib/layers/preflight";
  import {
    FetchOrgsError,
    fetchOrgsWithProgress,
    INSTALLATIONS_PER_PAGE,
    MAX_INSTALLATION_LIST_PAGES,
  } from "../lib/orgs/fetchOrgs";
  import { batchOrganizationsFullsendRepoExists } from "../lib/orgs/batchOrganizationsFullsendRepoGraphql";
  import { filterOrgsBySearch, type OrgRow } from "../lib/orgs/filter";
  import { resolveOrgListDeployRowCluster } from "../lib/orgs/installReadinessProbes";
  import {
    analyzeOrgForOrgList,
    buildDeployPreflight,
    type OrgListRowCluster,
  } from "../lib/orgs/orgListRow";
  import {
    clearOrgListAnalysisCache,
    getOrgListAnalysisCached,
    hasOrgListAnalysisCacheEntry,
    invalidateOrgListAnalysisCacheEntry,
    setOrgListAnalysisCached,
  } from "../lib/orgs/orgListAnalysisCache";
  import type { Octokit } from "@octokit/rest";

  /** Delays after an empty install list response for silent GitHub API rechecks (ms). */
  const EMPTY_LIST_RECHECK_DELAYS_MS = [14_000, 32_000, 55_000] as const;

  /** Abort stuck installation-list fetches so Refresh never stays disabled indefinitely. */
  const ORG_LIST_FETCH_TIMEOUT_MS = 60_000;

  type LoadOrgsOpts = {
    allowEmptyFollowUpPoll?: boolean;
    internalPoll?: boolean;
  };

  /** Max visible rows after filter (matches UX spec). */
  const DISPLAY_CAP = 15;
  const BATCH_FIRST = 10;
  const BATCH_INCREMENT = 5;

  let serverOrgs = $state<OrgRow[]>([]);
  let displayedOrgs = $state<OrgRow[]>([]);
  let search = $state("");
  let loading = $state(false);
  let error = $state<string | null>(null);
  let emptyHint = $state<string | null>(null);
  let resolvedAppSlug = $state<string | null>(null);
  let installationListTruncated = $state(false);

  let hasCompletedOrgFetchOnce = $state(false);
  let inlineListRefresh = $state(false);
  let listCheckAt = $state<number | null>(null);
  let pollSession = 0;
  let pollTimeouts: number[] = [];

  function clearPollTimeouts(): void {
    for (const id of pollTimeouts) clearTimeout(id);
    pollTimeouts = [];
  }

  function scheduleEmptyListRechecks(): void {
    const sid = pollSession;
    for (const delayMs of EMPTY_LIST_RECHECK_DELAYS_MS) {
      const id = window.setTimeout(() => {
        if (pollSession !== sid) return;
        void loadOrgs(true, { internalPoll: true });
      }, delayMs);
      pollTimeouts.push(id);
    }
  }

  /** Batched updates while the installation list fetch is still running (unfiltered growth from `onProgress`). */
  function commitDisplayedRowsFromScan(capped: OrgRow[], done: boolean): void {
    if (done) {
      displayedOrgs = capped;
      return;
    }
    const c = capped.length;
    const d = displayedOrgs.length;

    if (c <= BATCH_FIRST) {
      displayedOrgs = capped;
      return;
    }
    if (c >= DISPLAY_CAP) {
      displayedOrgs = capped;
      return;
    }
    if (d < BATCH_FIRST) {
      displayedOrgs = capped.slice(0, BATCH_FIRST);
      return;
    }
    if (c >= d + BATCH_INCREMENT) {
      displayedOrgs = capped;
    }
  }

  function applySearchFilterDisplay(): void {
    displayedOrgs = filterOrgsBySearch(serverOrgs, search).slice(0, DISPLAY_CAP);
  }

  let loadGeneration = 0;
  let loadAbort: AbortController | null = null;

  type RowUiEntry = OrgListRowCluster | "pending";

  let rowUi = $state<Record<string, RowUiEntry>>({});
  let rowEvalGen = 0;

  async function readDeployPreflightOrSkipped(octokit: Octokit, accessToken: string) {
    try {
      return buildDeployPreflight(await readTokenScopesHeaderCached(octokit, accessToken));
    } catch {
      return buildDeployPreflight(null);
    }
  }

  async function refreshOrgRow(login: string): Promise<void> {
    const token = loadToken()?.accessToken;
    if (!token) return;
    invalidateOrgListAnalysisCacheEntry(login);
    rowUi = { ...rowUi, [login]: "pending" };
    try {
      const octokit = createUserOctokit(token);
      const deployPreflight = await readDeployPreflightOrSkipped(octokit, token);
      const hints = await batchOrganizationsFullsendRepoExists(octokit, [login]);
      const hint = hints.get(login.trim().toLowerCase()) ?? null;
      const res = await analyzeOrgForOrgList(login, octokit, {
        fullsendRepoExistsHint: hint,
      });
      if (res.kind === "ok") {
        setOrgListAnalysisCached(login, res);
      }
      rowUi = {
        ...rowUi,
        [login]: await resolveOrgListDeployRowCluster(
          res,
          deployPreflight,
          octokit,
          get(githubUser)?.login ?? "",
          login,
        ),
      };
    } catch (e) {
      rowUi = {
        ...rowUi,
        [login]: {
          kind: "error",
          message: e instanceof Error ? e.message : "Failed to evaluate organisation.",
        },
      };
    }
  }

  function cannotDeployPopoverId(login: string): string {
    return `cd-${login.replace(/[^a-zA-Z0-9_-]/g, "_")}`;
  }

  function rowErrPopoverId(login: string): string {
    return `re-${login.replace(/[^a-zA-Z0-9_-]/g, "_")}`;
  }

  /** Debounced: filter changes must not re-hit GitHub on every keystroke. */
  const ROW_ANALYSIS_DEBOUNCE_MS = 280;
  const ORG_ROW_ANALYSIS_THROTTLE_MS = 400;

  /**
   * Background org analysis: visible (filtered) rows first, then the rest of the install list
   * so what the user sees resolves before off-screen orgs.
   */
  $effect(() => {
    const visible = displayedOrgs;
    const all = serverOrgs;
    const token = loadToken()?.accessToken;
    const user = $githubUser;
    const busyInitialLoad = loading && all.length === 0;

    if (!token || !user) {
      rowUi = {};
      return;
    }
    if (visible.length === 0 || busyInitialLoad) {
      return;
    }

    let alive = true;
    const gen = (rowEvalGen += 1);
    const octokit = createUserOctokit(token);

    const visibleLogins = visible.map((o) => o.login);
    const visibleSet = new Set(visibleLogins.map((l) => l.toLowerCase()));
    const restLogins = all
      .map((o) => o.login)
      .filter((l) => !visibleSet.has(l.toLowerCase()))
      .sort((a, b) => a.localeCompare(b));
    const priorityOrder = [...visibleLogins, ...restLogins];

    const t = setTimeout(() => {
      if (!alive) return;

      void (async () => {
        const deployPreflight = await readDeployPreflightOrSkipped(octokit, token);
        if (!alive || gen !== rowEvalGen) return;

        const nextUi: Record<string, RowUiEntry> = {};
        for (const login of visibleLogins) {
          const cached = getOrgListAnalysisCached(login);
          if (!cached) {
            nextUi[login] = "pending";
          } else {
            try {
              nextUi[login] = await resolveOrgListDeployRowCluster(
                cached,
                deployPreflight,
                octokit,
                get(githubUser)?.login ?? "",
                login,
              );
            } catch (e) {
              nextUi[login] = {
                kind: "error",
                message: e instanceof Error ? e.message : "Failed to evaluate organisation.",
              };
            }
          }
        }
        rowUi = nextUi;

        const needNetwork = priorityOrder.filter((login) => !hasOrgListAnalysisCacheEntry(login));
        if (needNetwork.length === 0) return;

        const hints = await batchOrganizationsFullsendRepoExists(octokit, needNetwork);
        if (!alive || gen !== rowEvalGen) return;

        for (let idx = 0; idx < needNetwork.length; idx++) {
          const login = needNetwork[idx]!;
          if (!alive || gen !== rowEvalGen) return;
          const hintKey = login.trim().toLowerCase();
          const hint = hints.get(hintKey) ?? null;
          const res = await analyzeOrgForOrgList(login, octokit, {
            fullsendRepoExistsHint: hint,
          });
          if (!alive || gen !== rowEvalGen) return;
          if (res.kind === "ok") {
            setOrgListAnalysisCached(login, res);
          }
          if (visibleSet.has(login.toLowerCase())) {
            try {
              rowUi = {
                ...rowUi,
                [login]: await resolveOrgListDeployRowCluster(
                  res,
                  deployPreflight,
                  octokit,
                  get(githubUser)?.login ?? "",
                  login,
                ),
              };
            } catch (e) {
              rowUi = {
                ...rowUi,
                [login]: {
                  kind: "error",
                  message: e instanceof Error ? e.message : "Failed to evaluate organisation.",
                },
              };
            }
          }
          if (idx < needNetwork.length - 1) {
            await new Promise((r) => setTimeout(r, ORG_ROW_ANALYSIS_THROTTLE_MS));
          }
        }
      })();
    }, ROW_ANALYSIS_DEBOUNCE_MS);

    return () => {
      alive = false;
      clearTimeout(t);
    };
  });

  async function loadOrgs(force: boolean, opts?: LoadOrgsOpts) {
    const token = loadToken()?.accessToken;
    if (!token) {
      loadAbort?.abort();
      loadAbort = null;
      loadGeneration += 1;
      clearPollTimeouts();
      pollSession += 1;
      serverOrgs = [];
      displayedOrgs = [];
      error = null;
      emptyHint = null;
      resolvedAppSlug = null;
      installationListTruncated = false;
      hasCompletedOrgFetchOnce = false;
      inlineListRefresh = false;
      listCheckAt = null;
      loading = false;
      rowUi = {};
      clearOrgListAnalysisCache();
      return;
    }

    const internalPoll = opts?.internalPoll === true;
    if (!internalPoll) {
      clearPollTimeouts();
      pollSession += 1;
    }

    if (force) {
      clearOrgListAnalysisCache();
    }

    loadAbort?.abort();
    loadAbort = new AbortController();
    const signal = loadAbort.signal;
    const gen = (loadGeneration += 1);

    const skipClearLists = force && hasCompletedOrgFetchOnce;
    inlineListRefresh = skipClearLists;

    loading = true;
    error = null;
    if (!skipClearLists) {
      emptyHint = null;
      installationListTruncated = false;
      serverOrgs = [];
      displayedOrgs = [];
    }

    let fetchTimedOut = false;
    let fetchTimeoutId: ReturnType<typeof setTimeout> | undefined;

    try {
      fetchTimeoutId = window.setTimeout(() => {
        fetchTimedOut = true;
        loadAbort?.abort();
      }, ORG_LIST_FETCH_TIMEOUT_MS);

      const r = await fetchOrgsWithProgress(token, {
        githubLogin: get(githubUser)?.login,
        force,
        signal,
        onProgress: (orgs, meta) => {
          if (gen !== loadGeneration) return;
          serverOrgs = orgs;
          const capped = filterOrgsBySearch(orgs, search).slice(0, DISPLAY_CAP);
          commitDisplayedRowsFromScan(capped, meta.done);
        },
      });
      if (gen !== loadGeneration) return;

      serverOrgs = r.orgs;
      emptyHint = r.emptyHint;
      installationListTruncated = r.installationListTruncated;
      resolvedAppSlug = r.appSlugFromApi ?? loadGithubAppSlug();
      const capped = filterOrgsBySearch(r.orgs, search).slice(0, DISPLAY_CAP);
      commitDisplayedRowsFromScan(capped, true);
      listCheckAt = Date.now();
      if (opts?.allowEmptyFollowUpPoll && r.orgs.length === 0 && !signal.aborted) {
        scheduleEmptyListRechecks();
      }
      hasCompletedOrgFetchOnce = true;
    } catch (e) {
      if (gen !== loadGeneration) return;
      if (e instanceof DOMException && e.name === "AbortError") {
        if (fetchTimedOut) {
          error = "Refreshing organisations timed out. Check your connection and try again.";
          hasCompletedOrgFetchOnce = true;
        }
        return;
      }
      if (e instanceof FetchOrgsError && e.status === 401) {
        return;
      }
      if (!skipClearLists) {
        serverOrgs = [];
        displayedOrgs = [];
      }
      emptyHint = null;
      installationListTruncated = false;
      resolvedAppSlug = null;
      if (e instanceof FetchOrgsError) {
        error = e.message;
      } else {
        error = e instanceof Error ? e.message : "Failed to load organisations.";
      }
      hasCompletedOrgFetchOnce = true;
    } finally {
      if (fetchTimeoutId !== undefined) {
        clearTimeout(fetchTimeoutId);
      }
      if (gen === loadGeneration) {
        loading = false;
        inlineListRefresh = false;
      }
    }
  }

  onMount(() => {
    const unsub = githubUser.subscribe((u) => {
      if (u) void loadOrgs(false, { allowEmptyFollowUpPoll: true });
      else {
        loadAbort?.abort();
        loadAbort = null;
        loadGeneration += 1;
        clearPollTimeouts();
        pollSession += 1;
        serverOrgs = [];
        displayedOrgs = [];
        error = null;
        emptyHint = null;
        resolvedAppSlug = null;
        installationListTruncated = false;
        hasCompletedOrgFetchOnce = false;
        inlineListRefresh = false;
        listCheckAt = null;
        loading = false;
        rowUi = {};
        clearOrgListAnalysisCache();
      }
    });
    return unsub;
  });

  const filteredAll = $derived(filterOrgsBySearch(serverOrgs, search));
  const showCapHint = $derived(filteredAll.length > DISPLAY_CAP);

  const installAppHref = $derived(githubAppInstallationsNewUrl((resolvedAppSlug ?? "").trim()));

  function orgAvatarUrl(login: string): string {
    return `https://github.com/${encodeURIComponent(login)}.png?size=64`;
  }

  function formatListCheckTime(ts: number): string {
    return new Date(ts).toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  }
</script>

<section class="orgs" aria-labelledby="orgs-h">
  <h1 id="orgs-h">Select an organisation to deploy or configure Fullsend</h1>

  {#if !$githubUser}
    <p class="muted">Sign in to load this list.</p>
  {:else}
    {#if loading && serverOrgs.length === 0 && !inlineListRefresh}
      <div class="org-loading" role="status" aria-live="polite" aria-busy="true">
        <div class="org-loading-spinner" aria-hidden="true"></div>
        <p class="org-loading-label">Loading organisations…</p>
      </div>
    {:else}
      <div class="toolbar">
        <label class="search-label">
          <span class="sr-only">Filter organisations</span>
          <input
            type="search"
            class="search"
            placeholder="Type to filter"
            bind:value={search}
            oninput={() => applySearchFilterDisplay()}
            autocomplete="off"
            spellcheck="false"
          />
        </label>
        <button
          type="button"
          class="btn btn-refresh"
          disabled={loading}
          aria-busy={loading}
          onclick={() => void loadOrgs(true, { allowEmptyFollowUpPoll: true })}
        >
          {#if loading}
            <span class="btn-refresh-spinner" aria-hidden="true"></span>
          {/if}
          <span>Refresh</span>
        </button>
      </div>

      {#if listCheckAt != null && !error && !loading}
        <p class="list-check-at" role="status">
          {#if serverOrgs.length === 0}
            Last checked at {formatListCheckTime(listCheckAt)}. If you just installed the app,
            GitHub can take a minute or longer before it shows up here. After you use Refresh, this
            page also rechecks a few times in the background while you stay on it.
          {:else}
            Organisations last refreshed at {formatListCheckTime(listCheckAt)}. Use Refresh after
            you add or remove installs on GitHub; changes can take a short while to appear.
          {/if}
        </p>
      {/if}

      {#if showCapHint}
        <p class="cap-hint" role="status">Showing up to 15 organisations</p>
      {/if}

      {#if installationListTruncated && !error}
        <p class="cap-hint" role="status">
          Organisation list may be incomplete — loading stopped after
          {MAX_INSTALLATION_LIST_PAGES} pages of GitHub app installations ({INSTALLATIONS_PER_PAGE} per
          page). Use <strong>Refresh</strong> after you change installs; if you need more rows, ask your
          operator to raise the page cap in code.
        </p>
      {/if}

      {#if error}
        <div class="banner banner--err" role="alert">
          <span class="banner-msg">{error}</span>
          <button
            type="button"
            class="btn banner-retry"
            onclick={() => void loadOrgs(true, { allowEmptyFollowUpPoll: true })}
          >
            Retry
          </button>
        </div>
      {:else if filteredAll.length === 0}
        {#if serverOrgs.length === 0}
          {#if emptyHint}
            <p class="hint hint--empty" role="note">{emptyHint}</p>
          {:else}
            <p class="muted">No organisations found for this account.</p>
          {/if}
        {:else}
          <p class="muted">No matching organisations.</p>
        {/if}
      {:else}
        <ul class="list">
          {#each displayedOrgs as o (o.login)}
            {@const ui = rowUi[o.login]}
            <li class="row">
              <div class="row-main">
                <img
                  class="org-avatar"
                  src={orgAvatarUrl(o.login)}
                  alt=""
                  width="36"
                  height="36"
                  loading="lazy"
                />
                <span class="org-name">{o.login}</span>
              </div>
              <div class="row-actions">
                {#if ui === undefined || ui === "pending"}
                  <div
                    class="row-spinner"
                    role="status"
                    aria-live="polite"
                    aria-busy="true"
                    aria-label="Checking deployment state"
                  >
                    <span class="row-spinner-disc" aria-hidden="true"></span>
                  </div>
                {:else if ui.kind === "configure"}
                  <a class="btn btn-muted" href="#/org/{encodeURIComponent(o.login)}">
                    Configure
                  </a>
                {:else if ui.kind === "deploy"}
                  <a class="btn btn-primary" href="#/install/{encodeURIComponent(o.login)}">
                    Deploy Fullsend
                  </a>
                {:else if ui.kind === "cannot_deploy"}
                  {@const cdId = cannotDeployPopoverId(o.login)}
                  <div class="cannot-deploy">
                    <span class="warn-icon" aria-hidden="true">⚠</span>
                    <span class="cannot-deploy-label">Cannot deploy</span>
                    <button
                      type="button"
                      class="info-btn"
                      popovertarget={cdId}
                      aria-haspopup="dialog"
                      aria-label={`Details for why ${o.login} cannot deploy`}
                    >
                      i
                    </button>
                    <div id={cdId} class="cannot-deploy-popover" popover>
                      <p class="cannot-deploy-popover-lead">{ui.reason}</p>
                      {#if ui.missingInstallRequirements?.length}
                        <p class="cannot-deploy-popover-sub">
                          Access an organisation owner may need to approve:
                        </p>
                        <ul class="cannot-deploy-popover-list">
                          {#each ui.missingInstallRequirements as line, i (i)}
                            <li>{line}</li>
                          {/each}
                        </ul>
                      {/if}
                      {#if ui.helpBullets?.length}
                        <p class="cannot-deploy-popover-sub">Next steps</p>
                        <ul class="cannot-deploy-popover-list">
                          {#each ui.helpBullets as line, i (i)}
                            <li>{line}</li>
                          {/each}
                        </ul>
                      {/if}
                    </div>
                  </div>
                {:else if ui.kind === "error"}
                  {@const errId = rowErrPopoverId(o.login)}
                  <div class="row-err">
                    <span class="err-icon" aria-hidden="true">▲</span>
                    <span class="row-err-label">Error</span>
                    <button
                      type="button"
                      class="info-btn info-btn--err"
                      popovertarget={errId}
                      aria-haspopup="dialog"
                      aria-label={`Technical details for error on ${o.login}`}
                    >
                      i
                    </button>
                    <div id={errId} class="row-err-popover" popover>
                      <p class="row-err-popover-lead">{ui.message}</p>
                    </div>
                    <button
                      type="button"
                      class="btn row-err-retry"
                      onclick={() => void refreshOrgRow(o.login)}
                    >
                      Retry
                    </button>
                  </div>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
        {#if loading && displayedOrgs.length > 0}
          <div class="org-more-loading" role="status" aria-live="polite" aria-busy="true">
            <div class="org-more-spinner" aria-hidden="true"></div>
            <span class="sr-only">Refreshing organisation list</span>
          </div>
        {/if}
      {/if}
    {/if}

    <div class="install-app-block" aria-labelledby="install-app-h">
      <h2 id="install-app-h" class="install-app-heading">Fullsend Admin app</h2>
      {#if serverOrgs.length === 0}
        <p class="install-app-copy">
          After you install or change access on GitHub, click <strong>Refresh</strong> at the top of this
          page. GitHub does not return you here automatically. It can take a minute or longer before a
          new install appears in GitHub’s data — after you refresh, we also recheck a few times in the
          background when the list is still empty.
        </p>
        <p class="install-app-line">
          {#if installAppHref}
            <a
              class="orgs-plain-link"
              href={installAppHref}
              target="_blank"
              rel="noopener noreferrer"
            >
              Install the Fullsend Admin app on GitHub
            </a>
            <span class="install-app-after-link"> (opens in a new tab)</span>
          {:else}
            <span class="install-app-unavailable-inline">
              Install link is unavailable (app slug not known). Your operator can set
              <code>GITHUB_APP_SLUG</code> on the site Worker or ensure installations return a slug.
            </span>
          {/if}
        </p>
      {:else}
        <p class="install-app-copy">
          To deploy or configure Fullsend for another organisation, install the Fullsend Admin app
          there. When you return from GitHub, click <strong>Refresh</strong> at the top of this page.
        </p>
        {#if installAppHref}
          <p class="install-app-line">
            <a
              class="orgs-plain-link"
              href={installAppHref}
              target="_blank"
              rel="noopener noreferrer"
            >
              Install the Fullsend Admin app on GitHub
            </a>
            <span class="install-app-after-link"> (opens in a new tab)</span>
          </p>
        {:else}
          <p class="muted install-app-unavailable">
            Install link is unavailable (app slug not known). Your operator can set
            <code>GITHUB_APP_SLUG</code> on the site Worker or ensure installations return a slug.
          </p>
        {/if}
      {/if}
    </div>
  {/if}
</section>

<style>
  .orgs {
    max-width: 42rem;
  }

  .orgs h1 {
    margin: 0 0 1rem;
    font-size: 1.15rem;
    font-weight: 600;
    line-height: 1.35;
  }

  .org-loading {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1rem;
    padding: 2.5rem 1rem;
    min-height: 8rem;
  }

  .org-loading-spinner {
    width: 2.25rem;
    height: 2.25rem;
    border: 3px solid #d0d7de;
    border-top-color: #24292f;
    border-radius: 50%;
    animation: org-spin 0.75s linear infinite;
  }

  .org-loading-label {
    margin: 0;
    font-size: 0.95rem;
    color: #444;
  }

  @keyframes org-spin {
    to {
      transform: rotate(360deg);
    }
  }

  .org-more-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: calc(5 * 2.75rem);
    margin-top: 0.25rem;
    border: 1px dashed #d0d7de;
    border-radius: 8px;
    background: #fafafa;
  }

  .org-more-spinner {
    width: 2rem;
    height: 2rem;
    border: 3px solid #d0d7de;
    border-top-color: #24292f;
    border-radius: 50%;
    animation: org-spin 0.75s linear infinite;
  }

  .toolbar {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  .list-check-at {
    margin: 0 0 0.75rem;
    font-size: 0.88rem;
    line-height: 1.45;
    color: #444;
    max-width: 40rem;
  }

  .cap-hint {
    margin: 0 0 0.75rem;
    font-size: 0.88rem;
    color: #cf222e;
    font-weight: 500;
  }

  .search-label {
    flex: 1;
    min-width: 12rem;
  }

  .search {
    width: 100%;
    box-sizing: border-box;
    padding: 0.4rem 0.6rem;
    font: inherit;
    border: 1px solid #ccc;
    border-radius: 6px;
  }

  .btn {
    cursor: pointer;
    padding: 0.4rem 0.75rem;
    border: 1px solid #888;
    border-radius: 6px;
    background: #f4f4f4;
    font: inherit;
  }

  .btn:focus-visible {
    outline: 2px solid #0969da;
    outline-offset: 2px;
  }

  .btn:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .btn-refresh {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
  }

  .btn-refresh-spinner {
    width: 0.95rem;
    height: 0.95rem;
    border: 2px solid #b0b8c1;
    border-top-color: #24292f;
    border-radius: 50%;
    animation: org-spin 0.75s linear infinite;
    flex-shrink: 0;
  }

  .btn-refresh:disabled {
    opacity: 0.88;
  }

  .row-actions a.btn {
    text-decoration: none;
    display: inline-flex;
    align-items: center;
    box-sizing: border-box;
  }

  .btn-muted {
    background: #eaeaea;
    border-color: #bbb;
    color: #333;
  }

  .btn-primary {
    background: #0969da;
    border-color: #0969da;
    color: #fff;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
    border: 0;
  }

  .list {
    list-style: none;
    margin: 0.75rem 0 0;
    padding: 0;
    border: 1px solid #ddd;
    border-radius: 8px;
    overflow: hidden;
  }

  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.65rem;
    padding: 0.55rem 0.75rem;
    border-bottom: 1px solid #eee;
  }

  .row:last-child {
    border-bottom: none;
  }

  .row-main {
    display: flex;
    align-items: center;
    gap: 0.65rem;
    min-width: 0;
  }

  .org-avatar {
    border-radius: 6px;
    flex-shrink: 0;
  }

  .org-name {
    font-size: 0.95rem;
    font-weight: 500;
    overflow-wrap: break-word;
  }

  .row-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    min-height: 2.25rem;
  }

  .row-spinner {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
  }

  .row-spinner-disc {
    display: block;
    width: 1.25rem;
    height: 1.25rem;
    border: 2px solid #d0d7de;
    border-top-color: #24292f;
    border-radius: 50%;
    animation: org-spin 0.75s linear infinite;
  }

  .cannot-deploy {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.88rem;
    color: #9a6700;
  }

  .warn-icon {
    font-size: 1rem;
    line-height: 1;
  }

  .cannot-deploy-label {
    font-weight: 600;
  }

  .info-btn {
    box-sizing: border-box;
    min-width: 1.35rem;
    height: 1.35rem;
    padding: 0;
    border-radius: 999px;
    border: 1px solid #bf8700;
    background: #fff8c5;
    color: #7d4e00;
    font-size: 0.72rem;
    font-weight: 700;
    font-style: italic;
    cursor: help;
    line-height: 1;
  }

  .info-btn:focus-visible {
    outline: 2px solid #0969da;
    outline-offset: 2px;
  }

  .info-btn--err {
    border-color: #cf222e;
    background: #ffeef0;
    color: #a40e26;
    font-style: italic;
    cursor: pointer;
  }

  .cannot-deploy-popover {
    max-width: min(22rem, calc(100vw - 2rem));
    padding: 0.75rem 0.85rem;
    border: 1px solid #d4a72c;
    border-radius: 8px;
    background: #fffef5;
    box-shadow: 0 4px 14px rgb(0 0 0 / 12%);
    color: #24292f;
    font-size: 0.85rem;
    line-height: 1.45;
  }

  .cannot-deploy-popover-lead {
    margin: 0 0 0.5rem;
    font-weight: 500;
  }

  .cannot-deploy-popover-sub {
    margin: 0.5rem 0 0.35rem;
    font-weight: 600;
    font-size: 0.82rem;
  }

  .cannot-deploy-popover-list {
    margin: 0;
    padding-left: 1.15rem;
  }

  .cannot-deploy-popover-list li {
    margin: 0.2rem 0;
  }

  .row-err {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem;
    max-width: 20rem;
    font-size: 0.85rem;
    color: #a40e26;
  }

  .err-icon {
    font-size: 0.75rem;
    line-height: 1;
  }

  .row-err-label {
    font-weight: 700;
  }

  .row-err-popover {
    max-width: min(22rem, calc(100vw - 2rem));
    padding: 0.75rem 0.85rem;
    border: 1px solid #f0b2b2;
    border-radius: 8px;
    background: #fff8f8;
    box-shadow: 0 4px 14px rgb(0 0 0 / 12%);
    color: #24292f;
    font-size: 0.85rem;
    line-height: 1.45;
    overflow-wrap: break-word;
  }

  .row-err-popover-lead {
    margin: 0;
    font-weight: 500;
  }

  .row-err-retry {
    flex-shrink: 0;
    padding: 0.25rem 0.5rem;
    font-size: 0.82rem;
  }

  .muted {
    color: #555;
    margin: 0 0 0.75rem;
  }

  .hint {
    margin: 0 0 0.75rem;
    padding: 0.65rem 0.75rem;
    font-size: 0.9rem;
    line-height: 1.45;
    color: #333;
    background: #f6f8fa;
    border: 1px solid #d8dee4;
    border-radius: 6px;
    max-width: 40rem;
  }

  .hint--empty {
    margin-bottom: 1rem;
  }

  .banner {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.65rem 1rem;
    padding: 0.65rem 0.75rem;
    margin: 0.5rem 0 0;
    border: 1px solid #ffc1c1;
    border-radius: 8px;
    background: #ffeef0;
    font-size: 0.92rem;
  }

  .banner-msg {
    flex: 1;
    min-width: 10rem;
    color: #24292f;
  }

  .banner-retry {
    flex-shrink: 0;
  }

  .install-app-block {
    margin-top: 1.25rem;
    padding-top: 1rem;
    border-top: 1px solid #d8dee4;
  }

  .install-app-heading {
    margin: 0 0 0.5rem;
    font-size: 1rem;
    font-weight: 600;
  }

  .install-app-copy {
    margin: 0 0 0.65rem;
    font-size: 0.9rem;
    line-height: 1.45;
    color: #444;
    max-width: 40rem;
  }

  .install-app-line {
    margin: 0 0 0.65rem;
    font-size: 0.9rem;
    line-height: 1.45;
    max-width: 40rem;
  }

  .orgs-plain-link,
  .orgs-plain-link:visited {
    appearance: none;
    display: inline;
    margin: 0;
    padding: 0;
    border: 0;
    border-radius: 0;
    background: transparent;
    box-shadow: none;
    font: inherit;
    font-weight: 400;
    line-height: inherit;
    color: #0969da;
    text-decoration: underline;
    text-underline-offset: 0.15em;
    cursor: pointer;
  }

  .orgs-plain-link:hover {
    color: #0550ae;
  }

  .orgs-plain-link:focus-visible {
    outline: 2px solid #0969da;
    outline-offset: 2px;
    border-radius: 2px;
  }

  .install-app-after-link {
    font-size: 0.9rem;
    color: #57606a;
    font-weight: 400;
  }

  .install-app-unavailable-inline {
    color: #57606a;
    font-weight: 400;
  }

  .install-app-unavailable {
    margin: 0;
    font-size: 0.88rem;
    max-width: 40rem;
  }

  .install-app-unavailable code {
    font-size: 0.85em;
  }
</style>
