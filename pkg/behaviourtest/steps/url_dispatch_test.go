package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// roundTripperFunc is an adapter to use a function as http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// speedUpRetries sets retry delays to zero for fast tests.
func speedUpRetries(t *testing.T) {
	t.Helper()
	origRaw := rawURLRetryDelay
	origFile := fileAccessRetryDelay
	rawURLRetryDelay = 0
	fileAccessRetryDelay = 0
	t.Cleanup(func() {
		rawURLRetryDelay = origRaw
		fileAccessRetryDelay = origFile
	})
}

// stubRawHTTPClient replaces rawHTTPClient with a mock that returns 200
// for all requests, simulating a publicly accessible raw URL.
func stubRawHTTPClient(t *testing.T) {
	t.Helper()
	speedUpRetries(t)
	orig := rawHTTPClient
	rawHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       http.NoBody,
			}, nil
		}),
	}
	t.Cleanup(func() { rawHTTPClient = orig })
}

// stubRawHTTPClientStatus replaces rawHTTPClient with a mock that returns
// the specified status code for all requests.
func stubRawHTTPClientStatus(t *testing.T, status int) {
	t.Helper()
	speedUpRetries(t)
	orig := rawHTTPClient
	rawHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       http.NoBody,
			}, nil
		}),
	}
	t.Cleanup(func() { rawHTTPClient = orig })
}

func TestGivenHarnessHostingRepo_Validation(t *testing.T) {
	w := &world.World{}
	require.Error(t, givenHarnessHostingRepo(w, ""))
	require.Error(t, givenHarnessHostingRepo(w, "repo"), "should fail when org is not set")
}

func TestGivenHarnessHostingRepo_SetsWorldFields(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}, repos: map[string]bool{}}
	w := &world.World{
		Org: "test-org",
		SCM: scm,
	}
	err := givenHarnessHostingRepo(w, "my-host-repo")
	require.NoError(t, err)
	assert.Equal(t, "test-org", w.URLHarnessRepoOwner)
	assert.Equal(t, "my-host-repo", w.URLHarnessRepoName)
	assert.True(t, scm.ensurePublicCalled, "EnsureRepoPublic should be called after CreateRepo")
}

func TestGivenHarnessHostingRepo_FailsWhenNotPublic(t *testing.T) {
	scm := &fakeURLSCM{
		files:           map[string][]byte{},
		repos:           map[string]bool{},
		ensurePublicErr: fmt.Errorf("org enforces private repos"),
	}
	w := &world.World{
		Org: "test-org",
		SCM: scm,
	}
	err := givenHarnessHostingRepo(w, "my-host-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be public")
	assert.Contains(t, err.Error(), "org enforces private repos")
}

func TestGivenURLSourcedCustomHarness_Validation(t *testing.T) {
	w := &world.World{}
	require.Error(t, givenURLSourcedCustomHarness(w, "", "doc", urlHarnessOpts{}))
	require.Error(t, givenURLSourcedCustomHarness(w, "agent", "", urlHarnessOpts{}))
}

func TestGivenURLSourcedCustomHarness_RequiresHostingRepo(t *testing.T) {
	w := &world.World{
		Install: &fakeURLInstall{owner: "test-org", repo: "test-repo"},
		SCM:     &fakeURLSCM{files: map[string][]byte{}},
	}
	err := givenURLSourcedCustomHarness(w, "url-test", "agent: agents/triage.md", urlHarnessOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness-hosting repo must be created first")
}

func TestGivenURLSourcedCustomHarness_SetsDispatchAgent(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "test-org", repo: "test-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "test-org",
		URLHarnessRepoName:  "harness-host",
	}
	err := givenURLSourcedCustomHarness(w, "url-test", "agent: agents/triage.md\nrole: triage\nslug: url-test", urlHarnessOpts{})
	require.NoError(t, err)
	assert.Equal(t, "url-test", w.DispatchAgent)
}

