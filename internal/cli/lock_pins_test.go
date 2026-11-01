package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// TestReadRunLock_PinsURLBaseFiles locks a harness with a URL
// base, moves the URL index for the base's relative files to other content,
// and checks that an offline load with the lock's pins still resolves the
// locked content (#8979).
func TestReadRunLock_PinsURLBaseFiles(t *testing.T) {
	agentContent := []byte("locked agent")
	policyContent := []byte("locked: policy")
	srvFiles := map[string][]byte{
		"/agents/triage.md":      agentContent,
		"/policies/sandbox.yaml": policyContent,
	}
	baseContent := []byte("agent: agents/triage.md\npolicy: policies/sandbox.yaml\nrole: test\n")
	srvFiles["/harness/triage.yaml"] = baseContent
	srv, policy := newLockTestServer(t, srvFiles)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	harnessPath := filepath.Join(dir, "harness", "code.yaml")
	require.NoError(t, os.WriteFile(harnessPath, []byte(fmt.Sprintf(
		"role: test\nbase: %s/harness/triage.yaml#sha256=%s\n", srv.URL, fetch.ComputeSHA256(baseContent))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout)))

	agentURL := srv.URL + "/agents/triage.md"
	policyURL := srv.URL + "/policies/sandbox.yaml"
	pins := runLockPins(t, dir, "code", harnessPath)
	assert.Equal(t, fetch.ComputeSHA256(agentContent), pins[agentURL].SHA256)
	assert.Equal(t, fetch.ComputeSHA256(policyContent), pins[policyURL].SHA256)

	// The URL index moves after locking, as when another harness re-fetches
	// a branch-ref base online.
	indexPath := filepath.Join(dir, ".fullsend-cache", "url-index.json")
	for u, content := range map[string][]byte{agentURL: []byte("moved agent"), policyURL: []byte("moved: policy")} {
		require.NoError(t, fetch.CachePut(dir, u, content))
		moveURLIndexEntry(t, indexPath, u, fetch.ComputeSHA256(content))
	}

	h, _, err := harness.LoadWithBase(context.Background(), harnessPath, harness.ComposeOpts{
		WorkspaceRoot: dir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{srv.URL + "/"},
		LockedPins:    pins,
	})
	require.NoError(t, err)
	agentData, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, agentContent, agentData)
	policyData, err := os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, policyContent, policyData)
}

// runLockPins returns the pins readRunLock builds for agentName.
func runLockPins(t *testing.T, dir, agentName, harnessPath string) map[string]harness.LockedPin {
	t.Helper()
	st, err := readRunLock(dir, agentName, harnessPath)
	require.NoError(t, err)
	return st.pins
}

// moveURLIndexEntry repoints one url-index.json entry.
func moveURLIndexEntry(t *testing.T, indexPath, rawURL, hash string) {
	t.Helper()
	data, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	var index map[string]string
	require.NoError(t, json.Unmarshal(data, &index))
	require.Contains(t, index, rawURL)
	index[rawURL] = hash
	out, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(indexPath, out, 0o600))
}

