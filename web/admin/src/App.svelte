<script lang="ts">
  import { onMount } from "svelte";
  import Router from "svelte-spa-router";
  import OrgList from "./routes/OrgList.svelte";
  import {
    authBootPending,
    githubUser,
    reauthenticateSuggested,
    refreshSession,
    signOut,
  } from "./lib/auth/session";
  import { GITHUB_USER_UNAUTHORIZED_EVENT } from "./lib/auth/githubUnauthorized";
  import {
    clearIntendedHashStash,
    clearSigningInBrowserState,
    completeGithubOAuthFromHandoff,
    consumeIntendedHashAfterGithubOAuth,
    consumeOAuthParamsFromDocumentUrl,
    SIGNING_IN_CANCELLED_MESSAGE,
    startGithubSignIn,
  } from "./lib/auth/oauth";

  const routes = {
    "/": OrgList,
    "/orgs": OrgList,
  };

  let oauthErr = $state<string | null>(null);

  /** Aborts in-flight OAuth completion (Turnstile + token exchange) on unmount. */
  let oauthBootAbort: AbortController | null = null;
  /** Bumps on each mount cleanup so a disposed boot’s `finally` cannot clear a newer mount’s boot state. */
  let oauthBootSeq = 0;

  function cancelSigningInAsDifferentAccount(): void {
    oauthErr = null;
    oauthBootAbort?.abort();
    signOut();
    clearSigningInBrowserState();
  }

  async function beginGithubSignIn(): Promise<void> {
    oauthErr = null;
    reauthenticateSuggested.set(false);
    try {
      await startGithubSignIn();
    } catch (e) {
      oauthErr = e instanceof Error ? e.message : "Sign-in failed to start.";
      console.error("[fullsend-admin] startGithubSignIn", e);
    }
  }

  onMount(() => {
    const onGithub401 = () => signOut({ suggestReauth: true });
    window.addEventListener(GITHUB_USER_UNAUTHORIZED_EVENT, onGithub401);

    const seq = (oauthBootSeq += 1);
    authBootPending.set(true);

    oauthBootAbort = new AbortController();
    const signal = oauthBootAbort.signal;

    void (async () => {
      try {
        const hadOAuthReturn = consumeOAuthParamsFromDocumentUrl();
        if (hadOAuthReturn) {
          const result = await completeGithubOAuthFromHandoff({ signal });
          if (signal.aborted) return;
          if (!result.ok) {
            if (result.error !== SIGNING_IN_CANCELLED_MESSAGE) {
              oauthErr = result.error;
              clearIntendedHashStash();
            }
          } else {
            oauthErr = null;
            const intended = consumeIntendedHashAfterGithubOAuth();
            if (intended && intended !== "#/" && intended !== "") {
              window.location.hash = intended;
            } else {
              window.location.hash = "#/orgs";
            }
          }
          return;
        }
        await refreshSession();
      } finally {
        if (seq === oauthBootSeq) {
          authBootPending.set(false);
        }
      }
    })();

    return () => {
      window.removeEventListener(GITHUB_USER_UNAUTHORIZED_EVENT, onGithub401);
      oauthBootAbort?.abort();
      oauthBootAbort = null;
      oauthBootSeq += 1;
      authBootPending.set(false);
    };
  });
</script>

