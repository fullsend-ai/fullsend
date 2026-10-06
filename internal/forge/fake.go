package forge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Compile-time interface checks.
var _ Client = (*FakeClient)(nil)
var _ GitHubExtensions = (*FakeClient)(nil)
var _ GitLabExtensions = (*FakeClient)(nil)

// NewFakeClient returns a FakeClient with all maps initialised.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		FileContents:             make(map[string][]byte),
		WorkflowRuns:             make(map[string]*WorkflowRun),
		Secrets:                  make(map[string]bool),
		SecretProtections:        make(map[string]SecretProtection),
		VariablesExist:           make(map[string]bool),
		VariableValues:           make(map[string]string),
		Errors:                   make(map[string]error),
		DirContents:              make(map[string][]DirectoryEntry),
		FileContentsRef:          make(map[string][]byte),
		BranchRefs:               make(map[string]string),
		ExistingBranches:         make(map[string]bool),
		Refs:                     make(map[string]string),
		ProtectedBranches:        make(map[string]bool),
		ProtectedBranchRules:     make(map[string]*ProtectedBranchRule),
		ProtectedTags:            make(map[string]bool),
		PipelineSchedules:        make(map[string][]PipelineSchedule),
		ForceReachableCommits:    make(map[string]int),
		PipelineTriggerTokens:    make(map[string][]PipelineTriggerToken),
		ProjectHooks:             make(map[string][]ProjectHook),
		PipelineVarOverrideRoles: make(map[string]string),
		// New trigger tokens are owned by a Developer-level user unless a
		// test says otherwise.
		TriggerTokenOwnerID: 1001,
		ProjectMemberAccess: map[int64]int{1001: GitLabAccessLevelDeveloper},
	}
}

// FileRecord records a file creation/update call.
type FileRecord struct {
	Owner, Repo, Path, Branch, Message string
	Content                            []byte
}

// SecretRecord records a secret creation call.
type SecretRecord struct {
	Owner, Repo, Name, Value string
}

// OrgVariableRecord records an org-level variable creation/update call.
type OrgVariableRecord struct {
	Org, Name, Value string
}

// VariableRecord records a variable creation/update call.
type VariableRecord struct {
	Owner, Repo, Name, Value string
	Protected                bool
}

// PipelineCallRecord records a CreatePipeline invocation.
type PipelineCallRecord struct {
	Owner, Repo, Ref string
	Variables        map[string]string
}

// PipelineInputsCallRecord records a CreatePipelineWithInputs invocation.
type PipelineInputsCallRecord struct {
	Owner, Repo, Ref string
	Inputs           map[string]PipelineInputValue
}

// ProtectedBranchMergeGrantRecord records a GrantProtectedBranchMergeUser call.
type ProtectedBranchMergeGrantRecord struct {
	Owner, Repo, Branch string
	UserID              int
}

// UpdatedCommentRecord records an issue comment update call.
type UpdatedCommentRecord struct {
	Owner, Repo string
	CommentID   int
	Body        string
}

// NoteOnParentRecord records a GetNoteOnParent/UpdateNoteOnParent call,
// letting tests assert that a caller addressed a note directly by its
// parent's type and IID instead of going through the ID-only scan that
// GetIssueComment/UpdateIssueComment use.
type NoteOnParentRecord struct {
	Owner, Repo, ParentType string
	ParentIID, NoteID       int
	Body                    string // empty for GetNoteOnParent calls
}

// CreatedIssueRecord records an issue creation call.
type CreatedIssueRecord struct {
	Owner, Repo string
	Title, Body string
	Labels      []string
	Number      int
}

// MinimizedCommentRecord records a comment minimize call.
type MinimizedCommentRecord struct {
	NodeID string
	Reason string
}

// ReactionRecord records an AddIssueReaction call.
type ReactionRecord struct {
	ID          int64
	Owner, Repo string
	Number      int
	Content     string
}

// CommentReactionRecord records an AddIssueCommentReaction call.
type CommentReactionRecord struct {
	Owner, Repo string
	CommentID   int
	Content     string
}

// ReviewRecord records a pull request review creation call.
type ReviewRecord struct {
	Owner, Repo string
	Number      int
	Event, Body string
	CommitSHA   string
	Comments    []ReviewComment
}

// DismissedReviewRecord records a review dismissal call.
type DismissedReviewRecord struct {
	Owner, Repo string
	Number      int
	ReviewID    int
	Message     string
}

// BranchSHARecord records a CreateBranchFromSHA call.
type BranchSHARecord struct {
	Owner, Repo, Branch, SHA string
}

// CommitFilesRecord records a CommitFiles call.
type CommitFilesRecord struct {
	Owner, Repo, Message string
	Files                []TreeFile
}

// CommitFilesToBranchRecord records a CommitFilesToBranch call.
type CommitFilesToBranchRecord struct {
	Owner, Repo, Branch, Message string
	Files                        []TreeFile
}

// ForceCommitFileToBranchRecord records a ForceCommitFileToBranch call.
type ForceCommitFileToBranchRecord struct {
	Owner, Repo, Branch, Path, Message string
	Content                            []byte
	StartSHA                           string
	Force                              bool
}

// ForceCommitFixedBaseSHA is the synthetic parent SHA of every FakeClient
// force-re-root commit. The GitLab implementation uses the repository's
// root commit; tests assert the fake always re-roots on this same base.
const ForceCommitFixedBaseSHA = "0000000000000000000000000000000000000001"

