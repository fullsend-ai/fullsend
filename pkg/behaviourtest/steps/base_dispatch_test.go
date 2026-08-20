package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// --- givenCustomHarnessWithLocalBase tests ---

func TestGivenCustomHarnessWithLocalBase_EmptyIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		org  string
		repo string
	}{
		{"empty org", "", "repo"},
		{"empty repo", "org", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &world.World{Org: tc.org, RepoName: tc.repo, SCM: &fakeURLSCM{files: map[string][]byte{}}}
			err := givenCustomHarnessWithLocalBase(w, "child", "base", "role: triage")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no repo configured")
		})
	}
}

func TestGivenCustomHarnessWithLocalBase_Validation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenCustomHarnessWithLocalBase(w, "", "base", "doc"))
	require.Error(t, givenCustomHarnessWithLocalBase(w, "child", "", "doc"))
	require.Error(t, givenCustomHarnessWithLocalBase(w, "child", "base", ""))
}

func TestGivenCustomHarnessWithLocalBase_PrependsBaseField(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	err := givenCustomHarnessWithLocalBase(w, "child", "my-base",
		"slug: fullsend-ai-child\ntrigger: |\n  event.entity.kind == \"work_item\"")
	require.NoError(t, err)

	// Verify the harness YAML was committed with base: field prepended.
	harnessData := scm.files["org/repo/.fullsend/harness/child.yaml"]
	require.NotNil(t, harnessData)
	assert.Contains(t, string(harnessData), "base: my-base.yaml\n")
	assert.Contains(t, string(harnessData), "slug: fullsend-ai-child")

	// Verify config was updated.
	cfgData := scm.files["org/repo/.fullsend/config.yaml"]
	cfg, err := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, err)
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "harness/child.yaml", cfg.AgentEntries()[0].Source)

	// Verify DispatchAgent was set.
	assert.Equal(t, "child", w.DispatchAgent)
}

func TestGivenCustomHarnessWithLocalBase_CommitsAgentResource(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      scm,
	}
	// The child doc references an agent — the agent resource should be
	// committed under .fullsend/ on the config repo (from the child's doc,
	// not the base: field).
	err := givenCustomHarnessWithLocalBase(w, "child", "base",
		"agent: agents/triage.md\nslug: child-test")
	require.NoError(t, err)

	agentData := scm.files["org/repo/.fullsend/agents/triage.md"]
	require.NotNil(t, agentData, "agent resource should be committed")
	assert.Equal(t, minimalAgentContent, string(agentData))
}

// --- givenURLSourcedBaseHarness tests ---

func TestGivenURLSourcedBaseHarness_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeURLSCM{files: map[string][]byte{}}}
	err := givenURLSourcedBaseHarness(w, "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenURLSourcedBaseHarness_Validation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenURLSourcedBaseHarness(w, "", "doc"))
	require.Error(t, givenURLSourcedBaseHarness(w, "base", ""))
}

func TestGivenURLSourcedBaseHarness_RequiresHostingRepo(t *testing.T) {
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      &fakeURLSCM{files: map[string][]byte{}},
	}
	err := givenURLSourcedBaseHarness(w, "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness-hosting repo must be created first")
}

func TestGivenURLSourcedBaseHarness_CommitsAndStoresURL(t *testing.T) {
	stubRawHTTPClient(t)
	content := "agent: agents/triage.md\nrole: triage\nslug: fullsend-ai-base"
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "base-host",
	}

	err := givenURLSourcedBaseHarness(w, "remote-base", content)
	require.NoError(t, err)

	// Verify harness was committed to hosting repo.
	harnessData := scm.files["org/base-host/harness/remote-base.yaml"]
	require.NotNil(t, harnessData)
	assert.Equal(t, content, string(harnessData))

	// Verify agent resource was committed to hosting repo.
	agentData := scm.files["org/base-host/agents/triage.md"]
	require.NotNil(t, agentData)
	assert.Equal(t, minimalAgentContent, string(agentData))

	// Verify URL was stored for child steps.
	require.NotNil(t, w.URLBaseHarnesses)
	storedURL, ok := w.URLBaseHarnesses["remote-base"]
	require.True(t, ok)
	expectedURL := fmt.Sprintf(
		"https://raw.githubusercontent.com/org/base-host/main/harness/remote-base.yaml#sha256=%s",
		expectedHash)
	assert.Equal(t, expectedURL, storedURL)

	// Verify NOT registered as an agent in config.yaml.
	cfgData := scm.files["org/repo/.fullsend/config.yaml"]
	cfg, err := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, err)
	assert.Empty(t, cfg.AgentEntries(), "base harness should not be registered as agent")

	// Verify allowlist was updated.
	assert.Contains(t, cfg.AllowedResources(), "https://raw.githubusercontent.com/org/base-host/")
}

