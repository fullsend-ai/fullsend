package forge

import (
	"context"
	"fmt"
	"sync"
)

// Compile-time check that FakeClient implements Client.
var _ Client = (*FakeClient)(nil)

// NewFakeClient returns a FakeClient with all maps initialised.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		FileContents:   make(map[string][]byte),
		WorkflowRuns:   make(map[string]*WorkflowRun),
		Secrets:        make(map[string]bool),
		VariablesExist: make(map[string]bool),
		VariableValues: make(map[string]string),
		Errors:         make(map[string]error),
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

// OrgSecretRecord records an org-level secret creation call.
type OrgSecretRecord struct {
	Org, Name, Value string
	RepoIDs          []int64
}

// OrgVariableRecord records an org-level variable creation/update call.
type OrgVariableRecord struct {
	Org, Name, Value string
	RepoIDs          []int64
}

// VariableRecord records a variable creation/update call.
type VariableRecord struct {
	Owner, Repo, Name, Value string
}

// UpdatedCommentRecord records an issue comment update call.
type UpdatedCommentRecord struct {
	Owner, Repo string
	CommentID   int
	Body        string
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

// CommitFilesRecord records a CommitFiles call.
type CommitFilesRecord struct {
	Owner, Repo, Message string
	Files                []TreeFile
}

// FakeClient is a thread-safe test double for forge.Client.
// Pre-populate its fields to control return values, and inspect
// recorder slices after the test to verify which calls were made.
type FakeClient struct {
	mu sync.Mutex

	// Pre-populated data
	Repos             []Repository
	FileContents      map[string][]byte       // key: "owner/repo/path"
	WorkflowRuns      map[string]*WorkflowRun // key: "owner/repo/workflow"
	AuthenticatedUser string
	OrgPlan           string // plan name returned by GetOrgPlan (default: "free")
	Installations     []Installation
	Secrets           map[string]bool             // key: "owner/repo/name"
	PullRequests      map[string][]ChangeProposal // key: "owner/repo"
	TokenScopes       []string                    // scopes returned by GetTokenScopes
	VariablesExist    map[string]bool             // key: "owner/repo/name"
	VariableValues    map[string]string           // key: "owner/repo/name"

	// App client IDs for GetAppClientID
	AppClientIDs map[string]string // key: app slug → client ID

	// Org-level secret state
	OrgSecrets       map[string]bool    // key: "org/name"
	OrgSecretRepoIDs map[string][]int64 // key: "org/name" → repo IDs

	// Org-level variable state
	OrgVariables      map[string]bool   // key: "org/name"
	OrgVariableValues map[string]string // key: "org/name" → value

	// Error injection: key is method name, value is error to return.
	Errors map[string]error

	// Issue comments for ListIssueComments / UpdateIssueComment.
	IssueComments map[string][]IssueComment // key: "owner/repo/number"
	OpenIssues    map[string][]Issue        // key: "owner/repo"

	// CommitFilesChanged controls the return value of CommitFiles (default true).
	CommitFilesChanged *bool

	// Pull request head SHA for GetPullRequestHeadSHA.
	PullRequestHeadSHA string

	// Pull request reviews for ListPullRequestReviews.
	PRReviews map[string][]PullRequestReview // key: "owner/repo/number"

	// Call recorders
	CreatedRepos        []Repository
	CreatedFiles        []FileRecord
	CreatedBranches     []string // "owner/repo/branch"
	CreatedProposals    []ChangeProposal
	DeletedRepos        []string // "owner/repo"
	DeletedFiles        []FileRecord
	CreatedSecrets      []SecretRecord
	Variables           []VariableRecord
	DeletedOrgSecrets   []string // "org/name"
	CreatedOrgSecrets   []OrgSecretRecord
	CreatedOrgVariables []OrgVariableRecord
	DeletedOrgVariables []string // "org/name"
	CreatedIssues       []CreatedIssueRecord
	UpdatedComments     []UpdatedCommentRecord
	MinimizedComments   []MinimizedCommentRecord
	CreatedReviews      []ReviewRecord
	DismissedReviews    []DismissedReviewRecord
	CommittedFiles      []CommitFilesRecord

	// internal counters
	proposalCounter int
	commentCounter  int
	issueCounter    int
}

// err checks for an injected error for the given method name.
func (f *FakeClient) err(method string) error {
	if f.Errors == nil {
		return nil
	}
	return f.Errors[method]
}

func (f *FakeClient) ListOrgRepos(_ context.Context, _ string) ([]Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("ListOrgRepos"); e != nil {
		return nil, e
	}

	var result []Repository
	for _, r := range f.Repos {
		if r.Archived || r.Fork || r.Private {
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
		if r.FullName == fullName || r.Name == name {
			return nil, fmt.Errorf("repository already exists: %s", fullName)
		}
	}
	// Check for duplicates in previously created repos.
	for _, r := range f.CreatedRepos {
		if r.FullName == fullName || r.Name == name {
			return nil, fmt.Errorf("repository already exists: %s", fullName)
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

	for i := range f.Repos {
		if f.Repos[i].FullName == owner+"/"+repo || f.Repos[i].Name == repo {
			return &f.Repos[i], nil
		}
	}
	// Also check created repos.
	for i := range f.CreatedRepos {
		if f.CreatedRepos[i].FullName == owner+"/"+repo || f.CreatedRepos[i].Name == repo {
			return &f.CreatedRepos[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, owner, repo)
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
		if r.FullName != fullName && r.Name != repo {
			filtered = append(filtered, r)
		}
	}
	f.Repos = filtered

	// Remove from CreatedRepos.
	filteredCreated := f.CreatedRepos[:0]
	for _, r := range f.CreatedRepos {
		if r.FullName != fullName && r.Name != repo {
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

	if e := f.err("GetFileContent"); e != nil {
		return nil, e
	}

	key := owner + "/" + repo + "/" + path
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

func (f *FakeClient) CommitFiles(_ context.Context, owner, repo, message string, files []TreeFile) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CommitFiles"); e != nil {
		return false, e
	}

	f.CommittedFiles = append(f.CommittedFiles, CommitFilesRecord{
		Owner:   owner,
		Repo:    repo,
		Message: message,
		Files:   files,
	})

	if f.FileContents == nil {
		f.FileContents = make(map[string][]byte)
	}
	for _, file := range files {
		f.FileContents[owner+"/"+repo+"/"+file.Path] = file.Content
	}

	changed := f.CommitFilesChanged == nil || *f.CommitFilesChanged
	return changed, nil
}

func (f *FakeClient) CreateBranch(_ context.Context, owner, repo, branchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateBranch"); e != nil {
		return e
	}

	f.CreatedBranches = append(f.CreatedBranches, owner+"/"+repo+"/"+branchName)
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

func (f *FakeClient) GetTokenScopes(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetTokenScopes"); e != nil {
		return nil, e
	}

	return f.TokenScopes, nil
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

func (f *FakeClient) CreateOrUpdateRepoVariable(_ context.Context, owner, repo, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrUpdateRepoVariable"); e != nil {
		return e
	}

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

func (f *FakeClient) GetPullRequestHeadSHA(_ context.Context, _, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetPullRequestHeadSHA"); e != nil {
		return "", e
	}
	return f.PullRequestHeadSHA, nil
}

func (f *FakeClient) CreatePullRequestReview(_ context.Context, owner, repo string, number int, event, body, commitSHA string, comments []ReviewComment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *FakeClient) ListWorkflowRuns(_ context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("ListWorkflowRuns"); e != nil {
		return nil, e
	}
	key := owner + "/" + repo + "/" + workflowFile
	if run, ok := f.WorkflowRuns[key]; ok {
		return []WorkflowRun{*run}, nil
	}
	return nil, nil
}

func (f *FakeClient) GetWorkflowRunLogs(_ context.Context, _, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("GetWorkflowRunLogs"); e != nil {
		return "", e
	}
	return "[fake workflow logs]", nil
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

func (f *FakeClient) CreateOrgSecret(_ context.Context, org, name, value string, selectedRepoIDs []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrgSecret"); e != nil {
		return e
	}

	f.CreatedOrgSecrets = append(f.CreatedOrgSecrets, OrgSecretRecord{
		Org:     org,
		Name:    name,
		Value:   value,
		RepoIDs: selectedRepoIDs,
	})

	if f.OrgSecrets == nil {
		f.OrgSecrets = make(map[string]bool)
	}
	f.OrgSecrets[org+"/"+name] = true
	return nil
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

func (f *FakeClient) SetOrgSecretRepos(_ context.Context, org, name string, repoIDs []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("SetOrgSecretRepos"); e != nil {
		return e
	}

	if f.OrgSecretRepoIDs == nil {
		f.OrgSecretRepoIDs = make(map[string][]int64)
	}
	f.OrgSecretRepoIDs[org+"/"+name] = repoIDs
	return nil
}

func (f *FakeClient) GetOrgSecretRepos(_ context.Context, org, name string) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("GetOrgSecretRepos"); e != nil {
		return nil, e
	}

	if f.OrgSecretRepoIDs == nil {
		return nil, nil
	}
	return f.OrgSecretRepoIDs[org+"/"+name], nil
}

func (f *FakeClient) CreateOrUpdateOrgVariable(_ context.Context, org, name, value string, selectedRepoIDs []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("CreateOrUpdateOrgVariable"); e != nil {
		return e
	}

	f.CreatedOrgVariables = append(f.CreatedOrgVariables, OrgVariableRecord{
		Org:     org,
		Name:    name,
		Value:   value,
		RepoIDs: selectedRepoIDs,
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

func (f *FakeClient) OrgVariableExists(_ context.Context, org, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("OrgVariableExists"); e != nil {
		return false, e
	}

	if f.OrgVariables == nil {
		return false, nil
	}
	return f.OrgVariables[org+"/"+name], nil
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