// FakeClient is a thread-safe test double for forge.Client.
// Pre-populate its fields to control return values, and inspect
// recorder slices after the test to verify which calls were made.
type FakeClient struct {
	mu sync.Mutex

	// Pre-populated data
	Repos                     []Repository
	OrgRepos                  map[string][]Repository  // per-org repos; when set, ListOrgRepos uses this instead of Repos
	FileContents              map[string][]byte        // key: "owner/repo/path"
	WorkflowRuns              map[string]*WorkflowRun  // key: "owner/repo/workflow"
	WorkflowRunsList          map[string][]WorkflowRun // key: "owner/repo/workflow" → multiple runs (takes precedence over WorkflowRuns)
	RecentWorkflowRuns        map[string][]WorkflowRun // key: "owner/repo"
	WorkflowRunArtifacts      map[int][]WorkflowArtifact
	WorkflowArtifactContents  map[int][]byte
	RepositoryArtifacts       map[string][]RepositoryArtifact // key: owner/repo
	Workflows                 map[string]*Workflow            // key: "owner/repo/workflow"
	AuthenticatedUser         string                          // login returned by GetAuthenticatedUser
	AuthenticatedUserIdentity *UserIdentity                   // identity returned by GetAuthenticatedUserIdentity
	OrgPlan                   string                          // plan name returned by GetOrgPlan (default: "free")
	Installations             []Installation
	Secrets                   map[string]bool             // key: "owner/repo/name"
	SecretProtections         map[string]SecretProtection // key: "owner/repo/name"; overrides the default fully-protected report for existing secrets
	PullRequests              map[string][]ChangeProposal // key: "owner/repo"
	TokenScopes               []string                    // scopes returned by GetTokenScopes
	InstallationToken         bool                        // IsInstallationToken return value
	VariablesExist            map[string]bool             // key: "owner/repo/name"
	VariableValues            map[string]string           // key: "owner/repo/name"

	// ForkOwner controls the return value of CreateFork. When non-empty,
	// CreateFork returns this value as the fork owner login. When empty,
	// CreateFork uses AuthenticatedUser.
	ForkOwner string

	// ExistingForks maps "owner/repo" to the fork owner login returned
	// by FindExistingFork. Entries simulate an already-existing fork.
	ExistingForks map[string]string

	// ForkParents maps "owner/repo" to the parent "owner/repo" for repos
	// that have Fork=true in Repos. Used by CreateForkInOrg to verify
	// that an existing fork has the expected parent (non-fork collision
	// detection). Only consulted when a repo in Repos has Fork=true.
	ForkParents map[string]string

	// App client IDs for GetAppClientID
	AppClientIDs map[string]string // key: app slug → client ID

	// CollaboratorPermissions maps "owner/repo/username" → role_name for GetCollaboratorPermission.
	CollaboratorPermissions map[string]string
	// AddedCollaborators records AddCollaborator calls as "owner/repo/username" → permission.
	AddedCollaborators map[string]string
	// OrgMemberships maps "org/username" → membership for GetOrgMembership.
	OrgMemberships map[string]OrgMembership

	// Org-level secret state
	OrgSecrets map[string]bool // key: "org/name"

	// Org-level variable state
	OrgVariables      map[string]bool   // key: "org/name"
	InstanceVariables []string          // instance-level CI/CD variable names
	OrgVariableValues map[string]string // key: "org/name" → value

	// Protected branches for IsProtectedBranch.
	ProtectedBranches map[string]bool // key: "owner/repo/branch"

	// ProtectedBranchRules stores push/merge access for GetProtectedBranch.
	// Key: "owner/repo/branch". When a key is present, the branch is
	// protected even if ProtectedBranches is false.
	ProtectedBranchRules map[string]*ProtectedBranchRule

	// ProtectedTags lists protected-tag rules for ListProtectedTags.
	ProtectedTags map[string]bool // key: "owner/repo/tag-pattern"

	// GrantedProtectedBranchMergeUsers records GrantProtectedBranchMergeUser calls.
	GrantedProtectedBranchMergeUsers []ProtectedBranchMergeGrantRecord

	// Pipeline schedules for List/Create/Delete/UpdatePipelineSchedule.
	PipelineSchedules map[string][]PipelineSchedule // key: "owner/repo"

	// PipelineTriggerTokens stores trigger tokens keyed by "owner/repo".
	PipelineTriggerTokens map[string][]PipelineTriggerToken
	// TriggerTokenOwnerID is the owner recorded on tokens minted by
	// CreatePipelineTriggerToken (GitLab binds a token to its creator).
	TriggerTokenOwnerID int64
	// ProjectMemberAccess maps a user ID to its effective project access
	// level for GetProjectMemberAccessLevel; absent users are not members.
	ProjectMemberAccess map[int64]int

	// ProjectHooks stores project webhooks keyed by "owner/repo".
	ProjectHooks map[string][]ProjectHook

	// PipelineVarOverrideRoles stores the GitLab
	// ci_pipeline_variables_minimum_override_role setting per project.
	// Key: "owner/repo". Missing keys round-trip as empty string.
	PipelineVarOverrideRoles map[string]string

	// Directory listings for ListDirectoryContents.
	DirContents map[string][]DirectoryEntry // key: "owner/repo/path@ref"

	// File contents at specific refs for GetFileContentAtRef.
	FileContentsRef map[string][]byte // key: "owner/repo/path@ref"

	// ForceReachableCommits maps "owner/repo/branch" to the number of
	// commits reachable on that branch after force-re-root writes.
	// Always 1 after a successful ForceCommitFileToBranch.
	ForceReachableCommits map[string]int

	// ExistingBranches, when set, makes CreateBranch and CreateBranchFromSHA
	// return ErrAlreadyExists for those keys ("owner/repo/branch").
	// DeleteBranch removes the matching key so a subsequent create succeeds.
	ExistingBranches map[string]bool

	// Branch refs for GetBranchRef.
	BranchRefs map[string]string // key: "owner/repo/branch" → commit SHA

	// Refs for GetRef.
	Refs map[string]string // key: "owner/repo/refPath" → commit SHA

	// Error injection: key is method name, value is error to return.
	Errors map[string]error

	// CreateBranchErrors injects per-repo errors for CreateBranch.
	// Key is "owner/repo", checked before the generic Errors map.
	CreateBranchErrors map[string]error

	// DeleteFilesErrors injects per-repo errors for DeleteFiles.
	// Key is "owner/repo", checked before the generic Errors map.
	DeleteFilesErrors map[string]error

	// GetFileContentErrors injects per-path errors for GetFileContent.
	// Key is "owner/repo/path", checked before the generic Errors map.
	GetFileContentErrors map[string]error

	// Issue comments for ListIssueComments / UpdateIssueComment.
	IssueComments map[string][]IssueComment // key: "owner/repo/number"
	OpenIssues    map[string][]Issue        // key: "owner/repo"

	// CommitFilesChanged controls the return value of both CommitFiles and
	// CommitFilesToBranch (default true). A single field suffices because
	// callers that test the fallback path inject an error on CommitFiles,
	// so only CommitFilesToBranch reads this value in practice.
	CommitFilesChanged *bool

	// CommitFilesErrSeq is an error queue for CommitFiles. Each call shifts
	// the first element; when empty, falls through to Errors["CommitFiles"].
	// A nil entry means no error for that call.
	CommitFilesErrSeq []error

	// CreateReviewErrSeq is an error queue for CreatePullRequestReview.
	// Each call shifts the first element; when empty, falls through to
	// Errors["CreatePullRequestReview"]. A nil entry means no error
	// for that call.
	CreateReviewErrSeq []error

	// CreatePipelineScheduleErrSeq is an error queue for CreatePipelineSchedule.
	// Each call shifts the first element; when empty, falls through to
	// Errors["CreatePipelineSchedule"]. A nil entry means no error for that call.
	CreatePipelineScheduleErrSeq []error

	// Pull request head SHA for GetPullRequestHeadSHA.
	PullRequestHeadSHA string

	// Pull request info for GetPullRequestInfo.
	PullRequestInfos map[string]PullRequestInfo // key: "owner/repo/number"

	// Pull request files for ListPullRequestFiles.
	PRFiles map[string][]string // key: "owner/repo/number"

	// Pull request commit SHAs (oldest first) for ListPullRequestCommits.
	PRCommits map[string][]string // key: "owner/repo/number"

	// Pull request file diffs for ListPullRequestFileDiffs.
	PRFileDiffs map[string][]PullRequestFileDiff // key: "owner/repo/number"

	// Pull request reviews for ListPullRequestReviews.
	PRReviews map[string][]PullRequestReview // key: "owner/repo/number"

	// WorkflowRunJobs for ListWorkflowRunJobs.
	WorkflowRunJobs map[int][]WorkflowJob // key: runID

	// Annotations for GetWorkflowRunAnnotations.
	Annotations []Annotation

	// Call recorders
	CreatedRepos            []Repository
	CreatedFiles            []FileRecord
	CreatedBranches         []string // "owner/repo/branch"
	CreatedBranchSHAs       []BranchSHARecord
	DeletedBranches         []string // "owner/repo/branch"
	DeletedRefs             []string // "owner/repo/refPath"
	CreatedProposals        []ChangeProposal
	DeletedRepos            []string // "owner/repo"
	DeletedFiles            []FileRecord
	CreatedSecrets          []SecretRecord
	DeletedSecrets          []SecretRecord
	Variables               []VariableRecord
	DeletedVariables        []VariableRecord
	DeletedOrgSecrets       []string // "org/name"
	CreatedOrgVariables     []OrgVariableRecord
	DeletedOrgVariables     []string // "org/name"
	CreatedIssues           []CreatedIssueRecord
	UpdatedComments         []UpdatedCommentRecord
	GetNoteOnParentCalls    []NoteOnParentRecord
	UpdateNoteOnParentCalls []NoteOnParentRecord
	MinimizedComments       []MinimizedCommentRecord
	AddedReactions          []ReactionRecord
	DeletedReactions        []int64
	AddedCommentReactions   []CommentReactionRecord
	DeletedCommentReactions []int64
	CreatedReviews          []ReviewRecord
	DismissedReviews        []DismissedReviewRecord
	CommittedFiles          []CommitFilesRecord
	CommittedFilesToBranch  []CommitFilesToBranchRecord
	ForceCommittedFiles     []ForceCommitFileToBranchRecord
	CreatedForks            []string // "owner/repo"
	ClosedProposals         []int    // PR numbers
	DeletedComments         []int    // comment IDs
	CreatedPipelines        []Pipeline
	PipelineCalls           []PipelineCallRecord
	PipelineInputsCalls     []PipelineInputsCallRecord
	CreatedSchedules        []PipelineSchedule
	DeletedScheduleIDs      []int64
	UpdatedScheduleIDs      []int64
	UpdatedVariables        []VariableRecord
	CreatedProtectedVars    []VariableRecord
	CreatedTriggerTokens    []PipelineTriggerToken
	RevokedTriggerTokenIDs  []int64
	CreatedProjectHooks     []ProjectHook
	UpdatedProjectHooks     []ProjectHook
	DeletedProjectHookIDs   []int64

	// CommitAncestry maps "owner/repo/base/head" to a comparison status
	// string ("ahead", "behind", "identical", "diverged") for CompareCommits.
	CommitAncestry map[string]string

	// internal counters
	proposalCounter int
	commentCounter  int
	issueCounter    int
	reactionCounter int64
	forceCommitSeq  int
	triggerTokenSeq int64
	projectHookSeq  int64
}

// err checks for an injected error for the given method name.
func (f *FakeClient) err(method string) error {
	if f.Errors == nil {
		return nil
	}
	return f.Errors[method]
}

func (f *FakeClient) ListOrgRepos(_ context.Context, org string, includePrivate bool) ([]Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListOrgRepos"); e != nil {
		return nil, e
	}

	source := f.Repos
	if f.OrgRepos != nil {
		source = f.OrgRepos[org]
	}

	var result []Repository
	for _, r := range source {
		if r.Archived || r.Fork {
			continue
		}
		if r.Private && !includePrivate {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

func (f *FakeClient) CreateRepo(_ context.Context, org, name, description string, private bool) (*Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateRepo"); e != nil {
		return nil, e
	}

	fullName := org + "/" + name
	// Check for duplicates in pre-populated repos.
	for _, r := range f.Repos {
		if r.FullName == fullName {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, fullName)
		}
	}
	// Check for duplicates in previously created repos.
	for _, r := range f.CreatedRepos {
		if r.FullName == fullName {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, fullName)
		}
	}

	r := Repository{
		Name:          name,
		FullName:      fullName,
		DefaultBranch: "main",
		Private:       private,
	}
	f.CreatedRepos = append(f.CreatedRepos, r)
	return &r, nil
}

func (f *FakeClient) GetRepo(_ context.Context, owner, repo string) (*Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetRepo"); e != nil {
		return nil, e
	}

	fullName := owner + "/" + repo
	for i := range f.Repos {
		if f.Repos[i].FullName == fullName {
			return &f.Repos[i], nil
		}
	}
	// Also check created repos.
	for i := range f.CreatedRepos {
		if f.CreatedRepos[i].FullName == fullName {
			return &f.CreatedRepos[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, owner, repo)
}

func (f *FakeClient) UpdateRepoVisibility(_ context.Context, owner, repo string, private bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("UpdateRepoVisibility"); e != nil {
		return e
	}

	fullName := owner + "/" + repo
	for i := range f.Repos {
		if f.Repos[i].FullName == fullName {
			f.Repos[i].Private = private
			return nil
		}
	}
	for i := range f.CreatedRepos {
		if f.CreatedRepos[i].FullName == fullName {
			f.CreatedRepos[i].Private = private
			return nil
		}
	}
	return fmt.Errorf("%w: %s/%s", ErrNotFound, owner, repo)
}

func (f *FakeClient) DeleteRepo(_ context.Context, owner, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteRepo"); e != nil {
		return e
	}

	f.DeletedRepos = append(f.DeletedRepos, owner+"/"+repo)

	// Remove from Repos.
	fullName := owner + "/" + repo
	filtered := f.Repos[:0]
	for _, r := range f.Repos {
		if r.FullName != fullName {
			filtered = append(filtered, r)
		}
	}
	f.Repos = filtered

	// Remove from CreatedRepos.
	filteredCreated := f.CreatedRepos[:0]
	for _, r := range f.CreatedRepos {
		if r.FullName != fullName {
			filteredCreated = append(filteredCreated, r)
		}
	}
	f.CreatedRepos = filteredCreated

	// Remove associated file contents.
	prefix := fullName + "/"
	for k := range f.FileContents {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(f.FileContents, k)
		}
	}

	return nil
}

