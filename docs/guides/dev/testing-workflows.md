# Testing workflow changes

This guide explains how to test changes to Fullsend's GitHub Actions workflows, composite actions, and the CLI itself.

## References

There are independent version reference inputs that control different parts of the system:

| Input | Controls | Where set |
|-------|----------|-----------|
| `@<ref>` on `uses:` | Which reusable workflow YAML runs | The `uses:` line in the caller workflow |
| `fullsend_ai_ref` | Which ref composite actions (`action.yml`) and defaults are loaded from at runtime | Passed as a `with:` input |
| `fullsend_version` | Which fullsend CLI binary is installed | Passed as a `with:` input |

If `uses:`, `fullsend_ai_ref` and `fullsend_version` diverge, the workflows, agents and harnesses, and
CLI diverge, potentially causing mismatch in behavior and failures.

## Per-repo mode

In your repository modify the dispatch job at `.github/workflows/fullsend.yaml` to
use the ref you want to test:

```yaml
# .github/workflows/fullsend.yaml
# [...]
jobs:
  dispatch:
    # [...]
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@<YOUR_BRANCH>
    with:
      # [...]
      fullsend_ai_ref: <YOUR_BRANCH>
      fullsend_version: <YOUR_BRANCH>
      # [...]
```

Then push this change and trigger a Fullsend action: `/fs-triage`, `/fs-code`, ... When the ref is
deleted from fullsend-ai/fullsend (branch deleted or commit amended), revert this back to the
desired reference.

**Note**: for forks, change the `fullsend-ai/fullsend` portion to point to your fork.

## Per-org mode

**WARNING**: this impacts all repositories, so proceed with care. You can install your test repository
using the repository install mode to avoid this problem.

In your `.fullsend` repository change the references for the `reusable-<stage>.yml` you want to
test (triage in the example below):

```yaml
# .github/workflows/triage.yml
# [...]
jobs:
  triage:
    # [...]
    uses: fullsend-ai/fullsend/.github/workflows/reusable-triage.yml@<YOUR_BRANCH>
    with:
      # [...]
      fullsend_ai_ref: <YOUR_BRANCH>
      fullsend_version: <YOUR_BRANCH>
      # [...]
```

Then push this change and trigger a Fullsend action on your test repository: `/fs-triage`, `/fs-code`, ...
When the ref is deleted from fullsend-ai/fullsend (branch deleted or commit amended), revert this back
to the desired reference.

**Note**: for forks, change the `fullsend-ai/fullsend` portion to point to your fork.
