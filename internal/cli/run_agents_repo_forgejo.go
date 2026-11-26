package cli

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/forgejo"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Environment variables that configure the Forgejo agent-definition source:
// the repository on the selected Forgejo instance that built-in agent
// harnesses, and the companion files they reference, are read from instead
// of fullsend-ai/agents on GitHub.
const (
	// forgejoURLEnv is the base URL of the selected Forgejo instance. It
	// may include a path prefix. https is required, except that plain
	// http is accepted for a loopback host (the local development stack).
	forgejoURLEnv = "FULLSEND_FORGEJO_URL"
	// forgejoAgentsRepoEnv is the owner/repo path of the agent-definition
	// repository on that instance; config.DefaultAgentsRepo when unset.
	forgejoAgentsRepoEnv = "FULLSEND_FORGEJO_AGENTS_REPO"
	// forgejoAgentsRefEnv is the full commit SHA the definitions are
	// pinned to. Branches, tags, and abbreviated SHAs are rejected.
	forgejoAgentsRefEnv = "FULLSEND_FORGEJO_AGENTS_REF"
	// forgejoTokenEnv is the optional API token sent to the instance; a
	// public repository is read anonymously without it.
	forgejoTokenEnv = "FORGEJO_TOKEN"
)

// forgejoCommitSHAPattern matches a full SHA-1 or SHA-256 commit ID.
var forgejoCommitSHAPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// maxForgejoTreeEntries bounds a single tree listing of the source.
const maxForgejoTreeEntries = 4 * gitfetch.MaxFiles

// forgejoGitReader is the part of *forgejo.LiveClient the source uses.
type forgejoGitReader interface {
	GetCommitSHA(ctx context.Context, owner, repo, sha string) (string, error)
	GetTree(ctx context.Context, owner, repo, sha string, recursive bool, maxEntries int) ([]forgejo.TreeEntry, error)
	GetBlob(ctx context.Context, owner, repo, sha string, limit int64) ([]byte, error)
}

// forgejoAgentsSource is a repository on a Forgejo instance pinned to one
// commit. Every read walks git objects from that commit by object ID
// (forgejo.LiveClient's git data API), so a branch or tag can never stand
// in for the pinned revision, and every blob is checked against its object
// ID. The client talks only to the configured instance and refuses
// redirects, so the token is never sent to another origin.
type forgejoAgentsSource struct {
	baseURL string // without a trailing slash
	owner   string
	repo    string
	sha     string
	client  forgejoGitReader
}

// forgejoAgentsSourceFn returns the configured Forgejo agent-definition
// source, nil when none is configured. It is a test seam.
var forgejoAgentsSourceFn = func() (*forgejoAgentsSource, error) {
	return forgejoAgentsSourceFromEnv(os.Getenv, nil)
}

// forgejoAgentsSourceFromEnv reads the source from the environment. It
// returns nil, nil when none of forgejoURLEnv, forgejoAgentsRepoEnv, and
// forgejoAgentsRefEnv is set. Once any is set the source must be complete
// and valid: the error then stops resolution rather than letting it fall
// back to GitHub.
func forgejoAgentsSourceFromEnv(getenv func(string) string, httpClient *http.Client) (*forgejoAgentsSource, error) {
	base := strings.TrimSpace(getenv(forgejoURLEnv))
	repoPath := strings.TrimSpace(getenv(forgejoAgentsRepoEnv))
	ref := strings.TrimSpace(getenv(forgejoAgentsRefEnv))
	if base == "" && repoPath == "" && ref == "" {
		return nil, nil
	}
	if base == "" {
		return nil, fmt.Errorf("%s must name the Forgejo instance that hosts agent definitions when %s or %s is set", forgejoURLEnv, forgejoAgentsRepoEnv, forgejoAgentsRefEnv)
	}
	if err := repos.ValidateForgejoURL(forgejoURLEnv, base); err != nil {
		return nil, err
	}
	if repoPath == "" {
		repoPath = config.DefaultAgentsRepo
	}
	owner, repo, ok := repos.SplitForgejoPath(repoPath)
	if !ok {
		return nil, fmt.Errorf("%s %q must be an owner/repo path on the Forgejo instance", forgejoAgentsRepoEnv, repoPath)
	}
	if ref == "" {
		return nil, fmt.Errorf("%s must be set to the full commit SHA of %s/%s that agent definitions are pinned to", forgejoAgentsRefEnv, owner, repo)
	}
	if !forgejoCommitSHAPattern.MatchString(ref) {
		return nil, fmt.Errorf("%s %q is not a full lowercase commit SHA: agent definitions resolve only from an immutable pinned commit, not a branch, tag, or abbreviated SHA", forgejoAgentsRefEnv, ref)
	}
	client, err := forgejo.New(base, getenv(forgejoTokenEnv), httpClient)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", forgejoURLEnv, err)
	}
	return &forgejoAgentsSource{
		baseURL: strings.TrimRight(base, "/"),
		owner:   owner,
		repo:    repo,
		sha:     ref,
		client:  client,
	}, nil
}

