package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// extractScaffoldHarnessDir writes all embedded scaffold files to dir and
// returns the harness subdirectory path.
func extractScaffoldHarnessDir(t *testing.T, dir string) string {
	t.Helper()
	err := scaffold.WalkFullsendRepoAll(func(path string, content []byte) error {
		dest := filepath.Join(dir, path)
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dest, content, 0o644)
	})
	require.NoError(t, err, "extracting scaffold")
	return filepath.Join(dir, "harness")
}

// TestLoadWithBase_WrapperMergesScaffold verifies the full pipeline: a thin
// wrapper harness with base: pointing to a local scaffold harness loads and
// merges correctly, producing the expected role/slug overrides and inherited fields.
func TestLoadWithBase_WrapperMergesScaffold(t *testing.T) {
	dir := t.TempDir()
	harnessDir := extractScaffoldHarnessDir(t, dir)

	wrapperPath := writeTestHarness(t, harnessDir, "wrapper-triage.yaml", `
base: triage.yaml
role: triage
slug: test-triage
`)

	h, deps, err := LoadWithBase(context.Background(), wrapperPath, ComposeOpts{
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	// Role and slug come from wrapper (overrides base).
	assert.Equal(t, "triage", h.Role)
	assert.Equal(t, "test-triage", h.Slug)

	// Agent, model, image, policy inherited from base.
	assert.Equal(t, "agents/triage.md", h.Agent)
	assert.Equal(t, "opus", h.Model)
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-sandbox:latest", h.Image)
	assert.Equal(t, "policies/triage.yaml", h.Policy)

	// PreScript and PostScript populated after forge.github resolution.
	assert.NotEmpty(t, h.PreScript, "PreScript should be set after forge resolution")
	assert.NotEmpty(t, h.PostScript, "PostScript should be set after forge resolution")

	// RunnerEnv contains both top-level keys and forge.github keys after merge.
	assert.Contains(t, h.RunnerEnv, "FULLSEND_OUTPUT_SCHEMA", "should have top-level runner_env key")
	assert.Contains(t, h.RunnerEnv, "GH_TOKEN", "should have forge.github runner_env key")
	assert.Contains(t, h.RunnerEnv, "GITHUB_ISSUE_URL", "should have forge.github runner_env key")

	// Skills includes base top-level skills (forge skills are concatenated by ResolveForge,
	// but the triage template has no forge-specific skills — only runner_env and scripts).
	assert.Contains(t, h.Skills, "skills/issue-labels")

	// Forge map is nil (consumed by ResolveForge).
	assert.Nil(t, h.Forge)

	// Base field is empty (consumed by LoadWithBase).
	assert.Empty(t, h.Base)

	// Local base -> no URL deps.
	assert.Nil(t, deps)

	// ValidationLoop inherited from base.
	assert.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "scripts/validate-output-schema.sh", h.ValidationLoop.Script)
	assert.Equal(t, 2, h.ValidationLoop.MaxIterations)
}

// TestLoadWithBase_WrapperOverridesBaseFields verifies that wrapper-level
// overrides (model, slug) take precedence over base values while other fields inherit.
func TestLoadWithBase_WrapperOverridesBaseFields(t *testing.T) {
	dir := t.TempDir()
	harnessDir := extractScaffoldHarnessDir(t, dir)

	wrapperPath := writeTestHarness(t, harnessDir, "wrapper-custom.yaml", `
base: code.yaml
role: coder
slug: my-org-coder
model: sonnet
`)

	h, _, err := LoadWithBase(context.Background(), wrapperPath, ComposeOpts{
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	assert.Equal(t, "coder", h.Role)
	assert.Equal(t, "my-org-coder", h.Slug)
	assert.Equal(t, "sonnet", h.Model, "wrapper model should override base model")
	assert.Equal(t, "agents/code.md", h.Agent, "agent should be inherited from base")
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-code:latest", h.Image, "image should be inherited from base")
}

// TestLoadWithOpts_ScaffoldTemplatesForgeResolution loads every scaffold harness
// template with ForgePlatform: "github" and verifies the merged state is
// consistent — pre/post scripts populated, runner_env merged, forge consumed.
func TestLoadWithOpts_ScaffoldTemplatesForgeResolution(t *testing.T) {
	dir := t.TempDir()
	harnessDir := extractScaffoldHarnessDir(t, dir)

	names, err := scaffold.HarnessNames()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(harnessDir, name+".yaml")

			h, loadErr := LoadWithOpts(path, LoadOpts{ForgePlatform: "github"})
			require.NoError(t, loadErr)

			assert.NotEmpty(t, h.PreScript, "PreScript should be set after forge resolution")
			assert.NotEmpty(t, h.PostScript, "PostScript should be set after forge resolution")
			assert.NotEmpty(t, h.RunnerEnv, "RunnerEnv should be non-empty after merge")
			assert.Nil(t, h.Forge, "Forge should be nil after resolution")
			assert.NotEmpty(t, h.Role, "Role should be set in scaffold template")
			assert.NotEmpty(t, h.Slug, "Slug should be set in scaffold template")
		})
	}
}

