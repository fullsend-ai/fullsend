# CI Workflows

Conventions for GitHub Actions workflows under `.github/workflows/`. Follow these conventions when adding or modifying workflows.

## Concurrency groups

- Use `${{ github.workflow }}` as the workflow identifier — never duplicate the workflow name as a hardcoded string prefix (see [exception for `workflow_call`-only workflows](#reusable-workflow-concurrency) below).
- Standard pattern for branch/PR workflows:
  ```yaml
  concurrency:
    group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
    cancel-in-progress: true
  ```
- For `pull_request_target` workflows, use a PR-number-scoped group for that event and fall back to the standard pattern for other triggers:
  ```yaml
  concurrency:
    group: >-
      ${{ github.event_name == 'pull_request_target'
          && format('{0}-{1}', github.workflow, github.event.pull_request.number)
          || format('{0}-{1}', github.workflow, github.ref) }}
  ```
- <a id="reusable-workflow-concurrency"></a>**Exception — `workflow_call`-only workflows:** workflows whose *only* trigger is `workflow_call` must use a hardcoded role-specific prefix with `inputs.*`-based suffixes instead of `${{ github.workflow }}`, because in `workflow_call` context `github.workflow` resolves to the *caller's* workflow name, not the reusable workflow's own name. Using it would produce incorrect concurrency scoping and could cancel the caller's runs. The suffix should derive from `inputs.*` (not `github.event.*`), since the caller's event context may not match the underlying PR/issue. See `reusable-code.yml` for the canonical pattern:
  ```yaml
  concurrency:
    group: fullsend-code-agent-${{ inputs.source_repo }}-${{ fromJSON(inputs.event_payload).issue.number || fromJSON(inputs.event_payload).pull_request.number }}
  ```
  Hybrid workflows that combine `workflow_call` with direct triggers like `pull_request_target` or `push` (e.g., `e2e.yml`, `functional-tests.yml`) may still use `${{ github.workflow }}` in the branch of their concurrency expression that handles direct triggers — the `workflow_call` invocations in these cases come from a thin caller that shares the same concurrency intent.
- Never cancel in-progress runs on the default branch (`refs/heads/main`). Gate `cancel-in-progress` when the workflow triggers on `push` to `main`.

**Why:** A hardcoded prefix like `my-workflow-${{ github.workflow }}` is redundant — `github.workflow` already resolves to the workflow `name:` field. The duplication creates a confusing group key and wastes characters. The reusable-workflow exception exists because GitHub resolves `github.workflow` from the caller's context, so a reusable workflow using it would share a concurrency group with its caller.

## Timeout policy

- Every non-reusable workflow job must set `timeout-minutes`.
- Use the minimum reasonable value for the job's workload.
- Add a comment explaining the choice when it is not obvious:
  ```yaml
  jobs:
    build:
      runs-on: ubuntu-24.04
      # Stub is near-instant; headroom for the real test suite.
      timeout-minutes: 15
  ```
- Reusable workflows (`on: workflow_call`) should document timeout expectations in a comment but leave `timeout-minutes` to the caller, since the caller controls the runner and workload context.

**Why:** GitHub Actions defaults to a 6-hour timeout. A runaway job without `timeout-minutes` consumes runner capacity silently. Explicit timeouts make resource usage visible and catch hangs early.

## Trigger completeness

- Path-filtered workflows (`on.push.paths` or `on.pull_request.paths`) must include `merge_group:` as a trigger.
- Since `merge_group` does not support `on.paths`, add a path-relevance guard step that skips the job when no relevant files changed:
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
- Mark the path-relevance grep with `# SYNC-WITH: push.paths` so reviewers can verify the path list stays in sync with the `on:` filter.
- On API failure or file-count truncation (>= 300), default to running the job — false positives are cheaper than false negatives.

**Why:** The merge queue creates temporary merge commits that do not match `on.push` or `on.pull_request` triggers. Without `merge_group:`, a path-filtered workflow is skipped entirely in the merge queue, which can block merges if the workflow is a required status check.

## Checkout pin

- Pin `actions/checkout` to a full SHA, not a tag.
- Use the repo-standard version and SHA. Current pin:
  ```yaml
  uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  ```
- When bumping the pin, update all workflow files in the same PR. Use `pinact` (available in CI) to verify SHA-to-tag consistency.
- Apply the same SHA-pinning convention to all third-party actions (e.g., `actions/setup-go`, `actions/upload-artifact`). Include a `# vX.Y.Z` comment after the SHA for human readability.

**Why:** Tag-only references are mutable — a compromised or force-pushed tag can inject arbitrary code into every workflow that references it. SHA pins are immutable and auditable. The version comment preserves discoverability for Dependabot/Renovate.

## Reusable workflow contracts

- Document required `inputs` and `secrets` at the top of the reusable workflow file in a comment block.
- Use descriptive `description:` fields on each input — callers read these when wiring up `workflow_call`.
- Specify `type:` (`string`, `boolean`, `number`) and `required:` on every input.
- Document expected `outputs` and their semantics when the reusable workflow produces values consumed by the caller.
- When changing a reusable workflow's inputs or outputs, check all callers. Search for the workflow filename across the repo:
  ```bash
  grep -r "uses:.*reusable-<name>.yml" .github/workflows/
  ```

**Why:** Reusable workflows are implicit contracts. Undocumented inputs lead to misconfigured callers, broken dispatches, and silent failures. Treating them as documented APIs prevents drift.

## Permissions

- Set `permissions: {}` at the workflow level and grant only the permissions each job needs at the job level.
- Never use `permissions: write-all` or omit permissions (which defaults to the repo's broad default token permissions).
- Separate jobs that need elevated permissions (e.g., `pull-requests: write`) from jobs that check out untrusted code.

## Secrets in pull_request_target jobs

Jobs triggered by `pull_request_target` that check out and execute PR-head code can expose secrets to untrusted authors. This is the well-documented "pwn request" vulnerability class. Five components form the attack chain:

1. **Event type** — `pull_request_target` runs with base-branch secrets and permissions, unlike `pull_request` which sandboxes fork PRs.
2. **Checkout of PR head** — `ref: github.event.pull_request.head.sha` or `allow-unsafe-pr-checkout: true` brings untrusted code onto the runner.
3. **Code execution** — a `run:` step (e.g., `make e2e-test`, `make behaviour-test`) executes that untrusted code.
4. **Env access** — secrets wired into the step's `env:` block are readable by any code the step runs.
5. **Credential type** — the blast radius of exfiltration depends on what was exposed.

When all five components are present, any code the PR author controls can read and exfiltrate every secret in that step's environment.

**ADR-0009 and the shim distinction:** ADR-0009 documents why `pull_request_target` is safe for the shim workflow — the shim never checks out PR code, so components 2–3 are absent. This safety reasoning does **not** transfer to jobs that check out and execute PR-head code (e.g., the e2e and behaviour jobs in `e2e.yml`).

### Credential blast radius

Not all credentials carry the same risk on exfiltration:

| Category | Examples | Blast radius |
|---|---|---|
| Short-lived, narrowly scoped | GitHub App installation tokens (`${{ github.token }}`), GCP WIF tokens | Expire in minutes/hours; scoped to specific repos or resources. Attacker window is small. |
| Long-lived, broadly scoped | Classic PATs, GitHub App PEM keys, Cloudflare API tokens | Valid until manually rotated; often grant access beyond the repo that exposed them. Attacker window is large. |

Prefer short-lived narrowly-scoped credentials whenever possible. When long-lived credentials are unavoidable (e.g., PEM keys for GitHub App impersonation during test setup), the authorization gate and code review become the primary defense.

### Gate job mitigation

The `check-e2e-authorization` gate job (`gate` in `e2e.yml`) mitigates the risk by requiring authorization before the e2e and behaviour jobs check out PR-head code:

- **Trusted authors** — org members and repo collaborators are auto-authorized.
- **External contributors** — require a maintainer to apply the `ok-to-test` label after reviewing the PR diff. The gate removes stale labels when new commits land.
- **Separation of concerns** — the gate job runs on the base-branch checkout with `pull-requests: write`; the e2e/behaviour jobs run on the PR-head checkout without write permissions.

**Limitations of the gate:**

- The gate authorizes *authors*, not *code*. A trusted author whose account is compromised bypasses the gate.
- The gate does not inspect the PR diff — it trusts that maintainers reviewed the code before labeling `ok-to-test`.
- The `ok-to-test` label check has a TOCTOU window: code can change between label application and job execution (mitigated by the stale-label removal on `synchronize` events, but not eliminated).

### Review checklist for secrets in e2e/behaviour jobs

When a PR adds or modifies secret references in a `pull_request_target` job, reviewers must verify:

- [ ] **Trace the execution path.** Identify which `run:` steps execute after checkout of PR-head code. Confirm the secret is wired into one of those steps (all five attack-chain components are present).
- [ ] **Assess blast radius.** Determine whether the credential is short-lived and narrowly scoped or long-lived and broadly scoped. Document the blast radius in a PR comment when adding long-lived credentials.
- [ ] **Prefer short-lived credentials.** Use WIF tokens or GitHub App installation tokens over classic PATs or PEM keys when the test infrastructure supports it.
- [ ] **Do not wire secrets before consumption code exists.** Adding a secret to `env:` in a PR that does not yet contain the code that uses it means the secret is exposed to whatever code does run — with no benefit.
- [ ] **Verify the gate job covers the new job.** If the PR adds a new job that checks out PR-head code with secrets, confirm that job has `needs: gate` and the appropriate `if:` condition gating on `needs.gate.outputs.authorized`.

### Behaviour debug artifact redaction

The behaviour job in `e2e.yml` uploads debug artifacts on failure. Because PR-head code populates that directory under `pull_request_target`, a malicious authorized PR could write job secrets into artifact files (GitHub masks logs but not uploaded artifact contents).

Before upload, the workflow checks out `.github/scripts/redact-behaviour-artifacts.sh` from the **base branch** (`github.sha` on `pull_request_target`; the merge-group head on `merge_group`) into a separate `base-scripts/` path. PR-head code cannot modify the checked-in script contents. The redaction step runs via `env -i` with a pinned `PATH` so earlier job steps cannot poison the interpreter search path or dynamic-linker hooks.

The behaviour test step tees job output to `behaviour-test.log` in that directory (with `shell: bash` so `pipefail` propagates `make behaviour-test` failures). Upload is gated on `steps.redact.outcome == 'success'`.

Redaction covers:

- Plain text artifacts (JSON, JSONL, logs, feature output) — literal env secrets (including multi-line PEM lines), common token patterns, and PEM blocks
- Nested archives (zip, tar.gz, gzip) — extract, redact, re-pack with the same size limits as behaviour artifact downloads
- Encrypted blobs (`.gpg`, `.age`, `.enc`) — replaced with a stub (cannot scan ciphertext)
- Binary and media files (images, video, PDF, opaque blobs, NUL-containing `.log` files) — replaced with a stub
- Symlinks under the artifact directory — replaced with a stub before upload

**Residual limitations:** content scanning cannot catch every encoding or obfuscation of a secret in a text-classified file (base64, hex, split tokens). Same-job PR-head code could theoretically race the upload step after redaction; isolating redaction in a separate job would narrow that window further.

## Additional conventions

- Always include the workflow file itself in its own `paths:` filter so changes to the workflow trigger its own CI.
- Use `ubuntu-24.04` (specific version), not `ubuntu-latest`, for reproducible builds.
- Environment variables that hold secrets must use `${{ secrets.NAME }}` — never hardcode sensitive values or use environment variables with defaults for secrets.
