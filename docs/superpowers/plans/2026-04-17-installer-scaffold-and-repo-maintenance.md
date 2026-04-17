# Installer Scaffold, Repo Maintenance, and E2E Triage Dispatch

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move all deployable `.fullsend/` content into `internal/scaffold/`, replace the CLI's direct enrollment with a repo-maintenance workflow, and extend the e2e test to verify a full install-to-triage-dispatch pipeline.

**Architecture:** Deployable content lives as real files in `internal/scaffold/fullsend-repo/` (mirroring the deployed `.fullsend/` repo) and `internal/scaffold/target-repo/` (shim templates). Go's `//go:embed` bundles them into the binary. `WorkflowsLayer` walks the embedded filesystem to write files. A repo-maintenance workflow in `.fullsend/` manages shims in target repos, replacing the CLI's `EnrollmentLayer` push. The e2e test merges the enrollment PR, files an issue, and confirms the triage workflow dispatches.

**Tech Stack:** Go (`embed`, `text/template`), GitHub Actions YAML, GitHub REST API, Playwright (e2e)

**Design spec:** `docs/superpowers/specs/2026-04-17-installer-agent-content-design.md`

**Supersedes:** `docs/superpowers/plans/2026-04-17-installer-agent-content.md`

---

## File Structure

### New files

| File | Responsibility |
|------|---------------|
| `internal/scaffold/scaffold.go` | `//go:embed` declaration, walker helpers |
| `internal/scaffold/scaffold_test.go` | Verify embedded files exist and contain expected content |
| `internal/scaffold/fullsend-repo/.github/workflows/triage.yml` | Triage workflow (workflow_dispatch) |
| `internal/scaffold/fullsend-repo/.github/workflows/code.yml` | Code workflow (workflow_dispatch) |
| `internal/scaffold/fullsend-repo/.github/workflows/review.yml` | Review workflow (workflow_dispatch) |
| `internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml` | Manages shims in target repos |
| `internal/scaffold/fullsend-repo/.github/actions/fullsend/action.yml` | Composite action: install CLI + run agent |
| `internal/scaffold/fullsend-repo/.github/scripts/setup-agent-env.sh` | Env prefix stripping helper |
| `internal/scaffold/fullsend-repo/agents/triage.md` | Triage agent definition |
| `internal/scaffold/fullsend-repo/env/gcp-vertex.env` | Vertex AI env config |
| `internal/scaffold/fullsend-repo/env/triage.env` | Triage env vars |
| `internal/scaffold/fullsend-repo/harness/triage.yaml` | Triage harness config |
| `internal/scaffold/fullsend-repo/policies/triage.yaml` | Triage sandbox policy |
| `internal/scaffold/fullsend-repo/scripts/validate-triage.sh` | Triage output validation |
| `internal/scaffold/target-repo/.github/workflows/fullsend.yaml` | Shim workflow (per-role dispatch) |

### Modified files

| File | Change |
|------|--------|
| `internal/layers/workflows.go` | Walk `scaffold.Content` instead of hardcoded constants |
| `internal/layers/enrollment.go` | Simplify: verify repos exist, poll for maintenance workflow PRs |
| `internal/forge/forge.go` | Add `MergePullRequest`, `CreateIssue`, `CloseIssue`, `ListWorkflowRuns` to interface |
| `internal/forge/github/github.go` | Implement the four new methods |
| `internal/forge/fake.go` | Add stub implementations for the four new methods |
| `e2e/admin/admin_test.go` | Add Phase 2.5: merge enrollment PR, file issue, verify triage dispatch |
| `e2e/admin/testutil.go` | Update timeout if needed |
| `Makefile` | Increase e2e-test timeout from 4m to 10m |
| `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md` | Expanded file set |
| `docs/normative/admin-install/v1/adr-0013-enrollment/SPEC.md` | Workflow-driven enrollment |
| `docs/normative/admin-install/v1/adr-0014-github-apps-and-secrets/SPEC.md` | Add GCP_REGION variable |
| `docs/architecture.md` | Per-role workflows, repo-maintenance |

### Deleted files

| File | Reason |
|------|--------|
| `dispatch/github/actions/fullsend/action.yml` | Moved to `internal/scaffold/fullsend-repo/` |
| `dispatch/github/scripts/setup-agent-env.sh` | Moved to `internal/scaffold/fullsend-repo/` |
| `dispatch/github/workflows/triage.yml` | Moved and adapted to `internal/scaffold/fullsend-repo/` |
| `dispatch/github/workflows/code.yml` | Moved and adapted |
| `dispatch/github/workflows/review.yml` | Moved and adapted |
| `docs/superpowers/plans/2026-04-17-installer-agent-content.md` | Superseded by this plan |
| `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/files/agent-dispatch-v1.yaml` | Replaced by per-role workflows |
| `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/files/repo-onboard-v1.yaml` | Replaced by repo-maintenance.yml |

