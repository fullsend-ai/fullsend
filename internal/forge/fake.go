package forge

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient is a test double for the forge Client interface.
// It records all calls and returns configurable responses.
//
//nolint:govet // fieldalignment: test fake, readability over alignment
type FakeClient struct {
	Errors            map[string]error        // Errors to inject, keyed by method name.
	Repos             []Repository            // Repos to return from ListOrgRepos.
	CreatedRepos      []createRepoCall        // Tracks calls to CreateRepo.
	CreatedFiles      []createFileCall        // Tracks calls to CreateFile/CreateOrUpdateFile.
	CreatedProposals  []createProposalCall    // Tracks calls to CreateChangeProposal.
	CreatedBranches   []createBranchCall      // Tracks calls to CreateBranch.
	DeletedRepos      []deleteRepoCall        // Tracks calls to DeleteRepo.
	CreatedSecrets    []createSecretCall      // Tracks calls to CreateRepoSecret.
	WorkflowRuns      map[string]*WorkflowRun // Keyed by "owner/repo/workflow".
	AuthenticatedUser string                  // Returned by GetAuthenticatedUser.
	mu                sync.Mutex
	proposalCounter   int
}

type createRepoCall struct {
	Org, Name, Description string
	Private                bool
}

type createFileCall struct {
	Owner, Repo, Branch, Path, Message string
	Content                            []byte
}

type createProposalCall struct {
	Owner, Repo, Title, Body, Head, Base string
}

type createBranchCall struct {
	Owner, Repo, BranchName string
}

type deleteRepoCall struct {
	Owner, Repo string
}

type createSecretCall struct {
	Owner, Repo, Name string
}

// NewFakeClient creates a FakeClient with no pre-configured state.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		Errors:       make(map[string]error),
		WorkflowRuns: make(map[string]*WorkflowRun),
	}
}

// ListOrgRepos implements the Client interface.
func (f *FakeClient) ListOrgRepos(_ context.Context, _ string) ([]Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["ListOrgRepos"]; err != nil {
		return nil, err
	}
	return f.Repos, nil
}

// CreateRepo implements the Client interface.
func (f *FakeClient) CreateRepo(_ context.Context, org, name, description string, private bool) (*Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateRepo"]; err != nil {
		return nil, err
	}

	f.CreatedRepos = append(f.CreatedRepos, createRepoCall{
		Org: org, Name: name, Description: description, Private: private,
	})

	return &Repository{
		Name:          name,
		FullName:      fmt.Sprintf("%s/%s", org, name),
		DefaultBranch: "main",
		Private:       private,
	}, nil
}

// CreateFile implements the Client interface.
func (f *FakeClient) CreateFile(_ context.Context, owner, repo, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateFile"]; err != nil {
		return err
	}

	f.CreatedFiles = append(f.CreatedFiles, createFileCall{
		Owner: owner, Repo: repo, Path: path, Message: message, Content: content,
	})
	return nil
}

// CreateOrUpdateFile implements the Client interface.
// In the fake, it behaves the same as CreateFile — overwrites any existing entry.
func (f *FakeClient) CreateOrUpdateFile(_ context.Context, owner, repo, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateOrUpdateFile"]; err != nil {
		return err
	}

	// Remove existing file with same path if present
	for i, file := range f.CreatedFiles {
		if file.Owner == owner && file.Repo == repo && file.Path == path {
			f.CreatedFiles = append(f.CreatedFiles[:i], f.CreatedFiles[i+1:]...)
			break
		}
	}

	f.CreatedFiles = append(f.CreatedFiles, createFileCall{
		Owner: owner, Repo: repo, Path: path, Message: message, Content: content,
	})
	return nil
}

