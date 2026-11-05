package steps

import (
	"context"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// errLeaseLost is returned by verifyLease when the repository under the
// scenario's leased slot name is no longer this scenario's lease, so a
// name-based mutation could land on another holder's resources.
var errLeaseLost = errors.New("leased slot is no longer this scenario's; refusing to mutate by name")

// verifyLease fails closed unless the scenario still owns its leased
// slot. It returns nil when ownership is not tracked (World.LeaseOwned
// is nil), errLeaseLost when the slot was taken over, and a wrapped
// error when ownership could not be verified. It changes no state.
func verifyLease(w *world.World) error {
	if w.LeaseOwned == nil {
		return nil
	}
	owns, err := w.LeaseOwned(context.Background())
	if err != nil {
		return fmt.Errorf("cannot verify the leased slot is still this scenario's; refusing to mutate by name: %w", err)
	}
	if !owns {
		return errLeaseLost
	}
	return nil
}

// mutationCtx returns the context for a name-based mutation on the
// leased slot's repositories. When lease ownership is tracked, the
// forge client re-verifies it after every retry wait (a rate-limit wait
// can outlast the lease), immediately before the mutating request of a
// multi-step commit (after its preparatory reads, on the first attempt
// too) and does not replay a request after an ambiguous failure, so a
// retry never lands on a replacement holder's resources. Without
// ownership tracking it is a plain background context.
func mutationCtx(w *world.World) context.Context {
	ctx := context.Background()
	if w.LeaseOwned == nil {
		return ctx
	}
	return forge.WithRetryGuard(forge.WithoutAmbiguousRetry(ctx), func(context.Context) error {
		// verifyLease uses its own context: requests made on the
		// guarded one would be subject to the same guard.
		return verifyLease(w)
	})
}

// commitVerified commits a file to the leased repository after a
// preparatory read. It verifies lease ownership right before the commit
// request, because a lease can be lost while the preceding read is in
// flight, so a verification done before the read is not enough. The
// guard in mutationCtx repeats the check after the client's own reads
// and before its first write, and after every retry wait; the explicit
// check here also covers SCM drivers that ignore the guard.
func commitVerified(w *world.World, path, message string, content []byte) error {
	if err := verifyLease(w); err != nil {
		return err
	}
	return w.SCM.CommitFile(mutationCtx(w), w.Org, w.RepoName, path, message, content)
}

// guardedCleanupRetry is cleanupRetry for a name-based mutation: every
// attempt, including each retry after a transient error, first verifies
// the scenario still owns its lease. A lost lease is not transient, so
// it ends the retries; an unverifiable lease is retried only when the
// verification error itself is transient.
func guardedCleanupRetry(w *world.World, desc string, fn func() error) error {
	return cleanupRetry(w.Logf, desc, func() error {
		if err := verifyLease(w); err != nil {
			return err
		}
		return fn()
	})
}
