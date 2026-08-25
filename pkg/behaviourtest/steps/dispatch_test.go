package steps

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestGivenCustomHarness_Validation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenCustomHarness(w, "", "doc"))
	require.Error(t, givenCustomHarness(w, "agent", ""))
}

func TestDispatchSteps_RequireScenarioStart(t *testing.T) {
	w := &world.World{}
	require.Error(t, thenHarnessWorkflowCompletes(w, "agent"))
	require.Error(t, thenHarnessAgentDidNotRun(w, "agent"))
}

func TestDispatchSteps_RequirePullRequest(t *testing.T) {
	w := &world.World{ScenarioStart: time.Now()}
	require.Error(t, whenPullRequestLabeled(w, "label"))
	require.Error(t, whenPullRequestReviewComment(w))
}

func TestEnsureHarnessArtifacts_NoWorkflowRun(t *testing.T) {
	w := &world.World{ScenarioStart: time.Now()}
	err := ensureHarnessArtifacts(w, "agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow run")
}

// --- givenKillSwitchActive tests ---

func TestGivenKillSwitchActive_SetsKillSwitch(t *testing.T) {
	scm := &fakeDispatchSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\n"),
	}
	w := &world.World{
		SCM:      scm,
		Org:      "org",
		RepoName: "repo",
	}
	err := givenKillSwitchActive(w)
	require.NoError(t, err)
	assert.True(t, scm.commitCalled, "CommitFile should have been called")
	assert.Contains(t, string(scm.committedContent), "kill_switch: true")
	assert.True(t, w.KillSwitchActivated, "KillSwitchActivated should be set for cleanup")
}

func TestDeactivateKillSwitch_ClearsKillSwitch(t *testing.T) {
	scm := &fakeDispatchSCM{
		fileContent: []byte("version: \"1\"\nkill_switch: true\nroles:\n  - triage\n"),
	}
	w := &world.World{
		SCM:      scm,
		Org:      "org",
		RepoName: "repo",
	}
	err := DeactivateKillSwitch(w)
	require.NoError(t, err)
	assert.True(t, scm.commitCalled, "CommitFile should have been called")
	assert.Contains(t, string(scm.committedContent), "kill_switch: false")
}

