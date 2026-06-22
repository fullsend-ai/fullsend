package scaffold

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderThinCallerNotVendored(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/triage.yml", raw, RenderOptions{
		Vendored: false,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: fullsend-ai/fullsend/.github/workflows/reusable-triage.yml@v0")
	assertFreeOfRenderPlaceholders(t, out)
	assert.NotContains(t, out, "distribution_mode")
	assert.NotContains(t, out, "fullsend_ai_repo:")
}

func TestRenderThinCallerVendoredPerOrg(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/triage.yml", raw, RenderOptions{
		Vendored: true,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: ./.github/workflows/reusable-triage.yml")
	assertFreeOfRenderPlaceholders(t, out)
	assert.NotContains(t, out, "distribution_mode")
	assert.Contains(t, out, "install_mode: per-org")
}

func TestRenderPerRepoShimVendored(t *testing.T) {
	raw, err := PerRepoShimTemplate()
	require.NoError(t, err)

	rendered, err := RenderTemplate("templates/shim-per-repo.yaml", raw, RenderOptions{
		Vendored: true,
		PerRepo:  true,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: ./.fullsend/.github/workflows/reusable-dispatch.yml")
	assert.NotContains(t, out, "distribution_mode")
}

func TestRenderPrioritizeThinCallerVendored(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{
		Vendored: true,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: ./.github/workflows/reusable-prioritize.yml")
	assert.NotContains(t, out, "distribution_mode")
	assert.Contains(t, out, "project_number: ${{ vars.FULLSEND_PROJECT_NUMBER }}")
}

func TestWalkUpstreamIncludesReusableWorkflows(t *testing.T) {
	var paths []string
	err := WalkUpstream(func(path string, _ []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)

	for _, want := range []string{
		".github/workflows/reusable-triage.yml",
		".github/workflows/reusable-prioritize.yml",
		".github/workflows/reusable-dispatch.yml",
		".github/actions/mint-token/action.yml",
		"action.yml",
	} {
		assert.Contains(t, paths, want)
	}
}

func TestRenderDispatchPerRepoStagePaths(t *testing.T) {
	var raw []byte
	err := WalkUpstream(func(path string, content []byte) error {
		if path == ".github/workflows/reusable-dispatch.yml" {
			raw = content
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	rendered := RenderDispatchPerRepoStagePaths(raw)
	assert.Contains(t, string(rendered), "uses: ./.fullsend/.github/workflows/reusable-triage.yml")
	assert.Contains(t, string(rendered), "uses: ./.fullsend/.github/workflows/reusable-prioritize.yml")
	assert.NotContains(t, string(rendered), "uses: fullsend-ai/fullsend/.github/workflows/reusable-triage.yml@v0")
}

func assertFreeOfRenderPlaceholders(t *testing.T, out string) {
	t.Helper()
	for _, placeholder := range []string{
		"__REUSABLE_WORKFLOW__",
		"__REUSABLE_DISPATCH__",
		"__UPSTREAM_REF__",
		"__DISTRIBUTION_MODE__",
	} {
		assert.NotContains(t, out, placeholder)
	}
}

func TestRenderDispatchPerRepoStagePathsIgnoresOtherRepos(t *testing.T) {
	input := []byte("uses: evil-org/evil-repo/.github/workflows/reusable-triage.yml@v0\n")
	rendered := RenderDispatchPerRepoStagePaths(input)
	assert.Equal(t, string(input), string(rendered))
}

func TestThinStageWorkflowRegistryMatchesTemplates(t *testing.T) {
	for _, w := range thinStageWorkflows {
		raw, err := FullsendRepoFile(w.path)
		require.NoError(t, err, w.path)
		assert.Contains(t, string(raw), "# fullsend-stage: "+w.stage, w.path)
		assert.True(t, isThinStageCaller(w.path), w.path)
		stage, err := thinStageName(string(raw))
		require.NoError(t, err, w.path)
		assert.Equal(t, w.stage, stage, w.path)
	}
}

func TestRenderAllThinCallersFreeOfPlaceholders(t *testing.T) {
	for _, w := range thinStageWorkflows {
		raw, err := FullsendRepoFile(w.path)
		require.NoError(t, err, w.path)
		for _, vendored := range []bool{false, true} {
			rendered, err := RenderTemplate(w.path, raw, RenderOptions{Vendored: vendored})
			require.NoError(t, err, w.path)
			assertFreeOfRenderPlaceholders(t, string(rendered))
		}
	}
}
