# CI Workflows

Conventions for GitHub Actions workflows under `.github/workflows/`. Use this checklist when adding or modifying workflows.

## Concurrency groups

- [ ] Use `${{ github.workflow }}` as the workflow identifier — never duplicate the workflow name as a hardcoded string prefix.
- [ ] Standard pattern for branch/PR workflows:
  ```yaml
  concurrency:
    group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
    cancel-in-progress: true
  ```
- [ ] For `pull_request_target` workflows, use a PR-number-scoped group for that event and fall back to the standard pattern for other triggers:
  ```yaml
  concurrency:
    group: >-
      ${{ github.event_name == 'pull_request_target'
          && format('{0}-{1}', github.workflow, github.event.pull_request.number)
          || format('{0}-{1}', github.workflow, github.ref) }}
  ```
- [ ] Never cancel in-progress runs on the default branch (`refs/heads/main`). Gate `cancel-in-progress` when the workflow triggers on `push` to `main`.

**Why:** A hardcoded prefix like `my-workflow-${{ github.workflow }}` is redundant — `github.workflow` already resolves to the workflow `name:` field. The duplication creates a confusing group key and wastes characters.

## Timeout policy

- [ ] Every non-reusable workflow job must set `timeout-minutes`.
- [ ] Use the minimum reasonable value for the job's workload.
- [ ] Add a comment explaining the choice when it is not obvious:
  ```yaml
  jobs:
    build:
      runs-on: ubuntu-24.04
      # Stub is near-instant; headroom for the real test suite.
      timeout-minutes: 15
  ```
- [ ] Reusable workflows (`on: workflow_call`) should document timeout expectations in a comment but leave `timeout-minutes` to the caller, since the caller controls the runner and workload context.

**Why:** GitHub Actions defaults to a 6-hour timeout. A runaway job without `timeout-minutes` consumes runner capacity silently. Explicit timeouts make resource usage visible and catch hangs early.

## Trigger completeness

- [ ] Path-filtered workflows (`on.push.paths` or `on.pull_request.paths`) must include `merge_group:` as a trigger.
- [ ] Since `merge_group` does not support `on.paths`, add a path-relevance guard step that skips the job when no relevant files changed:
  ```yaml
  on:
    push:
      branches: [main]
      paths:
        - "src/**"
        - ".github/workflows/my-workflow.yml"
    pull_request:
      paths:
        - "src/**"
        - ".github/workflows/my-workflow.yml"
    merge_group:

  jobs:
    build:
      steps:
        - name: Check for relevant changes
          id: changes
          if: github.event_name == 'merge_group'
          env:
            GH_TOKEN: ${{ github.token }}
            REPO: ${{ github.repository }}
            MERGE_GROUP_BASE: ${{ github.event.merge_group.base_sha }}
            MERGE_GROUP_HEAD: ${{ github.event.merge_group.head_sha }}
          # SYNC-WITH: push.paths / pull_request.paths filters above
          run: |
            FILES=$(gh api "repos/${REPO}/compare/${MERGE_GROUP_BASE}...${MERGE_GROUP_HEAD}" \
              --jq '.files[].filename') || {
              echo "::warning::Failed to fetch merge group files — running as a precaution"
              echo "relevant=true" >> "$GITHUB_OUTPUT"
              exit 0
            }
            FILE_COUNT=$(echo "$FILES" | wc -l)
            if [ "$FILE_COUNT" -ge 300 ]; then
              echo "::warning::Compare API returned $FILE_COUNT files (possible truncation) — running as a precaution"
              echo "relevant=true" >> "$GITHUB_OUTPUT"
              exit 0
            fi
            if echo "$FILES" | grep -qE '^src/|^\.github/workflows/my-workflow\.yml$'; then
              echo "relevant=true" >> "$GITHUB_OUTPUT"
            else
              echo "::notice::No relevant files changed — skipping"
              echo "relevant=false" >> "$GITHUB_OUTPUT"
            fi

        - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
          if: steps.changes.outputs.relevant != 'false'
  ```
- [ ] Mark the path-relevance grep with `# SYNC-WITH: push.paths` so reviewers can verify the path list stays in sync with the `on:` filter.
- [ ] On API failure or file-count truncation (>= 300), default to running the job — false positives are cheaper than false negatives.

**Why:** The merge queue creates temporary merge commits that do not match `on.push` or `on.pull_request` triggers. Without `merge_group:`, a path-filtered workflow is skipped entirely in the merge queue, which can block merges if the workflow is a required status check.

## Checkout pin

- [ ] Pin `actions/checkout` to a full SHA, not a tag.
- [ ] Use the repo-standard version and SHA. Current pin:
  ```yaml
  uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  ```
- [ ] When bumping the pin, update all workflow files in the same PR. Use `pinact` (available in CI) to verify SHA-to-tag consistency.
- [ ] Apply the same SHA-pinning convention to all third-party actions (e.g., `actions/setup-go`, `actions/upload-artifact`). Include a `# vX.Y.Z` comment after the SHA for human readability.

**Why:** Tag-only references are mutable — a compromised or force-pushed tag can inject arbitrary code into every workflow that references it. SHA pins are immutable and auditable. The version comment preserves discoverability for Dependabot/Renovate.

## Reusable workflow contracts

- [ ] Document required `inputs` and `secrets` at the top of the reusable workflow file in a comment block.
- [ ] Use descriptive `description:` fields on each input — callers read these when wiring up `workflow_call`.
- [ ] Specify `type:` (`string`, `boolean`, `number`) and `required:` on every input.
- [ ] Document expected `outputs` and their semantics when the reusable workflow produces values consumed by the caller.
- [ ] When changing a reusable workflow's inputs or outputs, check all callers. Search for the workflow filename across the repo:
  ```bash
  grep -r "uses:.*reusable-<name>.yml" .github/workflows/
  ```

**Why:** Reusable workflows are implicit contracts. Undocumented inputs lead to misconfigured callers, broken dispatches, and silent failures. Treating them as documented APIs prevents drift.

## Permissions

- [ ] Set `permissions: {}` at the workflow level and grant only the permissions each job needs at the job level.
- [ ] Never use `permissions: write-all` or omit permissions (which defaults to the repo's broad default token permissions).
- [ ] Separate jobs that need elevated permissions (e.g., `pull-requests: write`) from jobs that check out untrusted code.

## Additional conventions

- [ ] Always include the workflow file itself in its own `paths:` filter so changes to the workflow trigger its own CI.
- [ ] Use `ubuntu-24.04` (specific version), not `ubuntu-latest`, for reproducible builds.
- [ ] Environment variables that hold secrets must use `${{ secrets.NAME }}` — never hardcode sensitive values or use environment variables with defaults for secrets.
