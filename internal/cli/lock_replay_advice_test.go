package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// TestResolveFromLock_CacheMissAdviceNamesHarness checks that the cache-miss
// error resolveFromLock returns advises the lock-update command its caller
// passes — runAgent builds it with the shared lockUpdateHint, so the advice
// names the harness and succeeds as written. The bare `fullsend lock` the
// message used to print fails without an agent name and, with the name added,
// still re-locks nothing while the entry is current.
func TestResolveFromLock_CacheMissAdviceNamesHarness(t *testing.T) {
	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "agent",
				URL:    "https://example.com/agents/code.md",
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "https://example.com/agents/code.md#sha256=0000000000000000000000000000000000000000000000000000000000000000",
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, lockUpdateHint("code", "/custom/dir", "", ""), t.TempDir(), nil, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in cache")
	assert.Contains(t, err.Error(), "fullsend lock code --update")
	assert.Contains(t, err.Error(), "--fullsend-dir")
	assert.NotContains(t, err.Error(), "'fullsend lock'")
}

// TestResolveFromLock_AdviceArmsNameHarness pins the remaining advice arms
// of resolveFromLock — the allowlist rejection, the directory cache-miss and
// the plugins missing-lock-entry error — on the lock-update command its
// caller passes. runAgent builds that command with the shared lockUpdateHint,
// so each message names the harness and succeeds as written; the file
// cache-miss arm is pinned by TestResolveFromLock_CacheMissAdviceNamesHarness
// above.
func TestResolveFromLock_AdviceArmsNameHarness(t *testing.T) {
	const missing = "0000000000000000000000000000000000000000000000000000000000000000"
	agentURL := "https://example.com/agents/code.md"

	// The plugins case needs one resolvable dependency so the replay reaches
	// the plugin pre-validation: seed the agent's content in the cache and
	// pin its hash, while the harness's plugin URL has no lock entry.
	seeded := func(t *testing.T, root string) (*lock.HarnessLock, *harness.Harness) {
		t.Helper()
		content := []byte("agent v1")
		require.NoError(t, fetch.CachePut(root, agentURL, content))
		entry := &lock.HarnessLock{
			Dependencies: []lock.DependencyEntry{
				{Field: "agent", URL: agentURL, SHA256: fetch.ComputeSHA256(content)},
			},
		}
		h := &harness.Harness{
			Agent:                  agentURL + "#sha256=" + fetch.ComputeSHA256(content),
			AllowedRemoteResources: []string{"https://example.com/"},
			Plugins:                []harness.PluginSpec{{Path: "https://example.com/plugins/p"}},
		}
		return entry, h
	}

	cases := []struct {
		name    string
		wantErr string
		build   func(t *testing.T, root string) (*lock.HarnessLock, *harness.Harness)
	}{
		{
			name:    "allowlist rejection advises the named command",
			wantErr: "no longer in allowed_remote_resources",
			build: func(t *testing.T, root string) (*lock.HarnessLock, *harness.Harness) {
				t.Helper()
				return &lock.HarnessLock{
					Dependencies: []lock.DependencyEntry{
						{Field: "agent", URL: agentURL, SHA256: missing},
					},
				}, &harness.Harness{
					Agent:                  agentURL + "#sha256=" + missing,
					AllowedRemoteResources: []string{"https://allowed.example.com/"},
				}
			},
		},
		{
			name:    "directory cache-miss advises the named command",
			wantErr: "not in cache",
			build: func(t *testing.T, root string) (*lock.HarnessLock, *harness.Harness) {
				t.Helper()
				return &lock.HarnessLock{
					Dependencies: []lock.DependencyEntry{
						{Field: "agent", URL: agentURL, SHA256: missing, Type: "directory"},
					},
				}, &harness.Harness{
					Agent:                  agentURL + "#sha256=" + missing,
					AllowedRemoteResources: []string{"https://example.com/"},
				}
			},
		},
		{
			name:    "plugins missing lock entry advises the named command",
			wantErr: "has no entry in the lock file",
			build:   seeded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			entry, h := tc.build(t, root)
			printer := ui.New(os.Stdout)
			_, err := resolveFromLock(h, entry, lockUpdateHint("code", "/custom/dir", "", ""), root, nil, printer)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "fullsend lock code --update")
			assert.Contains(t, err.Error(), "--fullsend-dir")
		})
	}
}

// TestRunAgent_LockReplayCacheMissAdviceNamesHarness pins the production
// wiring: runAgent passes composeOpts.LockUpdateCommand (the shared
// lockUpdateHint) to resolveFromLock, so a lock-replay cache-miss during a
// run advises a command that names the harness. The child declares its own
// URL policy — composition localizes only the base's relative files, so the
// child's URL survives to the resolve step, where a current lock entry is
// replayed. The policy's pinned content leaves the cache (a pruned cache or
// a fresh machine), the replay reports the miss with the named advice and
// the run falls back to normal resolution. A regression of the run.go
// argument to a bare literal (or an empty string) fails here while every
// other test passes.
func TestRunAgent_LockReplayCacheMissAdviceNamesHarness(t *testing.T) {
	usePreScriptStub(t)
	skip := []byte("#!/bin/bash\necho \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n")
	baseContent := []byte("agent: agents/triage.md\npolicy: policies/sandbox.yaml\nrole: test\npre_script: scripts/pre-code.sh\n")
	childPolicy := []byte("version: 1\n# child policy\n")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/base.yaml":     baseContent,
		"/agents/triage.md":      []byte("# base agent\n"),
		"/policies/sandbox.yaml": []byte("version: 1\n"),
		"/scripts/pre-code.sh":   skip,
		"/policies/child.yaml":   childPolicy,
	})

	dir := t.TempDir()
	writeTestFiles(t, dir, map[string]string{
		"harness/code.yaml": fmt.Sprintf(
			"base: %q\npolicy: %q\n",
			srv.URL+"/harness/base.yaml#sha256="+fetch.ComputeSHA256(baseContent),
			srv.URL+"/policies/child.yaml#sha256="+fetch.ComputeSHA256(childPolicy)),
		"config.yaml": fmt.Sprintf("agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - %q\n", srv.URL+"/"),
	})

	fetch.DefaultPolicy = policy
	t.Cleanup(func() { fetch.DefaultPolicy = fetch.FetchPolicy{} })

	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout)))

	dropCachedContent(t, dir, fetch.ComputeSHA256(childPolicy))

	var out bytes.Buffer
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "not in cache")
	assert.Contains(t, out.String(), "fullsend lock code --update")
	assert.Contains(t, out.String(), "--fullsend-dir")
}
