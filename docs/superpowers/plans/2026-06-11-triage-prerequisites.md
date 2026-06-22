# Triage Prerequisites Action Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the triage agent's `blocked` action with a `prerequisites` action that can both reference existing blockers and create new upstream issues.

**Architecture:** Add `CreateIssuesConfig` to the config structs, update the triage result JSON schema, modify the agent prompt, and extend the post-script to create issues and handle the allowlist. The post-script reads `config.yaml` from `$GITHUB_WORKSPACE` (the config repo checkout) via `yq`.

**Tech Stack:** Go (config structs + tests), JSON Schema, bash (post-script), markdown (agent prompt + docs)

---

### Task 1: Add `CreateIssuesConfig` to config structs

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests for the new config types**

Add to `internal/config/config_test.go`:

```go
func TestOrgConfig_CreateIssues_ParseYAML(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
create_issues:
  allow_targets:
    orgs:
      - my-org
      - upstream-org
    repos:
      - other-org/specific-repo
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.CreateIssues)
	assert.Equal(t, []string{"my-org", "upstream-org"}, cfg.CreateIssues.AllowTargets.Orgs)
	assert.Equal(t, []string{"other-org/specific-repo"}, cfg.CreateIssues.AllowTargets.Repos)
}

func TestOrgConfig_CreateIssues_OmittedWhenEmpty(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos:  map[string]RepoConfig{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "create_issues")
}

func TestOrgConfig_CreateIssues_Marshal(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos:  map[string]RepoConfig{},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs:  []string{"my-org"},
				Repos: []string{"fullsend-ai/fullsend"},
			},
		},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "create_issues:")
	assert.Contains(t, string(data), "my-org")
	assert.Contains(t, string(data), "fullsend-ai/fullsend")
}

func TestOrgConfigValidate_CreateIssues_InvalidRepoFormat(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Repos: []string{"no-slash"},
			},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create_issues")
}

func TestOrgConfigValidate_CreateIssues_EmptyOrg(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs: []string{""},
			},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create_issues")
}

func TestOrgConfigValidate_CreateIssues_Valid(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs:  []string{"my-org"},
				Repos: []string{"other/repo"},
			},
		},
	}
	assert.NoError(t, cfg.Validate())
}

func TestOrgConfigValidate_CreateIssues_Nil(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	assert.NoError(t, cfg.Validate())
}

func TestNewOrgConfig_CreateIssuesDefaults(t *testing.T) {
	cfg := NewOrgConfig([]string{"repo-a"}, []string{"repo-a"}, []string{"fullsend"}, "", "my-org")
	require.NotNil(t, cfg.CreateIssues)
	assert.Contains(t, cfg.CreateIssues.AllowTargets.Orgs, "my-org")
	assert.Contains(t, cfg.CreateIssues.AllowTargets.Repos, "fullsend-ai/fullsend")
}

func TestPerRepoConfig_CreateIssues_ParseYAML(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - triage
create_issues:
  allow_targets:
    repos:
      - owner/target-repo
      - fullsend-ai/fullsend
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.CreateIssues)
	assert.Equal(t, []string{"owner/target-repo", "fullsend-ai/fullsend"}, cfg.CreateIssues.AllowTargets.Repos)
}

func TestNewPerRepoConfig_CreateIssuesDefaults(t *testing.T) {
	cfg := NewPerRepoConfig(nil, "owner/my-repo")
	require.NotNil(t, cfg.CreateIssues)
	assert.Contains(t, cfg.CreateIssues.AllowTargets.Repos, "owner/my-repo")
	assert.Contains(t, cfg.CreateIssues.AllowTargets.Repos, "fullsend-ai/fullsend")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd internal/config && go test -v -run 'CreateIssues' ./...`
Expected: compilation errors — types `CreateIssuesConfig`, `AllowTargets` not defined, `NewOrgConfig`/`NewPerRepoConfig` wrong arg count.

- [ ] **Step 3: Add the new types and update struct fields**

In `internal/config/config.go`, add the new types:

```go
// AllowTargets defines which orgs and repos agents may create issues in.
type AllowTargets struct {
	Orgs  []string `yaml:"orgs,omitempty"`
	Repos []string `yaml:"repos,omitempty"`
}

// CreateIssuesConfig controls cross-repo issue creation by agents.
type CreateIssuesConfig struct {
	AllowTargets AllowTargets `yaml:"allow_targets"`
}
```