func TestGivenURLSourcedCustomHarness_URLFormat(t *testing.T) {
	stubRawHTTPClient(t)
	content := "agent: agents/triage.md\nrole: triage\nslug: url-test"
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "url-test", content, urlHarnessOpts{})
	require.NoError(t, err)

	// Verify the harness was committed to the hosting repo, not the config repo.
	harnessData := scm.files["harness/url-test.yaml"]
	require.NotNil(t, harnessData, "harness should be committed to hosting repo")
	assert.Equal(t, content, string(harnessData))

	// Verify the config was updated with the correct URL source pointing
	// to the hosting repo.
	cfgData := scm.files[".fullsend/config.yaml"]
	expectedURL := fmt.Sprintf("https://raw.githubusercontent.com/my-org/harness-host/main/harness/url-test.yaml#sha256=%s", expectedHash)
	assert.Contains(t, string(cfgData), expectedURL)

	// Verify the allowlist was updated with the hosting repo prefix.
	assert.Contains(t, string(cfgData), "https://raw.githubusercontent.com/my-org/harness-host/")
}

func TestGivenURLSourcedCustomHarness_BadHash(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "bad-hash", "agent: agents/triage.md\nrole: triage\nslug: bad", urlHarnessOpts{badHash: true})
	require.NoError(t, err)

	cfgData := scm.files[".fullsend/config.yaml"]
	// The hash should be all zeros (wrong), not the real hash.
	assert.Contains(t, string(cfgData), "#sha256=0000000000000000000000000000000000000000000000000000000000000000")
}

func TestGivenURLSourcedCustomHarness_SkipAllowlist(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "no-allow", "agent: agents/triage.md\nrole: triage\nslug: no-allow", urlHarnessOpts{skipAllowlist: true})
	require.NoError(t, err)

	// Parse the config and verify the allowlist directly.
	cfgData := scm.files[".fullsend/config.yaml"]
	cfg, parseErr := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, parseErr)

	// The hosting repo URL prefix should NOT be in the allowlist.
	hostPrefix := "https://raw.githubusercontent.com/my-org/harness-host/"
	assert.NotContains(t, cfg.AllowedRemoteResources, hostPrefix)
	// The default fullsend-ai prefix should still be there.
	assert.Contains(t, cfg.AllowedRemoteResources, "https://raw.githubusercontent.com/fullsend-ai/fullsend/")
	// But the URL source should still be registered in agents.
	require.Len(t, cfg.Agents, 1)
	assert.Contains(t, cfg.Agents[0].Source, hostPrefix)
}

func TestGivenURLSourcedCustomHarness_UpdatesExistingAgent(t *testing.T) {
	stubRawHTTPClient(t)
	// When an agent with the same name already exists in config, the entry
	// should be updated in-place rather than appended as a duplicate.
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents:\n  - name: url-test\n    source: harness/url-test.yaml\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "url-test", "agent: agents/triage.md\nrole: triage\nslug: url-test", urlHarnessOpts{})
	require.NoError(t, err)

	cfgData := scm.files[".fullsend/config.yaml"]
	cfg, parseErr := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, parseErr)

	// Should have exactly one agent (updated, not duplicated).
	require.Len(t, cfg.Agents, 1)
	assert.Contains(t, cfg.Agents[0].Source, "https://raw.githubusercontent.com/")
}

func TestGivenURLSourcedCustomHarness_AllowlistDedup(t *testing.T) {
	stubRawHTTPClient(t)
	// When the hosting repo URL prefix is already in the allowlist,
	// it should not be added again.
	hostPrefix := "https://raw.githubusercontent.com/my-org/harness-host/"
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte(fmt.Sprintf("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n  - %q\n", hostPrefix)),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "agent1", "agent: agents/triage.md\nrole: triage\nslug: agent1", urlHarnessOpts{})
	require.NoError(t, err)

	cfgData := scm.files[".fullsend/config.yaml"]
	cfg, parseErr := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, parseErr)

	// Count occurrences of the host prefix — should appear exactly once.
	count := 0
	for _, res := range cfg.AllowedRemoteResources {
		if res == hostPrefix {
			count++
		}
	}
	assert.Equal(t, 1, count, "allowlist prefix should not be duplicated")
}