func TestDeactivateKillSwitch_GetFileContentError(t *testing.T) {
	scm := &fakeDispatchSCM{
		getFileErr: fmt.Errorf("not found"),
	}
	w := &world.World{
		SCM:      scm,
		Org:      "org",
		RepoName: "repo",
	}
	err := DeactivateKillSwitch(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestDeactivateKillSwitch_CommitFileError(t *testing.T) {
	scm := &fakeDispatchSCM{
		fileContent: []byte("version: \"1\"\nkill_switch: true\nroles:\n  - triage\n"),
		commitErr:   fmt.Errorf("commit failed"),
	}
	w := &world.World{
		SCM:      scm,
		Org:      "org",
		RepoName: "repo",
	}
	err := DeactivateKillSwitch(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating config")
}

func TestGivenKillSwitchActive_GetFileContentError(t *testing.T) {
	scm := &fakeDispatchSCM{
		getFileErr: fmt.Errorf("not found"),
	}
	w := &world.World{
		SCM:      scm,
		Org:      "org",
		RepoName: "repo",
	}
	err := givenKillSwitchActive(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestGivenKillSwitchActive_CommitFileError(t *testing.T) {
	scm := &fakeDispatchSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\n"),
		commitErr:   fmt.Errorf("commit failed"),
	}
	w := &world.World{
		SCM:      scm,
		Org:      "org",
		RepoName: "repo",
	}
	err := givenKillSwitchActive(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating config")
}

// --- empty identity guard tests ---

func TestGivenKillSwitchActive_EmptyIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		org  string
		repo string
	}{
		{"empty org", "", "repo"},
		{"empty repo", "org", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &world.World{Org: tc.org, RepoName: tc.repo, SCM: &fakeDispatchSCM{}}
			err := givenKillSwitchActive(w)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no repo configured")
		})
	}
}

func TestDeactivateKillSwitch_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeDispatchSCM{}}
	err := DeactivateKillSwitch(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenCustomHarness_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeDispatchSCM{}}
	err := givenCustomHarness(w, "test", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenDisabledCustomHarness_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeDispatchSCM{}}
	err := givenDisabledCustomHarness(w, "test", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestCommitLocalHarnessResources_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeDispatchSCM{}}
	err := commitLocalHarnessResources(context.Background(), w, "test", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

// fakeDispatchSCM implements scm.Driver for dispatch step tests.
type fakeDispatchSCM struct {
	fileContent      []byte
	getFileErr       error
	commitCalled     bool
	committedContent []byte
	commitErr        error
}

func (f *fakeDispatchSCM) GetFileContent(_ context.Context, _, _, _ string) ([]byte, error) {
	return f.fileContent, f.getFileErr
}
func (f *fakeDispatchSCM) CommitFile(_ context.Context, _, _, _, _ string, content []byte) error {
	f.commitCalled = true
	f.committedContent = content
	return f.commitErr
}
func (f *fakeDispatchSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}
func (f *fakeDispatchSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) CreateBranch(context.Context, string, string, string) error { return nil }
func (f *fakeDispatchSCM) DeleteBranch(context.Context, string, string, string) error { return nil }
func (f *fakeDispatchSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (f *fakeDispatchSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}
func (f *fakeDispatchSCM) CloseIssue(context.Context, string, string, int) error { return nil }
func (f *fakeDispatchSCM) DeleteRepo(context.Context, string, string) error      { return nil }
func (f *fakeDispatchSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeDispatchSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (f *fakeDispatchSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) CreateRepo(context.Context, string, string, string) error { return nil }
func (f *fakeDispatchSCM) EnsureRepoPublic(context.Context, string, string) error   { return nil }
func (f *fakeDispatchSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}
func (f *fakeDispatchSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}
func (f *fakeDispatchSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "abc123", nil
}
func (f *fakeDispatchSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}

func TestNegativeSettleDuration(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		world    *world.World
		now      time.Time
		wantZero bool          // true if settle should be skipped
		wantDur  time.Duration // exact expected duration (checked when wantZero is false)
	}{
		{
			name: "WorkflowRun set — skip settle",
			world: &world.World{
				ScenarioStart: now.Add(-30 * time.Second),
				WorkflowRun:   &forge.WorkflowRun{ID: 1},
			},
			now:      now,
			wantZero: true,
		},
		{
			name: "standalone negative — full settle",
			world: &world.World{
				ScenarioStart: now,
			},
			now:     now,
			wantDur: defaultSettleDuration,
		},
		{
			name:    "ScenarioStart zero — full settle (safety)",
			world:   &world.World{},
			now:     now,
			wantDur: defaultSettleDuration,
		},
		{
			name: "partial elapsed — remaining settle",
			world: &world.World{
				ScenarioStart: now.Add(-60 * time.Second),
			},
			now:     now,
			wantDur: 30 * time.Second,
		},
		{
			name: "elapsed >= settle budget — skip settle",
			world: &world.World{
				ScenarioStart: now.Add(-90 * time.Second),
			},
			now:      now,
			wantZero: true,
		},
		{
			name: "elapsed > settle budget — skip settle",
			world: &world.World{
				ScenarioStart: now.Add(-120 * time.Second),
			},
			now:      now,
			wantZero: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := negativeSettleDuration(tc.world, tc.now)
			if tc.wantZero {
				assert.Equal(t, time.Duration(0), got)
			} else {
				assert.Equal(t, tc.wantDur, got)
			}
		})
	}
}

func TestGivenCustomHarness_CommitsAgentFile(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"test-org/test-repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:      "test-org",
		RepoName: "test-repo",
		SCM:      scm,
	}
	err := givenCustomHarness(w, "local-test", "agent: agents/triage.md\nrole: triage\nslug: local-test")
	require.NoError(t, err)

	// The agent resource should be committed under .fullsend/ on the config repo.
	agentData := scm.files["test-org/test-repo/.fullsend/agents/triage.md"]
	require.NotNil(t, agentData, "agent resource should be committed to config repo")
	assert.Equal(t, minimalAgentContent, string(agentData))

	// The harness YAML should also be committed.
	harnessData := scm.files["test-org/test-repo/.fullsend/harness/local-test.yaml"]
	require.NotNil(t, harnessData, "harness YAML should be committed")
}

func TestGivenCustomHarness_CommitsAgentAndPolicy(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"test-org/test-repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:      "test-org",
		RepoName: "test-repo",
		SCM:      scm,
	}
	err := givenCustomHarness(w, "local-test", "agent: agents/test.md\npolicy: policies/test.md\nrole: triage\nslug: local-test")
	require.NoError(t, err)

	agentData := scm.files["test-org/test-repo/.fullsend/agents/test.md"]
	require.NotNil(t, agentData, "agent resource should be committed")
	assert.Equal(t, minimalAgentContent, string(agentData))

	policyData := scm.files["test-org/test-repo/.fullsend/policies/test.md"]
	require.NotNil(t, policyData, "policy resource should be committed")
	assert.Contains(t, string(policyData), "Minimal policy")
}

