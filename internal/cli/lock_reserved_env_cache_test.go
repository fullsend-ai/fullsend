package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A current lock entry skips composition as well as dependency resolution.
// Make that explicit rather than suggesting the absence of a warning means
// env.sandbox was checked. A missing base makes accidental composition fail;
// neither the single-harness nor --all path may rewrite the existing lock.
func TestLockCurrentEntry_ReportsSkippedDiagnostics(t *testing.T) {
	t.Parallel()

	for _, all := range []bool{false, true} {
		name := "single"
		if all {
			name = "all"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
			content := []byte("base: unavailable.yaml\nagent: https://example.invalid/agent.md\nrole: test\n" +
				"env:\n  sandbox:\n    HTTPS_PROXY: http://proxy.example:8080\n")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), content, 0o644))

			lockPath := filepath.Join(dir, "lock.yaml")
			lf := &lock.LockFile{Harnesses: map[string]lock.HarnessLock{
				"code": {
					Source: "harness/code.yaml", SHA256: fetch.ComputeSHA256(content),
					ResolvedAt: time.Unix(1, 0).UTC(),
					Dependencies: []lock.DependencyEntry{{
						Field: "agent", URL: "https://example.invalid/agent.md", SHA256: "not-in-cache",
					}},
				},
			}}
			require.NoError(t, lock.Save(lockPath, lf))
			before, err := os.ReadFile(lockPath)
			require.NoError(t, err)
			stamp := time.Unix(1, 0)
			require.NoError(t, os.Chtimes(lockPath, stamp, stamp))
			beforeInfo, err := os.Stat(lockPath)
			require.NoError(t, err)

			var out bytes.Buffer
			printer := ui.New(&out)
			if all {
				err = runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
			} else {
				err = runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer)
			}
			require.NoError(t, err)
			assert.Contains(t, out.String(), "up to date")
			assert.Contains(t, out.String(), "Harness diagnostics for code were skipped")
			assert.Contains(t, out.String(), "--update to check reserved env.sandbox keys")
			assert.NotContains(t, out.String(), "HTTPS_PROXY", "no composed-key check was performed")

			after, err := os.ReadFile(lockPath)
			require.NoError(t, err)
			afterInfo, err := os.Stat(lockPath)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a diagnostic notice must not refresh dependencies")
			assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), "the lock must not be rewritten")
			_, err = os.Stat(filepath.Join(dir, ".fullsend-cache"))
			assert.True(t, os.IsNotExist(err), "a cache hit must not compose or fetch resources")
		})
	}
}

func TestLockCurrentEntry_UpdateReportsReservedKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	content := []byte("base: base.yaml\nenv:\n  sandbox:\n    HTTPS_PROXY: http://proxy.example:8080\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), content, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "base.yaml"),
		[]byte("agent: agents/code.md\nrole: test\nenv:\n  sandbox:\n    PATH: /tmp/bin\n    MY_TOOL_SETTING: keep-me\n"), 0o644))
	lf := &lock.LockFile{Harnesses: map[string]lock.HarnessLock{
		"code": {Source: "harness/code.yaml", SHA256: fetch.ComputeSHA256(content)},
	}}

	var out bytes.Buffer
	_, err := lockOneAgent(context.Background(), "code", dir, "", true, lf, resolveFlags{}, ui.New(&out))
	require.NoError(t, err)
	assert.Contains(t, out.String(), "2 env.sandbox key(s) are reserved")
	assert.Contains(t, out.String(), "HTTPS_PROXY, PATH", "--update must check the composed base and child")
	assert.NotContains(t, out.String(), "MY_TOOL_SETTING")
	assert.NotContains(t, out.String(), "diagnostics for code were skipped")
}
