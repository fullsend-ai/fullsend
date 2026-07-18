package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// customizedReviewHarness returns the embedded review scaffold harness with
// the model field changed, producing a valid diff that only changes a scalar.
func customizedReviewHarness(t *testing.T, newModel string) string {
	t.Helper()
	data, err := scaffold.HarnessContent("review")
	require.NoError(t, err)
	return strings.Replace(string(data), "model: opus", "model: "+newModel, 1)
}

func setupCustomizedDir(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	customizedBase := filepath.Join(dir, "customized")
	for relPath, content := range files {
		full := filepath.Join(customizedBase, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
}

func fakeClientWithRepo(owner, repo string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{
		FullName:      owner + "/" + repo,
		Name:          repo,
		DefaultBranch: "main",
	}}
	return fc
}

func TestMigrateCustomizations_NoCustomizedDir(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", true, nil, printer)
	require.NoError(t, err)
}

func TestMigrateCustomizations_EmptyCustomizedDir(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "customized", "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "customized", "harness", ".gitkeep"), nil, 0o644))
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", true, nil, printer)
	require.NoError(t, err)
}

func TestMigrateCustomizations_DeadOverride_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123abc123abc123abc123abc123abc123abc1/harness/review.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`)
	setupCustomizedDir(t, dir, map[string]string{
		"harness/review.yaml": "agent: agents/review.md\nmodel: opus\n",
	})

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", true, nil, printer)
	require.NoError(t, err)

	// Dry-run should NOT delete the file.
	_, err = os.Stat(filepath.Join(dir, "customized", "harness", "review.yaml"))
	assert.NoError(t, err, "dry-run should not delete files")
}

func TestMigrateCustomizations_DeadOverride_CreatesPR(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123abc123abc123abc123abc123abc123abc1/harness/review.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`)
	setupCustomizedDir(t, dir, map[string]string{
		"harness/review.yaml": "agent: agents/review.md\nmodel: opus\n",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	// Should have created a branch.
	require.Len(t, fc.CreatedBranches, 1)
	assert.Equal(t, "my-org/.fullsend/fullsend/migrate-customizations", fc.CreatedBranches[0])

	// Should have committed files with a delete entry.
	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]
	assert.Equal(t, "fullsend/migrate-customizations", record.Branch)

	var deletePaths []string
	for _, f := range record.Files {
		if f.Delete {
			deletePaths = append(deletePaths, f.Path)
		}
	}
	assert.Contains(t, deletePaths, "customized/harness/review.yaml")

	// Should have created a PR.
	require.Len(t, fc.CreatedProposals, 1)
	assert.Contains(t, fc.CreatedProposals[0].Title, "migrate customized/")
}

func TestMigrateCustomizations_CustomAgent_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	setupCustomizedDir(t, dir, map[string]string{
		"harness/gh-classify.yaml": "agent: agents/gh-classify.md\nmodel: opus\n",
		"agents/gh-classify.md":    "You are gh-classify agent.\n",
	})

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", true, nil, printer)
	require.NoError(t, err)

	// Dry-run: customized file should still exist.
	_, err = os.Stat(filepath.Join(dir, "customized", "harness", "gh-classify.yaml"))
	assert.NoError(t, err, "file should still exist in dry-run mode")

	// Config should NOT be modified.
	cfgData, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(cfgData), "gh-classify")
}

func TestMigrateCustomizations_CustomAgent_CreatesPR(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/gh-classify.yaml": "agent: agents/gh-classify.md\nmodel: opus\n",
		"agents/gh-classify.md":    "You are gh-classify agent.\n",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	// Should have committed files.
	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	// Verify tree files: move harness + agent, delete both from customized/, update config.
	pathMap := make(map[string]forge.TreeFile)
	for _, f := range record.Files {
		pathMap[f.Path] = f
	}

	// Harness moved to regular dir.
	harnessFile, ok := pathMap["harness/gh-classify.yaml"]
	require.True(t, ok, "harness should be added at regular path")
	assert.Contains(t, string(harnessFile.Content), "agents/gh-classify.md")
	assert.False(t, harnessFile.Delete)

	// Agent prompt moved to regular dir.
	agentFile, ok := pathMap["agents/gh-classify.md"]
	require.True(t, ok, "agent prompt should be added at regular path")
	assert.Contains(t, string(agentFile.Content), "gh-classify agent")
	assert.False(t, agentFile.Delete)

	// Customized copies deleted.
	assert.True(t, pathMap["customized/harness/gh-classify.yaml"].Delete)
	assert.True(t, pathMap["customized/agents/gh-classify.md"].Delete)

	// Config updated with agent registration.
	cfgFile, ok := pathMap["config.yaml"]
	require.True(t, ok, "config should be updated")
	assert.Contains(t, string(cfgFile.Content), "harness/gh-classify.yaml")

	// PR created.
	require.Len(t, fc.CreatedProposals, 1)
}