Add `CreateIssues` field to `OrgConfig`:

```go
CreateIssues *CreateIssuesConfig `yaml:"create_issues,omitempty"`
```

Add `CreateIssues` field to `PerRepoConfig`:

```go
CreateIssues *CreateIssuesConfig `yaml:"create_issues,omitempty"`
```

- [ ] **Step 4: Update `NewOrgConfig` to accept org name and set defaults**

Change `NewOrgConfig` signature to add `org string` parameter:

```go
func NewOrgConfig(allRepos, enabledRepos, roles []string, inferenceProvider, org string) *OrgConfig {
```

Inside the function, after the existing config construction, add:

```go
if org != "" {
	cfg.CreateIssues = &CreateIssuesConfig{
		AllowTargets: AllowTargets{
			Orgs:  []string{org},
			Repos: []string{"fullsend-ai/fullsend"},
		},
	}
}
```

- [ ] **Step 5: Update `NewPerRepoConfig` to accept target repo and set defaults**

Change `NewPerRepoConfig` signature:

```go
func NewPerRepoConfig(roles []string, targetRepo string) *PerRepoConfig {
```

Inside the function, after the existing config construction, add:

```go
if targetRepo != "" {
	cfg.CreateIssues = &CreateIssuesConfig{
		AllowTargets: AllowTargets{
			Repos: []string{targetRepo, "fullsend-ai/fullsend"},
		},
	}
}
```

- [ ] **Step 6: Add validation for CreateIssues in `OrgConfig.Validate()`**

Before the `return nil` at the end of `Validate()`:

```go
if err := validateCreateIssues(c.CreateIssues); err != nil {
	return err
}
```

Add the helper:

```go
func validateCreateIssues(cfg *CreateIssuesConfig) error {
	if cfg == nil {
		return nil
	}
	for _, org := range cfg.AllowTargets.Orgs {
		if org == "" {
			return fmt.Errorf("create_issues.allow_targets.orgs contains empty string")
		}
	}
	for _, repo := range cfg.AllowTargets.Repos {
		if repo == "" || !strings.Contains(repo, "/") {
			return fmt.Errorf("create_issues.allow_targets.repos entry %q must be owner/name format", repo)
		}
	}
	return nil
}
```

