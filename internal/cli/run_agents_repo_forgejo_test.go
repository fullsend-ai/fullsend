package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/forgejostack"
)

const forgejoTestSHA = "0123456789abcdef0123456789abcdef01234567"

// fakeForgejoGit serves the git data API of one repository at one commit
// under /forgejo, with real git blob IDs. Tree IDs are "t" followed by the
// hex-encoded directory path.
type fakeForgejoGit struct {
	// prefix is the instance's path prefix; "/forgejo" when unset, "/" for
	// none.
	prefix              string
	owner, repo, commit string
	files               map[string][]byte
	symlinks            map[string]bool
	// corrupt maps a path to content served in place of the file's own
	// under its blob ID.
	corrupt map[string][]byte

	mu       sync.Mutex
	requests []string
	auth     []string
}

func (f *fakeForgejoGit) treeID(dir string) string {
	if dir == "" {
		return f.commit
	}
	return "t" + hex.EncodeToString([]byte(dir))
}

func (f *fakeForgejoGit) dirOf(id string) (string, bool) {
	if id == f.commit {
		return "", true
	}
	b, err := hex.DecodeString(strings.TrimPrefix(id, "t"))
	if err != nil || !strings.HasPrefix(id, "t") {
		return "", false
	}
	return string(b), true
}

func (f *fakeForgejoGit) entries(dir string, recursive bool) []map[string]any {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	seenDirs := map[string]bool{}
	var out []map[string]any
	paths := make([]string, 0, len(f.files))
	for p := range f.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		segs := strings.Split(rest, "/")
		for i := 0; i < len(segs)-1; i++ {
			if !recursive && i > 0 {
				break
			}
			sub := strings.Join(segs[:i+1], "/")
			if !seenDirs[sub] {
				seenDirs[sub] = true
				out = append(out, map[string]any{"path": sub, "mode": "040000", "type": "tree", "sha": f.treeID(prefix + sub)})
			}
		}
		if !recursive && len(segs) > 1 {
			continue
		}
		mode := "100644"
		if f.symlinks[p] {
			mode = "120000"
		}
		out = append(out, map[string]any{"path": rest, "mode": mode, "type": "blob", "size": len(f.files[p]), "sha": gitBlobID(f.files[p], len(f.commit))})
	}
	return out
}

