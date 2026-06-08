# GitLab Per-Repo Implementation Details

This document contains implementation details for GitLab per-repo support in fullsend. For the architectural decision and rationale, see [ADR 0043](../ADRs/0043-gitlab-per-repo-support.md).

## Table of Contents

1. [Dependency Graph](#dependency-graph)
2. [Phase 0: Forge Interface Preparation](#phase-0-forge-interface-preparation)
3. [Phase 1: GitLab Forge Client](#phase-1-gitlab-forge-client)
4. [Phase 2: Webhook Bridge Cloud Function](#phase-2-webhook-bridge-cloud-function)
5. [Phase 3: GitLab CI/CD Templates](#phase-3-gitlab-cicd-templates)
6. [Phase 4: CLI Changes](#phase-4-cli-changes)
7. [Phase 5: Integration and Testing](#phase-5-integration-and-testing)
8. [Security-Critical Code Paths](#security-critical-code-paths)
9. [Verification Checklist](#verification-checklist)

## Dependency Graph

```
Phase 0 (forge interface) ──┬──> Phase 1 (GitLab forge client) ──> Phase 4 (CLI changes) ──┐
                            │                                                                │
                            └──> Phase 2 (bridge function) ─────────────────────────────────>├──> Phase 5
                                                                                             │
Phase 3 (CI/CD templates) ─────────────────────────────────────────────────────────────────>─┘
```

Phases 1 and 2 depend on Phase 0 (forge interface changes). Phase 3 (CI/CD templates) has no code dependency on Phase 0 and can start immediately. Phase 4 depends on Phase 1. Phase 5 depends on all prior phases.

## Phase 0: Forge Interface Preparation

**Goal**: Prepare `forge.Client` for multi-forge support without breaking GitHub. Pure refactoring — no behavioral changes.

### New methods on `forge.Client`

Add to `internal/forge/forge.go`:

```go
CreateWebhook(ctx context.Context, owner, repo, targetURL, secretToken string, events []string) (webhookID string, err error)
DeleteWebhook(ctx context.Context, owner, repo, webhookID string) error
IsProtectedBranch(ctx context.Context, owner, repo, branch string) (bool, error)
TriggerPipeline(ctx context.Context, owner, repo, ref string, variables map[string]string) error
```

### New sentinel error

```go
var ErrNotSupported = errors.New("operation not supported by this forge")
```

GitHub returns `ErrNotSupported` for `TriggerPipeline`. GitLab returns it for `DispatchWorkflow`, `ListOrgInstallations`, `GetAppClientID`, and org-level secret/variable methods.

**Caller handling**: All callers of methods that may return `ErrNotSupported` must be audited during Phase 0 via `grep -rn 'MethodName' internal/` to build a complete call-site inventory. The expected handling per call site:
- `DispatchWorkflow` callers (dispatch layer): check for `ErrNotSupported`, fall back to `TriggerPipeline` for GitLab
- `CreateOrgSecret`/`OrgSecretExists` callers (secrets layer): skip with a log warning when `ErrNotSupported` — per-repo GitLab does not use org-level secrets
- `ListOrgInstallations`/`GetAppClientID` callers (appsetup, CLI): already gated behind `GitHubExtensions` type-assertion, so `ErrNotSupported` is never reached
- `GetLatestWorkflowRun`/`ListWorkflowRuns` callers: skip with a log warning — GitLab uses pipeline status via different mechanisms

### Extension interface

Move GitHub-only methods to an optional extension interface:

```go
type GitHubExtensions interface {
    ListOrgInstallations(ctx context.Context, org string) ([]Installation, error)
    GetAppClientID(ctx context.Context, slug string) (string, error)
}
```

Callers in `internal/cli/admin.go` and `internal/appsetup/appsetup.go` type-assert:
```go
if ghExt, ok := forgeClient.(forge.GitHubExtensions); ok {
    installations, err := ghExt.ListOrgInstallations(ctx, org)
}
```

### Forge detection

New file `internal/forge/detect.go`:

```go
func DetectForge(remoteURL string) (string, error) {
    u, err := url.Parse(remoteURL)
    if err != nil {
        return "", fmt.Errorf("invalid remote URL: %w", err)
    }
    host := strings.ToLower(u.Hostname())

    switch host {
    case "github.com":
        return "github", nil
    case "gitlab.com":
        return "gitlab", nil
    default:
        return "", fmt.Errorf("unknown forge host %q: use --forge flag for self-hosted instances", host)
    }
}
```

### Files

| Action | Path |
|--------|------|
| Modify | `internal/forge/forge.go` — add methods, sentinel, extension interface |
| Modify | `internal/forge/github/github.go` — implement new methods (webhooks/pipeline → `ErrNotSupported`; `IsProtectedBranch` → branch protection API); move `ListOrgInstallations`/`GetAppClientID` to `GitHubExtensions` |
| Modify | `internal/forge/fake.go` — implement new methods on FakeClient |
| Modify | `internal/appsetup/appsetup.go` — update `ListOrgInstallations`/`GetAppClientID` calls to use `GitHubExtensions` type-assertion |
| Modify | `internal/cli/admin.go` — update `ListOrgInstallations` calls to use `GitHubExtensions` type-assertion |
| Modify | `internal/cli/github.go` — update `GetAppClientID` calls to use `GitHubExtensions` type-assertion |
| Create | `internal/forge/detect.go` |
| Create | `internal/forge/detect_test.go` |

### Verification

`make go-test && make go-vet` — all existing tests pass unchanged.

## Phase 1: GitLab Forge Client

**Goal**: Implement `internal/forge/gitlab/gitlab.go` with the full `forge.Client` interface.

### Library

`gitlab.com/gitlab-org/api/client-go` (official GitLab Go client library).

### Constructor

```go
type LiveClient struct {
    client  *gitlab.Client
    baseURL string
}

func New(token, baseURL string) (*LiveClient, error)
```

Single-token model: the bot project access token (retrieved via OIDC/WIF from Secret Manager) serves all operations — REST, GraphQL, and MR creation.

Token resolution outside the client: `GL_TOKEN` / `GITLAB_TOKEN` / `glab auth token` — handled by the CLI for admin operations. At runtime, the bot PAT is retrieved from Secret Manager via OIDC/WIF and passed as `FULLSEND_FORGE_TOKEN`.

### Method mapping

| `forge.Client` method | GitLab API | Notes |
|---|---|---|
| `ListOrgRepos` | `Groups.ListGroupProjects` | GitLab groups = GitHub orgs |
| `GetRepo` | `Projects.GetProject` | |
| `CreateRepo` | `Projects.CreateProject` | |
| `DeleteRepo` | `Projects.DeleteProject` | |
| `CreateFile` | `RepositoryFiles.CreateFile` | |
| `CreateOrUpdateFile` | `RepositoryFiles.CreateFile` / `UpdateFile` | Check existence first |
| `GetFileContent` | `RepositoryFiles.GetFile` | |
| `DeleteFile` | `RepositoryFiles.DeleteFile` | |
| `CreateBranch` | `Branches.CreateBranch` | |
| `CommitFiles` | `Commits.CreateCommit` | Multi-file atomic commit with actions |
| `CreateChangeProposal` | `MergeRequests.CreateMergeRequest` | |
| `MergeChangeProposal` | `MergeRequests.AcceptMergeRequest` | |
| `ListRepoPullRequests` | `MergeRequests.ListProjectMergeRequests` | |
| `CreateIssue` | `Issues.CreateIssue` | |
| `CloseIssue` | `Issues.UpdateIssue` (state_event=close) | |
| `ListOpenIssues` | `Issues.ListProjectIssues` | |
| `CreateIssueComment` | `Notes.CreateIssueNote` | |
| `ListIssueComments` | `Notes.ListIssueNotes` | |
| `CreatePullRequestReview(APPROVE)` | `Notes.CreateMergeRequestNote` + `MergeRequestApprovals.ApproveMergeRequest` | |
| `CreatePullRequestReview(REQUEST_CHANGES)` | `Notes.CreateMergeRequestNote` with `<!-- fullsend:changes-requested -->` prefix | No native GitLab "request changes" |
| `CreatePullRequestReview(COMMENT)` | `Notes.CreateMergeRequestNote` | |
| `ListPullRequestReviews` | Synthesize from `Notes` + `MergeRequestApprovals` | Complex — no native review object |
| `DismissPullRequestReview` | `MergeRequestApprovals.UnapproveMergeRequest` | |
| `CreateFileOnBranch` | `RepositoryFiles.CreateFile` with branch param | |
| `CreateOrUpdateFileOnBranch` | `RepositoryFiles.CreateFile` / `UpdateFile` with branch param | |
| `GetPullRequestHeadSHA` | `MergeRequests.GetMergeRequest` → `SHA` field | |
| `ListPullRequestFiles` | `MergeRequests.ListMergeRequestDiffs` → file paths | |
| `ListPullRequestFileDiffs` | `MergeRequests.ListMergeRequestDiffs` → full diff data | |
| `MergeChangeProposal` | `MergeRequests.AcceptMergeRequest` | |
| `UpdateIssueComment` | `Notes.UpdateIssueNote` | |
| `DeleteIssueComment` | `Notes.DeleteIssueNote` | |
| `CreateRepoSecret` | `ProjectVariables.CreateVariable` (masked=true, protected=true) | |
| `CreateOrUpdateRepoVariable` | `ProjectVariables.CreateVariable` / `UpdateVariable` | |
| `RepoSecretExists` | `ProjectVariables.GetVariable` | |
| `RepoVariableExists` | `ProjectVariables.GetVariable` | |
| `GetRepoVariable` | `ProjectVariables.GetVariable` | |
| `DeleteOrgSecret` | Return `ErrNotSupported` | Per-repo only |
| `GetOrgSecretRepos` | Return `ErrNotSupported` | Per-repo only |
| `CreateOrUpdateOrgVariable` | Return `ErrNotSupported` | Per-repo only |
| `OrgVariableExists` | Return `ErrNotSupported` | Per-repo only |
| `DeleteOrgVariable` | Return `ErrNotSupported` | Per-repo only |
| `GetOrgVariableRepos` | Return `ErrNotSupported` | Per-repo only |
| `GetWorkflowRun` | Return `ErrNotSupported` | GitHub Actions-only |
| `ListWorkflowRuns` | Return `ErrNotSupported` | GitHub Actions-only |
| `GetWorkflowRunLogs` | Return `ErrNotSupported` | GitHub Actions-only |
| `GetWorkflowRunAnnotations` | Return `ErrNotSupported` | GitHub Actions-only |
| `CreateWebhook` | `ProjectHooks.AddProjectHook` | New forge method |
| `DeleteWebhook` | `ProjectHooks.DeleteProjectHook` | New forge method |
| `IsProtectedBranch` | `ProtectedBranches.GetProtectedBranch` | New forge method |
| `TriggerPipeline` | `PipelineTriggers.RunPipelineTrigger` | New forge method |
| `DispatchWorkflow` | Return `ErrNotSupported` | GitHub-only |
| `GetLatestWorkflowRun` | Return `ErrNotSupported` | GitHub-only |
| `ListOrgInstallations` | Moved to `GitHubExtensions` | Not on this client |
| `GetAppClientID` | Moved to `GitHubExtensions` | Not on this client |
| `CreateOrgSecret` | Return `ErrNotSupported` | Per-repo only |
| `OrgSecretExists` | Return `ErrNotSupported` | Per-repo only |
| `GetOrgPlan` | Return `"unknown", nil` (audit callers during implementation — if any branch on specific plan names, return `ErrNotSupported` instead) | Not needed for per-repo |
| `GetTokenScopes` | Return `nil, nil` | GitLab PATs don't use OAuth scopes |
| `MinimizeComment` | Return `nil` | No GitLab equivalent |
| `GetAuthenticatedUser` | `Users.CurrentUser` | |

### Owner/repo parameter mapping

- `owner` = group path (e.g., `mygroup` or `mygroup/subgroup`)
- `repo` = project name
- Full project path = `owner/repo`
- Internal project ID resolution: `owner/repo` → numeric ID via `Projects.GetProject`, cached

### Review semantics

GitLab has no native review object. The forge client synthesizes reviews:

**Creating reviews**:
- `APPROVE`: Create a note on the MR with the review body, then call the Approvals API to approve. The note includes a `<!-- fullsend:review:approve -->` marker.
- `REQUEST_CHANGES`: Create a note with `<!-- fullsend:changes-requested -->` prefix. GitLab has no "request changes" state, so this is tracked by convention.
- `COMMENT`: Create a note only.

**Listing reviews**: Query MR notes filtered by `<!-- fullsend:review:` markers, plus the Approvals API for approve/unapprove state. Synthesize into `forge.PullRequestReview` structs.

**Inline review comments**: GitLab supports MR discussion notes with position data (file path, line numbers). Map to/from `forge.ReviewComment` structs.

### Files

| Action | Path |
|--------|------|
| Create | `internal/forge/gitlab/gitlab.go` (~1500-2000 lines) |
| Create | `internal/forge/gitlab/gitlab_test.go` |

### What's reusable

- Client structure pattern from `internal/forge/github/github.go` (LiveClient, constructor, HTTP helpers, retry logic)
- `httptest.NewServer` testing pattern from `internal/forge/github/github_test.go`
- Error wrapping conventions from existing forge code

### What's net-new

- GitLab API method mapping (all method bodies)
- Single-token client (all methods use the same `client` — bot project access token retrieved via OIDC/WIF)
- Review synthesis logic (notes + approvals → reviews)
- Subgroup path handling

## Phase 2: Webhook Bridge Cloud Function

**Goal**: Translate GitLab webhook JSON into Pipeline Trigger API calls on enrolled projects.

### Module structure

`internal/bridge/` (separate Go module, same pattern as `internal/mint/`):

```
internal/bridge/
├── main.go           # Cloud Function entry point
├── main_test.go      # Tests
├── go.mod
└── go.sum
```

### Bridge configuration

```go
type ProjectConfig struct {
    ExpectedWebhookToken string `json:"expectedWebhookToken"`
    ProjectID            int    `json:"projectID"`
    TriggerToken         string `json:"triggerToken"`
    DefaultBranch        string `json:"defaultBranch"` // usually "main"
    GitLabBaseURL        string `json:"gitlabBaseURL"`  // e.g., "https://gitlab.com"
}
```

Per-project configs stored in GCP Secret Manager. Secret name format: `bridge-project-{sha256(project_path)}` (SHA256 is used because GitLab project paths may contain characters like `/`, `.`, and `-` that are invalid in Secret Manager secret names). Bridge reads from Secret Manager with a TTL cache (5-minute refresh).

### Request flow

```go
func HandleWebhook(w http.ResponseWriter, r *http.Request) {
    // 1. Extract X-Gitlab-Token header
    webhookToken := r.Header.Get("X-Gitlab-Token")

    // 2. Parse minimal payload to get project path
    var payload struct {
        Project struct {
            PathWithNamespace string `json:"path_with_namespace"`
        } `json:"project"`
    }
    // ... decode body ...

    // 3. Look up project config
    projectHash := sha256hex(payload.Project.PathWithNamespace)
    config, err := lookupProjectConfig(r.Context(), projectHash)

    // 4. Validate webhook token (SECURITY-CRITICAL)
    if subtle.ConstantTimeCompare(
        []byte(webhookToken),
        []byte(config.ExpectedWebhookToken),
    ) != 1 {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }

    // 4b. Replay protection — deduplicate by X-Gitlab-Event-UUID
    deliveryID := r.Header.Get("X-Gitlab-Event-UUID")
    if deliveryID != "" && deduplicationCache.Seen(deliveryID) {
        w.WriteHeader(http.StatusOK) // idempotent — acknowledge but don't re-trigger
        return
    }
    deduplicationCache.Add(deliveryID) // TTL-based, matches GitLab retry window
    // NOTE: This in-memory cache is per Cloud Function instance. Under horizontal
    // scaling, concurrent instances will not share dedup state. The downstream
    // resource_group concurrency control in GitLab CI provides a second layer of
    // deduplication for most scenarios (only one pipeline per resource group runs
    // at a time). For strict exactly-once semantics, consider a shared store
    // (e.g., Firestore or Memorystore) during implementation.

    // 5. Determine event type
    gitlabEvent := r.Header.Get("X-Gitlab-Event")
    eventType := mapEventType(gitlabEvent)

    // 6. Extract minimal payload, base64-encode
    minimalPayload := extractMinimalPayload(body, eventType)
    payloadB64 := base64.StdEncoding.EncodeToString(minimalPayload)

    // 7. Trigger pipeline (SECURITY-CRITICAL: ref is hardcoded)
    triggerPipeline(r.Context(), config, TriggerParams{
        Ref:             config.DefaultBranch, // HARDCODED — never from payload
        EventType:       eventType,
        EventPayloadB64: payloadB64,
        ResourceKey:     extractResourceKey(body, eventType),
    })
}
```

### Minimal payload extraction

The bridge extracts only the fields needed by the dispatch pipeline, not the entire webhook payload. This limits the attack surface for YAML injection (even with base64 encoding, smaller payloads are better).

For `Issue Hook`:
```json
{
  "object_attributes": {"iid": 42, "action": "update", "title": "..."},
  "labels": [{"title": "ready-to-code"}],
  "changes": {"labels": {"previous": [...], "current": [...]}}
}
```

For `Note Hook`:
```json
{
  "user": {"bot": false},
  "object_attributes": {"note": "/fs-triage", "noteable_type": "Issue", "noteable_id": 42},
  "issue": {"iid": 42, "labels": [{"title": "needs-info"}]},
  "merge_request": {"iid": 10, "source_project_id": 123, "target_project_id": 123}
}
```

For `Merge Request Hook`:
```json
{
  "object_attributes": {"iid": 10, "action": "open", "source_project_id": 123, "target_project_id": 123},
  "labels": [...]
}
```

### Event type mapping

```go
func mapEventType(gitlabEvent string) string {
    switch gitlabEvent {
    case "Issue Hook":
        return "issues"
    case "Note Hook":
        return "note"
    case "Merge Request Hook":
        return "merge_request"
    default:
        return "" // unrecognized — reject
    }
}
```

### Deployment

Extend `internal/dispatch/gcf/provisioner.go` to deploy the bridge as a second Cloud Function. The bridge runs in the same GCP project as the mint but is a separate function (compromise isolation).

New provisioner method:
```go
func (p *Provisioner) ProvisionBridge(ctx context.Context) (bridgeURL string, err error)
```

### Files

| Action | Path |
|--------|------|
| Create | `internal/bridge/main.go` (~400-500 lines) |
| Create | `internal/bridge/main_test.go` |
| Create | `internal/bridge/go.mod` |
| Modify | `internal/dispatch/gcf/provisioner.go` — add `ProvisionBridge` |
| Modify | `internal/dispatch/dispatch.go` — add `ProvisionBridge` to `Dispatcher` interface |

## Phase 3: GitLab CI/CD Templates

**Goal**: Create pipeline YAML templates that are committed to enrolled projects during install.

### Directory structure

```
internal/scaffold/fullsend-repo-gitlab/
├── .gitlab-ci.yml
├── .gitlab/
│   └── ci/
│       ├── dispatch.yml
│       ├── triage.yml
│       ├── code.yml
│       ├── review.yml
│       ├── fix.yml
│       ├── retro.yml
│       └── prioritize.yml
└── .fullsend/
    ├── config.yaml
    └── customized/
        ├── agents/.gitkeep
        ├── harness/.gitkeep
        ├── policies/.gitkeep
        ├── skills/.gitkeep
        └── scripts/.gitkeep
```

### Root pipeline (`.gitlab-ci.yml`)

```yaml
include:
  - local: '.gitlab/ci/dispatch.yml'
  - local: '.gitlab/ci/triage.yml'
  - local: '.gitlab/ci/code.yml'
  - local: '.gitlab/ci/review.yml'
  - local: '.gitlab/ci/fix.yml'
  - local: '.gitlab/ci/retro.yml'
  - local: '.gitlab/ci/prioritize.yml'

stages:
  - dispatch
  - agent

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "trigger" && $CI_COMMIT_REF_PROTECTED == "true"
```

### Dispatch routing (`.gitlab/ci/dispatch.yml`)

The dispatch job receives trigger variables from the bridge and sets the `STAGE` variable for downstream stage jobs.

```yaml
# fullsend-stage: dispatch

determine-stage:
  stage: dispatch
  image: alpine:latest
  script:
    - |
      set -euo pipefail

      # CI_DEBUG_TRACE guard
      if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
        echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
        exit 1
      fi

      # Check trigger source marker (set by bridge after webhook secret validation).
      # NOTE: WEBHOOK_VALIDATED is a pipeline-internal marker, not an independent
      # security boundary — anyone with a trigger token can set it. The real
      # security controls are the webhook secret validation in the bridge and
      # protected CI/CD variables (which prevent secret exposure on non-protected
      # branches regardless of trigger source).
      if [ "${WEBHOOK_VALIDATED}" != "true" ]; then
        echo "ERROR: WEBHOOK_VALIDATED marker not set — this pipeline was not triggered via the webhook bridge dispatch path"
        exit 1
      fi

      if [ "${CI_COMMIT_REF_PROTECTED}" != "true" ]; then
        echo "ERROR: Pipeline not running on protected branch"
        exit 1
      fi

      # Decode event payload to temp file (avoids shell expansion of payload content)
      EVENT_PAYLOAD_FILE=$(mktemp)
      trap 'rm -f "${EVENT_PAYLOAD_FILE}"' EXIT
      echo "${EVENT_PAYLOAD_B64}" | base64 -d > "${EVENT_PAYLOAD_FILE}"

      # Route event to stage
      STAGE=""
      case "${EVENT_TYPE}" in
        issues)
          # Issue routing: only label changes trigger stage dispatch.
          # Unlike GitHub (where issues.opened/edited triggers auto-triage),
          # GitLab auto-triage is initiated via /fs-triage slash commands.
          # This is intentional — GitLab's Issue Hook fires on every edit
          # (title, description, assignee, weight, etc.), creating excessive
          # noise for auto-triage. Label-based routing is more precise.
          # Check changes.labels (newly added labels only), not .labels
          # (all current labels). GitLab Issue Hook fires for every edit;
          # without this, any event on an issue with ready-to-code would
          # re-trigger the code stage.
          LABEL=$(jq -r '.changes.labels.current[]?.title // empty' "${EVENT_PAYLOAD_FILE}" | grep -E '^ready-(to-code|for-review)$' | head -1)
          PREV_LABELS=$(jq -r '.changes.labels.previous[]?.title // empty' "${EVENT_PAYLOAD_FILE}")
          if echo "${PREV_LABELS}" | grep -qxF "${LABEL}" 2>/dev/null; then
            LABEL=""  # label was already present, not newly added
          fi
          case "${LABEL}" in
            ready-to-code)   STAGE="code" ;;
            ready-for-review) STAGE="review" ;;
            *)               STAGE="" ;;
          esac
          ;;
        note)
          NOTE_BODY=$(jq -r '.object_attributes.note // empty' "${EVENT_PAYLOAD_FILE}")
          case "${NOTE_BODY}" in
            /fs-triage*)     STAGE="triage" ;;
            /fs-code*)       STAGE="code" ;;
            /fs-review*)     STAGE="review" ;;
            /fs-fix*)        STAGE="fix" ;;
            /fs-retro*)      STAGE="retro" ;;
            /fs-prioritize*) STAGE="prioritize" ;;
            *)
              NOTEABLE_TYPE=$(jq -r '.object_attributes.noteable_type // empty' "${EVENT_PAYLOAD_FILE}")
              if [ "${NOTEABLE_TYPE}" = "MergeRequest" ]; then
                # MR comment with changes-requested marker → fix stage.
                # Check this BEFORE the bot filter: the review agent posts via
                # a bot user (project access token), and its changes-requested
                # markers must trigger the fix stage.
                HAS_MARKER=$(echo "${NOTE_BODY}" | grep -cF '<!-- fullsend:changes-requested -->' || true)
                if [ "${HAS_MARKER}" -gt 0 ]; then
                  SOURCE_PROJECT=$(jq -r '.merge_request.source_project_id // empty' "${EVENT_PAYLOAD_FILE}")
                  TARGET_PROJECT=$(jq -r '.merge_request.target_project_id // empty' "${EVENT_PAYLOAD_FILE}")
                  if [ "${SOURCE_PROJECT}" = "${TARGET_PROJECT}" ]; then
                    STAGE="fix"
                  fi
                fi
              elif [ "${NOTEABLE_TYPE}" = "Issue" ]; then
                # Non-command comment on issue with needs-info label.
                # Guard: skip bot-authored comments to prevent triage
                # re-triggering on the agent's own replies.
                IS_BOT=$(jq -r '.user.bot // false' "${EVENT_PAYLOAD_FILE}")
                if [ "${IS_BOT}" != "true" ]; then
                  HAS_NEEDS_INFO=$(jq -r '.issue.labels[]?.title // empty' "${EVENT_PAYLOAD_FILE}" | grep -c '^needs-info$' || true)
                  if [ "${HAS_NEEDS_INFO}" -gt 0 ]; then
                    STAGE="triage"
                  fi
                fi
              fi
              ;;
          esac
          ;;
        merge_request)
          # No fork MR filter here: review runs on fork MRs by design
          # (read-only — it only posts comments, no code writes).
          # The fix stage has its own fork MR protection.
          ACTION=$(jq -r '.object_attributes.action // empty' "${EVENT_PAYLOAD_FILE}")
          case "${ACTION}" in
            open|update|reopen) STAGE="review" ;;
            merge)              STAGE="retro" ;;
          esac
          ;;
      esac

      if [ -z "${STAGE}" ]; then
        echo "No matching stage for event — skipping"
        # Write empty dotenv so downstream stage rules don't match any stage.
        # An empty file means STAGE is undefined (not ""), but stage rules
        # use `$STAGE == "code"` etc., which won't match either way.
        touch dispatch.env
        exit 0
      fi

      echo "STAGE=${STAGE}" >> dispatch.env
      echo "Routed to stage: ${STAGE}"
  artifacts:
    reports:
      dotenv: dispatch.env
  rules:
    - if: $EVENT_TYPE
```

### Stage pipeline example (`.gitlab/ci/code.yml`)

```yaml
# fullsend-stage: code

code:
  stage: agent
  image: ghcr.io/fullsend-ai/fullsend:latest
  needs:
    - job: determine-stage
      artifacts: true
  id_tokens:
    FULLSEND_ID_TOKEN:
      aud: "fullsend"
  variables:
    FULLSEND_FORGE: "gitlab"
  script:
    - |
      set -euo pipefail

      # CI_DEBUG_TRACE guard
      if [[ "${CI_DEBUG_TRACE:-}" == "true" ]]; then
        echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
        exit 1
      fi

      # Exchange OIDC token for GCP credentials via WIF
      gcloud auth login --cred-file=<(cat <<CRED
      {
        "type": "external_account",
        "audience": "//iam.googleapis.com/${FULLSEND_WIF_PROVIDER}",
        "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
        "token_url": "https://sts.googleapis.com/v1/token",
        "credential_source": { "file": "${FULLSEND_ID_TOKEN_FILE}" },
        "service_account_impersonation_url": "https://iam.googleapis.com/v1/projects/-/serviceAccounts/${FULLSEND_SA}:generateAccessToken"
      }
      CRED
      )

      # Read bot PAT from Secret Manager
      export FULLSEND_FORGE_TOKEN=$(gcloud secrets versions access latest \
        --secret="${FULLSEND_BOT_TOKEN_SECRET}" \
        --project="${FULLSEND_GCP_PROJECT_ID}")

      # Decode event payload
      EVENT_PAYLOAD_FILE=$(mktemp)
      trap 'rm -f "${EVENT_PAYLOAD_FILE}"' EXIT
      echo "${EVENT_PAYLOAD_B64}" | base64 -d > "${EVENT_PAYLOAD_FILE}"

      # Prepare workspace (layered content resolution)
      fullsend workspace prepare \
        --forge gitlab \
        --root .fullsend

      # Run the agent
      fullsend run \
        --stage code \
        --source-project "${CI_PROJECT_PATH}" \
        --event-type "${EVENT_TYPE}" \
        --event-payload-file "${EVENT_PAYLOAD_FILE}" \
        --forge gitlab \
        --fullsend-dir .fullsend
  resource_group: "fullsend-code-${RESOURCE_KEY}"
  rules:
    - if: $STAGE == "code"
```

### Stage-specific notes

**triage, review, retro, prioritize**: Same structure as `code.yml` — all stages use the same OIDC/WIF credential flow. The bot PAT is the single credential for all operations.

**fix**: Same as `code.yml`. Adds fork MR protection. Note: the fix stage is triggered by a Note Hook (MR comment with changes-requested marker), so MR metadata is under `.merge_request`, not `.object_attributes`:
```yaml
    - |
      # Fork MR protection — fix is triggered by Note Hook, so MR fields
      # are under .merge_request (not .object_attributes)
      SOURCE_PROJECT=$(echo "${EVENT_PAYLOAD_B64}" | base64 -d | jq -r '.merge_request.source_project_id // empty')
      TARGET_PROJECT=$(echo "${EVENT_PAYLOAD_B64}" | base64 -d | jq -r '.merge_request.target_project_id // empty')
      if [ -n "${SOURCE_PROJECT}" ] && [ -n "${TARGET_PROJECT}" ] && [ "${SOURCE_PROJECT}" != "${TARGET_PROJECT}" ]; then
        echo "Fork MR detected — skipping fix stage"
        exit 0
      fi
```

### Files

| Action | Path |
|--------|------|
| Create | `internal/scaffold/fullsend-repo-gitlab/` (entire tree) |
| Modify | `internal/scaffold/scaffold.go` — add `GitLabPerRepoScaffold()` function |

## Phase 4: CLI Changes

**Goal**: `fullsend admin install group/project --forge gitlab` works end-to-end.

### New flags

On `fullsend admin install`:
- `--forge {github|gitlab}` — auto-detected from remote URL, overridable
- `--gitlab-url` — GitLab instance URL (default: `https://gitlab.com`)
- `--bridge-project` — GCP project for the bridge Cloud Function (defaults to `--inference-project`)
- `--bridge-region` — GCP region for the bridge (default: `us-central1`)
- `--skip-bridge-deploy` — skip bridge deployment, reuse existing bridge URL
- `--bridge-url` — pre-existing bridge URL (requires `--skip-bridge-deploy`)

### Token resolution

```go
func resolveGitLabToken() (string, error) {
    if token := os.Getenv("GL_TOKEN"); token != "" {
        return token, nil
    }
    if token := os.Getenv("GITLAB_TOKEN"); token != "" {
        return token, nil
    }
    out, err := exec.Command("glab", "auth", "token").Output()
    if err == nil {
        token := strings.TrimSpace(string(out))
        if token != "" {
            return token, nil
        }
    }
    return "", fmt.Errorf("no GitLab token found: set GL_TOKEN, GITLAB_TOKEN, or run 'glab auth login'")
}
```

### Per-repo enforcement

```go
func newInstallCmd() *cobra.Command {
    // ...
    RunE: func(cmd *cobra.Command, args []string) error {
        forgeType := flagForge
        if forgeType == "" {
            // Auto-detect: try parsing as URL first (e.g., gitlab.com/group/project),
            // then fall back to inferring from well-known hosting patterns in the
            // argument (e.g., "gitlab.com/" prefix → gitlab). DetectForge expects
            // a URL, so bare "group/project" arguments won't match — --forge flag
            // is effectively required for non-URL arguments.
            forgeType, _ = forge.DetectForge(args[0])
        }

        if forgeType == "gitlab" {
            if !strings.Contains(args[0], "/") {
                return fmt.Errorf("GitLab installation supports per-repo mode only. Use: fullsend admin install group/project --forge gitlab")
            }
            return runGitLabPerRepoInstall(cmd.Context(), args[0], opts)
        }

        // existing GitHub logic...
    }
}
```

### GitLab per-repo install flow

```go
func runGitLabPerRepoInstall(ctx context.Context, target string, opts installOpts) error {
    // 1. Parse group/project
    owner, repo := splitOwnerRepo(target)

    // 2. Resolve token
    token, err := resolveGitLabToken()

    // 3. Create forge client (admin token for setup operations)
    client, err := gitlab.New(token, opts.gitlabURL)

    // 4. Validate project
    project, err := client.GetRepo(ctx, owner, repo)
    // Check user has Maintainer access
    // Check default branch exists

    // 5. Validate default branch is protected
    protected, err := client.IsProtectedBranch(ctx, owner, repo, project.DefaultBranch)
    if !protected {
        return fmt.Errorf("default branch %q is not protected — protect it before installing fullsend", project.DefaultBranch)
    }

    // 6. Check CI_DEBUG_TRACE is not enabled
    // GET /projects/:id/variables/CI_DEBUG_TRACE — if exists and value == "true", fail

    // 7. Create Project Access Token (Developer, api scope)
    // POST /projects/:id/access_tokens
    // Store in Secret Manager (not as CI/CD variable)

    // 8. Deploy bridge Cloud Function (if needed)
    bridgeURL := opts.bridgeURL
    if bridgeURL == "" && !opts.skipBridgeDeploy {
        bridgeURL, err = provisioner.ProvisionBridge(ctx)
    }

    // 9. Create pipeline trigger token
    // POST /projects/:id/triggers

    // 10. Register project with bridge
    // Store in Secret Manager: webhook secret + trigger token + project ID

    // 11. Create project webhook
    webhookSecret := generateWebhookSecret()
    client.CreateWebhook(ctx, owner, repo, bridgeURL, webhookSecret,
        []string{"issues", "note", "merge_request"})

    // 12. Commit CI/CD template files
    scaffoldFiles := scaffold.GitLabPerRepoScaffold()
    client.CommitFiles(ctx, owner, repo, project.DefaultBranch,
        "chore: add fullsend CI/CD pipeline", scaffoldFiles)

    // 13. Set protected CI/CD variables (WIF config only — no credentials)
    client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_WIF_PROVIDER", wifProviderResourceName)
    client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_SA", serviceAccountEmail)
    client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_BOT_TOKEN_SECRET", secretManagerSecretName)
    client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_GCP_PROJECT_ID", opts.inferenceProject)
    client.CreateOrUpdateRepoVariable(ctx, owner, repo, "FULLSEND_FORGE", "gitlab")
    client.CreateOrUpdateRepoVariable(ctx, owner, repo, "FULLSEND_PER_REPO_INSTALL", "true")

    // 14. Set up inference WIF (if --inference-project provided)
    // Same as GitHub per-repo
}
```

### Files

| Action | Path |
|--------|------|
| Modify | `internal/cli/admin.go` — add flags, `runGitLabPerRepoInstall()`, token resolution |
| Modify | `internal/config/config.go` — add `Forge` field, validation |

## Phase 5: Integration and Testing

### Integration wiring

- `fullsend run --forge gitlab` constructs a GitLab forge client with the bot PAT from `FULLSEND_FORGE_TOKEN` environment variable (retrieved from Secret Manager via OIDC/WIF in the pipeline script)
- `internal/dispatch/gcf/provisioner.go` deploys bridge alongside mint
- Config schema accepts `forge: gitlab` in `config.yaml`
- Forge detection integrated into CLI argument parsing

### Unit tests

| Component | Test focus |
|-----------|-----------|
| GitLab forge client | Mock HTTP responses via `httptest.NewServer`. Cover: MR creation, comment posting, label operations. Review synthesis from notes + approvals. Error handling. Subgroup paths. |
| Bridge function | Valid webhook acceptance. Invalid token rejection. Malformed payload handling. Event type mapping. `ref` hardcoding verification (assert `ref=main` in all trigger calls). Constant-time comparison. |
| Forge detection | GitHub URL → `"github"`. GitLab URL → `"gitlab"`. SSH remote → error. Self-hosted → error with flag suggestion. `--forge` override. |
| CLI | GitLab argument parsing. Per-repo enforcement for GitLab. Token resolution chain. |
| Config | `forge: gitlab` validation. Unknown forge rejection. |

### Integration tests

Mock GitLab webhook → bridge → mock Pipeline Trigger API:
1. Bridge receives valid webhook → triggers pipeline with correct variables
2. Bridge receives invalid token → rejects
3. Bridge receives unknown project → rejects
4. Full install flow with mock GitLab API (no real GitLab instance)

### E2E tests

Against GitLab.com:
1. Create a test project
2. Run `fullsend admin install group/project --forge gitlab`
3. Create an issue with `/fs-triage` comment
4. Verify triage pipeline fires and triage agent runs
5. Add `ready-to-code` label
6. Verify code pipeline fires and code agent creates MR
7. Verify review pipeline fires on MR open
8. Run `fullsend admin uninstall group/project --forge gitlab`
9. Verify cleanup (webhook removed, project access token revoked, variables deleted)

Self-hosted testing: Docker-based GitLab CE instance for version compatibility testing. Minimum GitLab version: 17.0+ (stable trigger API, CI/CD variable protection).

### FakeClient updates

Add implementations to `internal/forge/fake.go` for:
- `CreateWebhook` — record call, return fake webhook ID
- `DeleteWebhook` — record call
- `IsProtectedBranch` — configurable return value
- `TriggerPipeline` — record call with variables

## Security-Critical Code Paths

These paths require extra review attention. A bug here is a security vulnerability, not just a functional failure.

### 1. Bridge `ref` hardcoding

**File**: `internal/bridge/main.go`

The `ref` parameter in the Pipeline Trigger API call MUST be a string literal — never derived from any webhook payload field.

```go
// CORRECT — hardcoded
ref: config.DefaultBranch  // set at install time, not from payload

// WRONG — derived from payload
ref: payload.ObjectAttributes.TargetBranch  // NEVER DO THIS
```

**Consequence of bug**: Attacker pushes malicious branch, triggers pipeline on it, attempts OIDC token exchange (mitigated by WIF attribute conditions requiring `ref_protected == "true"`).

### 2. Webhook token validation

**File**: `internal/bridge/main.go`

MUST use `crypto/subtle.ConstantTimeCompare`, not `==` or `bytes.Equal`.

```go
// CORRECT
if subtle.ConstantTimeCompare([]byte(received), []byte(expected)) != 1 {
    // reject
}

// WRONG — timing side-channel
if received != expected {
    // reject
}
```

**Consequence of bug**: Attacker can determine the webhook secret byte-by-byte via timing analysis.

### 3. Protected variable creation

**File**: `internal/forge/gitlab/gitlab.go`

When creating CI/CD variables for secrets, the `Protected` flag MUST be `true`.

```go
// CORRECT
client.CreateVariable(pid, &gitlab.CreateProjectVariableOptions{
    Key:       gitlab.Ptr("FULLSEND_WIF_PROVIDER"),
    Value:     gitlab.Ptr(wifProviderResourceName),
    Masked:    gitlab.Ptr(true),
    Protected: gitlab.Ptr(true),  // MUST be true
})
```

**Consequence of bug**: Any pipeline (including on MR branches with attacker-modified `.gitlab-ci.yml`) can see WIF configuration. With `CI_DEBUG_TRACE`, this could enable OIDC token replay within the ~5 minute TTL. WIF attribute conditions (requiring `ref_protected == "true"`) provide independent defense.

### 4. `CI_DEBUG_TRACE` guard

**Files**: `internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/dispatch.yml`, all stage YAML files, `internal/cli/admin.go`

Every stage pipeline must exit early if debug tracing is detected. The install flow must validate it's not enabled.

**Consequence of bug**: All CI/CD variables (including WIF configuration) are printed to job logs. The bot PAT itself is not a CI/CD variable, but the OIDC token + WIF config could enable replay within the token's ~5 minute TTL.

### 5. Fork MR blocking

**File**: `internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fix.yml`

The fix stage must skip when `source_project_id != target_project_id`.

**Consequence of bug**: Fork MR triggers fix pipeline that pushes commits to the target project.

### 6. Payload base64 encoding

**File**: `internal/bridge/main.go`

Event payloads MUST be base64-encoded before passing as pipeline trigger variables.

**Consequence of bug**: YAML injection via issue titles or MR descriptions containing YAML metacharacters.

## Verification Checklist

- [ ] `make go-test` — all unit tests pass (existing + new)
- [ ] `make go-vet` — no issues
- [ ] `make lint` — passes
- [ ] Bridge unit test asserts `ref=main` in all trigger API calls
- [ ] Bridge unit test asserts `crypto/subtle.ConstantTimeCompare` usage
- [ ] GitLab client unit test asserts `Protected: true` on secret variable creation
- [ ] All stage YAML files contain `CI_DEBUG_TRACE` guard
- [ ] Fix stage YAML contains fork MR protection
- [ ] `fullsend admin install --dry-run testgroup/testproject --forge gitlab` shows correct plan
- [ ] `fullsend admin install testgroup --forge gitlab` returns per-repo enforcement error
- [ ] E2E: Install on GitLab.com test project → create issue → triage pipeline fires → code agent creates MR → review pipeline fires → uninstall cleans up