func TestGivenCustomHarness_SkipsAbsoluteAgentPath(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"test-org/test-repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:      "test-org",
		RepoName: "test-repo",
		SCM:      scm,
	}
	err := givenCustomHarness(w, "local-test", "agent: https://example.com/agent.md\nrole: triage\nslug: local-test")
	require.NoError(t, err)

	// No extra agent file should be committed — only harness YAML and config.
	for key := range scm.files {
		assert.NotContains(t, key, "agent.md", "URL agent paths should not be committed as files")
	}
}

func TestGivenDisabledCustomHarness_CommitsAgentFile(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"test-org/test-repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:      "test-org",
		RepoName: "test-repo",
		SCM:      scm,
	}
	err := givenDisabledCustomHarness(w, "disabled-test", "agent: agents/triage.md\nrole: triage\nslug: disabled-test")
	require.NoError(t, err)

	agentData := scm.files["test-org/test-repo/.fullsend/agents/triage.md"]
	require.NotNil(t, agentData, "agent resource should be committed for disabled harness")
	assert.Equal(t, minimalAgentContent, string(agentData))
}

func TestCommitLocalHarnessResources_CommitsAgentFile(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	err := commitLocalHarnessResources(context.Background(), w, "test",
		"agent: agents/triage.md\nrole: triage")
	require.NoError(t, err)
	assert.Equal(t, minimalAgentContent, string(scm.files["org/repo/.fullsend/agents/triage.md"]))
}

func TestCommitLocalHarnessResources_SkipsURLAgentPath(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	err := commitLocalHarnessResources(context.Background(), w, "test",
		"agent: https://example.com/agents/triage.md\nrole: triage")
	require.NoError(t, err)
	assert.Empty(t, scm.files, "URL agent paths should not be committed")
}

func TestCommitLocalHarnessResources_SkipsAbsoluteAgentPath(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	err := commitLocalHarnessResources(context.Background(), w, "test",
		"agent: /absolute/agents/triage.md\nrole: triage")
	require.NoError(t, err)
	assert.Empty(t, scm.files, "absolute agent paths should not be committed")
}

func TestCommitLocalHarnessResources_NoAgentField(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	err := commitLocalHarnessResources(context.Background(), w, "test",
		"role: triage\nslug: test")
	require.NoError(t, err)
	assert.Empty(t, scm.files, "no files should be committed without agent field")
}

func TestCommitLocalHarnessResources_InvalidYAML(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	err := commitLocalHarnessResources(context.Background(), w, "test",
		"invalid: [yaml: content")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing harness YAML")
}

func TestCommitLocalHarnessResources_CommitsScaffoldProfilesAndProviders(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := commitLocalHarnessResources(context.Background(), w, "pi-override",
		"agent: agents/pi-override.md\nrole: triage\nprofiles:\n  - profiles/fullsend-vertex-ai.yaml\nproviders:\n  - providers/vertex-ai.yaml\n")
	require.NoError(t, err)

	// The real scaffold files are committed, not placeholders: these grant
	// the sandbox its Vertex egress.
	profile := string(scm.files["org/repo/.fullsend/profiles/fullsend-vertex-ai.yaml"])
	assert.Contains(t, profile, "*.googleapis.com")
	provider := string(scm.files["org/repo/.fullsend/providers/vertex-ai.yaml"])
	assert.Contains(t, provider, "type: fullsend-vertex-ai")

	// An entry the scaffold does not ship is an error, not a silent gap.
	err = commitLocalHarnessResources(context.Background(), w, "bad",
		"agent: agents/bad.md\nrole: triage\nproviders:\n  - providers/nope.yaml\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "providers/nope.yaml")

	// URL entries are left to the harness resolver.
	scm2 := &fakeURLSCM{files: map[string][]byte{}}
	w2 := &world.World{Org: "org", RepoName: "repo", SCM: scm2}
	require.NoError(t, commitLocalHarnessResources(context.Background(), w2, "url",
		"agent: https://example.com/a.md\nrole: triage\nprofiles:\n  - https://example.com/p.yaml#sha256=abc\n"))
	assert.Empty(t, scm2.files)
}
