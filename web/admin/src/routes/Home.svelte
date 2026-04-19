<script lang="ts">
  import { authBootPending, githubUser } from "../lib/auth/session";

  const hash =
    typeof window !== "undefined" ? window.location.hash || "#/" : "#/";
</script>

<p>Admin shell is up.</p>
<p>Current hash: <code>{hash}</code></p>

<section
  class="proof"
  class:proof--pending={$authBootPending}
  aria-labelledby="api-proof-h"
  aria-busy={$authBootPending}
>
  <h2 id="api-proof-h">API access proof</h2>
  {#if $authBootPending}
    <p>Session loading…</p>
    <p class="mono muted">GitHub profile will appear here.</p>
  {:else if $githubUser}
    <p>Signed in as <strong>{$githubUser.login}</strong>.</p>
    <p class="mono">
      GET /user → login: {$githubUser.login}{$githubUser.name
        ? `, name: ${$githubUser.name}`
        : ""}
    </p>
  {:else}
    <p>Not signed in.</p>
    <p class="mono">Sign in to verify GitHub API access.</p>
  {/if}
</section>

<style>
  .proof {
    margin-top: 1.5rem;
    padding: 1rem;
    border: 1px solid #ddd;
    border-radius: 8px;
    max-width: 42rem;
  }
  .proof.proof--pending {
    opacity: 0.55;
    pointer-events: none;
  }
  .proof h2 {
    margin: 0 0 0.5rem;
    font-size: 1rem;
  }
  .mono {
    font-family: ui-monospace, monospace;
    font-size: 0.85rem;
    word-break: break-word;
  }
  .muted {
    opacity: 0.85;
  }
</style>
