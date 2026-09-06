package steps

import (
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// CleanupScenario is a no-op: repos are retained across runs to boost
// the GitHub App installation rate limit (12,500/hr at 170+ repos).
// Old runs are pruned by composedDriver.pruneOldRuns in Finalize,
// which deletes entire runs while keeping the repo count >= 200.
// When E2E_KEEP_REPOS is set, even Finalize skips pruning.
func CleanupScenario(_ *world.World) error {
	return nil
}
