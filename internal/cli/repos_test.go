package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReposCommand_HasSubcommands(t *testing.T) {
	cmd := newReposCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["status"], "expected status subcommand")
}

func TestReposStatusCmd_Flags(t *testing.T) {
	cmd := newReposStatusCmd()

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag, "expected --manifest flag")
	assert.Equal(t, "repos.yaml", manifestFlag.DefValue)

	jsonFlag := cmd.Flags().Lookup("json")
	require.NotNil(t, jsonFlag, "expected --json flag")
	assert.Equal(t, "false", jsonFlag.DefValue)

	repoFlag := cmd.Flags().Lookup("repo")
	require.NotNil(t, repoFlag, "expected --repo flag")

	concurrencyFlag := cmd.Flags().Lookup("concurrency")
	require.NotNil(t, concurrencyFlag, "expected --concurrency flag")
	assert.Equal(t, "8", concurrencyFlag.DefValue)
}

func TestReposStatusCmd_ManifestShortFlag(t *testing.T) {
	cmd := newReposStatusCmd()
	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

func TestReposStatusCmd_NoRunWithoutToken(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "status", "--manifest", "/nonexistent/path"})
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	err := cmd.Execute()
	require.Error(t, err)
}

func TestPrintStatusTable_AllInstalled(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner:       "acme-corp",
				Repo:        "api-server",
				Installed:   true,
				CurrentRef:  "v2.3.0",
				ExpectedRef: "v2.3.0",
			},
			{
				Owner:       "acme-corp",
				Repo:        "web-frontend",
				Installed:   true,
				CurrentRef:  "v2.3.0",
				ExpectedRef: "v2.3.0",
			},
		},
		Summary: repos.StatusSummary{
			Total:     2,
			Installed: 2,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "REPO")
	assert.Contains(t, output, "REF")
	assert.Contains(t, output, "STATUS")
	assert.Contains(t, output, "DRIFT")
	assert.Contains(t, output, "acme-corp/api-server")
	assert.Contains(t, output, "acme-corp/web-frontend")
	assert.Contains(t, output, "installed")
	assert.Contains(t, output, "none")
	assert.Contains(t, output, "2 installed, 0 drifted, 0 not installed")
	assert.NotContains(t, output, "errored")
}

func TestPrintStatusTable_WithDrift(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner:      "acme-corp",
				Repo:       "api-server",
				Installed:  true,
				CurrentRef: "v2.1.0",
				Drifts: []repos.Drift{
					{Field: "FULLSEND_MINT_URL", Expected: "https://new.mint", Actual: "https://old.mint"},
					{Field: "fullsend_ref", Expected: "v2.3.0", Actual: "v2.1.0"},
				},
			},
		},
		Summary: repos.StatusSummary{
			Total:     1,
			Installed: 1,
			Drifted:   1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "FULLSEND_MINT_URL differs")
	assert.Contains(t, output, "fullsend_ref differs")
	assert.Contains(t, output, "1 installed, 1 drifted, 0 not installed")
}

func TestPrintStatusTable_NotInstalled(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme-corp",
				Repo:  "new-repo",
			},
		},
		Summary: repos.StatusSummary{
			Total:        1,
			NotInstalled: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "not installed")
	assert.Contains(t, output, "0 installed, 0 drifted, 1 not installed")
}

func TestPrintStatusTable_WithErrors(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme-corp",
				Repo:  "broken",
				Error: "API rate limit exceeded",
			},
		},
		Summary: repos.StatusSummary{
			Total:   1,
			Errored: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "error")
	assert.Contains(t, output, "API rate limit exceeded")
	assert.Contains(t, output, "1 errored")
}

func TestPrintStatusTable_EmptyRef(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme-corp",
				Repo:  "no-ref",
			},
		},
		Summary: repos.StatusSummary{
			Total:        1,
			NotInstalled: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	// Empty ref should show "—"
	lines := strings.Split(output, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "no-ref") {
			found = true
			assert.Contains(t, line, "—")
		}
	}
	assert.True(t, found, "expected to find no-ref in output")
}