// TestLoad_ScaffoldTemplatesBackwardCompat loads every scaffold harness template
// via Load() (no forge platform) and verifies backward compatibility: the
// harness loads without error, top-level defaults are present, and the forge
// map is retained (not consumed).
func TestLoad_ScaffoldTemplatesBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	harnessDir := extractScaffoldHarnessDir(t, dir)

	names, err := scaffold.HarnessNames()
	require.NoError(t, err)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(harnessDir, name+".yaml")

			h, loadErr := Load(path)
			require.NoError(t, loadErr)

			// Top-level pre/post scripts serve as defaults.
			assert.NotEmpty(t, h.PreScript, "PreScript should be set at top level as default")
			assert.NotEmpty(t, h.PostScript, "PostScript should be set at top level as default")

			// Forge map is present and has "github" key.
			assert.NotNil(t, h.Forge, "Forge map should be present")
			assert.Contains(t, h.Forge, "github", "Forge should have a github key")
		})
	}
}

// TestDiscoverAgents_ScaffoldDirectory extracts the scaffold to a temp dir,
// runs DiscoverAgents on the harness directory, and verifies all agents are
// discovered with correct role/slug pairs.
func TestDiscoverAgents_ScaffoldDirectory(t *testing.T) {
	dir := t.TempDir()
	harnessDir := extractScaffoldHarnessDir(t, dir)

	agents, err := DiscoverAgents(harnessDir)
	require.NoError(t, err)

	// Expect all 6 scaffold harnesses discovered.
	require.Len(t, agents, 6, "should discover all 6 scaffold harnesses")

	// Build a map of filename -> AgentInfo for easier assertion.
	byFilename := make(map[string]AgentInfo, len(agents))
	for _, a := range agents {
		byFilename[a.Filename] = a
	}

	expected := map[string]struct{ role, slug string }{
		"code.yaml":       {"coder", "fullsend-ai-coder"},
		"fix.yaml":        {"coder", "fullsend-ai-coder"},
		"prioritize.yaml": {"prioritize", "fullsend-ai-prioritize"},
		"retro.yaml":      {"retro", "fullsend-ai-retro"},
		"review.yaml":     {"review", "fullsend-ai-review"},
		"triage.yaml":     {"triage", "fullsend-ai-triage"},
	}

	for filename, want := range expected {
		got, ok := byFilename[filename]
		require.True(t, ok, "should discover %s", filename)
		assert.Equal(t, want.role, got.Role, "%s role", filename)
		assert.Equal(t, want.slug, got.Slug, "%s slug", filename)
		assert.True(t, filepath.IsAbs(got.Path), "%s path should be absolute", filename)
	}

	// Verify sort order: by role, then by filename.
	sorted := make([]AgentInfo, len(agents))
	copy(sorted, agents)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Role != sorted[j].Role {
			return sorted[i].Role < sorted[j].Role
		}
		return sorted[i].Filename < sorted[j].Filename
	})
	assert.Equal(t, sorted, agents, "results should be sorted by role then filename")
}

// TestHarnessContentHash_MatchesEmbeddedContent verifies that HarnessContentHash
// produces correct SHA-256 hashes matching the embedded file content, and that
// HarnessBaseURLWithHash produces well-formed URLs with matching hash fragments.
func TestHarnessContentHash_MatchesEmbeddedContent(t *testing.T) {
	names, err := scaffold.HarnessNames()
	require.NoError(t, err)

	fakeCommitSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			// Compute hash via the scaffold package.
			hash, err := scaffold.HarnessContentHash(name)
			require.NoError(t, err)
			assert.Len(t, hash, 64, "SHA-256 hex digest should be 64 characters")

			// Independently compute hash from the embedded file content.
			content, err := scaffold.FullsendRepoFile("harness/" + name + ".yaml")
			require.NoError(t, err)
			sum := sha256.Sum256(content)
			independentHash := hex.EncodeToString(sum[:])
			assert.Equal(t, independentHash, hash,
				"HarnessContentHash should match sha256 of embedded file content")

			// Verify HarnessBaseURLWithHash produces a valid URL with matching hash.
			fullURL, err := scaffold.HarnessBaseURLWithHash(name, fakeCommitSHA)
			require.NoError(t, err)
			assert.Contains(t, fullURL, fakeCommitSHA)
			assert.Contains(t, fullURL, name+".yaml")
			assert.Contains(t, fullURL, "#sha256="+hash)
		})
	}
}

