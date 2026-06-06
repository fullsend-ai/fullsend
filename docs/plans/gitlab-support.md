# GitLab Support via Webhook Bridge

This document contains implementation details for GitLab support in fullsend. For the architectural decision and rationale, see [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md).

## Table of Contents

1. [Implementation Phases](#implementation-phases)
2. [Phase 0: Forge Interface Preparation](#phase-0-forge-interface-preparation)
3. [Phase 1: GitLab Forge Client](#phase-1-gitlab-forge-client)
4. [Phase 2: Webhook Bridge Function](#phase-2-webhook-bridge-function)
5. [Phase 3: Token Mint GitLab Support](#phase-3-token-mint-gitlab-support)
6. [Phase 4: GitLab CI/CD Templates](#phase-4-gitlab-cicd-templates)
7. [Phase 5: CLI Install/Uninstall](#phase-5-cli-installuninstall)
8. [Phase 6: Testing and Documentation](#phase-6-testing-and-documentation)
9. [Security Considerations](#security-considerations)
10. [Event Mapping](#event-mapping)
11. [Credential Rotation](#credential-rotation)

## Implementation Phases

```
Phase 0 (forge interface) ──┬──> Phase 1 (GitLab client) ──> Phase 5 (CLI)
                            │                                    ↑
                            ├──> Phase 2 (bridge function) ──────┤
                            │                                    │
                            └──> Phase 3 (mint extension) ───────┘
                                                                 ↑
                            Phase 4 (CI/CD templates) ───────────┘
```

Phases 1, 2, and 3 can proceed in parallel after Phase 0 completes. Phase 4 depends on the mint extension (Phase 3) for OIDC token exchange. Phase 5 integrates everything. Phase 6 is end-to-end validation.

## Phase 0: Forge Interface Preparation

**Goal**: Prepare `forge.Client` for multi-forge without breaking GitHub. Pure refactoring — no GitLab code.

### New methods on `forge.Client`

Add to `internal/forge/forge.go`:

```go
CreateWebhook(ctx context.Context, owner, repo, targetURL, secretToken string, events []string) (webhookID string, err error)
DeleteWebhook(ctx context.Context, owner, repo, webhookID string) error

CreateRoleCredential(ctx context.Context, owner, repo, roleName string, scopes []string, expiresAt time.Time) (credential string, credentialID string, err error)
RevokeRoleCredential(ctx context.Context, owner, repo, credentialID string) error

IsProtectedBranch(ctx context.Context, owner, repo, branch string) (bool, error)
```

Also add to `internal/forge/forge.go`:

```go
// ErrNotSupported is returned by forge methods that are conceptually inapplicable
// for a given forge implementation (as opposed to methods that are silently no-ops).
var ErrNotSupported = errors.New("operation not supported by this forge")
```

GitHub implementations:

- `CreateWebhook` / `DeleteWebhook`: no-op, return `"", nil` / `nil`. GitHub uses in-repo shim workflows, not webhooks. Returns `nil` (not `forge.ErrNotSupported`) because the operation is silently unnecessary — callers can safely call these without checking for support.
- `CreateRoleCredential` / `RevokeRoleCredential`: return `"", "", forge.ErrNotSupported`. GitHub uses Apps (credentials are managed outside the forge interface via the token mint). Returns `forge.ErrNotSupported` because the operation is conceptually inapplicable — callers must handle this and use a different credential path.
- `IsProtectedBranch`: call GitHub Branch Protection API.

### Extension interface for GitHub-specific methods

Move GitHub-only methods out of the main interface:

```go
type GitHubExtensions interface {
    ListOrgInstallations(ctx context.Context, org string) ([]Installation, error)
    GetAppClientID(ctx context.Context, slug string) (string, error)
}
```

Update callers in `internal/cli/admin.go` and `internal/appsetup/appsetup.go` to type-assert:

```go
if ghClient, ok := client.(forge.GitHubExtensions); ok {
    installations, err := ghClient.ListOrgInstallations(ctx, org)
    // ...
}
```

### Forge detection

Create `internal/forge/detect.go`:

```go
func DetectForge(remoteURL string) (string, error) {
    u, err := url.Parse(remoteURL)
    if err != nil {
        return "", fmt.Errorf("invalid URL: %w", err)
    }
    host := strings.ToLower(u.Hostname())

    switch host {
    case "github.com":
        return "github", nil
    case "gitlab.com":
        return "gitlab", nil
    default:
        return "", fmt.Errorf("unknown forge host %q — use --forge to specify", host)
    }
}
```

For self-hosted instances, `--forge gitlab --gitlab-url https://gitlab.example.com` overrides detection.

**SSH remote limitation**: `url.Parse` does not handle SSH remote URLs (`git@gitlab.com:group/project.git`), which are not valid RFC 3986 URLs. Auto-detection returns an error for SSH remotes; operators must pass `--forge` explicitly. Add SSH URL parsing as a follow-up if needed.

### CLI flag additions

Add to `fullsend admin install`:

- `--forge {github|gitlab}` — auto-detected from remote URL if not specified.
- `--gitlab-url` — GitLab instance URL for self-hosted (defaults to `https://gitlab.com`).

### Files modified

- `internal/forge/forge.go` — add new methods, define `GitHubExtensions`
- `internal/forge/github/github.go` — implement new methods (no-ops where applicable), implement `GitHubExtensions`
- `internal/forge/detect.go` — new file
- `internal/cli/admin.go` — add `--forge` and `--gitlab-url` flags, update `ListOrgInstallations` / `GetAppClientID` callers to type-assert
- `internal/cli/github.go` — update `ListOrgInstallations` caller (line 912) to type-assert against `GitHubExtensions`
- `internal/appsetup/appsetup.go` — update `GetAppClientID` callers to type-assert
- `internal/forge/fake.go` — implement `GitHubExtensions` on `FakeClient` to satisfy the new interface split
- `internal/forge/fake_test.go` — update tests that exercise `ListOrgInstallations` / `GetAppClientID`

### Verification

- `make go-test` passes (no behavioral changes for GitHub).
- `make go-vet` clean.
- All existing E2E tests pass unchanged.

## Phase 1: GitLab Forge Client

**Goal**: Implement `internal/forge/gitlab/gitlab.go` with the full `forge.Client` interface.

### Library

Use `gitlab.com/gitlab-org/api/client-go` (official GitLab Go client library).

### Constructor

```go
type LiveClient struct {
    client    *gitlab.Client
    baseURL   string
    forgeType string
}

func New(token, baseURL string) (*LiveClient, error) {
    if baseURL == "" {
        baseURL = "https://gitlab.com"
    }
    // Strip trailing slash before appending /api/v4 to avoid double slashes
    // when operators pass --gitlab-url with a trailing slash.
    baseURL = strings.TrimRight(baseURL, "/")
    client, err := gitlab.NewClient(token, gitlab.WithBaseURL(baseURL+"/api/v4"))
    if err != nil {
        return nil, fmt.Errorf("creating GitLab client: %w", err)
    }
    return &LiveClient{client: client, baseURL: baseURL, forgeType: "gitlab"}, nil
}
```

### Method mapping

| `forge.Client` method | GitLab API | Notes |
|---|---|---|
| `ListOrgRepos` | `Groups.ListGroupProjects` | GitLab groups = GitHub orgs |
| `GetRepo` | `Projects.GetProject` | |
| `CreateRepo` | `Projects.CreateProject` | Namespace = group ID |
| `DeleteRepo` | `Projects.DeleteProject` | |
| `CreateFile` | `RepositoryFiles.CreateFile` | |
| `CreateOrUpdateFile` | Get + Create or Update | |
| `GetFileContent` | `RepositoryFiles.GetRawFile` | |
| `DeleteFile` | `RepositoryFiles.DeleteFile` | |
| `CommitFiles` | `Commits.CreateCommit` with actions | Multiple file operations in one commit |
| `CreateBranch` | `Branches.CreateBranch` | |
| `CreateChangeProposal` | `MergeRequests.CreateMergeRequest` | head → source_branch, base → target_branch |
| `ListRepoPullRequests` | `MergeRequests.ListProjectMergeRequests` | |
| `MergeChangeProposal` | `MergeRequests.AcceptMergeRequest` | Squash merge |
| `CreateIssue` | `Issues.CreateIssue` | |
| `CloseIssue` | `Issues.UpdateIssue` with `StateEvent: "close"` | |
| `ListOpenIssues` | `Issues.ListProjectIssues` with state=opened | |
| `CreateIssueComment` | `Notes.CreateIssueNote` | |
| `ListIssueComments` | `Notes.ListIssueNotes` | |
| `UpdateIssueComment` | `Notes.UpdateIssueNote` | |
| `MinimizeComment` | No equivalent — return `nil` | |
| `CreatePullRequestReview` | Note + Approval (see below) | |
| `ListPullRequestReviews` | Synthesize from Notes + Approvals | |
| `DismissPullRequestReview` | `MergeRequestApprovals.UnapproveMergeRequest` | |
| `GetPullRequestHeadSHA` | `MergeRequests.GetMergeRequest` → `.SHA` | |
| `ListPullRequestFiles` | `MergeRequests.ListMergeRequestDiffs` | |
| `ListPullRequestFileDiffs` | `MergeRequests.ListMergeRequestDiffs` with full diff | |
| `CreateRepoSecret` | `ProjectVariables.CreateVariable` (masked + protected) | |
| `RepoSecretExists` | `ProjectVariables.GetVariable` | |
| `CreateOrUpdateRepoVariable` | Get + Create or Update variable | |
| `CreateOrgSecret` | `GroupVariables.CreateVariable` (masked + protected) | |
| `OrgSecretExists` | `GroupVariables.GetVariable` | |
| `DispatchWorkflow` | `PipelineTriggers.RunPipelineTrigger` | |
| `GetLatestWorkflowRun` | `Pipelines.ListProjectPipelines` (first result) | |
| `GetWorkflowRun` | `Pipelines.GetPipeline` | |
| `GetWorkflowRunLogs` | `Jobs.GetTraceFile` for each job in pipeline | |
| `ListWorkflowRuns` | `Pipelines.ListProjectPipelines` | |
| `GetAuthenticatedUser` | `Users.CurrentUser` | |
| `GetTokenScopes` | Return `nil` — not needed for GitLab PATs | |
| `GetOrgPlan` | Return `"unknown"` | |
| `CreateWebhook` | `ProjectHooks.AddProjectHook` | |
| `DeleteWebhook` | `ProjectHooks.DeleteProjectHook` | |
| `CreateRoleCredential` | `ProjectAccessTokens.CreateProjectAccessToken` | Returns token + stringified token ID |
| `RevokeRoleCredential` | `ProjectAccessTokens.RevokeProjectAccessToken` | Parses credential ID back to int |
| `IsProtectedBranch` | `ProtectedBranches.GetProtectedBranch` | |

### Pull request review semantics

GitLab has no single "review" object. The implementation synthesizes reviews from notes and approvals:

**`CreatePullRequestReview`**:
- If `event == "APPROVE"`: create a note with the review body, then call `MergeRequestApprovals.ApproveMergeRequest`.
- If `event == "REQUEST_CHANGES"`: create a note with the review body prefixed with `<!-- fullsend:changes-requested -->` on the first line. This hidden HTML comment acts as a machine-readable marker for the dispatch pipeline to detect and auto-trigger the fix agent. Do not approve.
- If `event == "COMMENT"`: create a note only.
- Inline review comments: create MR discussion notes with position data (file, line).

**`ListPullRequestReviews`**:
- List MR notes and the approval state.
- Synthesize `PullRequestReview` structs: approved = APPROVE, unapproved + review note = REQUEST_CHANGES, note only = COMMENT.

### Owner/repo parameter mapping

The `forge.Client` interface uses `owner` and `repo` string parameters. For GitLab:

- `owner` = group path (e.g., `mygroup` or `mygroup/subgroup`)
- `repo` = project name
- Full project path = `owner/repo`
- Internally, most GitLab API calls use a project ID (integer). The client resolves `owner/repo` to a project ID on first use and caches it.

### Files created

- `internal/forge/gitlab/gitlab.go` — full `forge.Client` implementation
- `internal/forge/gitlab/gitlab_test.go` — tests using `httptest.NewServer`

### Verification

- All methods have unit tests with mocked GitLab API responses.
- `make go-test` passes.

## Phase 2: Webhook Bridge Function

**Goal**: Deploy a Cloud Function that translates GitLab webhooks to pipeline trigger API calls.

### Function implementation

Create `internal/bridge/main.go`:

```go
type BridgeConfig struct {
    GitLabBaseURL string            // e.g., "https://gitlab.com"
    TriggerToken  string            // pipeline trigger token for .fullsend project
    ProjectID     string            // .fullsend project ID
    WebhookTokens map[string]string // sha256(project_path) -> expected token
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
    // 1. Extract webhook secret from X-Gitlab-Token header
    // 2. Extract source project path from payload
    // 3. Validate webhook token (constant-time comparison via crypto/subtle)
    // 4. Determine event type from X-Gitlab-Event header
    // 5. Extract relevant payload fields (including issue IID for resource_group)
    // 6. Base64-encode the payload
    // 7. Call Pipeline Trigger API:
    //    POST /api/v4/projects/{projectID}/trigger/pipeline
    //    Form body: token={triggerToken}&ref=main&variables[EVENT_TYPE]={type}&variables[SOURCE_PROJECT]={path}&variables[EVENT_PAYLOAD_B64]={b64}&variables[WEBHOOK_VALIDATED]=true&variables[ISSUE_IID]={iid}&variables[MR_IID]={mrIid}
    //    WEBHOOK_VALIDATED=true is set only after the bridge's constant-time token comparison
    //    succeeds — passing the raw webhook token as a trigger variable would expose it in
    //    the GitLab CI/CD UI to Developer+ project members. The pipeline trusts this flag
    //    trust boundary: the trigger token is a protected CI/CD variable accessible to
    //    .fullsend project Maintainers, who are already trusted with pipeline code and config.
    //    RESOURCE_KEY: always-populated concurrency key for resource_group (GitLab CI/CD
    //                  does not support bash ${VAR:-default} expansion in job keywords).
    //                  Format: "issue-{iid}" for Issue Hook (object_attributes.iid),
    //                  "mr-{iid}" for Merge Request Hook (object_attributes.iid),
    //                  "note-issue-{noteable_iid}" or "note-mr-{noteable_iid}" for Note Hook
    //                  (object_attributes.noteable_type + object_attributes.noteable_iid).
    //                  The bridge must explicitly extract noteable_type and noteable_iid from
    //                  the raw Note Hook payload before constructing MinimalPayload.
    // 8. ref=main is HARDCODED — never from payload
}
```

### Webhook token validation

```go
func validateWebhookToken(received, expected string) bool {
    receivedHash := sha256.Sum256([]byte(received))
    expectedHash := sha256.Sum256([]byte(expected))
    return subtle.ConstantTimeCompare(receivedHash[:], expectedHash[:]) == 1
}
```

### Event type mapping

The bridge extracts event type from the `X-Gitlab-Event` header:

| `X-Gitlab-Event` | Mapped `EVENT_TYPE` |
|---|---|
| `Issue Hook` | `issues` |
| `Note Hook` | `note` |
| `Merge Request Hook` | `merge_request` |
| `Pipeline Hook` | `pipeline` (ignored by dispatch) |

### Payload extraction

The bridge extracts a minimal payload (same principle as GitHub's dispatch.yml — reduce injection surface):

```go
type MinimalPayload struct {
    Issue        *MinimalIssue        `json:"issue,omitempty"`
    MergeRequest *MinimalMergeRequest `json:"merge_request,omitempty"`
    Note         *MinimalNote         `json:"note,omitempty"`
    Action       string               `json:"action"`
    Labels       []MinimalLabel       `json:"labels,omitempty"`
    Changes      *MinimalChanges      `json:"changes,omitempty"`
}

type MinimalChanges struct {
    Labels     *MinimalLabelChanges `json:"labels,omitempty"`
    // LastCommit is populated by the bridge when the MR webhook includes new commits
    // (i.e., object_attributes.oldrev is present). Non-nil means new commits were pushed.
    LastCommit *MinimalLastCommit   `json:"last_commit,omitempty"`
    // Title and Description are non-nil when the corresponding field changed in an
    // Issue Hook update event (changes.title / changes.description present in payload).
    // Used by determine-stage to detect content edits vs. metadata-only updates.
    Title       *struct{} `json:"title,omitempty"`
    Description *struct{} `json:"description,omitempty"`
}

type MinimalLabelChanges struct {
    Current  []MinimalLabel `json:"current"`
    Previous []MinimalLabel `json:"previous"`
}

type MinimalLastCommit struct {
    // ID is the previous HEAD SHA (object_attributes.oldrev), present only when
    // new commits are pushed to the MR. Used by determine-stage to distinguish
    // commit pushes from metadata updates.
    ID string `json:"id"`
}

type MinimalIssue struct {
    IID    int    `json:"iid"`
    WebURL string `json:"web_url"`
}

type MinimalMergeRequest struct {
    IID          int    `json:"iid"`
    WebURL       string `json:"web_url"`
    SourceBranch string `json:"source_branch"`
    TargetBranch string `json:"target_branch"`
    // SourceProject and TargetProject are composed by the bridge from GitLab's
    // webhook payload — they are not top-level fields in GitLab's MR webhook.
    // SourceProject: object_attributes.source.path_with_namespace
    // TargetProject: project.path_with_namespace
    SourceProject string `json:"source_project"`
    TargetProject string `json:"target_project"`
}

type MinimalNote struct {
    Body   string            `json:"body"`
    Author MinimalNoteAuthor `json:"author"`
}

type MinimalNoteAuthor struct {
    // Username is populated from object_attributes.author.username (Note Hook events).
    // Required for validating the <!-- fullsend:changes-requested --> marker against
    // the configured review bot account.
    Username string `json:"username"`
}

type MinimalLabel struct {
    Name string `json:"name"`
}
```

Note body is truncated to 4096 characters (same as GitHub dispatch).

### Deployment

Extend `internal/dispatch/gcf/provisioner.go` to deploy the bridge as a second Cloud Function in the same GCP project. The bridge function:

- Has a public HTTPS endpoint (reachable from GitLab.com or self-hosted instances).
- Environment variables: `GITLAB_BASE_URL`, `TRIGGER_TOKEN`, `PROJECT_ID`.
- Webhook tokens loaded from Secret Manager with a TTL cache (5-minute refresh interval). This ensures revoked enrollments take effect within minutes rather than requiring a cold-start.
- No access to PEM secrets (separate function from mint).

### Files created

- `internal/bridge/main.go` — bridge Cloud Function
- `internal/bridge/main_test.go` — tests: valid webhook, invalid token, malformed payload, event type mapping, ref hardcoding verification
- `internal/bridge/go.mod` — separate module (same pattern as `internal/mint/`)

### Files modified

- `internal/dispatch/gcf/provisioner.go` — add bridge deployment

### Verification

- Unit tests cover all event types, token validation, and ref hardcoding.
- Integration test: mock GitLab webhook → bridge → mock Pipeline Trigger API.

## Phase 3: Token Mint GitLab Support

**Goal**: Extend the mint to accept GitLab OIDC tokens and return GitLab credentials.

### OIDC claim normalization

GitLab OIDC tokens use different claim names:

| Purpose | GitHub claim | GitLab claim |
|---|---|---|
| Repository | `repository` | `project_path` |
| Owner/org | `repository_owner` | `namespace_path` |
| Workflow ref | `job_workflow_ref` | `ci_config_ref_uri` |
| Issuer | `https://token.actions.githubusercontent.com` | Instance URL (e.g., `https://gitlab.com`) |

Add a normalization layer in `internal/mint/main.go`:

```go
type NormalizedClaims struct {
    Issuer      string
    Repository  string
    Owner       string
    WorkflowRef string
    Audience    string
    // RefProtected indicates the pipeline ran on a protected ref.
    // For GitLab: extracted from the dedicated `ref_protected` boolean claim.
    //   The mint rejects GitLab requests where RefProtected is false.
    // For GitHub: not set here — GitHub's protected-ref enforcement uses a
    //   separate path (validating job_workflow_ref against the .fullsend repo's
    //   default branch). GitHub callers should not use this field.
    RefProtected bool
}

// forgeType is determined from ALLOWED_ISSUERS configuration, not string matching,
// so self-hosted GitLab instances with non-"gitlab" domains are handled correctly.
func normalizeClaims(rawClaims map[string]interface{}, issuer string, forge ForgeType) (*NormalizedClaims, error) {
    var keys struct{ repo, owner, workflow, aud string }
    switch forge {
    case ForgeGitLab:
        keys = struct{ repo, owner, workflow, aud string }{
            "project_path", "namespace_path", "ci_config_ref_uri", "aud",
        }
    default: // ForgeGitHub
        keys = struct{ repo, owner, workflow, aud string }{
            "repository", "repository_owner", "job_workflow_ref", "aud",
        }
    }

    getString := func(key string) (string, error) {
        v, ok := rawClaims[key]
        if !ok {
            return "", fmt.Errorf("missing claim %q", key)
        }
        s, ok := v.(string)
        if !ok {
            return "", fmt.Errorf("claim %q is not a string", key)
        }
        return s, nil
    }

    // Per RFC 7519 §4.1.3, "aud" may be a single string or an array of strings.
    // When it is an array, check whether the expected audience is present
    // rather than blindly returning the first element.
    // Phase 3 note: This function should also be used for GitHub token validation
    // to ensure consistent audience handling across both forges, unless GitHub
    // continues to use simpler single-string audience (in which case document why).
    getAudience := func(key, expected string) (string, error) {
        v, ok := rawClaims[key]
        if !ok {
            return "", fmt.Errorf("missing claim %q", key)
        }
        switch aud := v.(type) {
        case string:
            if aud != expected {
                return "", fmt.Errorf("expected audience %q, got %q", expected, aud)
            }
            return aud, nil
        case []interface{}:
            if len(aud) == 0 {
                return "", fmt.Errorf("claim %q is an empty array", key)
            }
            for _, elem := range aud {
                s, ok := elem.(string)
                if !ok {
                    continue
                }
                if s == expected {
                    return s, nil
                }
            }
            return "", fmt.Errorf("expected audience %q not found in %v", expected, aud)
        default:
            return "", fmt.Errorf("claim %q has unexpected type %T", key, v)
        }
    }

    repo, err := getString(keys.repo)
    if err != nil { return nil, err }
    owner, err := getString(keys.owner)
    if err != nil { return nil, err }
    workflow, err := getString(keys.workflow)
    if err != nil { return nil, err }
    aud, err := getAudience(keys.aud, "fullsend-mint")
    if err != nil { return nil, err }

    // Extract RefProtected. GitLab provides a dedicated `ref_protected` boolean claim.
    // GitHub does not have an equivalent — callers must validate the workflow ref
    // against the repo's default/protected branch separately.
    refProtected := false
    if forge == ForgeGitLab {
        if v, ok := rawClaims["ref_protected"]; ok {
            switch val := v.(type) {
            case string:
                // GitLab OIDC tokens serialize ref_protected as a JSON string
                // ("true"/"false"), which json.Unmarshal into map[string]interface{}
                // decodes as Go string. This is the expected primary case.
                refProtected = val == "true"
            case bool:
                // Defensive fallback: future GitLab versions or RFC-compliant
                // serializers may emit ref_protected as a JSON boolean.
                refProtected = val
                log.Printf("WARN: ref_protected claim is a bool (expected string); check GitLab version")
            }
        }
    }

    return &NormalizedClaims{
        Issuer:       issuer,
        Repository:   repo,
        Owner:        owner,
        WorkflowRef:  workflow,
        Audience:     aud,
        RefProtected: refProtected,
    }, nil
}
```

In the mint's request handler, enforce the protected-ref requirement for GitLab before dispatching to the credential backend:

```go
// After normalizeClaims succeeds:
if forge == ForgeGitLab && !claims.RefProtected {
    return nil, fmt.Errorf("GitLab pipeline did not run on a protected ref")
}
```

GitHub's protected-ref validation uses a separate path (checking `job_workflow_ref` against the `.fullsend` repo's default branch); `RefProtected` is not set for GitHub tokens.

### Credential backend abstraction

```go
type CredentialBackend interface {
    MintToken(ctx context.Context, org, role string, repos []string) (token string, expiresAt string, err error)
}

type GitHubCredentialBackend struct {
    secretClient *secretmanager.Client
    // existing fields
}

type GitLabCredentialBackend struct {
    secretClient *secretmanager.Client
}
```

`GitLabCredentialBackend.MintToken`:
1. Validate that `repos` contains exactly one element. GitLab PATs are per-project; multi-repo requests are unsupported. Return an error if `len(repos) != 1`. Also validate that the group extracted from `repos[0]` matches the `org` parameter (both refer to the GitLab namespace); return an error if they disagree — `repos[0]` is authoritative for the Secret Manager key, `org` is used only for OIDC claim validation.
2. Validate that `repos[0]` contains at least one `/`. A top-level project path with no `/` (e.g., a root-level namespace) would produce an empty group component and a malformed Secret Manager key. Return an error if `strings.IndexByte(repos[0], '/') < 0`.
3. Decompose `repos[0]` (the full project path, e.g., `myorg/subgroup/myproject`) into group and project components for the Secret Manager key. The project is the last `/`-separated component; the group is everything before the last `/` (e.g., group=`myorg/subgroup`, project=`myproject`). Apply the `/`→`_` escaping scheme (see Secret Manager naming) to both components before constructing the key.
4. Retrieve the stored PAT from Secret Manager (`fullsend-{escaped-group}--{project}--{role}-pat`).
5. Return the PAT value and its expiry time.
6. No JWT generation or token exchange needed — PATs are long-lived and pre-scoped. Note: unlike GitHub installation tokens, PATs cannot be further scoped down at mint time — the PAT's permissions are fixed at creation.

### WIF configuration

Add a WIF provider for GitLab's OIDC issuer:

- For GitLab.com: issuer = `https://gitlab.com`, JWKS URI = `https://gitlab.com/oauth/discovery/keys`
- For self-hosted: issuer = instance URL, JWKS URI = `{instance_url}/oauth/discovery/keys`
- CEL attributes for validation: `assertion.namespace_path` (group allowlist), `assertion.project_path` (project allowlist)

Multiple providers can coexist in the same WIF pool (one for GitHub, one per GitLab instance).

### Environment variables

New mint environment variables:

- `ALLOWED_ISSUERS` — comma-separated list of accepted OIDC issuers (replaces hardcoded GitHub issuer check). Default: `https://token.actions.githubusercontent.com`.
- `GITLAB_INSTANCES` — comma-separated list of GitLab instance URLs to configure WIF providers for.

### Files modified

- `internal/mint/main.go` — add claim normalization, credential backend abstraction, multi-issuer support
- `internal/mint/main_test.go` — tests for GitLab OIDC tokens, credential backend selection
- `internal/mint/go.mod` — no new dependencies expected

### Verification

- Unit tests with mock GitLab OIDC tokens.
- Tests for credential backend selection (GitHub vs GitLab based on issuer).
- Existing GitHub mint tests continue to pass.

## Phase 4: GitLab CI/CD Templates

**Goal**: Create scaffold templates for GitLab pipelines.

### Directory structure

Create `internal/scaffold/fullsend-repo-gitlab/`:

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
├── config.yaml
└── scripts/
    ├── post-triage.sh
    ├── post-code.sh
    ├── post-review.sh
    └── post-fix.sh
```

### Root pipeline (`.gitlab-ci.yml`)

```yaml
include:
  - local: '.gitlab/ci/dispatch.yml'

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "trigger"
```

### Dispatch pipeline (`.gitlab/ci/dispatch.yml`)

```yaml
# fullsend-stage: dispatch

stages:
  - validate
  - prepare
  - dispatch

validate-webhook:
  stage: validate
  image: alpine:latest
  rules:
    - if: $CI_COMMIT_REF_PROTECTED == "true" && $CI_PIPELINE_SOURCE == "trigger"
  script:
    - apk add --no-cache jq bash yq
    - bash <<'BASH'
      set -euo pipefail

      # CI_DEBUG_TRACE exposes all CI/CD variable values in pipeline logs,
      # including protected secrets. This guard exits early if debug tracing
      # is enabled by a user script check. Note: GitLab's built-in trace logging
      # prints variable values to the job log *before* user scripts execute, so
      # this guard reduces but cannot fully prevent exposure if debug tracing is
      # already enabled when the pipeline starts. Restrict who can enable
      # CI_DEBUG_TRACE at the project level via GitLab project settings
      # (Settings → CI/CD → General pipelines → Debug tracing).
      if [[ "${CI_DEBUG_TRACE:-}" == "true" ]]; then
        echo "ERROR: CI_DEBUG_TRACE is enabled, which would expose secrets in logs"
        exit 1
      fi

      if [[ -z "${SOURCE_PROJECT:-}" || -z "${EVENT_TYPE:-}" || -z "${EVENT_PAYLOAD_B64:-}" ]]; then
        echo "ERROR: Missing required pipeline variables"
        exit 1
      fi

      # Validate SOURCE_PROJECT format
      if [[ ! "$SOURCE_PROJECT" =~ ^[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)+$ ]]; then
        echo "ERROR: Invalid source project format: $SOURCE_PROJECT"
        exit 1
      fi

      # Confirm the bridge validated the webhook token before triggering this pipeline.
      # The bridge passes WEBHOOK_VALIDATED=true only after its constant-time token
      # comparison succeeds. The raw token is never forwarded as a trigger variable
      # (it would be visible in the GitLab CI/CD UI to Developer+ members).
      # Trust boundary: FULLSEND_DISPATCH_TOKEN is the sole authentication boundary —
      # possession of this token is what allows setting WEBHOOK_VALIDATED=true.
      # It is stored as a protected CI/CD variable accessible to .fullsend project
      # Maintainers (already trusted with pipeline code and config). Leakage of the
      # trigger token through non-bridge paths (e.g., CI/CD variable export or log
      # exposure) would allow bypass. Treat FULLSEND_DISPATCH_TOKEN with the same
      # sensitivity as a webhook secret.
      #
      # Threat model note: This control is a convention, not a cryptographic guarantee.
      # Anyone with FULLSEND_DISPATCH_TOKEN can call the Pipeline Trigger API directly
      # and set WEBHOOK_VALIDATED=true, bypassing webhook secret validation. This is
      # an accepted risk given Maintainer trust (they could also modify pipeline code
      # directly). A stronger alternative — HMAC signing the payload with a shared
      # Secret Manager secret — would eliminate this bypass but adds implementation
      # complexity. See Open Questions in ADR 0043.
      if [[ "${WEBHOOK_VALIDATED:-}" != "true" ]]; then
        echo "ERROR: Pipeline was not triggered by the validated bridge"
        exit 1
      fi

      # Validate source project is enrolled.
      # config.yaml stores GitLab repos by full project path as the key
      # (e.g., "myorg/subgroup/myproject"), matching the SOURCE_PROJECT format.
      # Use yq bracket notation to avoid dotpath collision issues — bracket notation
      # supports literal slashes and special characters without escaping.
      ENABLED=$(yq ".repos[\"${SOURCE_PROJECT}\"].enabled" config.yaml)
      if [[ "$ENABLED" != "true" ]]; then
        echo "ERROR: Project not enrolled: ${SOURCE_PROJECT}"
        exit 1
      fi

      echo "Validated: ${SOURCE_PROJECT} (${EVENT_TYPE})"
      BASH

determine-stage:
  stage: prepare
  image: alpine:latest
  needs: [validate-webhook]
  rules:
    - if: $CI_COMMIT_REF_PROTECTED == "true" && $CI_PIPELINE_SOURCE == "trigger"
  script:
    - apk add --no-cache jq bash
    - bash <<'BASH'
      set -euo pipefail

      PAYLOAD=$(echo "${EVENT_PAYLOAD_B64}" | base64 -d)
      STAGE=""

      case "${EVENT_TYPE}" in
        issues)
          ACTION=$(echo "$PAYLOAD" | jq -r '.action')
          case "$ACTION" in
            open) STAGE="triage" ;;
            update)
              # GitLab sends action="update" for both content edits and label changes
              # (unlike GitHub which uses a distinct "labeled" action). Check for newly
              # added labels first; if a fullsend label was added, route to its stage.
              # Compute newly added labels by diffing current vs previous.
              FULLSEND_LABELS=("ready-to-code" "ready-for-review")
              NEW_LABELS=$(echo "$PAYLOAD" | jq -r '
                (.changes.labels.current // []) as $cur |
                (.changes.labels.previous // []) as $prev |
                ($prev | map(.name)) as $prev_names |
                [$cur[] | select(.name as $n | $prev_names | index($n) | not) | .name] |
                .[]')
              MATCHING_LABELS=$(echo "$NEW_LABELS" | grep -F -x -e "ready-to-code" -e "ready-for-review" || true)
              MATCH_COUNT=$(echo "$MATCHING_LABELS" | grep -c . || true)
              if [[ "$MATCH_COUNT" -gt 1 ]]; then
                # Fail closed: multiple fullsend labels added simultaneously is not
                # a supported workflow. Nondeterministic ordering means silently
                # picking one would be unpredictable; error instead.
                echo "ERROR: Multiple fullsend labels added simultaneously: ${MATCHING_LABELS//$'\n'/, }"
                exit 1
              fi
              LABEL=$(echo "$MATCHING_LABELS" | head -1)
              case "$LABEL" in
                ready-to-code) STAGE="code" ;;
                ready-for-review) STAGE="review" ;;
                *)
                  # No fullsend label added. Only trigger triage for content
                  # changes (title or description edits). Metadata updates
                  # (assignee, milestone, weight, due date, etc.) are skipped
                  # to avoid flooding the triage agent on minor changes.
                  HAS_CONTENT_CHANGE=$(echo "$PAYLOAD" | jq -r '
                    ((.changes.title // null) != null or
                     (.changes.description // null) != null) | tostring')
                  if [[ "$HAS_CONTENT_CHANGE" == "true" ]]; then
                    STAGE="triage"
                  fi
                  ;;
              esac
              ;;
          esac
          ;;
        note)
          BODY=$(echo "$PAYLOAD" | jq -r '.note.body // ""')
          FIRST_LINE=$(echo "$BODY" | head -1)
          COMMAND=$(echo "$FIRST_LINE" | awk '{print $1}')
          case "$COMMAND" in
            /fs-triage) STAGE="triage" ;;
            /fs-code) STAGE="code" ;;
            /fs-review) STAGE="review" ;;
            /fs-fix) STAGE="fix" ;;
            /fs-retro) STAGE="retro" ;;
            /fs-prioritize) STAGE="prioritize" ;;
            *)
              # Auto-trigger fix when the review bot posts a changes-requested note.
              # The GitLab forge client prefixes REQUEST_CHANGES notes with a
              # machine-readable marker (see Phase 1 review semantics). This mirrors
              # GitHub's pull_request_review.submitted (state=changes_requested) → fix routing.
              # Author check: only accept the marker from the configured review bot account
              # (FULLSEND_REVIEW_BOT_USERNAME CI/CD variable) to prevent injection by
              # any user with MR note write access.
              NOTE_AUTHOR=$(echo "$PAYLOAD" | jq -r '.note.author.username // ""')
              BOT_USERNAME="${FULLSEND_REVIEW_BOT_USERNAME:-}"
              if [[ "$FIRST_LINE" == "<!-- fullsend:changes-requested -->" ]]; then
                if [[ -z "$BOT_USERNAME" ]]; then
                  # Log diagnostic: operator needs to set FULLSEND_REVIEW_BOT_USERNAME
                  # or auto-fix will never trigger from changes-requested reviews.
                  echo "INFO: changes-requested marker detected but FULLSEND_REVIEW_BOT_USERNAME is not set — auto-fix disabled"
                elif [[ "$NOTE_AUTHOR" == "$BOT_USERNAME" ]]; then
                  STAGE="fix"
                fi
              fi
              ;;
          esac
          ;;
        merge_request)
          ACTION=$(echo "$PAYLOAD" | jq -r '.action')
          # Block cross-project (forked) MRs: source and target project must match.
          # This is the GitLab equivalent of GitHub's fork PR blocking for the fix agent.
          MR_SOURCE=$(echo "$PAYLOAD" | jq -r '.merge_request.source_project // ""')
          MR_TARGET=$(echo "$PAYLOAD" | jq -r '.merge_request.target_project // ""')
          if [[ -n "$MR_SOURCE" && -n "$MR_TARGET" && "$MR_SOURCE" != "$MR_TARGET" ]]; then
            echo "Skipping cross-project MR: source=${MR_SOURCE} target=${MR_TARGET}"
            touch stage.env
            exit 0
          fi
          case "$ACTION" in
            open) STAGE="review" ;;
            update)
              # Only trigger review for new commits, not MR metadata edits
              # (title, assignee, labels, etc.). GitLab indicates new commits
              # via changes.last_commit in the webhook payload.
              HAS_NEW_COMMITS=$(echo "$PAYLOAD" | jq -r '
                ((.changes.last_commit // null) != null) | tostring')
              if [[ "$HAS_NEW_COMMITS" == "true" ]]; then
                STAGE="review"
              fi
              ;;
            close|merge) STAGE="retro" ;;
          esac
          ;;
      esac

      if [[ -z "$STAGE" ]]; then
        echo "No stage matched — skipping dispatch"
        touch stage.env
        exit 0
      fi

      echo "STAGE=$STAGE" >> stage.env

      # Map stage name to canonical mint role (matches GitHub's dispatch.yml convention).
      # The mint uses "coder" not "code"; retro/prioritize share the "fullsend" role.
      case "$STAGE" in
        code|fix) MINT_ROLE="coder" ;;
        retro|prioritize) MINT_ROLE="fullsend" ;;
        *) MINT_ROLE="$STAGE" ;;  # triage, review
      esac
      echo "MINT_ROLE=$MINT_ROLE" >> stage.env
      echo "Routed to stage: $STAGE (role: $MINT_ROLE)"
      BASH
  artifacts:
    reports:
      dotenv: stage.env

