<script lang="ts">
  let { params = { org: "" } }: { params?: { org?: string } } = $props();
  const org = $derived(params?.org ?? "");
</script>

<section class="stub" aria-labelledby="inst-h">
  <h1 id="inst-h">Deploy Fullsend — {org}</h1>
  <p class="lede">
    Install / onboard wizard flows are planned in <strong>Tasks 13–14</strong> of the admin SPA plan.
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