---

## Task 1: Create scaffold package with embedded files

**Files:**
- Create: `internal/scaffold/scaffold.go`
- Create: `internal/scaffold/scaffold_test.go`
- Create: all files under `internal/scaffold/fullsend-repo/` and `internal/scaffold/target-repo/`

This is the foundation task. All deployable content moves from Go string constants and `dispatch/` into the scaffold directory as real files.

- [ ] **Step 1: Write the test**

Create `internal/scaffold/scaffold_test.go`:

```go
package scaffold

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFullsendRepoFilesExist(t *testing.T) {
	expected := []string{
		".github/workflows/triage.yml",
		".github/workflows/code.yml",
		".github/workflows/review.yml",
		".github/workflows/repo-maintenance.yml",
		".github/actions/fullsend/action.yml",
		".github/scripts/setup-agent-env.sh",
		"agents/triage.md",
		"env/gcp-vertex.env",
		"env/triage.env",
		"harness/triage.yaml",
		"policies/triage.yaml",
		"scripts/validate-triage.sh",
	}

	for _, path := range expected {
		content, err := FullsendRepoFile(path)
		require.NoError(t, err, "reading %s", path)
		assert.NotEmpty(t, content, "%s should not be empty", path)
	}
}

func TestTargetRepoShimExists(t *testing.T) {
	content, err := TargetRepoFile(".github/workflows/fullsend.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(content), "dispatch-triage")
	assert.Contains(t, string(content), "dispatch-code")
	assert.Contains(t, string(content), "dispatch-review")
}

func TestWalkFullsendRepo(t *testing.T) {
	var paths []string
	err := WalkFullsendRepo(func(path string, content []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, len(paths) >= 12, "expected at least 12 files, got %d", len(paths))
}

func TestTriageWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "event_type")
	assert.Contains(t, s, "source_repo")
	assert.Contains(t, s, "event_payload")
	assert.Contains(t, s, "setup-agent-env.sh")
	assert.Contains(t, s, "fullsend")
}

func TestCompositeActionContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/actions/fullsend/action.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "fullsend run")
	assert.Contains(t, s, "openshell")
}

func TestSetupAgentEnvContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/scripts/setup-agent-env.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "AGENT_PREFIX")
	assert.Contains(t, s, "GITHUB_ENV")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scaffold/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Create scaffold.go**

Create `internal/scaffold/scaffold.go`:

```go
package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed fullsend-repo target-repo
var content embed.FS

// FullsendRepoFile returns the content of a file from the fullsend-repo scaffold.
// The path is relative to the fullsend-repo root (e.g., ".github/workflows/triage.yml").
func FullsendRepoFile(path string) ([]byte, error) {
	return content.ReadFile("fullsend-repo/" + path)
}

// TargetRepoFile returns the content of a file from the target-repo scaffold.
// The path is relative to the target-repo root (e.g., ".github/workflows/fullsend.yaml").
func TargetRepoFile(path string) ([]byte, error) {
	return content.ReadFile("target-repo/" + path)
}

// WalkFullsendRepo calls fn for each file in the fullsend-repo scaffold.
// Paths passed to fn are relative to the fullsend-repo root.
func WalkFullsendRepo(fn func(path string, content []byte) error) error {
	return fs.WalkDir(content, "fullsend-repo", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := content.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		// Strip the "fullsend-repo/" prefix so callers get repo-relative paths.
		relPath := path[len("fullsend-repo/"):]
		return fn(relPath, data)
	})
}
```

- [ ] **Step 4: Create scaffold fullsend-repo files**

Copy and adapt files from `dispatch/` and the design spec into the scaffold directory. Each file is listed below with its full content.

**`internal/scaffold/fullsend-repo/.github/workflows/triage.yml`** — Adapt from `dispatch/github/workflows/triage.yml`: change triggers to `workflow_dispatch`, update secret names to `FULLSEND_*`, extract issue number from `inputs.event_payload` via `fromJSON()`, remove the `if:` (event filtering happens in the shim).

**`internal/scaffold/fullsend-repo/.github/workflows/code.yml`** — Same adaptation from `dispatch/github/workflows/code.yml`. Use `fetch-depth: 0` for target repo checkout (code agent needs full history). Add `contents: write` permission.

**`internal/scaffold/fullsend-repo/.github/workflows/review.yml`** — Same adaptation from `dispatch/github/workflows/review.yml`. Add `GITHUB_PR_URL` env var.

**`internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml`** — New file. See step 5.

**`internal/scaffold/fullsend-repo/.github/actions/fullsend/action.yml`** — Copy from `dispatch/github/actions/fullsend/action.yml` as-is.

**`internal/scaffold/fullsend-repo/.github/scripts/setup-agent-env.sh`** — Copy from `dispatch/github/scripts/setup-agent-env.sh` as-is.

**`internal/scaffold/fullsend-repo/agents/triage.md`** — Triage agent definition with frontmatter:

```markdown
---
name: triage
description: Inspect a single GitHub issue and produce a triage assessment.
skills: []
tools: Bash(gh)
model: opus
---