func (f *FakeClient) FindExistingFork(_ context.Context, owner, repo string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("FindExistingFork"); e != nil {
		return "", "", e
	}

	if f.ExistingForks != nil {
		if forkOwner, ok := f.ExistingForks[owner+"/"+repo]; ok {
			return forkOwner, repo, nil
		}
	}
	return "", "", nil
}

func (f *FakeClient) CreateFork(_ context.Context, owner, repo string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateFork"); e != nil {
		return "", "", e
	}

	f.CreatedForks = append(f.CreatedForks, owner+"/"+repo)

	forkOwner := f.ForkOwner
	if forkOwner == "" {
		forkOwner = f.AuthenticatedUser
	}
	if forkOwner == "" {
		forkOwner = "fake-fork-owner"
	}

	// Auto-populate the fork in Repos so GetRepo (and therefore
	// waitForFork) finds it immediately instead of hanging for the
	// full timeout.
	f.Repos = append(f.Repos, Repository{
		FullName:      forkOwner + "/" + repo,
		Name:          repo,
		DefaultBranch: "main",
		Fork:          true,
	})

	return forkOwner, repo, nil
}

func (f *FakeClient) CreateForkInOrg(_ context.Context, owner, repo, org, forkName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateForkInOrg"); e != nil {
		return "", e
	}

	// Check if a repo with the target name already exists.
	targetFullName := org + "/" + forkName
	sourceFullName := owner + "/" + repo
	for _, r := range f.Repos {
		if r.FullName == targetFullName {
			if !r.Fork {
				return "", fmt.Errorf("repo %s already exists and is not a fork: %w",
					targetFullName, ErrNotFork)
			}
			if f.ForkParents != nil {
				if parent, ok := f.ForkParents[targetFullName]; ok && parent != sourceFullName {
					return "", fmt.Errorf("repo %s is a fork of %s, not %s: %w",
						targetFullName, parent, sourceFullName, ErrNotFork)
				}
			}
			// Existing fork of the correct source — idempotent.
			return forkName, nil
		}
	}

	f.CreatedForks = append(f.CreatedForks, owner+"/"+repo)
	return forkName, nil
}

func (f *FakeClient) CreateFile(_ context.Context, owner, repo, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateFile"); e != nil {
		return e
	}

	f.CreatedFiles = append(f.CreatedFiles, FileRecord{
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Message: message,
		Content: content,
	})

	if f.FileContents == nil {
		f.FileContents = make(map[string][]byte)
	}
	f.FileContents[owner+"/"+repo+"/"+path] = content
	return nil
}

func (f *FakeClient) CreateOrUpdateFile(_ context.Context, owner, repo, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrUpdateFile"); e != nil {
		return e
	}

	f.CreatedFiles = append(f.CreatedFiles, FileRecord{
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Message: message,
		Content: content,
	})

	if f.FileContents == nil {
		f.FileContents = make(map[string][]byte)
	}
	f.FileContents[owner+"/"+repo+"/"+path] = content
	return nil
}

func (f *FakeClient) GetFileContent(_ context.Context, owner, repo, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := owner + "/" + repo + "/" + path
	if f.GetFileContentErrors != nil {
		if e, ok := f.GetFileContentErrors[key]; ok {
			return nil, e
		}
	}
	if e := f.err("GetFileContent"); e != nil {
		return nil, e
	}
	data, ok := f.FileContents[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return data, nil
}

func (f *FakeClient) DeleteFile(_ context.Context, owner, repo, path, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteFile"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + path
	if _, ok := f.FileContents[key]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}

	delete(f.FileContents, key)
	f.DeletedFiles = append(f.DeletedFiles, FileRecord{
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Message: message,
	})
	return nil
}

func (f *FakeClient) ListRepositoryFiles(_ context.Context, owner, repo string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListRepositoryFiles"); e != nil {
		return nil, e
	}

	prefix := owner + "/" + repo + "/"
	var paths []string
	for key := range f.FileContents {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			paths = append(paths, key[len(prefix):])
		}
	}
	return paths, nil
}

func (f *FakeClient) DeleteFiles(_ context.Context, owner, repo, message string, paths []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e, ok := f.DeleteFilesErrors[owner+"/"+repo]; ok {
		return 0, e
	}
	if e := f.err("DeleteFiles"); e != nil {
		return 0, e
	}

	var deleted int
	for _, path := range paths {
		key := owner + "/" + repo + "/" + path
		if _, ok := f.FileContents[key]; !ok {
			continue
		}
		delete(f.FileContents, key)
		f.DeletedFiles = append(f.DeletedFiles, FileRecord{
			Owner:   owner,
			Repo:    repo,
			Path:    path,
			Message: message,
		})
		deleted++
	}
	return deleted, nil
}

func (f *FakeClient) ListDirectoryContents(_ context.Context, owner, repo, path, ref string, _ bool) ([]DirectoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListDirectoryContents"); e != nil {
		return nil, e
	}

	key := fmt.Sprintf("%s/%s/%s@%s", owner, repo, path, ref)
	entries, ok := f.DirContents[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return entries, nil
}

func (f *FakeClient) GetFileContentAtRef(_ context.Context, owner, repo, path, ref string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetFileContentAtRef"); e != nil {
		return nil, e
	}

	key := fmt.Sprintf("%s/%s/%s@%s", owner, repo, path, ref)
	content, ok := f.FileContentsRef[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return content, nil
}

func (f *FakeClient) CommitFiles(_ context.Context, owner, repo, message string, files []TreeFile) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.CommitFilesErrSeq) > 0 {
		e := f.CommitFilesErrSeq[0]
		f.CommitFilesErrSeq = f.CommitFilesErrSeq[1:]
		if e != nil {
			return false, e
		}
	} else if e := f.err("CommitFiles"); e != nil {
		return false, e
	}

	f.CommittedFiles = append(f.CommittedFiles, CommitFilesRecord{
		Owner:   owner,
		Repo:    repo,
		Message: message,
		Files:   files,
	})

	f.applyFileContents(owner, repo, files)

	changed := f.CommitFilesChanged == nil || *f.CommitFilesChanged
	return changed, nil
}

func (f *FakeClient) CommitFilesToBranch(_ context.Context, owner, repo, branch, message string, files []TreeFile) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CommitFilesToBranch"); e != nil {
		return false, e
	}

	f.CommittedFilesToBranch = append(f.CommittedFilesToBranch, CommitFilesToBranchRecord{
		Owner:   owner,
		Repo:    repo,
		Branch:  branch,
		Message: message,
		Files:   files,
	})

	f.applyFileContents(owner, repo, files)

	changed := f.CommitFilesChanged == nil || *f.CommitFilesChanged
	return changed, nil
}

func (f *FakeClient) ForceCommitFileToBranch(_ context.Context, owner, repo, branch, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ForceCommitFileToBranch"); e != nil {
		return e
	}
	if branch == "" || path == "" {
		return fmt.Errorf("force commit: branch and path are required")
	}

	if !strings.Contains(message, "[skip ci]") {
		message = strings.TrimSpace(message)
		if message == "" {
			message = "[skip ci]"
		} else {
			message += " [skip ci]"
		}
	}

	copied := make([]byte, len(content))
	copy(copied, content)

	f.ForceCommittedFiles = append(f.ForceCommittedFiles, ForceCommitFileToBranchRecord{
		Owner:    owner,
		Repo:     repo,
		Branch:   branch,
		Path:     path,
		Message:  message,
		Content:  copied,
		StartSHA: ForceCommitFixedBaseSHA,
		Force:    true,
	})

	if f.FileContentsRef == nil {
		f.FileContentsRef = make(map[string][]byte)
	}
	// Force-re-root replaces the branch tree with base + this one file.
	prefix := owner + "/" + repo + "/"
	suffix := "@" + branch
	for k := range f.FileContentsRef {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, suffix) {
			delete(f.FileContentsRef, k)
		}
	}
	f.FileContentsRef[fmt.Sprintf("%s/%s/%s@%s", owner, repo, path, branch)] = copied

	f.forceCommitSeq++
	tipSHA := fmt.Sprintf("force-%d", f.forceCommitSeq)
	if f.BranchRefs == nil {
		f.BranchRefs = make(map[string]string)
	}
	if f.Refs == nil {
		f.Refs = make(map[string]string)
	}
	f.BranchRefs[owner+"/"+repo+"/"+branch] = tipSHA
	f.Refs[owner+"/"+repo+"/heads/"+branch] = tipSHA

	if f.ForceReachableCommits == nil {
		f.ForceReachableCommits = make(map[string]int)
	}
	f.ForceReachableCommits[owner+"/"+repo+"/"+branch] = 1

	return nil
}

func (f *FakeClient) applyFileContents(owner, repo string, files []TreeFile) {
	if f.FileContents == nil {
		f.FileContents = make(map[string][]byte)
	}
	for _, file := range files {
		key := owner + "/" + repo + "/" + file.Path
		if file.Delete {
			delete(f.FileContents, key)
		} else {
			f.FileContents[key] = file.Content
		}
	}
}

func (f *FakeClient) getRefLocked(owner, repo, refPath string) (string, bool) {
	key := owner + "/" + repo + "/" + refPath
	sha, ok := f.Refs[key]
	return sha, ok
}

func (f *FakeClient) GetRef(_ context.Context, owner, repo, refPath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetRef"); e != nil {
		return "", e
	}

	sha, ok := f.getRefLocked(owner, repo, refPath)
	if !ok {
		return "", fmt.Errorf("%w: ref %s in %s/%s", ErrNotFound, refPath, owner, repo)
	}
	return sha, nil
}

