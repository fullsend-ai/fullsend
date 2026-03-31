package github

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient is a test double for the GitHub Client interface.
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

	// CreatedPRs tracks calls to CreatePullRequest.
	CreatedPRs []createPRCall

	// CreatedBranches tracks calls to CreateBranch.
	CreatedBranches []createBranchCall

	mu        sync.Mutex
	prCounter int
}

type createRepoCall struct {
	Org, Name, Description string
	Private                bool
}

type createFileCall struct {
	Owner, Repo, Branch, Path, Message string
	Content                            []byte
}

type createPRCall struct {
	Owner, Repo, Title, Body, Head, Base string
}

type createBranchCall struct {
	Owner, Repo, BranchName string
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

// CreatePullRequest implements the Client interface.
func (f *FakeClient) CreatePullRequest(_ context.Context, owner, repo, title, body, head, base string) (*PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.Errors["CreatePullRequest"]; err != nil {
		return nil, err
	}

	f.prCounter++
	pr := &PullRequest{
		Number:  f.prCounter,
		HTMLURL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, f.prCounter),
		Title:   title,
	}

	f.CreatedPRs = append(f.CreatedPRs, createPRCall{
		Owner: owner, Repo: repo, Title: title, Body: body, Head: head, Base: base,
	})

	return pr, nil
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
