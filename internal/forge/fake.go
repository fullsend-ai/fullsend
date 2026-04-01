package forge

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient is a test double for the forge Client interface.
// It records all calls and returns configurable responses.
type FakeClient struct {
	// Errors to inject, keyed by method name.
	Errors map[string]error

	// Repos to return from ListOrgRepos.
	Repos []Repository

	// CreatedRepos tracks calls to CreateRepo.
	CreatedRepos []createRepoCall

	// CreatedFiles tracks calls to CreateFile and CreateFileOnBranch.
	CreatedFiles []createFileCall

	// CreatedProposals tracks calls to CreateChangeProposal.
	CreatedProposals []createProposalCall

	// CreatedBranches tracks calls to CreateBranch.
	CreatedBranches []createBranchCall

	// DeletedRepos tracks calls to DeleteRepo.
	DeletedRepos []deleteRepoCall

	mu              sync.Mutex
	proposalCounter int
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

// NewFakeClient creates a FakeClient with no pre-configured state.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		Errors: make(map[string]error),
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