func (f *FakeClient) GetBranchRef(_ context.Context, owner, repo, branch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetBranchRef"); e != nil {
		return "", e
	}
	key := owner + "/" + repo + "/" + branch
	if sha, ok := f.BranchRefs[key]; ok {
		return sha, nil
	}
	sha, ok := f.getRefLocked(owner, repo, "heads/"+branch)
	if !ok {
		return "", fmt.Errorf("%w: ref heads/%s in %s/%s", ErrNotFound, branch, owner, repo)
	}
	return sha, nil
}

func (f *FakeClient) CreateBranch(_ context.Context, owner, repo, branchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.CreateBranchErrors != nil {
		if e, ok := f.CreateBranchErrors[owner+"/"+repo]; ok {
			return e
		}
	}
	if e := f.err("CreateBranch"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + branchName
	if f.ExistingBranches[key] {
		return fmt.Errorf("%w: branch %s", ErrAlreadyExists, branchName)
	}

	f.CreatedBranches = append(f.CreatedBranches, key)
	return nil
}

func (f *FakeClient) CreateBranchFromSHA(_ context.Context, owner, repo, branchName, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.CreateBranchErrors != nil {
		if e, ok := f.CreateBranchErrors[owner+"/"+repo]; ok {
			return e
		}
	}
	if e := f.err("CreateBranchFromSHA"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + branchName
	if f.ExistingBranches[key] {
		return fmt.Errorf("%w: branch %s", ErrAlreadyExists, branchName)
	}

	f.CreatedBranches = append(f.CreatedBranches, key)
	f.CreatedBranchSHAs = append(f.CreatedBranchSHAs, BranchSHARecord{
		Owner: owner, Repo: repo, Branch: branchName, SHA: sha,
	})
	return nil
}

func (f *FakeClient) DeleteBranch(_ context.Context, owner, repo, branchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteBranch"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + branchName
	f.DeletedBranches = append(f.DeletedBranches, key)
	delete(f.ExistingBranches, key)
	return nil
}

func (f *FakeClient) DeleteRef(_ context.Context, owner, repo, refPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteRef"); e != nil {
		return e
	}

	f.DeletedRefs = append(f.DeletedRefs, owner+"/"+repo+"/"+refPath)
	return nil
}

func (f *FakeClient) CreateFileOnBranch(_ context.Context, owner, repo, branch, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateFileOnBranch"); e != nil {
		return e
	}

	f.CreatedFiles = append(f.CreatedFiles, FileRecord{
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Branch:  branch,
		Message: message,
		Content: content,
	})
	return nil
}

func (f *FakeClient) CreateOrUpdateFileOnBranch(_ context.Context, owner, repo, branch, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrUpdateFileOnBranch"); e != nil {
		return e
	}

	f.CreatedFiles = append(f.CreatedFiles, FileRecord{
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Branch:  branch,
		Message: message,
		Content: content,
	})
	// Also update FileContents so subsequent reads see the new content.
	if f.FileContents == nil {
		f.FileContents = make(map[string][]byte)
	}
	f.FileContents[owner+"/"+repo+"/"+path] = content
	return nil
}

func (f *FakeClient) CreateChangeProposal(_ context.Context, owner, repo, title, body, head, base string) (*ChangeProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateChangeProposal"); e != nil {
		return nil, e
	}

	f.proposalCounter++
	cp := ChangeProposal{
		URL:    fmt.Sprintf("https://forge.example.com/%s/%s/pull/%d", owner, repo, f.proposalCounter),
		Title:  title,
		Number: f.proposalCounter,
		Head:   head,
		Base:   base,
	}
	f.CreatedProposals = append(f.CreatedProposals, cp)
	return &cp, nil
}

func (f *FakeClient) CreateCrossRepoChangeProposal(_ context.Context, baseOwner, baseRepo, headOwner, headRepo, title, body, headBranch, baseBranch string) (*ChangeProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateCrossRepoChangeProposal"); e != nil {
		return nil, e
	}

	f.proposalCounter++
	cp := ChangeProposal{
		URL:    fmt.Sprintf("https://forge.example.com/%s/%s/pull/%d", baseOwner, baseRepo, f.proposalCounter),
		Title:  title,
		Number: f.proposalCounter,
		Head:   headOwner + ":" + headBranch,
		Base:   baseBranch,
	}
	f.CreatedProposals = append(f.CreatedProposals, cp)
	return &cp, nil
}

func (f *FakeClient) ListRepoPullRequests(_ context.Context, owner, repo string) ([]ChangeProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListRepoPullRequests"); e != nil {
		return nil, e
	}

	if f.PullRequests != nil {
		if prs, ok := f.PullRequests[owner+"/"+repo]; ok {
			return prs, nil
		}
	}
	return []ChangeProposal{}, nil
}

func (f *FakeClient) CloseChangeProposal(_ context.Context, _, _ string, number int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CloseChangeProposal"); e != nil {
		return e
	}

	f.ClosedProposals = append(f.ClosedProposals, number)
	return nil
}

func (f *FakeClient) GetOrgPlan(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetOrgPlan"); e != nil {
		return "", e
	}

	if f.OrgPlan == "" {
		return "free", nil
	}
	return f.OrgPlan, nil
}

func (f *FakeClient) GetAuthenticatedUser(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetAuthenticatedUser"); e != nil {
		return "", e
	}

	return f.AuthenticatedUser, nil
}

func (f *FakeClient) GetAuthenticatedUserIdentity(_ context.Context) (*UserIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetAuthenticatedUserIdentity"); e != nil {
		return nil, e
	}

	if f.AuthenticatedUserIdentity != nil {
		return f.AuthenticatedUserIdentity, nil
	}
	return nil, fmt.Errorf("%w: no user identity configured", ErrNotFound)
}

func (f *FakeClient) GetTokenScopes(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetTokenScopes"); e != nil {
		return nil, e
	}

	return f.TokenScopes, nil
}

func (f *FakeClient) IsInstallationToken(_ context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("IsInstallationToken"); e != nil {
		return false, e
	}

	return f.InstallationToken, nil
}

func (f *FakeClient) CreateRepoSecret(_ context.Context, owner, repo, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateRepoSecret"); e != nil {
		return e
	}

	f.CreatedSecrets = append(f.CreatedSecrets, SecretRecord{
		Owner: owner,
		Repo:  repo,
		Name:  name,
		Value: value,
	})
	if f.Secrets == nil {
		f.Secrets = make(map[string]bool)
	}
	f.Secrets[owner+"/"+repo+"/"+name] = true
	return nil
}

func (f *FakeClient) DeleteRepoSecret(_ context.Context, owner, repo, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteRepoSecret"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + name
	if f.Secrets != nil {
		delete(f.Secrets, key)
	}
	f.DeletedSecrets = append(f.DeletedSecrets, SecretRecord{
		Owner: owner,
		Repo:  repo,
		Name:  name,
	})
	return nil
}

func (f *FakeClient) RepoSecretExists(_ context.Context, owner, repo, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("RepoSecretExists"); e != nil {
		return false, e
	}

	if f.Secrets == nil {
		return false, nil
	}
	return f.Secrets[owner+"/"+repo+"/"+name], nil
}

func (f *FakeClient) GetRepoSecretProtection(_ context.Context, owner, repo, name string) (SecretProtection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetRepoSecretProtection"); e != nil {
		return SecretProtection{}, e
	}

	key := owner + "/" + repo + "/" + name
	if p, ok := f.SecretProtections[key]; ok {
		return p, nil
	}
	if !f.Secrets[key] {
		return SecretProtection{}, nil
	}
	return SecretProtection{Exists: true, Masked: true, Protected: true}, nil
}

func (f *FakeClient) CreateOrUpdateRepoVariable(_ context.Context, owner, repo, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrUpdateRepoVariable"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + name
	if f.VariableValues == nil {
		f.VariableValues = make(map[string]string)
	}
	f.VariableValues[key] = value
	if f.VariablesExist == nil {
		f.VariablesExist = make(map[string]bool)
	}
	f.VariablesExist[key] = true
	f.Variables = append(f.Variables, VariableRecord{
		Owner: owner,
		Repo:  repo,
		Name:  name,
		Value: value,
	})
	return nil
}

func (f *FakeClient) RepoVariableExists(_ context.Context, owner, repo, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("RepoVariableExists"); e != nil {
		return false, e
	}

	if f.VariablesExist == nil {
		return false, nil
	}
	return f.VariablesExist[owner+"/"+repo+"/"+name], nil
}

func (f *FakeClient) GetRepoVariable(_ context.Context, owner, repo, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetRepoVariable"); e != nil {
		return "", false, e
	}

	if f.VariableValues != nil {
		if val, ok := f.VariableValues[owner+"/"+repo+"/"+name]; ok {
			return val, true, nil
		}
	}
	return "", false, nil
}

func (f *FakeClient) ListRepoVariables(_ context.Context, owner, repo string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListRepoVariables"); e != nil {
		return nil, e
	}

	prefix := owner + "/" + repo + "/"
	result := make(map[string]string)
	for key, val := range f.VariableValues {
		if strings.HasPrefix(key, prefix) {
			name := strings.TrimPrefix(key, prefix)
			result[name] = val
		}
	}
	return result, nil
}

func (f *FakeClient) DeleteRepoVariable(_ context.Context, owner, repo, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteRepoVariable"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + name
	if f.VariableValues != nil {
		delete(f.VariableValues, key)
	}
	if f.VariablesExist != nil {
		delete(f.VariablesExist, key)
	}
	f.DeletedVariables = append(f.DeletedVariables, VariableRecord{
		Owner: owner,
		Repo:  repo,
		Name:  name,
	})
	return nil
}

func (f *FakeClient) GetWorkflow(_ context.Context, owner, repo, workflowFile string) (*Workflow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetWorkflow"); e != nil {
		return nil, e
	}

	key := owner + "/" + repo + "/" + workflowFile
	if f.Workflows != nil {
		if wf, ok := f.Workflows[key]; ok {
			return wf, nil
		}
	}

	return &Workflow{
		Name:  workflowFile,
		Path:  ".github/workflows/" + workflowFile,
		State: "active",
	}, nil
}