func TestReadRunLock_NoPins(t *testing.T) {
	harnessContent := []byte("role: test\n")
	harnessHash := fetch.ComputeSHA256(harnessContent)

	setup := func(t *testing.T, lf *lock.LockFile) (string, string) {
		t.Helper()
		dir := t.TempDir()
		harnessPath := filepath.Join(dir, "harness", "code.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(harnessPath), 0o755))
		require.NoError(t, os.WriteFile(harnessPath, harnessContent, 0o644))
		if lf != nil {
			require.NoError(t, lock.Save(filepath.Join(dir, "lock.yaml"), lf))
		}
		return dir, harnessPath
	}
	entry := func(sha string) *lock.LockFile {
		return &lock.LockFile{Harnesses: map[string]lock.HarnessLock{"code": {
			SHA256: sha,
			Dependencies: []lock.DependencyEntry{
				{Field: "agent", URL: "https://example.com/agents/triage.md", SHA256: fetch.ComputeSHA256([]byte("a"))},
				{Field: "policy", URL: "https://example.com/policies/sandbox.yaml"},
				{Field: "skills[0]", URL: "https://example.com/skills/common/SKILL.md", SHA256: fetch.ComputeSHA256([]byte("t")), Type: "directory"},
			},
		}}}
	}

	t.Run("no lock file", func(t *testing.T) {
		dir, harnessPath := setup(t, nil)
		st, err := readRunLock(dir, "code", harnessPath)
		require.NoError(t, err)
		assert.Equal(t, runLockState{}, st)
	})

	t.Run("unreadable lock file", func(t *testing.T) {
		dir, harnessPath := setup(t, nil)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "lock.yaml"), []byte("version: [\n"), 0o644))
		st, err := readRunLock(dir, "code", harnessPath)
		require.NoError(t, err)
		assert.Error(t, st.loadErr)
		assert.Nil(t, st.pins)
	})

	t.Run("no entry for the agent", func(t *testing.T) {
		dir, harnessPath := setup(t, entry(harnessHash))
		st, err := readRunLock(dir, "other", harnessPath)
		require.NoError(t, err)
		assert.Equal(t, runLockState{}, st)
	})

	t.Run("stale entry", func(t *testing.T) {
		dir, harnessPath := setup(t, entry(fetch.ComputeSHA256([]byte("older harness"))))
		st, err := readRunLock(dir, "code", harnessPath)
		require.NoError(t, err)
		assert.True(t, st.stale)
		assert.NotNil(t, st.entry)
		assert.Nil(t, st.pins)
	})

	t.Run("missing harness file", func(t *testing.T) {
		dir, _ := setup(t, entry(harnessHash))
		_, err := readRunLock(dir, "code", filepath.Join(dir, "harness", "missing.yaml"))
		assert.ErrorContains(t, err, "reading harness file for lock check")
	})

	t.Run("current entry keeps each pin's type and skips entries without a hash", func(t *testing.T) {
		dir, harnessPath := setup(t, entry(harnessHash))
		assert.Equal(t, map[string]harness.LockedPin{
			"https://example.com/agents/triage.md":       {SHA256: fetch.ComputeSHA256([]byte("a"))},
			"https://example.com/skills/common/SKILL.md": {SHA256: fetch.ComputeSHA256([]byte("t")), Type: "directory"},
		}, runLockPins(t, dir, "code", harnessPath))
	})
}

// TestLock_ConfigURLAgentPinsOwnRelativeFiles locks a config-registered URL
// agent whose harness names its agent and policy by relative path, moves
// their URL index entries, and checks that the offline load runAgent does
// with the lock's pins uses the locked content.
func TestLock_ConfigURLAgentPinsOwnRelativeFiles(t *testing.T) {
	agentContent := []byte("locked agent")
	policyContent := []byte("locked: policy")
	harnessContent := []byte("agent: agents/code.md\npolicy: policies/sandbox.yaml\nrole: test\n")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/code.yaml":     harnessContent,
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := t.TempDir()
	harnessURL := fmt.Sprintf("%s/harness/code.yaml#sha256=%s", srv.URL, fetch.ComputeSHA256(harnessContent))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"agents:\n  - name: code\n    source: \"%s\"\nallowed_remote_resources:\n  - \"%s/\"\n", harnessURL, srv.URL)), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout)))

	agentURL := srv.URL + "/agents/code.md"
	policyURL := srv.URL + "/policies/sandbox.yaml"
	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	require.NotNil(t, entry.LookupDep(agentURL), "lock must record the URL agent's relative agent file")
	require.NotNil(t, entry.LookupDep(policyURL), "lock must record the URL agent's relative policy file")

	indexPath := filepath.Join(dir, ".fullsend-cache", "url-index.json")
	for u, content := range map[string][]byte{agentURL: []byte("moved agent"), policyURL: []byte("moved: policy")} {
		require.NoError(t, fetch.CachePut(dir, u, content))
		moveURLIndexEntry(t, indexPath, u, fetch.ComputeSHA256(content))
	}

	offline := fetch.FetchPolicy{Offline: true}
	orgCfg, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	harnessPath, sourceDeps, err := resolveHarnessForLock(context.Background(), dir, "code", orgCfg, resolveFlags{}, offline, ui.New(os.Stdout))
	require.NoError(t, err)
	require.Len(t, sourceDeps, 1)

	load := func(t *testing.T) *harness.Harness {
		t.Helper()
		h, _, err := harness.LoadWithBase(context.Background(), harnessPath, harness.ComposeOpts{
			WorkspaceRoot: dir,
			FetchPolicy:   offline,
			OrgAllowlist:  []string{srv.URL + "/"},
			SourceURL:     sourceDeps[0].URL,
			LockedPins:    runLockPins(t, dir, "code", harnessPath),
		})
		require.NoError(t, err)
		return h
	}

	t.Run("locked content", func(t *testing.T) {
		h := load(t)
		assert.Equal(t, string(agentContent), readFileString(t, h.Agent))
		assert.Equal(t, string(policyContent), readFileString(t, h.Policy))
	})

	t.Run("lock written without the relative files keeps the index behaviour", func(t *testing.T) {
		old := *entry
		old.Dependencies = nil
		for _, d := range entry.Dependencies {
			if d.Field == "agent_source" {
				old.Dependencies = append(old.Dependencies, d)
			}
		}
		require.Len(t, old.Dependencies, 1)
		lf.SetHarness("code", old)
		require.NoError(t, lock.Save(filepath.Join(dir, "lock.yaml"), lf))

		h := load(t)
		assert.Equal(t, "moved agent", readFileString(t, h.Agent))
		assert.Equal(t, "moved: policy", readFileString(t, h.Policy))
	})
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(data)
}