generate-child-config:
  stage: prepare
  image: alpine:latest
  needs: [determine-stage]
  rules:
    - if: $CI_COMMIT_REF_PROTECTED == "true" && $CI_PIPELINE_SOURCE == "trigger" && $STAGE
  script:
    - |
      if [ -z "${STAGE:-}" ]; then
        echo "No stage set — skipping"
        exit 0
      fi

      MATCHED=false
      for pipeline_file in .gitlab/ci/*.yml; do
        [ -f "$pipeline_file" ] || continue
        STAGE_MARKER=$(grep -E '^# fullsend-stage:' "$pipeline_file" | head -1 | sed 's/^# fullsend-stage: *//' || true)
        if [ "$STAGE_MARKER" = "$STAGE" ]; then
          echo "Generating child config for: $pipeline_file"
          # Use printf rather than a heredoc to avoid closing-delimiter
          # indentation issues — bash requires <<EOF's closing delimiter
          # to appear at column 0, which conflicts with script indentation.
          printf 'include:\n  - local: "%s"\nvariables:\n  IS_CHILD_PIPELINE: "true"\n' \
            "$pipeline_file" > .gitlab-ci-child.yml
          MATCHED=true
          break
        fi
      done

      if [ "$MATCHED" != "true" ]; then
        echo "ERROR: No pipeline found for stage: $STAGE"
        exit 1
      fi
  artifacts:
    paths:
      - .gitlab-ci-child.yml
    expire_in: 1 hour