You are a triage agent. Your job is to inspect a single GitHub issue and produce a structured triage assessment.

## Inputs

- The environment variable `GITHUB_ISSUE_URL` contains the API URL to the issue.

## Steps

### 1. Fetch the issue

Use the `gh` CLI to retrieve the issue details:

```
gh issue view "$GITHUB_ISSUE_URL" --json number,title,body,labels,assignees,createdAt,updatedAt,author,comments,state,milestone
```

If the command fails, report the error clearly and exit.

### 2. Triage the issue

Analyze the issue and determine:

- **Priority** — P0-critical, P1-high, P2-medium, or P3-low
- **Category** — bug, feature-request, question, documentation, infrastructure, security, performance, tech-debt
- **Actionability** — ready, needs-info, or stale
- **Suggested labels**
- **Summary** — one-sentence summary
- **Recommended action**

### 3. Write the triage report

Write to `$FULLSEND_OUTPUT_DIR/triage-report.md`:

```markdown
# Issue Triage Report

**Issue:** #{number} — {title}
**Repository:** {owner/repo}
**Author:** {author}
**Created:** {date}

## Assessment

- **Priority:** {priority}
- **Category:** {category}
- **Actionability:** {ready | needs-info | stale}
- **Suggested labels:** {labels}

## Summary

{summary}

## Recommended Action

{action}
```

## Guidelines

- Do NOT modify the issue (no labels, comments, or assignments). Read-only triage.
- When in doubt on priority, err toward higher.
- Factor comments into your assessment.
```

**`internal/scaffold/fullsend-repo/env/gcp-vertex.env`:**

```bash
export CLAUDE_CODE_USE_VERTEX=1
export ANTHROPIC_VERTEX_PROJECT_ID=${ANTHROPIC_VERTEX_PROJECT_ID}
export CLOUD_ML_REGION=${CLOUD_ML_REGION}
export GOOGLE_APPLICATION_CREDENTIALS=/tmp/workspace/.gcp-credentials.json
```

**`internal/scaffold/fullsend-repo/env/triage.env`:**

```bash
export GITHUB_ISSUE_URL="${GITHUB_ISSUE_URL}"
export GH_TOKEN=${GH_TOKEN}
```

**`internal/scaffold/fullsend-repo/harness/triage.yaml`:**

```yaml
agent: agents/triage.md
model: opus
image: quay.io/manonru/fullsend-exp:latest
policy: policies/triage.yaml

host_files:
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/workspace/.gcp-credentials.json
  - src: env/triage.env
    dest: /tmp/workspace/.env.d/triage.env
    expand: true

skills: []

validation_loop:
  script: scripts/validate-triage.sh
  max_iterations: 3

timeout_minutes: 10
```

**`internal/scaffold/fullsend-repo/policies/triage.yaml`:**

```yaml
version: 1

filesystem_policy:
  include_workdir: true
  read_only: [/usr, /lib, /proc, /dev/urandom, /app, /etc, /var/log]
  read_write: [/sandbox, /tmp, /dev/null]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
  run_as_group: sandbox

network_policies:
  vertex_ai:
    name: vertex-ai
    endpoints:
      - host: "*.github.com"
        port: 443
        protocol: tcp
        enforcement: enforce
        access: allow
      - host: "*.googleapis.com"
        port: 443
        protocol: tcp
        enforcement: enforce
        access: allow
    binaries:
      - path: "**/curl"
      - path: "**/claude"
      - path: "**/node"
```

**`internal/scaffold/fullsend-repo/scripts/validate-triage.sh`:**

```bash
#!/usr/bin/env bash
set -euo pipefail

TRIAGE_REPORT_FILE="output/triage-report.md"

if [ ! -f "$TRIAGE_REPORT_FILE" ]; then
  echo "FAIL: $TRIAGE_REPORT_FILE not found"
  exit 1
fi

# Post the triage report as a comment on the issue.
REPO=$(echo "$GITHUB_ISSUE_URL" | sed 's|https://api.github.com/repos/||; s|/issues/.*||')
ISSUE_NUMBER=$(basename "$GITHUB_ISSUE_URL")

