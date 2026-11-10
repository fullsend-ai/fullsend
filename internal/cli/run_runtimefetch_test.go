package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Fetch-only and bare-declaration harness allowlist tests for fullsend run
// (#9291). They sit in their own file, separate from run_test.go's
// URL-reference cases: the gate under test is shouldStartFetchService, so
// any harness whose runtime fetch service can start has its allowlist
// checked against config.yaml's.
func TestRunAgent_FetchOnlyHarnessCoveredListPassesValidation(t *testing.T) {
	// Acceptance criterion 2: a fetch-only harness whose list the
	// config-level allowlist covers gets past allowlist validation. The
	// run still fails at the sandbox gateway stub ("openshell"), like
	// TestRunAgent_HarnessLoadPipeline — but not on the allowlist.
	useFakeOpenshell(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	writePolicyFixture(t, dir)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nallow_runtime_fetch: true\nallowed_remote_resources:\n  - \"https://allowed.example.com/\"\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"https://allowed.example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", "", rFlags, statusOpts{}, printer, false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
	assert.NotContains(t, err.Error(), "validating allowed remote resources")
}

func TestRunAgent_BareDeclarationRemoteResourcesNotSubset(t *testing.T) {
	// A bare allowed_remote_resources declaration (no allow_runtime_fetch,
	// no URL references) still starts the runtime fetch service for
	// backward compatibility, so the deprecated shape is held to the same
	// config-level subset check too (#9291).
	useFakeOpenshell(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	writePolicyFixture(t, dir)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nallowed_remote_resources:\n  - \"https://disallowed.example.com/\"\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"https://allowed.example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", "", rFlags, statusOpts{}, printer, false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating allowed remote resources")
	assert.Contains(t, err.Error(), `"https://disallowed.example.com/" is not covered by the org allowlist`)
}

func TestRunAgent_FetchOnlyHarnessNoOrgConfigErrors(t *testing.T) {
	// Without config.yaml there is no config-level allowlist to validate
	// against, so run fails closed for a fetch-capable harness exactly as
	// lock does (TestRunLock_FetchOnlyHarnessNoOrgConfigErrors), and the
	// error names the fetch-capable path, not URL references (#9291).
	// A local harness cannot reach this branch without a config (agent
	// resolution needs config entries), so the harness comes from the
	// agents-repo offline cache, as a previous online run would have left
	// it; its references stay local, so it is fetch-only. No
	// useFakeOpenshell here: it neutralizes the agents-repo prefix
	// (#5569), and the run fails at the allowlist gate long before the
	// sandbox gateway anyway.
	dir := t.TempDir()
	sha := strings.Repeat("a", 40)
	fetched := time.Now().UTC().Add(-time.Hour)
	harnessYAML := "agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nallow_runtime_fetch: true\nallowed_remote_resources:\n  - \"https://example.com/\"\n"
	cacheAgentsRepoFile(t, dir, sha, "harness/triage.yaml", []byte(harnessYAML), fetched)
	// The harness's relative agent and policy resolve against its source
	// URL and are rewritten to local cache paths, so the composed harness
	// has no URL references — it is fetch-only. url-index.json maps those
	// companion URLs to their cached content hashes, as the online run
	// that populated the cache would have.
	agentContent := []byte("You are a coding agent.")
	policyContent := []byte("version: 1\n")
	agentURL := cacheAgentsRepoFile(t, dir, sha, "agents/code.md", agentContent, fetched)
	policyURL := cacheAgentsRepoFile(t, dir, sha, "policies/base.yaml", policyContent, fetched)
	index := map[string]string{
		agentURL:  fetch.ComputeSHA256(agentContent),
		policyURL: fetch.ComputeSHA256(policyContent),
	}
	indexData, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".fullsend-cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".fullsend-cache", "url-index.json"), indexData, 0o600))

	// Deliberately do NOT create config.yaml.

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50, offline: true}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	runErr := runAgent(context.Background(), "triage", dir, "", repoDir, "", nil, false, "", "", "", rFlags, statusOpts{}, printer, false, runOverrideFlags{})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "runtime fetch requires a config.yaml with allowed_remote_resources")
	assert.Contains(t, runErr.Error(), "config.yaml")
}

func TestRunAgent_FetchOnlyHarnessMalformedOrgConfigErrors(t *testing.T) {
	// A config.yaml that exists but cannot be parsed is reported with its
	// load error, never as the fetch gate's missing-file message: only an
	// absent config.yaml is "runtime fetch requires" (the previous test).
	// The strict config load at the top of runAgent fails before the gate,
	// so the fetch-only harness setup from that test carries over
	// unchanged — the cached agents-repo harness needs no config entries.
	dir := t.TempDir()
	sha := strings.Repeat("a", 40)
	fetched := time.Now().UTC().Add(-time.Hour)
	harnessYAML := "agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nallow_runtime_fetch: true\nallowed_remote_resources:\n  - \"https://example.com/\"\n"
	cacheAgentsRepoFile(t, dir, sha, "harness/triage.yaml", []byte(harnessYAML), fetched)
	agentContent := []byte("You are a coding agent.")
	policyContent := []byte("version: 1\n")
	agentURL := cacheAgentsRepoFile(t, dir, sha, "agents/code.md", agentContent, fetched)
	policyURL := cacheAgentsRepoFile(t, dir, sha, "policies/base.yaml", policyContent, fetched)
	index := map[string]string{
		agentURL:  fetch.ComputeSHA256(agentContent),
		policyURL: fetch.ComputeSHA256(policyContent),
	}
	indexData, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".fullsend-cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".fullsend-cache", "url-index.json"), indexData, 0o600))

	// A config.yaml that exists but does not parse.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{{invalid yaml"), 0o644))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50, offline: true}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	runErr := runAgent(context.Background(), "triage", dir, "", repoDir, "", nil, false, "", "", "", rFlags, statusOpts{}, printer, false, runOverrideFlags{})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "loading fullsend config")
	assert.NotContains(t, runErr.Error(), "runtime fetch requires")
}