trigger-stage:
  stage: dispatch
  needs: [determine-stage, generate-child-config]
  rules:
    - if: $CI_COMMIT_REF_PROTECTED == "true" && $CI_PIPELINE_SOURCE == "trigger" && $STAGE
  trigger:
    include:
      - artifact: .gitlab-ci-child.yml
        job: generate-child-config
    strategy: depend
  variables:
    IS_CHILD_PIPELINE: "true"
    EVENT_PAYLOAD_B64: $EVENT_PAYLOAD_B64
    SOURCE_PROJECT: $SOURCE_PROJECT
    EVENT_TYPE: $EVENT_TYPE
    ISSUE_IID: $ISSUE_IID
    MR_IID: $MR_IID
    RESOURCE_KEY: $RESOURCE_KEY
    MINT_ROLE: $MINT_ROLE
```

### Stage pipeline example (`.gitlab/ci/triage.yml`)

```yaml
# fullsend-stage: triage

workflow:
  rules:
    - if: $IS_CHILD_PIPELINE == "true"

triage:
  stage: build
  image: ghcr.io/fullsend-ai/fullsend-triage:latest
  id_tokens:
    FULLSEND_OIDC_TOKEN:
      aud: fullsend-mint
  variables:
    FULLSEND_MINT_URL: $FULLSEND_MINT_URL
  script:
    - |
      set -euo pipefail

      # CI_DEBUG_TRACE guard: child pipelines run in a separate context from the
      # dispatch pipeline's validate-webhook guard. Repeat the check here to protect
      # FULLSEND_FORGE_TOKEN (minted PAT) from being printed to logs if debug tracing
      # is re-enabled post-install. GitLab's built-in trace logging runs before user
      # scripts, so this guard cannot prevent that — but it stops further processing.
      if [[ "${CI_DEBUG_TRACE:-}" == "true" ]]; then
        echo "ERROR: CI_DEBUG_TRACE is enabled — aborting to prevent credential exposure"
        exit 1
      fi

      # Mint a scoped token for the triage role
      MINT_RESPONSE=$(curl -sSf --retry 3 --retry-delay 2 \
        -H "Authorization: Bearer ${FULLSEND_OIDC_TOKEN}" \
        -H "Content-Type: application/json" \
        -d "{\"role\":\"${MINT_ROLE}\",\"repos\":[\"${SOURCE_PROJECT}\"]}" \
        "${FULLSEND_MINT_URL}/v1/token")

      TOKEN=$(echo "$MINT_RESPONSE" | jq -r '.token // empty' 2>/dev/null)
      if [[ -z "$TOKEN" || "$TOKEN" == "null" ]]; then
        ERROR=$(echo "$MINT_RESPONSE" | jq -r '.error // "unknown error"' 2>/dev/null || echo "$MINT_RESPONSE")
        echo "ERROR: Mint returned no token: ${ERROR}"
        exit 1
      fi
      export FULLSEND_FORGE_TOKEN="$TOKEN"

      # Decode event payload to a temp file (avoids exposing content in ps aux
      # and prevents shell metacharacter injection from user-controlled fields)
      EVENT_PAYLOAD_FILE=$(mktemp)
      trap 'rm -f "$EVENT_PAYLOAD_FILE"' EXIT
      echo "${EVENT_PAYLOAD_B64}" | base64 -d > "$EVENT_PAYLOAD_FILE"

      # Run the agent
      fullsend run \
        --stage triage \
        --source-project "${SOURCE_PROJECT}" \
        --event-type "${EVENT_TYPE}" \
        --event-payload-file "${EVENT_PAYLOAD_FILE}"
  resource_group: "fullsend-triage-${SOURCE_PROJECT}-${RESOURCE_KEY}"