Add the same `validateCreateIssues` call to `PerRepoConfig.Validate()`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd internal/config && go test -v ./...`
Expected: all tests pass including new `CreateIssues` tests.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -S -s -m "feat(config): add create_issues allowlist config (#401)

Add CreateIssuesConfig and AllowTargets types to both OrgConfig and
PerRepoConfig. NewOrgConfig populates defaults with the org and
fullsend-ai/fullsend. NewPerRepoConfig populates with the target repo
and fullsend-ai/fullsend.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

### Task 2: Fix callers of `NewOrgConfig` and `NewPerRepoConfig`

**Files:**
- Modify: `internal/cli/admin.go`
- Modify: `internal/cli/github.go`
- Modify: `internal/cli/admin_test.go`
- Modify: `internal/cli/github_test.go`
- Modify: `internal/layers/configrepo_test.go`

Task 1 changed the signatures of `NewOrgConfig` (added `org string`) and `NewPerRepoConfig` (added `targetRepo string`). All callers must be updated.

- [ ] **Step 1: Find all call sites and update them**

Update each `NewOrgConfig(...)` call to pass the `org` variable as the final argument. The `org` variable is already in scope at every call site in `admin.go` and `github.go`.

In `internal/cli/github.go:464`:
```go
orgCfg := config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
```

In `internal/cli/github.go:513`:
```go
orgCfg = config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
```

In `internal/cli/admin.go:1174`:
```go
cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
```

In `internal/cli/admin.go:1502`:
```go
cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
```

In `internal/cli/admin.go:1640`:
```go
emptyCfg := config.NewOrgConfig(nil, nil, nil, "", "")
```

In `internal/cli/admin.go:1781`:
```go
cfg := config.NewOrgConfig(repoNames, nil, defaultRoles, "", org)
```

Update each `NewPerRepoConfig(...)` call to pass `cfg.target` (the `owner/repo` string):

In `internal/cli/github.go:210`:
```go
perRepoCfg := config.NewPerRepoConfig(roles, cfg.target)
```

In `internal/cli/admin.go:647`:
```go
cfg := config.NewPerRepoConfig(roles, target)
```
(Check the variable name — it may be `cfg.target` or `target` depending on the function scope.)

Update test call sites — these typically pass `""` for the new parameters since tests don't care about create_issues defaults:

In `internal/cli/admin_test.go:583`:
```go
return config.NewOrgConfig(repoNames, enabledRepos, []string{"triage"}, "", "")
```

In `internal/cli/admin_test.go:1082`, `1123`:
```go
config.NewOrgConfig(..., "")
```

In `internal/cli/github_test.go:395`:
```go
cfg := config.NewOrgConfig([]string{"widget"}, []string{"widget"}, []string{"triage"}, "", "")
```

In `internal/config/config_test.go`, update existing tests that call `NewOrgConfig` without the org param:

`TestNewOrgConfig`: add `""` as last arg.
`TestNewOrgConfig_WithInferenceProvider`: change to `NewOrgConfig(nil, nil, nil, "vertex", "")`.
`TestNewOrgConfig_WithoutInferenceProvider`: change to `NewOrgConfig(nil, nil, nil, "", "")`.
`TestNewOrgConfig_KillSwitchDefaultFalse`: change to `NewOrgConfig(nil, nil, []string{"fullsend"}, "", "")`.

In `internal/config/config_test.go`, update existing tests for `NewPerRepoConfig`:

`TestNewPerRepoConfig_DefaultRoles`: change to `NewPerRepoConfig(nil, "")`.
`TestNewPerRepoConfig_CustomRoles`: change to `NewPerRepoConfig([]string{"triage", "review"}, "")`.
`TestPerRepoConfig_RoundTrip`: change to `NewPerRepoConfig([]string{...}, "")`.

In `internal/layers/configrepo_test.go`, update any `NewOrgConfig` / `NewPerRepoConfig` calls similarly.

- [ ] **Step 2: Run full test suite to verify**

Run: `make go-test`
Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/admin.go internal/cli/github.go internal/cli/admin_test.go internal/cli/github_test.go internal/config/config_test.go internal/layers/configrepo_test.go
git commit -S -s -m "refactor: update NewOrgConfig/NewPerRepoConfig callers for create_issues (#401)

Pass org name and target repo to config constructors so create_issues
defaults are populated at install time.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

### Task 3: Update triage result JSON schema

**Files:**
- Modify: `internal/scaffold/fullsend-repo/schemas/triage-result.schema.json`
- Test: `internal/scaffold/fullsend-repo/scripts/validate-output-schema-test.sh` (if it exists)

- [ ] **Step 1: Replace `blocked` with `prerequisites` in action enum**

In `triage-result.schema.json`, change line 12:

```json
"enum": ["insufficient", "duplicate", "sufficient", "prerequisites", "question"]
```

- [ ] **Step 2: Remove the `blocked_by` property**

Delete lines 33-37 (the `blocked_by` property).

- [ ] **Step 3: Add the `prerequisites` property definition**

Add to the `properties` object:

```json
"prerequisites": {
  "type": "object",
  "required": ["existing", "create"],
  "properties": {
    "existing": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["url"],
        "properties": {
          "url": {
            "type": "string",
            "pattern": "^https://github\\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/(issues|pull)/[0-9]+$"
          }
        },
        "additionalProperties": false
      }
    },
    "create": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["repo", "title", "body"],
        "properties": {
          "repo": {
            "type": "string",
            "pattern": "^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$"
          },
          "title": {
            "type": "string",
            "minLength": 1
          },
          "body": {
            "type": "string",
            "minLength": 1
          }
        },
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false
}
```

- [ ] **Step 4: Update the conditional validation**

Replace the `blocked` conditional (the `allOf` entry at lines 55-58):

```json
{
  "if": { "properties": { "action": { "const": "prerequisites" } }, "required": ["action"] },
  "then": {
    "required": ["prerequisites"],
    "properties": {
      "prerequisites": {
        "anyOf": [
          { "properties": { "existing": { "minItems": 1 } } },
          { "properties": { "create": { "minItems": 1 } } }
        ]
      }
    }
  }
}
```

- [ ] **Step 5: Validate the schema is valid JSON**

Run: `jq empty internal/scaffold/fullsend-repo/schemas/triage-result.schema.json`
Expected: no output (valid JSON).

- [ ] **Step 6: Test with sample inputs**

Create a temp file `/tmp/test-prereq.json`:

```json
{
  "action": "prerequisites",
  "reasoning": "Blocked by upstream work",
  "comment": "This needs upstream changes first.",
  "prerequisites": {
    "existing": [{"url": "https://github.com/org/repo/issues/42"}],
    "create": [{"repo": "org/upstream", "title": "Add X", "body": "Need X for downstream."}]
  }
}
```

Run the schema validator if available:
```bash
fullsend-check-output /tmp/test-prereq.json 2>&1 || echo "Manual validation needed"
```

Also test that a `prerequisites` result with both arrays empty is rejected, and that the old `blocked` action is rejected.

- [ ] **Step 7: Commit**

```bash
git add internal/scaffold/fullsend-repo/schemas/triage-result.schema.json
git commit -S -s -m "feat(schema): replace blocked with prerequisites action (#401)