gh issue comment "$ISSUE_NUMBER" --repo "$REPO" --body-file "$TRIAGE_REPORT_FILE"

echo "PASS: output validated"
exit 0
```

- [ ] **Step 5: Create the repo-maintenance workflow**

Create `internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml`:

```yaml
name: Repo Maintenance

on:
  push:
    branches: [main]
    paths: [config.yaml]
  workflow_dispatch:

concurrency:
  group: repo-maintenance
  cancel-in-progress: false

permissions:
  contents: read

jobs:
  reconcile:
    name: Reconcile enrollment
    runs-on: ubuntu-latest
    steps:
      - name: Checkout .fullsend
        uses: actions/checkout@v4

      - name: Generate app token
        id: app-token
        uses: actions/create-github-app-token@v2
        with:
          app-id: ${{ vars.FULLSEND_FULLSEND_APP_ID }}
          private-key: ${{ secrets.FULLSEND_FULLSEND_APP_PRIVATE_KEY }}
          owner: ${{ github.repository_owner }}

      - name: Install fullsend CLI
        uses: ./.github/actions/fullsend
        with:
          agent: __install_only__

      - name: Reconcile target repos
        env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          FULLSEND_DIR: ${{ github.workspace }}
        run: |
          fullsend admin reconcile-enrollment \
            --org "${{ github.repository_owner }}" \
            --fullsend-dir "${FULLSEND_DIR}" \
            --token "${GH_TOKEN}"
```

Note: This workflow invokes a `fullsend admin reconcile-enrollment` subcommand that we will implement. It reads `config.yaml`, iterates repos, and creates/updates/removes enrollment PRs. Keeping the logic in Go makes it testable.

- [ ] **Step 6: Create the target-repo shim**

Create `internal/scaffold/target-repo/.github/workflows/fullsend.yaml`:

```yaml
# fullsend shim workflow
# Routes events to per-role agent dispatch workflows in .fullsend.
#
# Security: pull_request_target runs the BASE branch version of this workflow,
# preventing PRs from modifying it to exfiltrate the dispatch token.
# This shim never checks out PR code, so it is not vulnerable to "pwn request"
# attacks (see: Trivy CVE-2026-33634, hackerbot-claw campaign).
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

- [ ] **Step 7: Run tests**

Run: `go test ./internal/scaffold/ -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/scaffold/
git commit -m "feat: add scaffold package with embedded .fullsend content"
```

---

## Task 2: Update WorkflowsLayer to use scaffold

**Files:**
- Modify: `internal/layers/workflows.go`
- Create: `internal/layers/workflows_test.go`

The layer stops using hardcoded string constants and walks the scaffold instead.

- [ ] **Step 1: Write the test**

Create `internal/layers/workflows_test.go`:

```go
package layers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestManagedFilesMatchScaffold(t *testing.T) {
	// Verify that every file in the scaffold is included in managedFiles.
	var scaffoldPaths []string
	err := scaffold.WalkFullsendRepo(func(path string, _ []byte) error {
		scaffoldPaths = append(scaffoldPaths, path)
		return nil
	})
	require.NoError(t, err)

	for _, path := range scaffoldPaths {
		found := false
		for _, managed := range managedFiles {
			if managed == path {
				found = true
				break
			}
		}
		assert.True(t, found, "managedFiles should include scaffold file %s", path)
	}
}

func TestManagedFilesDoNotIncludeOldPlaceholders(t *testing.T) {
	for _, path := range managedFiles {
		assert.NotEqual(t, ".github/workflows/agent.yaml", path,
			"managedFiles should not include old agent.yaml placeholder")
		assert.NotEqual(t, ".github/workflows/repo-onboard.yaml", path,
			"managedFiles should not include old repo-onboard.yaml placeholder")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/layers/ -run TestManagedFiles -v`
Expected: FAIL — old constants still present, scaffold paths not in managedFiles

- [ ] **Step 3: Update workflows.go**

Replace the hardcoded constants and file list with scaffold-driven logic:

```go
package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const codeownersPath = "CODEOWNERS"

// managedFiles lists every file this layer manages, in write order.
// Built from the scaffold at init time, with CODEOWNERS appended last.
var managedFiles []string

func init() {
	_ = scaffold.WalkFullsendRepo(func(path string, _ []byte) error {
		managedFiles = append(managedFiles, path)
		return nil
	})
	managedFiles = append(managedFiles, codeownersPath)
}

// WorkflowsLayer manages workflow files and CODEOWNERS in the .fullsend
// config repo. It writes all files from the scaffold plus a dynamically
// generated CODEOWNERS.
type WorkflowsLayer struct {
	org               string
	client            forge.Client
	ui                *ui.Printer
	authenticatedUser string
}

var _ Layer = (*WorkflowsLayer)(nil)

func NewWorkflowsLayer(org string, client forge.Client, printer *ui.Printer, user string) *WorkflowsLayer {
	return &WorkflowsLayer{
		org:               org,
		client:            client,
		ui:                printer,
		authenticatedUser: user,
	}
}

func (l *WorkflowsLayer) Name() string {
	return "workflows"
}

func (l *WorkflowsLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		return []string{"repo", "workflow"}
	case OpUninstall:
		return nil
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

func (l *WorkflowsLayer) Install(ctx context.Context) error {
	// Write all scaffold files first.
	err := scaffold.WalkFullsendRepo(func(path string, content []byte) error {
		l.ui.StepStart("Writing " + path)
		writeErr := l.client.CreateOrUpdateFile(ctx, l.org, forge.ConfigRepoName, path, "chore: update "+path, content)
		if writeErr != nil {
			l.ui.StepFail("Failed to write " + path)
			return fmt.Errorf("writing %s: %w", path, writeErr)
		}
		l.ui.StepDone("Wrote " + path)
		return nil
	})
	if err != nil {
		return err
	}

	// Write CODEOWNERS last (failure is non-fatal).
	l.ui.StepStart("Writing " + codeownersPath)
	if err := l.client.CreateOrUpdateFile(ctx, l.org, forge.ConfigRepoName, codeownersPath,
		"chore: update "+codeownersPath, []byte(l.codeownersContent())); err != nil {
		l.ui.StepWarn("Could not write " + codeownersPath + ": " + err.Error())
	} else {
		l.ui.StepDone("Wrote " + codeownersPath)
	}

	return nil
}

func (l *WorkflowsLayer) Uninstall(_ context.Context) error {
	return nil
}

func (l *WorkflowsLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	var present, missing []string
	for _, path := range managedFiles {
		_, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, path)
		if err != nil {
			if forge.IsNotFound(err) {
				missing = append(missing, path)
				continue
			}
			return nil, fmt.Errorf("checking %s: %w", path, err)
		}
		present = append(present, path)
	}

	switch {
	case len(missing) == 0:
		report.Status = StatusInstalled
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
	case len(present) == 0:
		report.Status = StatusNotInstalled
		for _, m := range missing {
			report.WouldInstall = append(report.WouldInstall, "write "+m)
		}
	default:
		report.Status = StatusDegraded
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
		for _, m := range missing {
			report.WouldFix = append(report.WouldFix, "write "+m)
		}
	}

	return report, nil
}

func (l *WorkflowsLayer) codeownersContent() string {
	return fmt.Sprintf("# fullsend configuration is governed by org admins.\n* @%s\n", l.authenticatedUser)
}
```

