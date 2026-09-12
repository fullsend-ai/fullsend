// Package ctxerr provides a single predicate for detecting whether an
// operation failed because the caller's own context was canceled or
// exceeded its deadline.
//
// Prefer this over errors.Is against context.DeadlineExceeded. Go's
// net/http Client.Timeout error unwraps to context.DeadlineExceeded
// from an internal context even when the caller's context is still
// live, so errors.Is cannot tell a caller-context expiry from a
// transport timeout. The same false positive appears once an
// intermediate type (for example mintclient.retryableError) implements
// Unwrap. See #6424, #6425, and #7240.
package ctxerr

import "context"

// IsDeadlineExceededOrCanceled reports whether err is non-nil and ctx
// itself is done (canceled or past its deadline).
//
// A nil err is never a context failure. A live ctx is never reported
// as expired, even when err wraps context.DeadlineExceeded or
// implements Timeout() bool — those are nested timeouts (for example
// net/http Client.Timeout), not this call's context.
func IsDeadlineExceededOrCanceled(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return ctx.Err() != nil
}