Replace the blocked action and blocked_by field with a prerequisites
action containing existing[] and create[] arrays. At least one array
must be non-empty.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

### Task 4: Update the triage agent prompt

**Files:**
- Modify: `internal/scaffold/fullsend-repo/agents/triage.md`

- [ ] **Step 1: Replace the `blocked` action section**

Replace the "Action: `blocked`" section (lines 182-195) with:

```markdown
### Action: `prerequisites`

Progress on this issue depends on work that must happen first — either in this repository or another. Use this action when you identify specific blocking dependencies: existing issues/PRs that must be resolved, or upstream work that needs a tracking issue created.

**HARD CONSTRAINT:** Never emit `sufficient` if unresolved prerequisites exist. Use `prerequisites` instead.

The `prerequisites` object contains two arrays:

- `existing` — issues or PRs that already exist and block this work. Include the full HTML URL.
- `create` — issues that need to be filed in other repos before this work can proceed. Include the target `repo` (owner/name format), a `title`, and a `body`. Write the body for the target repo's audience — include enough technical context for upstream maintainers to understand what is needed. Use your judgment on whether to include a back-reference to the originating issue; sometimes it provides helpful context, sometimes it leaks internal details.

At least one of the two arrays must have entries.

```json
{
  "action": "prerequisites",
  "reasoning": "Brief explanation of the dependencies and why this issue cannot proceed",
  "prerequisites": {
    "existing": [
      { "url": "https://github.com/org/repo/issues/99" }
    ],
    "create": [
      {
        "repo": "org/upstream-lib",
        "title": "Add support for X",
        "body": "Technical description of what is needed and why, written for the upstream repo's maintainers."
      }
    ]
  },
  "comment": "A professional comment explaining the blocking dependencies. Link to existing blockers and describe what new issues need to be created upstream. Be specific about why each dependency must be resolved before this issue can proceed."
}
```
```

- [ ] **Step 2: Update the anti-premature-resolution rule**

In the "Anti-premature-resolution rule" paragraph (line 125), add after the existing hard constraint:

```markdown
**Anti-premature-prerequisites rule (HARD CONSTRAINT):** If your assessment identifies unresolved prerequisites — dependencies on work in other repos or unmerged changes that must land first — you MUST use `action: "prerequisites"`. Do NOT emit `action: "sufficient"` when prerequisites exist. The `sufficient` action means there are zero blockers and zero open questions.
```

- [ ] **Step 3: Update Step 3 Phase 3 to reference prerequisites**

In Phase 3 (line 108), update the last bullet:

```markdown
- **Is progress blocked on other work?** Consider whether the fix depends on an unresolved issue or unmerged PR — in this repo or another. If a developer cannot meaningfully start work until some other issue is resolved, this issue has prerequisites regardless of how clear the problem description is. If the blocking work has no tracking issue yet, you can recommend creating one via the `prerequisites` action's `create` array.
```

- [ ] **Step 4: Update Step 2c to reference prerequisites instead of blocked**