{#if $authBootPending}
  <div class="boot-screen" role="status" aria-live="polite" aria-busy="true">
    <div class="boot-spinner" aria-hidden="true"></div>
    <p class="boot-signing-label">Signing in as:</p>
    {#if $githubUser}
      <div class="boot-identity">
        {#if $githubUser.avatarUrl}
          <img class="boot-avatar" src={$githubUser.avatarUrl} alt="" width="48" height="48" />
        {/if}
        <div class="boot-user-text">
          <span class="boot-login">{$githubUser.login}</span>
          {#if $githubUser.name}
            <span class="boot-display-name">{$githubUser.name}</span>
          {/if}
        </div>
      </div>
    {:else}
      <p class="boot-wait-hint">Hang on while we verify this session with Cloudflare and GitHub.</p>
    {/if}
    <button
      type="button"
      class="btn boot-different-account"
      onclick={cancelSigningInAsDifferentAccount}
    >
      Sign in with a different account
    </button>
  </div>
{:else if $githubUser}
  <header class="bar account-bar">
    <div class="user-cluster">
      {#if $githubUser.avatarUrl}
        <img class="user-avatar" src={$githubUser.avatarUrl} alt="" width="32" height="32" />
      {/if}
      <div class="user-text">
        <span class="user-login">{$githubUser.login}</span>
        {#if $githubUser.name}
          <span class="user-name">{$githubUser.name}</span>
        {/if}
      </div>
    </div>
    <span class="spacer"></span>
    <button type="button" class="btn" onclick={() => signOut()}>Sign out</button>
  </header>

  {#if $reauthenticateSuggested}
    <div class="banner banner--warn" role="status">
      <span class="banner-msg">Your GitHub session expired or was revoked.</span>
      <button type="button" class="btn banner-action" onclick={() => void beginGithubSignIn()}>
        Re-authenticate
      </button>
    </div>
  {/if}

  {#if oauthErr}
    <div class="banner banner--err" role="alert">
      <span class="banner-msg">{oauthErr}</span>
      <button
        type="button"
        class="btn banner-action"
        onclick={() => {
          oauthErr = null;
          clearIntendedHashStash();
        }}
      >
        Dismiss
      </button>
      <button
        type="button"
        class="btn banner-action primary"
        onclick={() => void beginGithubSignIn()}
      >
        Re-authenticate
      </button>
    </div>
  {/if}

  <main class="main">
    <Router {routes} />
  </main>
{:else}
  {#if oauthErr}
    <div class="banner banner--err banner--edge" role="alert">
      <span class="banner-msg">{oauthErr}</span>
      <button
        type="button"
        class="btn banner-action"
        onclick={() => {
          oauthErr = null;
          clearIntendedHashStash();
        }}
      >
        Dismiss
      </button>
      <button
        type="button"
        class="btn banner-action primary"
        onclick={() => void beginGithubSignIn()}
      >
        Re-authenticate
      </button>
    </div>
  {/if}

  {#if $reauthenticateSuggested}
    <div class="banner banner--warn banner--edge" role="status">
      <span class="banner-msg">Your GitHub session expired or was revoked.</span>
      <button type="button" class="btn banner-action" onclick={() => void beginGithubSignIn()}>
        Re-authenticate
      </button>
    </div>
  {/if}

  <div class="login-screen">
    <h1 class="login-title">Fullsend Admin</h1>
    <p class="login-sub">Sign in to manage Fullsend for your organisations.</p>
    <button type="button" class="signin-github" onclick={() => void beginGithubSignIn()}>
      <svg class="gh-mark" width="20" height="20" viewBox="0 0 16 16" aria-hidden="true">
        <path
          fill="currentColor"
          d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"
        />
      </svg>
      Sign in with GitHub
    </button>
  </div>
{/if}

<style>
  .boot-screen {
    min-height: 100vh;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1rem;
    padding: 1.5rem;
    box-sizing: border-box;
    background: #f6f8fa;
    border-top: 1px solid #d8dee4;
  }

  .boot-spinner {
    width: 2.75rem;
    height: 2.75rem;
    border: 3px solid #d0d7de;
    border-top-color: #24292f;
    border-radius: 50%;
    animation: spin 0.75s linear infinite;
  }

  .boot-signing-label {
    margin: 0;
    font-size: 1rem;
    font-weight: 600;
    color: #24292f;
  }

  .boot-identity {
    display: flex;
    align-items: center;
    gap: 0.85rem;
    padding: 0.75rem 1rem;
    background: #fff;
    border: 1px solid #d0d7de;
    border-radius: 10px;
    box-shadow: 0 1px 2px rgb(31 35 40 / 4%);
  }

  .boot-avatar {
    border-radius: 50%;
    flex-shrink: 0;
  }

  .boot-user-text {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    line-height: 1.25;
    text-align: left;
  }

  .boot-login {
    font-weight: 700;
    font-size: 1rem;
  }

  .boot-display-name {
    font-size: 0.9rem;
    color: #57606a;
  }

  .boot-wait-hint {
    margin: 0;
    max-width: 22rem;
    text-align: center;
    font-size: 0.95rem;
    line-height: 1.45;
    color: #444;
  }

  .boot-different-account {
    margin-top: 0.25rem;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  .login-screen {
    min-height: 100vh;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 1.5rem;
    box-sizing: border-box;
    background: #fafafa;
  }

  .login-title {
    margin: 0 0 0.35rem;
    font-size: 1.5rem;
  }

  .login-sub {
    margin: 0 0 1.75rem;
    color: #555;
    text-align: center;
    max-width: 22rem;
    line-height: 1.45;
  }

  .signin-github {
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.65rem 1.25rem;
    font: inherit;
    font-size: 1rem;
    font-weight: 600;
    background: #0d1117;
    color: #fff;
    border: 1px solid #010409;
    border-radius: 8px;
    cursor: pointer;
    box-shadow: 0 1px 0 rgb(255 255 255 / 4%) inset;
  }

  .signin-github:hover {
    background: #161b22;
    border-color: #30363d;
  }

  .signin-github:focus-visible {
    outline: 2px solid #0969da;
    outline-offset: 2px;
  }

  .gh-mark {
    flex-shrink: 0;
  }

  .bar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid #d0d7de;
    background: #fff;
  }

  .spacer {
    flex: 1;
    min-width: 0.5rem;
  }

  .user-cluster {
    display: flex;
    align-items: center;
    gap: 0.65rem;
  }

  .user-avatar {
    border-radius: 50%;
    object-fit: cover;
  }

  .user-text {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    line-height: 1.2;
  }

  .user-login {
    font-weight: 700;
    font-size: 0.95rem;
  }

  .user-name {
    font-weight: 400;
    font-size: 0.85rem;
    color: #444;
  }

  .btn {
    cursor: pointer;
    padding: 0.35rem 0.75rem;
    border: 1px solid #888;
    border-radius: 6px;
    background: #f4f4f4;
    font: inherit;
  }

  .btn:focus-visible {
    outline: 2px solid #0969da;
    outline-offset: 2px;
  }

  .btn.primary {
    background: #24292f;
    color: #fff;
    border-color: #24292f;
  }

  .banner {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem 1rem;
    padding: 0.65rem 1rem;
    border-bottom: 1px solid #d0d7de;
    font-size: 0.92rem;
  }

  .banner--edge {
    max-width: 100%;
  }

  .banner--warn {
    background: #fff8c5;
    color: #24292f;
  }

  .banner--err {
    background: #ffeef0;
    color: #24292f;
  }

  .banner-msg {
    flex: 1;
    min-width: 12rem;
  }

  .banner-action.primary {
    background: #24292f;
    color: #fff;
    border-color: #24292f;
  }

  .main {
    padding: 1rem;
  }
</style>