func (f *FakeClient) GetLatestWorkflowRun(_ context.Context, owner, repo, workflowFile string) (*WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetLatestWorkflowRun"); e != nil {
		return nil, e
	}

	key := owner + "/" + repo + "/" + workflowFile
	run, ok := f.WorkflowRuns[key]
	if !ok {
		return nil, fmt.Errorf("no workflow run found: %s", key)
	}
	return run, nil
}

func (f *FakeClient) GetWorkflowRun(_ context.Context, owner, repo string, runID int) (*WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetWorkflowRun"); e != nil {
		return nil, e
	}

	for _, run := range f.WorkflowRuns {
		if run.ID == runID {
			return run, nil
		}
	}
	return nil, fmt.Errorf("workflow run %d not found in %s/%s", runID, owner, repo)
}

func (f *FakeClient) DispatchWorkflow(_ context.Context, _, _, _, _ string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DispatchWorkflow"); e != nil {
		return e
	}

	return nil
}

func (f *FakeClient) CreateIssue(_ context.Context, owner, repo, title, body string, labels ...string) (*Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("CreateIssue"); e != nil {
		return nil, e
	}
	f.issueCounter++
	issue := Issue{
		Number: f.issueCounter,
		Title:  title,
		Body:   body,
		URL:    fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, f.issueCounter),
		Labels: append([]string(nil), labels...),
	}
	f.CreatedIssues = append(f.CreatedIssues, CreatedIssueRecord{
		Owner:  owner,
		Repo:   repo,
		Title:  title,
		Body:   body,
		Labels: append([]string(nil), labels...),
		Number: issue.Number,
	})
	key := owner + "/" + repo
	if f.OpenIssues == nil {
		f.OpenIssues = make(map[string][]Issue)
	}
	f.OpenIssues[key] = append(f.OpenIssues[key], issue)
	return &issue, nil
}

func (f *FakeClient) AddIssueLabels(_ context.Context, owner, repo string, number int, labels ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("AddIssueLabels"); e != nil {
		return e
	}
	key := owner + "/" + repo
	issues := f.OpenIssues[key]
	for i := range issues {
		if issues[i].Number != number {
			continue
		}
		seen := make(map[string]struct{}, len(issues[i].Labels))
		for _, l := range issues[i].Labels {
			seen[l] = struct{}{}
		}
		for _, l := range labels {
			if _, ok := seen[l]; !ok {
				issues[i].Labels = append(issues[i].Labels, l)
				seen[l] = struct{}{}
			}
		}
		f.OpenIssues[key][i] = issues[i]
		return nil
	}
	return fmt.Errorf("issue #%d not found", number)
}

func (f *FakeClient) GetIssue(_ context.Context, owner, repo string, number int) (*Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetIssue"); e != nil {
		return nil, e
	}
	for _, issue := range f.OpenIssues[owner+"/"+repo] {
		if issue.Number == number {
			copy := issue
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (f *FakeClient) CloseIssue(_ context.Context, _, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err("CloseIssue")
}

func (f *FakeClient) ListOpenIssues(_ context.Context, owner, repo string, labels ...string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListOpenIssues"); e != nil {
		return nil, e
	}
	if f.OpenIssues == nil {
		return nil, nil
	}
	issues := f.OpenIssues[owner+"/"+repo]
	if len(labels) == 0 {
		return append([]Issue(nil), issues...), nil
	}
	filtered := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if issueHasLabels(issue, labels) {
			filtered = append(filtered, issue)
		}
	}
	return filtered, nil
}

func issueHasLabels(issue Issue, labels []string) bool {
	present := make(map[string]struct{}, len(issue.Labels))
	for _, label := range issue.Labels {
		present[label] = struct{}{}
	}
	for _, label := range labels {
		if _, ok := present[label]; !ok {
			return false
		}
	}
	return true
}

func (f *FakeClient) ListIssueComments(_ context.Context, owner, repo string, number int) ([]IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListIssueComments"); e != nil {
		return nil, e
	}
	if f.IssueComments != nil {
		key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
		if comments, ok := f.IssueComments[key]; ok {
			return comments, nil
		}
	}
	return nil, nil
}

func (f *FakeClient) CreateIssueComment(_ context.Context, owner, repo string, number int, body string) (*IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("CreateIssueComment"); e != nil {
		return nil, e
	}
	f.commentCounter++
	comment := IssueComment{
		ID:        f.commentCounter,
		NodeID:    fmt.Sprintf("IC_fake_%d", f.commentCounter),
		HTMLURL:   fmt.Sprintf("https://github.com/%s/%s/issues/%d#issuecomment-%d", owner, repo, number, f.commentCounter),
		Body:      body,
		Author:    f.AuthenticatedUser,
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	if f.IssueComments == nil {
		f.IssueComments = make(map[string][]IssueComment)
	}
	f.IssueComments[key] = append(f.IssueComments[key], comment)
	return &comment, nil
}

func (f *FakeClient) GetIssueComment(_ context.Context, _, _ string, commentID int) (*IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetIssueComment"); e != nil {
		return nil, e
	}
	for _, comments := range f.IssueComments {
		for _, c := range comments {
			if c.ID == commentID {
				found := c
				return &found, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: comment %d", ErrNotFound, commentID)
}

// GetNoteOnParent implements GitLabExtensions. Unlike the real GitLab
// client, FakeClient has no notion of noteTarget-vs-parentType mismatch;
// it records the call (so tests can assert the direct-addressing path
// was taken) and otherwise looks the note up the same way GetIssueComment
// does.
func (f *FakeClient) GetNoteOnParent(_ context.Context, owner, repo, parentType string, parentIID, noteID int) (*IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GetNoteOnParentCalls = append(f.GetNoteOnParentCalls, NoteOnParentRecord{
		Owner: owner, Repo: repo, ParentType: parentType, ParentIID: parentIID, NoteID: noteID,
	})
	if e := f.err("GetNoteOnParent"); e != nil {
		return nil, e
	}
	for _, comments := range f.IssueComments {
		for _, c := range comments {
			if c.ID == noteID {
				found := c
				return &found, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: comment %d", ErrNotFound, noteID)
}

// UpdateNoteOnParent implements GitLabExtensions. See GetNoteOnParent.
func (f *FakeClient) UpdateNoteOnParent(_ context.Context, owner, repo, parentType string, parentIID, noteID int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpdateNoteOnParentCalls = append(f.UpdateNoteOnParentCalls, NoteOnParentRecord{
		Owner: owner, Repo: repo, ParentType: parentType, ParentIID: parentIID, NoteID: noteID, Body: body,
	})
	if e := f.err("UpdateNoteOnParent"); e != nil {
		return e
	}
	for key, comments := range f.IssueComments {
		for i, c := range comments {
			if c.ID == noteID {
				f.IssueComments[key][i].Body = body
				return nil
			}
		}
	}
	return fmt.Errorf("%w: comment %d", ErrNotFound, noteID)
}

func (f *FakeClient) UpdateIssueComment(_ context.Context, owner, repo string, commentID int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("UpdateIssueComment"); e != nil {
		return e
	}
	f.UpdatedComments = append(f.UpdatedComments, UpdatedCommentRecord{
		Owner:     owner,
		Repo:      repo,
		CommentID: commentID,
		Body:      body,
	})
	for key, comments := range f.IssueComments {
		for i, c := range comments {
			if c.ID == commentID {
				f.IssueComments[key][i].Body = body
				return nil
			}
		}
	}
	return fmt.Errorf("%w: comment %d", ErrNotFound, commentID)
}

func (f *FakeClient) DeleteIssueComment(_ context.Context, _, _ string, commentID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("DeleteIssueComment"); e != nil {
		return e
	}
	f.DeletedComments = append(f.DeletedComments, commentID)
	for key, comments := range f.IssueComments {
		for i, c := range comments {
			if c.ID == commentID {
				f.IssueComments[key] = append(comments[:i], comments[i+1:]...)
				return nil
			}
		}
	}
	return nil
}

func (f *FakeClient) MinimizeComment(_ context.Context, nodeID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("MinimizeComment"); e != nil {
		return e
	}
	f.MinimizedComments = append(f.MinimizedComments, MinimizedCommentRecord{
		NodeID: nodeID,
		Reason: reason,
	})
	return nil
}

func (f *FakeClient) AddIssueReaction(_ context.Context, owner, repo string, number int, content string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("AddIssueReaction"); e != nil {
		return 0, e
	}
	f.reactionCounter++
	f.AddedReactions = append(f.AddedReactions, ReactionRecord{
		ID:      f.reactionCounter,
		Owner:   owner,
		Repo:    repo,
		Number:  number,
		Content: content,
	})
	return f.reactionCounter, nil
}

func (f *FakeClient) DeleteIssueReaction(_ context.Context, _, _ string, _ int, reactionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("DeleteIssueReaction"); e != nil {
		return e
	}
	f.DeletedReactions = append(f.DeletedReactions, reactionID)
	return nil
}

func (f *FakeClient) AddIssueCommentReaction(_ context.Context, owner, repo string, commentID int, content string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("AddIssueCommentReaction"); e != nil {
		return 0, e
	}
	f.reactionCounter++
	f.AddedCommentReactions = append(f.AddedCommentReactions, CommentReactionRecord{
		Owner:     owner,
		Repo:      repo,
		CommentID: commentID,
		Content:   content,
	})
	return f.reactionCounter, nil
}

func (f *FakeClient) DeleteIssueCommentReaction(_ context.Context, _, _ string, _ int, reactionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("DeleteIssueCommentReaction"); e != nil {
		return e
	}
	f.DeletedCommentReactions = append(f.DeletedCommentReactions, reactionID)
	return nil
}

func (f *FakeClient) ListIssueReactions(_ context.Context, owner, repo string, number int) ([]Reaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListIssueReactions"); e != nil {
		return nil, e
	}
	deleted := make(map[int64]bool, len(f.DeletedReactions))
	for _, id := range f.DeletedReactions {
		deleted[id] = true
	}
	var reactions []Reaction
	for _, r := range f.AddedReactions {
		if r.Owner == owner && r.Repo == repo && r.Number == number && !deleted[r.ID] {
			reactions = append(reactions, Reaction{
				Content: r.Content,
				User:    f.AuthenticatedUser,
			})
		}
	}
	return reactions, nil
}

func (f *FakeClient) GetPullRequestInfo(_ context.Context, owner, repo string, number int) (*PullRequestInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetPullRequestInfo"); e != nil {
		return nil, e
	}
	if f.PullRequestInfos != nil {
		key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
		if info, ok := f.PullRequestInfos[key]; ok {
			return &info, nil
		}
	}
	return nil, ErrNotFound
}

func (f *FakeClient) GetPullRequestHeadSHA(_ context.Context, _, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetPullRequestHeadSHA"); e != nil {
		return "", e
	}
	return f.PullRequestHeadSHA, nil
}

func (f *FakeClient) ListPullRequestFiles(_ context.Context, owner, repo string, number int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListPullRequestFiles"); e != nil {
		return nil, e
	}
	if f.PRFiles != nil {
		key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
		if files, ok := f.PRFiles[key]; ok {
			return files, nil
		}
	}
	return nil, nil
}

func (f *FakeClient) ListPullRequestCommits(_ context.Context, owner, repo string, number int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListPullRequestCommits"); e != nil {
		return nil, e
	}
	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	return f.PRCommits[key], nil
}

func (f *FakeClient) ListPullRequestFileDiffs(_ context.Context, owner, repo string, number int) ([]PullRequestFileDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListPullRequestFileDiffs"); e != nil {
		return nil, e
	}
	if f.PRFileDiffs != nil {
		key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
		if files, ok := f.PRFileDiffs[key]; ok {
			return files, nil
		}
	}
	return nil, nil
}