func TestMigrateCustomizations_StandaloneFiles_CreatesPR(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"env/common.env": "SHARED_KEY=value\n",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	pathMap := make(map[string]forge.TreeFile)
	for _, f := range record.Files {
		pathMap[f.Path] = f
	}

	// File moved to regular dir.
	envFile, ok := pathMap["env/common.env"]
	require.True(t, ok)
	assert.Equal(t, "SHARED_KEY=value\n", string(envFile.Content))
	assert.False(t, envFile.Delete)

	// Customized copy deleted.
	assert.True(t, pathMap["customized/env/common.env"].Delete)
}

func TestMigrateCustomizations_RequiresRepoFlag(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/gh-classify.yaml": "agent: agents/gh-classify.md\nmodel: opus\n",
	})

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", false, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--repo is required")
}

func TestMigrateCustomizations_RequiresForgeClient(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/gh-classify.yaml": "agent: agents/gh-classify.md\nmodel: opus\n",
	})

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge client required")
}

func TestMigrateCustomizations_InvalidRepoFormat(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/gh-classify.yaml": "agent: agents/gh-classify.md\nmodel: opus\n",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "invalid-repo", false, fc, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo format")
}

func TestMigrateCustomizations_StandaloneFiles_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"env/common.env": "SHARED_KEY=value\n",
	})

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", true, nil, printer)
	require.NoError(t, err)

	// Dry-run: file should still be in customized/.
	_, err = os.Stat(filepath.Join(dir, "customized", "env", "common.env"))
	assert.NoError(t, err, "dry-run should not move files")
}

func TestMigrateCustomizations_PerRepoMode_DryRun(t *testing.T) {
	dir := t.TempDir()
	// Realistic per-repo layout: --agent-dir points to .fullsend/.
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	writePerRepoConfig(t, fullsendDir, "")

	customizedBase := filepath.Join(fullsendDir, "customized")
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "harness", "my-agent.yaml"),
		[]byte("agent: agents/my-agent.md\n"),
		0o644,
	))

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), fullsendDir, "", true, nil, printer)
	require.NoError(t, err)

	// Dry-run: file should still exist.
	_, err = os.Stat(filepath.Join(customizedBase, "harness", "my-agent.yaml"))
	assert.NoError(t, err)
}

func TestMigrateCustomizations_PerRepoMode_CreatesPR(t *testing.T) {
	dir := t.TempDir()
	// Realistic per-repo layout: --agent-dir points to .fullsend/.
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	writePerRepoConfig(t, fullsendDir, "")

	customizedBase := filepath.Join(fullsendDir, "customized")
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "harness", "my-agent.yaml"),
		[]byte("agent: agents/my-agent.md\n"),
		0o644,
	))

	fc := fakeClientWithRepo("my-org", "my-repo")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), fullsendDir, "my-org/my-repo", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	pathMap := make(map[string]forge.TreeFile)
	for _, f := range record.Files {
		pathMap[f.Path] = f
	}

	// Per-repo prefix is .fullsend/customized/.
	assert.True(t, pathMap[".fullsend/customized/harness/my-agent.yaml"].Delete)

	// File moved to regular path under .fullsend/.
	_, ok := pathMap[".fullsend/harness/my-agent.yaml"]
	assert.True(t, ok)

	// Config updated at per-repo path.
	cfgFile, ok := pathMap[".fullsend/config.yaml"]
	require.True(t, ok)
	assert.Contains(t, string(cfgFile.Content), "harness/my-agent.yaml")
}

