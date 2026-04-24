# Reconcile Org Secret Access Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `FULLSEND_DISPATCH_TOKEN` org secret repo access management from the installer into the reconcile-repos script so newly enrolled repos automatically get access.

**Architecture:** Add `organization_secrets` permission to the fullsend app so the reconcile script's app token can manage org secret visibility. Extend `reconcile-repos.sh` to grant/revoke secret access during enroll/unenroll. Remove repo ID tracking from `DispatchTokenLayer` since the reconcile loop handles it.

**Tech Stack:** Go, Bash, GitHub Actions, GitHub REST API

---

### Task 1: Add `OrganizationSecrets` to `AppPermissions` and fullsend app config

**Files:**
- Modify: `internal/forge/github/types.go:6-14` (add field to struct)
- Modify: `internal/forge/github/types.go:63-74` (add permission to fullsend role)
- Modify: `internal/forge/github/types_test.go:17-35` (assert new permission)

- [ ] **Step 1: Write the failing test**

In `internal/forge/github/types_test.go`, add an assertion to `TestAgentAppConfig_Fullsend`:

```go
assert.Equal(t, "write", cfg.Permissions.OrganizationSecrets)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/forge/github/ -run TestAgentAppConfig_Fullsend -v`
Expected: FAIL — `OrganizationSecrets` field doesn't exist

- [ ] **Step 3: Add `OrganizationSecrets` field to `AppPermissions`**

In `internal/forge/github/types.go`, add to the `AppPermissions` struct:

```go
type AppPermissions struct {
	Issues              string `json:"issues,omitempty"`
	PullRequests        string `json:"pull_requests,omitempty"`
	Checks              string `json:"checks,omitempty"`
	Contents            string `json:"contents,omitempty"`
	Workflows           string `json:"workflows,omitempty"`
	Administration      string `json:"administration,omitempty"`
	Members             string `json:"members,omitempty"`
	OrganizationSecrets string `json:"organization_secrets,omitempty"`
}
```

- [ ] **Step 4: Add the permission to the fullsend role in `AgentAppConfig`**

In `internal/forge/github/types.go`, update the `"fullsend"` case:

```go
	case "fullsend":
		base.Description = fmt.Sprintf("Fullsend orchestrator for %s", org)
		base.Permissions = AppPermissions{
			Contents:            "write",
			Workflows:           "write",
			Issues:              "read",
			PullRequests:        "write",
			Checks:              "read",
			Administration:      "write",
			Members:             "read",
			OrganizationSecrets: "write",
		}
		base.Events = []string{"issues", "push", "workflow_dispatch"}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/forge/github/ -run TestAgentAppConfig_Fullsend -v`
Expected: PASS

- [ ] **Step 6: Run all types tests**

Run: `go test ./internal/forge/github/ -run TestAgentAppConfig -v`
Expected: All pass (other roles should have empty `OrganizationSecrets`)

- [ ] **Step 7: Commit**

```bash
git add internal/forge/github/types.go internal/forge/github/types_test.go
git commit -m "feat: add organization_secrets permission to fullsend app"
```

---

### Task 2: Remove repo ID management from `DispatchTokenLayer`

**Files:**
- Modify: `internal/layers/dispatch.go:26-50` (remove `enrolledRepoIDs` field and constructor param)
- Modify: `internal/layers/dispatch.go:73-108` (simplify Install to skip `SetOrgSecretRepos`)
- Modify: `internal/layers/dispatch_test.go` (update all tests)

- [ ] **Step 1: Update test helper and tests**

In `internal/layers/dispatch_test.go`, remove `repoIDs` from the helper and all tests:

```go
func newDispatchLayer(t *testing.T, client *forge.FakeClient, token string) (*DispatchTokenLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewDispatchTokenLayer("test-org", client, token, printer, nil)
	return layer, &buf
}
```

Update `TestDispatchTokenLayer_Install_CreatesOrgSecret`:

```go
func TestDispatchTokenLayer_Install_CreatesOrgSecret(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newDispatchLayer(t, client, "ghp_secrettoken123")

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedOrgSecrets, 1)
	assert.Equal(t, "test-org", client.CreatedOrgSecrets[0].Org)
	assert.Equal(t, "FULLSEND_DISPATCH_TOKEN", client.CreatedOrgSecrets[0].Name)
	assert.Equal(t, "ghp_secrettoken123", client.CreatedOrgSecrets[0].Value)
	assert.Nil(t, client.CreatedOrgSecrets[0].RepoIDs)
}
```

