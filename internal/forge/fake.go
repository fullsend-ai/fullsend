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

// VariableRecord records a variable creation/update call.
type VariableRecord struct {
	Owner, Repo, Name, Value string
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
	Installations     []Installation
	Secrets           map[string]bool             // key: "owner/repo/name"
	PullRequests      map[string][]ChangeProposal // key: "owner/repo"
	TokenScopes       []string                    // scopes returned by GetTokenScopes
	VariablesExist    map[string]bool             // key: "owner/repo/name"

	// Org-level secret state
	OrgSecrets       map[string]bool    // key: "org/name"
	OrgSecretRepoIDs map[string][]int64 // key: "org/name" → repo IDs

	// Error injection: key is method name, value is error to return.
	Errors map[string]error

	// Call recorders
	CreatedRepos      []Repository
	CreatedFiles      []FileRecord
	CreatedBranches   []string // "owner/repo/branch"
	CreatedProposals  []ChangeProposal
	DeletedRepos      []string // "owner/repo"
	CreatedSecrets    []SecretRecord
	Variables         []VariableRecord
	DeletedOrgSecrets []string // "org/name"
	CreatedOrgSecrets []OrgSecretRecord

	// internal counter for change proposal numbers
	proposalCounter int
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
		if r.Archived || r.Fork {
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

func (f *FakeClient) DeleteFile(_ context.Context, owner, repo, path, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e := f.err("DeleteFile"); e != nil {
		return e
	}

	key := owner + "/" + repo + "/" + path
	delete(f.FileContents, key)
	return nil
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

func (f *FakeClient) CreateIssue(_ context.Context, _, _, _, _ string) (*Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.err("CreateIssue"); e != nil {
		return nil, e
	}
	return &Issue{Number: 1, Title: "fake", URL: "https://fake"}, nil
}

func (f *FakeClient) CloseIssue(_ context.Context, _, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err("CloseIssue")
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