func TestMigrateCustomizations_MixedDeadAndCustom(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123abc123abc123abc123abc123abc123abc1/harness/review.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`)
	setupCustomizedDir(t, dir, map[string]string{
		"harness/review.yaml":      "agent: agents/review.md\nmodel: opus\n",
		"harness/gh-classify.yaml": "agent: agents/gh-classify.md\nmodel: sonnet\n",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	pathMap := make(map[string]forge.TreeFile)
	for _, f := range record.Files {
		pathMap[f.Path] = f
	}

	// Dead override: only delete.
	assert.True(t, pathMap["customized/harness/review.yaml"].Delete)

	// Custom agent: move + delete + config.
	_, ok := pathMap["harness/gh-classify.yaml"]
	assert.True(t, ok, "custom agent harness should be moved")
	assert.True(t, pathMap["customized/harness/gh-classify.yaml"].Delete)

	// Config updated with custom agent.
	cfgFile := pathMap["config.yaml"]
	assert.Contains(t, string(cfgFile.Content), "harness/gh-classify.yaml")
}

func TestWalkCustomized(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"harness/.gitkeep":      "",
		"harness/review.yaml":   "test",
		"agents/review.md":      "test",
		"agents/.gitkeep":       "",
		"scripts/pre-review.sh": "#!/bin/sh",
	}
	for relPath, content := range files {
		full := filepath.Join(dir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	result, err := walkCustomized(dir)
	require.NoError(t, err)

	// .gitkeep files should be excluded.
	for _, f := range result {
		assert.NotEqual(t, ".gitkeep", filepath.Base(f))
	}
	assert.Len(t, result, 3)
}

func TestWalkCustomized_SkipsSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create a real file and a symlink.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "real.yaml"), []byte("test"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "harness", "symlink.yaml")))

	result, err := walkCustomized(dir)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, filepath.Join("harness", "real.yaml"), result[0])
}

func TestReadTreeFile_PreservesExecutableBit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))

	// Non-executable file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "config.yaml"), []byte("key: val"), 0o644))
	tf, err := readTreeFile(dir, "scripts/config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "100644", tf.Mode)

	// Executable file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh"), 0o755))
	tf, err = readTreeFile(dir, "scripts/run.sh")
	require.NoError(t, err)
	assert.Equal(t, "100755", tf.Mode)
	assert.Equal(t, "scripts/run.sh", tf.Path)
	assert.Equal(t, "#!/bin/sh", string(tf.Content))
}

func TestMigrateCustomizations_ExecutableScriptMode(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	// Create an executable script in customized/.
	customizedBase := filepath.Join(dir, "customized")
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "scripts", "deploy.sh"),
		[]byte("#!/bin/bash\necho deploy"),
		0o755,
	))

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	for _, f := range record.Files {
		if f.Path == "scripts/deploy.sh" {
			assert.Equal(t, "100755", f.Mode, "executable script should preserve 100755 mode")
			return
		}
	}
	t.Fatal("scripts/deploy.sh not found in committed files")
}

func TestBuildModifiedAgentFiles_DiffAbort(t *testing.T) {
	origSHA := commitSHA
	commitSHA = "abcdef1234567890abcdef1234567890abcdef12"
	t.Cleanup(func() { commitSHA = origSHA })

	dir := t.TempDir()
	customizedBase := filepath.Join(dir, "customized")
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "harness"), 0o755))

	// Create a customized harness that removes skills from the scaffold base.
	// The review scaffold harness has skills, so an empty skills list triggers removal.
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "harness", "review.yaml"),
		[]byte("agent: agents/review.md\nmodel: opus\nimage: ghcr.io/fullsend-ai/fullsend-code:latest\nskills: []\n"),
		0o644,
	))

	cfg := func() config.ConfigWriter {
		data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [fullsend]\nrepos: {}\n")
		c, _ := config.ParseOrgConfig(data)
		return c
	}()

	m := agentMigration{
		name:   "review",
		action: migrateModified,
		files:  []string{"harness/review.yaml"},
	}

	printer := ui.New(io.Discard)
	_, err := buildModifiedAgentFiles(customizedBase, "customized/", "", m, cfg, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "diff aborted")
}

func TestBuildModifiedAgentFiles_DevCommitSHAError(t *testing.T) {
	origSHA := commitSHA
	commitSHA = "dev"
	t.Cleanup(func() { commitSHA = origSHA })

	dir := t.TempDir()
	customizedBase := filepath.Join(dir, "customized")
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "harness", "review.yaml"),
		[]byte(customizedReviewHarness(t, "sonnet")),
		0o644,
	))

	cfg := func() config.ConfigWriter {
		data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [fullsend]\nrepos: {}\n")
		c, _ := config.ParseOrgConfig(data)
		return c
	}()

	m := agentMigration{
		name:   "review",
		action: migrateModified,
		files:  []string{"harness/review.yaml"},
	}

	printer := ui.New(io.Discard)
	_, err := buildModifiedAgentFiles(customizedBase, "customized/", "", m, cfg, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine base URL")
}

func TestMigrateCustomizations_ModifiedAgent_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/review.yaml": customizedReviewHarness(t, "sonnet"),
	})

	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "", true, nil, printer)
	require.NoError(t, err)

	// Dry-run should not modify anything.
	_, err = os.Stat(filepath.Join(dir, "customized", "harness", "review.yaml"))
	assert.NoError(t, err, "dry-run should not delete files")
}

func TestMigrateCustomizations_ModifiedAgent_CreatesPR(t *testing.T) {
	// Set commitSHA to a valid value so scaffold URL fallback works.
	origSHA := commitSHA
	commitSHA = "abcdef1234567890abcdef1234567890abcdef12"
	t.Cleanup(func() { commitSHA = origSHA })

	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/review.yaml": customizedReviewHarness(t, "sonnet"),
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	pathMap := make(map[string]forge.TreeFile)
	for _, f := range record.Files {
		pathMap[f.Path] = f
	}

	// Should have a composition harness with base: URL and diff content.
	harnessFile, ok := pathMap["harness/review.yaml"]
	require.True(t, ok, "composition harness should be created")
	harnessContent := string(harnessFile.Content)
	assert.Contains(t, harnessContent, "base:")
	assert.Contains(t, harnessContent, "raw.githubusercontent.com")
	assert.Contains(t, harnessContent, "model: sonnet", "diff should include the changed model field")
	assert.NotContains(t, harnessContent, "model: opus", "base model should not appear in diff")

	// Old customized harness should be deleted.
	assert.True(t, pathMap["customized/harness/review.yaml"].Delete)

	// Config should have the agent registered.
	cfgFile, ok := pathMap["config.yaml"]
	require.True(t, ok)
	assert.Contains(t, string(cfgFile.Content), "harness/review.yaml")

	// PR created.
	require.Len(t, fc.CreatedProposals, 1)
}

func TestBuildModifiedAgentFiles_WithAssociatedFiles(t *testing.T) {
	origSHA := commitSHA
	commitSHA = "abcdef1234567890abcdef1234567890abcdef12"
	t.Cleanup(func() { commitSHA = origSHA })

	dir := t.TempDir()
	customizedBase := filepath.Join(dir, "customized")

	// Set up customized harness + associated agent prompt.
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(customizedBase, "agents"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "harness", "review.yaml"),
		[]byte(customizedReviewHarness(t, "sonnet")),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(customizedBase, "agents", "review.md"),
		[]byte("Custom review agent prompt.\n"),
		0o644,
	))

	cfg := func() config.ConfigWriter {
		data := []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
repos: {}
`)
		c, _ := config.ParseOrgConfig(data)
		return c
	}()

	m := agentMigration{
		name:   "review",
		action: migrateModified,
		files:  []string{"harness/review.yaml", "agents/review.md"},
	}

	printer := ui.New(io.Discard)
	files, err := buildModifiedAgentFiles(customizedBase, "customized/", "", m, cfg, printer)
	require.NoError(t, err)

	pathMap := make(map[string]forge.TreeFile)
	for _, f := range files {
		pathMap[f.Path] = f
	}

	// Composition harness should have base: URL.
	harnessFile, ok := pathMap["harness/review.yaml"]
	require.True(t, ok)
	assert.Contains(t, string(harnessFile.Content), "base:")

	// Old customized harness deleted.
	assert.True(t, pathMap["customized/harness/review.yaml"].Delete)

	// Associated agent file moved.
	agentFile, ok := pathMap["agents/review.md"]
	require.True(t, ok)
	assert.Equal(t, "Custom review agent prompt.\n", string(agentFile.Content))

	// Customized agent file deleted.
	assert.True(t, pathMap["customized/agents/review.md"].Delete)

	// Agent registered in config.
	found := false
	for _, a := range cfg.AgentEntries() {
		if a.Source == "harness/review.yaml" {
			found = true
		}
	}
	assert.True(t, found, "agent should be registered in config")
}