// TestLoadRaw_GeneratedWrapperFormat verifies that the wrapper YAML format
// produced by HarnessWrappersLayer (base + role + slug) parses correctly via
// LoadRaw and contains the expected identity fields.
func TestLoadRaw_GeneratedWrapperFormat(t *testing.T) {
	names, err := scaffold.HarnessNames()
	require.NoError(t, err)

	fakeCommitSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			baseURL, err := scaffold.HarnessBaseURLWithHash(name, fakeCommitSHA)
			require.NoError(t, err)

			// Simulate the wrapper format produced by HarnessWrappersLayer.
			wrapperYAML := "base: " + baseURL + "\n" +
				"role: " + name + "\n" +
				"slug: test-" + name + "\n"

			dir := t.TempDir()
			path := writeTestHarness(t, dir, name+".yaml", wrapperYAML)

			h, err := LoadRaw(path)
			require.NoError(t, err)

			assert.Equal(t, baseURL, h.Base, "base should be the full URL with hash")
			assert.Equal(t, name, h.Role)
			assert.Equal(t, "test-"+name, h.Slug)
		})
	}
}

// TestResolveForge_ScaffoldRunnerEnvMerge verifies that forge resolution
// produces the expected merged runner_env for each scaffold template, with
// both top-level (platform-neutral) and forge.github (platform-specific)
// keys present in the final merged state.
func TestResolveForge_ScaffoldRunnerEnvMerge(t *testing.T) {
	dir := t.TempDir()
	harnessDir := extractScaffoldHarnessDir(t, dir)

	tests := []struct {
		file            string
		topLevelKeys    []string
		forgeGithubKeys []string
	}{
		{
			file:            "triage.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"GITHUB_ISSUE_URL", "GH_TOKEN"},
		},
		{
			file:            "code.yaml",
			topLevelKeys:    []string{"CODE_ALLOWED_TARGET_BRANCHES", "FULLSEND_OUTPUT_SCHEMA", "FULLSEND_OUTPUT_FILE"},
			forgeGithubKeys: []string{"PUSH_TOKEN", "PUSH_TOKEN_SOURCE", "REPO_FULL_NAME", "ISSUE_NUMBER", "REPO_DIR"},
		},
		{
			file:            "review.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"REVIEW_TOKEN", "REPO_FULL_NAME", "PR_NUMBER", "GITHUB_PR_URL"},
		},
		{
			file:            "fix.yaml",
			topLevelKeys:    []string{"TARGET_BRANCH", "TRIGGER_SOURCE", "HUMAN_INSTRUCTION", "FIX_ITERATION", "REVIEW_BODY_FILE", "PRE_AGENT_HEAD", "FULLSEND_OUTPUT_SCHEMA", "FULLSEND_OUTPUT_FILE"},
			forgeGithubKeys: []string{"PUSH_TOKEN", "PUSH_TOKEN_SOURCE", "REPO_FULL_NAME", "PR_NUMBER", "REPO_DIR"},
		},
		{
			file:            "retro.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"ORIGINATING_URL", "REPO_FULL_NAME", "GH_TOKEN"},
		},
		{
			file:            "prioritize.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"GITHUB_ISSUE_URL", "GH_TOKEN", "ORG", "PROJECT_NUMBER"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			path := filepath.Join(harnessDir, tt.file)

			h, loadErr := LoadWithOpts(path, LoadOpts{ForgePlatform: "github"})
			require.NoError(t, loadErr)

			for _, key := range tt.topLevelKeys {
				assert.Contains(t, h.RunnerEnv, key, "merged RunnerEnv should contain top-level key %s", key)
			}
			for _, key := range tt.forgeGithubKeys {
				assert.Contains(t, h.RunnerEnv, key, "merged RunnerEnv should contain forge.github key %s", key)
			}
		})
	}
}