func TestGivenHarnessHostingRepo_CreateRepoError(t *testing.T) {
	scm := &fakeURLSCM{
		files:         map[string][]byte{},
		repos:         map[string]bool{},
		createRepoErr: fmt.Errorf("permission denied"),
	}
	w := &world.World{
		Org: "test-org",
		SCM: scm,
	}
	err := givenHarnessHostingRepo(w, "my-host-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating harness-hosting repo")
	assert.Contains(t, err.Error(), "permission denied")
}

func TestGivenURLSourcedCustomHarness_CommitHarnessError(t *testing.T) {
	scm := &fakeURLSCM{
		files:          map[string][]byte{".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n")},
		commitFileErr:  fmt.Errorf("commit failed"),
		commitFileRepo: "harness-host", // only fail for hosting repo commits
	}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}
	err := givenURLSourcedCustomHarness(w, "agent1", "content", urlHarnessOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing harness to hosting repo")
}

func TestGivenURLSourcedCustomHarness_GetConfigError(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{}} // no config file
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}
	err := givenURLSourcedCustomHarness(w, "agent1", "content", urlHarnessOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestGivenURLSourcedCustomHarness_NonMainDefaultBranch(t *testing.T) {
	stubRawHTTPClient(t)
	content := "agent: agents/triage.md\nrole: triage\nslug: url-test"
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	scm := &fakeURLSCM{
		files: map[string][]byte{
			".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
		},
		defaultBranch: "master",
	}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "url-test", content, urlHarnessOpts{})
	require.NoError(t, err)

	cfgData := scm.files[".fullsend/config.yaml"]
	// The URL must use "master" (the actual default branch), not "main".
	expectedURL := fmt.Sprintf("https://raw.githubusercontent.com/my-org/harness-host/master/harness/url-test.yaml#sha256=%s", expectedHash)
	assert.Contains(t, string(cfgData), expectedURL)
	assert.NotContains(t, string(cfgData), "/main/harness/")
}

func TestGivenURLSourcedCustomHarness_GetDefaultBranchError(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{
		files: map[string][]byte{
			".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
		},
		defaultBranchErr: fmt.Errorf("API rate limited"),
	}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "url-test", "content", urlHarnessOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting default branch")
	assert.Contains(t, err.Error(), "API rate limited")
}

func TestGivenURLSourcedCustomHarness_RawURLNotAccessible(t *testing.T) {
	// Simulate raw URL returning 404 (e.g., repo is private despite
	// Contents API succeeding with an auth token).
	stubRawHTTPClientStatus(t, http.StatusNotFound)
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install:             &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:                 scm,
		URLHarnessRepoOwner: "my-org",
		URLHarnessRepoName:  "harness-host",
	}

	err := givenURLSourcedCustomHarness(w, "url-test", "content", urlHarnessOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "raw URL not accessible")
}

func TestVerifyRawURLAccessible_Success(t *testing.T) {
	stubRawHTTPClient(t)
	err := verifyRawURLAccessible("https://raw.githubusercontent.com/org/repo/main/file.yaml#sha256=abc123")
	require.NoError(t, err)
}

func TestVerifyRawURLAccessible_NotFound(t *testing.T) {
	stubRawHTTPClientStatus(t, http.StatusNotFound)
	err := verifyRawURLAccessible("https://raw.githubusercontent.com/org/repo/main/file.yaml#sha256=abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not accessible after")
	assert.Contains(t, err.Error(), "status 404")
}

func TestVerifyRawURLAccessible_Forbidden(t *testing.T) {
	stubRawHTTPClientStatus(t, http.StatusForbidden)
	err := verifyRawURLAccessible("https://raw.githubusercontent.com/org/repo/main/file.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 403")
}

func TestVerifyRawURLAccessible_StripsFragment(t *testing.T) {
	// Verify the fragment is stripped before the request.
	var capturedURL string
	orig := rawHTTPClient
	rawHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			capturedURL = r.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       http.NoBody,
			}, nil
		}),
	}
	t.Cleanup(func() { rawHTTPClient = orig })

	err := verifyRawURLAccessible("https://raw.githubusercontent.com/org/repo/main/file.yaml#sha256=abc")
	require.NoError(t, err)
	assert.NotContains(t, capturedURL, "#sha256=")
	assert.Contains(t, capturedURL, "file.yaml")
}

