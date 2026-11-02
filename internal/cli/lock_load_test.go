package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestLockLoadError(t *testing.T) {
	path := "/tmp/lock.yaml"
	tests := []struct {
		name        string
		err         error
		contains    []string
		notContains []string
	}{
		{
			name: "newer version",
			err:  &lock.UnsupportedVersionError{Version: 99, Expected: 1},
			contains: []string{
				path, "version 99", "version 1", "Upgrade fullsend",
			},
			notContains: []string{"Delete"},
		},
		{
			name:        "zero version",
			err:         &lock.UnsupportedVersionError{Version: 0, Expected: 1},
			contains:    []string{path, "version 0", "Delete"},
			notContains: []string{"Upgrade"},
		},
		{
			name: "parse",
			err:  &lock.ParseError{Err: errors.New("yaml: did not find expected node")},
			contains: []string{
				path, "cannot be parsed", "Delete", "did not find expected node",
			},
		},
		{
			name:        "read",
			err:         fmt.Errorf("reading lock file: %w", os.ErrPermission),
			contains:    []string{path, "could not read"},
			notContains: []string{"Delete", "Upgrade"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lockLoadError(path, tt.err).Error()
			for _, s := range tt.contains {
				assert.Contains(t, got, s)
			}
			for _, s := range tt.notContains {
				assert.NotContains(t, got, s)
			}
		})
	}
}

func TestRunLock_CorruptLockFileAborts(t *testing.T) {
	dir, lockPath, before := setupLockedRemoteHarness(t, []byte("{{corrupt yaml"))

	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be parsed")
	assert.Contains(t, err.Error(), "Delete")
	assertLockUnchanged(t, lockPath, before)
}

func TestRunLock_NewerLockVersionAborts(t *testing.T) {
	contents := []byte("version: 99\ngenerated_at: 2026-06-08T12:00:00Z\nharnesses: {}\n")
	dir, lockPath, before := setupLockedRemoteHarness(t, contents)

	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 99")
	assert.Contains(t, err.Error(), "Upgrade fullsend")
	assert.NotContains(t, err.Error(), "Delete")
	assertLockUnchanged(t, lockPath, before)
}

func TestRunLock_ZeroVersionLockFileAborts(t *testing.T) {
	dir, lockPath, before := setupLockedRemoteHarness(t, []byte(""))

	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 0")
	assert.Contains(t, err.Error(), "Delete")
	assert.NotContains(t, err.Error(), "Upgrade")
	assertLockUnchanged(t, lockPath, before)
}

func TestLockAll_NewerLockVersionAborts(t *testing.T) {
	contents := []byte("version: 99\ngenerated_at: 2026-06-08T12:00:00Z\nharnesses: {}\n")
	dir, lockPath, before := setupLockedRemoteHarness(t, contents)

	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, ui.New(os.Stdout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 99")
	assert.Contains(t, err.Error(), "Upgrade fullsend")
	assertLockUnchanged(t, lockPath, before)
}

func TestRunLock_MissingLockFileStillProceeds(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})
	dir := setupLockTestDir(t, srv, agentHash, policyHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
	require.NoError(t, err)

	lf, loadErr := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, loadErr)
	require.NotNil(t, lf)
	require.NotNil(t, lf.Lookup("code"))
}

func setupLockedRemoteHarness(t *testing.T, lockContents []byte) (dir, lockPath string, before []byte) {
	t.Helper()
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})
	dir = setupLockTestDir(t, srv, agentHash, policyHash)

	lockPath = filepath.Join(dir, "lock.yaml")
	require.NoError(t, os.WriteFile(lockPath, lockContents, 0o644))
	before = append([]byte(nil), lockContents...)

	fetch.DefaultPolicy = policy
	t.Cleanup(func() { fetch.DefaultPolicy = fetch.FetchPolicy{} })
	return dir, lockPath, before
}

func assertLockUnchanged(t *testing.T, lockPath string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "unreadable lock file must be left unchanged")
}
