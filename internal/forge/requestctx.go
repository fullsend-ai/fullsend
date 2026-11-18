package forge

import "context"

// Request-context options that a forge client honours. They live here,
// not in a forge-specific package, so callers that must not import a
// concrete forge (behaviour steps, for example) can still ask the client
// for guarded, non-replaying mutations. A client that does not support an
// option ignores it.

type noAmbiguousRetryKey struct{}

// WithoutAmbiguousRetry returns a context that stops the client from
// retrying a request after an ambiguous failure: a transport timeout or a
// 5xx response, where the server may already have applied the request.
// Rate-limit rejections are still retried because the server did not
// process those. Use it for requests that must not be replayed blindly,
// such as deleting a repository by name that another party may have
// recreated; the caller re-checks state after the error.
func WithoutAmbiguousRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noAmbiguousRetryKey{}, true)
}

// AmbiguousRetryDisabled reports whether ctx came from WithoutAmbiguousRetry.
func AmbiguousRetryDisabled(ctx context.Context) bool {
	v, _ := ctx.Value(noAmbiguousRetryKey{}).(bool)
	return v
}

type retryGuardKey struct{}

// WithRetryGuard returns a context under which the client calls guard
// after every retry wait (including rate-limit waits, repository-race
// waits and commit-retry delays) and before resending the request or
// rerunning the operation. It also calls guard immediately before the
// mutating request of a multi-step commit (after the preparatory reads),
// including on the first attempt. A non-nil error from guard stops the
// operation: nothing is sent and that error is returned, wrapped. Use it
// for requests whose target may change while the client waits or reads,
// such as writing to or deleting a repository by name that another party
// may have deleted and recreated. guard receives the request context;
// requests it makes on that context are subject to the same guard, so it
// should use its own context.
func WithRetryGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, retryGuardKey{}, guard)
}

// RetryGuard returns the guard installed by WithRetryGuard, or nil.
func RetryGuard(ctx context.Context) func(context.Context) error {
	guard, _ := ctx.Value(retryGuardKey{}).(func(context.Context) error)
	return guard
}