func TestPlanMigrations_Categories(t *testing.T) {
	files := []string{
		"harness/review.yaml",    // in config → dead
		"harness/triage.yaml",    // in scaffold, not in config → modified
		"harness/my-custom.yaml", // not in scaffold → custom
	}

	cfg := func() config.ConfigWriter {
		data := []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
repos: {}
agents:
  - source: "https://example.com/review.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowed_remote_resources:
  - "https://example.com/"
`)
		c, err := config.ParseOrgConfig(data)
		require.NoError(t, err)
		return c
	}()

	scaffoldSet := map[string]bool{
		"triage":     true,
		"code":       true,
		"fix":        true,
		"review":     true,
		"retro":      true,
		"prioritize": true,
	}

	migrations := planMigrations(files, cfg, scaffoldSet)
	require.Len(t, migrations, 3)

	byName := make(map[string]agentMigration)
	for _, m := range migrations {
		byName[m.name] = m
	}

	assert.Equal(t, migrateDead, byName["review"].action)
	assert.Equal(t, migrateModified, byName["triage"].action)
	assert.Equal(t, migrateCustom, byName["my-custom"].action)
}

func TestPlanMigrations_AssociatesNonHarnessFiles(t *testing.T) {
	files := []string{
		"harness/review.yaml",
		"agents/review.md",
		"scripts/pre-review.sh",
		"scripts/post-review.sh",
		"policies/review.yaml",
	}

	cfg := func() config.ConfigWriter {
		data := []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
repos: {}
`)
		c, _ := config.ParseOrgConfig(data)
		return c
	}()

	scaffoldSet := map[string]bool{"review": true}
	migrations := planMigrations(files, cfg, scaffoldSet)
	require.Len(t, migrations, 1)

	m := migrations[0]
	assert.Equal(t, "review", m.name)

	// Should have harness + associated files.
	fileSet := make(map[string]bool)
	for _, f := range m.files {
		fileSet[f] = true
	}
	assert.True(t, fileSet["harness/review.yaml"])
	assert.True(t, fileSet["agents/review.md"])
	assert.True(t, fileSet["scripts/pre-review.sh"])
	assert.True(t, fileSet["scripts/post-review.sh"])
	assert.True(t, fileSet["policies/review.yaml"])
}