// TestRunAgent_ReportsLockPinning checks that runAgent reports pinning, a
// stale entry and an unreadable lock file before composition, including
// for a harness with no URL references.
func TestRunAgent_ReportsLockPinning(t *testing.T) {
	usePreScriptStub(t)
	tests := []struct {
		name string
		lock func(t *testing.T, dir string)
		want string
	}{
		{"current entry", func(t *testing.T, dir string) {
			writeRunLock(t, dir, harnessHashOf(t, dir))
		}, "Pinning 1 dependency from lock file"},
		{"stale entry", func(t *testing.T, dir string) {
			writeRunLock(t, dir, fetch.ComputeSHA256([]byte("older harness")))
		}, "Harness has changed since lock file was generated"},
		{"unreadable lock file", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "lock.yaml"), []byte("version: [\n"), 0o644))
		}, "Could not load lock file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")
			tt.lock(t, dir)
			var out bytes.Buffer
			err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
				resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
			require.NoError(t, err)
			assert.Contains(t, out.String(), tt.want)
		})
	}
}

func harnessHashOf(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "harness", "code.yaml"))
	require.NoError(t, err)
	return fetch.ComputeSHA256(data)
}

func writeRunLock(t *testing.T, dir, harnessHash string) {
	t.Helper()
	require.NoError(t, lock.Save(filepath.Join(dir, "lock.yaml"), &lock.LockFile{Harnesses: map[string]lock.HarnessLock{"code": {
		SHA256: harnessHash,
		Dependencies: []lock.DependencyEntry{
			{Field: "agent", URL: "https://example.com/agents/code.md", SHA256: fetch.ComputeSHA256([]byte("a"))},
		},
	}}}))
}

// TestRunAgent_WarnsWhenLockEntryLeavesDependenciesUnpinned checks that a
// current entry written before a URL agent's own files were locked asks for
// `fullsend lock --update`, and a fully pinned entry does not (#8979).
func TestRunAgent_WarnsWhenLockEntryLeavesDependenciesUnpinned(t *testing.T) {
	usePreScriptStub(t)
	harnessContent := []byte("agent: agents/code.md\npolicy: policies/sandbox.yaml\nrole: test\npre_script: scripts/pre-code.sh\n")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/code.yaml":     harnessContent,
		"/agents/code.md":        []byte("locked agent"),
		"/policies/sandbox.yaml": []byte("version: 1\n"),
		"/scripts/pre-code.sh":   []byte("#!/bin/bash\necho \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n"),
	})

	dir := t.TempDir()
	harnessURL := fmt.Sprintf("%s/harness/code.yaml#sha256=%s", srv.URL, fetch.ComputeSHA256(harnessContent))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"agents:\n  - name: code\n    source: \"%s\"\nallowed_remote_resources:\n  - \"%s/\"\n", harnessURL, srv.URL)), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout)))
	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)

	run := func(t *testing.T) string {
		t.Helper()
		var out bytes.Buffer
		err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
			resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
		require.NoError(t, err)
		return out.String()
	}

	t.Run("fully pinned entry", func(t *testing.T) {
		assert.NotContains(t, run(t), "fullsend lock --update")
	})

	t.Run("entry with only agent_source", func(t *testing.T) {
		old := *entry
		old.Dependencies = nil
		for _, d := range entry.Dependencies {
			if d.Field == "agent_source" {
				old.Dependencies = append(old.Dependencies, d)
			}
		}
		require.Len(t, old.Dependencies, 1)
		lf.SetHarness("code", old)
		require.NoError(t, lock.Save(lockPath, lf))

		out := run(t)
		assert.Contains(t, out, "Lock entry for code does not pin 3 dependencies")
		assert.Contains(t, out, "fullsend lock --update")
	})
}

