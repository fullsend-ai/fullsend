package suite

import (
	"context"
	"fmt"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeCleanupSCM records DeleteRepo calls for cleanup tests.
type fakeCleanupSCM struct {
	scm.Driver
	deletedRepos []string
	deleteErr    error
}

func (f *fakeCleanupSCM) DeleteRepo(_ context.Context, owner, repo string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedRepos = append(f.deletedRepos, owner+"/"+repo)
	return nil
}

// panickingSCM is a fake scm.Driver whose DeleteRepo panics.
// Used to verify that afterScenario handles cleanup panics.
type panickingSCM struct {
	scm.Driver
}

func (p *panickingSCM) DeleteRepo(context.Context, string, string) error {
	panic("simulated cleanup panic in DeleteRepo")
}

// fakeDriver is a minimal install.Driver for unit testing suite hooks.
type fakeDriver struct {
	created []string
	marked  []string
}

func (f *fakeDriver) CreateRepo(_ context.Context, hint string) (string, error) {
	name := fmt.Sprintf("bt-fake-%s", hint)
	f.created = append(f.created, name)
	return name, nil
}

func (f *fakeDriver) MarkDeleted(repoName string) {
	f.marked = append(f.marked, repoName)
}

func (f *fakeDriver) Finalize(_ context.Context) error { return nil }
func (f *fakeDriver) DefaultConcurrency() int          { return 4 }

var _ install.Driver = (*fakeDriver)(nil)

func TestTagNames(t *testing.T) {
	names := tagNames([]*messages.PickleTag{{Name: "@foo"}, {Name: "@bar"}})
	assert.Equal(t, []string{"@foo", "@bar"}, names)
}

func TestResetScenarioWorld_ClearsSharedState(t *testing.T) {
	w := &world.World{
		PRNumber:            99,
		DispatchAgent:       "dispatch",
		IssueNumber:         1,
		ArtifactDir:         "/tmp/x",
		ForkOwner:           "org",
		ForkRepo:            "repo-fork",
		ForkPRNumber:        42,
		ForkPRBranch:        "branch",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "harness-host",
	}
	resetScenarioWorld(w)
	assert.Equal(t, 0, w.PRNumber)
	assert.Equal(t, "", w.DispatchAgent)
	assert.Equal(t, 0, w.IssueNumber)
	assert.Equal(t, "", w.ArtifactDir)
	assert.False(t, w.ScenarioStart.IsZero())
	assert.Equal(t, "", w.ForkOwner)
	assert.Equal(t, "", w.ForkRepo)
	assert.Equal(t, 0, w.ForkPRNumber)
	assert.Equal(t, "", w.ForkPRBranch)
	assert.Equal(t, "", w.URLHarnessRepoOwner)
	assert.Equal(t, "", w.URLHarnessRepoName)
}

func TestSkipErrorForTagNames(t *testing.T) {
	w := &world.World{Config: env.RunnerConfig{SCM: "github"}}

	tests := []struct {
		name    string
		tags    []string
		wantErr error
		cfg     env.RunnerConfig
	}{
		{name: "no tags", tags: nil, wantErr: nil},
		{name: "skip gitlab on github", tags: []string{"@skip:gitlab"}, wantErr: nil},
		{name: "skip gitlab on gitlab", tags: []string{"@skip:gitlab"}, wantErr: godog.ErrSkip, cfg: env.RunnerConfig{SCM: "gitlab"}},
		{name: "requires capability undeclared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: godog.ErrSkip},
		{name: "requires capability declared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: nil,
			cfg: env.RunnerConfig{SCM: "github", Capabilities: []string{"applier-branch-namespace"}}},
		{name: "requires capability other declared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: godog.ErrSkip,
			cfg: env.RunnerConfig{SCM: "github", Capabilities: []string{"something-else"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ww := w
			if tt.cfg.SCM != "" {
				ww = &world.World{Config: tt.cfg}
			}
			err := SkipErrorForTagNames(tt.tags, ww)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSkipErrorForTagNames_MalformedCapabilityTag(t *testing.T) {
	w := &world.World{Config: env.RunnerConfig{SCM: "github"}}
	err := SkipErrorForTagNames([]string{"@requires:capability:"}, w)
	require.Error(t, err)
	assert.NotErrorIs(t, err, godog.ErrSkip, "an empty capability name is a tag-authoring mistake, not a normal skip")
	assert.Contains(t, err.Error(), "needs a name")
}

// --- Before/After hook tests ---

func TestBeforeScenario_ClonesAndResetsWorld(t *testing.T) {
	template := &world.World{
		Org:         "test-org",
		RepoName:    "test-repo",
		IssueNumber: 42, // scenario field — should be zeroed by reset
	}

	scenario := &godog.Scenario{Name: "my scenario"}
	ctx, err := beforeScenario(context.Background(), scenario, nil, template)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.NotSame(t, template, w)
	assert.Equal(t, "test-org", w.Org)
	assert.Equal(t, "", w.RepoName, "scenario fields should be zeroed")
	assert.Equal(t, 0, w.IssueNumber, "scenario fields should be zeroed")
	assert.False(t, w.ScenarioStart.IsZero(), "ScenarioStart should be set")
	assert.Equal(t, "my scenario", w.ScenarioName)
}

func TestBeforeScenario_NilDriver(t *testing.T) {
	template := &world.World{Org: "test-org"}
	scenario := &godog.Scenario{Name: "nil driver scenario"}

	ctx, err := beforeScenario(context.Background(), scenario, nil, template)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.Equal(t, "", w.RepoName, "no driver → no repo name")
	assert.Equal(t, "nil driver scenario", w.ScenarioName)
}

func TestAfterScenario_NilWorld(t *testing.T) {
	origErr := godog.ErrSkip
	ctx := context.Background()

	_, err := afterScenario(ctx, origErr)
	assert.Equal(t, origErr, err, "original error should be preserved")
}

// TODO: restore deletion tests after reverting keepRepos hardcode.
// The following tests are temporarily simplified because CleanupScenario
// is hardcoded to return nil (keep all repos for debugging).

func TestAfterScenario_NoErrorWhenCleanupSkipped(t *testing.T) {
	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		SCM:       scmDriver,
		Driver:    &fakeDriver{},
		RepoOwner: "test-org",
		RepoName:  "bt-fake-test",
		Org:       "test-org",
	}
	ctx := world.WithWorld(context.Background(), w)

	_, err := afterScenario(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, scmDriver.deletedRepos, "cleanup is temporarily disabled")
}

func TestAfterScenario_PreservesOriginalError(t *testing.T) {
	w := &world.World{
		SCM:       &fakeCleanupSCM{},
		RepoOwner: "test-org",
		RepoName:  "bt-fake-test",
		Org:       "test-org",
		Driver:    &fakeDriver{},
	}
	ctx := world.WithWorld(context.Background(), w)

	origErr := assert.AnError
	_, err := afterScenario(ctx, origErr)
	assert.Equal(t, origErr, err)
}