func (f *FakeClient) CreatePullRequestReview(_ context.Context, owner, repo string, number int, event, body, commitSHA string, comments []ReviewComment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.CreateReviewErrSeq) > 0 {
		e := f.CreateReviewErrSeq[0]
		f.CreateReviewErrSeq = f.CreateReviewErrSeq[1:]
		if e != nil {
			return e
		}
	}
	if e := f.err("CreatePullRequestReview"); e != nil {
		return e
	}
	f.CreatedReviews = append(f.CreatedReviews, ReviewRecord{
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Event:     event,
		Body:      body,
		CommitSHA: commitSHA,
		Comments:  comments,
	})

	review := PullRequestReview{
		ID:     len(f.CreatedReviews) + 1000,
		NodeID: fmt.Sprintf("PRR_fake_%d", len(f.CreatedReviews)+1000),
		User:   f.AuthenticatedUser,
		State:  event,
		Body:   body,
	}
	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	if f.PRReviews == nil {
		f.PRReviews = make(map[string][]PullRequestReview)
	}
	f.PRReviews[key] = append(f.PRReviews[key], review)
	return nil
}

func (f *FakeClient) ListPullRequestReviews(_ context.Context, owner, repo string, number int) ([]PullRequestReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListPullRequestReviews"); e != nil {
		return nil, e
	}
	if f.PRReviews != nil {
		key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
		if reviews, ok := f.PRReviews[key]; ok {
			return reviews, nil
		}
	}
	return nil, nil
}

func (f *FakeClient) DismissPullRequestReview(_ context.Context, owner, repo string, number, reviewID int, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("DismissPullRequestReview"); e != nil {
		return e
	}
	f.DismissedReviews = append(f.DismissedReviews, DismissedReviewRecord{
		Owner:    owner,
		Repo:     repo,
		Number:   number,
		ReviewID: reviewID,
		Message:  message,
	})
	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	if f.PRReviews != nil {
		for i, r := range f.PRReviews[key] {
			if r.ID == reviewID {
				f.PRReviews[key][i].State = "DISMISSED"
				break
			}
		}
	}
	return nil
}

func (f *FakeClient) MergeChangeProposal(_ context.Context, _, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err("MergeChangeProposal")
}

func (f *FakeClient) UpdatePullRequestBranch(_ context.Context, _, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err("UpdatePullRequestBranch")
}

func (f *FakeClient) ListWorkflowRuns(_ context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListWorkflowRuns"); e != nil {
		return nil, e
	}
	key := owner + "/" + repo + "/" + workflowFile
	if f.WorkflowRunsList != nil {
		if runs, ok := f.WorkflowRunsList[key]; ok {
			return append([]WorkflowRun(nil), runs...), nil
		}
	}
	if run, ok := f.WorkflowRuns[key]; ok {
		return []WorkflowRun{*run}, nil
	}
	return nil, nil
}

// ListWorkflowRunsSince returns the same configured runs as ListWorkflowRuns,
// filtered to those created at or after since. Unlike the live GitHub
// client's ListWorkflowRuns (capped at per_page=10), FakeClient never
// truncates its configured list, so this filters rather than paginates —
// tests configure WorkflowRunsList directly with as many runs as a scenario
// needs, including more than a single live-API page, to exercise
// since-bounded selection (#7996 review).
func (f *FakeClient) ListWorkflowRunsSince(ctx context.Context, owner, repo, workflowFile string, since time.Time) ([]WorkflowRun, error) {
	f.mu.Lock()
	e := f.err("ListWorkflowRunsSince")
	f.mu.Unlock()
	if e != nil {
		return nil, e
	}
	runs, err := f.ListWorkflowRuns(ctx, owner, repo, workflowFile)
	if err != nil {
		return nil, err
	}
	var matched []WorkflowRun
	for _, run := range runs {
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(since) {
			continue
		}
		matched = append(matched, run)
	}
	return matched, nil
}

func (f *FakeClient) ListWorkflowRunJobs(_ context.Context, _, _ string, runID int) ([]WorkflowJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListWorkflowRunJobs"); e != nil {
		return nil, e
	}
	if f.WorkflowRunJobs == nil {
		return nil, nil
	}
	return append([]WorkflowJob(nil), f.WorkflowRunJobs[runID]...), nil
}

func (f *FakeClient) ListWorkflowRunArtifacts(_ context.Context, _, _ string, runID int) ([]WorkflowArtifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListWorkflowRunArtifacts"); e != nil {
		return nil, e
	}
	if f.WorkflowRunArtifacts == nil {
		return nil, nil
	}
	return append([]WorkflowArtifact(nil), f.WorkflowRunArtifacts[runID]...), nil
}

func (f *FakeClient) ListRecentWorkflowRuns(_ context.Context, owner, repo string, perPage int) ([]WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListRecentWorkflowRuns"); e != nil {
		return nil, e
	}
	key := owner + "/" + repo
	runs := f.RecentWorkflowRuns[key]
	if perPage > 0 && len(runs) > perPage {
		runs = runs[:perPage]
	}
	return append([]WorkflowRun(nil), runs...), nil
}

func (f *FakeClient) DownloadWorkflowRunArtifact(_ context.Context, _, _ string, artifactID int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("DownloadWorkflowRunArtifact"); e != nil {
		return nil, e
	}
	if f.WorkflowArtifactContents == nil {
		return nil, ErrNotFound
	}
	data, ok := f.WorkflowArtifactContents[artifactID]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (f *FakeClient) ListRepositoryArtifacts(_ context.Context, owner, repo string, perPage int) ([]RepositoryArtifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListRepositoryArtifacts"); e != nil {
		return nil, e
	}
	key := owner + "/" + repo
	arts := f.RepositoryArtifacts[key]
	if perPage > 0 && len(arts) > perPage {
		arts = arts[:perPage]
	}
	return append([]RepositoryArtifact(nil), arts...), nil
}

func (f *FakeClient) GetWorkflowRunLogs(_ context.Context, _, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetWorkflowRunLogs"); e != nil {
		return "", e
	}
	return "[fake workflow logs]", nil
}

func (f *FakeClient) GetWorkflowRunAnnotations(_ context.Context, _, _ string, _ int) ([]Annotation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetWorkflowRunAnnotations"); e != nil {
		return nil, e
	}
	return f.Annotations, nil
}

func (f *FakeClient) ListOrgInstallations(_ context.Context, _ string) ([]Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListOrgInstallations"); e != nil {
		return nil, e
	}

	return f.Installations, nil
}

func (f *FakeClient) GetAppClientID(_ context.Context, slug string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetAppClientID"); e != nil {
		return "", e
	}

	if f.AppClientIDs != nil {
		if id, ok := f.AppClientIDs[slug]; ok {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: app %s", ErrNotFound, slug)
}

func (f *FakeClient) GetCollaboratorPermission(_ context.Context, owner, repo, username string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetCollaboratorPermission"); e != nil {
		return "", e
	}

	key := owner + "/" + repo + "/" + username
	if f.CollaboratorPermissions != nil {
		if role, ok := f.CollaboratorPermissions[key]; ok {
			return role, nil
		}
	}
	return "", ErrNotFound
}

func (f *FakeClient) AddCollaborator(_ context.Context, owner, repo, username, permission string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("AddCollaborator"); e != nil {
		return e
	}

	if f.AddedCollaborators == nil {
		f.AddedCollaborators = make(map[string]string)
	}
	f.AddedCollaborators[owner+"/"+repo+"/"+username] = permission
	return nil
}

func (f *FakeClient) GetOrgMembership(_ context.Context, org, username string) (OrgMembership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetOrgMembership"); e != nil {
		return OrgMembership{}, e
	}

	key := org + "/" + username
	if f.OrgMemberships != nil {
		if m, ok := f.OrgMemberships[key]; ok {
			return m, nil
		}
	}
	return OrgMembership{}, ErrNotFound
}

func (f *FakeClient) OrgSecretExists(_ context.Context, org, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("OrgSecretExists"); e != nil {
		return false, e
	}

	if f.OrgSecrets == nil {
		return false, nil
	}
	return f.OrgSecrets[org+"/"+name], nil
}

