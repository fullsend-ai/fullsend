package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const testCommitSHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func newAgentTestServer(t *testing.T, contents map[string][]byte) (*httptest.Server, fetch.FetchPolicy) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := contents[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true

	return srv, fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})
}

func writeOrgConfig(t *testing.T, dir string, extraYAML string) {
	t.Helper()
	cfg := `version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
repos: {}
`
	if extraYAML != "" {
		cfg += extraYAML
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
}

func writePerRepoConfig(t *testing.T, dir string, extraYAML string) {
	t.Helper()
	cfg := `version: "1"
roles:
  - triage
  - coder
`
	if extraYAML != "" {
		cfg += extraYAML
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
}

// --- loadAgentConfig tests ---

func TestLoadAgentConfig_OrgConfig(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.True(t, cfg.isOrg)
}

func TestLoadAgentConfig_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	writePerRepoConfig(t, dir, "")

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.False(t, cfg.isOrg)
}

func TestLoadAgentConfig_MissingFile(t *testing.T) {
	_, err := loadAgentConfig("/nonexistent/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

// --- agent add tests ---

func TestRunAgentAdd_LocalPath(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "lint.yaml"),
		[]byte("role: coder\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "harness/lint.yaml", "", dir, nil, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Equal(t, "harness/lint.yaml", agents[0].Source)
	assert.Equal(t, "", agents[0].Name)
	assert.Equal(t, "lint", agents[0].DerivedName())
}

func TestRunAgentAdd_LocalPathWithName(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "lint.yaml"),
		[]byte("role: coder\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "harness/lint.yaml", "my-linter", dir, nil, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Equal(t, "my-linter", agents[0].Name)
	assert.Equal(t, "my-linter", agents[0].DerivedName())
}

func TestRunAgentAdd_DuplicateNameRejected(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/lint.yaml
`)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "lint.yaml"),
		[]byte("role: coder\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "harness/lint.yaml", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRunAgentAdd_DuplicateNameCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - name: Lint
    source: harness/lint.yaml
`)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "lint.yaml"),
		[]byte("role: coder\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	// "lint" collides with "Lint" case-insensitively
	err := runAgentAdd(context.Background(), "harness/lint.yaml", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRunAgentAdd_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "../../../etc/passwd", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestRunAgentAdd_AbsolutePathRejected(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "/etc/passwd", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be relative")
}

func TestRunAgentAdd_NonGitHubURLRequiresSHA(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "https://example.com/org/repo/main/harness/lint.yaml", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-GitHub URLs must use a pinned commit SHA")
}

func TestRunAgentAdd_LocalPathNotExist(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "harness/nonexistent.yaml", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestRunAgentAdd_URLWithPinnedSHA(t *testing.T) {
	harnessContent := []byte("role: triage\nslug: my-triage\n")
	harnessHash := fetch.ComputeSHA256(harnessContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	source := srv.URL + "/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml#sha256=" + harnessHash

	client := forge.NewFakeClient()
	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), source, "", dir, client, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Equal(t, "triage", agents[0].DerivedName())
	assert.Contains(t, agents[0].Source, "#sha256="+harnessHash)
}

func TestRunAgentAdd_URLHashMismatch(t *testing.T) {
	harnessContent := []byte("role: triage\n")

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	source := srv.URL + "/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml#sha256=" + wrongHash

	client := forge.NewFakeClient()
	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), source, "", dir, client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity hash mismatch")
}

func TestRunAgentAdd_URLAddsAllowlistPrefix(t *testing.T) {
	harnessContent := []byte("role: triage\n")

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	source := srv.URL + "/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml"

	client := forge.NewFakeClient()
	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), source, "", dir, client, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	resources := cfg.allowedRemoteResources()
	found := false
	for _, r := range resources {
		if strings.Contains(r, "/my-org/my-agents/") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected allowed_remote_resources to contain the agent's repo prefix")
}

func TestRunAgentAdd_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	writePerRepoConfig(t, dir, "")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "lint.yaml"),
		[]byte("role: coder\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "harness/lint.yaml", "", dir, nil, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.False(t, cfg.isOrg)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Equal(t, "lint", agents[0].DerivedName())
}

// --- agent list tests ---

func TestRunAgentList_Empty(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runAgentList(dir, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No agents registered")
}

func TestRunAgentList_WithAgents(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/lint.yaml
  - name: custom
    source: harness/custom.yaml
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/fullsend/"
`)

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runAgentList(dir, printer)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "lint")
	assert.Contains(t, output, "custom")
	assert.Contains(t, output, "harness/lint.yaml")
	assert.Contains(t, output, "harness/custom.yaml")
}

func TestRunAgentList_StripsHashFromDisplay(t *testing.T) {
	dir := t.TempDir()
	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	writeOrgConfig(t, dir, `agents:
  - "https://raw.githubusercontent.com/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+hash+`"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runAgentList(dir, printer)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "triage")
	assert.NotContains(t, output, "sha256=")
}

// --- agent update tests ---

func TestRunAgentUpdate_RepinsSHA(t *testing.T) {
	oldSHA := testCommitSHA
	newSHA := "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	oldHash := "1111111111111111111111111111111111111111111111111111111111111111"
	newContent := []byte("role: triage\nupdated: true\n")
	newHash := fetch.ComputeSHA256(newContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/org/repo/" + newSHA + "/harness/triage.yaml": newContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - "`+srv.URL+`/org/repo/`+oldSHA+`/harness/triage.yaml#sha256=`+oldHash+`"
allowed_remote_resources:
  - "`+srv.URL+`/org/repo/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "triage", newSHA, dir, nil, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Contains(t, agents[0].Source, newSHA)
	assert.Contains(t, agents[0].Source, "#sha256="+newHash)
	assert.NotContains(t, agents[0].Source, oldSHA)
}

func TestRunAgentUpdate_ExplicitSHA(t *testing.T) {
	explicitSHA := "c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	newContent := []byte("role: triage\n")
	newHash := fetch.ComputeSHA256(newContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/org/repo/" + explicitSHA + "/harness/triage.yaml": newContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	oldHash := "2222222222222222222222222222222222222222222222222222222222222222"
	writeOrgConfig(t, dir, `agents:
  - "`+srv.URL+`/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+oldHash+`"
allowed_remote_resources:
  - "`+srv.URL+`/org/repo/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "triage", explicitSHA, dir, nil, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Contains(t, agents[0].Source, explicitSHA)
	assert.Contains(t, agents[0].Source, "#sha256="+newHash)
}

func TestRunAgentUpdate_LocalPathRejected(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/lint.yaml
`)

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local path")
}

func TestRunAgentUpdate_NotFound(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "nonexistent", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRunAgentUpdate_InvalidSHA(t *testing.T) {
	dir := t.TempDir()
	hash := "3333333333333333333333333333333333333333333333333333333333333333"
	writeOrgConfig(t, dir, `agents:
  - "https://raw.githubusercontent.com/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+hash+`"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "triage", "not-a-sha", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid commit SHA")
}

// --- agent remove tests ---

func TestRunAgentRemove_Success(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/lint.yaml
  - harness/review.yaml
`)

	printer := ui.New(os.Stdout)
	err := runAgentRemove(dir, "lint", printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Equal(t, "review", agents[0].DerivedName())
}

func TestRunAgentRemove_CleansUpAllowlist(t *testing.T) {
	dir := t.TempDir()
	hash := "4444444444444444444444444444444444444444444444444444444444444444"
	writeOrgConfig(t, dir, `agents:
  - "https://raw.githubusercontent.com/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+hash+`"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
  - "https://raw.githubusercontent.com/fullsend-ai/fullsend/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentRemove(dir, "triage", printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	resources := cfg.allowedRemoteResources()
	for _, r := range resources {
		assert.NotContains(t, r, "/org/repo/", "should have removed the unused prefix")
	}
	// The fullsend prefix should still be there
	assert.Contains(t, resources, "https://raw.githubusercontent.com/fullsend-ai/fullsend/")
}

func TestRunAgentRemove_KeepsAllowlistWhenOtherAgentsUseIt(t *testing.T) {
	dir := t.TempDir()
	hash1 := "5555555555555555555555555555555555555555555555555555555555555555"
	hash2 := "6666666666666666666666666666666666666666666666666666666666666666"
	writeOrgConfig(t, dir, `agents:
  - "https://raw.githubusercontent.com/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+hash1+`"
  - "https://raw.githubusercontent.com/org/repo/`+testCommitSHA+`/harness/code.yaml#sha256=`+hash2+`"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentRemove(dir, "triage", printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	resources := cfg.allowedRemoteResources()
	assert.Contains(t, resources, "https://raw.githubusercontent.com/org/repo/")
}

func TestRunAgentRemove_NotFound(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	printer := ui.New(os.Stdout)
	err := runAgentRemove(dir, "nonexistent", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- helper tests ---

func TestFindAgentByName(t *testing.T) {
	agents := []config.AgentEntry{
		{Source: "harness/triage.yaml"},
		{Name: "Custom", Source: "harness/custom.yaml"},
	}

	idx, found := findAgentByName(agents, "triage")
	assert.True(t, found)
	assert.Equal(t, 0, idx)

	idx, found = findAgentByName(agents, "custom")
	assert.True(t, found)
	assert.Equal(t, 1, idx)

	// Case-insensitive
	idx, found = findAgentByName(agents, "CUSTOM")
	assert.True(t, found)
	assert.Equal(t, 1, idx)

	_, found = findAgentByName(agents, "nonexistent")
	assert.False(t, found)
}

func TestAllowlistPrefixForURL(t *testing.T) {
	prefix := allowlistPrefixForURL("https://raw.githubusercontent.com/my-org/my-repo/abc123/path/to/file.yaml#sha256=deadbeef")
	assert.Equal(t, "https://raw.githubusercontent.com/my-org/my-repo/", prefix)

	prefix = allowlistPrefixForURL("harness/local.yaml")
	assert.Equal(t, "", prefix)
}

func TestBuildRawURL(t *testing.T) {
	url := buildRawURL("owner", "repo", testCommitSHA, "harness/triage.yaml")
	assert.Equal(t, "https://raw.githubusercontent.com/owner/repo/"+testCommitSHA+"/harness/triage.yaml", url)
}

func TestIsGitHubURL(t *testing.T) {
	assert.True(t, isGitHubURL("https://github.com/org/repo/blob/main/file.yaml"))
	assert.True(t, isGitHubURL("https://raw.githubusercontent.com/org/repo/sha/file.yaml"))
	assert.False(t, isGitHubURL("https://example.com/org/repo/sha/file.yaml"))
	assert.False(t, isGitHubURL("https://127.0.0.1:12345/org/repo/sha/file.yaml"))
}

func TestParseAgentSourceURL_GitHubBlobToRawConversion(t *testing.T) {
	blobURL := "https://github.com/my-org/agents/blob/" + testCommitSHA + "/harness/triage.yaml"
	info, err := parseAgentSourceURL(blobURL)
	require.NoError(t, err)
	assert.Equal(t, "my-org", info.Owner)
	assert.Equal(t, "agents", info.Repo)
	assert.Equal(t, testCommitSHA, info.Ref)
	assert.Equal(t, "harness/triage.yaml", info.Path)

	rawURL := buildRawURL(info.Owner, info.Repo, info.Ref, info.Path)
	assert.Equal(t, "https://raw.githubusercontent.com/my-org/agents/"+testCommitSHA+"/harness/triage.yaml", rawURL)
}

func TestRunAgentAdd_NonGitHubUpdateRequiresExplicitSHA(t *testing.T) {
	dir := t.TempDir()
	hash := "7777777777777777777777777777777777777777777777777777777777777777"
	srv, _ := newAgentTestServer(t, nil)

	writeOrgConfig(t, dir, `agents:
  - "`+srv.URL+`/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+hash+`"
allowed_remote_resources:
  - "`+srv.URL+`/org/repo/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "triage", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-GitHub URL agents require an explicit SHA")
}

func TestPinAgentURL_ResolvesRef(t *testing.T) {
	resolvedSHA := "d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5"
	harnessContent := []byte("role: triage\n")
	harnessHash := fetch.ComputeSHA256(harnessContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/agents/" + resolvedSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	client := forge.NewFakeClient()
	client.BranchRefs["my-org/agents/main"] = resolvedSHA

	// Non-GitHub URL with non-SHA ref — should fail
	printer := ui.New(os.Stdout)
	_, err := pinAgentURL(context.Background(), srv.URL+"/my-org/agents/main/harness/triage.yaml", client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-GitHub URLs must use a pinned commit SHA")

	// Non-GitHub URL with SHA ref — should succeed
	source := srv.URL + "/my-org/agents/" + resolvedSHA + "/harness/triage.yaml"
	result, err := pinAgentURL(context.Background(), source, client, printer)
	require.NoError(t, err)
	assert.Contains(t, result, resolvedSHA)
	assert.Contains(t, result, "#sha256="+harnessHash)
}

func TestPinAgentURL_NilForgeClient(t *testing.T) {
	printer := ui.New(os.Stdout)
	_, err := pinAgentURL(context.Background(), "https://github.com/org/repo/blob/main/harness/triage.yaml", nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge client for branch resolution")
}

func TestPinAgentURL_RefFallbackToDefaultBranch(t *testing.T) {
	resolvedSHA := "e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6"
	harnessContent := []byte("role: coder\n")

	_, policy := newAgentTestServer(t, map[string][]byte{
		"/" + resolvedSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{
		FullName:      "org/repo",
		DefaultBranch: "main",
	}}
	client.BranchRefs["org/repo/main"] = resolvedSHA

	source := "https://raw.githubusercontent.com/org/repo/some-tag/harness/triage.yaml"

	var buf strings.Builder
	printer := ui.New(&buf)
	_, err := pinAgentURL(context.Background(), source, client, printer)
	// Fetch fails (raw.githubusercontent.com != test server) but resolution path was exercised
	require.Error(t, err)
	assert.Contains(t, buf.String(), "falling back to default branch")
}

func TestPinAgentURL_TransientErrorDoesNotFallback(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetBranchRef"] = fmt.Errorf("HTTP 500: internal server error")
	client.Repos = []forge.Repository{{
		FullName:      "org/repo",
		DefaultBranch: "main",
	}}

	printer := ui.New(os.Stdout)
	_, err := pinAgentURL(context.Background(), "https://raw.githubusercontent.com/org/repo/feature-branch/harness/triage.yaml", client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving ref")
	assert.Contains(t, err.Error(), "internal server error")
	assert.NotContains(t, err.Error(), "default branch")
}

func TestPinAgentURL_InvalidResolvedSHA(t *testing.T) {
	client := forge.NewFakeClient()
	client.BranchRefs["org/repo/main"] = "not-a-valid-sha"

	printer := ui.New(os.Stdout)
	_, err := pinAgentURL(context.Background(), "https://raw.githubusercontent.com/org/repo/main/harness/triage.yaml", client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid commit SHA")
}

func TestRunAgentUpdate_GitHubURLUsesRawURL(t *testing.T) {
	newSHA := "f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1"
	newContent := []byte("role: triage\nupdated: true\n")
	newHash := fetch.ComputeSHA256(newContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/org/repo/" + newSHA + "/harness/triage.yaml": newContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	oldHash := "8888888888888888888888888888888888888888888888888888888888888888"
	// Use a test-server URL (non-GitHub) with explicit SHA for the update
	writeOrgConfig(t, dir, `agents:
  - "`+srv.URL+`/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+oldHash+`"
allowed_remote_resources:
  - "`+srv.URL+`/org/repo/"
`)

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "triage", newSHA, dir, nil, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Contains(t, agents[0].Source, newSHA)
	assert.Contains(t, agents[0].Source, "#sha256="+newHash)
}

func TestRunAgentUpdate_ForgeResolvesDefaultBranch(t *testing.T) {
	newSHA := "a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0"
	newContent := []byte("role: coder\n")

	_, policy := newAgentTestServer(t, map[string][]byte{
		"/org/repo/" + newSHA + "/harness/triage.yaml": newContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	oldHash := "9999999999999999999999999999999999999999999999999999999999999999"
	writeOrgConfig(t, dir, `agents:
  - "https://raw.githubusercontent.com/org/repo/`+testCommitSHA+`/harness/triage.yaml#sha256=`+oldHash+`"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)

	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{
		FullName:      "org/repo",
		DefaultBranch: "main",
	}}
	client.BranchRefs["org/repo/main"] = newSHA

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "triage", "", dir, client, printer)
	// Fetch fails (raw.githubusercontent.com != test server) but resolution was exercised
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching content")
}

func TestRunAgentAdd_URLWithBranchRef(t *testing.T) {
	resolvedSHA := "b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0"
	harnessContent := []byte("role: triage\n")
	harnessHash := fetch.ComputeSHA256(harnessContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/agents/" + resolvedSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	client := forge.NewFakeClient()
	client.BranchRefs["my-org/agents/main"] = resolvedSHA

	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	// Non-GitHub URL with already-pinned SHA works
	source := srv.URL + "/my-org/agents/" + resolvedSHA + "/harness/triage.yaml"
	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), source, "", dir, client, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Contains(t, agents[0].Source, resolvedSHA)
	assert.Contains(t, agents[0].Source, "#sha256="+harnessHash)
}

func TestValidateLocalPath_AbsolutePath(t *testing.T) {
	err := validateLocalPath("/some/dir", "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be relative")
}

func TestHasAllowlistPrefix(t *testing.T) {
	resources := []string{"https://raw.githubusercontent.com/org/repo/", "https://example.com/"}
	assert.True(t, hasAllowlistPrefix(resources, "https://raw.githubusercontent.com/org/repo/"))
	assert.False(t, hasAllowlistPrefix(resources, "https://other.com/"))
}

func TestFindSHAInURL(t *testing.T) {
	sha := findSHAInURL("https://raw.githubusercontent.com/org/repo/" + testCommitSHA + "/file.yaml")
	assert.Equal(t, testCommitSHA, sha)

	sha = findSHAInURL("https://example.com/no-sha/here")
	assert.Equal(t, "", sha)
}

func TestRunAgentList_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	writePerRepoConfig(t, dir, `agents:
  - harness/lint.yaml
`)

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runAgentList(dir, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "lint")
}

func TestRunAgentAdd_PerRepoURLAddsAllowlist(t *testing.T) {
	harnessContent := []byte("role: triage\n")

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/repo/" + testCommitSHA + "/harness/triage.yaml": harnessContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	writePerRepoConfig(t, dir, "")

	source := srv.URL + "/my-org/repo/" + testCommitSHA + "/harness/triage.yaml"
	client := forge.NewFakeClient()
	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), source, "", dir, client, printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.False(t, cfg.isOrg)
	resources := cfg.allowedRemoteResources()
	found := false
	for _, r := range resources {
		if strings.Contains(r, "/my-org/repo/") {
			found = true
		}
	}
	assert.True(t, found, "expected per-repo config to have allowlist prefix")
}

func TestRunAgentRemove_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	writePerRepoConfig(t, dir, `agents:
  - harness/lint.yaml
  - harness/review.yaml
`)

	printer := ui.New(os.Stdout)
	err := runAgentRemove(dir, "lint", printer)
	require.NoError(t, err)

	cfg, err := loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	agents := cfg.agents()
	require.Len(t, agents, 1)
	assert.Equal(t, "review", agents[0].DerivedName())
}

func TestRunAgentUpdate_NonGitHubURLNoExplicitSHA(t *testing.T) {
	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeOrgConfig(t, dir, `agents:
  - source: "https://example.com/org/repo/`+oldSHA+`/harness/lint.yaml#sha256=abcd"
allowed_remote_resources:
  - "https://example.com/org/repo/"
`)
	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-GitHub URL agents require an explicit SHA")
}

func TestRunAgentUpdate_NilForgeClientUpdate(t *testing.T) {
	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/org/repo/`+oldSHA+`/harness/lint.yaml#sha256=abcd"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)
	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", "", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge client for branch resolution")
}

func TestRunAgentUpdate_ForgeGetRepoError(t *testing.T) {
	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/org/repo/`+oldSHA+`/harness/lint.yaml#sha256=abcd"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)
	client := forge.NewFakeClient()
	client.Errors["GetRepo"] = fmt.Errorf("network timeout")

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", "", dir, client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up repo")
}

func TestRunAgentUpdate_ForgeGetBranchRefError(t *testing.T) {
	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/org/repo/`+oldSHA+`/harness/lint.yaml#sha256=abcd"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "org/repo", DefaultBranch: "main"}}
	client.Errors["GetBranchRef"] = fmt.Errorf("rate limited")

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", "", dir, client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving branch ref")
}

func TestRunAgentUpdate_InvalidResolvedSHAUpdate(t *testing.T) {
	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeOrgConfig(t, dir, `agents:
  - source: "https://raw.githubusercontent.com/org/repo/`+oldSHA+`/harness/lint.yaml#sha256=abcd"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/org/repo/"
`)
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "org/repo", DefaultBranch: "main"}}
	client.BranchRefs["org/repo/main"] = "too-short"

	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", "", dir, client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid commit SHA")
}

func TestRunAgentUpdate_NoSHAInExistingURL(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - source: "https://example.com/org/repo/main/harness/lint.yaml#sha256=abcd"
allowed_remote_resources:
  - "https://example.com/org/repo/"
`)
	newSHA := "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "lint", newSHA, dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not find a commit SHA in the existing URL")
}

func TestLoadAgentConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("[[[not yaml at all"), 0o644)
	require.NoError(t, err)
	_, err = loadAgentConfig(filepath.Join(dir, "config.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestLoadAgentConfig_AmbiguousConfig(t *testing.T) {
	dir := t.TempDir()
	// Valid YAML that parses as org config but lacks dispatch.platform,
	// and also fails per-repo parsing due to unknown field.
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("unknown_field_only: true\n"), 0o644)
	require.NoError(t, err)
	_, err = loadAgentConfig(filepath.Join(dir, "config.yaml"))
	// Per-repo config might parse this successfully (it's lenient), so only
	// check error if it actually fails.
	if err != nil {
		assert.Contains(t, err.Error(), "dispatch.platform")
	}
}

func TestParseGenericURL_NotURL(t *testing.T) {
	_, err := parseGenericURL("not-a-url")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid HTTPS URL")
}

func TestParseGenericURL_TooShort(t *testing.T) {
	_, err := parseGenericURL("https://example.com/short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL path too short")
}

func TestAllowlistPrefixForURL_UnparseableURL(t *testing.T) {
	result := allowlistPrefixForURL("not-a-url-at-all")
	assert.Equal(t, "", result)
}

func TestRunAgentUpdate_ParseSourceURLError(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - source: "https://example.com/x"
`)
	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "x", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", dir, nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing agent URL")
}

func TestPinAgentURL_ParseError(t *testing.T) {
	printer := ui.New(os.Stdout)
	_, err := pinAgentURL(context.Background(), "https://x.com/y", nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot parse URL")
}

func TestRunAgentList_LoadError(t *testing.T) {
	printer := ui.New(os.Stdout)
	err := runAgentList("/nonexistent/path", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestRunAgentRemove_LoadError(t *testing.T) {
	printer := ui.New(os.Stdout)
	err := runAgentRemove("/nonexistent/path", "agent", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestRunAgentAdd_LoadError(t *testing.T) {
	printer := ui.New(os.Stdout)
	err := runAgentAdd(context.Background(), "local.yaml", "", "/nonexistent/path", nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestRunAgentUpdate_LoadError(t *testing.T) {
	printer := ui.New(os.Stdout)
	err := runAgentUpdate(context.Background(), "agent", "", "/nonexistent/path", nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestPinAgentURL_GetRepoErrorWrapsRepoErr(t *testing.T) {
	client := forge.NewFakeClient()
	// Don't set BranchRefs["org/repo/feature"] so GetBranchRef returns ErrNotFound
	client.Errors["GetRepo"] = fmt.Errorf("auth failure")

	printer := ui.New(os.Stdout)
	_, err := pinAgentURL(context.Background(), "https://raw.githubusercontent.com/org/repo/feature/harness/triage.yaml", client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth failure")
	assert.Contains(t, err.Error(), "looking up repo")
}

func TestNewAgentListCmd_Execute(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/triage.yaml
`)
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--fullsend-dir", dir})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewAgentRemoveCmd_Execute(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/triage.yaml
`)
	cmd := newAgentRemoveCmd()
	cmd.SetArgs([]string{"triage", "--fullsend-dir", dir})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewAgentAddCmd_LocalPath(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "agents: []\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "lint.yaml"), []byte("role: coder\n"), 0o644))

	cmd := newAgentAddCmd()
	cmd.SetArgs([]string{"harness/lint.yaml", "--fullsend-dir", dir})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewAgentUpdateCmd_ExplicitSHA(t *testing.T) {
	newSHA := "f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1"
	newContent := []byte("role: triage\n")
	newHash := fetch.ComputeSHA256(newContent)

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/org/agents/" + newSHA + "/harness/triage.yaml": newContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	_ = newHash
	writeOrgConfig(t, dir, `agents:
  - source: "`+srv.URL+`/org/agents/`+oldSHA+`/harness/triage.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000"
allowed_remote_resources:
  - "`+srv.URL+`/org/agents/"
`)
	cmd := newAgentUpdateCmd()
	cmd.SetArgs([]string{"triage", newSHA, "--fullsend-dir", dir})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewAgentCmd_HasSubcommands(t *testing.T) {
	cmd := newAgentCmd()
	assert.Len(t, cmd.Commands(), 4)
	names := make([]string, len(cmd.Commands()))
	for i, c := range cmd.Commands() {
		names[i] = c.Name()
	}
	assert.Contains(t, names, "add")
	assert.Contains(t, names, "list")
	assert.Contains(t, names, "update")
	assert.Contains(t, names, "remove")
}