Delete the old `agentWorkflowContent` and `onboardWorkflowContent` constants and the old `agentWorkflowPath`/`onboardWorkflowPath` constants from the bottom of the file (they're being replaced).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/layers/ -v`
Expected: PASS

- [ ] **Step 5: Run vet and lint**

Run: `make go-vet && make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/layers/workflows.go internal/layers/workflows_test.go
git commit -m "feat: WorkflowsLayer deploys files from scaffold"
```

---

## Task 3: Simplify EnrollmentLayer

**Files:**
- Modify: `internal/layers/enrollment.go`

The layer no longer pushes shims directly. It reads the shim content from the scaffold (for the repo-maintenance workflow to use) and verifies repos exist. The actual enrollment is handled by the repo-maintenance workflow triggered by the config.yaml push.

For now, keep the existing enrollment behavior as a fallback — the repo-maintenance workflow is a new addition, and we want the CLI to still work if the workflow hasn't been set up yet. The key change is that the shim content comes from the scaffold instead of a hardcoded string.

- [ ] **Step 1: Write the test**

Add to or create `internal/layers/enrollment_test.go`:

```go
package layers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestShimContentMatchesScaffold(t *testing.T) {
	l := &EnrollmentLayer{}
	shimContent := l.shimWorkflowContent()

	scaffoldContent, err := scaffold.TargetRepoFile(".github/workflows/fullsend.yaml")
	require.NoError(t, err)

	assert.Equal(t, string(scaffoldContent), shimContent,
		"shim content should match scaffold target-repo shim")
}

func TestShimContentHasPerRoleDispatch(t *testing.T) {
	l := &EnrollmentLayer{}
	content := l.shimWorkflowContent()
	assert.Contains(t, content, "dispatch-triage")
	assert.Contains(t, content, "dispatch-code")
	assert.Contains(t, content, "dispatch-review")
	assert.Contains(t, content, "triage.yml")
	assert.Contains(t, content, "code.yml")
	assert.Contains(t, content, "review.yml")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/layers/ -run TestShim -v`
Expected: FAIL — shim content doesn't match scaffold

- [ ] **Step 3: Update enrollment.go**

Replace `shimWorkflowContent()` to read from the scaffold:

```go
func (l *EnrollmentLayer) shimWorkflowContent() string {
	content, err := scaffold.TargetRepoFile(".github/workflows/fullsend.yaml")
	if err != nil {
		// Scaffold is embedded at compile time — this should never fail.
		panic(fmt.Sprintf("reading shim from scaffold: %v", err))
	}
	return string(content)
}
```

Add the import for `"github.com/fullsend-ai/fullsend/internal/scaffold"`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/layers/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/layers/enrollment.go internal/layers/enrollment_test.go
git commit -m "feat: EnrollmentLayer reads shim content from scaffold"
```

---

## Task 4: Add forge client methods for e2e test

**Files:**
- Modify: `internal/forge/forge.go`
- Modify: `internal/forge/github/github.go`
- Modify: `internal/forge/fake.go`

Add `MergePullRequest`, `CreateIssue`, `CloseIssue`, and `ListWorkflowRuns` to the forge interface and GitHub implementation.

- [ ] **Step 1: Add types to forge.go**

Add to `internal/forge/forge.go` after the existing `WorkflowRun` struct:

```go
// Issue represents a forge issue.
type Issue struct {
	Number int
	Title  string
	URL    string
}
```

- [ ] **Step 2: Add methods to Client interface**

Add these methods to the `Client` interface in `internal/forge/forge.go`:

```go
	// Issue operations
	CreateIssue(ctx context.Context, owner, repo, title, body string) (*Issue, error)
	CloseIssue(ctx context.Context, owner, repo string, number int) error

	// Change proposal merge
	MergePullRequest(ctx context.Context, owner, repo string, number int) error

	// Workflow run listing
	ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error)
```

- [ ] **Step 3: Implement in github.go**

Add to `internal/forge/github/github.go`:

```go
func (c *LiveClient) CreateIssue(ctx context.Context, owner, repo, title, body string) (*forge.Issue, error) {
	url := fmt.Sprintf("repos/%s/%s/issues", owner, repo)
	var result struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
	}
	resp, err := c.post(ctx, url, map[string]string{"title": title, "body": body})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parsing issue response: %w", err)
	}
	return &forge.Issue{Number: result.Number, Title: result.Title, URL: result.HTMLURL}, nil
}

func (c *LiveClient) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	url := fmt.Sprintf("repos/%s/%s/issues/%d", owner, repo, number)
	_, err := c.patch(ctx, url, map[string]string{"state": "closed"})
	return err
}

func (c *LiveClient) MergePullRequest(ctx context.Context, owner, repo string, number int) error {
	url := fmt.Sprintf("repos/%s/%s/pulls/%d/merge", owner, repo, number)
	_, err := c.put(ctx, url, map[string]string{"merge_method": "squash"})
	return err
}

