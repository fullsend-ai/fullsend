package cf

import (
	"context"
	"os"
	"os/exec"

	"github.com/fullsend-ai/fullsend/internal/wranglerprep"
)

// wranglerCommand builds the command for a Wrangler invocation. args are
// the Wrangler arguments without a leading "wrangler". If
// wranglerprep.WranglerBinEnv is set, the prepared executable is run
// directly; otherwise the command is `npx wrangler <args...>`. Concurrent
// `fullsend mint deploy` processes would otherwise race on the shared npx
// cache.
func wranglerCommand(ctx context.Context, args ...string) *exec.Cmd {
	if bin := os.Getenv(wranglerprep.WranglerBinEnv); bin != "" {
		return exec.CommandContext(ctx, bin, args...)
	}
	return exec.CommandContext(ctx, "npx", append([]string{"wrangler"}, args...)...)
}
