# Enrollment v1 (admin install) — normative specification

**Version:** v1
**ADR:** [0013](../../../../ADRs/0013-admin-install-repo-enrollment-v1.md)
**Scope:** How the admin install enrolls a GitHub-hosted repository into the fullsend agent pipeline by opening a pull request that adds a *shim* workflow. The shim triggers `workflow_dispatch` entrypoints for per-role workflow files (`triage.yml`, `code.yml`, `review.yml`) in the org’s `.fullsend` config repository using the org Actions secret `FULLSEND_DISPATCH_TOKEN` (see [ADR 0008](../../../../ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md), [ADR 0009](../../../../ADRs/0009-pull-request-target-in-shim-workflows.md)). This spec is the contract for tooling (for example the enrollment layer and `forge.Client`); hosting details follow GitHub’s REST API unless stated otherwise.

## 1. Definitions

- **Owner** — GitHub organization or user that owns the repository (the install target). In admin-install configuration this is the organization login passed to tooling as the owner namespace.
- **Target repository** — A repository listed as enabled for the pipeline in org configuration (see ADR 0011).
- **Shim workflow** — The workflow file at **shim path** whose job is to forward repository events to per-role workflows in the org’s `.fullsend` repository by running `gh workflow run` against the appropriate workflow file (`triage.yml`, `code.yml`, `review.yml`) with authentication from **dispatch secret** (see §3).
- **Enrollment branch** — The head branch for the enrollment change proposal.
- **Base branch** — The branch the enrollment pull request targets.

## 2. Dispatch target (`.fullsend` / per-role workflows)

At run time the shim MUST resolve the config repository as `${{ github.repository_owner }}/.fullsend` and MUST invoke per-role workflow files (`triage.yml`, `code.yml`, `review.yml`) in that repository.

**Rules:**

1. The committed shim MUST use GitHub Actions context `github.repository_owner` so the same file content is valid for any target repository under that owner namespace. Installers MUST NOT perform owner-string substitution or other templating on the shim body for v1.
2. The dispatch inputs MUST include at least:
   - `event_type` — `${{ github.event_name }}`
   - `source_repo` — `${{ github.repository }}`
   - `event_payload` — JSON for `${{ toJSON(github.event) }}` (same expression shape as the reference implementation).

## 3. Constants (v1)

| Item | Value |
|------|--------|
| Shim path | `.github/workflows/fullsend.yaml` |
| Enrollment branch name | `fullsend/onboard` |
| Unenrollment branch name | `fullsend/offboard` |
| Enrollment PR title | `Connect to fullsend agent pipeline` |
| Unenrollment PR title | `Disconnect from fullsend agent pipeline` |
| Dispatch workflow files (in `.fullsend`) | `triage.yml`, `code.yml`, `review.yml` |
| Dispatch secret (org Actions secret name) | `FULLSEND_DISPATCH_TOKEN` |
| Default base branch if unspecified | `main` |
| Commit message for adding the shim | `chore: add fullsend shim workflow` |
| Commit message for removing the shim | `chore: remove fullsend shim workflow` |

## 4. Enrollment detection

A target repository is **enrolled** for v1 if and only if the file at **shim path** is readable on the repository’s **default branch** (forge `GetFileContent(owner, repo, shim path)` semantics: no other ref).

- If the file exists (any content): the repository MUST be treated as already enrolled; installers MUST NOT create another enrollment change proposal for v1.
- If the forge reports “not found” (404 / `ErrNotFound`): the repository MUST be treated as not enrolled.
- Any other error while checking MUST fail that repository’s enrollment attempt and MUST be surfaced to the operator; it MUST NOT be interpreted as enrolled.

## 5. Shim workflow content (normative)

The file at **shim path** MUST be valid GitHub Actions workflow YAML and MUST match the following structure and keys. Comments are non-normative except where they restate these rules.

The shim file MUST match the content embedded in `internal/scaffold/fullsend-repo/templates/shim-workflow.yaml`. The key design properties are:

1. **Event payload via environment variables** — All GitHub Actions context values (`toJSON(github.event)`, `github.event_name`, `github.repository`, `github.repository_owner`) are assigned to environment variables (`EVENT_PAYLOAD`, `EVENT_TYPE`, `SOURCE_REPO`, `DISPATCH_REPO`) and referenced as `"$VAR"` in the `run:` script. This prevents script injection from attacker-controlled fields (issue titles, comment bodies, PR descriptions).

2. **Slash-command matching** — Uses exact equality (`github.event.comment.body == '/triage'`) for bare commands and `startsWith(github.event.comment.body, '/triage ')` (with trailing space) for commands with arguments (e.g. `/triage this issue is ready`). This avoids false positives from partial matches like `/triagefoo` while allowing natural-language arguments after the command.

3. **Label matching** — Uses `github.event.action == 'labeled' && github.event.label.name == 'ready-to-code'` (exact match on the triggering label) rather than `contains(github.event.issue.labels.*.name, ...)` (which scans all labels). This ensures dispatch fires only on the labeling action that matters, not on unrelated label additions to an issue that already has the label.

4. **`pull_request_target`** — Used instead of `pull_request` so the workflow runs the base-branch version, preventing PR authors from modifying the shim to exfiltrate the dispatch token.