```

### Post-script portability

Most post-scripts (`post-triage.sh`, `post-code.sh`, etc.) are shell scripts that call forge operations via the `forge.Client` abstraction or `gh` CLI. For GitLab:

- Replace `gh` CLI calls with `glab` CLI calls or direct `curl` to the GitLab API.
- Alternatively, use `fullsend` CLI subcommands that abstract over the forge (preferred long-term approach).
- Label operations, comment operations, and MR creation have direct GitLab API equivalents.

### Concurrency control

GitLab uses `resource_group:` instead of GitHub's concurrency groups:

```yaml
resource_group: "fullsend-triage-${SOURCE_PROJECT}-${RESOURCE_KEY}"
```

This serializes execution per issue or MR per project, matching GitHub's behavior. `RESOURCE_KEY` is always pre-populated by the bridge (e.g., `issue-42`, `mr-7`, `note-issue-42`) — GitLab CI/CD `resource_group:` does not support bash `${VAR:-default}` expansion, so the bridge must produce a single non-empty key rather than relying on pipeline-side fallback logic.

### Files created

- `internal/scaffold/fullsend-repo-gitlab/` — entire directory tree

### Files modified

- `internal/scaffold/scaffold.go` — add GitLab scaffold variant selection

## Phase 5: CLI Install/Uninstall

**Goal**: `fullsend admin install` and `fullsend admin uninstall` work for GitLab groups and projects.

### Install flow (per-org)

```
fullsend admin install <group> --forge gitlab [--gitlab-url https://gitlab.example.com]
```

1. Detect or confirm forge type.
2. Create GitLab client from `GL_TOKEN` / `GITLAB_TOKEN` env var.
3. Validate the group exists and the user has Owner/Maintainer access.
4. Create `.fullsend` project in the group (if not exists).
5. Validate `.fullsend` default branch is protected.
5a. Check that `CI_DEBUG_TRACE` is not enabled on the `.fullsend` project (`GET /api/v4/projects/:id/variables/CI_DEBUG_TRACE`). Fail with a hard error if it is — enabling debug tracing exposes all protected CI/CD variables (including PATs and webhook tokens) to job logs before user scripts execute. Operators must disable it before enrollment can proceed.
6. Create per-role Project Access Tokens for each enrolled project:
   - triage → `triage` role (Reporter), code|fix → `coder` role (Developer), review → `review` role (Developer), retro|prioritize → `fullsend` role (Maintainer), orchestrator → `fullsend` role (Maintainer)
   - This matches GitHub's stage-to-role mapping: `code|fix → coder`, `retro|prioritize → fullsend`. The earlier draft used separate `retro` (Reporter) and `prioritize` (Developer) roles, but GitHub evidence shows both need Maintainer-level access (e.g., closing issues, managing labels, commenting on MRs). Aligning with GitHub's mapping ensures feature parity.
   - **Security note**: The `fullsend` orchestrator PAT has Maintainer access — the highest-sensitivity credential in the set, since Maintainer allows modifying protected branch settings, CI/CD variables, and project settings. A compromised orchestrator PAT could undermine the `ref=main` security invariant. Rotate this PAT independently and audit its usage separately. Evaluate whether Developer access with specific API permissions could replace Maintainer for orchestrator operations.
7. Store PATs in Secret Manager (`fullsend-{escaped-group}--{project}--{role}-pat`). Before storing, verify no existing secret with the same escaped name belongs to a different project path (collision check guards against the `/`→`_` escaping ambiguity). Fail with a hard error if a collision is detected.
8. Deploy the webhook bridge Cloud Function (if not already deployed).
9. Create pipeline trigger token for `.fullsend` project.
10. Write CI/CD templates to `.fullsend` project (from GitLab scaffold).
11. Write `config.yaml` with `forge: gitlab` and `gitlab_instance_url`.
11a. Set `FULLSEND_REVIEW_BOT_USERNAME` as a protected CI/CD variable in `.fullsend` with the GitLab username of the review bot account. Required for the auto-fix trigger from changes-requested reviews; warn if not provided, since the trigger will be silently disabled.
12. For each enrolled project:
    - Create webhook pointing to bridge function URL.
    - Generate and store webhook secret token in Secret Manager (for the bridge's TTL-cached lookup). The bridge reads webhook secrets from Secret Manager; storing them as CI/CD variables in `.fullsend` is not needed — the dispatch pipeline trusts `WEBHOOK_VALIDATED=true` from the bridge rather than re-reading the raw secret.

### Install flow (per-repo)

```
fullsend admin install <group> <project> --forge gitlab
```

Same as per-org but:
- `.fullsend/` directory lives within the target project.
- Webhook is on the target project itself.
- PATs created on the target project.

### Uninstall flow

1. Delete webhooks from enrolled projects.
2. Revoke Project Access Tokens.
3. Delete secrets from Secret Manager.
4. Delete CI/CD variables from `.fullsend` project.
5. Optionally delete `.fullsend` project.
6. Optionally remove bridge Cloud Function (if no other orgs use it).

### Config schema

```yaml
# config.yaml additions
forge: gitlab
gitlab_instance_url: https://gitlab.example.com  # optional, defaults to https://gitlab.com
```

### Layer modifications

- `internal/layers/workflows.go` — select GitLab scaffold when `config.forge == "gitlab"`
- `internal/layers/enrollment.go` — create webhooks instead of shim workflow PRs; webhook URL = bridge function URL
- `internal/layers/dispatch.go` — provision bridge Cloud Function for GitLab
- `internal/layers/secrets.go` — store GitLab PATs in Secret Manager (same Secret Manager, different naming convention)
- `internal/config/config.go` — add `Forge` and `GitLabInstanceURL` fields

### Files modified

- `internal/cli/admin.go`
- `internal/appsetup/appsetup.go`
- `internal/layers/workflows.go`
- `internal/layers/enrollment.go`
- `internal/layers/dispatch.go`
- `internal/layers/secrets.go`
- `internal/config/config.go`

## Phase 6: Testing and Documentation

### Pre-production gate: PAT model prototype

Before Phase 6 testing begins, prototype the group-level bot account approach described in [ADR 0043's Open Questions](../ADRs/0043-gitlab-support-via-webhook-bridge.md#open-questions). If the prototype is viable — a privileged service account can generate project-scoped, time-limited PATs on demand — update the architecture to replace static PAT storage with on-demand generation before finalizing the implementation. If not viable, this section serves as documentation that the static PAT model was explicitly chosen after evaluation.

### E2E testing

- Create a test group on GitLab.com with test projects.
- E2E test flow: install → create issue → verify triage fires → verify code agent creates MR → uninstall.
- Self-hosted testing: Docker-based GitLab instance for version-specific testing.
- CI matrix: GitLab.com + self-hosted GitLab 17.x.

### Documentation

- Write new ADR 0043 (this is the companion to this document).
- Update `docs/architecture.md`:
  - Add GitLab embodiment diagram parallel to the GitHub one.
  - Update layer mapping table with GitLab entries.
- Add GitLab installation guide to `docs/guides/getting-started/`.
- Update `docs/guides/README.md` with GitLab guide links.
- Mark ADR 0028 as superseded by ADR 0043.

## Security Considerations

### Protected branch enforcement

The `.fullsend` project's default branch MUST be `main` and MUST be protected before enrollment. The CLI validates both conditions via `IsProtectedBranch()` during install and fails with a clear error if either is not met. `fullsend admin analyze` re-checks on each run — including both that the default branch is still named `main` and that it is still protected. A Maintainer renaming the default branch or removing its protection after install would break the `ref=main` security invariant; `analyze` must flag this as a hard error.

### Protected CI/CD variables

All secrets in the `.fullsend` project MUST be marked as "protected":

- `FULLSEND_DISPATCH_TOKEN` — pipeline trigger token
- `FULLSEND_REVIEW_BOT_USERNAME` — GitLab username of the fullsend review bot account; used to validate the `<!-- fullsend:changes-requested -->` marker. If not set, the auto-fix trigger is silently disabled.
- Cloud provider credentials (GCP WIF, inference)

Note: per-role PATs and per-project webhook secrets are **not** stored as CI/CD variables. Both are stored in Secret Manager — PATs retrieved via the token mint (OIDC authentication), webhook secrets read by the bridge Cloud Function (TTL-cached). Storing credentials as CI/CD variables was ADR 0028's approach (Alternative 3), explicitly rejected by ADR 0043.

Protected variables are only exposed to pipelines running on protected branches. This is the primary defense-in-depth control if the bridge is compromised to trigger with a non-`main` ref.

`CI_DEBUG_TRACE` must be disabled on the `.fullsend` project. When enabled, GitLab prints all CI/CD variable values (including protected secrets and webhook tokens) to pipeline logs before user scripts execute. The dispatch pipeline includes a guard that exits early if `CI_DEBUG_TRACE` is set, but this cannot prevent the built-in trace logging that occurs prior to script execution — it only ensures no further processing happens after the guard fires. The effective mitigation is restricting who can enable debug tracing at the project level (Settings → CI/CD → General pipelines → Debug tracing). Project maintainers must verify this setting is off during install (enforced as a hard error by `fullsend admin install`); `fullsend admin analyze` treats `CI_DEBUG_TRACE` enabled as a hard error — it exits with a non-zero status immediately, the same behavior as `fullsend admin install`, rather than reporting a warning and continuing.

### Webhook secret management

Each enrolled project gets a unique webhook secret (cryptographically random, 32 bytes, hex-encoded). The secret is:
- Set on the project webhook via GitLab API.
- Stored in Secret Manager (key: `fullsend-webhook-{escaped-group}--{project}`, same escaping as PAT secrets) so the bridge Cloud Function can look it up with a TTL cache (5-minute refresh interval).
- Validated by the bridge Cloud Function using constant-time comparison (`crypto/subtle.ConstantTimeCompare`) before triggering the dispatch pipeline. The dispatch pipeline only checks the `WEBHOOK_VALIDATED=true` flag set by the bridge.

Note: webhook secrets are NOT stored as CI/CD variables in `.fullsend`. The dispatch pipeline does not need the raw token; the bridge handles all cryptographic validation.

### Fork/cross-project MR protection

For fix agent triggers from MR reviews, the dispatch pipeline validates that the MR source and target projects match (same check as GitHub's fork PR blocking). Cross-project MRs are blocked from triggering the fix agent.

### MR code never executes with secrets

The webhook-based architecture ensures that MR code never runs in a pipeline context with access to fullsend secrets. The dispatch pipeline and all agent pipelines run in the `.fullsend` project on the protected `main` branch. Agent code runs in a sandbox without credentials (same model as GitHub).

## Event Mapping

| GitHub event | GitLab webhook | `X-Gitlab-Event` header | Dispatch stage |
|---|---|---|---|
| `issues.opened` | Issue created | `Issue Hook` | triage |
| `issues.edited` | Issue updated | `Issue Hook` | triage |
| `issues.labeled` (ready-to-code) | Issue label changed | `Issue Hook` | code |
| `issues.labeled` (ready-for-review) | Issue label changed | `Issue Hook` | review |
| `issue_comment.created` | Note on issue | `Note Hook` | varies by `/fs-*` command |
| `pull_request_target.opened` | MR opened | `Merge Request Hook` | review |
| `pull_request_target.synchronize` | MR updated (new commits) | `Merge Request Hook` | review |
| `pull_request_target.closed` | MR merged/closed | `Merge Request Hook` | retro |
| `pull_request_review.submitted` | Review bot changes-requested note | `Note Hook` | fix (if note starts with `<!-- fullsend:changes-requested -->`) |

### GitLab-specific event differences

- **Label events**: GitLab issue webhooks include a `changes` field showing which labels were added/removed. The bridge extracts the triggering label from this field.
- **MR reviews**: GitLab has no single "review submitted" event. The review bot's changes-requested reviews are delivered as `Note Hook` events with a `<!-- fullsend:changes-requested -->` prefix, which the dispatch pipeline detects in the `note` handler to auto-trigger the fix stage.
- **Auto-triage on `needs-info` reply**: Same logic — check if the note is on an issue with the `needs-info` label and the commenter is authorized.

## Credential Rotation

### Problem

GitLab Project Access Tokens expire after a maximum of 1 year. GitHub App private keys do not expire. Rotation automation is required for GitLab deployments.

PATs are created with a **120-day expiry** with a **90-day rotation cadence** — the 30-day buffer prevents expiry if a scheduled rotation runs slightly late or fails once. Using equal expiry and rotation intervals creates a zero-grace-period race. The `fullsend admin analyze` 30-day warning window fires when a PAT has 30 days remaining (i.e., at day 90 of the 120-day life, which is when rotation should have already run). If rotation ran on time, the PAT was already replaced and has 120 days remaining — no warning fires. The warning is the signal that rotation has fallen behind.

### Solution

**`fullsend admin analyze`** (existing command) extended to check PAT expiry:
- Reads PAT metadata from Secret Manager.
- Warns when any PAT is within 30 days of expiry.
- Reports exact expiry dates for all roles.

**`fullsend admin rotate-tokens`** (new command):
- Creates a new PAT for each role via GitLab API.
- Stores the new PAT in Secret Manager (overwrites old value).
- Revokes the old PAT.
- Atomic per-role: if rotation fails for one role, others are unaffected.

### Secret Manager naming

PAT secrets follow the same convention as GitHub PEMs:
- GitHub: `fullsend-{org}--{role}-app-pem`
- GitLab: `fullsend-{escaped-group}--{project}--{role}-pat`

**Path escaping**: GitLab namespace paths can contain `/` for subgroups (e.g., `myorg/subgroup`), but GCP Secret Manager secret IDs only allow `[a-zA-Z0-9_-]`. The convention replaces `/` with `_` in the group path component:
- Example: group `myorg/subgroup`, project `my-project`, role `triage` → `fullsend-myorg_subgroup--my-project--triage-pat`

This escaping is applied consistently in `fullsend admin install`, `rotate-tokens`, and the mint's credential lookup. Note: this escaping is not injective — a group named `myorg_subgroup` (literal underscore) and a group path `myorg/subgroup` (subgroup via slash) produce the same escaped form. This collision is documented as an accepted edge case; organizations with such naming conflicts should not enroll both simultaneously.

Additional metadata stored alongside the PAT value:
- `expiry`: ISO 8601 timestamp
- `token_id`: GitLab token ID (needed for revocation)
- `created_at`: creation timestamp

### PAT usage audit alerting (required)

Because PATs are long-lived credentials (up to 90 days), PAT usage anomaly detection is a **required** control (not merely recommended) to compensate for the inability to scope down token lifetime:

- **GitLab audit log streaming**: Enable GitLab audit log streaming to an external SIEM during `fullsend admin install`. Flag alerts for: PAT used outside CI pipeline context, PAT used from unexpected IP ranges, PAT used on unexpected projects.
- **`fullsend admin analyze`**: Check that audit log streaming is configured; treat it as a hard error if not present (same level as CI_DEBUG_TRACE).

This elevates audit alerting from "recommended" to required to compensate for the PAT exposure window.

## Open Questions

### Mint statefulness for GitLab (decided)

[ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md) accepted option 1: the mint stores `O(projects × roles)` secrets. This scaling cost is accepted because the mint provides centralized credential management, OIDC validation, and audit logging. See [#1717](https://github.com/fullsend-ai/fullsend/issues/1717) for related discussion on consolidating role PEMs. If GitLab adds group-level access tokens with project scoping in the future, the model could be simplified.

### Post-script forge abstraction

Post-scripts currently use `gh` CLI and GitHub API directly. For GitLab, they need `glab` CLI or direct API calls. Options:

1. **Fork post-scripts per forge**: Maintain `post-triage-github.sh` and `post-triage-gitlab.sh`. Simple but doubles maintenance.
2. **Forge-neutral post-script commands**: Add `fullsend label add`, `fullsend comment create`, etc. — CLI subcommands that call `forge.Client` internally. Single post-script works for both forges.
3. **Environment-based dispatch**: Single post-script that checks `FULLSEND_FORGE` env var and calls the appropriate CLI/API.

Recommendation: Option 2 (forge-neutral CLI subcommands) is the cleanest long-term approach but requires new CLI commands. Option 3 is a pragmatic intermediate step.

### Reusable pipeline equivalent

GitHub uses reusable workflows (`workflow_call`) so `.fullsend` agent workflows delegate to upstream `fullsend-ai/fullsend`. GitLab's equivalent is `include:` with remote files or CI/CD components. The exact mechanism for distributing upstream fullsend pipeline templates to GitLab orgs needs design work. Options:

1. **`include: remote:`** — include YAML from a URL (e.g., raw file from the fullsend GitLab mirror). Simple but no version pinning beyond URL path.
2. **CI/CD components** (GitLab 17.0+) — GitLab's native reusable pipeline mechanism. Requires publishing fullsend as a CI/CD component catalog entry.
3. **Copy on install** — copy full pipeline YAML during `fullsend admin install` (current GitHub approach before ADR 0031). Simple but requires re-install for updates.

### GitLab runner requirements

Agent execution requires a runner with:
- Docker executor (for sandbox container images)
- Sufficient resources (CPU, memory, disk) for LLM-driven agent work
- Network access to the token mint and inference provider

Runner registration, tagging, and resource allocation are deployment concerns outside this document's scope.

## References

- [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md): GitLab support via webhook bridge
- [ADR 0005](../ADRs/0005-forge-abstraction-layer.md): Forge abstraction layer
- [ADR 0009](../ADRs/0009-pull-request-target-in-shim-workflows.md): `pull_request_target` security model
- [ADR 0029](../ADRs/0029-central-token-mint-secretless-fullsend.md): Central token mint
- [ADR 0041](../ADRs/0041-synchronous-workflow-call-event-dispatch.md): Synchronous dispatch
- [GitLab CI/CD documentation](https://docs.gitlab.com/ee/ci/)
- [GitLab Project Access Tokens](https://docs.gitlab.com/ee/user/project/settings/project_access_tokens.html)
- [GitLab Pipeline Triggers](https://docs.gitlab.com/ee/ci/triggers/)
- [GitLab OIDC](https://docs.gitlab.com/ee/ci/secrets/id_token_authentication.html)
