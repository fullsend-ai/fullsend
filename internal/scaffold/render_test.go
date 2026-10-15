package scaffold

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderThinCallerNotVendored(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{
		Vendored: false,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@main")
	assertFreeOfRenderPlaceholders(t, out)
	assert.NotContains(t, out, "distribution_mode")
	assert.NotContains(t, out, "fullsend_ai_repo:")
}

func TestRenderPerRepoShimVendored(t *testing.T) {
	raw, err := PerRepoShimTemplate()
	require.NoError(t, err)

	rendered, err := RenderTemplate("templates/shim-per-repo.yaml", raw, RenderOptions{
		Vendored: true,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: ./.github/workflows/reusable-dispatch.yml")
	assert.NotContains(t, out, "distribution_mode")
	assert.NotContains(t, out, "install_mode",
		"per-repo shim must rely on the reusable-dispatch.yml install_mode default")
}

func TestRenderPrioritizeThinCaller(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{
		Vendored: true,
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: ./.github/workflows/reusable-prioritize.yml")
	assert.NotContains(t, out, "install_mode")
	assert.NotContains(t, out, "distribution_mode")
	assert.Contains(t, out, "project_number: ${{ inputs.project_number || vars.FULLSEND_PROJECT_NUMBER }}")
	assertFreeOfRenderPlaceholders(t, out)
}

func TestRenderThinCallerNormalizesLegacyInstallMode(t *testing.T) {
	legacy := []byte("# fullsend-stage: prioritize\n" +
		"uses: __REUSABLE_WORKFLOW__\n" +
		"install_mode: per-org\n")

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", legacy, RenderOptions{})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "install_mode: per-repo")
	assert.NotContains(t, out, "install_mode: per-org")
}

func TestWalkUpstreamIncludesReusableWorkflows(t *testing.T) {
	var paths []string
	err := WalkUpstream(func(path string, _ []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)

	for _, want := range []string{
		".github/workflows/reusable-prioritize.yml",
		".github/workflows/reusable-dispatch.yml",
		".github/actions/mint-token/action.yml",
		"action.yml",
	} {
		assert.Contains(t, paths, want)
	}
}

func assertFreeOfRenderPlaceholders(t *testing.T, out string) {
	t.Helper()
	for _, placeholder := range []string{
		"__REUSABLE_WORKFLOW__",
		"__REUSABLE_DISPATCH__",
		"__UPSTREAM_REF__",
		"__DISTRIBUTION_MODE__",
		"__FULLSEND_AI_REF__",
		"__GH_RUNNER__",
	} {
		assert.NotContains(t, out, placeholder)
	}
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

func TestRenderThinCallerPinnedSHA(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{
		UpstreamRef: "abc123def456",
		UpstreamTag: "v0.19.0",
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@abc123def456")
	assert.Contains(t, out, "# v0.19.0")
	assert.NotContains(t, out, "fullsend_ai_ref:")
	assertFreeOfRenderPlaceholders(t, out)
}

func TestRenderPerRepoShimPinnedSHA(t *testing.T) {
	raw, err := PerRepoShimTemplate()
	require.NoError(t, err)

	rendered, err := RenderTemplate("templates/shim-per-repo.yaml", raw, RenderOptions{
		UpstreamRef: "abc123def456",
		UpstreamTag: "v0.19.0",
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123def456")
	assert.Contains(t, out, "# v0.19.0")
	assert.NotContains(t, out, "fullsend_ai_ref:")
	assertFreeOfRenderPlaceholders(t, out)
}

func TestRenderDefaultRunner(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "runner_image: ubuntu-24.04")
	assert.NotContains(t, out, "__GH_RUNNER__")
}

func TestRenderCustomRunner(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{
		RunnerImage: "ubuntu-22.04",
	})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "runner_image: ubuntu-22.04")
	assert.NotContains(t, out, "ubuntu-24.04")
	assert.NotContains(t, out, "__GH_RUNNER__")
}

func TestRenderPerRepoShimRunner(t *testing.T) {
	raw, err := PerRepoShimTemplate()
	require.NoError(t, err)

	rendered, err := RenderTemplate("templates/shim-per-repo.yaml", raw, RenderOptions{})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "runner_image: ubuntu-24.04")
	assert.Contains(t, out, "runs-on: ubuntu-24.04")
	assert.NotContains(t, out, "__GH_RUNNER__")
}

func TestRenderFallbackToDefaultRef(t *testing.T) {
	raw, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)

	rendered, err := RenderTemplate(".github/workflows/prioritize.yml", raw, RenderOptions{})
	require.NoError(t, err)
	out := string(rendered)
	assert.Contains(t, out, "@main")
	assert.NotContains(t, out, "fullsend_ai_ref:")
}

// Inline version comments need two spaces before '#' to satisfy yamllint's
// default comments rule (#7902).
func TestRenderRefCommentSpacing(t *testing.T) {
	const sha = "abc123def456"
	base := "fullsend-ai/fullsend/.github/workflows/"
	tests := []struct {
		name string
		opts RenderOptions
		ref  string
		wf   string
		disp string
	}{
		{
			name: "sha with tag",
			opts: RenderOptions{UpstreamRef: sha, UpstreamTag: "v0.19.0"},
			ref:  sha + "  # v0.19.0",
			wf:   base + "reusable-prioritize.yml@" + sha + "  # v0.19.0",
			disp: base + "reusable-dispatch.yml@" + sha + "  # v0.19.0",
		},
		{
			name: "tag equals ref",
			opts: RenderOptions{UpstreamRef: "v0.19.0", UpstreamTag: "v0.19.0"},
			ref:  "v0.19.0",
			wf:   base + "reusable-prioritize.yml@v0.19.0",
			disp: base + "reusable-dispatch.yml@v0.19.0",
		},
		{
			name: "no tag",
			opts: RenderOptions{UpstreamRef: sha},
			ref:  sha,
			wf:   base + "reusable-prioritize.yml@" + sha,
			disp: base + "reusable-dispatch.yml@" + sha,
		},
		{
			name: "vendored ignores tag",
			opts: RenderOptions{Vendored: true, UpstreamRef: sha, UpstreamTag: "v0.19.0"},
			ref:  sha + "  # v0.19.0",
			wf:   "./.github/workflows/reusable-prioritize.yml",
			disp: "./.github/workflows/reusable-dispatch.yml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.ref, resolvedRefWithComment(tt.opts))
			assert.Equal(t, tt.wf, reusableWorkflowUses("prioritize", tt.opts))
			assert.Equal(t, tt.disp, reusableDispatchUses(tt.opts))
		})
	}
}

func TestRenderedTemplatesHaveNoSingleSpaceInlineComments(t *testing.T) {
	singleSpace := regexp.MustCompile(`\S #`)
	opts := RenderOptions{UpstreamRef: "abc123def456", UpstreamTag: "v0.19.0"}

	shim, err := PerRepoShimTemplate()
	require.NoError(t, err)
	files := map[string][]byte{"templates/shim-per-repo.yaml": shim}
	for _, w := range thinStageWorkflows {
		raw, err := FullsendRepoFile(w.path)
		require.NoError(t, err, w.path)
		files[w.path] = raw
	}

	for path, raw := range files {
		rendered, err := RenderTemplate(path, raw, opts)
		require.NoError(t, err, path)
		for i, line := range strings.Split(string(rendered), "\n") {
			if strings.Contains(line, "uses:") {
				assert.NotRegexp(t, singleSpace, line, "%s:%d", path, i+1)
			}
		}
		assert.Contains(t, string(rendered), "@abc123def456  # v0.19.0", path)
	}
}