```yaml
# See internal/scaffold/fullsend-repo/templates/shim-workflow.yaml for the
# canonical content. The embedded file is the source of truth; this snippet
# shows the structure for reference only.
name: fullsend

on:
  issues:
    types: [opened, edited, labeled]
  issue_comment:
    types: [created]
  pull_request_target:
    types: [opened, synchronize, ready_for_review]
  pull_request_review:
    types: [submitted]

jobs:
  dispatch-triage:
    runs-on: ubuntu-latest
    if: >-
      github.event_name == 'issues' ||
      (github.event_name == 'issue_comment' && (
        github.event.comment.body == '/triage' ||
        startsWith(github.event.comment.body, '/triage ')
      ))
    steps:
      - name: Dispatch triage
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
          EVENT_PAYLOAD: ${{ toJSON(github.event) }}
          EVENT_TYPE: ${{ github.event_name }}
          SOURCE_REPO: ${{ github.repository }}
          DISPATCH_REPO: ${{ github.repository_owner }}/.fullsend
        run: |
          gh workflow run triage.yml \
            --repo "$DISPATCH_REPO" \
            --field event_type="$EVENT_TYPE" \
            --field source_repo="$SOURCE_REPO" \
            --field event_payload="$EVENT_PAYLOAD"

  # dispatch-code and dispatch-review follow the same env-var pattern.
  # See the embedded scaffold file for the full content.
```

## 6. Install operation sequence (workflow-driven enrollment)

The CLI no longer directly creates branches, files, or PRs in target repositories. Instead, enrollment is delegated to the `repo-maintenance.yml` workflow in `.fullsend`, which handles all target-repo operations.

The installer MUST perform these operations in order:

1. **DispatchWorkflow** — Dispatch `repo-maintenance.yml` on the `.fullsend` config repo’s default branch via `workflow_dispatch`. No inputs are required; the workflow reads `config.yaml` from the checkout.
2. **AwaitWorkflowRun** — Poll `ListWorkflowRuns` for a run created after the dispatch time. Wait until the run reaches `completed` status (up to 3 minutes). If the run cannot be found or times out, warn and continue (non-fatal).
3. **ReportReconciliationPRs** — For each enabled target repository, list open pull requests and report any with the title `Connect to fullsend agent pipeline`. For each disabled target repository, report any with the title `Disconnect from fullsend agent pipeline`.

The `repo-maintenance.yml` workflow (deployed as scaffold content by the `WorkflowsLayer`) performs the actual enrollment:

- Reads enabled repos from `config.yaml`
- For each repo not already enrolled: creates the enrollment branch, writes the shim workflow, and opens a PR
- For repos with an existing enrollment PR: updates the shim content on the branch
- For already-enrolled repos: skips

The shim template used by the workflow lives at `templates/shim-workflow.yaml` in the `.fullsend` repo (source: `internal/scaffold/fullsend-repo/templates/shim-workflow.yaml`).

## 7. Failure behavior

- If there are no enabled or disabled repos to reconcile, Install MUST report “no repositories to reconcile” and return success.
- If `DispatchWorkflow` fails, Install MUST return an error.
- If the workflow run cannot be found or times out, Install MUST warn but MUST NOT return an error (the workflow may still succeed asynchronously).
- If the workflow run completes with a non-success conclusion, Install MUST warn with the conclusion.
- Uninstall: v1 does **not** require removing the shim from target repositories via the CLI uninstall command. Unenrollment is handled by setting `enabled: false` in `config.yaml` and running install, which dispatches the reconciliation workflow.

## 8. Analyze (read model)

For reporting, a repository is **enrolled** / **not enrolled** / **unknown** using the same **shim path** and default-branch read as §4. Partial results across enabled repos MUST be representable as degraded state when some are enrolled and some are not.

Disabled repos with a stale shim (file still present at **shim path**) MUST be reported as requiring a removal PR (`WouldFix`). A mix of enrolled enabled repos and stale-shim disabled repos MUST report `StatusDegraded`.

## 9. Repo maintenance workflow

The `repo-maintenance.yml` workflow in `.fullsend` is the primary enrollment mechanism. It runs:

- On push to `main` when `config.yaml` changes
- On `workflow_dispatch` (triggered by the CLI during install)

The workflow uses a GitHub App token (generated via `actions/create-github-app-token`) to authenticate cross-repo operations. It delegates to `scripts/reconcile-repos.sh`, which performs bidirectional reconciliation:

**Enrollment (enabled repos):**
1. Reads enabled repos from `config.yaml` using `yq`
2. For each repo, checks if the shim already exists on the default branch
3. If an enrollment PR already exists, updates the shim content on the branch
4. Otherwise, creates a branch from the default branch tip, writes the shim, and opens a PR titled `Connect to fullsend agent pipeline`
5. Closes any stale unenrollment PR (`fullsend/offboard` branch) for the repo

**Unenrollment (disabled repos):**
1. Reads disabled repos from `config.yaml` using `yq`
2. For each repo, checks if the shim exists on the default branch
3. If no shim exists, skips (already clean)
4. If a removal PR already exists, skips
5. Otherwise, fetches the shim's blob SHA, creates a branch from the default branch tip, deletes the shim via the GitHub Contents API (requires SHA), and opens a PR titled `Disconnect from fullsend agent pipeline`
6. Closes any stale enrollment PR (`fullsend/onboard` branch) for the repo

Repo names are validated against `^[a-zA-Z0-9._-]+$` to prevent path injection in API calls.

This workflow is deployed as part of the scaffold (see ADR 0012) and is not part of the shim content in §5.
