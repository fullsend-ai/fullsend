# Design: Wire the Installer to Lay Down Agent Runtime Content

**Date:** 2026-04-17
**Stories:** 1 (installer), unblocking 3, 4, 5
**Depends on:** Stories 2 and 7 (done)

## Problem

The installer (Story 1) creates a `.fullsend/` repo and enrolls target repos, but it lays down placeholder workflows that echo instead of running agents. Stories 2 (entry point / dispatch) and 7 (sandbox / policies) are done — their artifacts exist in `dispatch/` and in Marta's working setup at `nonflux/.fullsend` — but the installer doesn't deploy them. Stories 3 (triage), 4 (code), and 5 (review) are blocked until the installer lays down the real content.

## Goals

1. The installer lays down a working `.fullsend/` repo: per-role workflows, composite action, agent definitions, harness configs, sandbox policies, env configs, and scripts.
2. A "repo maintenance" workflow in `.fullsend/` manages shims in target repos, replacing the CLI's direct enrollment push.
3. The e2e test verifies the full pipeline: install, merge enrollment PR, file an issue, confirm the triage workflow dispatches.
4. All deployable content lives in `internal/scaffold/` for easy maintenance.
5. `dispatch/` is culled; its content moves into the scaffold directory.

## Non-Goals

- Writing the actual triage/code/review agent logic (Stories 3–5).
- Config schema validation or migration tooling (#179).
- Feedback loop (#131).
- Prompt injection defense (#129).

---

## 1. Scaffold Directory

### 1.1 Why

The installer currently embeds workflow content as Go string constants in `workflows.go` and `enrollment.go`. The existing plan doc (`docs/superpowers/plans/2026-04-17-installer-agent-content.md`) proposed adding more string constants in a `content.go` file. This is hard to maintain: YAML embedded in Go strings is difficult to read, edit, lint, and diff. Anyone changing agent content must edit Go source.

A scaffold directory mirrors the deployed `.fullsend/` repo structure 1:1. Contributors see the files as they'll appear after install. YAML stays YAML. Go's `//go:embed` bundles the directory into the binary at compile time.

### 1.2 Layout

```
internal/scaffold/
├── fullsend-repo/                    # Content deployed to <org>/.fullsend
│   ├── .github/
│   │   ├── actions/
│   │   │   └── fullsend/
│   │   │       └── action.yml        # Composite action: install CLI + openshell + run agent
│   │   ├── scripts/
│   │   │   └── setup-agent-env.sh    # Env prefix stripping helper
│   │   └── workflows/
│   │       ├── triage.yml            # Triage agent workflow (workflow_dispatch)
│   │       ├── code.yml              # Code agent workflow (workflow_dispatch)
│   │       ├── review.yml            # Review agent workflow (workflow_dispatch)
│   │       └── repo-maintenance.yml  # Manages shims in target repos
│   ├── agents/
│   │   └── triage.md                 # Triage agent definition (frontmatter + prompt)
│   ├── env/
│   │   ├── gcp-vertex.env            # Vertex AI credential env vars
│   │   └── triage.env                # Triage-specific env vars
│   ├── harness/
│   │   └── triage.yaml               # Triage harness config
│   ├── policies/
│   │   └── triage.yaml               # Triage sandbox policy (Landlock + network)
│   ├── scripts/
│   │   └── validate-triage.sh        # Triage output validation
│   └── templates/
│       └── shim-workflow.yaml        # Shim workflow deployed to target repos
```

### 1.3 Source of content

| Scaffold file | Source | Adaptation needed |
|---|---|---|
| `action.yml` | `dispatch/github/actions/fullsend/action.yml` | None — use as-is |
| `setup-agent-env.sh` | `dispatch/github/scripts/setup-agent-env.sh` | None — use as-is |
| `triage.yml` | `dispatch/github/workflows/triage.yml` | Change triggers from direct (`on: issues:`) to `workflow_dispatch` with inputs; reference secrets by `FULLSEND_*` naming convention |
| `code.yml` | `dispatch/github/workflows/code.yml` | Same adaptation |
| `review.yml` | `dispatch/github/workflows/review.yml` | Same adaptation |
| `repo-maintenance.yml` | New | See section 2 |
| `agents/triage.md` | Existing plan doc content | Use the triage agent prompt from the plan |
| `env/gcp-vertex.env` | `nonflux/.fullsend/env/gcp-vertex.env` | Match Marta's working version |
| `env/triage.env` | Plan doc content | Triage-specific env vars |
| `harness/triage.yaml` | `nonflux/.fullsend/harness/code.yaml` | Adapt for triage role |
| `policies/triage.yaml` | `nonflux/.fullsend/policies/hello-world.yaml` | Add `*.github.com` to network allowlist |
| `validate-triage.sh` | Plan doc content | Triage report validation + issue comment posting |
| `templates/shim-workflow.yaml` | Current `enrollment.go` shim | Add per-role dispatch jobs with event filtering |

### 1.4 Embedding in Go

```go
// internal/scaffold/scaffold.go
package scaffold

import "embed"

//go:embed all:fullsend-repo
var content embed.FS
```

The `WorkflowsLayer` walks `fullsend-repo/` to discover files and writes each one to `.fullsend`. The shim template lives at `fullsend-repo/templates/shim-workflow.yaml` and is deployed alongside the other scaffold files; `reconcile-repos.sh` reads it at runtime to write shim workflows to target repos.

### 1.5 Culling `dispatch/`

The `dispatch/` directory is deleted. Its files move into `internal/scaffold/fullsend-repo/` (adapted for `workflow_dispatch`). The prior work in `dispatch/` was the prototype; the scaffold is the production version.

---

## 2. Repo Maintenance Workflow

### 2.1 Why

The current `EnrollmentLayer` pushes shims to target repos directly via the GitHub API. This works for initial install but doesn't handle ongoing lifecycle:

- Adding new agent roles later requires re-running the CLI.
- Disabling a repo in `config.yaml` leaves stale shims.
- The CLI must be available and authenticated to manage enrollment.

A "repo maintenance" workflow in `.fullsend/` handles this declaratively: any push to `config.yaml` on main triggers the workflow, which reconciles shims across all repos.

### 2.2 Trigger

```yaml
on:
  push:
    branches: [main]
    paths: [config.yaml]
  workflow_dispatch:  # Manual trigger for re-reconciliation
```

### 2.3 Behavior

The workflow reads `config.yaml`, iterates repos, and for each:

**If `enabled: true`:**
- Check if the shim workflow exists on the repo's default branch.
- If missing: create branch `fullsend/onboard`, write the shim, open a PR titled "Connect to fullsend agent pipeline".
- If present but outdated (content differs from current template): create branch `fullsend/update-shim`, write the updated shim, open a PR titled "Update fullsend shim workflow".

**If `enabled: false` (or repo removed from config):**
- Check if the shim workflow exists.
- If present: create branch `fullsend/offboard`, add a commit removing the shim, open a PR titled "Disconnect from fullsend agent pipeline".

**Identity:** The workflow authenticates using the `fullsend` role's GitHub App (`FULLSEND_FULLSEND_APP_PRIVATE_KEY` + `FULLSEND_FULLSEND_APP_ID`) to generate an installation token. This is the admin/bootstrap persona — it has `contents: write` and `pull-requests: write` on target repos.

**Concurrency:** `concurrency: { group: repo-maintenance, cancel-in-progress: false }` — only one reconciliation runs at a time; new pushes queue rather than cancel.

### 2.4 Shim content (per-role dispatch)

The shim in target repos changes from a single dispatch job to per-role jobs with event filtering:

```yaml
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
        startsWith(github.event.comment.body || '', '/triage') ||
        github.event.comment.body == '/triage'
      ))
    steps:
      - name: Dispatch triage
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
        run: |
          gh workflow run triage.yml \
            --repo "${{ github.repository_owner }}/.fullsend" \
            --field event_type="${{ github.event_name }}" \
            --field source_repo="${{ github.repository }}" \
            --field event_payload='${{ toJSON(github.event) }}'

  dispatch-code:
    runs-on: ubuntu-latest
    if: >-
      (github.event_name == 'issues' && github.event.action == 'labeled'
        && github.event.label.name == 'ready-to-code') ||
      (github.event_name == 'issue_comment' && (
        startsWith(github.event.comment.body || '', '/code') ||
        github.event.comment.body == '/code'
      ))
    steps:
      - name: Dispatch code
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
        run: |
          gh workflow run code.yml \
            --repo "${{ github.repository_owner }}/.fullsend" \
            --field event_type="${{ github.event_name }}" \
            --field source_repo="${{ github.repository }}" \
            --field event_payload='${{ toJSON(github.event) }}'

  dispatch-review:
    runs-on: ubuntu-latest
    if: >-
      (github.event_name == 'issues' && github.event.action == 'labeled'
        && github.event.label.name == 'ready-for-review') ||
      (github.event_name == 'issue_comment' && (
        startsWith(github.event.comment.body || '', '/review') ||
        github.event.comment.body == '/review'
      )) ||
      github.event_name == 'pull_request_target' ||
      github.event_name == 'pull_request_review'
    steps:
      - name: Dispatch review
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
        run: |
          gh workflow run review.yml \
            --repo "${{ github.repository_owner }}/.fullsend" \
            --field event_type="${{ github.event_name }}" \
            --field source_repo="${{ github.repository }}" \
            --field event_payload='${{ toJSON(github.event) }}'
```

The shim is **not** templated with Go variables — it uses `github.repository_owner` at runtime (per ADR 0013 §2, rule 1). The `.tmpl` extension is used only if we need to conditionally include/exclude dispatch jobs based on which roles are enabled for that repo in config.yaml. For MVP, all three jobs are included for every enabled repo.

### 2.5 Installer CLI changes

The `EnrollmentLayer` simplifies:

1. **Install:** No longer creates branches, writes shims, or opens PRs. Instead, it verifies that the repos listed as `enabled: true` exist and are accessible. The repo-maintenance workflow (triggered by the config.yaml push from `ConfigRepoLayer`) handles the actual enrollment.
2. **PR URL harvesting:** After `InstallAll()` completes, the CLI polls the repo-maintenance workflow run in `.fullsend` (via `GET /repos/{owner}/.fullsend/actions/runs?event=push&status=completed`). Once the run completes, the CLI lists open PRs in each enabled target repo and displays their URLs.
3. **Analyze:** Unchanged — still checks for shim file on default branch.
4. **Uninstall:** Unchanged — still a no-op (shim removal is handled by the repo-maintenance workflow when `enabled` is set to `false`).

### 2.6 Failure modes

- **Repo-maintenance workflow fails:** The CLI's polling step reports that enrollment may not have completed and prints the workflow run URL for manual inspection.
- **Target repo permissions insufficient:** The workflow logs an error for that repo and continues with others (same as current `EnrollmentLayer` behavior).
- **Race condition (multiple config pushes):** The concurrency group serializes workflow runs. The last push wins.

---

## 3. Per-Role Workflow Adaptation

The workflows in `dispatch/` use direct triggers (`on: issues:`, etc.) because they were designed for repos where the workflow lives alongside the code. In the `.fullsend` dispatch model, these workflows receive events via `workflow_dispatch` from the shim.

### 3.1 Changes from `dispatch/` to scaffold

Each per-role workflow needs these changes:

1. **Trigger:** Replace direct event triggers with `workflow_dispatch` and three string inputs (`event_type`, `source_repo`, `event_payload`).
2. **Concurrency key:** Extract issue/PR number from `inputs.event_payload` via `fromJSON()`.
3. **Checkout target repo:** Use `inputs.source_repo` as the `repository:` parameter.
4. **Secret names:** Use `FULLSEND_GCP_SA_KEY_JSON` (not `GCP_SA_KEY`), `FULLSEND_GCP_PROJECT_ID` (not `GCP_PROJECT`).
5. **Remove `if:` conditions:** Event filtering happens in the shim, not the workflow. The workflow trusts that it was dispatched for the right event.
6. **GH_TOKEN:** Use the role-specific App's installation token (generated at the start of the job) or `FULLSEND_DISPATCH_TOKEN` for cross-repo operations.

### 3.2 Checkout pattern

Both the `.fullsend` repo and the target repo are checked out side-by-side:

```yaml
- uses: actions/checkout@v4
  with:
    path: .fullsend          # This repo (contains agent config)

- uses: actions/checkout@v4
  with:
    repository: ${{ inputs.source_repo }}
    path: target-repo         # The repo the issue was filed against
    token: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
```

The composite action then references `--fullsend-dir .fullsend --target-repo target-repo`.

---

## 4. Installer Layer Changes

### 4.1 WorkflowsLayer

**Before:** Writes 3 files (`agent.yaml`, `repo-onboard.yaml`, `CODEOWNERS`).

**After:** Walks `scaffold.Content` under `fullsend-repo/` and writes every file found. `CODEOWNERS` is still generated dynamically (needs the authenticated user's login) and written last with non-fatal failure. All other files are deployed from the scaffold as-is.

The `managedFiles` list is built dynamically from the embedded filesystem rather than hardcoded. This means adding a file to `internal/scaffold/fullsend-repo/` automatically deploys it on next install — no Go code change needed.

### 4.2 EnrollmentLayer

**Before:** Creates branches, writes shim workflows, opens PRs in target repos.

**After:**
- `Install()`: Verifies enabled repos exist and are accessible. Logs which repos will be enrolled by the repo-maintenance workflow. Optionally polls for the workflow run and harvests PR URLs.
- `shimWorkflowContent()`: Removed (shim content lives in `internal/scaffold/fullsend-repo/templates/shim-workflow.yaml`).
- `enrollRepo()`: Removed (repo-maintenance workflow handles this).

### 4.3 New: scaffold package

A thin `internal/scaffold/` package with:
- `Content embed.FS` — the embedded filesystem
- `WalkFullsendRepo(fn func(path string, content []byte) error) error` — iterates files for `WorkflowsLayer`
- `ShimWorkflowContent() ([]byte, error)` — reads and returns the shim template for the repo-maintenance workflow and any remaining CLI use
- `RenderTemplate(name string, data any) ([]byte, error)` — renders `.tmpl` files

### 4.4 ConfigRepoLayer

Unchanged. It still creates the `.fullsend` repo and writes `config.yaml`. The push to `config.yaml` on main triggers the repo-maintenance workflow.

### 4.5 SecretsLayer / InferenceLayer

Minor addition: `InferenceLayer` should store `FULLSEND_GCP_REGION` as a repo variable (the workflows reference `${{ vars.FULLSEND_GCP_REGION || 'us-east5' }}`). Check if this already happens; add if not.

---

## 5. E2E Test Extension

### 5.1 Current test phases

1. Full install (app setup + layer stack)
2. Verify installed (files exist, secrets set, enrollment PR created)
3. Idempotent reinstall
4. Uninstall
5. Idempotent uninstall

### 5.2 New phase: Triage dispatch smoke test

Insert between phases 2 and 3:

**Phase 2.5: Triage Dispatch Smoke Test**

1. **Merge the enrollment PR** in the target repo so the shim workflow becomes active. Requires a `MergePullRequest` method on the forge client (add if missing).
2. **Wait** for GitHub to process the merge (~5s).
3. **File a test issue** in the target repo: `e2e-triage-test-<runID>`. Requires a `CreateIssue` method on the forge client (add if missing).
4. **Wait for the shim workflow to trigger** in the target repo. Poll `ListWorkflowRuns` on the target repo for a `fullsend` workflow run. This confirms the shim fires on `issues: opened`.
5. **Wait for the triage workflow to be dispatched** in `.fullsend`. Poll `ListWorkflowRuns` on `.fullsend` for a `triage.yml` workflow run. This confirms the shim dispatched to the right workflow.
6. **Assert the triage workflow ran** (status `completed` or `in_progress`). It will likely fail (no real inference credentials in most CI environments), but the dispatch itself succeeding is the assertion.
7. **Cleanup:** Close the test issue. The issue cleanup runs in `t.Cleanup()`.

### 5.3 Forge client additions

Methods that may need adding to `forge.Client` interface and `github.LiveClient`:

- `MergePullRequest(ctx, owner, repo, prNumber int) error`
- `CreateIssue(ctx, owner, repo, title, body string) (*Issue, error)`
- `CloseIssue(ctx, owner, repo, number int) error`
- `ListWorkflowRuns(ctx, owner, repo, workflowFile string) ([]WorkflowRun, error)`

These are straightforward GitHub REST API wrappers.

### 5.4 Test gating

The triage dispatch smoke test runs unconditionally — it doesn't require inference credentials. The shim and workflow dispatch are pure GitHub Actions mechanics. If we later want to verify the agent actually posts a triage comment, that deeper test is gated on `E2E_HALFSEND_VERTEX_KEY`.

### 5.5 Timeout

The e2e test timeout needs increasing. The current 4m is tight with the new phase. The dispatch polling loop (up to 30 attempts x 10s = 5m) needs headroom. Increase to 10m.

---

## 6. Documentation Updates

### 6.1 Normative specs

**`docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md`:**

Replace the tracked paths table (section 2) with the expanded file set. Remove `agent.yaml` and `repo-onboard.yaml`. Add all files from `internal/scaffold/fullsend-repo/`. Update per-path requirements (section 3) for each new file. Replace the normative YAML fixtures in `files/` with the new workflow content.

**`docs/normative/admin-install/v1/adr-0013-enrollment/SPEC.md`:**

Major revision. The enrollment mechanism changes from "CLI pushes shims" to "repo-maintenance workflow pushes shims." Key changes:
- Section 2: Dispatch targets change from `agent.yaml` to per-role workflow files (`triage.yml`, `code.yml`, `review.yml`).
- Section 3: Constants table — dispatch workflow file changes from `agent.yaml` to role-specific names.
- Section 5: Shim content — replace single-dispatch shim with per-role dispatch shim.
- Section 6: Forge operation sequence — describe the repo-maintenance workflow's behavior instead of the CLI's.
- New section: Repo-maintenance workflow contract.

**`docs/normative/admin-install/v1/adr-0014-github-apps-and-secrets/SPEC.md`:**

Minor: add `FULLSEND_GCP_REGION` as a repo variable in section 5.

### 6.2 ADRs

File a new ADR (next available number) to record the decision to move enrollment from CLI-driven to workflow-driven. Reference the repo-maintenance workflow design. This supersedes the relevant parts of ADR 0013.

Consider filing an ADR for the scaffold directory pattern (embedding deployable content as files rather than Go string constants).

### 6.3 Architecture doc

Update `docs/architecture.md`:
- In "Agent Infrastructure," note that per-role workflows (`triage.yml`, `code.yml`, `review.yml`) replace the placeholder `agent.yaml`.
- In the layer stack description, note that enrollment is now handled by a repo-maintenance workflow rather than the CLI's `EnrollmentLayer`.
- Add the repo-maintenance workflow to the "Decided" list.

### 6.4 Existing plan doc

The existing plan at `docs/superpowers/plans/2026-04-17-installer-agent-content.md` is superseded by this design and the implementation plan that follows. It should be removed or marked superseded.

---

## 7. Approach: Red-Green TDD

The implementation plan (written after this design is approved) will follow red-green TDD:

1. **Scaffold package tests first** — verify embedded files exist and contain expected content. Run red. Then create the scaffold directory and files. Run green.
2. **WorkflowsLayer tests** — verify the layer writes the expanded file set. Run red. Then update the layer to walk `scaffold.Content`. Run green.
3. **EnrollmentLayer tests** — verify the simplified behavior (no shim pushing, just repo validation). Run red. Then simplify the layer. Run green.
4. **Forge client method tests** — test `MergePullRequest`, `CreateIssue`, `CloseIssue`, `ListWorkflowRuns`. Run red. Then implement. Run green.
5. **E2E test extension** — write the Phase 2.5 test. It fails because the installer still lays down placeholders. Then wire everything together. Run green (with live credentials).
6. **Normative spec updates and ADRs** — not TDD, but reviewed before merge.
7. **Cull `dispatch/`** — delete the directory. All tests still pass because content now lives in scaffold.

---

## 8. Open Questions

1. **Shim template vs. static file.** Should the shim be a Go template that conditionally includes dispatch jobs based on which roles are enabled for a specific repo in config.yaml? Or should all three dispatch jobs always be present (simpler, roles that aren't configured just won't have matching workflows to dispatch to)? **Recommendation:** Static for MVP. A dispatch to a non-existent workflow fails silently (GitHub returns 404 from `gh workflow run`), which is harmless.

2. **Repo-maintenance workflow complexity.** The workflow needs to read config.yaml, iterate repos, check for existing shims, and create/update/remove PRs. This is substantial shell scripting in a workflow. Should it instead invoke the `fullsend` CLI (`fullsend admin reconcile-enrollment` or similar)? **Recommendation:** Use the CLI. The workflow installs the `fullsend` binary (via the composite action pattern) and runs a reconcile subcommand. This keeps the logic in Go where it's testable.

3. **Dispatch token scope.** The `FULLSEND_DISPATCH_TOKEN` is currently scoped to `.fullsend` with `actions: write`. The per-role workflows also use it to check out target repos. Does the token need broader scope? **Recommendation:** Yes — the fine-grained PAT needs `contents: read` on target repos in addition to `actions: write` on `.fullsend`. The installer's `createDispatchPAT` flow should add this permission. Alternatively, use the role-specific App installation token for target repo checkout (more secure, but more complex).

4. **Timing of `dispatch/` removal.** Should `dispatch/` be removed in the same PR as the scaffold creation, or in a follow-up? **Recommendation:** Same PR. The content is moved, not deleted — git will detect the rename. Keeps the repo clean.

5. **E2e test timeout.** Increasing from 4m to 10m makes the test slower in CI. Keep it as a single test function — we can break it up later if it gets too slow.
