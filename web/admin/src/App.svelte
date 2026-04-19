<script lang="ts">
  import { onMount } from "svelte";
  import Router from "svelte-spa-router";
  import Home from "./routes/Home.svelte";
  import {
    authBootPending,
    githubLogin,
    refreshSession,
    signOut,
  } from "./lib/auth/session";
  import {
    completeGithubOAuthFromHandoff,
    consumeOAuthParamsFromDocumentUrl,
    startGithubSignIn,
  } from "./lib/auth/oauth";

  const routes = {
    "/": Home,
  };

  let oauthErr = $state<string | null>(null);

  onMount(() => {
    const onGithub401 = () => signOut();
    window.addEventListener("fullsend:github-unauthorized", onGithub401);

    void (async () => {
      try {
        const hadOAuthReturn = consumeOAuthParamsFromDocumentUrl();
        if (hadOAuthReturn) {
          const result = await completeGithubOAuthFromHandoff();
          if (!result.ok) {
            oauthErr = result.error;
          }
          return;
        }
        await refreshSession();
      } finally {
        authBootPending.set(false);
      }
    })();

    return () =>
      window.removeEventListener("fullsend:github-unauthorized", onGithub401);
  });
</script>

<header class="bar" aria-busy={$authBootPending}>
  <strong>Fullsend Admin</strong>
  <span class="tag">{import.meta.env.DEV ? "local dev" : "production"}</span>
  <span class="spacer"></span>
  {#if $authBootPending}
    <span class="auth-wait" role="status">
      <span class="spinner" aria-hidden="true"></span>
      <span class="auth-wait-label">Loading session…</span>
    </span>
  {:else if $githubLogin}
    <span class="user">{$githubLogin}</span>
    <button type="button" class="btn" onclick={() => signOut()}>Sign out</button>
  {:else}
    <button
      type="button"
      class="btn primary"
      onclick={async () => {
        try {
          await startGithubSignIn();
        } catch (e) {
          const msg =
            e instanceof Error ? e.message : "Sign-in failed to start.";
          console.error("[fullsend-admin] startGithubSignIn", e);
          window.alert(msg);
        }
      }}>Sign in with GitHub</button>
  {/if}
</header>
{#if oauthErr}
  <p class="oauth-err">{oauthErr}</p>
  <p class="oauth-err-actions">
    <button type="button" class="link-btn" onclick={() => (oauthErr = null)}>
      Dismiss
    </button>
  </p>
{/if}
<main class="main" class:main--pending={$authBootPending}>
  <Router {routes} />
</main>

<style>
  .bar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid #ccc;
  }
  .tag {
    font-size: 0.75rem;
    opacity: 0.7;
  }
  .spacer {
    flex: 1;
    min-width: 0.5rem;
  }
  .user {
    font-size: 0.9rem;
  }
  .auth-wait {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
    color: #444;
  }
  .auth-wait-label {
    white-space: nowrap;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .spinner {
    display: inline-block;
    width: 1.1rem;
    height: 1.1rem;
    border: 2px solid #ccc;
    border-top-color: #24292f;
    border-radius: 50%;
    animation: spin 0.7s linear infinite;
  }
  .btn {
    cursor: pointer;
    padding: 0.35rem 0.75rem;
    border: 1px solid #888;
    border-radius: 6px;
    background: #f4f4f4;
    font: inherit;
  }
  .btn.primary {
    background: #24292f;
    color: #fff;
    border-color: #24292f;
  }
  .oauth-err {
    margin: 0;
    padding: 0.75rem 1rem;
    color: #a40000;
    border-bottom: 1px solid #ecc;
    max-width: 42rem;
  }
  .oauth-err-actions {
    margin: 0;
    padding: 0 1rem 0.5rem;
    font-size: 0.9rem;
  }
  .link-btn {
    cursor: pointer;
    border: none;
    background: none;
    padding: 0;
    font: inherit;
    color: #0969da;
    text-decoration: underline;
  }
  .main {
    padding: 1rem;
  }
  .main.main--pending {
    opacity: 0.55;
    pointer-events: none;
  }
</style>