// String names the source as owner/repo@<short sha> for messages.
func (s *forgejoAgentsSource) String() string {
	return s.owner + "/" + s.repo + "@" + s.sha[:12]
}

// allowPrefix is the allowed_remote_resources entry that admits the
// repository's raw files.
func (s *forgejoAgentsSource) allowPrefix() string {
	return s.baseURL + "/" + s.owner + "/" + s.repo + "/raw/"
}

// urlPrefix is the prefix of the source's file URLs: Forgejo's raw URL of
// the repository at the pinned commit.
func (s *forgejoAgentsSource) urlPrefix() string {
	return s.allowPrefix() + "commit/" + s.sha + "/"
}

// relPathOf returns the repository path of a URL under urlPrefix.
func (s *forgejoAgentsSource) relPathOf(rawURL string) (string, error) {
	rest, ok := strings.CutPrefix(rawURL, s.urlPrefix())
	if !ok {
		return "", fmt.Errorf("%q is not a file of %s", rawURL, s)
	}
	if err := validateForgejoRelPath(rest); err != nil {
		return "", err
	}
	return rest, nil
}

// forgeInfo describes relPath at the pinned commit for directory fetches.
// Forge carries the instance host and any path prefix, so the clone URL
// built from it is unique to the instance; the opts.TreeFetcher hook that
// useForgejoAgentsRepoFetch installs recognises the requests that are for
// the source by it.
func (s *forgejoAgentsSource) forgeInfo(relPath string) *forge.ForgeURLInfo {
	host := strings.TrimPrefix(strings.TrimPrefix(s.baseURL, "https://"), "http://")
	return &forge.ForgeURLInfo{
		Forge:    host,
		Owner:    s.owner,
		Repo:     s.repo,
		Path:     relPath,
		Ref:      s.sha,
		PathType: "tree",
	}
}

// validateForgejoRelPath accepts a clean relative repository path.
func validateForgejoRelPath(p string) error {
	if p == "" || strings.ContainsAny(p, "?#\\") {
		return fmt.Errorf("unsupported repository path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("unsupported repository path %q", p)
		}
	}
	return nil
}

// verifyCommit confirms the pinned SHA names a commit of the repository.
func (s *forgejoAgentsSource) verifyCommit(ctx context.Context) error {
	got, err := s.client.GetCommitSHA(ctx, s.owner, s.repo, s.sha)
	if err != nil {
		return fmt.Errorf("resolving pinned commit %s of %s/%s: %w", s.sha, s.owner, s.repo, err)
	}
	if !strings.EqualFold(got, s.sha) {
		return fmt.Errorf("pinned commit %s of %s/%s resolved to %s", s.sha, s.owner, s.repo, got)
	}
	return nil
}

// lookup walks the pinned commit's trees to the entry at relPath. A missing
// entry wraps forge.ErrNotFound.
func (s *forgejoAgentsSource) lookup(ctx context.Context, relPath string) (forgejo.TreeEntry, error) {
	treeSHA := s.sha
	segs := strings.Split(relPath, "/")
	for i, seg := range segs {
		entries, err := s.client.GetTree(ctx, s.owner, s.repo, treeSHA, false, maxForgejoTreeEntries)
		if err != nil {
			return forgejo.TreeEntry{}, fmt.Errorf("listing %s: %w", s, err)
		}
		var found *forgejo.TreeEntry
		for j := range entries {
			if entries[j].Path == seg {
				found = &entries[j]
				break
			}
		}
		if found == nil {
			return forgejo.TreeEntry{}, fmt.Errorf("%q not found in %s: %w", relPath, s, forge.ErrNotFound)
		}
		if i == len(segs)-1 {
			return *found, nil
		}
		if found.Type != "tree" {
			return forgejo.TreeEntry{}, fmt.Errorf("%q not found in %s: %q is not a directory: %w", relPath, s, strings.Join(segs[:i+1], "/"), forge.ErrNotFound)
		}
		treeSHA = found.SHA
	}
	return forgejo.TreeEntry{}, fmt.Errorf("unsupported repository path %q", relPath)
}