func TestWaitForFileAccessible_ImmediateSuccess(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"harness/test.yaml": []byte("content"),
	}}
	w := &world.World{SCM: scm}
	err := waitForFileAccessible(context.Background(), w, "org", "repo", "harness/test.yaml")
	require.NoError(t, err)
}

func TestWaitForFileAccessible_FileNotFound(t *testing.T) {
	speedUpRetries(t)
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{SCM: scm}
	err := waitForFileAccessible(context.Background(), w, "org", "repo", "harness/missing.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not accessible after")
	assert.Contains(t, err.Error(), "5 attempts")
}

// --- fakes ---

type fakeURLInstall struct {
	owner string
	repo  string
}

func (f *fakeURLInstall) Mode() string               { return "per-repo" }
func (f *fakeURLInstall) TestRepo() string           { return f.repo }
func (f *fakeURLInstall) ConfigOwner() string        { return f.owner }
func (f *fakeURLInstall) ConfigRepo() string         { return f.repo }
func (f *fakeURLInstall) ConfigPathPrefix() string   { return ".fullsend" }
func (f *fakeURLInstall) TriageWorkflowRepo() string { return f.repo }
func (f *fakeURLInstall) TriageWorkflowFile() string { return "fullsend.yaml" }
func (f *fakeURLInstall) AgentWorkflowFile() string  { return "reusable-triage.yml" }
func (f *fakeURLInstall) AgentArtifactName() string  { return "fullsend-triage" }

type fakeURLSCM struct {
	files              map[string][]byte
	repos              map[string]bool
	createRepoErr      error
	commitFileErr      error
	commitFileRepo     string // only return commitFileErr when repo matches
	ensurePublicErr    error
	ensurePublicCalled bool
	defaultBranch      string // returned by GetDefaultBranch; defaults to "main"
	defaultBranchErr   error
}

func (f *fakeURLSCM) CommitFile(_ context.Context, _, repo, path, _ string, content []byte) error {
	if f.commitFileErr != nil && (f.commitFileRepo == "" || f.commitFileRepo == repo) {
		return f.commitFileErr
	}
	f.files[path] = content
	return nil
}

func (f *fakeURLSCM) GetFileContent(_ context.Context, _, _, path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return data, nil
}

func (f *fakeURLSCM) CreateRepo(_ context.Context, _, name, _ string) error {
	if f.createRepoErr != nil {
		return f.createRepoErr
	}
	if f.repos == nil {
		f.repos = map[string]bool{}
	}
	f.repos[name] = true
	return nil
}

func (f *fakeURLSCM) EnsureRepoPublic(_ context.Context, _, _ string) error {
	f.ensurePublicCalled = true
	return f.ensurePublicErr
}

func (f *fakeURLSCM) GetDefaultBranch(_ context.Context, _, _ string) (string, error) {
	if f.defaultBranchErr != nil {
		return "", f.defaultBranchErr
	}
	if f.defaultBranch != "" {
		return f.defaultBranch, nil
	}
	return "main", nil
}

func (f *fakeURLSCM) DeleteRepo(_ context.Context, _, repo string) error {
	delete(f.repos, repo)
	return nil
}

// Unused SCM methods — satisfy the interface.
func (f *fakeURLSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}
func (f *fakeURLSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}
func (f *fakeURLSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}
func (f *fakeURLSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}
func (f *fakeURLSCM) CreateBranch(context.Context, string, string, string) error { return nil }
func (f *fakeURLSCM) DeleteBranch(context.Context, string, string, string) error { return nil }
func (f *fakeURLSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (f *fakeURLSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (f *fakeURLSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}
func (f *fakeURLSCM) CloseIssue(context.Context, string, string, int) error { return nil }
func (f *fakeURLSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeURLSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (f *fakeURLSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
