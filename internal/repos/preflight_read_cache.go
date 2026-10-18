package repos

import (
	"context"
	"slices"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// NewPreflightReadCache wraps clients so that repeated repository-state
// reads return the first answer instead of calling the forge again: file
// contents on the default branch, repository variables, secret existence
// and org repository listings (#8501). Every other forge.Client method is
// passed through unchanged.
//
// The cache never observes a write it did not make, so it is only correct
// for a window in which nothing writes to the forge. `repos install` uses
// one for its read-only configuration preflights (ownership, install
// defaults, proposed safety, effective infrastructure) and discards it
// before the manifest is written; convergence keeps reading current state
// through the original clients. Successful reads and not-found answers are
// cached; any other error is returned uncached.
func NewPreflightReadCache(clients ForgeClientFactory) ForgeClientFactory {
	return &preflightReadCache{clients: clients, byForge: make(map[string]*cachingReadClient)}
}

type preflightReadCache struct {
	clients ForgeClientFactory

	mu      sync.Mutex
	byForge map[string]*cachingReadClient
}

// ConfigFor returns the wrapped factory's ForgeConfig with its client
// replaced by a caching client shared by every call for the same forge
// (the wrapped factory caches its clients per forge too). Factory errors
// are returned as-is and not cached.
func (c *preflightReadCache) ConfigFor(forgeName string) (ForgeConfig, error) {
	fc, err := c.clients.ConfigFor(forgeName)
	if err != nil {
		return fc, err
	}
	if fc.Client == nil {
		return fc, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.byForge[forgeName]
	if !ok {
		cached = newCachingReadClient(fc.Client)
		c.byForge[forgeName] = cached
	}
	fc.Client = cached
	return fc, nil
}

type fileReadResult struct {
	data []byte
	err  error
}

type variableReadResult struct {
	value  string
	exists bool
}

type orgReposKey struct {
	org            string
	includePrivate bool
}

// cachingReadClient memoizes the repository-state reads of the embedded
// client. See NewPreflightReadCache for when that is safe.
type cachingReadClient struct {
	forge.Client

	mu        sync.Mutex
	files     map[[3]string]fileReadResult
	variables map[[3]string]variableReadResult
	secrets   map[[3]string]bool
	orgRepos  map[orgReposKey][]forge.Repository
}

func newCachingReadClient(client forge.Client) *cachingReadClient {
	return &cachingReadClient{
		Client:    client,
		files:     make(map[[3]string]fileReadResult),
		variables: make(map[[3]string]variableReadResult),
		secrets:   make(map[[3]string]bool),
		orgRepos:  make(map[orgReposKey][]forge.Repository),
	}
}

// GetFileContent caches content and not-found answers per owner, repo and
// path. Each caller receives its own copy of the content.
func (c *cachingReadClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	key := [3]string{owner, repo, path}
	c.mu.Lock()
	r, ok := c.files[key]
	c.mu.Unlock()
	if ok {
		return slices.Clone(r.data), r.err
	}
	data, err := c.Client.GetFileContent(ctx, owner, repo, path)
	if err != nil && !forge.IsNotFound(err) {
		return data, err
	}
	c.mu.Lock()
	c.files[key] = fileReadResult{data: slices.Clone(data), err: err}
	c.mu.Unlock()
	return data, err
}

// GetRepoVariable caches the value and existence per owner, repo and name.
func (c *cachingReadClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	key := [3]string{owner, repo, name}
	c.mu.Lock()
	r, ok := c.variables[key]
	c.mu.Unlock()
	if ok {
		return r.value, r.exists, nil
	}
	value, exists, err := c.Client.GetRepoVariable(ctx, owner, repo, name)
	if err != nil {
		return value, exists, err
	}
	c.mu.Lock()
	c.variables[key] = variableReadResult{value: value, exists: exists}
	c.mu.Unlock()
	return value, exists, nil
}

// RepoSecretExists caches existence per owner, repo and name.
func (c *cachingReadClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	key := [3]string{owner, repo, name}
	c.mu.Lock()
	exists, ok := c.secrets[key]
	c.mu.Unlock()
	if ok {
		return exists, nil
	}
	exists, err := c.Client.RepoSecretExists(ctx, owner, repo, name)
	if err != nil {
		return exists, err
	}
	c.mu.Lock()
	c.secrets[key] = exists
	c.mu.Unlock()
	return exists, nil
}

// ListOrgRepos caches the listing per org and visibility, so each preflight
// expanding the same glob reuses one listing. Each caller receives its own
// copy of the slice.
func (c *cachingReadClient) ListOrgRepos(ctx context.Context, org string, includePrivate bool) ([]forge.Repository, error) {
	key := orgReposKey{org: org, includePrivate: includePrivate}
	c.mu.Lock()
	r, ok := c.orgRepos[key]
	c.mu.Unlock()
	if ok {
		return slices.Clone(r), nil
	}
	repos, err := c.Client.ListOrgRepos(ctx, org, includePrivate)
	if err != nil {
		return repos, err
	}
	c.mu.Lock()
	c.orgRepos[key] = slices.Clone(repos)
	c.mu.Unlock()
	return repos, nil
}
