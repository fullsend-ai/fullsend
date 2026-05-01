---
title: "26. GitLab Support Architecture"
status: Proposed
relates_to:
  - 0005-forge-abstraction-layer
  - 0007-per-role-github-apps
  - 0008-workflow-dispatch-for-cross-repo-dispatch
  - 0009-pull-request-target-in-shim-workflows
topics:
  - gitlab
  - forge
  - ci-cd
  - multi-platform
---

# 26. GitLab Support Architecture

Date: 2026-04-29

## Status

Proposed

## Context

Fullsend currently supports GitHub exclusively, using GitHub-specific primitives throughout the agent pipeline:
- GitHub Actions workflows for CI/CD orchestration
- GitHub Apps with fine-grained per-role permissions for authentication
- `pull_request_target` trigger for secure event handling
- `workflow_dispatch` API for cross-repository workflow triggers
- GitHub labels as state machine
- Org-level Actions secrets with repository visibility controls

Organizations using GitLab (self-hosted or GitLab.com) cannot adopt fullsend. Adding GitLab support requires:
1. Mapping GitHub primitives to GitLab equivalents
2. Maintaining security properties (untrusted MR code cannot access secrets)
3. Preserving the same agent workflow (triage → code → review → fix)
4. Keeping the architecture parallel where possible to minimize divergence

The `forge.Client` abstraction (ADR-0005) was designed for this: all forge-specific operations are isolated, making GitLab support an implementation of the interface rather than a rewrite of core logic.

## Decision

### High-Level Architecture

GitLab support mirrors the GitHub architecture where primitives map cleanly, and adapts where GitLab's model differs. The config repo convention remains `<group>/.fullsend` (GitLab groups are equivalent to GitHub orgs).

### 1. Directory Structure

**GitHub**: `.github/workflows/*.yml`
**GitLab**: `.gitlab/ci/*.yml` 

GitLab allows organizing CI/CD files in subdirectories via `include:`. The `.fullsend` config repo uses:

```
.fullsend/
  .gitlab/
    ci/
      dispatch.yml          # Main dispatcher
      triage.yml           # fullsend-stage: triage
      code.yml             # fullsend-stage: code
      review.yml           # fullsend-stage: review
      fix.yml              # fullsend-stage: fix
  templates/
    shim-pipeline.yml      # Template for enrolled repos
```

**Rationale**: GitLab supports both `.gitlab-ci.yml` at root and `.gitlab/ci/*.yml` via includes. The subdirectory approach keeps the config repo organized and parallel to GitHub's `.github/workflows/` structure.

### 2. CI/CD Pipeline Architecture

**GitHub**: Workflows triggered by events (issues, pull_request_target, issue_comment, pull_request_review)
**GitLab**: Pipelines triggered by events (issues, merge_requests, notes) with `workflow:rules`

Each stage workflow (triage, code, review, fix) is a separate `.gitlab/ci/*.yml` file with a `# fullsend-stage: <name>` comment marker (same pattern as GitHub).

**Dispatch pattern**: The `dispatch.yml` pipeline:
1. Receives trigger API call from enrolled repos
2. Scans `.gitlab/ci/*.yml` files for `# fullsend-stage:` markers
3. Uses GitLab's downstream pipeline API to trigger matching stage pipelines
4. Passes event payload and context via pipeline variables

**Key difference from GitHub**: GitLab uses parent/child pipeline relationships and pipeline trigger tokens instead of `workflow_dispatch`. The dispatch pipeline triggers child pipelines via `trigger:` keyword or API calls.

### 3. Authentication Model

**GitHub**: Per-role GitHub Apps with fine-grained repository permissions
**GitLab**: Per-role Project Access Tokens with role-based permissions

GitLab doesn't have an exact GitHub Apps equivalent, but Project Access Tokens (PATs) provide similar functionality:
- Scoped to specific projects (not user-based)
- Support role-based permissions (Guest, Reporter, Developer, Maintainer)
- Can be created programmatically via GitLab API
- Expire after configurable period (max 1 year, renewable)

**Role mapping**:
| Role    | GitLab Permission | Capabilities |
|---------|------------------|--------------|
| fullsend (orchestrator) | Maintainer | Read/write .fullsend config repo, trigger pipelines, manage project access tokens |
| triage  | Reporter | Read target repos, comment on issues |
| code    | Developer | Read/write target repos, create MRs, push branches |
| review  | Developer | Read repos, create MR reviews/comments |
| fix     | Developer | Read/write target repos, push to MR branches |

**Storage**: Project Access Token values stored as CI/CD variables:
- Group-level masked variable: `FULLSEND_DISPATCH_TOKEN` (visible to all enrolled projects)
- Project-level masked variables in `.fullsend`: 
  - `FULLSEND_TRIAGE_TOKEN`
  - `FULLSEND_CODE_TOKEN`
  - `FULLSEND_REVIEW_TOKEN`
  - `FULLSEND_FIX_TOKEN`

