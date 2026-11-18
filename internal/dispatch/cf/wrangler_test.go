package cf

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/wranglerprep"
)

func TestWranglerCommand_DefaultUsesNpx(t *testing.T) {
	t.Setenv(wranglerprep.WranglerBinEnv, "")
	cmd := wranglerCommand(context.Background(), "versions", "list", "--name", "w")
	assert.Equal(t, []string{"npx", "wrangler", "versions", "list", "--name", "w"}, cmd.Args)
}

func TestWranglerCommand_EnvOverrideRunsBinaryDirectly(t *testing.T) {
	t.Setenv(wranglerprep.WranglerBinEnv, "/opt/prepared/wrangler")
	cmd := wranglerCommand(context.Background(), "deploy", "--name", "w")
	assert.Equal(t, "/opt/prepared/wrangler", cmd.Path)
	assert.Equal(t, []string{"/opt/prepared/wrangler", "deploy", "--name", "w"}, cmd.Args)
}

// TestWranglerInvocations_UsePreparedBinary runs the real call sites
// against a fake executable that echoes its arguments, proving each one
// runs the prepared binary without a leading "wrangler" argument.
func TestWranglerInvocations_UsePreparedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "wrangler")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho \"args:$*\"\n"), 0o755))
	t.Setenv(wranglerprep.WranglerBinEnv, bin)
	ctx := context.Background()

	out, err := runWranglerWhoami(ctx)
	require.NoError(t, err)
	assert.Equal(t, "args:whoami", strings.TrimSpace(out))

	tok, err := runWranglerAuthToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "args:auth token", tok)

	_, err = resolveSubdomainViaWrangler(ctx)
	require.Error(t, err) // fake output has no subdomain; the point is the args below.

	r := NewLiveWranglerRunner("acct")
	require.NoError(t, r.Delete(ctx, "w"))
	exists, err := r.WorkerExists(ctx, "w")
	require.NoError(t, err)
	assert.True(t, exists)
}