func (c *LiveClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	url := fmt.Sprintf("repos/%s/%s/actions/workflows/%s/runs?per_page=10", owner, repo, workflowFile)
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	var result struct {
		WorkflowRuns []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			CreatedAt  string `json:"created_at"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing workflow runs: %w", err)
	}
	runs := make([]forge.WorkflowRun, len(result.WorkflowRuns))
	for i, r := range result.WorkflowRuns {
		runs[i] = forge.WorkflowRun{
			ID:         r.ID,
			Name:       r.Name,
			Status:     r.Status,
			Conclusion: r.Conclusion,
			HTMLURL:    r.HTMLURL,
			CreatedAt:  r.CreatedAt,
		}
	}
	return runs, nil
}
```

- [ ] **Step 4: Add stubs to fake.go**

Add to `internal/forge/fake.go`:

```go
func (f *FakeClient) CreateIssue(_ context.Context, _, _, _, _ string) (*Issue, error) {
	return &Issue{Number: 1, Title: "fake", URL: "https://fake"}, nil
}

func (f *FakeClient) CloseIssue(_ context.Context, _, _ string, _ int) error {
	return nil
}

func (f *FakeClient) MergePullRequest(_ context.Context, _, _ string, _ int) error {
	return nil
}

func (f *FakeClient) ListWorkflowRuns(_ context.Context, _, _, _ string) ([]WorkflowRun, error) {
	return nil, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./... -short`
Expected: PASS (compilation check — all interface implementations satisfy the interface)

- [ ] **Step 6: Commit**

```bash
git add internal/forge/forge.go internal/forge/github/github.go internal/forge/fake.go
git commit -m "feat: add MergePullRequest, CreateIssue, CloseIssue, ListWorkflowRuns to forge"
```

---

## Task 5: Extend e2e test with triage dispatch smoke test

**Files:**
- Modify: `e2e/admin/admin_test.go`
- Modify: `Makefile`

Add Phase 2.5 to the existing `TestAdminInstallUninstall`: merge enrollment PR, file an issue, verify the triage workflow dispatches.

- [ ] **Step 1: Increase e2e timeout in Makefile**

In `Makefile`, find the `e2e-test` target and change `-timeout 4m` to `-timeout 10m`.

- [ ] **Step 2: Add Phase 2.5 to TestAdminInstallUninstall**

In `e2e/admin/admin_test.go`, add a new phase between Phase 2 (idempotent install) and Phase 3 (uninstall):

```go
	// =========================================
	// Phase 2.5: Triage dispatch smoke test
	// =========================================
	t.Log("=== Phase 2.5: Triage Dispatch Smoke Test ===")
	runTriageDispatchSmokeTest(t, env)
```

Add the helper function:

```go
func runTriageDispatchSmokeTest(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	// Find and merge the enrollment PR so the shim workflow becomes active.
	prs, err := env.client.ListRepoPullRequests(ctx, testOrg, testRepo)
	require.NoError(t, err, "listing PRs for %s", testRepo)

	var enrollmentPR *forge.ChangeProposal
	for _, pr := range prs {
		if strings.Contains(pr.Title, "fullsend") {
			cp := pr // avoid loop variable capture
			enrollmentPR = &cp
			break
		}
	}
	require.NotNil(t, enrollmentPR, "enrollment PR should exist for %s", testRepo)

	t.Logf("Merging enrollment PR #%d: %s", enrollmentPR.Number, enrollmentPR.URL)
	err = env.client.MergePullRequest(ctx, testOrg, testRepo, enrollmentPR.Number)
	require.NoError(t, err, "merging enrollment PR")

	// Wait for GitHub to process the merge.
	time.Sleep(5 * time.Second)

	// File a test issue to trigger the shim workflow.
	issueTitle := fmt.Sprintf("e2e-triage-test-%s", env.runID)
	issueBody := "Automated e2e test issue to verify the triage dispatch pipeline."
	issue, err := env.client.CreateIssue(ctx, testOrg, testRepo, issueTitle, issueBody)
	require.NoError(t, err, "creating test issue")
	t.Logf("Created test issue #%d: %s", issue.Number, issue.URL)
	t.Cleanup(func() {
		t.Log("Closing test issue...")
		if closeErr := env.client.CloseIssue(ctx, testOrg, testRepo, issue.Number); closeErr != nil {
			t.Logf("warning: could not close test issue: %v", closeErr)
		}
	})

	// Wait for the triage workflow to be dispatched in .fullsend.
	// The shim fires on issues:opened and dispatches to triage.yml.
	t.Log("Waiting for triage workflow to be dispatched...")
	var triageRunFound bool
	for attempt := 0; attempt < 30; attempt++ {
		time.Sleep(10 * time.Second)
		runs, listErr := env.client.ListWorkflowRuns(ctx, testOrg, forge.ConfigRepoName, "triage.yml")
		if listErr != nil {
			t.Logf("Attempt %d: error listing workflow runs: %v", attempt+1, listErr)
			continue
		}
		for _, run := range runs {
			t.Logf("Attempt %d: found run %d (status: %s, conclusion: %s)", attempt+1, run.ID, run.Status, run.Conclusion)
			triageRunFound = true
			break
		}
		if triageRunFound {
			break
		}
		t.Logf("Attempt %d: no triage workflow runs found yet", attempt+1)
	}
	assert.True(t, triageRunFound, "triage workflow should have been dispatched in .fullsend repo")
}
```

- [ ] **Step 3: Update verifyInstalled to check new files**

In `verifyInstalled()`, replace the old file existence checks:

```go
	// Agent runtime files exist (from scaffold).
	for _, path := range []string{
		".github/workflows/triage.yml",
		".github/workflows/code.yml",
		".github/workflows/review.yml",
		".github/workflows/repo-maintenance.yml",
		".github/actions/fullsend/action.yml",
		".github/scripts/setup-agent-env.sh",
		"agents/triage.md",
		"harness/triage.yaml",
		"policies/triage.yaml",
		"env/triage.env",
		"env/gcp-vertex.env",
		"scripts/validate-triage.sh",
		"CODEOWNERS",
	} {
		_, err := env.client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, path)
		assert.NoError(t, err, "%s should exist in .fullsend", path)
	}
```

Remove the old checks for `agent.yaml` and `repo-onboard.yaml`.

- [ ] **Step 4: Run unit tests to verify compilation**

Run: `go test ./e2e/admin/ -run NONE -tags e2e`
Expected: PASS (compiles, runs no tests)

- [ ] **Step 5: Commit**

```bash
git add e2e/admin/admin_test.go Makefile
git commit -m "test: e2e verifies triage dispatch pipeline end-to-end"
```

---

## Task 6: Delete dispatch/ directory

**Files:**
- Delete: `dispatch/github/actions/fullsend/action.yml`
- Delete: `dispatch/github/scripts/setup-agent-env.sh`
- Delete: `dispatch/github/workflows/triage.yml`
- Delete: `dispatch/github/workflows/code.yml`
- Delete: `dispatch/github/workflows/review.yml`

Content has moved to `internal/scaffold/fullsend-repo/`.

- [ ] **Step 1: Delete dispatch/**

```bash
rm -rf dispatch/
```

- [ ] **Step 2: Verify no code references dispatch/**

Run: `grep -r "dispatch/" --include="*.go" . | grep -v vendor`
Expected: No results (or only this plan doc and design spec references)

- [ ] **Step 3: Run all tests**

Run: `make go-test && make go-vet && make lint`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git rm -r dispatch/
git commit -m "chore: remove dispatch/ (content moved to internal/scaffold/)"
```

---

## Task 7: Delete superseded plan doc

**Files:**
- Delete: `docs/superpowers/plans/2026-04-17-installer-agent-content.md`

- [ ] **Step 1: Delete the old plan**

```bash
git rm docs/superpowers/plans/2026-04-17-installer-agent-content.md
git commit -m "chore: remove superseded plan doc"
```

---

## Task 8: Update normative specs

**Files:**
- Modify: `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md`
- Modify: `docs/normative/admin-install/v1/adr-0013-enrollment/SPEC.md`
- Modify: `docs/normative/admin-install/v1/adr-0014-github-apps-and-secrets/SPEC.md`
- Delete: `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/files/agent-dispatch-v1.yaml`
- Delete: `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/files/repo-onboard-v1.yaml`

- [ ] **Step 1: Update ADR 0012 SPEC**

Replace section 2's tracked paths table with the expanded file set from the scaffold. Remove `agent.yaml` and `repo-onboard.yaml`. Add per-path requirements for each new file (sections 3.2–3.14). Update section 4 to reference `internal/scaffold/` instead of `internal/layers/workflows.go`. Delete the old normative fixtures in `files/`.

- [ ] **Step 2: Update ADR 0013 SPEC**

Update section 2: dispatch targets change from `agent.yaml` to per-role workflow files. Update section 3 constants table. Replace section 5 shim content with per-role dispatch. Update section 6 to describe repo-maintenance workflow behavior. Add new section on the repo-maintenance workflow contract.

- [ ] **Step 3: Update ADR 0014 SPEC**

Add `FULLSEND_GCP_REGION` as a repo variable in section 5's table.

- [ ] **Step 4: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add docs/normative/
git rm docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/files/agent-dispatch-v1.yaml
git rm docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/files/repo-onboard-v1.yaml
git commit -m "docs: update normative specs for scaffold and repo-maintenance"
```

---

## Task 9: Update architecture.md

**Files:**
- Modify: `docs/architecture.md`

- [ ] **Step 1: Update Agent Infrastructure section**

In the "Decided" bullet list, update:
- Replace the bullet about `workflow_dispatch` to mention per-role workflows (`triage.yml`, `code.yml`, `review.yml`) instead of a single `agent.yaml`.
- Add a bullet: "Repo maintenance: a workflow in `.fullsend/` reconciles enrollment shims in target repos when `config.yaml` changes, replacing CLI-driven enrollment ([design spec](../superpowers/specs/2026-04-17-installer-agent-content-design.md))."
- Update the layer stack bullet to note the installer deploys content from an embedded scaffold (`internal/scaffold/`).

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add docs/architecture.md
git commit -m "docs: update architecture for per-role workflows and repo-maintenance"
```

---

## Task 10: Final validation

- [ ] **Step 1: Run all tests**

```bash
make go-test
make go-vet
make lint
```

Expected: All PASS.

- [ ] **Step 2: Run e2e test (if credentials available)**

```bash
make e2e-test
```

Expected: PASS — full install, file verification, triage dispatch, uninstall.

- [ ] **Step 3: Final commit if any fixups needed**

```bash
git add -A
git commit -m "fix: address CI feedback from final validation"
```