func TestPlanMigrations_NonPerAgentDirBecomesStandalone(t *testing.T) {
	files := []string{
		"harness/review.yaml",
		"docs/review.md",
		"templates/review.yaml",
	}

	cfg := func() config.ConfigWriter {
		data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [fullsend]\nrepos: {}\n")
		c, _ := config.ParseOrgConfig(data)
		return c
	}()

	scaffoldSet := map[string]bool{"review": true}
	migrations := planMigrations(files, cfg, scaffoldSet)
	require.Len(t, migrations, 1)

	m := migrations[0]
	assert.Equal(t, "review", m.name)

	fileSet := make(map[string]bool)
	for _, f := range m.files {
		fileSet[f] = true
	}
	assert.True(t, fileSet["harness/review.yaml"])
	assert.False(t, fileSet["docs/review.md"], "files in non-per-agent dirs should not be associated")
	assert.False(t, fileSet["templates/review.yaml"], "files in non-per-agent dirs should not be associated")

	standalone := findStandaloneFiles(files, migrations)
	standaloneSet := make(map[string]bool)
	for _, f := range standalone {
		standaloneSet[f] = true
	}
	assert.True(t, standaloneSet["docs/review.md"])
	assert.True(t, standaloneSet["templates/review.yaml"])
}

func TestCheckDuplicateDestinations_NoDuplicates(t *testing.T) {
	files := []forge.TreeFile{
		{Path: "harness/review.yaml", Content: []byte("a"), Mode: "100644"},
		{Path: "harness/triage.yaml", Content: []byte("b"), Mode: "100644"},
		{Path: "customized/harness/review.yaml", Delete: true},
	}
	assert.NoError(t, checkDuplicateDestinations(files))
}

func TestCheckDuplicateDestinations_Conflict(t *testing.T) {
	files := []forge.TreeFile{
		{Path: "harness/review.yaml", Content: []byte("a"), Mode: "100644"},
		{Path: "harness/review.yaml", Content: []byte("b"), Mode: "100644"},
	}
	err := checkDuplicateDestinations(files)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration conflict")
	assert.Contains(t, err.Error(), "harness/review.yaml")
}

