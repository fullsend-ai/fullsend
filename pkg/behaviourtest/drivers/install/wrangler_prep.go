package install

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/wranglerprep"
)

// prepareWranglerEnvFn is the Wrangler preparation hook. It uses
// internal/wranglerprep rather than the Cloudflare provisioner package,
// which pulls in the mint core that pkg/behaviourtest must not import.
// Override in tests to skip running npm in factory tests.
var prepareWranglerEnvFn = wranglerprep.PrepareWranglerEnv

// prepareWranglerForMints installs the pinned Wrangler once and exports its
// path as wranglerprep.WranglerBinEnv, so every per-org `fullsend mint
// deploy` child started by the CF mint drivers (which inherit this
// process's environment) runs it directly. Without this, the concurrent
// children each run `npx wrangler` and race on the shared npx cache.
//
// Only the CF mint factories call it, so it fails closed: a deploy that
// would hit the race is not started.
func prepareWranglerForMints(ctx context.Context, logf func(string, ...any)) error {
	if err := prepareWranglerEnvFn(ctx); err != nil {
		return fmt.Errorf("preparing wrangler for concurrent mint deploys: %w", err)
	}
	logf("[cfmint] wrangler prepared once for mint deploys (%s)", wranglerprep.WranglerBinEnv)
	return nil
}