func TestGivenURLSourcedBaseHarness_LogsDiagnostics(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	var logged []string
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
		Logf:                func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) },
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.NoError(t, err)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "base")
	assert.Contains(t, logged[0], "rawURL=")
}

func TestGivenURLSourcedBaseHarness_AllowlistDedup(t *testing.T) {
	stubRawHTTPClient(t)
	hostPrefix := "https://raw.githubusercontent.com/org/host/"
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte(fmt.Sprintf(
			"version: \"1\"\nagents: []\nallowed_remote_resources:\n  - %q\n", hostPrefix)),
	}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.NoError(t, err)

	cfgData := scm.files["org/repo/.fullsend/config.yaml"]
	cfg, err := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, err)
	count := 0
	for _, r := range cfg.AllowedResources() {
		if r == hostPrefix {
			count++
		}
	}
	assert.Equal(t, 1, count, "allowlist prefix should not be duplicated")
}

// --- givenCustomHarnessWithURLBase tests ---

func TestGivenCustomHarnessWithURLBase_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeURLSCM{files: map[string][]byte{}}}
	err := givenCustomHarnessWithURLBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenCustomHarnessWithURLBase_Validation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenCustomHarnessWithURLBase(w, "", "base", "doc"))
	require.Error(t, givenCustomHarnessWithURLBase(w, "child", "", "doc"))
	require.Error(t, givenCustomHarnessWithURLBase(w, "child", "base", ""))
}

func TestGivenCustomHarnessWithURLBase_RequiresBaseRegistered(t *testing.T) {
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		SCM:      &fakeURLSCM{files: map[string][]byte{}},
	}
	err := givenCustomHarnessWithURLBase(w, "child", "nonexistent", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGivenCustomHarnessWithURLBase_PrependsBaseURL(t *testing.T) {
	baseURL := "https://raw.githubusercontent.com/org/host/main/harness/base.yaml#sha256=abc123"
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:              "org",
		RepoName:         "repo",
		SCM:              scm,
		URLBaseHarnesses: map[string]string{"remote-base": baseURL},
	}
	err := givenCustomHarnessWithURLBase(w, "child", "remote-base",
		"slug: fullsend-ai-child\ntrigger: |\n  event.entity.kind == \"work_item\"")
	require.NoError(t, err)

	// Verify the harness YAML includes the base: URL.
	harnessData := scm.files["org/repo/.fullsend/harness/child.yaml"]
	require.NotNil(t, harnessData)
	assert.Contains(t, string(harnessData), "base: "+baseURL)
	assert.Contains(t, string(harnessData), "slug: fullsend-ai-child")

	// Verify it was registered as a local-source agent.
	cfgData := scm.files["org/repo/.fullsend/config.yaml"]
	cfg, err := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, err)
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "harness/child.yaml", cfg.AgentEntries()[0].Source)
}

// --- givenURLSourcedCustomHarnessWithURLBase tests ---

func TestGivenURLSourcedCustomHarnessWithURLBase_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{SCM: &fakeURLSCM{files: map[string][]byte{}}}
	err := givenURLSourcedCustomHarnessWithURLBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenURLSourcedCustomHarnessWithURLBase_Validation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenURLSourcedCustomHarnessWithURLBase(w, "", "base", "doc"))
	require.Error(t, givenURLSourcedCustomHarnessWithURLBase(w, "child", "", "doc"))
	require.Error(t, givenURLSourcedCustomHarnessWithURLBase(w, "child", "base", ""))
}

func TestGivenURLSourcedCustomHarnessWithURLBase_RequiresHostingRepo(t *testing.T) {
	w := &world.World{
		Org:              "org",
		RepoName:         "repo",
		SCM:              &fakeURLSCM{files: map[string][]byte{}},
		URLBaseHarnesses: map[string]string{"base": "https://example.com/base.yaml#sha256=abc"},
	}
	err := givenURLSourcedCustomHarnessWithURLBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness-hosting repo must be created first")
}

func TestGivenURLSourcedCustomHarnessWithURLBase_RequiresBaseRegistered(t *testing.T) {
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 &fakeURLSCM{files: map[string][]byte{}},
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedCustomHarnessWithURLBase(w, "child", "nonexistent", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGivenURLSourcedCustomHarnessWithURLBase_PrependsBaseURL(t *testing.T) {
	stubRawHTTPClient(t)
	baseURL := "https://raw.githubusercontent.com/org/host/main/harness/base.yaml#sha256=abc123"
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/org/host/\"\n"),
	}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
		URLBaseHarnesses:    map[string]string{"remote-base": baseURL},
	}

	err := givenURLSourcedCustomHarnessWithURLBase(w, "child", "remote-base",
		"slug: fullsend-ai-child\ntrigger: |\n  event.entity.kind == \"work_item\"")
	require.NoError(t, err)

	// Verify the harness was committed to the hosting repo with the base: URL.
	harnessData := scm.files["org/host/harness/child.yaml"]
	require.NotNil(t, harnessData)
	assert.Contains(t, string(harnessData), "base: "+baseURL)
	assert.Contains(t, string(harnessData), "slug: fullsend-ai-child")

	// Verify it was registered as a URL-sourced agent in config.yaml.
	cfgData := scm.files["org/repo/.fullsend/config.yaml"]
	cfg, err := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, err)
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Contains(t, cfg.AgentEntries()[0].Source, "https://raw.githubusercontent.com/org/host/")
}

