<script lang="ts">
  /** `svelte-spa-router` passes matched params into the routed component. */
  let { params = { org: "" } }: { params?: { org?: string } } = $props();
  const org = $derived(params?.org ?? "");
</script>

<section class="stub" aria-labelledby="stub-h">
  <h1 id="stub-h">Organisation: {org}</h1>
  <p class="lede">
    Organisation dashboard (repo union, status panes) is planned in <strong>Task 12</strong>
    of the admin SPA plan.
  </p>
  <p>
    <a class="back" href="#/orgs">← Back to organisations</a>
  </p>
</section>

<style>
  .stub {
    max-width: 40rem;
  }

  .stub h1 {
    margin: 0 0 0.5rem;
    font-size: 1.2rem;
  }

  .lede {
    margin: 0 0 1rem;
    line-height: 1.5;
    color: #333;
  }

  .back {
    color: #0969da;
  }
</style>
