package harness

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func canonTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func TestRegisteredAgents_Valid(t *testing.T) {
	cfg := &config.DirConfig{
		Agents: []config.AgentEntry{
			{Source: "harness/triage.yaml"},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	agents, err := RegisteredAgents(cfg)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "triage", agents[0].Name)
}

func TestRegisteredAgents_Empty(t *testing.T) {
	agents, err := RegisteredAgents(&config.DirConfig{})
	require.NoError(t, err)
	assert.Nil(t, agents)
}

func TestRegisteredAgents_NilConfig(t *testing.T) {
	_, err := RegisteredAgents(nil)
	require.Error(t, err)
}

func TestRegisteredAgents_TypedNilConfig(t *testing.T) {
	// A typed nil (e.g. (*DirConfig)(nil) passed as ConfigReader) should
	// be caught by the nil guard, not cause a panic on first method call.
	var cfg config.ConfigReader = (*config.DirConfig)(nil)
	_, err := RegisteredAgents(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestRegisteredAgents_SkipsDisabled(t *testing.T) {
	f := false
	cfg := &config.DirConfig{
		Agents: []config.AgentEntry{
			{Source: "harness/triage.yaml"},
			{Name: "review", Source: "harness/review.yaml", Enabled: &f},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	agents, err := RegisteredAgents(cfg)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "triage", agents[0].Name)
}

func TestRegisteredAgents_RejectsThreeEntryChain(t *testing.T) {
	f := false
	tr := true
	cfg := &config.DirConfig{
		Agents: []config.AgentEntry{
			{Name: "retro", Source: "harness/retro-v1.yaml", Enabled: &tr},
			{Name: "retro", Enabled: &f},
			{Name: "retro", Source: "harness/retro-v2.yaml", Enabled: &tr},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	_, err := RegisteredAgents(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestRegisteredAgents_DuplicateName(t *testing.T) {
	cfg := &config.DirConfig{
		Agents: []config.AgentEntry{
			{Name: "Ping", Source: "harness/a.yaml"},
			{Name: "ping", Source: "harness/b.yaml"},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	_, err := RegisteredAgents(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestResolveRegisteredPath_LocalValid(t *testing.T) {
	dir := canonTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "agent.yaml"), []byte("test"), 0o644))

	resolved, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "harness/agent.yaml"}, nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "agent.yaml"), resolved.Path)
}

func TestResolveRegisteredPath_AbsoluteRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "/etc/evil.yaml"}, nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be relative, not absolute")
}

func TestResolveRegisteredPath_TraversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "harness/../../etc/passwd"}, nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes config directory")
}

func TestResolveRegisteredPath_DotSegmentsCleaned(t *testing.T) {
	dir := canonTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "agent.yaml"), []byte("test"), 0o644))

	resolved, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "harness/./agent.yaml"}, nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "agent.yaml"), resolved.Path)
}

func TestResolveRegisteredPath_SymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "evil.yaml"), []byte("pwned"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(outside, "evil.yaml"), filepath.Join(dir, "harness", "evil.yaml")))

	_, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "harness/evil.yaml"}, nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes config directory via symlink")
}

func TestResolveRegisteredPath_MissingLocalFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "harness/missing.yaml"}, nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness path")
}

func TestResolveRegisteredPath_SymlinkWithinDir(t *testing.T) {
	dir := canonTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	target := filepath.Join(dir, "harness", "agent.yaml")
	require.NoError(t, os.WriteFile(target, []byte("test"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "harness", "link.yaml")))

	resolved, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: "harness/link.yaml"}, nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, target, resolved.Path)
}

func TestResolveRegisteredPath_URL(t *testing.T) {
	content := []byte("role: triage\n")
	hash := fetch.ComputeSHA256(content)
	commitSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/org/repo/"+commitSHA+"/harness/triage.yaml" {
			w.Write(content)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	t.Cleanup(func() { fetch.DefaultPolicy = origPolicy })

	dir := t.TempDir()
	rawURL := srv.URL + "/org/repo/" + commitSHA + "/harness/triage.yaml#sha256=" + hash
	opts := ComposeOpts{
		WorkspaceRoot: dir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{srv.URL + "/"},
	}

	resolved, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: rawURL}, opts.OrgAllowlist, opts)
	require.NoError(t, err)
	assert.NotEmpty(t, resolved.Path)
	assert.NotEmpty(t, resolved.Dep.URL)
}

func TestResolveRegisteredPath_URLNotAllowlisted(t *testing.T) {
	dir := t.TempDir()
	rawURL := "https://evil.example.com/org/repo/sha/harness/triage.yaml#sha256=" + strings.Repeat("a", 64)
	_, err := ResolveRegisteredPath(context.Background(), dir, config.AgentEntry{Source: rawURL}, []string{"https://example.com/"}, ComposeOpts{WorkspaceRoot: dir})
	require.Error(t, err)
}