// --- registerLocalAgentConfig error-path tests ---

func TestRegisterLocalAgentConfig_GetConfigError(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{}} // no config file
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := registerLocalAgentConfig(context.Background(), w, "test", "commit msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestRegisterLocalAgentConfig_InvalidConfigYAML(t *testing.T) {
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("invalid: [yaml: content"),
	}}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := registerLocalAgentConfig(context.Background(), w, "test", "commit msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}

func TestRegisterLocalAgentConfig_CommitConfigError(t *testing.T) {
	scm := &fakeURLSCM{
		files: map[string][]byte{
			"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
		},
		commitFileErr:  fmt.Errorf("commit failed"),
		commitFileRepo: "repo",
	}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := registerLocalAgentConfig(context.Background(), w, "test", "commit msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating config")
}

func TestRegisterLocalAgentConfig_UpdatesExistingAgent(t *testing.T) {
	// Config already has an agent named "Test" (different case) with a
	// different source. registerLocalAgentConfig should replace it via the
	// case-insensitive DerivedName match, not duplicate.
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents:\n  - name: Test\n    source: old-source.yaml\n"),
	}}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := registerLocalAgentConfig(context.Background(), w, "test", "commit msg")
	require.NoError(t, err)

	cfgData := scm.files["org/repo/.fullsend/config.yaml"]
	cfg, parseErr := config.ParsePerRepoConfig(cfgData)
	require.NoError(t, parseErr)

	// Should have exactly one agent (updated, not duplicated).
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "harness/test.yaml", cfg.AgentEntries()[0].Source)
}

// --- givenCustomHarnessWithLocalBase error-path tests ---

func TestGivenCustomHarnessWithLocalBase_CommitHarnessError(t *testing.T) {
	scm := &fakeURLSCM{
		files:          map[string][]byte{},
		commitFileErr:  fmt.Errorf("commit failed"),
		commitFileRepo: "repo",
	}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := givenCustomHarnessWithLocalBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing harness")
}

func TestGivenCustomHarnessWithLocalBase_InvalidResourceYAML(t *testing.T) {
	// commitLocalHarnessResources fails when parsing an invalid doc YAML.
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := givenCustomHarnessWithLocalBase(w, "child", "base", "invalid: [yaml: content")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing harness YAML")
}