In section 2c (line 66-77), update the heading and text to say "Check existing prerequisites" instead of "Check existing blockers", and reference the `prerequisites` action instead of `blocked`.

- [ ] **Step 5: Commit**

```bash
git add internal/scaffold/fullsend-repo/agents/triage.md
git commit -S -s -m "feat(triage): replace blocked action with prerequisites in agent prompt (#401)

The triage agent can now recommend creating upstream issues via the
prerequisites action's create array, in addition to referencing existing
blockers. Adds hard constraint against emitting sufficient when
prerequisites exist.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

### Task 5: Update the post-script to handle `prerequisites`

**Files:**
- Modify: `internal/scaffold/fullsend-repo/scripts/post-triage.sh`

- [ ] **Step 1: Replace the `blocked)` case with `prerequisites)`**

Replace the entire `blocked)` case (lines 122-141) with:

```bash
  prerequisites)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'prerequisites' but no comment provided"
      exit 1
    fi

    # Read the allowlist from config.yaml. The config repo is checked out
    # at $GITHUB_WORKSPACE by the reusable workflow.
    CONFIG_FILE="${GITHUB_WORKSPACE}/config.yaml"
    if [[ ! -f "${CONFIG_FILE}" ]]; then
      # Per-repo mode: config is under .fullsend/
      CONFIG_FILE="${GITHUB_WORKSPACE}/.fullsend/config.yaml"
    fi

    ALLOWED_ORGS=""
    ALLOWED_REPOS=""
    if [[ -f "${CONFIG_FILE}" ]] && command -v yq &>/dev/null; then
      ALLOWED_ORGS=$(yq -r '.create_issues.allow_targets.orgs // [] | .[]' "${CONFIG_FILE}" 2>/dev/null || true)
      ALLOWED_REPOS=$(yq -r '.create_issues.allow_targets.repos // [] | .[]' "${CONFIG_FILE}" 2>/dev/null || true)
    fi

    # The source repo is always implicitly allowed.
    SOURCE_ORG="${REPO%%/*}"

    is_target_allowed() {
      local target_repo="$1"
      local target_org="${target_repo%%/*}"

      # Source repo is always allowed.
      if [[ "${target_repo}" == "${REPO}" ]]; then
        return 0
      fi

      # Check org allowlist.
      if [[ -n "${ALLOWED_ORGS}" ]] && echo "${ALLOWED_ORGS}" | grep -qFx "${target_org}"; then
        return 0
      fi

      # Check repo allowlist.
      if [[ -n "${ALLOWED_REPOS}" ]] && echo "${ALLOWED_REPOS}" | grep -qFx "${target_repo}"; then
        return 0
      fi

      return 1
    }

    # Process create entries: create issues, collect URLs.
    CREATE_COUNT=$(jq '.prerequisites.create // [] | length' "${RESULT_FILE}")
    CREATED_URLS=""
    FAILED_CREATES=""

    for i in $(seq 0 $((CREATE_COUNT - 1))); do
      TARGET_REPO=$(jq -r ".prerequisites.create[${i}].repo" "${RESULT_FILE}")
      ISSUE_TITLE=$(jq -r ".prerequisites.create[${i}].title" "${RESULT_FILE}")
      ISSUE_BODY=$(jq -r ".prerequisites.create[${i}].body" "${RESULT_FILE}")

      if ! is_target_allowed "${TARGET_REPO}"; then
        echo "::warning::Skipping issue creation in '${TARGET_REPO}' — not in create_issues.allow_targets"
        FAILED_CREATES="${FAILED_CREATES}
<details>
<summary>Prerequisite: ${TARGET_REPO} — ${ISSUE_TITLE}</summary>

${ISSUE_BODY}