func (f *FakeClient) DeleteOrgSecret(_ context.Context, org, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteOrgSecret"); e != nil {
		return e
	}

	f.DeletedOrgSecrets = append(f.DeletedOrgSecrets, org+"/"+name)
	return nil
}

func (f *FakeClient) CreateOrUpdateOrgVariableAll(_ context.Context, org, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrUpdateOrgVariableAll"); e != nil {
		return e
	}

	f.CreatedOrgVariables = append(f.CreatedOrgVariables, OrgVariableRecord{
		Org:   org,
		Name:  name,
		Value: value,
	})

	if f.OrgVariables == nil {
		f.OrgVariables = make(map[string]bool)
	}
	f.OrgVariables[org+"/"+name] = true

	if f.OrgVariableValues == nil {
		f.OrgVariableValues = make(map[string]string)
	}
	f.OrgVariableValues[org+"/"+name] = value
	return nil
}

func (f *FakeClient) GetOrgVariable(_ context.Context, org, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetOrgVariable"); e != nil {
		return "", false, e
	}

	key := org + "/" + name
	if f.OrgVariables == nil || !f.OrgVariables[key] {
		return "", false, nil
	}
	if f.OrgVariableValues == nil {
		return "", true, nil
	}
	return f.OrgVariableValues[key], true, nil
}

// ListInstanceVariables returns the instance-level CI/CD variable names
// configured in InstanceVariables.
func (f *FakeClient) ListInstanceVariables(_ context.Context) ([]OrgVariable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListInstanceVariables"); e != nil {
		return nil, e
	}
	out := make([]OrgVariable, 0, len(f.InstanceVariables))
	for _, name := range f.InstanceVariables {
		out = append(out, OrgVariable{Name: name})
	}
	return out, nil
}

func (f *FakeClient) ListOrgVariables(_ context.Context, org string) ([]OrgVariable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListOrgVariables"); e != nil {
		return nil, e
	}

	prefix := org + "/"
	var out []OrgVariable
	for key, ok := range f.OrgVariables {
		if !ok || !strings.HasPrefix(key, prefix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		val := ""
		if f.OrgVariableValues != nil {
			val = f.OrgVariableValues[key]
		}
		out = append(out, OrgVariable{Name: name, Value: val})
	}
	return out, nil
}

func (f *FakeClient) DeleteOrgVariable(_ context.Context, org, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteOrgVariable"); e != nil {
		return e
	}

	f.DeletedOrgVariables = append(f.DeletedOrgVariables, org+"/"+name)
	delete(f.OrgVariables, org+"/"+name)
	return nil
}

func (f *FakeClient) IsProtectedBranch(_ context.Context, owner, repo, branch string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("IsProtectedBranch"); e != nil {
		return false, e
	}

	key := owner + "/" + repo + "/" + branch
	if _, ok := f.ProtectedBranchRules[key]; ok {
		return true, nil
	}
	return f.ProtectedBranches[key], nil
}

func cloneProtectedBranchRule(rule *ProtectedBranchRule) *ProtectedBranchRule {
	if rule == nil {
		return nil
	}
	out := *rule
	if rule.PushAccessLevels != nil {
		out.PushAccessLevels = append([]ProtectedBranchAccess(nil), rule.PushAccessLevels...)
	}
	if rule.MergeAccessLevels != nil {
		out.MergeAccessLevels = append([]ProtectedBranchAccess(nil), rule.MergeAccessLevels...)
	}
	return &out
}

func (f *FakeClient) GetProtectedBranch(_ context.Context, owner, repo, branch string) (*ProtectedBranchRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetProtectedBranch"); e != nil {
		return nil, e
	}

	key := owner + "/" + repo + "/" + branch
	if rule, ok := f.ProtectedBranchRules[key]; ok {
		return cloneProtectedBranchRule(rule), nil
	}
	if f.ProtectedBranches[key] {
		// Bool-only protection matches GitLab's default Protected preset:
		// Developers can merge, so a Developer-level poller can create pipelines.
		return &ProtectedBranchRule{
			Name:              branch,
			MergeAccessLevels: []ProtectedBranchAccess{{AccessLevel: GitLabAccessLevelDeveloper}},
		}, nil
	}
	return nil, nil
}

func (f *FakeClient) ListProtectedBranches(_ context.Context, owner, repo string) ([]ProtectedBranchRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListProtectedBranches"); e != nil {
		return nil, e
	}

	prefix := owner + "/" + repo + "/"
	seen := map[string]bool{}
	var out []ProtectedBranchRule
	for key, rule := range f.ProtectedBranchRules {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok || rule == nil {
			continue
		}
		seen[name] = true
		r := *cloneProtectedBranchRule(rule)
		r.Name = name
		out = append(out, r)
	}
	for key, protected := range f.ProtectedBranches {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok || !protected || seen[name] {
			continue
		}
		out = append(out, ProtectedBranchRule{
			Name:              name,
			MergeAccessLevels: []ProtectedBranchAccess{{AccessLevel: GitLabAccessLevelDeveloper}},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *FakeClient) ListProtectedTags(_ context.Context, owner, repo string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListProtectedTags"); e != nil {
		return nil, e
	}

	prefix := owner + "/" + repo + "/"
	var out []string
	for key, protected := range f.ProtectedTags {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok || !protected {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (f *FakeClient) GrantProtectedBranchMergeUser(_ context.Context, owner, repo, branch string, userID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.GrantedProtectedBranchMergeUsers = append(f.GrantedProtectedBranchMergeUsers, ProtectedBranchMergeGrantRecord{
		Owner: owner, Repo: repo, Branch: branch, UserID: userID,
	})

	if e := f.err("GrantProtectedBranchMergeUser"); e != nil {
		return e
	}
	if userID <= 0 {
		return fmt.Errorf("grant protected branch merge: invalid user ID %d", userID)
	}

	key := owner + "/" + repo + "/" + branch
	rule := f.ProtectedBranchRules[key]
	if rule == nil && f.ProtectedBranches[key] {
		rule = &ProtectedBranchRule{
			Name:              branch,
			MergeAccessLevels: []ProtectedBranchAccess{{AccessLevel: GitLabAccessLevelDeveloper}},
		}
	}
	if rule == nil {
		return fmt.Errorf("grant protected branch merge: %s is not protected", branch)
	}
	for _, l := range rule.MergeAccessLevels {
		if l.UserID == userID {
			f.ProtectedBranchRules[key] = rule
			return nil
		}
	}
	for _, l := range rule.PushAccessLevels {
		if l.UserID == userID {
			f.ProtectedBranchRules[key] = rule
			return nil
		}
	}
	rule.MergeAccessLevels = append(rule.MergeAccessLevels, ProtectedBranchAccess{UserID: userID})
	f.ProtectedBranchRules[key] = rule
	return nil
}

func (f *FakeClient) CreatePipeline(_ context.Context, owner, repo, ref string, variables map[string]string) (*Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	vars := make(map[string]string, len(variables))
	for k, v := range variables {
		vars[k] = v
	}
	f.PipelineCalls = append(f.PipelineCalls, PipelineCallRecord{
		Owner:     owner,
		Repo:      repo,
		Ref:       ref,
		Variables: vars,
	})

	if e := f.err("CreatePipeline"); e != nil {
		return nil, e
	}

	p := Pipeline{
		ID:     int64(len(f.CreatedPipelines) + 1),
		WebURL: fmt.Sprintf("https://gitlab.example.com/-/pipelines/%d", len(f.CreatedPipelines)+1),
	}
	f.CreatedPipelines = append(f.CreatedPipelines, p)
	return &p, nil
}

// CreatePipelineWithInputs records the call and returns a fake pipeline,
// mirroring CreatePipeline's behavior but without ever touching the
// variables-based call record — callers can use PipelineInputsCalls to
// assert that no user-defined variables were required for a dispatch.
func (f *FakeClient) CreatePipelineWithInputs(_ context.Context, owner, repo, ref string, inputs map[string]PipelineInputValue) (*Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	in := make(map[string]PipelineInputValue, len(inputs))
	for k, v := range inputs {
		in[k] = v
	}
	f.PipelineInputsCalls = append(f.PipelineInputsCalls, PipelineInputsCallRecord{
		Owner:  owner,
		Repo:   repo,
		Ref:    ref,
		Inputs: in,
	})

	if e := f.err("CreatePipelineWithInputs"); e != nil {
		return nil, e
	}

	p := Pipeline{
		ID:     int64(len(f.CreatedPipelines) + 1),
		WebURL: fmt.Sprintf("https://gitlab.example.com/-/pipelines/%d", len(f.CreatedPipelines)+1),
	}
	f.CreatedPipelines = append(f.CreatedPipelines, p)
	return &p, nil
}

func (f *FakeClient) CreatePipelineSchedule(_ context.Context, owner, repo, ref, description, cron string, variables map[string]string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.CreatePipelineScheduleErrSeq) > 0 {
		e := f.CreatePipelineScheduleErrSeq[0]
		f.CreatePipelineScheduleErrSeq = f.CreatePipelineScheduleErrSeq[1:]
		if e != nil {
			return 0, e
		}
	} else if e := f.err("CreatePipelineSchedule"); e != nil {
		return 0, e
	}

	s := PipelineSchedule{
		ID:          int64(len(f.CreatedSchedules) + 1),
		Description: description,
		Ref:         ref,
		Cron:        cron,
		Active:      true,
		Variables:   variables,
	}
	f.CreatedSchedules = append(f.CreatedSchedules, s)
	key := owner + "/" + repo
	if f.PipelineSchedules == nil {
		f.PipelineSchedules = make(map[string][]PipelineSchedule)
	}
	f.PipelineSchedules[key] = append(f.PipelineSchedules[key], s)
	return s.ID, nil
}

func (f *FakeClient) DeletePipelineSchedule(_ context.Context, owner, repo string, scheduleID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeletePipelineSchedule"); e != nil {
		return e
	}

	f.DeletedScheduleIDs = append(f.DeletedScheduleIDs, scheduleID)
	key := owner + "/" + repo
	if f.PipelineSchedules != nil {
		schedules := f.PipelineSchedules[key]
		filtered := schedules[:0]
		for _, s := range schedules {
			if s.ID != scheduleID {
				filtered = append(filtered, s)
			}
		}
		f.PipelineSchedules[key] = filtered
	}
	return nil
}

func (f *FakeClient) GetPipelineSchedule(_ context.Context, owner, repo string, scheduleID int64) (*PipelineSchedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("GetPipelineSchedule"); err != nil {
		return nil, err
	}
	for _, schedule := range f.PipelineSchedules[owner+"/"+repo] {
		if schedule.ID == scheduleID {
			result := schedule
			result.Variables = make(map[string]string, len(schedule.Variables))
			for key, value := range schedule.Variables {
				result.Variables[key] = value
			}
			return &result, nil
		}
	}
	return nil, ErrNotFound
}

func (f *FakeClient) DeletePipelineScheduleVariable(_ context.Context, owner, repo string, scheduleID int64, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("DeletePipelineScheduleVariable"); err != nil {
		return err
	}
	for _, schedule := range f.PipelineSchedules[owner+"/"+repo] {
		if schedule.ID == scheduleID {
			delete(schedule.Variables, key)
			return nil
		}
	}
	return ErrNotFound
}

func (f *FakeClient) UpdatePipelineSchedule(_ context.Context, owner, repo string, scheduleID int64, active bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("UpdatePipelineSchedule"); e != nil {
		return e
	}

	key := owner + "/" + repo
	schedules := f.PipelineSchedules[key]
	for i := range schedules {
		if schedules[i].ID == scheduleID {
			schedules[i].Active = active
			f.PipelineSchedules[key] = schedules
			f.UpdatedScheduleIDs = append(f.UpdatedScheduleIDs, scheduleID)
			return nil
		}
	}
	return fmt.Errorf("%w: pipeline schedule %d", ErrNotFound, scheduleID)
}

func (f *FakeClient) ListPipelineSchedules(_ context.Context, owner, repo string) ([]PipelineSchedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListPipelineSchedules"); e != nil {
		return nil, e
	}

	return f.PipelineSchedules[owner+"/"+repo], nil
}

func (f *FakeClient) UpdateCIVariable(_ context.Context, owner, repo, name, value string, protected bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("UpdateCIVariable"); e != nil {
		return e
	}

	f.UpdatedVariables = append(f.UpdatedVariables, VariableRecord{
		Owner:     owner,
		Repo:      repo,
		Name:      name,
		Value:     value,
		Protected: protected,
	})
	if f.VariableValues == nil {
		f.VariableValues = make(map[string]string)
	}
	f.VariableValues[owner+"/"+repo+"/"+name] = value
	if f.VariablesExist == nil {
		f.VariablesExist = make(map[string]bool)
	}
	f.VariablesExist[owner+"/"+repo+"/"+name] = true
	return nil
}

func (f *FakeClient) CreateProtectedCIVariable(_ context.Context, owner, repo, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateProtectedCIVariable"); e != nil {
		return e
	}

	f.CreatedProtectedVars = append(f.CreatedProtectedVars, VariableRecord{
		Owner:     owner,
		Repo:      repo,
		Name:      name,
		Value:     value,
		Protected: true,
	})
	if f.VariableValues == nil {
		f.VariableValues = make(map[string]string)
	}
	f.VariableValues[owner+"/"+repo+"/"+name] = value
	if f.VariablesExist == nil {
		f.VariablesExist = make(map[string]bool)
	}
	f.VariablesExist[owner+"/"+repo+"/"+name] = true
	return nil
}