func TestGivenCustomHarnessWithLocalBase_GetConfigError(t *testing.T) {
	// CommitFile for the harness succeeds, commitLocalHarnessResources
	// succeeds (no agent/policy in doc), but registerLocalAgentConfig
	// fails reading the config.
	scm := &fakeURLSCM{files: map[string][]byte{}} // no config file
	w := &world.World{Org: "org", RepoName: "repo", SCM: scm}
	err := givenCustomHarnessWithLocalBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

// --- givenURLSourcedBaseHarness error-path tests ---

func TestGivenURLSourcedBaseHarness_CommitHarnessError(t *testing.T) {
	scm := &fakeURLSCM{
		files:          map[string][]byte{},
		commitFileErr:  fmt.Errorf("commit failed"),
		commitFileRepo: "host",
	}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing base harness to hosting repo")
}

func TestGivenURLSourcedBaseHarness_CommitRelativeResourcesError(t *testing.T) {
	// Use policyFailSCM to fail on the 2nd CommitFile call (agent resource
	// commit inside commitRelativeResources), allowing the 1st call
	// (harness YAML commit) to succeed.
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 &policyFailSCM{fakeURLSCM: scm},
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing base harness resources")
}

func TestGivenURLSourcedBaseHarness_FileNotAccessibleAfterCommit(t *testing.T) {
	speedUpRetries(t)
	scm := &fakeURLSCM{
		files:                map[string][]byte{},
		getFileContentAlways: fmt.Errorf("file not found"),
	}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base harness file not accessible")
}

func TestGivenURLSourcedBaseHarness_RelativeResourceNotAccessible(t *testing.T) {
	speedUpRetries(t)
	calls := 0
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 &selectiveFailSCM{fakeURLSCM: scm, failPath: "agents/triage.md", calls: &calls},
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base harness resource")
	assert.Contains(t, err.Error(), "not accessible")
}

func TestGivenURLSourcedBaseHarness_GetDefaultBranchError(t *testing.T) {
	scm := &fakeURLSCM{
		files:            map[string][]byte{},
		defaultBranchErr: fmt.Errorf("API rate limited"),
	}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting default branch")
	assert.Contains(t, err.Error(), "API rate limited")
}

func TestGivenURLSourcedBaseHarness_RawURLNotAccessible(t *testing.T) {
	stubRawHTTPClientStatus(t, http.StatusNotFound)
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base harness raw URL not accessible")
}

func TestGivenURLSourcedBaseHarness_GetConfigError(t *testing.T) {
	stubRawHTTPClient(t)
	// No config file in the SCM — GetFileContent will fail for the config
	// path, but CommitFile stores the harness/resource files so
	// waitForFileAccessible succeeds.
	scm := &fakeURLSCM{files: map[string][]byte{}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestGivenURLSourcedBaseHarness_InvalidConfigYAML(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("invalid: [yaml: content"),
	}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}

func TestGivenURLSourcedBaseHarness_CommitAllowlistError(t *testing.T) {
	stubRawHTTPClient(t)
	// commitFileRepo targets "repo" (the enrolled repo), so commits to the
	// hosting repo ("host") succeed but the allowlist config commit fails.
	scm := &fakeURLSCM{
		files: map[string][]byte{
			"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
		},
		commitFileErr:  fmt.Errorf("commit failed"),
		commitFileRepo: "repo",
	}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host",
	}
	err := givenURLSourcedBaseHarness(w, "base", "agent: agents/triage.md\nrole: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating config")
}

// --- givenCustomHarnessWithURLBase error-path tests ---

func TestGivenCustomHarnessWithURLBase_CommitHarnessError(t *testing.T) {
	baseURL := "https://raw.githubusercontent.com/org/host/main/harness/base.yaml#sha256=abc123"
	scm := &fakeURLSCM{
		files:          map[string][]byte{},
		commitFileErr:  fmt.Errorf("commit failed"),
		commitFileRepo: "repo",
	}
	w := &world.World{
		Org:              "org",
		RepoName:         "repo",
		SCM:              scm,
		URLBaseHarnesses: map[string]string{"base": baseURL},
	}
	err := givenCustomHarnessWithURLBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing harness")
}

func TestGivenCustomHarnessWithURLBase_InvalidResourceYAML(t *testing.T) {
	baseURL := "https://raw.githubusercontent.com/org/host/main/harness/base.yaml#sha256=abc123"
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:              "org",
		RepoName:         "repo",
		SCM:              scm,
		URLBaseHarnesses: map[string]string{"base": baseURL},
	}
	err := givenCustomHarnessWithURLBase(w, "child", "base", "invalid: [yaml: content")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing harness YAML")
}

func TestGivenCustomHarnessWithURLBase_GetConfigError(t *testing.T) {
	baseURL := "https://raw.githubusercontent.com/org/host/main/harness/base.yaml#sha256=abc123"
	scm := &fakeURLSCM{files: map[string][]byte{}} // no config file
	w := &world.World{
		Org:              "org",
		RepoName:         "repo",
		SCM:              scm,
		URLBaseHarnesses: map[string]string{"base": baseURL},
	}
	err := givenCustomHarnessWithURLBase(w, "child", "base", "role: triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

// --- givenCustomHarnessWithLocalBase / givenCustomHarnessWithURLBase
//     additional coverage: whitespace-only inputs pass TrimSpace as empty ---

func TestGivenCustomHarnessWithLocalBase_WhitespaceValidation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	// Leading/trailing whitespace should be trimmed; if the trimmed value
	// is empty, validation should fail.
	require.Error(t, givenCustomHarnessWithLocalBase(w, "  ", "base", "doc"))
	require.Error(t, givenCustomHarnessWithLocalBase(w, "child", "  ", "doc"))
	require.Error(t, givenCustomHarnessWithLocalBase(w, "child", "base", "  "))
}

func TestGivenCustomHarnessWithURLBase_WhitespaceValidation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenCustomHarnessWithURLBase(w, "  ", "base", "doc"))
	require.Error(t, givenCustomHarnessWithURLBase(w, "child", "  ", "doc"))
	require.Error(t, givenCustomHarnessWithURLBase(w, "child", "base", "  "))
}

func TestGivenURLSourcedCustomHarnessWithURLBase_WhitespaceValidation(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	require.Error(t, givenURLSourcedCustomHarnessWithURLBase(w, "  ", "base", "doc"))
	require.Error(t, givenURLSourcedCustomHarnessWithURLBase(w, "child", "  ", "doc"))
	require.Error(t, givenURLSourcedCustomHarnessWithURLBase(w, "child", "base", "  "))
}