// CreateChangeProposal implements the Client interface.
func (f *FakeClient) CreateChangeProposal(_ context.Context, owner, repo, title, body, head, base string) (*ChangeProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateChangeProposal"]; err != nil {
		return nil, err
	}

	f.proposalCounter++
	proposal := &ChangeProposal{
		Number: f.proposalCounter,
		URL:    fmt.Sprintf("https://example.com/%s/%s/proposals/%d", owner, repo, f.proposalCounter),
		Title:  title,
	}

	f.CreatedProposals = append(f.CreatedProposals, createProposalCall{
		Owner: owner, Repo: repo, Title: title, Body: body, Head: head, Base: base,
	})

	return proposal, nil
}

// CreateBranch implements the Client interface.
func (f *FakeClient) CreateBranch(_ context.Context, owner, repo, branchName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateBranch"]; err != nil {
		return err
	}

	f.CreatedBranches = append(f.CreatedBranches, createBranchCall{
		Owner: owner, Repo: repo, BranchName: branchName,
	})
	return nil
}

// GetFileContent implements forge.Client.
func (f *FakeClient) GetFileContent(_ context.Context, owner, repo, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["GetFileContent"]; err != nil {
		return nil, err
	}

	// Look through created files for a match
	for _, file := range f.CreatedFiles {
		if file.Owner == owner && file.Repo == repo && file.Path == path {
			return file.Content, nil
		}
	}

	return nil, fmt.Errorf("file not found: %s/%s/%s", owner, repo, path)
}

// DeleteRepo implements forge.Client.
func (f *FakeClient) DeleteRepo(_ context.Context, owner, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["DeleteRepo"]; err != nil {
		return err
	}

	f.DeletedRepos = append(f.DeletedRepos, deleteRepoCall{Owner: owner, Repo: repo})
	return nil
}

// CreateRepoSecret implements the Client interface.
func (f *FakeClient) CreateRepoSecret(_ context.Context, owner, repo, name, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateRepoSecret"]; err != nil {
		return err
	}

	f.CreatedSecrets = append(f.CreatedSecrets, createSecretCall{
		Owner: owner, Repo: repo, Name: name,
	})
	return nil
}

// RepoSecretExists implements the Client interface.
func (f *FakeClient) RepoSecretExists(_ context.Context, owner, repo, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["RepoSecretExists"]; err != nil {
		return false, err
	}

	for _, s := range f.CreatedSecrets {
		if s.Owner == owner && s.Repo == repo && s.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// GetAuthenticatedUser implements the Client interface.
func (f *FakeClient) GetAuthenticatedUser(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["GetAuthenticatedUser"]; err != nil {
		return "", err
	}

	if f.AuthenticatedUser == "" {
		return "testuser", nil
	}
	return f.AuthenticatedUser, nil
}

// CreateFileOnBranch implements the Client interface.
func (f *FakeClient) CreateFileOnBranch(_ context.Context, owner, repo, branch, path, message string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreateFileOnBranch"]; err != nil {
		return err
	}

	f.CreatedFiles = append(f.CreatedFiles, createFileCall{
		Owner: owner, Repo: repo, Branch: branch, Path: path, Message: message, Content: content,
	})
	return nil
}

// GetLatestWorkflowRun implements the Client interface.
func (f *FakeClient) GetLatestWorkflowRun(_ context.Context, owner, repo, workflowFile string) (*WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["GetLatestWorkflowRun"]; err != nil {
		return nil, err
	}

	key := fmt.Sprintf("%s/%s/%s", owner, repo, workflowFile)
	run, ok := f.WorkflowRuns[key]
	if !ok {
		return nil, nil
	}
	return run, nil
}

// GetWorkflowRun implements the Client interface.
func (f *FakeClient) GetWorkflowRun(_ context.Context, owner, repo string, runID int) (*WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["GetWorkflowRun"]; err != nil {
		return nil, err
	}

	for _, run := range f.WorkflowRuns {
		if run.ID == runID {
			return run, nil
		}
	}
	return nil, nil
}

// ListRepoPullRequests implements the Client interface.
func (f *FakeClient) ListRepoPullRequests(_ context.Context, _, _ string) ([]ChangeProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["ListRepoPullRequests"]; err != nil {
		return nil, err
	}

	return []ChangeProposal{}, nil
}
