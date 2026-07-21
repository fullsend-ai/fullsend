package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestGivenURLSourcedCustomHarness_Validation(t *testing.T) {
	w := &world.World{}
	require.Error(t, givenURLSourcedCustomHarness(w, "", "doc", urlHarnessOpts{}))
	require.Error(t, givenURLSourcedCustomHarness(w, "agent", "", urlHarnessOpts{}))
}

func TestGivenURLSourcedCustomHarness_SetsDispatchAgent(t *testing.T) {
	w := &world.World{
		Install: &fakeURLInstall{owner: "test-org", repo: "test-repo"},
		SCM:     &fakeURLSCM{files: map[string][]byte{".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n")}},
	}
	err := givenURLSourcedCustomHarness(w, "url-test", "agent: agents/triage.md\nrole: triage\nslug: url-test", urlHarnessOpts{})
	require.NoError(t, err)
	assert.Equal(t, "url-test", w.DispatchAgent)
}

func TestGivenURLSourcedCustomHarness_URLFormat(t *testing.T) {
	content := "agent: agents/triage.md\nrole: triage\nslug: url-test"
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install: &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:     scm,
	}

	err := givenURLSourcedCustomHarness(w, "url-test", content, urlHarnessOpts{})
	require.NoError(t, err)

	// Verify the config was updated with the correct URL source.
	cfgData := scm.files[".fullsend/config.yaml"]
	expectedURL := fmt.Sprintf("https://raw.githubusercontent.com/my-org/my-repo/main/.fullsend/harness/url-test.yaml#sha256=%s", expectedHash)
	assert.Contains(t, string(cfgData), expectedURL)

	// Verify the allowlist was updated.
	assert.Contains(t, string(cfgData), "https://raw.githubusercontent.com/my-org/my-repo/")
}

func TestGivenURLSourcedCustomHarness_BadHash(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install: &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:     scm,
	}

	err := givenURLSourcedCustomHarness(w, "bad-hash", "agent: agents/triage.md\nrole: triage\nslug: bad", urlHarnessOpts{badHash: true})
	require.NoError(t, err)

	cfgData := scm.files[".fullsend/config.yaml"]
	// The hash should be all zeros (wrong), not the real hash.
	assert.Contains(t, string(cfgData), "#sha256=0000000000000000000000000000000000000000000000000000000000000000")
}

func TestGivenURLSourcedCustomHarness_SkipAllowlist(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		".fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/fullsend-ai/fullsend/\"\n"),
	}}
	w := &world.World{
		Install: &fakeURLInstall{owner: "my-org", repo: "my-repo"},
		SCM:     scm,
	}

	err := givenURLSourcedCustomHarness(w, "no-allow", "agent: agents/triage.md\nrole: triage\nslug: no-allow", urlHarnessOpts{skipAllowlist: true})
	require.NoError(t, err)

	// Parse the config and verify the allowlist directly.
	cfgData := scm.files[".fullsend/config.yaml"]
	cfg, parseErr := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, parseErr)

	// The pool org URL prefix should NOT be in the allowlist.
	poolPrefix := "https://raw.githubusercontent.com/my-org/my-repo/"
	assert.NotContains(t, cfg.AllowedRemoteResources, poolPrefix)
	// The default fullsend-ai prefix should still be there.
	assert.Contains(t, cfg.AllowedRemoteResources, "https://raw.githubusercontent.com/fullsend-ai/fullsend/")
	// But the URL source should still be registered in agents.
	require.Len(t, cfg.Agents, 1)
	assert.Contains(t, cfg.Agents[0].Source, poolPrefix)
}

func TestContainsPrefix(t *testing.T) {
	list := []string{"https://example.com/a/", "https://example.com/b/"}
	assert.True(t, containsPrefix(list, "https://example.com/a/"))
	assert.False(t, containsPrefix(list, "https://example.com/c/"))
	assert.False(t, containsPrefix(nil, "https://example.com/"))
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
	files map[string][]byte
}

func (f *fakeURLSCM) CommitFile(_ context.Context, _, _, path, _ string, content []byte) error {
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
