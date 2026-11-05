package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A current lock entry skips composition, so the guide sends users to
// --update to check reserved keys. --update must compose base and child.
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
}