// readBlob reads the regular file entry at relPath and checks its content
// against the entry's object ID.
func (s *forgejoAgentsSource) readBlob(ctx context.Context, relPath string, e forgejo.TreeEntry, limit int64) ([]byte, error) {
	if e.Type != "blob" {
		return nil, fmt.Errorf("%q in %s is a %s, not a file", relPath, s, e.Type)
	}
	if e.Mode == "120000" {
		return nil, fmt.Errorf("%q in %s is a symlink; symlinks are not supported", relPath, s)
	}
	content, err := s.client.GetBlob(ctx, s.owner, s.repo, e.SHA, limit)
	if err != nil {
		return nil, fmt.Errorf("reading %q from %s: %w", relPath, s, err)
	}
	if got := gitBlobID(content, len(e.SHA)); !strings.EqualFold(got, e.SHA) {
		return nil, fmt.Errorf("integrity check failed for %q in %s: content hashes to object %s, expected %s", relPath, s, got, e.SHA)
	}
	return content, nil
}

// readFile reads the file at relPath in the pinned commit.
func (s *forgejoAgentsSource) readFile(ctx context.Context, relPath string, limit int64) ([]byte, error) {
	if err := validateForgejoRelPath(relPath); err != nil {
		return nil, err
	}
	e, err := s.lookup(ctx, relPath)
	if err != nil {
		return nil, err
	}
	return s.readBlob(ctx, relPath, e, limit)
}

// readDir reads every file below dirPath in the pinned commit, keyed by
// path relative to dirPath. Like git sparse checkout it refuses symlinks
// and submodules, and bounds the file count (gitfetch.MaxFiles), each
// file's size (limit), and the total size (maxAgentsRepoHostTreeBytes).
func (s *forgejoAgentsSource) readDir(ctx context.Context, dirPath string, limit int64) (map[string][]byte, error) {
	if err := validateForgejoRelPath(dirPath); err != nil {
		return nil, err
	}
	dir, err := s.lookup(ctx, dirPath)
	if err != nil {
		return nil, err
	}
	if dir.Type != "tree" {
		return nil, fmt.Errorf("%q in %s is not a directory", dirPath, s)
	}
	entries, err := s.client.GetTree(ctx, s.owner, s.repo, dir.SHA, true, maxForgejoTreeEntries)
	if err != nil {
		return nil, fmt.Errorf("listing %q in %s: %w", dirPath, s, err)
	}
	files := make(map[string][]byte)
	var total int64
	for _, e := range entries {
		if e.Type == "tree" {
			continue
		}
		if err := validateForgejoRelPath(e.Path); err != nil {
			return nil, fmt.Errorf("directory %q in %s: %w", dirPath, s, err)
		}
		if len(files) >= gitfetch.MaxFiles {
			return nil, fmt.Errorf("directory %q in %s exceeds the %d-file limit", dirPath, s, gitfetch.MaxFiles)
		}
		content, err := s.readBlob(ctx, dirPath+"/"+e.Path, e, limit)
		if err != nil {
			return nil, err
		}
		total += int64(len(content))
		if total > maxAgentsRepoHostTreeBytes {
			return nil, fmt.Errorf("directory %q in %s exceeds the %d MiB total size limit", dirPath, s, maxAgentsRepoHostTreeBytes>>20)
		}
		files[e.Path] = content
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("directory %q in %s contains no files", dirPath, s)
	}
	return files, nil
}

