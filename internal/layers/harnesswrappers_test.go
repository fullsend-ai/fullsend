package layers

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCommitSHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func testPrinter() *ui.Printer {
	var buf bytes.Buffer
	return ui.New(&buf)
}

func testAgents() []AgentCredentials {
	return []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "fullsend", Name: "test-fullsend", Slug: "test-fullsend"}},
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "test-triage", Slug: "test-triage"}},
		{AgentEntry: config.AgentEntry{Role: "coder", Name: "test-coder", Slug: "test-coder"}},
		{AgentEntry: config.AgentEntry{Role: "review", Name: "test-review", Slug: "test-review"}},
		{AgentEntry: config.AgentEntry{Role: "retro", Name: "test-retro", Slug: "test-retro"}},
		{AgentEntry: config.AgentEntry{Role: "prioritize", Name: "test-prioritize", Slug: "test-prioritize"}},
	}
}

func TestHarnessWrappersLayer_Name(t *testing.T) {
	layer := NewHarnessWrappersLayer("org", nil, testPrinter(), nil, "dev")
	assert.Equal(t, "harness-wrappers", layer.Name())
}

func TestHarnessWrappersLayer_RequiredScopes(t *testing.T) {
	layer := NewHarnessWrappersLayer("org", nil, testPrinter(), nil, "dev")
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpInstall))
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpAnalyze))
	assert.Nil(t, layer.RequiredScopes(OpUninstall))
}

func TestHarnessWrappersLayer_Install_DevBuild(t *testing.T) {
	client := forge.NewFakeClient()
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), testAgents(), "dev")

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.CommittedFiles, "dev build should not commit any files")
}

func TestHarnessWrappersLayer_Install_EmptyCommitSHA(t *testing.T) {
	client := forge.NewFakeClient()
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), testAgents(), "")

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.CommittedFiles)
}

func TestHarnessWrappersLayer_Install_GeneratesWrappers(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "org/.fullsend", DefaultBranch: "main"}}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), testAgents(), testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	batch := client.CommittedFiles[0]
	assert.Equal(t, "org", batch.Owner)
	assert.Equal(t, ".fullsend", batch.Repo)

	paths := make(map[string]string)
	for _, f := range batch.Files {
		paths[f.Path] = string(f.Content)
	}

	// fullsend role should be skipped (no harness)
	assert.NotContains(t, paths, "harness/fullsend.yaml")

	// triage should have a wrapper
	assert.Contains(t, paths, "harness/triage.yaml")
	assert.Contains(t, paths["harness/triage.yaml"], "role: triage")
	assert.Contains(t, paths["harness/triage.yaml"], "slug: test-triage")
	assert.Contains(t, paths["harness/triage.yaml"], "base: https://raw.githubusercontent.com/fullsend-ai/fullsend/")

	// review should have a wrapper
	assert.Contains(t, paths, "harness/review.yaml")
	assert.Contains(t, paths["harness/review.yaml"], "role: review")
	assert.Contains(t, paths["harness/review.yaml"], "slug: test-review")

	// coder role should generate both code and fix wrappers
	assert.Contains(t, paths, "harness/code.yaml")
	assert.Contains(t, paths["harness/code.yaml"], "role: coder")
	assert.Contains(t, paths["harness/code.yaml"], "slug: test-coder")

	assert.Contains(t, paths, "harness/fix.yaml")
	assert.Contains(t, paths["harness/fix.yaml"], "role: coder")
	assert.Contains(t, paths["harness/fix.yaml"], "slug: test-coder")

	// retro and prioritize should have wrappers
	assert.Contains(t, paths, "harness/retro.yaml")
	assert.Contains(t, paths["harness/retro.yaml"], "role: retro")
	assert.Contains(t, paths["harness/retro.yaml"], "slug: test-retro")

	assert.Contains(t, paths, "harness/prioritize.yaml")
	assert.Contains(t, paths["harness/prioritize.yaml"], "role: prioritize")
	assert.Contains(t, paths["harness/prioritize.yaml"], "slug: test-prioritize")
}

func TestHarnessWrappersLayer_Install_WrapperContainsManagedHeader(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	content := string(client.CommittedFiles[0].Files[0].Content)
	assert.True(t, len(content) > 0)
	assert.Contains(t, content, "# This file is managed by fullsend.")
	assert.Contains(t, content, "# To customize, use customized/harness/ instead (see ADR-0035).")
}

func TestHarnessWrappersLayer_Install_WrapperContainsIntegrityHash(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	content := string(client.CommittedFiles[0].Files[0].Content)
	assert.Contains(t, content, "#sha256=")
}

func TestHarnessWrappersLayer_Install_SkipsCustomizedFile(t *testing.T) {
	client := forge.NewFakeClient()
	// Pre-populate with a customized (non-managed) harness file
	client.FileContents["org/.fullsend/harness/triage.yaml"] = []byte("agent: agents/custom-triage.md\nmodel: sonnet\n")

	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// No files should be committed since the only file was customized
	assert.Empty(t, client.CommittedFiles)
}

func TestHarnessWrappersLayer_Install_OverwritesManagedFile(t *testing.T) {
	client := forge.NewFakeClient()
	// Pre-populate with a managed harness file
	client.FileContents["org/.fullsend/harness/triage.yaml"] = []byte("# This file is managed by fullsend. Do not edit it directly.\nbase: https://old-url\nrole: triage\nslug: old-slug\n")

	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	content := string(client.CommittedFiles[0].Files[0].Content)
	assert.Contains(t, content, "slug: test-triage")
	assert.NotContains(t, content, "old-slug")
}

