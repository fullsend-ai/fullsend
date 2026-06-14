# Testing workflow changes

This guide explains how to test changes to Fullsend's GitHub Actions workflows, composite actions, and the CLI itself.

## References

There are independent version reference inputs that control different parts of the system:

| Input | Controls | Where set |
|-------|----------|-----------|
| `@<ref>` on `uses:` | Which reusable workflow YAML runs | The `uses:` line in the caller workflow |
| `fullsend_ai_ref` | Which ref composite actions (`action.yml`) and defaults are loaded from at runtime | Passed as a `with:` input |
| `fullsend_version` | Which fullsend CLI binary is installed | Passed as a `with:` input |

When no release exists for `fullsend_version`, `action.yml` falls back to cloning
and building from source at that ref (see the `install-method=source` path).

If `uses:`, `fullsend_ai_ref` and `fullsend_version` diverge, the workflows, agents and harnesses, and
CLI diverge, potentially causing mismatch in behavior and failures.

## Vendored installs (recommended for PR testing)

Install or re-install with `--vendor` to copy reusable workflows, actions, agent
definitions, and the CLI binary from your local checkout into the config repo or
`.fullsend/` directory:

```bash
fullsend admin install "$ORG" \
  --vendor \
  --fullsend-source "$PWD" \
  --skip-app-setup \
  --skip-mint-check \
  --mint-url "$MINT_URL" \
  # ... other flags
```

After changing reusable workflows or agent content, re-run install (or
`fullsend github setup`) with `--vendor` to refresh vendored files.
`fullsend github sync-scaffold` updates thin caller templates and auto-detects
vendored vs layered mode from `.defaults/action.yml` presence.

Runtime skips the upstream sparse checkout when `.defaults/action.yml` is
present (vendored install) and stages content from `.defaults/` instead.

See [ADR 0047](../../ADRs/0047-vendored-installs-with-vendor-flag.md) for the
full distribution model.

## Layered installs: pin upstream ref

In layered mode (default), thin callers reference upstream reusable workflows at
`fullsend-ai/fullsend@v0`. To test a specific upstream ref without vendoring,
change the `uses:` ref and matching `with:` inputs in the thin caller workflows.

**Note**: for forks, change the `fullsend-ai/fullsend` portion to point to your fork.

### Per-repo mode

In your repository modify the dispatch job at `.github/workflows/fullsend.yaml`:

```yaml
# .github/workflows/fullsend.yaml
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

### Per-org mode

**WARNING**: this impacts all repositories, so proceed with care. You can install
your test repository using per-repo mode to avoid this problem.

In your `.fullsend` repository change the references for the `reusable-<stage>.yml` you want to
test (triage in the example below):

```yaml
# .github/workflows/triage.yml
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