func (f *fakeForgejoGit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	base := strings.TrimSuffix(f.prefix, "/") + "/api/v1/repos/" + f.owner + "/" + f.repo + "/git/"
	rest, ok := strings.CutPrefix(r.URL.Path, base)
	kind, id, _ := strings.Cut(rest, "/")
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"object does not exist"}`))
	}
	if !ok {
		notFound()
		return
	}
	switch kind {
	case "commits":
		if id != f.commit {
			notFound()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": f.commit})
	case "trees":
		dir, ok := f.dirOf(id)
		if !ok {
			notFound()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": id, "tree": f.entries(dir, r.URL.Query().Get("recursive") == "true"), "truncated": false})
	case "blobs":
		for p, content := range f.files {
			if gitBlobID(content, len(f.commit)) != id {
				continue
			}
			if c, ok := f.corrupt[p]; ok {
				content = c
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64", "size": len(content)})
			return
		}
		notFound()
	default:
		notFound()
	}
}

func (f *fakeForgejoGit) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...), append([]string(nil), f.auth...)
}

// forgejoAgentsFixture is the minimal agent-definition repository the
// tests resolve built-in agents from.
func forgejoAgentsFixture() map[string][]byte {
	return map[string][]byte{
		"harness/triage.yaml":             []byte("role: triage\nslug: triage\nagent: agents/triage.md\npolicy: policies/triage.yaml\nskills:\n  - skills/review\n"),
		"agents/triage.md":                []byte("# triage agent"),
		"policies/triage.yaml":            []byte("# triage policy"),
		"skills/review/SKILL.md":          []byte("# review"),
		"skills/review/checks/lint.md":    []byte("# lint"),
		"eval/measurements/triage.yaml":   []byte("metrics: []\n"),
		"skills/review-extra/SKILL.md":    []byte("# sibling"),
		"skills/review-extra/checks/x.md": []byte("# not part of the skill"),
	}
}

// githubTrap is a GitHub API stand-in that records every request; the
// Forgejo path must never reach it.
type githubTrap struct {
	mu    sync.Mutex
	calls []string
}

func newGitHubTrap(t *testing.T) (*githubTrap, *gh.LiveClient) {
	t.Helper()
	trap := &githubTrap{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trap.mu.Lock()
		trap.calls = append(trap.calls, r.URL.Path)
		trap.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return trap, gh.New("github-token").WithBaseURL(srv.URL)
}

func (g *githubTrap) assertUntouched(t *testing.T) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.calls, "the Forgejo source must not reach GitHub")
}

// setupForgejoAgents serves files as fullsend-ai/agents at forgejoTestSHA,
// points the environment at it, and returns compose options that allow it.
func setupForgejoAgents(t *testing.T, f *fakeForgejoGit) (string, harness.ComposeOpts) {
	t.Helper()
	if f.owner == "" {
		f.owner, f.repo = "fullsend-ai", "agents"
	}
	if f.commit == "" {
		f.commit = forgejoTestSHA
	}
	if f.prefix == "" {
		f.prefix = "/forgejo"
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	base := srv.URL + strings.TrimSuffix(f.prefix, "/")
	t.Setenv(forgejoURLEnv, base)
	t.Setenv(forgejoAgentsRepoEnv, "")
	t.Setenv(forgejoAgentsRefEnv, f.commit)
	t.Setenv(forgejoTokenEnv, "forgejo-run-token")
	dir := t.TempDir()
	return base, harness.ComposeOpts{
		WorkspaceRoot: dir,
		AuditLogPath:  filepath.Join(dir, ".fullsend-cache", "fetch-audit.jsonl"),
		FetchPolicy:   fetch.FetchPolicy{MaxSizeBytes: 1 << 20},
		OrgAllowlist:  []string{base + "/" + f.owner + "/" + f.repo + "/raw/"},
	}
}

func TestForgejoAgentsSourceFromEnv(t *testing.T) {
	sha256 := strings.Repeat("ab", 32)
	tests := []struct {
		name    string
		env     map[string]string
		wantNil bool
		wantErr string
		check   func(t *testing.T, s *forgejoAgentsSource)
	}{
		{name: "nothing configured", wantNil: true},
		{
			name: "pinned default repository",
			env:  map[string]string{forgejoURLEnv: "https://git.example.com/forgejo/", forgejoAgentsRefEnv: forgejoTestSHA},
			check: func(t *testing.T, s *forgejoAgentsSource) {
				assert.Equal(t, "https://git.example.com/forgejo", s.baseURL)
				assert.Equal(t, "fullsend-ai", s.owner)
				assert.Equal(t, "agents", s.repo)
				assert.Equal(t, "https://git.example.com/forgejo/fullsend-ai/agents/raw/commit/"+forgejoTestSHA+"/", s.urlPrefix())
				assert.Equal(t, "fullsend-ai/agents@0123456789ab", s.String())
				assert.Equal(t, "https://git.example.com/forgejo/fullsend-ai/agents.git", s.forgeInfo("").CloneURL())
			},
		},
		{
			name: "custom repository and SHA-256 commit on a loopback http instance",
			env:  map[string]string{forgejoURLEnv: "http://127.0.0.1:3000", forgejoAgentsRepoEnv: "acme/fleet", forgejoAgentsRefEnv: sha256},
			check: func(t *testing.T, s *forgejoAgentsSource) {
				assert.Equal(t, "acme/fleet@"+sha256[:12], s.String())
				assert.Equal(t, sha256, s.sha)
			},
		},
		{name: "ref without instance", env: map[string]string{forgejoAgentsRefEnv: forgejoTestSHA}, wantErr: forgejoURLEnv + " must name the Forgejo instance"},
		{name: "repository without instance", env: map[string]string{forgejoAgentsRepoEnv: "acme/fleet"}, wantErr: forgejoURLEnv + " must name the Forgejo instance"},
		{name: "plain http to a remote host", env: map[string]string{forgejoURLEnv: "http://git.example.com", forgejoAgentsRefEnv: forgejoTestSHA}, wantErr: "must use https"},
		{name: "credentials in the URL", env: map[string]string{forgejoURLEnv: "https://u:p@git.example.com", forgejoAgentsRefEnv: forgejoTestSHA}, wantErr: "must not include credentials"},
		{name: "invalid repository", env: map[string]string{forgejoURLEnv: "https://git.example.com", forgejoAgentsRepoEnv: "acme", forgejoAgentsRefEnv: forgejoTestSHA}, wantErr: "must be an owner/repo path"},
		{name: "instance without a pin", env: map[string]string{forgejoURLEnv: "https://git.example.com"}, wantErr: forgejoAgentsRefEnv + " must be set to the full commit SHA of fullsend-ai/agents"},
		{name: "branch name", env: map[string]string{forgejoURLEnv: "https://git.example.com", forgejoAgentsRefEnv: "main"}, wantErr: "not a full lowercase commit SHA"},
		{name: "abbreviated SHA", env: map[string]string{forgejoURLEnv: "https://git.example.com", forgejoAgentsRefEnv: forgejoTestSHA[:12]}, wantErr: "not a full lowercase commit SHA"},
		{name: "uppercase SHA", env: map[string]string{forgejoURLEnv: "https://git.example.com", forgejoAgentsRefEnv: strings.ToUpper(forgejoTestSHA)}, wantErr: "not a full lowercase commit SHA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := forgejoAgentsSourceFromEnv(func(k string) string { return tt.env[k] }, nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, s)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, s)
				return
			}
			require.NotNil(t, s)
			tt.check(t, s)
		})
	}
}

// TestResolveAgentSource_Forgejo is the GitHub-free path: a built-in agent
// resolves from the configured Forgejo repository at its pinned commit, its
// companion files and skill directory load through the same source, the
// Forgejo token goes only to the Forgejo instance, and GitHub is never
// contacted.
func TestResolveAgentSource_Forgejo(t *testing.T) {
	f := &fakeForgejoGit{files: forgejoAgentsFixture()}
	base, opts := setupForgejoAgents(t, f)
	trap, ghClient := newGitHubTrap(t)
	ctx := context.Background()

	harnessPath, deps, err := resolveAgentSource(ctx, opts.WorkspaceRoot, "Triage", ghClient, nil, opts, ui.New(io.Discard))
	require.NoError(t, err)
	prefix := base + "/fullsend-ai/agents/raw/commit/" + forgejoTestSHA + "/"
	require.Len(t, deps, 1)
	assert.Equal(t, prefix+"harness/triage.yaml", deps[0].URL)
	assert.Equal(t, fetch.ComputeSHA256(f.files["harness/triage.yaml"]), deps[0].SHA256)
	got, err := os.ReadFile(harnessPath)
	require.NoError(t, err)
	assert.Equal(t, f.files["harness/triage.yaml"], got, "content matches the pinned commit")

	audit, err := os.ReadFile(opts.AuditLogPath)
	require.NoError(t, err)
	assert.Contains(t, string(audit), prefix+"harness/triage.yaml")

	opts.SourceURL = deps[0].URL
	useAgentsRepoHostFetch(&opts, ghClient)
	h, loaded, err := harness.LoadWithBase(ctx, harnessPath, opts)
	require.NoError(t, err)
	got, err = os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, "# triage agent", string(got))
	got, err = os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, "# triage policy", string(got))
	require.Len(t, h.Skills, 1)
	got, err = os.ReadFile(filepath.Join(h.Skills[0].Source, "checks", "lint.md"))
	require.NoError(t, err)
	assert.Equal(t, "# lint", string(got))
	assert.NoFileExists(t, filepath.Join(h.Skills[0].Source, "checks", "x.md"))
	var urls []string
	for _, d := range loaded {
		urls = append(urls, d.URL)
	}
	assert.Contains(t, urls, prefix+"agents/triage.md")

	trap.assertUntouched(t)
	paths, auth := f.seen()
	assert.Contains(t, paths, "/forgejo/api/v1/repos/fullsend-ai/agents/git/commits/"+forgejoTestSHA)
	for _, a := range auth {
		assert.Equal(t, "token forgejo-run-token", a)
	}

	// Offline, the pinned URL resolves from the cache without a request.
	before := len(paths)
	opts.FetchPolicy.Offline = true
	offlinePath, offlineDeps, err := resolveAgentSource(ctx, opts.WorkspaceRoot, "triage", ghClient, nil, opts, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, harnessPath, offlinePath)
	assert.True(t, offlineDeps[0].CacheHit)
	paths, _ = f.seen()
	assert.Len(t, paths, before, "offline resolution makes no request")
	trap.assertUntouched(t)
}

// TestResolveAgentSource_ForgejoDevFixture resolves and loads the triage
// harness of the forgejo-dev agent-definition fixture with the environment
// the stack reports, served from a loopback http instance with no path
// prefix as the stack serves it, and without reaching GitHub. It also
// keeps the environment variable names the stack duplicates in step with
// the ones read here.
func TestResolveAgentSource_ForgejoDevFixture(t *testing.T) {
	files := map[string][]byte{}
	for p, c := range forgejostack.AgentFixtureFiles() {
		files[p] = []byte(c)
	}
	f := &fakeForgejoGit{prefix: "/", owner: forgejostack.AgentsOwner, repo: forgejostack.AgentsRepo, commit: forgejoTestSHA, files: files}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv(forgejoTokenEnv, "")
	env := forgejostack.AgentSourceEnv(srv.URL+"/", forgejoTestSHA)
	require.ElementsMatch(t, []string{forgejoURLEnv, forgejoAgentsRepoEnv, forgejoAgentsRefEnv}, slices.Collect(maps.Keys(env)))
	for k, v := range env {
		t.Setenv(k, v)
	}
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetch.FetchPolicy{MaxSizeBytes: 1 << 20},
		OrgAllowlist:  []string{srv.URL + "/fullsend-ai/agents/raw/"},
	}
	trap, ghClient := newGitHubTrap(t)
	ctx := context.Background()

	harnessPath, deps, err := resolveAgentSource(ctx, opts.WorkspaceRoot, "triage", ghClient, nil, opts, ui.New(io.Discard))
	require.NoError(t, err)
	opts.SourceURL = deps[0].URL
	useAgentsRepoHostFetch(&opts, ghClient)
	h, _, err := harness.LoadWithBase(ctx, harnessPath, opts)
	require.NoError(t, err)
	got, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, files["agents/triage.md"], got)
	assert.FileExists(t, h.Policy)
	trap.assertUntouched(t)
	_, auth := f.seen()
	for _, a := range auth {
		assert.Empty(t, a, "a public fixture is read anonymously")
	}
}

// TestResolveAgentSource_ForgejoFailures checks that every failure of a
// configured Forgejo source is a clear error, and none falls back to GitHub.
func TestResolveAgentSource_ForgejoFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		setup   func(t *testing.T, f *fakeForgejoGit, opts *harness.ComposeOpts)
		agent   string
		wantErr string
	}{
		{
			name: "missing agent definition",
			setup: func(_ *testing.T, f *fakeForgejoGit, _ *harness.ComposeOpts) {
				delete(f.files, "harness/triage.yaml")
			},
			wantErr: `"harness/triage.yaml" not found in fullsend-ai/agents@0123456789ab`,
		},
		{
			name: "missing repository",
			setup: func(t *testing.T, _ *fakeForgejoGit, opts *harness.ComposeOpts) {
				t.Setenv(forgejoAgentsRepoEnv, "acme/missing")
				opts.OrgAllowlist = []string{os.Getenv(forgejoURLEnv) + "/acme/missing/raw/"}
			},
			wantErr: "resolving pinned commit " + forgejoTestSHA + " of acme/missing",
		},
		{
			name: "pinned commit does not exist",
			setup: func(t *testing.T, f *fakeForgejoGit, _ *harness.ComposeOpts) {
				t.Setenv(forgejoAgentsRefEnv, strings.Repeat("f", 40))
			},
			wantErr: "resolving pinned commit ffffffff",
		},
		{
			name: "mutable ref",
			setup: func(t *testing.T, _ *fakeForgejoGit, _ *harness.ComposeOpts) {
				t.Setenv(forgejoAgentsRefEnv, "main")
			},
			wantErr: "not a full lowercase commit SHA",
		},
		{
			name: "unreachable instance",
			setup: func(t *testing.T, _ *fakeForgejoGit, opts *harness.ComposeOpts) {
				dead := httptest.NewServer(http.NotFoundHandler())
				dead.Close()
				t.Setenv(forgejoURLEnv, dead.URL)
				opts.OrgAllowlist = []string{dead.URL + "/fullsend-ai/agents/raw/"}
			},
			wantErr: "resolving pinned commit",
		},
		{
			name: "not in allowed_remote_resources",
			setup: func(_ *testing.T, _ *fakeForgejoGit, opts *harness.ComposeOpts) {
				opts.OrgAllowlist = []string{"https://raw.githubusercontent.com/fullsend-ai/agents/"}
			},
			wantErr: "is not in allowed_remote_resources (add " + "http://",
		},
		{
			name: "content does not match its object ID",
			setup: func(_ *testing.T, f *fakeForgejoGit, _ *harness.ComposeOpts) {
				f.corrupt = map[string][]byte{"harness/triage.yaml": []byte("role: triage\nagent: https://evil.example/a.md\n")}
			},
			wantErr: `integrity check failed for "harness/triage.yaml"`,
		},
		{
			name: "symlinked definition",
			setup: func(_ *testing.T, f *fakeForgejoGit, _ *harness.ComposeOpts) {
				f.symlinks = map[string]bool{"harness/triage.yaml": true}
			},
			wantErr: "symlinks are not supported",
		},
		{
			name: "path below a file",
			setup: func(_ *testing.T, f *fakeForgejoGit, _ *harness.ComposeOpts) {
				delete(f.files, "harness/triage.yaml")
				f.files["harness"] = []byte("not a directory")
			},
			wantErr: `"harness" is not a directory`,
		},
		{
			name: "offline with nothing cached",
			setup: func(_ *testing.T, _ *fakeForgejoGit, opts *harness.ComposeOpts) {
				opts.FetchPolicy.Offline = true
			},
			wantErr: "is not cached and offline mode cannot fetch it",
		},
		{
			name:    "unknown agent is never fetched",
			agent:   "custom",
			wantErr: `resolving agent "custom": no config and agents-repo fallback unavailable`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeForgejoGit{files: forgejoAgentsFixture()}
			_, opts := setupForgejoAgents(t, f)
			if tt.setup != nil {
				tt.setup(t, f, &opts)
			}
			trap, ghClient := newGitHubTrap(t)
			agent := tt.agent
			if agent == "" {
				agent = "triage"
			}
			_, _, err := resolveAgentSource(ctx, opts.WorkspaceRoot, agent, ghClient, nil, opts, ui.New(io.Discard))
			require.ErrorContains(t, err, tt.wantErr)
			trap.assertUntouched(t)
		})
	}
}

func TestTryAgentsRepoMeasurementManifest_Forgejo(t *testing.T) {
	ctx := context.Background()
	f := &fakeForgejoGit{files: forgejoAgentsFixture()}
	_, opts := setupForgejoAgents(t, f)
	trap, ghClient := newGitHubTrap(t)

	p, ok := tryAgentsRepoMeasurementManifest(ctx, "triage", ghClient, opts, ui.New(io.Discard))
	require.True(t, ok)
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "metrics: []\n", string(got))

	delete(f.files, "eval/measurements/triage.yaml")
	opts.WorkspaceRoot = t.TempDir()
	_, ok = tryAgentsRepoMeasurementManifest(ctx, "triage", ghClient, opts, ui.New(io.Discard))
	assert.False(t, ok, "a missing manifest is skipped")

	opts.OrgAllowlist = nil
	_, ok = tryAgentsRepoMeasurementManifest(ctx, "triage", ghClient, opts, ui.New(io.Discard))
	assert.False(t, ok, "other failures warn and skip")

	t.Setenv(forgejoAgentsRefEnv, "main")
	_, ok = tryAgentsRepoMeasurementManifest(ctx, "triage", ghClient, opts, ui.New(io.Discard))
	assert.False(t, ok, "an invalid source warns and skips")
	trap.assertUntouched(t)
}

// TestForgejoAgentsRepoFetchHooks checks the compose hooks directly: URLs
// outside the source keep the default handling, traversal and unpinned
// requests are refused, and directory reads apply their bounds.
func TestForgejoAgentsRepoFetchHooks(t *testing.T) {
	ctx := context.Background()
	files := forgejoAgentsFixture()
	files["skills/linked/SKILL.md"] = []byte("# linked")
	files["skills/empty/.keep"] = []byte("")
	f := &fakeForgejoGit{files: files, symlinks: map[string]bool{"skills/linked/SKILL.md": true}}
	base, opts := setupForgejoAgents(t, f)
	src, err := forgejoAgentsSourceFn()
	require.NoError(t, err)
	prefix := src.urlPrefix()

	var nextCalls []string
	opts.TreeFetcher = func(_ context.Context, cloneURL, _, _, _ string) (map[string][]byte, error) {
		nextCalls = append(nextCalls, cloneURL)
		return map[string][]byte{"SKILL.md": []byte("other")}, nil
	}
	opts.SourceURL = prefix + "harness/triage.yaml"
	require.True(t, useConfiguredForgejoAgentsRepoFetch(&opts))

	got, err := opts.FetchFile(ctx, prefix+"agents/triage.md")
	require.NoError(t, err)
	assert.Equal(t, "# triage agent", string(got))
	_, err = opts.FetchFile(ctx, prefix+"agents/../harness/triage.yaml")
	assert.ErrorContains(t, err, "unsupported repository path")
	_, err = opts.FetchFile(ctx, "https://evil.example/agents/triage.md")
	assert.Error(t, err, "other URLs go through the fetch policy")

	info, err := opts.ParseRawURL(prefix + "skills/review")
	require.NoError(t, err)
	assert.Equal(t, "skills/review", info.Path)
	assert.Equal(t, forgejoTestSHA, info.Ref)
	_, err = opts.ParseRawURL(prefix + "skills/review?x=1")
	assert.Error(t, err)
	info, err = opts.ParseRawURL("https://raw.githubusercontent.com/acme/repo/" + forgejoTestSHA + "/skills/x")
	require.NoError(t, err, "other URLs keep the default parser")
	assert.Equal(t, "acme", info.Owner)

	cloneURL := src.forgeInfo("").CloneURL()
	dir, err := opts.TreeFetcher(ctx, cloneURL, "skills/review", forgejoTestSHA, "")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"SKILL.md": []byte("# review"), "checks/lint.md": []byte("# lint")}, dir)
	_, err = opts.TreeFetcher(ctx, cloneURL, "skills/review", "main", "")
	assert.ErrorContains(t, err, "not the pinned commit")
	_, err = opts.TreeFetcher(ctx, cloneURL, "skills/linked", forgejoTestSHA, "")
	assert.ErrorContains(t, err, "symlinks are not supported")
	_, err = opts.TreeFetcher(ctx, cloneURL, "skills/review/SKILL.md", forgejoTestSHA, "")
	assert.ErrorContains(t, err, "is not a directory")
	_, err = opts.TreeFetcher(ctx, cloneURL, "skills/nope", forgejoTestSHA, "")
	assert.ErrorIs(t, err, forge.ErrNotFound)
	_, err = opts.TreeFetcher(ctx, cloneURL, "../x", forgejoTestSHA, "")
	assert.ErrorContains(t, err, "unsupported repository path")
	assert.Empty(t, nextCalls)
	other, err := opts.TreeFetcher(ctx, "https://github.com/acme/repo.git", "skills/x", forgejoTestSHA, "")
	require.NoError(t, err)
	assert.Equal(t, "other", string(other["SKILL.md"]))
	assert.Equal(t, []string{"https://github.com/acme/repo.git"}, nextCalls)

	limited := opts
	limited.FetchPolicy.MaxSizeBytes = 3
	limited.TreeFetcher = nil
	require.True(t, useConfiguredForgejoAgentsRepoFetch(&limited))
	_, err = limited.FetchFile(ctx, prefix+"agents/triage.md")
	assert.ErrorContains(t, err, "3-byte limit")

	// A harness from anywhere else leaves the options alone.
	plain := harness.ComposeOpts{SourceURL: base + "/acme/other/raw/commit/" + forgejoTestSHA + "/harness/triage.yaml"}
	assert.False(t, useConfiguredForgejoAgentsRepoFetch(&plain))
	assert.Nil(t, plain.FetchFile)
	t.Setenv(forgejoAgentsRefEnv, "")
	t.Setenv(forgejoURLEnv, "")
	assert.False(t, useConfiguredForgejoAgentsRepoFetch(&opts), "no source configured")
}

func TestForgejoAgentsSource_ReadDirBounds(t *testing.T) {
	ctx := context.Background()
	files := map[string][]byte{"harness/triage.yaml": []byte("x")}
	for i := 0; i <= 1000; i++ {
		files[path.Join("skills/many", "f"+hex.EncodeToString([]byte{byte(i >> 8), byte(i)}))] = []byte{byte(i >> 8), byte(i)}
	}
	f := &fakeForgejoGit{files: files}
	setupForgejoAgents(t, f)
	src, err := forgejoAgentsSourceFn()
	require.NoError(t, err)
	_, err = src.readDir(ctx, "skills/many", 1<<20)
	assert.ErrorContains(t, err, "exceeds the 1000-file limit")
}

func TestGitBlobID(t *testing.T) {
	// git hash-object of "hello\n".
	assert.Equal(t, "ce013625030ba8dba906f756967f9e9ca394464a", gitBlobID([]byte("hello\n"), 40))
	// git hash-object of "hello\n" in a SHA-256 repository.
	assert.Equal(t, "2cf8d83d9ee29543b34a87727421fdecb7e3f3a183d337639025de576db9ebb4", gitBlobID([]byte("hello\n"), 64))
}

func TestFetchLimit(t *testing.T) {
	assert.Equal(t, int64(7), fetchLimit(fetch.FetchPolicy{MaxSizeBytes: 7}))
	assert.Equal(t, fetch.DefaultPolicy.MaxSizeBytes, fetchLimit(fetch.FetchPolicy{}))
}