// gitBlobID returns the git object ID of a blob with content: SHA-1 for a
// 40-character ID, SHA-256 for a 64-character one.
func gitBlobID(content []byte, idLen int) string {
	var h hash.Hash
	if idLen == 64 {
		h = sha256.New()
	} else {
		// Git blob IDs use SHA-1 for compatibility with forge APIs.
		// nosemgrep: go.lang.security.audit.crypto.use-of-sha1
		h = sha1.New() //nolint:gosec // Git's blob hash algorithm, not used for security
	}
	h.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// fetchLimit is the per-file size bound of policy, or the default policy's
// when policy sets none.
func fetchLimit(policy fetch.FetchPolicy) int64 {
	if policy.MaxSizeBytes > 0 {
		return policy.MaxSizeBytes
	}
	return fetch.DefaultPolicy.MaxSizeBytes
}

// fetchForgejoAgentsRepoFile reads relPath from the Forgejo source and caches
// it exactly as fetchPinnedAgentsRepoFile caches a file of
// fullsend-ai/agents: the URL must be admitted by allowed_remote_resources,
// the content is stored by its SHA-256 and recorded in the fetch audit log,
// and offline mode resolves the pinned URL from the cache. Errors are
// returned, never swallowed: a configured Forgejo source does not fall back
// to GitHub. A missing file wraps forge.ErrNotFound.
func fetchForgejoAgentsRepoFile(ctx context.Context, src *forgejoAgentsSource, relPath string, composeOpts harness.ComposeOpts, printer *ui.Printer, noun string) (string, harness.Dependency, error) {
	var none harness.Dependency
	if err := validateForgejoRelPath(relPath); err != nil {
		return "", none, err
	}
	rawURL := src.fileURL(relPath)
	allowedBy := harness.MatchingAllowedPrefixInList(rawURL, composeOpts.OrgAllowlist)
	if allowedBy == "" {
		return "", none, fmt.Errorf("%s from %s: URL %s is not in allowed_remote_resources (add %s to allowed_remote_resources in config.yaml)", noun, src, rawURL, src.allowPrefix())
	}

	if composeOpts.FetchPolicy.Offline {
		// CacheFindNewest re-verifies the content hash before returning a hit.
		_, entry, err := fetch.CacheFindNewest(composeOpts.WorkspaceRoot, func(u string) bool { return u == rawURL })
		if err != nil {
			return "", none, fmt.Errorf("searching the cache for %s from %s: %w", noun, src, err)
		}
		if entry == nil {
			return "", none, fmt.Errorf("%s from %s is not cached and offline mode cannot fetch it", noun, src)
		}
		return recordAgentsRepoFile(composeOpts, rawURL, entry.SHA256, allowedBy, entry.FetchTime, true)
	}

	printer.StepStart(fmt.Sprintf("Fetching %s from %s", noun, src))
	if err := src.verifyCommit(ctx); err != nil {
		return "", none, fmt.Errorf("fetching %s: %w", noun, err)
	}
	content, err := src.readFile(ctx, relPath, fetchLimit(composeOpts.FetchPolicy))
	if err != nil {
		return "", none, fmt.Errorf("fetching %s: %w", noun, err)
	}
	if err := fetch.CachePut(composeOpts.WorkspaceRoot, rawURL, content); err != nil {
		return "", none, fmt.Errorf("caching %s: %w", noun, err)
	}
	localPath, dep, err := recordAgentsRepoFile(composeOpts, rawURL, fetch.ComputeSHA256(content), allowedBy, time.Now().UTC(), false)
	if err != nil {
		return "", none, err
	}
	printer.StepDone(fmt.Sprintf("%s resolved from %s", noun, src))
	return localPath, dep, nil
}

// fileURL is the URL relPath is recorded under.
func (s *forgejoAgentsSource) fileURL(relPath string) string {
	return s.urlPrefix() + relPath
}

// recordAgentsRepoFile returns the cache path of content cached for rawURL
// under contentHash and its dependency, after appending the fetch audit
// entry.
func recordAgentsRepoFile(composeOpts harness.ComposeOpts, rawURL, contentHash, allowedBy string, fetchedAt time.Time, cacheHit bool) (string, harness.Dependency, error) {
	var none harness.Dependency
	cachePath, err := fetch.CachePath(composeOpts.WorkspaceRoot, contentHash)
	if err != nil {
		return "", none, fmt.Errorf("resolving cache path for %s: %w", rawURL, err)
	}
	localPath := filepath.Join(cachePath, "content")
	if composeOpts.AuditLogPath != "" {
		if err := fetch.AppendFetchAudit(composeOpts.AuditLogPath, fetch.FetchAuditEntry{
			TraceID:   composeOpts.TraceID,
			FetchTime: fetchedAt,
			URL:       rawURL,
			SHA256:    contentHash,
			FetchType: "static",
			AllowedBy: allowedBy,
			CacheHit:  cacheHit,
		}); err != nil {
			return "", none, fmt.Errorf("writing fetch audit log: %w", err)
		}
	}
	return localPath, harness.Dependency{
		Field:     "base",
		URL:       rawURL,
		LocalPath: localPath,
		SHA256:    contentHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
		Type:      "file",
	}, nil
}

// agentsRepoFallback resolves a built-in agent's harness from the agents
// repository: from the Forgejo source when one is configured
// (forgejoAgentsSourceFn), otherwise from fullsend-ai/agents on GitHub
// (tryAgentsRepoFallback). ok is false when the agent is not a built-in
// one or the GitHub fallback was not usable. A configured Forgejo source
// never falls back to GitHub: an invalid configuration or a failed read is
// returned as an error.
func agentsRepoFallback(ctx context.Context, agentName string, forgeClient forge.Client, composeOpts harness.ComposeOpts, printer *ui.Printer) (string, []harness.Dependency, bool, error) {
	src, err := forgejoAgentsSourceFn()
	if err != nil {
		return "", nil, false, fmt.Errorf("Forgejo agent definitions: %w", err)
	}
	if src == nil {
		path, deps, ok := tryAgentsRepoFallback(ctx, agentName, forgeClient, composeOpts, printer)
		return path, deps, ok, nil
	}
	normalizedName := strings.ToLower(agentName)
	if !defaultAgentsRepoKnownAgents[normalizedName] {
		return "", nil, false, nil
	}
	path, dep, err := fetchForgejoAgentsRepoFile(ctx, src, "harness/"+normalizedName+".yaml", composeOpts, printer, "agent "+agentName)
	if err != nil {
		return "", nil, false, err
	}
	return path, []harness.Dependency{dep}, true, nil
}

// forgejoMeasurementManifest is tryAgentsRepoMeasurementManifest for a
// configured Forgejo source: a missing manifest skips, other errors warn.
func forgejoMeasurementManifest(ctx context.Context, src *forgejoAgentsSource, agentName string, composeOpts harness.ComposeOpts, printer *ui.Printer) (string, bool) {
	noun := "eval measurement manifest for " + agentName
	path, _, err := fetchForgejoAgentsRepoFile(ctx, src, "eval/measurements/"+strings.ToLower(agentName)+".yaml", composeOpts, printer, noun)
	if err != nil {
		if forge.IsNotFound(err) {
			printer.StepInfo(fmt.Sprintf("No %s in %s; skipping", noun, src))
		} else {
			printer.StepWarn(fmt.Sprintf("Failed to fetch %s: %v", noun, err))
		}
		return "", false
	}
	return path, true
}

// useForgejoAgentsRepoFetch makes opts read the companion files and
// directory resources of a harness fetched from the Forgejo source through
// the source, at its pinned commit. URLs outside the source keep the
// default handling.
func useForgejoAgentsRepoFetch(opts *harness.ComposeOpts, src *forgejoAgentsSource) {
	prefix := src.urlPrefix()
	policy := opts.FetchPolicy
	limit := fetchLimit(policy)
	githubURL := opts.GitHubURL
	opts.FetchFile = func(ctx context.Context, rawURL string) ([]byte, error) {
		if !strings.HasPrefix(rawURL, prefix) {
			return fetch.FetchURL(ctx, rawURL, policy)
		}
		relPath, err := src.relPathOf(rawURL)
		if err != nil {
			return nil, err
		}
		return src.readFile(ctx, relPath, limit)
	}
	opts.ParseRawURL = func(rawURL string) (*forge.ForgeURLInfo, error) {
		if !strings.HasPrefix(rawURL, prefix) {
			return forge.ParseRawContentURLOnHost(rawURL, githubURL)
		}
		relPath, err := src.relPathOf(rawURL)
		if err != nil {
			return nil, err
		}
		return src.forgeInfo(relPath), nil
	}
	next := opts.TreeFetcher
	if next == nil {
		next = gitfetch.FetchTree
	}
	cloneURL := src.forgeInfo("").CloneURL()
	opts.TreeFetcher = func(ctx context.Context, gotCloneURL, dirPath, ref, token string) (map[string][]byte, error) {
		if gotCloneURL != cloneURL {
			return next(ctx, gotCloneURL, dirPath, ref, token)
		}
		if ref != src.sha {
			return nil, fmt.Errorf("directory %q of %s requested at %q, not the pinned commit", dirPath, src, ref)
		}
		return src.readDir(ctx, dirPath, limit)
	}
}

// useConfiguredForgejoAgentsRepoFetch applies useForgejoAgentsRepoFetch when
// opts.SourceURL is a file of the configured Forgejo source, and reports
// whether it did.
func useConfiguredForgejoAgentsRepoFetch(opts *harness.ComposeOpts) bool {
	src, err := forgejoAgentsSourceFn()
	if err != nil || src == nil || !strings.HasPrefix(opts.SourceURL, src.urlPrefix()) {
		return false
	}
	useForgejoAgentsRepoFetch(opts, src)
	return true
}
