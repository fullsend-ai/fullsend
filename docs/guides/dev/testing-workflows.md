# Testing workflow changes

This guide explains how to test changes to Fullsend's GitHub Actions workflows.

## Per-repo mode

In your repository modify the dispatch job at `.github/workflows/fullsend.yaml` to
use the ref you want to test. Change the reference `uses` use and
`fullsend_ai_ref` to the same value.

```yaml
# .github/workflows/fullsend.yaml
# [...]
jobs:
  dispatch:
    # [...]
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@<YOUR_VERSION>
    with:
      # [...]
      fullsend_ai_ref: <YOUR_VERSION>
      # [...]
```

Then push this change and trigger a Fullsend action: `/fs-triage`, `/fs-code`, ... When the ref is
deleted from fullsend-ai/fullsend (branch deleted or commit amended), revert this back to the
desired reference.

## Per-org mode

**WARNING**: this impacts all repositories, so proceed with care. You can install your test repository
using the repository install mode to avoid this problem.

In your `.fullsend` repository modify the desired stage workflow file (triage in the example below).
Change the reference on `uses` for the `reusable-<stage>.yml` and the `fullsend_ai_ref` passed to it:

```yaml
# .github/workflows/triage.yml
# [...]
jobs:
  triage:
    # [...]
    uses: fullsend-ai/fullsend/.github/workflows/reusable-triage.yml@<YOUR_VERSION>
    with:
      # [...]
      fullsend_ai_ref: <YOUR_VERSION>
      # [...]
```

Then push this change and trigger a Fullsend action on your test repository: `/fs-triage`, `/fs-code`, ...
When the ref is deleted from fullsend-ai/fullsend (branch deleted or commit amended), revert this back
to the desired reference.