**Limitations vs GitHub Apps**:
- No installation flow (tokens created via API, no OAuth redirect)
- Less granular permissions (e.g., can't grant "issues:write but not code:write")
- Expiration requires rotation (GitHub App keys don't expire)
- No per-permission scoping within a role (e.g., Developer can push and approve, can't separate)

**Alternative considered**: OAuth Applications. Rejected because they're user-scoped (not project-scoped) and require user interaction, similar to GitHub App manifest flow but less suitable for automation.

### 4. Shim Pipeline Security

**GitHub**: `pull_request_target` ensures the shim workflow runs the base branch version, preventing untrusted PR code from modifying the workflow to exfiltrate secrets.

**GitLab**: No `pull_request_target` equivalent. Security achieved via:

1. **Separate shim pipeline file**: `.gitlab/fullsend-shim.yml` included in main `.gitlab-ci.yml`
2. **Protected branch enforcement**: Use `workflow:rules` to ensure shim only runs from protected branches:
   ```yaml
   workflow:
     rules:
       # Only run this pipeline from protected branches (main, etc.)
       - if: $CI_COMMIT_REF_PROTECTED == "true"
   ```
3. **No source checkout**: Shim pipeline never checks out MR source, only reads MR metadata from CI variables
4. **Trigger API call**: Shim makes HTTP request to `.fullsend` project's trigger endpoint

**Shim template** (`.gitlab/fullsend-shim.yml`):
```yaml
# fullsend shim pipeline
# Routes events to agent pipelines in .fullsend via pipeline triggers.
# Security: This file is included from main .gitlab-ci.yml and runs from 
# the protected branch only. MR authors cannot modify it.

workflow:
  rules:
    - if: $CI_COMMIT_REF_PROTECTED == "true"

.dispatch-template:
  stage: dispatch
  image: alpine:latest
  script:
    - apk add --no-cache curl jq
    - |
      # Build event payload
      PAYLOAD=$(jq -cn \
        --arg event_type "$EVENT_TYPE" \
        --arg source_project "$CI_PROJECT_PATH" \
        --arg event_json "$EVENT_JSON" \
        '{event_type: $event_type, source_project: $source_project, event_payload: $event_json}')
      
      # Trigger dispatch pipeline in .fullsend
      curl -X POST \
        -F token="$FULLSEND_DISPATCH_TOKEN" \
        -F ref=main \
        -F "variables[STAGE]=$STAGE" \
        -F "variables[EVENT_PAYLOAD]=$PAYLOAD" \
        "https://gitlab.com/api/v4/projects/${FULLSEND_PROJECT_ID}/trigger/pipeline"

dispatch-triage:
  extends: .dispatch-template
  variables:
    STAGE: triage
    EVENT_TYPE: merge_request
    EVENT_JSON: '{"merge_request": {"iid": "$CI_MERGE_REQUEST_IID", "title": "$CI_MERGE_REQUEST_TITLE"}}'
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
      when: on_success

# Similar jobs for dispatch-code, dispatch-review, dispatch-fix
```

**Key difference from GitHub**: Must rely on protected branch + workflow rules instead of dedicated trigger type. Requires project settings to enforce protected branch for `main`.

### 5. Cross-Repo Dispatch Mechanism

**GitHub**: `workflow_dispatch` API call with input parameters
**GitLab**: Pipeline trigger API with pipeline variables

**Trigger token creation**: 
- Created via GitLab API: `POST /projects/:id/triggers`
- Stored as group-level variable `FULLSEND_DISPATCH_TOKEN`
- Scoped to `.fullsend` project

**Dispatch workflow** (`.gitlab/ci/dispatch.yml`):
```yaml
# fullsend-stage: dispatch
# Dispatcher pipeline that fans out to stage pipelines

dispatch:
  stage: dispatch
  image: alpine:latest
  script:
    - apk add --no-cache yq jq curl
    - |
      # Extract inputs
      STAGE="${STAGE}"
      SOURCE_PROJECT="${SOURCE_PROJECT}"
      EVENT_PAYLOAD="${EVENT_PAYLOAD}"
      
      # Validate source project is enrolled
      PROJECT_NAME="${SOURCE_PROJECT##*/}"
      ENABLED=$(yq ".repos.\"$PROJECT_NAME\".enabled" config.yaml)
      if [ "$ENABLED" != "true" ]; then
        echo "Project not enrolled"
        exit 1
      fi
      
      # Scan for workflows with matching stage marker
      for pipeline_file in .gitlab/ci/*.yml; do
        STAGE_MARKER=$(grep -E '^# fullsend-stage:' "$pipeline_file" | head -1 | sed 's/^# fullsend-stage: *//')
        
        if [ "$STAGE_MARKER" = "$STAGE" ]; then
          echo "Triggering $pipeline_file"
          
          # Use downstream pipeline trigger
          curl -X POST \
            -H "PRIVATE-TOKEN: $FULLSEND_ORCHESTRATOR_TOKEN" \
            -F ref=main \
            -F "variables[EVENT_PAYLOAD]=$EVENT_PAYLOAD" \
            -F "variables[SOURCE_PROJECT]=$SOURCE_PROJECT" \
            "https://gitlab.com/api/v4/projects/${CI_PROJECT_ID}/trigger/pipeline"
        fi
      done
  rules:
    - if: $STAGE != null
```

**Alternative considered**: GitLab child pipelines via `trigger:` keyword. This requires pre-defining child pipeline files, whereas the dispatch pattern needs dynamic discovery. The trigger API approach matches GitHub's `workflow_dispatch` flexibility.

### 6. Stage Markers

**Pattern**: Same `# fullsend-stage: <name>` comment convention as GitHub
**Location**: Top of `.gitlab/ci/*.yml` files

This keeps the dispatch scanning logic identical across GitHub and GitLab.

### 7. Event Mapping

| GitHub Event | GitLab Event | Trigger Mechanism |
|-------------|--------------|-------------------|
| issues.labeled | issue (labels changed) | Webhook → shim pipeline |
| issue_comment.created | note (on issue) | Webhook → shim pipeline |
| pull_request_target | merge_request_event | Pipeline on protected branch |
| pull_request_review.submitted | Merge request approval | Webhook → shim pipeline |

**GitLab webhook limitations**: 
- No direct equivalent to GitHub's granular event types
- Must filter events in pipeline logic (e.g., check `$CI_MERGE_REQUEST_EVENT_TYPE`)
- Issue webhooks don't include label details in all cases (may need API call)

### 8. State Machine Primitives

**Labels**: GitLab labels work nearly identically to GitHub
- Same label names: `ready-to-code`, `ready-for-review`, `ready-for-merge`, `needs-info`
- Applied via GitLab API: `PUT /projects/:id/merge_requests/:iid/labels`
- Scoped to projects (not group-level by default)

**Approval rules**: GitLab has native approval mechanisms:
- Required approvals count
- Code owners approvals (similar to GitHub CODEOWNERS)
- Can integrate with fullsend review stage

### 9. Implementation Phases

**Phase 1: Forge abstraction**
- Implement `internal/forge/gitlab/gitlab.go` with `forge.Client` interface
- Add GitLab API client (use `go-gitlab` library)
- Implement equivalent methods for repos, MRs, labels, CI/CD variables, pipeline triggers

**Phase 2: CI/CD templates**
- Create `.gitlab/ci/dispatch.yml` dispatcher
- Create stage pipelines (triage.yml, code.yml, review.yml, fix.yml)
- Create `templates/shim-pipeline.yml` template
- Add scripts for GitLab-specific operations (parallel to `.github/scripts/`)

**Phase 3: CLI updates**
- Detect forge type (GitHub vs GitLab) from repo URL or config
- Add `--forge` flag to `fullsend admin install`
- Update `appsetup` package to create Project Access Tokens instead of GitHub Apps
- Update `layers` package to deploy `.gitlab/` directory instead of `.github/`
- Add GitLab-specific enrollment (include shim in `.gitlab-ci.yml`)

**Phase 4: Configuration**
- Add `forge: github` or `forge: gitlab` to `config.yaml`
- Support forge-specific settings (GitLab instance URL for self-hosted)
- Update config schema and validation

**Phase 5: Testing**
- E2E tests against GitLab.com or GitLab test instance
- Parallel GitHub/GitLab test suite
- Migration testing (GitHub → GitLab config equivalence)

## Consequences

### Positive

- **Multi-forge support**: Organizations on GitLab can adopt fullsend
- **Forge abstraction validated**: Implementing GitLab proves the `forge.Client` abstraction works
- **Parallel architecture**: GitLab implementation closely mirrors GitHub, reducing cognitive load
- **Same workflow**: Triage → Code → Review → Fix stages work identically from user perspective
- **Minimal CLI changes**: Forge detection mostly automatic, users specify `--forge` only during install

### Negative

- **Increased maintenance**: Two CI/CD template sets to maintain (`.github/` and `.gitlab/`)
- **Authentication complexity**: Project Access Tokens less capable than GitHub Apps, require rotation
- **Security model differences**: No `pull_request_target` equivalent requires careful protected branch configuration
- **Feature parity gaps**: Some GitHub features may not map perfectly (e.g., fine-grained permissions)
- **Testing overhead**: Need GitLab instance for E2E tests (self-hosted or GitLab.com)

### Risks

- **Protected branch misconfiguration**: If GitLab project doesn't protect `main`, MR authors could modify shim
- **Token expiration**: Project Access Tokens expire (max 1 year), need renewal automation
- **API rate limits**: GitLab.com has lower rate limits than GitHub, may need request throttling
- **Self-hosted GitLab versions**: Wide version range, feature availability varies

### Mitigations

- **Validation during install**: CLI checks that target branch is protected before enrolling repos
- **Token expiration monitoring**: Warn 30 days before expiration, provide renewal command
- **Rate limit handling**: Exponential backoff + retry in GitLab client
- **Version detection**: CLI detects GitLab version, warns about unsupported versions

## Implementation Notes

### CLI Changes Required

**Forge detection**:
```go
// internal/cli/admin.go
func detectForge(repoURL string) (string, error) {
    if strings.Contains(repoURL, "github.com") || strings.Contains(repoURL, "github.enterprise") {
        return "github", nil
    }
    if strings.Contains(repoURL, "gitlab.com") || strings.Contains(repoURL, "gitlab") {
        return "gitlab", nil
    }
    return "", fmt.Errorf("unknown forge: %s", repoURL)
}
```

**Install command changes**:
- Add `--forge {github|gitlab}` flag (auto-detected if not specified)
- Add `--gitlab-url` for self-hosted GitLab instances
- Update app setup flow to create Project Access Tokens for GitLab
- Update workflows layer to deploy `.gitlab/` instead of `.github/`

**Config schema changes**:
```yaml
# config.yaml
forge: gitlab  # or "github"
gitlab_instance_url: https://gitlab.example.com  # optional, defaults to gitlab.com
```

**New packages**:
- `internal/forge/gitlab/` - GitLab client implementation
- `internal/scaffold/fullsend-repo/.gitlab/` - GitLab CI/CD templates
- `internal/scaffold/fullsend-repo/.gitlab/scripts/` - GitLab-specific scripts

**Modified packages**:
- `internal/cli/admin.go` - Forge detection, conditional logic
- `internal/layers/workflows.go` - Deploy `.gitlab/` or `.github/` based on forge
- `internal/layers/enrollment.go` - GitLab enrollment uses `.gitlab-ci.yml` include
- `internal/appsetup/` - Create Project Access Tokens for GitLab
- `internal/config/config.go` - Add forge type field

### Security Considerations

**Protected branch requirement**: 
- Must be enforced before enrollment
- CLI validates via GitLab API: `GET /projects/:id/protected_branches/:branch`
- Error if `main` is not protected

**Token scoping**:
- Project Access Tokens scoped to specific projects, not group-wide
- Separate token per enrolled project for code/review/fix roles
- Dispatch token is group-level variable but only triggers `.fullsend` project

**Webhook authenticity**:
- GitLab webhooks include secret token for verification
- Shim pipeline should validate webhook token before triggering (implementation detail)

**MR source checkout prevention**:
- Shim pipeline must never run `git clone` or `git fetch` of MR source
- Only read CI variables like `$CI_MERGE_REQUEST_IID`, `$CI_MERGE_REQUEST_TITLE`

## Alternatives Considered

### Alternative 1: GitLab CI/CD Templates at Root

Instead of `.gitlab/ci/*.yml`, use single `.gitlab-ci.yml` with includes.

**Rejected**: Less organized than GitHub's `.github/workflows/` pattern, harder to scan for stage markers.

### Alternative 2: Group Access Tokens Instead of Project Access Tokens

Use group-level tokens for all roles instead of project-level.

**Rejected**: Less secure (group-wide permissions), harder to scope per-repo. Project Access Tokens better match GitHub Apps model.

### Alternative 3: Service Accounts with Personal Access Tokens

Create GitLab user accounts for each role (fullsend-triage, fullsend-code, etc.) and use their PATs.

**Rejected**: Requires managing user accounts, consumes user licenses, PATs are user-scoped not project-scoped. Project Access Tokens are purpose-built for automation.

### Alternative 4: Unified `.fullsend-ci.yml` Format

Define a forge-neutral CI/CD format that compiles to GitHub Actions or GitLab CI.

**Rejected**: Adds complexity, requires custom compiler, loses ability to use forge-native features. Better to maintain parallel templates that map proven GitHub patterns to GitLab.

## References

- ADR-0005: Forge abstraction layer
- ADR-0007: Per-role GitHub Apps (authentication model to replicate)
- ADR-0008: workflow_dispatch for cross-repo dispatch (pattern to replicate with triggers)
- ADR-0009: pull_request_target security model (challenge to solve)
- GitLab CI/CD documentation: https://docs.gitlab.com/ee/ci/
- GitLab Project Access Tokens: https://docs.gitlab.com/ee/user/project/settings/project_access_tokens.html
- GitLab Pipeline Triggers: https://docs.gitlab.com/ee/ci/triggers/