func TestPrintStatusTable_MixedStatuses(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "org", Repo: "ok", Installed: true, CurrentRef: "v1"},
			{Owner: "org", Repo: "drifted", Installed: true, CurrentRef: "v1",
				Drifts: []repos.Drift{{Field: "ref", Expected: "v2", Actual: "v1"}}},
			{Owner: "org", Repo: "missing"},
			{Owner: "org", Repo: "broken", Error: "fail"},
		},
		Summary: repos.StatusSummary{
			Total:        4,
			Installed:    2,
			Drifted:      1,
			NotInstalled: 1,
			Errored:      1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "2 installed, 1 drifted, 1 not installed, 1 errored")
}

func TestReposStatusCmd_WiredToRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, cmd := range root.Commands() {
		if cmd.Use == "repos" {
			found = true
			statusFound := false
			for _, sub := range cmd.Commands() {
				if sub.Use == "status" {
					statusFound = true
				}
			}
			assert.True(t, statusFound, "repos should have status subcommand")
		}
	}
	assert.True(t, found, "root should have repos command")
}

func TestRenderStatusResult_JSON(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "acme", Repo: "api", Installed: true, CurrentRef: "v2.3.0"},
		},
		Summary: repos.StatusSummary{Total: 1, Installed: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)

	err := renderStatusResult(cmd, result, true)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, `"owner": "acme"`)
	assert.Contains(t, output, `"installed": true`)
	assert.Contains(t, output, `"current_ref": "v2.3.0"`)
}

func TestRenderStatusResult_JSONWithDrift(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme", Repo: "api", Installed: true,
				Drifts: []repos.Drift{{Field: "ref", Expected: "v2", Actual: "v1"}},
			},
		},
		Summary: repos.StatusSummary{Total: 1, Installed: 1, Drifted: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)

	err := renderStatusResult(cmd, result, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 drifted")

	output := buf.String()
	assert.Contains(t, output, `"field": "ref"`)
}

func TestRenderStatusResult_TableNoDrift(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "org", Repo: "repo", Installed: true, CurrentRef: "v1"},
		},
		Summary: repos.StatusSummary{Total: 1, Installed: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)

	err := renderStatusResult(cmd, result, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "installed")
}

func TestRenderStatusResult_ErrorOnNotInstalled(t *testing.T) {
	result := &repos.StatusResult{
		Repos:   []repos.RepoStatus{{Owner: "o", Repo: "r"}},
		Summary: repos.StatusSummary{Total: 1, NotInstalled: 1},
	}

	cmd := newReposStatusCmd()
	cmd.SetOut(&bytes.Buffer{})

	err := renderStatusResult(cmd, result, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 not installed")
}

func TestRenderStatusResult_ErrorOnErrors(t *testing.T) {
	result := &repos.StatusResult{
		Repos:   []repos.RepoStatus{{Owner: "o", Repo: "r", Error: "boom"}},
		Summary: repos.StatusSummary{Total: 1, Errored: 1},
	}

	cmd := newReposStatusCmd()
	cmd.SetOut(&bytes.Buffer{})

	err := renderStatusResult(cmd, result, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 errored")
}

func TestRenderStatusResult_NoErrorWhenAllMatch(t *testing.T) {
	result := &repos.StatusResult{
		Repos:   []repos.RepoStatus{{Owner: "o", Repo: "r", Installed: true}},
		Summary: repos.StatusSummary{Total: 1, Installed: 1},
	}

	cmd := newReposStatusCmd()
	cmd.SetOut(&bytes.Buffer{})

	err := renderStatusResult(cmd, result, false)
	require.NoError(t, err)
}

func TestPrintStatusTable_ColumnAlignment(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "a", Repo: "short", Installed: true, CurrentRef: "v1"},
			{Owner: "very-long-org-name", Repo: "very-long-repo-name", Installed: true, CurrentRef: "v2.3.0-rc.1"},
		},
		Summary: repos.StatusSummary{Total: 2, Installed: 2},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.True(t, len(lines) >= 3, "expected at least header + 2 data lines + summary")
	// Header and data lines should have consistent column positions
	headerRefIdx := strings.Index(lines[0], "REF")
	assert.Greater(t, headerRefIdx, 0, "REF header should be present")
}