func TestCheckDuplicateDestinations_DeletesIgnored(t *testing.T) {
	files := []forge.TreeFile{
		{Path: "customized/harness/review.yaml", Delete: true},
		{Path: "customized/harness/review.yaml", Delete: true},
	}
	assert.NoError(t, checkDuplicateDestinations(files))
}

func TestResolveBaseURL_DevCommitSHA(t *testing.T) {
	origSHA := commitSHA
	commitSHA = "dev"
	t.Cleanup(func() { commitSHA = origSHA })

	_, err := resolveBaseURL("review")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine base URL")
}

func TestResolveBaseURL_ValidCommitSHA(t *testing.T) {
	origSHA := commitSHA
	commitSHA = "abc123abc123abc123abc123abc123abc123abc1"
	t.Cleanup(func() { commitSHA = origSHA })

	url, err := resolveBaseURL("review")
	require.NoError(t, err)
	assert.Contains(t, url, commitSHA)
	assert.Contains(t, url, "review")
}

func TestRegisterMigratedAgent_AddsEntryAndAllowlist(t *testing.T) {
	data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [fullsend]\nrepos: {}\n")
	orgCfg, err := config.ParseOrgConfig(data)
	require.NoError(t, err)
	var cfg config.ConfigWriter = orgCfg

	baseURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml"
	registerMigratedAgent(cfg, "triage", baseURL)

	_, found := findAgentByName(cfg.AgentEntries(), "triage")
	assert.True(t, found, "agent should be registered")

	resources := cfg.AllowedResources()
	assert.NotEmpty(t, resources, "allowlist should have an entry")
}

func TestRegisterMigratedAgent_NoDuplicate(t *testing.T) {
	data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [fullsend]\nrepos: {}\nagents:\n  - source: \"harness/review.yaml\"\n")
	orgCfg, err := config.ParseOrgConfig(data)
	require.NoError(t, err)
	var cfg config.ConfigWriter = orgCfg

	before := len(cfg.AgentEntries())
	registerMigratedAgent(cfg, "review", "https://example.com/review.yaml")
	assert.Equal(t, before, len(cfg.AgentEntries()), "should not duplicate existing agent")
}