Update `TestDispatchTokenLayer_Install_ReusesExistingToken` — it should no longer call `SetOrgSecretRepos`:

```go
func TestDispatchTokenLayer_Install_ReusesExistingToken(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{
			"test-org/FULLSEND_DISPATCH_TOKEN": true,
		},
	}
	layer, _ := newDispatchLayer(t, client, "")

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// No secret should be created when reusing existing.
	assert.Empty(t, client.CreatedOrgSecrets)
	// No SetOrgSecretRepos call — reconcile-repos.sh handles this now.
	assert.Empty(t, client.OrgSecretRepoIDs)
}
```

Update `TestDispatchTokenLayer_Install_Error`:

```go
func TestDispatchTokenLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"CreateOrgSecret": errors.New("permission denied")},
	}
	layer, _ := newDispatchLayer(t, client, "ghp_token")

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}
```

Update `TestDispatchTokenLayer_Install_CallsPromptFn`:

```go
func TestDispatchTokenLayer_Install_CallsPromptFn(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret does not exist
	}

	promptFn := func(ctx context.Context) (string, error) {
		return "ghp_prompted_token", nil
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewDispatchTokenLayer("test-org", client, "", printer, promptFn)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedOrgSecrets, 1)
	assert.Equal(t, "test-org", client.CreatedOrgSecrets[0].Org)
	assert.Equal(t, "FULLSEND_DISPATCH_TOKEN", client.CreatedOrgSecrets[0].Name)
	assert.Equal(t, "ghp_prompted_token", client.CreatedOrgSecrets[0].Value)
	assert.Nil(t, client.CreatedOrgSecrets[0].RepoIDs)
}
```

Update `TestDispatchTokenLayer_Install_ErrorWhenNoPromptFn`:

```go
func TestDispatchTokenLayer_Install_ErrorWhenNoPromptFn(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret does not exist
	}
	layer, _ := newDispatchLayer(t, client, "")

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch token not provided")
	assert.Contains(t, err.Error(), "does not exist")
}
```

Update `TestDispatchTokenLayer_Install_PromptFnError`:

```go
func TestDispatchTokenLayer_Install_PromptFnError(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret does not exist
	}

	promptFn := func(ctx context.Context) (string, error) {
		return "", errors.New("user cancelled")
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewDispatchTokenLayer("test-org", client, "", printer, promptFn)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user cancelled")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/layers/ -run TestDispatchToken -v`
Expected: FAIL — constructor signature mismatch

- [ ] **Step 3: Update `DispatchTokenLayer` struct and constructor**

In `internal/layers/dispatch.go`, remove `enrolledRepoIDs` from the struct and constructor:

```go
type DispatchTokenLayer struct {
	org           string
	client        forge.Client
	dispatchToken string // the PAT value to store (empty if reusing existing)
	ui            *ui.Printer
	promptFn      PromptTokenFunc
}

func NewDispatchTokenLayer(org string, client forge.Client, token string, printer *ui.Printer, promptFn PromptTokenFunc) *DispatchTokenLayer {
	return &DispatchTokenLayer{
		org:           org,
		client:        client,
		dispatchToken: token,
		ui:            printer,
		promptFn:      promptFn,
	}
}
```

- [ ] **Step 4: Simplify `Install` method**

Remove the `SetOrgSecretRepos` call from the "reuse existing" path. The method becomes:

```go
func (l *DispatchTokenLayer) Install(ctx context.Context) error {
	if l.dispatchToken != "" {
		return l.createSecret(ctx, l.dispatchToken)
	}

	exists, err := l.client.OrgSecretExists(ctx, l.org, dispatchTokenName)
	if err != nil {
		return fmt.Errorf("checking org secret %s: %w", dispatchTokenName, err)
	}

	if exists {
		l.ui.StepInfo("reusing existing dispatch token")
		return nil
	}

	if l.promptFn == nil {
		return fmt.Errorf("dispatch token not provided and org secret %s does not exist", dispatchTokenName)
	}

	token, err := l.promptFn(ctx)
	if err != nil {
		return fmt.Errorf("prompting for dispatch token: %w", err)
	}

	return l.createSecret(ctx, token)
}
```

Update `createSecret` to pass `nil` for repo IDs:

```go
func (l *DispatchTokenLayer) createSecret(ctx context.Context, token string) error {
	l.ui.StepStart("creating org secret " + dispatchTokenName)
	if err := l.client.CreateOrgSecret(ctx, l.org, dispatchTokenName, token, nil); err != nil {
		l.ui.StepFail(fmt.Sprintf("failed to create org secret %s", dispatchTokenName))
		return fmt.Errorf("creating org secret %s: %w", dispatchTokenName, err)
	}
	l.ui.StepDone("created org secret " + dispatchTokenName)
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/layers/ -run TestDispatchToken -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/layers/dispatch.go internal/layers/dispatch_test.go
git commit -m "refactor: remove repo ID management from DispatchTokenLayer

Repo access to the FULLSEND_DISPATCH_TOKEN org secret is now managed
by reconcile-repos.sh instead of the installer."
```

---

### Task 3: Remove `collectEnrolledRepoIDs` and plumbing from `admin.go`

**Files:**
- Modify: `internal/cli/admin.go:348-349` (remove call in analyze path)
- Modify: `internal/cli/admin.go:418-434` (remove call in install path)
- Modify: `internal/cli/admin.go:487-493` (update uninstall path)
- Modify: `internal/cli/admin.go:621-646` (remove param from `buildLayerStack`)
- Delete: `internal/cli/admin.go:770-782` (`collectEnrolledRepoIDs` function)

- [ ] **Step 1: Remove `enrolledRepoIDs` param from `buildLayerStack`**

Update the function signature and the `NewDispatchTokenLayer` call:

```go
func buildLayerStack(
	org string,
	client forge.Client,
	cfg *config.OrgConfig,
	printer *ui.Printer,
	user string,
	hasPrivate bool,
	enabledRepos []string,
	agentCreds []layers.AgentCredentials,
	inferenceProvider inference.Provider,
	vendorBinary bool,
	vendorFn layers.VendorFunc,
	promptTokenFn layers.PromptTokenFunc,
) *layers.Stack {
	return layers.NewStack(
		layers.NewConfigRepoLayer(org, client, cfg, printer, hasPrivate),
		layers.NewWorkflowsLayer(org, client, printer, user),
		layers.NewVendorBinaryLayer(org, client, printer, vendorBinary, vendorFn),
		layers.NewSecretsLayer(org, client, agentCreds, printer),
		layers.NewInferenceLayer(org, client, inferenceProvider, printer),
		layers.NewDispatchTokenLayer(org, client, "", printer, promptTokenFn),
		layers.NewEnrollmentLayer(org, client, enabledRepos, cfg.DisabledRepos(), printer),
	)
}
```

- [ ] **Step 2: Update call sites**

In the analyze path (~line 348-349), remove `enrolledRepoIDs`:

```go
stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, agentCreds, inferenceProvider, false, nil, nil)
```

In the install path (~line 418-434), remove `enrolledRepoIDs`:

```go
stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, agentCreds, inferenceProvider, vendorBinary, vendorFullsendBinary, func(ctx context.Context) (string, error) {
	return promptDispatchToken(ctx, client, printer, org)
})
```

In the uninstall path (~line 487-493), update `NewDispatchTokenLayer`:

```go
layers.NewDispatchTokenLayer(org, client, "", printer, nil),
```

- [ ] **Step 3: Delete `collectEnrolledRepoIDs`**

Remove the function at lines 770-782 and its corresponding call sites (the two `enrolledRepoIDs := collectEnrolledRepoIDs(...)` lines).

- [ ] **Step 4: Run all Go tests**

Run: `go test ./...`
Expected: All PASS

- [ ] **Step 5: Run vet and lint**

Run: `make lint && make go-vet`
Expected: Clean

- [ ] **Step 6: Commit**

```bash
git add internal/cli/admin.go
git commit -m "refactor: remove enrolledRepoIDs plumbing from admin CLI

The installer no longer sets per-repo access on the dispatch token
org secret. This is now handled by the reconcile-repos script."
```

---

### Task 4: Add org secret access management to `reconcile-repos.sh`

**Files:**
- Modify: `internal/scaffold/fullsend-repo/scripts/reconcile-repos.sh`

- [ ] **Step 1: Add the `DISPATCH_SECRET_NAME` constant**

Near the top of the script, after the existing constants (line 21), add:

```bash
DISPATCH_SECRET_NAME="FULLSEND_DISPATCH_TOKEN"
```

- [ ] **Step 2: Add a helper function to grant org secret access**

After the `close_pr_on_branch` function (after line 76), add:

```bash
# grant_secret_access adds a repo to the org secret's selected repositories.
grant_secret_access() {
  local repo="$1"
  local repo_id
  repo_id=$(gh api "repos/$ORG/$repo" --jq '.id' 2>/dev/null || true)
  if [ -z "$repo_id" ]; then
    echo "::warning::Could not get repo ID for $repo, skipping secret access grant"
    return
  fi
  if gh api "orgs/$ORG/actions/secrets/$DISPATCH_SECRET_NAME/repositories/$repo_id" \
    --method PUT --silent 2>/dev/null; then
    echo "  Granted $DISPATCH_SECRET_NAME access to $repo"
  else
    echo "::warning::Failed to grant $DISPATCH_SECRET_NAME access to $repo"
  fi
}

# revoke_secret_access removes a repo from the org secret's selected repositories.
revoke_secret_access() {
  local repo="$1"
  local repo_id
  repo_id=$(gh api "repos/$ORG/$repo" --jq '.id' 2>/dev/null || true)
  if [ -z "$repo_id" ]; then
    echo "::warning::Could not get repo ID for $repo, skipping secret access revoke"
    return
  fi
  gh api "orgs/$ORG/actions/secrets/$DISPATCH_SECRET_NAME/repositories/$repo_id" \
    --method DELETE --silent 2>/dev/null || true
  echo "  Revoked $DISPATCH_SECRET_NAME access from $repo"
}
```

- [ ] **Step 3: Call `grant_secret_access` after successful enrollment**

In Phase 1, after `echo "✓ $REPO already enrolled"` (line 99), add:

```bash
      grant_secret_access "$REPO"
```

After `echo "✓ Created enrollment PR for $REPO: $PR_URL"` (line 192), add:

```bash
    grant_secret_access "$REPO"
```

Also after the existing-PR update path, after the `ENROLLED=$((ENROLLED + 1))` on line 119, add:

```bash
      grant_secret_access "$REPO"
```

- [ ] **Step 4: Call `revoke_secret_access` after successful unenrollment**

In Phase 2, after `echo "✓ Created removal PR for $REPO: $PR_URL"` (line 302), add:

```bash
    revoke_secret_access "$REPO"
```

After `echo "✓ $REPO already unenrolled (no shim on default branch)"` (line 232), add:

```bash
      revoke_secret_access "$REPO"
```

- [ ] **Step 5: Test the script syntax**

Run: `bash -n internal/scaffold/fullsend-repo/scripts/reconcile-repos.sh`
Expected: No syntax errors

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: Clean

- [ ] **Step 7: Commit**

```bash
git add internal/scaffold/fullsend-repo/scripts/reconcile-repos.sh
git commit -m "feat: reconcile-repos grants/revokes org secret access

When enrolling a repo, grant it access to FULLSEND_DISPATCH_TOKEN.
When unenrolling, revoke access. This ensures newly enrolled repos
can use the dispatch token without manual secret configuration.

Relates to: https://github.com/konflux-ci/caching/actions/runs/24844542404/job/72727757382?pr=781"
```

---

### Task 5: Update `repo-maintenance.yml` comment to document the new permission requirement

**Files:**
- Modify: `internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml`

- [ ] **Step 1: Update the workflow comment**

Add a comment documenting that the fullsend app needs `organization_secrets: write` for secret access management. Update the `Reconcile target repos` step name to reflect both enrollment and secret access:

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
        uses: actions/checkout@v6

      - name: Generate app token
        id: app-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.FULLSEND_FULLSEND_CLIENT_ID }}
          private-key: ${{ secrets.FULLSEND_FULLSEND_APP_PRIVATE_KEY }}
          owner: ${{ github.repository_owner }}

      # Reconciles enrollment state and org secret access for each repo.
      # Requires the fullsend app to have organization_secrets:write permission.
      - name: Reconcile target repos
        env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          WORKSPACE: ${{ github.workspace }}
        run: bash scripts/reconcile-repos.sh "$WORKSPACE"
```

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: Clean

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml
git commit -m "docs: document organization_secrets permission requirement in repo-maintenance"
```

---

### Task 6: Final verification

- [ ] **Step 1: Run all Go tests**

Run: `make go-test`
Expected: All PASS

- [ ] **Step 2: Run vet**

Run: `make go-vet`
Expected: Clean

- [ ] **Step 3: Run lint**

Run: `make lint`
Expected: Clean
