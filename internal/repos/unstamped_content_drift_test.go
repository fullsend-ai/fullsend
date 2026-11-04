package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

const unstampedRunnerTag = "fullsend-gitlab-runner"

// newUnstampedGitLabRepo seeds a GitLab repository installed by a build
// without a release ref and without gitlab.fullsend_ref: the scaffold is
// rendered at the default ref (no version marker) with no runner tags.
func newUnstampedGitLabRepo(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	populateGitLabScaffoldContent(t, fc, "acme", "api", "")
	populateGitLabTypedRoot(t, fc, "acme", "api", fc.FileContents["acme/api/"+fullsendPipelineInclude])
	return fc
}

// unstampedTaggedConvergeCfg is a convergence of a manifest without
// gitlab.fullsend_ref by a build without a release ref (empty UpstreamRef)
// whose manifest now sets agent and control runner tags.
func unstampedTaggedConvergeCfg() ConvergeConfig {
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.GitLab.FullsendRef = ""
	cfg.Manifest.GitLab.AgentRunnerTags = []string{unstampedRunnerTag}
	cfg.Manifest.GitLab.ControlRunnerTags = []string{unstampedRunnerTag}
	return cfg
}

func committedScaffold(sc *spyScaffoldCommit) map[string][]byte {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make(map[string][]byte)
	for _, f := range sc.files {
		if !f.Delete {
			out[f.Path] = f.Content
		}
	}
	return out
}

// TestConverge_GitLab_UnstampedInstallAppliesRunnerTags guards #8689:
// re-running install with a build that has no release ref and a manifest
// without gitlab.fullsend_ref must still converge runner-tag changes into
// the installed templates, and a second run must be a no-op.
func TestConverge_GitLab_UnstampedInstallAppliesRunnerTags(t *testing.T) {
	fc := newUnstampedGitLabRepo(t)
	cfg := unstampedTaggedConvergeCfg()

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	committed := committedScaffold(sc)
	for _, path := range []string{".gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-poll.yml"} {
		content, ok := committed[path]
		require.True(t, ok, "%s not converged; committed: %v", path, committed)
		assert.Contains(t, string(content), unstampedRunnerTag, "%s must carry the manifest runner tags", path)
	}
	// The default ref is kept: the wrapper gains no version marker.
	if wrapper, ok := committed[fullsendPipelineInclude]; ok {
		assert.NotContains(t, string(wrapper), "fullsend-ref:")
	}

	// Apply the commit and converge again: nothing further to change.
	for path, content := range committed {
		fc.FileContents["acme/api/"+path] = content
	}
	again := &spyScaffoldCommit{}
	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), again.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, committedScaffold(again), "second convergence must not rewrite converged templates")
}

// TestConverge_GitLab_PinnedInstallWithoutRefWarns verifies that when no
// target ref can be resolved and the installed scaffold is pinned to some
// other ref, convergence leaves the scaffold untouched (rendering at the
// default ref would repoint it) but says that content drift was not
// checked instead of skipping silently.
func TestConverge_GitLab_PinnedInstallWithoutRefWarns(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	populateGitLabScaffoldContent(t, fc, "acme", "api", "v2.5.0")
	populateGitLabTypedRoot(t, fc, "acme", "api", fc.FileContents["acme/api/"+fullsendPipelineInclude])
	cfg := unstampedTaggedConvergeCfg()

	var messages []string
	progress := func(_, _, msg string) { messages = append(messages, msg) }
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), progress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	committed := committedScaffold(sc)
	assert.NotContains(t, committed, ".gitlab/ci/fullsend-agent.yml")
	assert.Contains(t, strings.Join(messages, "\n"), "scaffold content drift not checked: gitlab.fullsend_ref is not set")
}