// TestRunAgent_UnpinnedWarningNamesForgePlatform checks that a thin child
// over a base with forge.github files, locked without --forge, is told to
// lock with --forge github when a github run composes those files (#8979).
func TestRunAgent_UnpinnedWarningNamesForgePlatform(t *testing.T) {
	usePreScriptStub(t)
	skip := []byte("#!/bin/bash\necho \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n")
	baseContent := []byte("agent: agents/code.md\npolicy: policies/sandbox.yaml\nrole: test\npre_script: scripts/pre-code.sh\nforge:\n  github:\n    pre_script: scripts/gh.sh\n")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/base.yaml":     baseContent,
		"/agents/code.md":        []byte("locked agent"),
		"/policies/sandbox.yaml": []byte("version: 1\n"),
		"/scripts/pre-code.sh":   skip,
		"/scripts/gh.sh":         skip,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(fmt.Sprintf(
		"role: test\nbase: %s/harness/base.yaml#sha256=%s\n", srv.URL, fetch.ComputeSHA256(baseContent))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"%s/\"\n", srv.URL)), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout)))

	run := func(t *testing.T) string {
		t.Helper()
		var out bytes.Buffer
		err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "github", "",
			resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
		require.NoError(t, err)
		return out.String()
	}

	out := run(t)
	assert.Contains(t, out, "fullsend lock --update --forge github code")

	require.NoError(t, runLock(context.Background(), "code", dir, "github", true, resolveFlags{}, ui.New(os.Stdout)))
	assert.NotContains(t, run(t), "fullsend lock --update")
}

// A child that declares several forges is locked for all of them without
// --forge, so the hint must not narrow the entry to the run's platform.
func TestRunAgent_UnpinnedWarningOmitsForgeForMultiForgeChild(t *testing.T) {
	usePreScriptStub(t)
	skip := []byte("#!/bin/bash\necho \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n")
	baseContent := []byte("agent: agents/code.md\npolicy: policies/sandbox.yaml\nrole: test\npre_script: scripts/pre-code.sh\n")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/base.yaml":     baseContent,
		"/agents/code.md":        []byte("locked agent"),
		"/policies/sandbox.yaml": []byte("version: 1\n"),
		"/scripts/pre-code.sh":   skip,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(fmt.Sprintf(
		"role: test\nbase: %s/harness/base.yaml#sha256=%s\nforge:\n  github:\n    env:\n      A: \"1\"\n  gitlab:\n    env:\n      A: \"2\"\n",
		srv.URL, fetch.ComputeSHA256(baseContent))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"%s/\"\n", srv.URL)), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout)))

	// Drop the agent pin, as an entry written before it was locked would.
	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	entry := *lf.Lookup("code")
	var kept []lock.DependencyEntry
	for _, d := range entry.Dependencies {
		if d.Field != "agent" {
			kept = append(kept, d)
		}
	}
	require.Len(t, kept, len(entry.Dependencies)-1)
	entry.Dependencies = kept
	lf.SetHarness("code", entry)
	require.NoError(t, lock.Save(lockPath, lf))

	var out bytes.Buffer
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "github", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{}))
	assert.Contains(t, out.String(), "fullsend lock --update code")
	assert.NotContains(t, out.String(), "--forge")
}