func TestMigrateCustomizations_DeadOverrideWithNonHarnessFiles(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123abc123abc123abc123abc123abc123abc1/harness/review.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`)
	setupCustomizedDir(t, dir, map[string]string{
		"harness/review.yaml":    "agent: agents/review.md\nmodel: opus\n",
		"agents/review.md":       "Custom review agent prompt.\n",
		"scripts/pre-review.sh":  "#!/bin/sh\necho pre",
		"scripts/post-review.sh": "#!/bin/sh\necho post",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	for _, f := range record.Files {
		assert.True(t, f.Delete, "dead override file %s should be deleted, not moved", f.Path)
		assert.Contains(t, f.Path, "customized/", "dead override deletes should target customized/ paths")
	}
}

func TestMigrateCustomizations_CustomAgent_RewritesPaths(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	setupCustomizedDir(t, dir, map[string]string{
		"harness/explore.yaml": `agent: customized/agents/explore.md
model: opus
pre_script: customized/scripts/pre-explore.sh
post_script: customized/scripts/post-explore.sh
skills:
  - customized/skills/public-research
host_files:
  - src: customized/env/explore-agent.env
    dest: /sandbox/workspace/.env.d/explore-agent.env
`,
		"agents/explore.md":       "You are the explore agent.\n",
		"scripts/pre-explore.sh":  "#!/bin/sh\necho pre\n",
		"scripts/post-explore.sh": "#!/bin/sh\necho post\n",
		"env/explore-agent.env":   "KEY=val\n",
	})

	fc := fakeClientWithRepo("my-org", ".fullsend")
	printer := ui.New(io.Discard)
	err := runMigrateCustomizations(context.Background(), dir, "my-org/.fullsend", false, fc, printer)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	record := fc.CommittedFilesToBranch[0]

	pathMap := make(map[string]forge.TreeFile)
	for _, f := range record.Files {
		pathMap[f.Path] = f
	}

	harnessFile, ok := pathMap["harness/explore.yaml"]
	require.True(t, ok, "harness should be added at regular path")
	content := string(harnessFile.Content)

	assert.NotContains(t, content, "customized/", "customized/ prefixes should be stripped from harness content")
	assert.Contains(t, content, "agent: agents/explore.md")
	assert.Contains(t, content, "pre_script: scripts/pre-explore.sh")
	assert.Contains(t, content, "post_script: scripts/post-explore.sh")
	assert.Contains(t, content, "skills/public-research")
	assert.Contains(t, content, "src: env/explore-agent.env")
}

func TestRewriteCustomizedPaths(t *testing.T) {
	input := `agent: customized/agents/explore.md
doc: customized/agents/explore-doc.md
policy: customized/policies/explore.yaml
pre_script: customized/scripts/pre-explore.sh
post_script: customized/scripts/post-explore.sh
agent_input: customized/schemas/explore-input.json
skills:
  - customized/skills/public-research
  - customized/skills/jira-read
plugins:
  - customized/plugins/my-plugin
host_files:
  - src: customized/env/explore-agent.env
    dest: /sandbox/workspace/.env.d/explore-agent.env
api_servers:
  - name: test-server
    script: customized/scripts/api-server.sh
    port: 8080
validation_loop:
  script: customized/scripts/validate-output-explore.sh
  schema: customized/schemas/explore-output.json
  max_iterations: 3
runner_env:
  FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/customized/schemas/explore-result.schema.json
  GH_TOKEN: "${GH_TOKEN}"
forge:
  github:
    pre_script: customized/scripts/gh-pre.sh
    post_script: customized/scripts/gh-post.sh
    skills:
      - customized/skills/gh-only-skill
    validation_loop:
      script: customized/scripts/gh-validate.sh
      schema: customized/schemas/gh-output.json
      max_iterations: 2
    runner_env:
      GH_SCHEMA: ${FULLSEND_DIR}/customized/schemas/gh.json
`
	rewritten, err := rewriteHarnessContent([]byte(input))
	require.NoError(t, err)
	content := string(rewritten)

	assert.NotContains(t, content, "customized/")
	assert.Contains(t, content, "agent: agents/explore.md")
	assert.Contains(t, content, "doc: agents/explore-doc.md")
	assert.Contains(t, content, "policy: policies/explore.yaml")
	assert.Contains(t, content, "pre_script: scripts/pre-explore.sh")
	assert.Contains(t, content, "post_script: scripts/post-explore.sh")
	assert.Contains(t, content, "agent_input: schemas/explore-input.json")
	assert.Contains(t, content, "skills/public-research")
	assert.Contains(t, content, "skills/jira-read")
	assert.Contains(t, content, "plugins/my-plugin")
	assert.Contains(t, content, "src: env/explore-agent.env")
	assert.Contains(t, content, "script: scripts/api-server.sh")
	assert.Contains(t, content, "script: scripts/validate-output-explore.sh")
	assert.Contains(t, content, "schema: schemas/explore-output.json")
	assert.Contains(t, content, "${FULLSEND_DIR}/schemas/explore-result.schema.json")
	assert.Contains(t, content, "${GH_TOKEN}", "non-customized env values should be unchanged")
	assert.Contains(t, content, "pre_script: scripts/gh-pre.sh", "forge pre_script should be rewritten")
	assert.Contains(t, content, "post_script: scripts/gh-post.sh", "forge post_script should be rewritten")
	assert.Contains(t, content, "skills/gh-only-skill", "forge skills should be rewritten")
	assert.Contains(t, content, "script: scripts/gh-validate.sh", "forge validation_loop.script should be rewritten")
	assert.Contains(t, content, "schema: schemas/gh-output.json", "forge validation_loop.schema should be rewritten")
	assert.Contains(t, content, "${FULLSEND_DIR}/schemas/gh.json", "forge runner_env should be rewritten")
}

func TestRewriteEnvMap_NoFalsePositive(t *testing.T) {
	m := map[string]string{
		"SAFE":    "https://example.com/customized/config",
		"REWRITE": "${FULLSEND_DIR}/customized/schemas/foo.json",
		"PREFIX":  "customized/scripts/bar.sh",
	}
	rewriteEnvMap(m)
	assert.Equal(t, "https://example.com/config", m["SAFE"])
	assert.Equal(t, "${FULLSEND_DIR}/schemas/foo.json", m["REWRITE"])
	assert.Equal(t, "scripts/bar.sh", m["PREFIX"])
}