func (f *FakeClient) GetPipelineVariablesMinimumOverrideRole(_ context.Context, owner, repo string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetPipelineVariablesMinimumOverrideRole"); e != nil {
		return "", e
	}
	return f.PipelineVarOverrideRoles[owner+"/"+repo], nil
}

func (f *FakeClient) SetPipelineVariablesMinimumOverrideRole(_ context.Context, owner, repo, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("SetPipelineVariablesMinimumOverrideRole"); e != nil {
		return e
	}
	if err := ValidatePipelineVarOverrideRole(role); err != nil {
		return err
	}
	if f.PipelineVarOverrideRoles == nil {
		f.PipelineVarOverrideRoles = make(map[string]string)
	}
	f.PipelineVarOverrideRoles[owner+"/"+repo] = role
	return nil
}

func (f *FakeClient) CompareCommits(_ context.Context, owner, repo, base, head string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CompareCommits"); e != nil {
		return "", e
	}

	if f.CommitAncestry != nil {
		key := owner + "/" + repo + "/" + base + "/" + head
		if status, ok := f.CommitAncestry[key]; ok {
			return status, nil
		}
	}
	return "", fmt.Errorf("%w: no comparison data for %s/%s %s...%s", ErrNotFound, owner, repo, base, head)
}

func (f *FakeClient) CreatePipelineTriggerToken(_ context.Context, owner, repo, description string) (*PipelineTriggerToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreatePipelineTriggerToken"); e != nil {
		return nil, e
	}

	f.triggerTokenSeq++
	tok := PipelineTriggerToken{
		ID:          f.triggerTokenSeq,
		Description: description,
		Token:       fmt.Sprintf("glptt-fake-%d", f.triggerTokenSeq),
		OwnerID:     f.TriggerTokenOwnerID,
	}
	f.CreatedTriggerTokens = append(f.CreatedTriggerTokens, tok)
	key := owner + "/" + repo
	if f.PipelineTriggerTokens == nil {
		f.PipelineTriggerTokens = make(map[string][]PipelineTriggerToken)
	}
	stored := tok
	stored.Token = "" // GitLab omits the secret after creation.
	f.PipelineTriggerTokens[key] = append(f.PipelineTriggerTokens[key], stored)
	return &tok, nil
}

func (f *FakeClient) ListPipelineTriggerTokens(_ context.Context, owner, repo string) ([]PipelineTriggerToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListPipelineTriggerTokens"); e != nil {
		return nil, e
	}
	if f.PipelineTriggerTokens == nil {
		return nil, nil
	}
	src := f.PipelineTriggerTokens[owner+"/"+repo]
	out := make([]PipelineTriggerToken, len(src))
	copy(out, src)
	return out, nil
}

func (f *FakeClient) GetProjectMemberAccessLevel(_ context.Context, _, _ string, userID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetProjectMemberAccessLevel"); e != nil {
		return 0, e
	}
	level, ok := f.ProjectMemberAccess[userID]
	if !ok {
		return 0, fmt.Errorf("%w: project member %d", ErrNotFound, userID)
	}
	return level, nil
}

func (f *FakeClient) RevokePipelineTriggerToken(_ context.Context, owner, repo string, tokenID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("RevokePipelineTriggerToken"); e != nil {
		return e
	}

	f.RevokedTriggerTokenIDs = append(f.RevokedTriggerTokenIDs, tokenID)
	key := owner + "/" + repo
	tokens := f.PipelineTriggerTokens[key]
	filtered := tokens[:0]
	found := false
	for _, tok := range tokens {
		if tok.ID == tokenID {
			found = true
			continue
		}
		filtered = append(filtered, tok)
	}
	if !found {
		return fmt.Errorf("%w: pipeline trigger token %d", ErrNotFound, tokenID)
	}
	f.PipelineTriggerTokens[key] = filtered
	return nil
}

func (f *FakeClient) CreateProjectHook(_ context.Context, owner, repo string, hook ProjectHook) (*ProjectHook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateProjectHook"); e != nil {
		return nil, e
	}

	f.projectHookSeq++
	created := hook
	created.ID = f.projectHookSeq
	f.CreatedProjectHooks = append(f.CreatedProjectHooks, created)
	key := owner + "/" + repo
	if f.ProjectHooks == nil {
		f.ProjectHooks = make(map[string][]ProjectHook)
	}
	stored := created
	stored.Token = "" // GitLab never returns the webhook secret.
	f.ProjectHooks[key] = append(f.ProjectHooks[key], stored)
	return &stored, nil
}

func (f *FakeClient) ListProjectHooks(_ context.Context, owner, repo string) ([]ProjectHook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListProjectHooks"); e != nil {
		return nil, e
	}
	if f.ProjectHooks == nil {
		return nil, nil
	}
	src := f.ProjectHooks[owner+"/"+repo]
	out := make([]ProjectHook, len(src))
	copy(out, src)
	return out, nil
}

func (f *FakeClient) UpdateProjectHook(_ context.Context, owner, repo string, hookID int64, hook ProjectHook) (*ProjectHook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("UpdateProjectHook"); e != nil {
		return nil, e
	}

	key := owner + "/" + repo
	hooks := f.ProjectHooks[key]
	for i, existing := range hooks {
		if existing.ID != hookID {
			continue
		}
		updated := hook
		updated.ID = hookID
		f.UpdatedProjectHooks = append(f.UpdatedProjectHooks, updated)
		stored := updated
		stored.Token = "" // GitLab never returns the webhook secret.
		hooks[i] = stored
		f.ProjectHooks[key] = hooks
		return &stored, nil
	}
	return nil, fmt.Errorf("%w: project hook %d", ErrNotFound, hookID)
}

func (f *FakeClient) DeleteProjectHook(_ context.Context, owner, repo string, hookID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteProjectHook"); e != nil {
		return e
	}

	f.DeletedProjectHookIDs = append(f.DeletedProjectHookIDs, hookID)
	key := owner + "/" + repo
	hooks := f.ProjectHooks[key]
	filtered := hooks[:0]
	found := false
	for _, h := range hooks {
		if h.ID == hookID {
			found = true
			continue
		}
		filtered = append(filtered, h)
	}
	if !found {
		return fmt.Errorf("%w: project hook %d", ErrNotFound, hookID)
	}
	f.ProjectHooks[key] = filtered
	return nil
}