// TestConverge_GitLab_MainMarkedInstallKeepsRuntimeTarget verifies that a
// GitLab scaffold marked fullsend-ref: main (rendered with an explicit main
// ref and FULLSEND_VERSION=main) is not treated as unstamped: with neither
// manifest nor build refs, convergence must not re-render it with empty refs,
// which would drop the marker and switch the bootstrap script from a main
// source build to a latest-release download.
func TestConverge_GitLab_MainMarkedInstallKeepsRuntimeTarget(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	populateGitLabScaffoldContent(t, fc, "acme", "api", config.DefaultUpstreamRef)
	populateGitLabTypedRoot(t, fc, "acme", "api", fc.FileContents["acme/api/"+fullsendPipelineInclude])
	require.Contains(t, string(fc.FileContents["acme/api/"+fullsendPipelineInclude]), "fullsend-ref: "+config.DefaultUpstreamRef)
	cfg := unstampedTaggedConvergeCfg()

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	committed := committedScaffold(sc)
	for path, content := range committed {
		assert.NotContains(t, string(content), `FULLSEND_VERSION="latest"`, "%s must not switch to a latest-release download", path)
	}
	if wrapper, ok := committed[fullsendPipelineInclude]; ok {
		assert.Contains(t, string(wrapper), "fullsend-ref: "+config.DefaultUpstreamRef, "the main marker must be preserved")
	}
	assert.NotContains(t, committed, ".gitlab/ci/fullsend-agent.yml", "no baseline can be rendered without repointing the ref")
}

// TestStatus_GitLab_UnstampedInstallReportsRunnerTagDrift verifies that
// repos status reports the same runner-tag drift convergence repairs for
// an unstamped installation, and none once the templates match.
func TestStatus_GitLab_UnstampedInstallReportsRunnerTagDrift(t *testing.T) {
	fc := newUnstampedGitLabRepo(t)
	m := unstampedTaggedConvergeCfg().Manifest

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	require.Empty(t, status.Repos[0].Error)
	assert.Contains(t, driftFields(status.Repos[0].Drifts), ".gitlab/ci/fullsend-agent.yml")

	// Seed the templates with the manifest's tags: no content drift.
	files, err := BuildScaffoldFiles(InstallConfig{
		Owner:             "acme",
		Repo:              "api",
		Forge:             ForgeGitLab,
		Roles:             config.PerRepoDefaultRoles(),
		AgentRunnerTags:   m.GitLab.AgentRunnerTags,
		ControlRunnerTags: m.GitLab.ControlRunnerTags,
		ManagedConfig:     emptyManagedOverlay(t),
	})
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasPrefix(f.Path, ".gitlab/ci/") {
			fc.FileContents["acme/api/"+f.Path] = f.Content
		}
	}
	status, err = Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	assert.NotContains(t, driftFields(status.Repos[0].Drifts), ".gitlab/ci/fullsend-agent.yml")
}

func driftFields(drifts []Drift) []string {
	fields := make([]string, 0, len(drifts))
	for _, d := range drifts {
		fields = append(fields, d.Field)
	}
	return fields
}

func TestUnpinnedScaffoldContent(t *testing.T) {
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, ForgeConfig: GitLabForgeConfig()}

	files, err := unpinnedScaffoldContent(resolved, DriftConfig{}, false)
	require.NoError(t, err)
	assert.Nil(t, files, "no release ref and a pinned install: no baseline")

	files, err = unpinnedScaffoldContent(resolved, DriftConfig{}, true)
	require.NoError(t, err)
	assert.NotEmpty(t, files, "unstamped install renders at the default ref")

	files, err = unpinnedScaffoldContent(resolved, DriftConfig{UpstreamRef: "release-sha", UpstreamTag: "v1.0.0"}, false)
	require.NoError(t, err)
	require.NotEmpty(t, files, "a release build renders at its own ref")
	for _, f := range files {
		if f.Path == fullsendPipelineInclude {
			assert.Contains(t, string(f.Content), "fullsend-ref: release-sha (v1.0.0)")
		}
	}
}

func TestIsUnstampedInstallRef(t *testing.T) {
	cases := []struct {
		forge, ref string
		want       bool
	}{
		{ForgeGitHub, config.DefaultUpstreamRef, true},
		{ForgeGitHub, "", false}, // vendored callers carry no ref
		{ForgeGitHub, "v1.0.0", false},
		{ForgeGitLab, config.DefaultUpstreamRef, false}, // explicit main marker, not an unstamped wrapper
		{ForgeGitLab, "", true},                         // wrapper without a version marker
		{ForgeGitLab, "v2.5.0", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isUnstampedInstallRef(tc.forge, tc.ref), "%s %q", tc.forge, tc.ref)
	}
	assert.False(t, unstampedWorkflow(ForgeGitLab, []ComponentStatus{{Name: "workflow", Present: false}}),
		"a missing workflow has no known ref")
	assert.False(t, unstampedWorkflow(ForgeGitLab, nil))
}