func TestHarnessWrappersLayer_Install_CommitFilesError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["CommitFiles"] = errors.New("network error")

	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing harness wrappers")
}

func TestHarnessWrappersLayer_Install_NoAgentsNoCommit(t *testing.T) {
	client := forge.NewFakeClient()
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), nil, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.CommittedFiles)
}

func TestHarnessWrappersLayer_Install_OnlyFullsendRoleNoCommit(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "fullsend", Name: "fs", Slug: "test-fullsend"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.CommittedFiles)
}

func TestHarnessWrappersLayer_Install_WrapperParsesAsValidHarness(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	content := client.CommittedFiles[0].Files[0].Content

	// Write to a temp file and verify it parses via LoadRaw
	dir := t.TempDir()
	path := filepath.Join(dir, "triage.yaml")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	h, loadErr := harness.LoadRaw(path)
	require.NoError(t, loadErr)
	assert.Equal(t, "triage", h.Role)
	assert.Equal(t, "test-triage", h.Slug)
	assert.NotEmpty(t, h.Base)
}

func TestHarnessWrappersLayer_Install_BaseURLMatchesScaffold(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	expectedURL, urlErr := scaffold.HarnessBaseURLWithHash("triage", testCommitSHA)
	require.NoError(t, urlErr)

	content := string(client.CommittedFiles[0].Files[0].Content)
	assert.Contains(t, content, "base: "+expectedURL)
}

func TestHarnessWrappersLayer_Uninstall_NoOp(t *testing.T) {
	layer := NewHarnessWrappersLayer("org", nil, testPrinter(), nil, "dev")
	err := layer.Uninstall(context.Background())
	require.NoError(t, err)
}

func TestHarnessWrappersLayer_Analyze_DevBuild(t *testing.T) {
	layer := NewHarnessWrappersLayer("org", nil, testPrinter(), nil, "dev")
	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Contains(t, report.Details[0], "dev build")
}

func TestHarnessWrappersLayer_Analyze_AllPresent(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	client.FileContents["org/.fullsend/harness/triage.yaml"] = []byte("role: triage\n")

	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)
	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusInstalled, report.Status)
}

func TestHarnessWrappersLayer_Analyze_AllMissing(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}

	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)
	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Len(t, report.WouldInstall, 1)
	assert.Contains(t, report.WouldInstall[0], "harness/triage.yaml")
}

func TestHarnessWrappersLayer_Analyze_Degraded(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
		{AgentEntry: config.AgentEntry{Role: "review", Name: "r", Slug: "test-review"}},
	}
	// Only triage exists
	client.FileContents["org/.fullsend/harness/triage.yaml"] = []byte("role: triage\n")

	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)
	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.Len(t, report.Details, 1)
	assert.Len(t, report.WouldFix, 1)
}

func TestHarnessWrappersLayer_Analyze_NoAgents(t *testing.T) {
	client := forge.NewFakeClient()
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), nil, testCommitSHA)
	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusNotInstalled, report.Status)
}

func TestHarnessesForRole(t *testing.T) {
	tests := []struct {
		role     string
		expected []string
	}{
		{"fullsend", nil},
		{"coder", []string{"code", "fix"}},
		{"triage", []string{"triage"}},
		{"review", []string{"review"}},
		{"retro", []string{"retro"}},
		{"prioritize", []string{"prioritize"}},
		{"custom", []string{"custom"}},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			assert.Equal(t, tt.expected, harnessesForRole(tt.role))
		})
	}
}

func TestHarnessWrappersLayer_Install_FileMode(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	for _, f := range client.CommittedFiles[0].Files {
		assert.Equal(t, "100644", f.Mode, "wrapper files should be regular files")
	}
}

func TestHarnessWrappersLayer_Install_CoderFixDedup(t *testing.T) {
	client := forge.NewFakeClient()
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "coder", Name: "coder-a", Slug: "slug-a"}},
		{AgentEntry: config.AgentEntry{Role: "coder", Name: "coder-b", Slug: "slug-b"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	paths := make(map[string]bool)
	for _, f := range client.CommittedFiles[0].Files {
		assert.False(t, paths[f.Path], "duplicate file in commit: %s", f.Path)
		paths[f.Path] = true
	}
	assert.True(t, paths["harness/code.yaml"])
	assert.True(t, paths["harness/fix.yaml"])
	assert.Len(t, client.CommittedFiles[0].Files, 2)
}

func TestHarnessWrappersLayer_Install_LoadExistingHarnessesError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetFileContent"] = errors.New("permission denied")
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking existing harnesses")
}

func TestHarnessWrappersLayer_Install_IdempotentNoChange(t *testing.T) {
	client := forge.NewFakeClient()
	changed := false
	client.CommitFilesChanged = &changed
	agents := []AgentCredentials{
		{AgentEntry: config.AgentEntry{Role: "triage", Name: "t", Slug: "test-triage"}},
	}
	layer := NewHarnessWrappersLayer("org", client, testPrinter(), agents, testCommitSHA)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	// Should succeed without error even when tree is unchanged
}
