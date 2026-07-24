# Implementation Plan: GitLab Cron-Polling Event Dispatch

> **Note (2026-07):** This plan describes the pre-#5556 architecture using
> child-pipeline dispatch (bridge jobs, `generate-child-pipeline` CLI,
> `dispatches.json` output). #5556 replaced that with direct API-triggered
> pipelines. The cron-polling input driver and event discovery logic are
> unchanged; the dispatch output path is superseded.

**Context:** [ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md) decides a two-path event dispatch model for GitLab — native CI for MR open/update/reopen events, cron-polled scheduled pipelines for issues, comments, labels, and MR merges. This document contains the implementation plan and pseudocode for the cron-polling subsystem.

## Table of Contents

1. [Dependency Graph](#dependency-graph)
2. [Phase 0: Forge Interface Preparation](#phase-0-forge-interface-preparation)
3. [Phase 1: GitLab Forge Client](#phase-1-gitlab-forge-client)
4. [Phase 2: Cron Poller](#phase-2-cron-poller)
5. [Phase 3: GitLab CI/CD Templates](#phase-3-gitlab-cicd-templates)
6. [Phase 4: CLI Changes](#phase-4-cli-changes)
7. [Phase 5: Integration and Testing](#phase-5-integration-and-testing)
8. [Security-Critical Code Paths](#security-critical-code-paths)
9. [Verification Checklist](#verification-checklist)

## Dependency Graph

```
Phase 0 (forge interface) ──┬──> Phase 1 (GitLab forge client) ──> Phase 4 (CLI changes) ──┐
                            │                                                                │
                            └──> Phase 2 (cron-poller) ────────────────────────────────────>├──> Phase 5
                                                                                             │
Phase 3 (CI/CD templates) ─────────────────────────────────────────────────────────────────>─┘
```

Phases 1 and 2 depend on Phase 0 (forge interface changes). Phase 3 (CI/CD templates) has no code dependency on Phase 0 and can start immediately. Phase 4 depends on Phase 1. Phase 5 depends on all prior phases.

## Phase 0: Forge Interface Preparation

**Goal**: Prepare `forge.Client` for multi-forge support without breaking GitHub. Pure refactoring — no behavioral changes.

### New methods on `forge.Client`

Add to `internal/forge/forge.go`:

```go
IsProtectedBranch(ctx context.Context, owner, repo, branch string) (bool, error)
CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string) (int64, error)
DeletePipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64) error
ListPipelineSchedules(ctx context.Context, owner, repo string) ([]PipelineSchedule, error)
UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error
CreateProtectedCIVariable(ctx context.Context, owner, repo, name, value string) error
```

These methods are forge-neutral by design. `IsProtectedBranch` maps to GitHub's branch protection API and GitLab's protected branches API. `CreatePipelineSchedule` and `DeletePipelineSchedule` are GitLab-native; the GitHub implementation returns `ErrNotSupported`. `UpdateCIVariable` maps to GitLab's CI/CD variable API. `CreateProtectedCIVariable` creates a CI/CD variable with `Protected: true, Masked: false` — used for poll state variables (watermark, label state) that must not be accessible on non-protected branches but whose values are not secrets. The "CI" prefix distinguishes these from the existing `RepoVariable` methods which model GitHub Actions variables.

### New sentinel error

```go
var ErrNotSupported = errors.New("operation not supported by this forge")
```

This complements the existing sentinel errors in `forge.go`.

GitHub returns `ErrNotSupported` for `CreatePipelineSchedule`, `DeletePipelineSchedule`. GitLab returns it for `DispatchWorkflow`, `GetWorkflow`, `GetWorkflowRun`, `GetWorkflowRunLogs`, `GetWorkflowRunAnnotations`, `GetLatestWorkflowRun`, `ListWorkflowRuns`, `ListOrgInstallations`, `GetAppClientID`, and org-level secret/variable methods.

**Decision rule**: Use extension interfaces (`GitHubExtensions`) for methods that conceptually do not exist on the other platform (e.g., `ListOrgInstallations`, `GetAppClientID` — GitHub App concepts with no GitLab analogue). Use `ErrNotSupported` for methods with a forge-neutral contract that one forge does not implement yet (e.g., `CreatePipelineSchedule` on GitHub). Callers of extension-interface methods use a type-assertion gate; callers of `ErrNotSupported` methods handle the error per call site.

**Caller handling**: Audit all call sites via `grep -rn 'MethodName' internal/` to build a call-site inventory. Expected handling per call site:
- `DispatchWorkflow` callers (enrollment layer, `internal/layers/enrollment.go` `Install` via `dispatchRepoMaintenanceWithRetry` and `Uninstall`): repo-maintenance dispatch after enrollment/unenrollment. Skip with a log warning on `ErrNotSupported` — GitLab per-repo installs do not use cross-repo repo-maintenance workflows; enrollment changes are applied directly
- `DispatchWorkflow` callers (CLI, `internal/cli/admin.go`): repo-maintenance dispatch after enrollment config changes. Skip with a log warning on `ErrNotSupported` — same rationale as enrollment layer
- `CreateOrgSecret`/`OrgSecretExists` callers (dispatch layer, `internal/layers/dispatch.go`; CLI, `internal/cli/github.go`): skip with a log warning when `ErrNotSupported` — per-repo GitLab does not use org-level secrets
- `ListOrgInstallations`/`GetAppClientID` callers (appsetup, CLI): already gated behind `GitHubExtensions` type-assertion, so `ErrNotSupported` is never reached
- `GetLatestWorkflowRun`/`ListWorkflowRuns` callers: skip with a log warning — GitLab uses pipeline status via different mechanisms
- `GetWorkflow` callers (`internal/layers/enrollment.go:177`, `awaitWorkflowRegistration`): checks whether the repo-maintenance workflow file exists before dispatching. Skip with a log warning on `ErrNotSupported` — GitLab per-repo installs do not use cross-repo repo-maintenance workflows
- `GetWorkflowRun` callers: not currently called in production code but on the `forge.Client` interface. GitLab returns `ErrNotSupported`
- `GetWorkflowRunLogs` callers (`internal/layers/enrollment.go:255`): downloads logs for a completed repo-maintenance workflow run. Skip with a log warning on `ErrNotSupported` — same rationale as `GetWorkflow`
- `GetWorkflowRunAnnotations` callers (`internal/cli/admin.go:2710`): fetches annotations from a workflow run. Skip with a log warning on `ErrNotSupported` — GitHub Actions concept with no GitLab equivalent

### Extension interface

Move GitHub-only methods to a `GitHubExtensions` interface:

```go
type GitHubExtensions interface {
    ListOrgInstallations(ctx context.Context, org string) ([]Installation, error)
    GetAppClientID(ctx context.Context, slug string) (string, error)
}
```

Callers type-assert to access these methods. This keeps the core `forge.Client` interface forge-neutral.

### Forge detection

New file `internal/forge/detect.go`:

```go
func DetectForge(remoteURL string) (string, error) {
    host := extractHost(remoteURL)
    if host == "" {
        return "", fmt.Errorf("cannot extract host from remote URL %q: use --forge flag", remoteURL)
    }

    switch strings.ToLower(host) {
    case "github.com":
        return "github", nil
    case "gitlab.com":
        return "gitlab", nil
    default:
        return "", fmt.Errorf("unknown forge host %q: use --forge flag for self-hosted instances", host)
    }
}

// extractHost handles both HTTPS and SSH remote URL formats:
//   - HTTPS: https://github.com/org/repo.git → github.com
//   - SSH:   git@github.com:org/repo.git     → github.com
func extractHost(remoteURL string) string {
    if u, err := url.Parse(remoteURL); err == nil && u.Hostname() != "" {
        return u.Hostname()
    }
    // SSH format: user@host:path
    if at := strings.Index(remoteURL, "@"); at >= 0 {
        rest := remoteURL[at+1:]
        if colon := strings.Index(rest, ":"); colon > 0 {
            return rest[:colon]
        }
    }
    return ""
}
```

### Files

| Action | Path |
|--------|------|
| Modify | `internal/forge/forge.go` — add methods, sentinel, extension interface |
| Modify | `internal/forge/github/github.go` — implement new methods (schedule → `ErrNotSupported`; `IsProtectedBranch` → branch protection API); move `ListOrgInstallations`/`GetAppClientID` to `GitHubExtensions` |
| Modify | `internal/forge/fake.go` — implement new methods on FakeClient |
| Modify | `internal/appsetup/appsetup.go` — update `ListOrgInstallations`/`GetAppClientID` calls to use `GitHubExtensions` type-assertion |
| Modify | `internal/cli/admin.go` — update `ListOrgInstallations` calls to use `GitHubExtensions` type-assertion |
| Modify | `internal/cli/github.go` — update `GetAppClientID` calls to use `GitHubExtensions` type-assertion |
| Create | `internal/forge/detect.go` |
| Create | `internal/forge/detect_test.go` |
| Modify | `docs/normative/normalized-event/v1/normalized-event.schema.json` — extend `source.system` enum with `"gitlab"` (non-breaking v1 change per versioning policy); relax `repo_path` pattern to allow multiple `/` segments for GitLab nested group paths (e.g. `org/sub1/sub2/project`) |

### NormalizedEvent schema updates

Three schema changes are required before the `gitlab-poll` input driver can
emit valid `NormalizedEvent` values. **Status: completed** (PR #3191).

1. **`source.system` enum**: Add `"gitlab"` to the allowed values. The v1
   versioning policy permits adding new enum values as a non-breaking change.
2. **`repo_path` pattern**: Relax from `^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`
   (GitHub `owner/repo` only) to `^[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)+$` to
   allow GitLab nested group paths like `org/sub1/sub2/project`. This is a
   non-breaking relaxation — existing `owner/repo` values remain valid.
3. **`transition.kind` enum**: Add `"merged"` to the allowed values. Required
   for MR merge detection (`mr_event` → `"merged"`). Non-breaking per
   versioning policy.

### Verification

`make go-test && make go-vet` — all existing tests pass unchanged.

## Phase 1: GitLab Forge Client

**Goal**: Implement `internal/forge/gitlab/gitlab.go` with the full `forge.Client` interface.

### Constructor

```go
func New(token string, opts ...Option) (*LiveClient, error)
```

Single-token constructor for the bot project access token. The token is used for all REST and GraphQL API calls. Options include `WithBaseURL(url)` for self-hosted instances (default: `https://gitlab.com`).

### Full method mapping

| `forge.Client` method | GitLab SDK / API | Notes |
|---|---|---|
| `GetRepo` | `Projects.GetProject` | Returns project metadata |
| `GetDefaultBranch` | `Projects.GetProject` → `DefaultBranch` | |
| `GetCommit` | `Commits.GetCommit` | |
| `ListCommits` | `Commits.ListCommits` | |
| `CreateBranch` | `Branches.CreateBranch` | |
| `DeleteBranch` | `Branches.DeleteBranch` | |
| `GetBranchRef` | `Branches.GetBranch` | Returns HEAD commit SHA |
| `GetFileContent` | `RepositoryFiles.GetFile` | Base64 decode content |
| `ListFiles` | `Repositories.ListTree` | Recursive via `Recursive: true` |
| `CreateOrUpdateFile` | `RepositoryFiles.CreateFile` / `UpdateFile` | Check existence first |
| `CreateChangeProposal` | `MergeRequests.CreateMergeRequest` | MR, not PR |
| `GetPR` | `MergeRequests.GetMergeRequest` | |
| `ListRepoPullRequests` | `MergeRequests.ListProjectMergeRequests` | |
| `UpdatePR` | `MergeRequests.UpdateMergeRequest` | |
| `MergePR` | `MergeRequests.AcceptMergeRequest` | |
| `CreatePRComment` | `Notes.CreateMergeRequestNote` | Notes, not comments |
| `ListPRComments` | `Notes.ListMergeRequestNotes` | |
| `CreatePRReview` | Synthesized from notes + approvals | No native review object |
| `RequestPRReviewers` | `MergeRequestApprovals.SetApprovers` | Approvers, not reviewers |
| `ListPRReviews` | Synthesized from notes + approvals | |
| `GetPRDiff` | `MergeRequests.GetMergeRequestDiff` | |
| `AddLabels` | `MergeRequests.UpdateMergeRequest` or `Issues.UpdateIssue` | Labels in update payload |
| `RemoveLabel` | Same as above | Full label list minus removed |
| `CreateIssue` | `Issues.CreateIssue` | |
| `GetIssue` | `Issues.GetIssue` | |
| `ListIssues` | `Issues.ListProjectIssues` | |
| `UpdateIssue` | `Issues.UpdateIssue` | |
| `CreateIssueComment` | `Notes.CreateIssueNote` | |
| `ListIssueComments` | `Notes.ListIssueNotes` | |
| `ListResourceLabelEvents` | `ResourceLabelEvents.ListLabelEvents` | For resolving label applier identity |
| `CreateRepoSecret` | `ProjectVariables.CreateVariable` | With `Protected: true`, `Masked: true` |
| `DeleteRepoSecret` | `ProjectVariables.RemoveVariable` | |
| `CreateOrUpdateRepoVariable` | `ProjectVariables.CreateVariable` / `UpdateVariable` | |
| `IsProtectedBranch` | `ProtectedBranches.GetProtectedBranch` | 404 → not protected |
| `CreatePipelineSchedule` | `PipelineSchedules.CreatePipelineSchedule` | GitLab-specific |
| `DeletePipelineSchedule` | `PipelineSchedules.DeletePipelineSchedule` | GitLab-specific |
| `ListPipelineSchedules` | `PipelineSchedules.ListProjectPipelineSchedules` | For uninstall cleanup |
| `UpdateCIVariable` | `ProjectVariables.UpdateVariable` | For poll watermark |
| `CreateProtectedCIVariable` | `ProjectVariables.CreateVariable` | With `Protected: true`, `Masked: false` — for poll state |
| `DispatchWorkflow` | → `ErrNotSupported` | GitHub-only |
| `ListOrgInstallations` | → `GitHubExtensions` (not on base interface) | GitHub-only |
| `GetAppClientID` | → `GitHubExtensions` (not on base interface) | GitHub-only |
| `CreateOrgSecret` | → `ErrNotSupported` | Per-repo only |
| `OrgSecretExists` | → `ErrNotSupported` | Per-repo only |
| `GetLatestWorkflowRun` | → `ErrNotSupported` | GitHub Actions concept |
| `ListWorkflowRuns` | → `ErrNotSupported` | GitHub Actions concept |
| `GetWorkflow` | → `ErrNotSupported` | GitHub Actions concept |
| `GetWorkflowRun` | → `ErrNotSupported` | GitHub Actions concept |
| `GetWorkflowRunLogs` | → `ErrNotSupported` | GitHub Actions concept |
| `GetWorkflowRunAnnotations` | → `ErrNotSupported` | GitHub Actions concept |
| `CommitFiles` | `Commits.CreateCommit` | Multi-file commit |

### Review synthesis

GitLab has no native "review" object like GitHub's pull request review. Reviews are synthesized from:
- **Notes** with suggestion blocks → "changes requested"
- **Approval status** via `MergeRequestApprovals.GetConfiguration` → "approved"
- **Discussion resolution status** → tracks whether feedback has been addressed

The `CreatePRReview` method posts a note and optionally approves/unapproves the MR.

### Additional polling-support methods

These are internal methods on the client struct (not on `forge.Client`), used by the poller:

```go
func (c *LiveClient) ListIssuesUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]Issue, error)
func (c *LiveClient) ListMergeRequestsUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]MergeRequest, error)
func (c *LiveClient) ListProjectEvents(ctx context.Context, owner, repo string, targetType string, after time.Time) ([]Event, error)
func (c *LiveClient) ListIssueNotes(ctx context.Context, owner, repo string, issueIID int) ([]Note, error)
func (c *LiveClient) ListMergeRequestNotes(ctx context.Context, owner, repo string, mrIID int) ([]Note, error)
func (c *LiveClient) ListResourceLabelEvents(ctx context.Context, owner, repo string, issueIID int) ([]ResourceLabelEvent, error)
func (c *LiveClient) GetVariable(ctx context.Context, owner, repo, key string) (string, error)
func (c *LiveClient) GetAuthenticatedUser(ctx context.Context) (*User, error) // GET /user
func (c *LiveClient) CreateNoteAwardEmoji(ctx context.Context, owner, repo string, issueIID, noteID int, emoji string) error
```

### Subgroup path handling

GitLab supports deeply nested namespaces (`org/sub1/sub2/project`). The client must URL-encode the full project path for API calls, or use numeric project IDs. The `GetRepo` method resolves `owner/repo` to a project ID, and subsequent calls use the numeric ID.

### Files

| Action | Path |
|--------|------|
| Create | `internal/forge/gitlab/gitlab.go` (~1500-2000 lines) |
| Create | `internal/forge/gitlab/gitlab_test.go` |

## Phase 2: Cron Poller

**Goal**: Implement the event polling logic that runs inside scheduled GitLab CI/CD pipelines. The poller is a Go package compiled into the `fullsend` binary and invoked via `fullsend poll`. No external infrastructure is required — no Cloud Function, no webhook bridge, no separate deployment.

### Architecture

```
fullsend poll
├── Read FULLSEND_LAST_POLL_AT_{FAST,FULL} from CI variable
├── Query GitLab API for changes since last poll
│   ├── GET /projects/:id/issues?updated_after=T
│   ├── GET /projects/:id/merge_requests?updated_after=T
│   └── GET /projects/:id/events?target_type=note&after=D
├── For each changed item with new notes:
│   └── GET /projects/:id/issues/:iid/notes (or merge_requests/:iid/notes)
├── Apply event routing rules → list of (stage, event) pairs
├── Dispatch each via parent-child pipeline trigger
│   └── Create child pipeline with STAGE, EVENT_PAYLOAD_B64, RESOURCE_KEY
├── Update FULLSEND_LAST_POLL_AT_{FAST,FULL} via API
└── Exit
```

### Package structure

```
internal/poll/
├── poll.go           # Main poll loop
├── poll_test.go      # Unit tests
├── events.go         # Event detection and deduplication
├── events_test.go    # Event detection tests
├── dispatch.go       # Child pipeline triggering
└── state.go          # Watermark state management
```

### CLI command

New subcommand `fullsend poll` added to `internal/cli/`:

```go
func newPollCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "poll",
        Short: "Poll GitLab API for new events and dispatch agent stages",
        RunE: func(cmd *cobra.Command, args []string) error {
            forgeToken := os.Getenv("FULLSEND_FORGE_TOKEN")
            projectPath := os.Getenv("CI_PROJECT_PATH")
            gcpProjectID := os.Getenv("FULLSEND_GCP_PROJECT_ID")

            client, err := gitlab.New(forgeToken, gitlabURL)
            if err != nil {
                return err
            }

            botUser, err := client.GetAuthenticatedUser(cmd.Context())
            if err != nil {
                return fmt.Errorf("identify bot user: %w", err)
            }

            poller := poll.New(client, projectPath, poll.Options{
                SlashCommandsOnly: os.Getenv("FULLSEND_POLL_MODE") == "fast",
                BotUserID:         botUser.ID,
            })

            return poller.Run(cmd.Context())
        },
    }
}
```

### Poll loop (`poll.go`)

```go
type Poller struct {
    client       *gitlab.LiveClient
    dispatchCore *dispatch.Core // shared dispatch core (authorization + CEL routing)
    projectPath  string
    owner        string
    repo         string
    botUserID    int // GitLab user ID of the enrolled fullsend bot
    opts         Options
}

type Options struct {
    SlashCommandsOnly bool // fast-poll mode: only check for /fs-* commands
    BotUserID         int  // GitLab user ID of the enrolled fullsend bot
}

func (p *Poller) Run(ctx context.Context) error {
    p.owner, p.repo = splitOwnerRepo(p.projectPath)
    p.botUserID = p.opts.BotUserID

    // 1. Read watermark
    lastPollAt, err := p.readWatermark(ctx, p.owner, p.repo)
    if err != nil {
        return fmt.Errorf("read watermark: %w", err)
    }

    // 2. Discover events
    var events []RoutableEvent
    var labelState LabelState    // non-nil only for full polls
    var minSkippedAt time.Time   // earliest issue skipped due to note-fetch failure
    if p.opts.SlashCommandsOnly {
        events, err = p.discoverSlashCommands(ctx, p.owner, p.repo, lastPollAt)
    } else {
        events, labelState, minSkippedAt, err = p.discoverAllEvents(ctx, p.owner, p.repo, lastPollAt)
    }
    if err != nil {
        return fmt.Errorf("discover events: %w", err)
    }

    // 3. Pre-filter and deduplicate
    events = p.filterBotEvents(events)
    events = p.deduplicate(events)

    // 4. Convert to NormalizedEvent, route via dispatch core, and dispatch.
    // Track maxUpdatedAt for successfully dispatched and unroutable events.
    // Separately track minFailedAt — the earliest UpdatedAt among failed
    // dispatches — so the watermark never advances past unprocessed events.
    // Also incorporate minSkippedAt from discovery-time note-fetch failures.
    dispatched := 0
    var maxUpdatedAt time.Time
    var minFailedAt time.Time
    failedLabelEvents := make(map[int]map[string]bool) // IID → labels whose dispatch failed
    for _, event := range events {
        normalizedEvent, err := p.toNormalizedEvent(ctx, event)
        if err != nil {
            log.Printf("WARNING: skipping %s event on IID %d: %v", event.Type, event.IID, err)
            if minFailedAt.IsZero() || event.UpdatedAt.Before(minFailedAt) {
                minFailedAt = event.UpdatedAt
            }
            if event.Type == "issue_label" {
                if failedLabelEvents[event.IID] == nil {
                    failedLabelEvents[event.IID] = make(map[string]bool)
                }
                for _, label := range event.Labels {
                    failedLabelEvents[event.IID][label] = true
                }
            }
            continue
        }
        stages, err := p.dispatchCore.Route(ctx, normalizedEvent)
        if err != nil {
            log.Printf("dispatch core error for %s: %v", event.Key(), err)
            if minFailedAt.IsZero() || event.UpdatedAt.Before(minFailedAt) {
                minFailedAt = event.UpdatedAt
            }
            if event.Type == "issue_label" {
                if failedLabelEvents[event.IID] == nil {
                    failedLabelEvents[event.IID] = make(map[string]bool)
                }
                for _, label := range event.Labels {
                    failedLabelEvents[event.IID][label] = true
                }
            }
            continue
        }
        if len(stages) == 0 {
            if event.UpdatedAt.After(maxUpdatedAt) {
                maxUpdatedAt = event.UpdatedAt
            }
            continue
        }

        for _, stage := range stages {
            if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
                log.Printf("dispatch %s for %s failed: %v", stage, event.Key(), err)
                if minFailedAt.IsZero() || event.UpdatedAt.Before(minFailedAt) {
                    minFailedAt = event.UpdatedAt
                }
                if event.Type == "issue_label" {
                    if failedLabelEvents[event.IID] == nil {
                        failedLabelEvents[event.IID] = make(map[string]bool)
                    }
                    for _, label := range event.Labels {
                        failedLabelEvents[event.IID][label] = true
                    }
                }
                continue
            }
            dispatched++
            if event.UpdatedAt.After(maxUpdatedAt) {
                maxUpdatedAt = event.UpdatedAt
            }
            // Acknowledge slash commands with a reaction so users know the
            // command was picked up (avoids blind 5–60 min wait).
            if event.NoteID != 0 && strings.HasPrefix(strings.TrimSpace(event.NoteBody), "/fs-") {
                _ = p.client.CreateNoteAwardEmoji(ctx, p.owner, p.repo, event.IID, event.NoteID, "eyes")
            }
        }
    }

    // 5. Update watermark (with 30s overlap for clock skew).
    // Only fall back to time.Now() on a truly empty poll (no events
    // discovered). When events exist but all dispatches failed,
    // maxUpdatedAt stays zero and the watermark is not advanced —
    // those events remain in the next poll's lookback window.
    // In the mixed success/failure case, cap maxUpdatedAt at minFailedAt
    // so the window always includes unprocessed failed events.
    if maxUpdatedAt.IsZero() && len(events) == 0 {
        maxUpdatedAt = time.Now()
    }
    if maxUpdatedAt.IsZero() {
        log.Printf("WARNING: all %d dispatches failed, watermark not advanced", len(events))
        return nil
    }
    if !minFailedAt.IsZero() && minFailedAt.Before(maxUpdatedAt) {
        maxUpdatedAt = minFailedAt
    }
    if !minSkippedAt.IsZero() && minSkippedAt.Before(maxUpdatedAt) {
        maxUpdatedAt = minSkippedAt
    }
    newWatermark := maxUpdatedAt.Add(-30 * time.Second)
    if err := p.updateWatermark(ctx, p.owner, p.repo, newWatermark); err != nil {
        log.Printf("WARNING: failed to update watermark: %v", err)
    }

    // 6. Persist label state after dispatch.
    // Remove labels from failed dispatches so they remain "unseen" and
    // are re-detected on the next poll cycle.
    if labelState != nil {
        for iid, failedLabels := range failedLabelEvents {
            if current, ok := labelState[iid]; ok {
                var kept []string
                for _, label := range current {
                    if !failedLabels[label] {
                        kept = append(kept, label)
                    }
                }
                labelState[iid] = kept
            }
        }
        if err := p.persistLabelState(ctx, p.owner, p.repo, labelState); err != nil {
            log.Printf("WARNING: %v (next poll may re-dispatch label events)", err)
        }
    }

    log.Printf("poll complete: %d events discovered, %d dispatched", len(events), dispatched)
    return nil
}
```

### Event discovery (`events.go`)

```go
type RoutableEvent struct {
    Type         string    // "issue_label", "issue_note", "mr_note", "mr_event"
    IID          int       // issue or MR IID
    UpdatedAt    time.Time
    Labels       []string  // newly-added labels for issue_label; current labels for issue_note
    NoteBody     string    // comment body (for slash command routing)
    NoteID       int       // note ID (for dedup)
    NoteAuthorID int       // note author user ID (for authorization checks)
    IsBot        bool      // whether the note author is a bot
    MRSource     int       // source project ID (for fork MR protection)
    MRTarget     int       // target project ID (for fork MR protection)
}

// discoverAllEvents returns:
//   - events: all routable events found since the given time
//   - labelState: updated label state for persistence (with skipped issues restored)
//   - minSkippedAt: earliest UpdatedAt among issues skipped due to note-fetch
//     failures (zero if none skipped); the caller must cap the watermark at this
//     value so skipped events are retried on the next poll
//   - error
func (p *Poller) discoverAllEvents(ctx context.Context, owner, repo string, since time.Time) ([]RoutableEvent, LabelState, time.Time, error) {
    var events []RoutableEvent

    // 1. Issues updated since last poll
    issues, err := p.client.ListIssuesUpdatedSince(ctx, owner, repo, since)
    if err != nil {
        return nil, nil, time.Time{}, fmt.Errorf("list issues: %w", err)
    }

    // Detect newly-added labels (state diff against previous poll).
    // On error, abort — continuing with nil newLabels would silently
    // drop all label-based events while the watermark advances past them.
    // Label state is NOT persisted here — the caller persists after
    // dispatch so that failed dispatches are re-detected next poll.
    newLabels, updatedLabelState, previousLabels, err := p.detectNewLabels(ctx, owner, repo, issues)
    if err != nil {
        return nil, nil, time.Time{}, fmt.Errorf("detect new labels: %w", err)
    }

    var minSkippedAt time.Time // earliest UpdatedAt among skipped issues
    for _, issue := range issues {
        // Fetch notes first — if this fails, skip the entire issue
        // (including label events) so that neither notes nor labels
        // advance maxUpdatedAt past events we couldn't fully discover.
        notes, err := p.client.ListIssueNotes(ctx, owner, repo, issue.IID)
        if err != nil {
            log.Printf("list notes for issue %d: %v (skipping issue entirely)", issue.IID, err)
            // Restore this issue's previous label state so its labels
            // remain "unseen" — detectNewLabels already marked them as
            // seen in updatedLabelState, but we never emitted events.
            if prev, ok := previousLabels[issue.IID]; ok {
                updatedLabelState[issue.IID] = prev
            } else {
                delete(updatedLabelState, issue.IID)
            }
            if minSkippedAt.IsZero() || issue.UpdatedAt.Before(minSkippedAt) {
                minSkippedAt = issue.UpdatedAt
            }
            continue
        }

        // Check for label-based triggers — one event per newly-added
        // routable label so that multiple labels in the same poll window
        // each dispatch independently.
        if added, ok := newLabels[issue.IID]; ok {
            for _, label := range added {
                events = append(events, RoutableEvent{
                    Type:      "issue_label",
                    IID:       issue.IID,
                    UpdatedAt: issue.UpdatedAt,
                    Labels:    []string{label},
                })
            }
        }
        for _, note := range notes {
            if note.CreatedAt.Before(since) {
                continue // skip old notes (client-side filtering)
            }
            events = append(events, RoutableEvent{
                Type:         "issue_note",
                IID:          issue.IID,
                UpdatedAt:    note.CreatedAt,
                NoteBody:     note.Body,
                NoteID:       note.ID,
                NoteAuthorID: note.Author.ID,
                IsBot:        note.Author.Bot,
                Labels:       issue.Labels,
            })
        }
    }

    // 2. MRs updated since last poll (for MR comments and MR merge detection).
    //    MR open/update/reopen are handled by native CI (`merge_request_event`);
    //    MR merge is detected here via `merged_at > watermark` because GitLab
    //    does not fire `merge_request_event` on merge (it fires a `push` event
    //    on the target branch instead). A persistent MR API failure must not
    //    block issue event processing, so we log and continue with issue-only
    //    events rather than aborting.
    mrs, err := p.client.ListMergeRequestsUpdatedSince(ctx, owner, repo, since)
    if err != nil {
        log.Printf("list merge requests: %v (continuing with issue events only)", err)
        if minSkippedAt.IsZero() || since.Before(minSkippedAt) {
            minSkippedAt = since
        }
        return events, updatedLabelState, minSkippedAt, nil
    }

    for _, mr := range mrs {
        // Detect MR merge — primary path for retro-stage dispatch.
        if !mr.MergedAt.IsZero() && mr.MergedAt.After(since) {
            events = append(events, RoutableEvent{
                Type:         "mr_event",
                IID:          mr.IID,
                UpdatedAt:    mr.MergedAt,
                NoteAuthorID: mr.MergedByID,
                IsBot:        mr.MergedBy.Bot,
                MRSource:     mr.SourceProjectID,
                MRTarget:     mr.TargetProjectID,
            })
        }

        notes, err := p.client.ListMergeRequestNotes(ctx, owner, repo, mr.IID)
        if err != nil {
            log.Printf("list notes for MR %d: %v (skipping MR entirely)", mr.IID, err)
            if minSkippedAt.IsZero() || mr.UpdatedAt.Before(minSkippedAt) {
                minSkippedAt = mr.UpdatedAt
            }
            continue
        }
        for _, note := range notes {
            if note.CreatedAt.Before(since) {
                continue
            }
            events = append(events, RoutableEvent{
                Type:         "mr_note",
                IID:          mr.IID,
                UpdatedAt:    note.CreatedAt,
                NoteBody:     note.Body,
                NoteID:       note.ID,
                NoteAuthorID: note.Author.ID,
                IsBot:        note.Author.Bot,
                MRSource:     mr.SourceProjectID,
                MRTarget:     mr.TargetProjectID,
            })
        }
    }

    return events, updatedLabelState, minSkippedAt, nil
}

// isProjectAccessTokenBot detects GitLab project access token bot users.
// GitLab's Events API author object does not include a `bot` field, so
// fast-poll mode uses this username heuristic. Full-poll mode uses the
// Notes API `Author.Bot` field instead (more reliable). This inconsistency
// is accepted: fast-poll only handles slash commands, not changes-requested
// markers, limiting the blast radius of a false negative.
func isProjectAccessTokenBot(username string) bool {
    return strings.HasPrefix(username, "project_") && strings.Contains(username, "_bot_")
}

func (p *Poller) discoverSlashCommands(ctx context.Context, owner, repo string, since time.Time) ([]RoutableEvent, error) {
    // Fast-poll mode: use the Events API to find new notes only.
    // This avoids querying all issues/MRs — just look for note-type events.
    //
    // GitLab Events API response fields used:
    //   evt.Note.NoteableType → "Issue" | "MergeRequest" (mapped to internal event types)
    //   evt.Note.NoteableIID  → issue/MR IID
    //   evt.Note.Body         → comment text (checked for /fs-* prefix)
    //   evt.Note.ID           → note ID
    //   evt.Author.ID         → author user ID (for authorization check)
    //   evt.Author.Username   → username (for bot detection via pattern match)
    //   evt.CreatedAt         → event timestamp
    projectEvents, err := p.client.ListProjectEvents(ctx, owner, repo, "Note", since)
    if err != nil {
        return nil, fmt.Errorf("list note events: %w", err)
    }

    var events []RoutableEvent
    for _, evt := range projectEvents {
        if evt.CreatedAt.Before(since) {
            continue // client-side filtering (Events API after= is date-only)
        }
        // Only include notes that look like slash commands
        if !strings.HasPrefix(strings.TrimSpace(evt.Note.Body), "/fs-") {
            continue
        }
        // Normalize NoteableType to internal event type constants.
        // GitLab returns capitalized values ("Issue", "MergeRequest").
        var eventType string
        switch evt.Note.NoteableType {
        case "Issue":
            eventType = "issue_note"
        case "MergeRequest":
            eventType = "mr_note"
        default:
            continue
        }
        events = append(events, RoutableEvent{
            Type:         eventType,
            IID:          evt.Note.NoteableIID,
            UpdatedAt:    evt.CreatedAt,
            NoteBody:     evt.Note.Body,
            NoteID:       evt.Note.ID,
            NoteAuthorID: evt.Author.ID,
            IsBot:        isProjectAccessTokenBot(evt.Author.Username),
        })
    }

    return events, nil
}
```

### NormalizedEvent conversion and dispatch core integration

Per [ADR 0061](../ADRs/0061-harness-cel-dispatch.md), event routing is
performed by CEL `trigger` expressions evaluated in the shared dispatch
core — not by the input driver. The `gitlab-poll` input driver converts
`RoutableEvent` values to `NormalizedEvent` values and passes them to
the dispatch core for authorization and routing.

The pseudocode below uses flat field names as shorthand for the nested
`NormalizedEvent` schema fields defined in
[`docs/normative/normalized-event/v1/`](../normative/normalized-event/v1/README.md).
The implementation must map to the normative schema; the `dispatch.NormalizedEvent`
Go type does not exist yet and will be defined in Phase 2.

| Pseudocode field | Schema path | Type adaptation notes |
|------------------|-------------|----------------------|
| `Forge` | `source.system` | `"gitlab"` |
| `EventType` | `transition.kind` | Translate: `"issue_label"` → `"label_changed"`, `"issue_note"` → `"comment_added"`, `"mr_note"` → `"comment_added"`, `"mr_event"` → `"merged"` |
| `IID` | `entity.id` | Integer in both pseudocode and schema — no conversion needed |
| `Labels` | `state.labels` | Current label set (array of strings) |
| `Timestamp` | *(no direct schema field)* | v1 schema has no event timestamp field; pass as adapter-internal metadata for watermark comparison, not included in the emitted `NormalizedEvent` |
| `NoteBody` | `transition.comment.body` | Present only for `comment_added` transitions |
| `TransitionLabel` | `transition.label` | Present only for `label_changed` transitions; `name` from label string, `action` = `"added"` (poller detects additions only) |
| `NoteAuthorID` | `actor.id` | Convert int to string; schema `actor.id` is string type. For `issue_label` events, resolved via resource label events API (see `resolveLabelAuthor`) |
| `IsBot` | `actor.kind` | Map to `"bot"` or `"human"` string |
| `MRSourceProjectID` | `state.change_proposal.head_repo` | Convert project ID (int) to GitLab project path (string) via API lookup |
| `MRTargetProjectID` | `state.change_proposal.base_repo` | Convert project ID (int) to GitLab project path (string) via API lookup |
| *(not in pseudocode)* | `repo` | Required; set from `CI_PROJECT_PATH` or poller config |
| *(not in pseudocode)* | `entity.kind` | Required; `"work_item"` for `issue_label`/`issue_note`, `"change_proposal"` for `mr_note`/`mr_event` |
| *(not in pseudocode)* | `entity.url` | Required; construct from GitLab project URL + IID |
| `ActorRole` | `actor.role` | Resolved via Members API; map GitLab access level to role string (see mapping table below) |
| *(not in pseudocode)* | `actor.is_entity_author` | Required; compare actor ID against entity author ID |
| *(not in pseudocode)* | `source.raw_type` | Required; e.g. `"issue_event"`, `"merge_request_event"` |

The pseudocode omits several required schema fields for brevity. The
implementation must populate all required fields per the normative schema.
Type conversions (int→string for `actor.id`, project ID→path for
`head_repo`/`base_repo`) are input-driver responsibilities resolved at
conversion time. `entity.id` is integer in both the pseudocode and the
schema — no conversion needed.

**GitLab access level → `actor.role` mapping:**

| GitLab access level | Numeric value | `actor.role` |
|---------------------|---------------|--------------|
| Guest               | 10            | `read`       |
| Reporter            | 20            | `triage`     |
| Developer           | 30            | `write`      |
| Maintainer          | 40            | `maintain`   |
| Owner               | 50            | `admin`      |

This mapping is used by `resolveActorRole` to translate the GitLab Members
API `access_level` field into the NormalizedEvent `actor.role` string.
Reporter (20) maps to `triage` (not `read`) to preserve the distinction
required by ADR 0054 — needs-info triage dispatch requires Reporter+
access, which must be distinguishable from Guest.

```go
// toNormalizedEvent converts the input driver's internal RoutableEvent
// into a NormalizedEvent for the dispatch core. The dispatch core handles
// authorization (ADR 0054) and CEL trigger evaluation (ADR 0061).
func (p *Poller) toNormalizedEvent(ctx context.Context, event RoutableEvent) (dispatch.NormalizedEvent, error) {
    ne := dispatch.NormalizedEvent{
        Forge:     "gitlab",
        EventType: translateEventType(event.Type), // see mapping table above
        IID:       event.IID,
        Labels:    event.Labels,
        Timestamp: event.UpdatedAt,
    }
    if event.NoteBody != "" {
        ne.NoteBody = event.NoteBody
    }
    // Populate actor fields — NoteAuthorID carries the actor for note events;
    // for mr_event (merge), it carries MergedByID (set during discovery).
    // For issue_label events, the label applier is not available from the
    // Issues API (state diff detection). The implementation must resolve the
    // label applier via GitLab's resource label events API:
    //   GET /projects/:id/issues/:iid/resource_label_events
    // and populate actor.id, actor.kind, actor.role, actor.is_entity_author
    // from the most recent label event matching the detected label name.
    // Label application requires Developer+ access, so actor.role will be
    // at least "write" for valid label events.
    if event.NoteAuthorID != 0 {
        ne.NoteAuthorID = event.NoteAuthorID
        ne.IsBot = event.IsBot
    } else if event.Type == "issue_label" {
        labelAuthor, err := p.resolveLabelAuthor(ctx, event.IID, event.Labels[0])
        if err == nil {
            ne.NoteAuthorID = labelAuthor.ID
            ne.IsBot = labelAuthor.IsBot
        }
    }
    if event.MRSource != 0 {
        ne.MRSourceProjectID = event.MRSource
        ne.MRTargetProjectID = event.MRTarget
    }
    // Populate transition.label for issue_label events — required by v1
    // schema when transition.kind == "label_changed".
    if event.Type == "issue_label" && len(event.Labels) > 0 {
        ne.TransitionLabel = dispatch.TransitionLabel{
            Name:   event.Labels[0],
            Action: "added",
        }
    }
    // Resolve actor.role from GitLab access level via Members API.
    // Required for dispatch core authorization (ADR 0054).
    // Guard: if NoteAuthorID is still zero (e.g., resolveLabelAuthor failed),
    // skip the event — fail-closed prevents dispatching without attribution.
    if ne.NoteAuthorID == 0 {
        return dispatch.NormalizedEvent{}, fmt.Errorf("unresolvable actor for %s event on IID %d", event.Type, event.IID)
    }
    ne.ActorRole = p.resolveActorRole(ctx, ne.NoteAuthorID)
    return ne, nil
}

type LabelAuthor struct {
    ID    int
    IsBot bool
}

// resolveLabelAuthor queries GitLab's resource label events API to find
// who applied a specific label. Returns the most recent label event
// matching the given label name.
//   GET /projects/:id/issues/:iid/resource_label_events
func (p *Poller) resolveLabelAuthor(ctx context.Context, issueIID int, labelName string) (LabelAuthor, error) {
    events, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, issueIID)
    if err != nil {
        return LabelAuthor{}, err
    }
    for i := len(events) - 1; i >= 0; i-- {
        if events[i].Label.Name == labelName && events[i].Action == "add" {
            return LabelAuthor{
                ID:    events[i].User.ID,
                IsBot: events[i].User.Bot,
            }, nil
        }
    }
    return LabelAuthor{}, fmt.Errorf("no label event found for %q on issue %d", labelName, issueIID)
}
```

The dispatch core evaluates each `NormalizedEvent` against harness CEL
`trigger` expressions to determine which stages to dispatch. This
replaces hardcoded label→stage or command→stage mappings with
user-configurable trigger rules, enabling functional parity with GitHub
while allowing per-project customization.

#### Input-driver pre-filters

The following filters run in the input driver *before* conversion to
`NormalizedEvent`, because they are transport-level concerns that should
never reach the dispatch core:

```go
// filterBotEvents removes bot-authored events that should not trigger
// dispatch. Exception: the enrolled fullsend bot's changes-requested
// markers are retained — these trigger the fix stage.
func (p *Poller) filterBotEvents(events []RoutableEvent) []RoutableEvent {
    var filtered []RoutableEvent
    for _, event := range events {
        if !event.IsBot {
            filtered = append(filtered, event)
            continue
        }
        // Retain the enrolled bot's changes-requested markers
        if event.Type == "mr_note" &&
            strings.Contains(event.NoteBody, "<!-- fullsend:changes-requested -->") &&
            event.NoteAuthorID == p.botUserID {
            filtered = append(filtered, event)
            continue
        }
    }
    return filtered
}

// isForkMR returns true if the MR is a fork (source != target) OR if
// fork status is unknown (zero-valued fields). Deny-by-default: when
// the fast-poll path omits MRSource/MRTarget, fork-sensitive stages
// (fix, code) are blocked rather than silently allowed.
func isForkMR(event RoutableEvent) bool {
    if event.MRSource == 0 || event.MRTarget == 0 {
        return true // unknown — deny by default
    }
    return event.MRSource != event.MRTarget
}
```

Fork MR protection is carried on the `NormalizedEvent` via
`MRSourceProjectID`/`MRTargetProjectID` fields. The dispatch core
enforces the deny-by-default rule: stages that modify repository
contents (fix, code) are blocked when the source and target projects
differ or when fork status is unknown.

Authorization (Developer-level access for slash commands, Reporter+ or
issue author for non-command comments on `needs-info` issues) is handled
by the dispatch core per
[ADR 0054](../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).
Label-triggered dispatches do not require a separate authorization gate —
GitLab restricts label application to Developer+ access at the platform
level. The `NoteAuthorID` field on `NormalizedEvent` provides the
identity for the authorization check on note-based triggers.

#### Label filtering

The hardcoded `routableLabels` set matches the current event routing table.
Custom CEL triggers referencing other labels will not fire for polled events
unless the label is added to this set. A future iteration could derive the
set dynamically from enrolled harness `trigger` expressions.

```go
var routableLabels = map[string]bool{
    "ready-to-code":    true,
    "ready-for-review": true,
    // "needs-info" is intentionally excluded — it is a condition qualifier
    // for note-based triage dispatch, not a label-added trigger. Adding
    // needs-info emits no dispatch event; instead, issue_note events on
    // issues bearing needs-info are routed to triage by the CEL trigger.
}

func filterRoutableLabels(labels []string) []string {
    var out []string
    for _, l := range labels {
        if routableLabels[l] {
            out = append(out, l)
        }
    }
    return out
}
```

### Deduplication

```go
func (p *Poller) deduplicate(events []RoutableEvent) []RoutableEvent {
    seen := make(map[string]bool)
    var unique []RoutableEvent

    for _, event := range events {
        key := event.Key()
        if seen[key] {
            continue
        }
        seen[key] = true
        unique = append(unique, event)
    }

    return unique
}

func (e RoutableEvent) Key() string {
    if e.NoteID != 0 {
        return fmt.Sprintf("note-%d", e.NoteID)
    }
    // Label events are emitted one-at-a-time (single label per RoutableEvent),
    // so the join is currently a single element. If label batching is added,
    // sort the slice first to ensure stable keys.
    sorted := append([]string(nil), e.Labels...)
    sort.Strings(sorted)
    return fmt.Sprintf("%s-%d-%s", e.Type, e.IID, strings.Join(sorted, ","))
}
```

### Label state tracking

The poller needs to distinguish "label was just added" from "label was already present". Since polling sees only current state (no `changes` object like webhook payloads provide), label change detection is implemented client-side via state comparison.

**Approach**: Store the set of previously-seen labels per issue in a CI/CD variable (`FULLSEND_LABEL_STATE`), encoded as JSON. On each poll, diff current labels against stored state. Only newly-appearing labels trigger routing.

```go
type LabelState map[int][]string // issue IID → label list

// detectNewLabels returns:
//   - newLabels: map of issue IID → newly-added labels
//   - updatedState: label state with all current labels marked as "seen"
//   - previousLabels: snapshot of each issue's previous labels (before update),
//     so the caller can restore entries for issues that couldn't be fully processed
//   - error
func (p *Poller) detectNewLabels(ctx context.Context, owner, repo string, issues []Issue) (map[int][]string, LabelState, map[int][]string, error) {
    // Read stored state
    stateJSON, err := p.client.GetVariable(ctx, owner, repo, "FULLSEND_LABEL_STATE")
    if err != nil {
        if errors.Is(err, forge.ErrNotFound) {
            stateJSON = "{}" // first run — all labels are "new"
        } else {
            return nil, nil, nil, fmt.Errorf("read label state: %w", err)
        }
    }

    var previousState LabelState
    if err := json.Unmarshal([]byte(stateJSON), &previousState); err != nil {
        // Graceful degradation: if stored JSON is corrupt or truncated
        // (e.g., exceeding GitLab's 10,000-char variable limit), fall back
        // to empty state — all current labels will be treated as "new,"
        // causing duplicate dispatches mitigated by resource_group.
        log.Warn("unmarshal label state failed, resetting to empty", "error", err)
        previousState = make(LabelState)
    }

    newLabels := make(map[int][]string)
    previousLabels := make(map[int][]string) // snapshot before update

    // Merge into previous state rather than replacing — only update entries
    // for issues present in the current poll, retaining entries for issues
    // not in the current result set. This prevents spurious "new label"
    // detections when a previously-tracked issue reappears after being
    // absent from the updated_after window.
    for _, issue := range issues {
        prev := previousState[issue.IID]
        previousLabels[issue.IID] = prev // snapshot for rollback
        prevSet := toSet(prev)

        // Only track fullsend-routable labels to keep state bounded
        // within GitLab's 10,000-character CI/CD variable limit.
        routable := filterRoutableLabels(issue.Labels)
        for _, label := range routable {
            if !prevSet[label] {
                newLabels[issue.IID] = append(newLabels[issue.IID], label)
            }
        }

        // Update this issue's entry with only routable labels
        previousState[issue.IID] = routable
    }

    // Prune closed issues to keep state bounded.
    // Skip IIDs in the current poll set — their state was just updated
    // and should not be pruned even if newly closed.
    polledIIDs := make(map[int]bool, len(issues))
    for _, issue := range issues {
        polledIIDs[issue.IID] = true
    }
    for iid := range previousState {
        if !polledIIDs[iid] && p.isIssueClosed(ctx, owner, repo, iid) {
            delete(previousState, iid)
        }
    }

    // Return newLabels, updated state, and previous labels WITHOUT persisting.
    // The caller filters out labels from failed dispatches and restores
    // entries for skipped issues before persisting.
    return newLabels, previousState, previousLabels, nil
}

func (p *Poller) persistLabelState(ctx context.Context, owner, repo string, state LabelState) error {
    stateBytes, err := json.Marshal(state)
    if err != nil {
        return fmt.Errorf("marshal label state: %w", err)
    }
    if err := p.client.UpdateCIVariable(ctx, owner, repo, "FULLSEND_LABEL_STATE", string(stateBytes), true); err != nil {
        return fmt.Errorf("persist label state: %w", err)
    }
    return nil
}
```

**CI/CD variable size limit**: GitLab CI/CD variables have a 10,000-character limit. For projects with many issues, the label state JSON may exceed this. Mitigation: only track issues with fullsend-relevant labels (`fullsend:*`), and prune entries for closed issues on each poll. If the state exceeds the limit, fall back to treating all matching labels as "new" (which may cause duplicate dispatches, handled by `resource_group` concurrency control).

### Watermark state management (`state.go`)

```go
func (p *Poller) readWatermark(ctx context.Context, owner, repo string) (time.Time, error) {
    varName := p.watermarkVarName()
    value, err := p.client.GetVariable(ctx, owner, repo, varName)
    if err != nil {
        if errors.Is(err, forge.ErrNotFound) {
            return time.Now().Add(-1 * time.Hour), nil
        }
        return time.Time{}, fmt.Errorf("read watermark %s: %w", varName, err)
    }
    return time.Parse(time.RFC3339, value)
}

func (p *Poller) watermarkVarName() string {
    if p.opts.SlashCommandsOnly {
        return "FULLSEND_LAST_POLL_AT_FAST"
    }
    return "FULLSEND_LAST_POLL_AT_FULL"
}

func (p *Poller) updateWatermark(ctx context.Context, owner, repo string, t time.Time) error {
    return p.client.UpdateCIVariable(ctx, owner, repo, p.watermarkVarName(), t.Format(time.RFC3339), true)
}
```

### Child pipeline dispatch (`dispatch.go`)

The poller dispatches agent stages by generating a child pipeline YAML file. The parent pipeline (poll.yml) uses `trigger: include: artifact:` to start child pipelines from the generated YAML. This keeps everything within GitLab's native pipeline hierarchy without requiring trigger tokens.

**Retry coverage boundary**: The watermark and label-state retry mechanisms (steps 5–6 in the poll loop) protect against poll-time failures — specifically, file I/O errors when writing `dispatches.json` via `appendDispatch`. They do NOT cover child pipeline runtime failures (agent crash, credential issue, transient API error), because the watermark advances as soon as the poll job completes successfully, before child pipelines execute. For child pipeline failures, the retry strategy is: (1) GitLab's native `retry:` keyword on child pipeline jobs for transient errors, (2) manual re-trigger via the GitLab UI or `/fs-*` slash command for persistent failures, (3) `resource_group` concurrency control ensures re-triggered stages don't conflict with in-progress runs.

```go
type Dispatch struct {
    Stage          string `json:"stage"`
    EventType      string `json:"event_type"`
    EventPayloadB64 string `json:"event_payload_b64"`
    ResourceKey    string `json:"resource_key"`
}

func (p *Poller) dispatch(ctx context.Context, owner, repo, stage string, event RoutableEvent) error {
    // Build minimal event payload
    payload := p.buildEventPayload(event)
    payloadB64 := base64.StdEncoding.EncodeToString(payload)

    dispatch := Dispatch{
        Stage:          stage,
        EventType:      event.Type,
        EventPayloadB64: payloadB64,
        ResourceKey:    fmt.Sprintf("%s-%d", event.Type, event.IID),
    }

    // Append to dispatches list. The --output flag writes all accumulated
    // dispatches as a JSON array (not NDJSON) so that downstream jq
    // commands like `jq 'length'` work correctly.
    if err := p.appendDispatch(dispatch); err != nil {
        return fmt.Errorf("append dispatch: %w", err)
    }
    return nil
}
```

**Child pipeline YAML generation:**

```go
func (p *Poller) generateChildPipelineYAML(dispatches []Dispatch) string {
    var buf bytes.Buffer
    for i, d := range dispatches {
        fmt.Fprintf(&buf, "agent-%d:\n", i)
        fmt.Fprintf(&buf, "  trigger:\n")
        fmt.Fprintf(&buf, "    include: .gitlab/ci/fullsend-agent.yml\n")
        fmt.Fprintf(&buf, "    strategy: depend\n")
        fmt.Fprintf(&buf, "  variables:\n")
        fmt.Fprintf(&buf, "    STAGE: %q\n", d.Stage)
        fmt.Fprintf(&buf, "    EVENT_TYPE: %q\n", d.EventType)
        fmt.Fprintf(&buf, "    EVENT_PAYLOAD_B64: %q\n", d.EventPayloadB64)
        fmt.Fprintf(&buf, "    RESOURCE_KEY: %q\n", d.ResourceKey)
        fmt.Fprintf(&buf, "  rules:\n")
        fmt.Fprintf(&buf, "    - when: always\n")
    }
    return buf.String()
}
```

### Files

| Action | Path |
|--------|------|
| Create | `internal/poll/poll.go` (~300 lines) |
| Create | `internal/poll/poll_test.go` |
| Create | `internal/poll/events.go` (~250 lines) |
| Create | `internal/poll/events_test.go` |
| Create | `internal/poll/dispatch.go` (~150 lines) |
| Create | `internal/poll/state.go` (~80 lines) |
| Modify | `internal/cli/root.go` — add `poll` subcommand |

## Phase 3: GitLab CI/CD Templates

**Goal**: Create pipeline YAML templates that are committed to enrolled projects during install.

### Directory structure

```
internal/scaffold/fullsend-repo-gitlab/
├── .gitlab-ci.yml
├── .gitlab/
│   └── ci/
│       ├── fullsend-dispatch.yml   ← MR event routing (native CI path)
│       ├── fullsend-poll.yml      ← cron-poller (scheduled pipeline)
│       └── fullsend-agent.yml     ← generic agent stage (parameterized by $STAGE)
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
  - local: '.gitlab/ci/fullsend-dispatch.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  - local: '.gitlab/ci/fullsend-poll.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "schedule"
  # The generic agent template (fullsend-agent.yml) is NOT included here —
  # it is included by the dynamically generated child pipeline YAML from
  # both the MR dispatch path (mr-dispatch-pipeline.yml) and the cron-poller
  # path (child-pipeline.yml). Including it in the root pipeline would add
  # a job that never matches (STAGE is unset in root context).

stages:
  - dispatch
  - poll
  - generate
  - agent

workflow:
  rules:
    # Native MR events (review only — retro uses cron-poller)
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    # Scheduled polling (triage, code, slash commands)
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    # Push-triggered pipelines are intentionally excluded — fullsend
    # agents respond to MR and schedule events only. Child pipelines
    # created via trigger: include: get their configuration from the
    # referenced artifact/file and do not re-read workflow:rules.
```

### MR dispatch (`.gitlab/ci/fullsend-dispatch.yml`)

Handles native MR events — routes `merge_request_event` pipelines to the review stage. The `merged)` case is a race-condition fallback only; the primary retro path is the cron-poller. Authorization (ADR 0054) is delegated to `fullsend run` at runtime, which evaluates the same dispatch core rules as the cron-poller path.

**Parent-child pipeline pattern**: The dispatch job generates a child pipeline YAML with `STAGE` embedded as a pipeline-level variable, then a trigger job launches it. This is necessary because GitLab evaluates `rules:` at pipeline creation time — dotenv variables from prior jobs are not available during rules evaluation, so passing `STAGE` via dotenv would prevent stage template `rules:` gates from matching.

```yaml
# fullsend-stage: dispatch (MR events only)

dispatch:
  stage: dispatch
  image: ghcr.io/fullsend-ai/fullsend-runner:latest
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  script:
    - |
      set -euo pipefail

      # CI_DEBUG_TRACE guard
      if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
        echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
        exit 1
      fi

      # GitLab has no predefined variable for the MR lifecycle action
      # (created, updated, merged, closed). CI_MERGE_REQUEST_EVENT_TYPE
      # indicates the pipeline execution mode (detached, merged_result,
      # merge_train), not the triggering action. Query state via the API.
      MR_STATE=$(curl -sf "${CI_API_V4_URL}/projects/${CI_PROJECT_ID}/merge_requests/${CI_MERGE_REQUEST_IID}" \
        -H "JOB-TOKEN: ${CI_JOB_TOKEN}" | jq -r '.state')

      case "${MR_STATE}" in
        merged)
          # Race-condition fallback: merge_request_event does not fire on
          # MR merge, but a pipeline triggered just before merge may query
          # state and find "merged". Primary retro path is the cron-poller.
          STAGE=retro
          RESOURCE_KEY="mr-${CI_MERGE_REQUEST_IID}"
          ;;
        opened)
          # GitLab returns "opened" for both new and reopened MRs —
          # there is no separate "reopened" state in the MR API response.
          STAGE=review
          RESOURCE_KEY="mr-${CI_MERGE_REQUEST_IID}"
          ;;
        closed)
          echo "MR closed without merge — no dispatch"
          echo 'no-op: { script: ["echo MR closed — no dispatch"], rules: [{ when: always }] }' > mr-dispatch-pipeline.yml
          exit 0
          ;;
        *)
          echo "Unhandled MR state: ${MR_STATE}"
          echo 'no-op: { script: ["echo Unhandled MR state"], rules: [{ when: always }] }' > mr-dispatch-pipeline.yml
          exit 0
          ;;
      esac

      # Generate child pipeline YAML — STAGE is a pipeline-level
      # variable so it is available during rules evaluation.
      cat > mr-dispatch-pipeline.yml <<YAML
variables:
  STAGE: "${STAGE}"
  RESOURCE_KEY: "${RESOURCE_KEY}"
  EVENT_TYPE: "merge_request_event"

include:
  - local: .gitlab/ci/fullsend-agent.yml
YAML
  artifacts:
    paths:
      - mr-dispatch-pipeline.yml
    expire_in: 1 hour

dispatch-mr-agents:
  stage: agent
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  needs:
    - job: dispatch
      artifacts: true
  trigger:
    include:
      - artifact: mr-dispatch-pipeline.yml
        job: dispatch
    strategy: depend
```

### Cron poller pipeline (`.gitlab/ci/fullsend-poll.yml`)

```yaml
# fullsend-stage: poll

poll-events:
  stage: poll
  image: ghcr.io/fullsend-ai/fullsend-runner:latest
  resource_group: fullsend-poll
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
  id_tokens:
    FULLSEND_ID_TOKEN:
      aud: "fullsend"
  variables:
    FULLSEND_FORGE: "gitlab"
  script:
    - |
      set -euo pipefail

      # CI_DEBUG_TRACE guard — critical in variable mode (sole defense
      # against PAT exposure at job init), defense-in-depth in WIF mode.
      if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
        echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
        exit 1
      fi

      # Credential retrieval — mode selected at install time
      if [ "${FULLSEND_CREDENTIAL_MODE}" = "wif" ]; then
        # WIF mode: exchange OIDC token for GCP credentials, then
        # retrieve bot PAT from Secret Manager.
        # id_tokens: declares FULLSEND_ID_TOKEN (token value as string),
        # not a _FILE variant — write to temp file for credential_source.
        OIDC_TOKEN_FILE=$(mktemp)
        echo "${FULLSEND_ID_TOKEN}" > "${OIDC_TOKEN_FILE}"
        gcloud auth login --cred-file=<(cat <<CRED
      {
        "type": "external_account",
        "audience": "//iam.googleapis.com/${FULLSEND_WIF_PROVIDER}",
        "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
        "token_url": "https://sts.googleapis.com/v1/token",
        "credential_source": { "file": "${OIDC_TOKEN_FILE}" },
        "service_account_impersonation_url": "https://iam.googleapis.com/v1/projects/-/serviceAccounts/${FULLSEND_SA}:generateAccessToken"
      }
      CRED
        )
        rm -f "${OIDC_TOKEN_FILE}"
        export FULLSEND_FORGE_TOKEN=$(gcloud secrets versions access latest \
          --secret="${FULLSEND_BOT_TOKEN_SECRET}" \
          --project="${FULLSEND_GCP_PROJECT_ID}")
      fi
      # In variable mode, FULLSEND_FORGE_TOKEN is already set from the
      # protected CI/CD variable — no retrieval step needed.

      # Run the poller — outputs dispatches.json
      fullsend poll \
        --forge gitlab \
        --project "${CI_PROJECT_PATH}" \
        --gitlab-url "${FULLSEND_GITLAB_URL:-${CI_SERVER_URL}}" \
        --output dispatches.json
  artifacts:
    paths:
      - dispatches.json
    expire_in: 1 hour

# Generate dynamic child pipeline YAML from poll results
generate-child-pipelines:
  stage: generate
  image: ghcr.io/fullsend-ai/fullsend-runner:latest
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
  needs:
    - job: poll-events
      artifacts: true
  script:
    - |
      set -euo pipefail

      # dispatches.json is a JSON array (not NDJSON) — the poller
      # writes all dispatches as a single array on completion.
      if [ ! -s dispatches.json ] || [ "$(jq 'length' dispatches.json)" = "0" ]; then
        echo "No events to dispatch"
        # Write a no-op child pipeline
        echo 'no-op: { script: ["echo No events"], rules: [{ when: always }] }' > child-pipeline.yml
        exit 0
      fi

      # Generate child pipeline YAML from dispatches
      fullsend poll generate-child-pipeline \
        --dispatches dispatches.json \
        --output child-pipeline.yml
  artifacts:
    paths:
      - child-pipeline.yml
    expire_in: 1 hour

# Trigger child pipelines for each dispatched event
dispatch-agents:
  stage: agent
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
  needs:
    - job: generate-child-pipelines
      artifacts: true
  trigger:
    include:
      - artifact: child-pipeline.yml
        job: generate-child-pipelines
    strategy: depend
```

### Generic agent template (`.gitlab/ci/fullsend-agent.yml`)

A single generic template handles all agent stages, parameterized by the `$STAGE` pipeline variable. All stages share the same credential retrieval flow (WIF or variable mode), authorization gate, and `fullsend run` invocation. Events arrive via parent pipeline variables (from the poller's child pipeline) or via native MR event dispatch.

```yaml
agent:
  stage: agent
  image: ghcr.io/fullsend-ai/fullsend-runner:latest
  id_tokens:
    FULLSEND_ID_TOKEN:
      aud: "fullsend"
  variables:
    FULLSEND_FORGE: "gitlab"
  script:
    - |
      set -euo pipefail

      # CI_DEBUG_TRACE guard
      if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
        echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
        exit 1
      fi

      # Credential retrieval — mode selected at install time
      # ... (WIF or variable mode — see actual template for full boilerplate)

      # Fork MR protection — code/fix stages only
      if [ "${STAGE}" = "code" ] || [ "${STAGE}" = "fix" ]; then
        if [ "${IS_FORK:-true}" = "true" ]; then
          echo "Fork MR detected — skipping ${STAGE} stage"
          exit 0
        fi
      fi

      # Run the agent — fullsend run resolves the harness file, reads
      # the image field, and creates the sandbox container via Podman.
      fullsend run "${STAGE}" \
        --fullsend-dir .fullsend \
        --target-repo . \
        --output-dir /tmp/fullsend-output \
        --forge gitlab \
        --run-url "${CI_PIPELINE_URL}" \
        --status-repo "${CI_PROJECT_PATH}" \
        --status-number "${CI_MERGE_REQUEST_IID:-0}"
  resource_group: "fullsend-${STAGE}-${RESOURCE_KEY}"
  rules:
    - if: $STAGE != ""
```

This mirrors how GitHub's `reusable-dispatch.yml` consolidates all stage logic into one file. The dispatch and poll templates set `STAGE` as a pipeline variable; the child pipeline `include: local: .gitlab/ci/fullsend-agent.yml` selects this template. When CEL triggers land (#2896-2901), the routing layer changes but the agent template is unaffected — it only needs `$STAGE`.

### Files

| Action | Path |
|--------|------|
| Create | `internal/scaffold/fullsend-repo-gitlab/` (entire tree) |
| Modify | `internal/scaffold/scaffold.go` — add `GitLabPerRepoFile()` and `WalkGitLabPerRepo()` functions |

## Phase 4: CLI Changes

**Goal**: `fullsend admin install group/project --forge gitlab` works end-to-end.

**Follow-up**: Consolidate kill switch and role enablement checks into `fullsend run` so both forges get them from Go code instead of duplicated shell scripts (#5416). Currently checked in GitHub's `reusable-dispatch.yml` and GitLab's `fullsend-agent.yml` (added in Phase 3, PR #3193).

### New flags

On `fullsend admin install`:
- `--forge {github|gitlab}` — auto-detected from remote URL, overridable
- `--gitlab-url` — GitLab instance URL (default: `${CI_SERVER_URL}`)
- `--poll-interval` — cron schedule for polling (default: auto-detect from tier)
- `--skip-schedule-create` — skip pipeline schedule creation (for externally managed schedules)

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

`fullsend admin install testgroup --forge gitlab` returns an error: "GitLab installation supports per-repo mode only. Provide a group/project path."

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

    // 6. Check CI_DEBUG_TRACE is not enabled at project or group level.
    // GET /projects/:id/variables/CI_DEBUG_TRACE — if exists and value == "true", fail.
    // Also check group-level: GET /groups/:id/variables for each ancestor group.
    // In variable mode, the script-level guard cannot prevent PAT exposure
    // because GitLab logs CI/CD variables at job init before any script runs.
    // Document that a Maintainer re-adding CI_DEBUG_TRACE after install (at
    // any level) bypasses the guard in variable mode.

    // 7. Create Project Access Token (Maintainer, api scope)
    // Maintainer role is required for CI/CD variable updates (watermark,
    // label state). POST /projects/:id/access_tokens
    // On Free tier gitlab.com, project access tokens are not available —
    // prompt the user for a personal access token instead.
    botPAT, err := createProjectAccessToken(ctx, client, owner, repo)
    if err != nil {
        log.Printf("project access token creation failed (Free tier?): %v", err)
        botPAT = promptForPersonalAccessToken()
    }

    // 8. Store bot PAT — mode depends on --gcp-project flag
    credentialMode := "variable" // default: no GCP required
    if opts.gcpProject != "" {
        credentialMode = "wif"
        // Store PAT in GCP Secret Manager
        storePATInSecretManager(ctx, opts.gcpProject, owner, repo, botPAT)
    } else {
        // Store PAT as a protected, masked CI/CD variable
        client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_FORGE_TOKEN", botPAT)
        maintainerCount := countMaintainers(ctx, client, owner, repo)
        if maintainerCount > 1 {
            log.Warn("Variable mode selected with %d Maintainers. Any Maintainer can "+
                "enable CI_DEBUG_TRACE after install, exposing the bot PAT in job logs. "+
                "Consider using --gcp-project for WIF mode instead.", maintainerCount)
        }
    }

    // 9. Detect GitLab tier for poll interval configuration
    tier := detectGitLabTier(ctx, client, owner, repo)
    pollInterval := determinePollInterval(tier, opts.pollInterval)
    // Free tier: "0 * * * *" (hourly)
    // Premium+: "*/5 * * * *" (every 5 minutes)

    // 10. Create pipeline schedule(s)
    if !opts.skipScheduleCreate {
        if tier == "premium" || tier == "ultimate" {
            // Fast poll: every 5 minutes, slash commands only
            client.CreatePipelineSchedule(ctx, owner, repo, project.DefaultBranch,
                "fullsend fast poll", "*/5 * * * *",
                map[string]string{"FULLSEND_POLL_MODE": "fast"})
            // Slow poll: every 15 minutes, full event scan
            client.CreatePipelineSchedule(ctx, owner, repo, project.DefaultBranch,
                "fullsend full poll", "*/15 * * * *",
                map[string]string{"FULLSEND_POLL_MODE": "full"})
        } else {
            // Free tier: single hourly poll
            client.CreatePipelineSchedule(ctx, owner, repo, project.DefaultBranch,
                "fullsend poll", "0 * * * *", nil)
        }
    }

    // 11. Commit CI/CD template files
    var scaffoldFiles []gitlab.CommitActionOptions
    scaffold.WalkGitLabPerRepo(func(path string, content []byte) error {
        scaffoldFiles = append(scaffoldFiles, gitlab.CommitActionOptions{
            Action:   gitlab.FileCreate,
            FilePath: path,
            Content:  string(content),
        })
        return nil
    })
    client.CommitFilesToBranch(ctx, owner, repo, project.DefaultBranch,
        "chore: add fullsend CI/CD pipeline", scaffoldFiles)

    // 12. Set protected CI/CD variables.
    // Use CreateProtectedCIVariable (Protected: true, Masked: false) for
    // configuration identifiers — CreateRepoSecret (Protected + Masked)
    // requires values >= 8 characters (e.g. "wif" would fail) and masks
    // GCP resource names in logs, hindering debugging.
    client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_CREDENTIAL_MODE", credentialMode)
    if credentialMode == "wif" {
        client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_WIF_PROVIDER", wifProviderResourceName)
        client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_SA", serviceAccountEmail)
        client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_BOT_TOKEN_SECRET", secretManagerSecretName)
        client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_GCP_PROJECT_ID", opts.gcpProject)
    }
    client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_FORGE", "gitlab")
    client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_PER_REPO_INSTALL", "true")

    // 13. Initialize poll watermarks (protected — must not be accessible
    // to pipelines on non-protected branches to prevent tampering).
    // Separate watermarks for fast-poll (slash commands only) and
    // full-poll (all events) to prevent fast polls from advancing
    // the watermark past unprocessed label/note events.
    // Cross-mode duplicate dispatch (fast-poll discovers a slash command,
    // full-poll re-discovers it) is harmless — resource_group serialization
    // ensures at most one pipeline at a time; see ADR 0067.
    initTime := time.Now().Format(time.RFC3339)
    client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_LAST_POLL_AT_FAST", initTime)
    client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_LAST_POLL_AT_FULL", initTime)
    client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_LABEL_STATE", "{}")

    // 14. Set up inference WIF (if --inference-project provided)

    // 15. Print CI minute warning for shared runners
    if tier == "free" {
        log.Warn("Free tier detected. Polling will consume CI minutes on shared runners. " +
            "Consider using self-hosted runners. See ADR 0067 for details.")
    }
}
```

### Tier detection

```go
func detectGitLabTier(ctx context.Context, client *gitlab.LiveClient, owner, repo string) string {
    // Try to create a test pipeline schedule with 5-min interval.
    // If it fails with "is too frequent", we're on Free tier.
    // This is a heuristic — GitLab doesn't expose the tier via API.
    //
    // Alternative: check if project access tokens are available
    // (Premium+ on gitlab.com, all tiers on self-managed).
    //
    // For self-managed instances, assume Premium capabilities
    // (admins can configure any schedule interval).
}
```

### Uninstall flow

```go
func runGitLabPerRepoUninstall(ctx context.Context, target string, opts uninstallOpts) error {
    owner, repo := splitOwnerRepo(target)

    // 1. Delete pipeline schedules
    schedules, _ := client.ListPipelineSchedules(ctx, owner, repo)
    for _, s := range schedules {
        if strings.HasPrefix(s.Description, "fullsend") {
            client.DeletePipelineSchedule(ctx, owner, repo, s.ID)
        }
    }

    // 2. Revoke project access token
    // 3. Clean up credential storage (mode-dependent)
    //    - WIF mode: delete Secret Manager secret, remove WIF attribute condition
    //    - Variable mode: delete FULLSEND_FORGE_TOKEN CI/CD variable
    // 4. Remove CI/CD template files
    // 5. Remove CI/CD variables (FULLSEND_LAST_POLL_AT_FAST, FULLSEND_LAST_POLL_AT_FULL,
    //    FULLSEND_LABEL_STATE, FULLSEND_CREDENTIAL_MODE, FULLSEND_FORGE,
    //    FULLSEND_PER_REPO_INSTALL)
}
```

### Files

| Action | Path |
|--------|------|
| Modify | `internal/cli/admin.go` — add flags, `runGitLabPerRepoInstall()`, token resolution |
| Modify | `internal/cli/root.go` — add `poll` subcommand |
| Create | `internal/cli/poll.go` — `fullsend poll` command |
| Modify | `internal/config/config.go` — add `Forge` field, validation |

## Phase 5: Integration and Testing

### Integration wiring

- `fullsend run --forge gitlab` constructs a GitLab forge client with bot PAT from `FULLSEND_FORGE_TOKEN`
- `fullsend poll --forge gitlab` runs the polling loop
- Config schema accepts `forge: gitlab` in `config.yaml`
- Forge detection integrated into CLI argument parsing

### Unit tests

| Component | Test focus |
|-----------|-----------|
| GitLab forge client | Mock HTTP responses via `httptest.NewServer`. Cover: MR creation, comment posting, label operations. Review synthesis from notes + approvals. Error handling. Subgroup paths. Polling query methods (`ListIssuesUpdatedSince`, etc.). |
| Poller | Event discovery with mock API responses. Slash command detection. Label state diffing. Event routing. Deduplication. Watermark management. Fast-poll vs full-poll modes. |
| Forge detection | GitHub URL → `"github"`. GitLab URL → `"gitlab"`. SSH remote → error. Self-hosted → error with flag suggestion. `--forge` override. |
| CLI | GitLab argument parsing. Per-repo enforcement for GitLab. Token resolution chain. Poll interval selection by tier. |
| Config | `forge: gitlab` validation. Unknown forge rejection. |

### Integration tests

Mock GitLab API → poller → child pipeline generation:
1. Poller discovers new issue with `ready-to-code` label → dispatches code stage
2. Poller discovers `/fs-triage` comment → dispatches triage stage
3. Poller discovers MR comment with changes-requested marker (same project) → dispatches fix stage
4. Poller discovers MR comment with changes-requested marker (fork MR) → skips fix stage
5. Poller skips bot-authored comments → no dispatch
6. Poller handles empty poll (no events since last watermark) → no dispatch, watermark advances to current time
7. Poller deduplicates events across overlapping windows → single dispatch
8. Label state tracking: newly-added label triggers dispatch, pre-existing label does not
9. Poller discovers MR with `merged_at > watermark` → dispatches retro stage
10. Full install flow with mock GitLab API (no real GitLab instance)

### E2E tests

Against GitLab.com:
1. Create a test project
2. Run `fullsend admin install group/project --forge gitlab`
3. Verify pipeline schedule(s) created with correct intervals
4. Create an issue with `/fs-triage` comment
5. Wait for next poll cycle → verify triage pipeline fires and triage agent runs
6. Add `ready-to-code` label to issue
7. Wait for next poll cycle → verify code pipeline fires and code agent creates MR
8. Verify review pipeline fires immediately on MR open (native CI path)
9. Merge MR → wait for next poll cycle → verify retro pipeline fires (cron-poller path)
10. Run `fullsend admin uninstall group/project --forge gitlab`
11. Verify cleanup (schedule deleted, project access token revoked, variables deleted)

Self-hosted testing: Docker-based GitLab CE instance for version compatibility testing. Minimum GitLab version: 17.0+ (stable trigger API, CI/CD variable protection, pipeline schedules).

### FakeClient updates

Add implementations to `internal/forge/fake.go` for:
- `IsProtectedBranch` — configurable return value
- `CreatePipelineSchedule` — record call, return fake schedule ID
- `DeletePipelineSchedule` — record call
- `UpdateCIVariable` — record call

## Security-Critical Code Paths

These paths require extra review attention. A bug here is a security vulnerability, not just a functional failure.

### 1. Pipeline schedule targets protected default branch only

**File**: `internal/cli/admin.go` (install flow), `.gitlab-ci.yml` (workflow rules)

The pipeline schedule MUST target the protected default branch. The `workflow:rules` enforce `$CI_COMMIT_REF_PROTECTED == "true"` for scheduled pipelines.

```yaml
# CORRECT — schedule always targets default branch
workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
```

**Consequence of bug**: Pipeline runs on a non-protected branch. In WIF mode, WIF attribute conditions (requiring `ref_protected == "true"`) provide defense-in-depth — the OIDC token exchange fails. In variable mode, protected variable status prevents exposure on non-protected branches.

### 2. Protected variable creation

**File**: `internal/forge/gitlab/gitlab.go`

When creating CI/CD variables for secrets, the `Protected` flag MUST be `true`. Protected variables are only exposed to pipelines running on protected branches.

**Consequence of bug**: Any pipeline (including on MR branches with attacker-modified `.gitlab-ci.yml`) can see credentials. In WIF mode, this exposes WIF configuration (OIDC token replay within ~5 minute TTL). In variable mode, this directly exposes the bot PAT.

### 3. `CI_DEBUG_TRACE` guard

**Files**: All CI/CD template YAML files, `internal/cli/admin.go`

Every stage pipeline must exit early if debug tracing is detected. This prevents credential leakage through verbose job logs. **In variable mode, this guard is the sole defense** — GitLab logs all CI/CD variables at job initialization, before any script runs. In WIF mode, the guard is defense-in-depth — even if bypassed, the PAT is not in a CI/CD variable and is retrieved after the guard runs.

### 4. Fork MR blocking

**File**: `internal/poll/events.go` (poller routing), `.gitlab/ci/fullsend-agent.yml` (pipeline template)

Fork MR protection in three places:
- `isForkMR` helper denies when `source_project_id != target_project_id`
- `isForkMR` also denies when source/target are unknown (zero-valued) — this covers the fast-poll path where MR details are not fetched, ensuring deny-by-default
- Fix pipeline template checks `source_project_id != target_project_id` (defense-in-depth)

**Consequence of bug**: Fork MR triggers fix/code pipeline that pushes commits to the target project.

### 5. Slash command authorization

**File**: `internal/dispatch/core.go` (dispatch core, shared across input drivers)

The dispatch core MUST verify that slash command authors have Developer-level (30+) project access before dispatching agent stages, per [ADR 0054](../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md). The `NormalizedEvent.NoteAuthorID` field carries the author identity from the input driver to the dispatch core. Without this check, Guest/Reporter users could post `/fs-code` or `/fs-fix` commands to trigger agent stages.

**Exception — needs-info triage**: Comments on issues with the `needs-info` label trigger triage with a reduced authorization gate — the commenter must have at least Reporter-level (20+) project access or be the issue author. This mirrors the GitHub path where ADR 0054 requires `author_association != NONE` for non-command triage triggers, preventing unauthenticated cost exposure on public projects. This exception applies only to the triage stage — all other agent stages require Developer+ authorization via slash commands.

**Consequence of bug**: Unauthorized users trigger code generation or fix stages, potentially modifying repository contents.

### 6. Event payload base64 encoding

**File**: `internal/poll/dispatch.go`

Event payloads MUST be base64-encoded before passing as child pipeline variables.

**Consequence of bug**: YAML injection via issue titles or MR descriptions containing YAML metacharacters.

### 7. Bot comment filtering

**File**: `internal/poll/events.go`

The poller MUST skip bot-authored comments to prevent the agent's own replies from re-triggering agent stages. Exception: bot-authored comments containing `<!-- fullsend:changes-requested -->` markers must trigger the fix stage.

**Consequence of bug**: Infinite loop — agent posts a comment, poller detects it as a new event, dispatches the stage again.

### 8. Poll state variable protection

**File**: `internal/poll/state.go`

Both `FULLSEND_LAST_POLL_AT_FAST`, `FULLSEND_LAST_POLL_AT_FULL`, and `FULLSEND_LABEL_STATE` MUST be protected (created as protected variables during install). Tampering with any requires Maintainer access — the same privilege level as modifying the pipeline. Separate watermarks prevent fast polls (slash commands only) from advancing past unprocessed label/note events that the full poll handles.

**Consequence of bug**: For the watermark, an attacker could set it far in the future (skipping events) or far in the past (reprocessing old events). Reprocessing is handled by deduplication and `resource_group` concurrency control. Skipping is the higher risk — but requires Maintainer access, which is already within the insider threat model. For the label state, an attacker could clear it so all existing labels re-fire as "new," causing spurious agent stage dispatches.

## Verification Checklist

- [ ] `make go-test` — all unit tests pass (existing + new)
- [ ] `make go-vet` — no issues
- [ ] `make lint` — passes
- [ ] Poller unit test covers: event discovery, NormalizedEvent conversion, label state diffing, dedup
- [ ] Poller unit test verifies bot comment pre-filtering (both skip and changes-requested exception)
- [ ] Dispatch core unit test verifies fork MR protection and slash command authorization via NormalizedEvent fields
- [ ] GitLab client unit test asserts `Protected: true` on secret variable creation
- [ ] All stage YAML files contain `CI_DEBUG_TRACE` guard
- [ ] Fix stage YAML contains fork MR protection
- [ ] `workflow:rules` require `$CI_COMMIT_REF_PROTECTED == "true"` for scheduled pipelines
- [ ] Poll watermark variable created as protected during install
- [ ] Event payloads base64-encoded before passing to child pipelines
- [ ] Child pipeline YAML generation produces valid GitLab CI syntax
- [ ] `fullsend admin install --dry-run testgroup/testproject --forge gitlab` shows correct plan
- [ ] `fullsend admin install testgroup --forge gitlab` returns per-repo enforcement error
- [ ] E2E: Install on GitLab.com test project → pipeline schedules created → issue events detected → agent pipelines fire → uninstall cleans up