</details>"
        continue
      fi

      echo "Creating prerequisite issue in ${TARGET_REPO}..."
      CREATED_URL=$(gh issue create --repo "${TARGET_REPO}" --title "${ISSUE_TITLE}" --body "${ISSUE_BODY}" 2>&1) || {
        echo "::warning::Failed to create issue in '${TARGET_REPO}': ${CREATED_URL}"
        FAILED_CREATES="${FAILED_CREATES}
<details>
<summary>Prerequisite: ${TARGET_REPO} — ${ISSUE_TITLE}</summary>

${ISSUE_BODY}

</details>"
        continue
      }
      echo "Created: ${CREATED_URL}"
      CREATED_URLS="${CREATED_URLS} ${CREATED_URL}"
    done

    # Collect existing URLs.
    EXISTING_COUNT=$(jq '.prerequisites.existing // [] | length' "${RESULT_FILE}")
    EXISTING_URLS=""
    for i in $(seq 0 $((EXISTING_COUNT - 1))); do
      URL=$(jq -r ".prerequisites.existing[${i}].url" "${RESULT_FILE}")
      EXISTING_URLS="${EXISTING_URLS} ${URL}"
    done

    # Merge all blocker URLs for the comment.
    ALL_URLS="${EXISTING_URLS} ${CREATED_URLS}"
    ALL_URLS=$(echo "${ALL_URLS}" | xargs)  # trim whitespace

    if [[ -n "${ALL_URLS}" ]]; then
      BLOCKER_LIST=""
      for url in ${ALL_URLS}; do
        BLOCKER_LIST="${BLOCKER_LIST}
- ${url}"
      done
      COMMENT="${COMMENT}

**Blocked by:**${BLOCKER_LIST}"
    fi

    if [[ -n "${FAILED_CREATES}" ]]; then
      COMMENT="${COMMENT}

**Could not create automatically** (file manually or update \`create_issues.allow_targets\` in config.yaml):
${FAILED_CREATES}"
    fi

    remove_label "ready-to-code"
    remove_label "needs-info"
    add_label "blocked"
    ;;
```

- [ ] **Step 2: Verify the script is syntactically valid**

Run: `bash -n internal/scaffold/fullsend-repo/scripts/post-triage.sh`
Expected: no output (valid syntax).

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/scripts/post-triage.sh
git commit -S -s -m "feat(triage): handle prerequisites action in post-script (#401)

Replace the blocked handler with prerequisites. The post-script reads
the create_issues allowlist from config.yaml, creates permitted upstream
issues via gh, and includes collapsed draft bodies for disallowed or
failed creates so humans can file them manually.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

### Task 6: Update user-facing triage docs

**Files:**
- Modify: `docs/agents/triage.md`

- [ ] **Step 1: Update control labels table**

Replace the `blocked` row:

```markdown
| `blocked` | The issue depends on prerequisites — existing issues/PRs or newly created upstream issues. The agent identified or created the blockers. |
```

- [ ] **Step 2: Add new section on `create_issues` configuration**

After the "Configuration and extension" heading, add:

```markdown
### Cross-repo issue creation

The triage agent can create prerequisite issues in other repositories when it
identifies upstream dependencies that don't have tracking issues yet. This is
controlled by the `create_issues` section in `config.yaml`:

```yaml
create_issues:
  allow_targets:
    orgs:
      - my-org
    repos:
      - upstream-org/specific-repo
```

**Defaults:** At install time, fullsend populates this with your org (in org mode)
or your repo (in per-repo mode), plus `fullsend-ai/fullsend` as an upstream target.

**When to expand the allowlist:** If your project depends on libraries or services
in other GitHub orgs and you want the triage agent to automatically file
prerequisite issues there, add those orgs or repos to `allow_targets`.

**When to restrict the allowlist:** If you don't want agents creating issues
outside your org, remove entries. If `allow_targets` is empty, automatic
prerequisite creation is disabled entirely — the agent will still identify
the dependency and include a draft issue body in its comment for a human to
file manually.

The source repo (where triage is running) is always implicitly allowed
regardless of the allowlist.
```

- [ ] **Step 3: Commit**

```bash
git add docs/agents/triage.md
git commit -S -s -m "docs: document prerequisites action and create_issues config (#401)

Update triage agent docs to explain the new prerequisites action and the
create_issues.allow_targets configuration surface.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

### Task 7: Run linters and full test suite

**Files:**
- All modified files from Tasks 1-6

- [ ] **Step 1: Run linter**

Run: `make lint`
Expected: no failures.

- [ ] **Step 2: Run Go tests**

Run: `make go-test`
Expected: all tests pass.

- [ ] **Step 3: Run vet**

Run: `make go-vet`
Expected: no issues.

- [ ] **Step 4: Fix any issues found and commit fixes**

If lint or tests reveal issues, fix them and commit.
