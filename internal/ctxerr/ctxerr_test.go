package ctxerr

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// httpTimeoutErr mimics net/http's Client.Timeout error: it unwraps to
// context.DeadlineExceeded and implements Timeout() bool. errors.Is
// against DeadlineExceeded matches it even when the caller's context
// is still live — the false positive this helper exists to prevent.
type httpTimeoutErr struct {
	err error
}

func (e *httpTimeoutErr) Error() string {
	return e.err.Error() + " (Client.Timeout exceeded while awaiting headers)"
}

func (e *httpTimeoutErr) Unwrap() error { return e.err }

func (e *httpTimeoutErr) Timeout() bool { return true }

// retryableWrapper mimics mintclient.retryableError: Unwrap only, no
// Timeout() method. After Unwrap is added, errors.Is against
// DeadlineExceeded matches a wrapped transport timeout.
type retryableWrapper struct {
	error
}

func (e *retryableWrapper) Unwrap() error { return e.error }

func TestIsDeadlineExceededOrCanceled(t *testing.T) {
	t.Parallel()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	expiredCtx, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(expire)

	liveDE := context.DeadlineExceeded
	wrappedDE := fmt.Errorf("request failed: %w", context.DeadlineExceeded)
	httpTimeout := &httpTimeoutErr{err: context.DeadlineExceeded}
	retryableTimeout := &retryableWrapper{error: httpTimeout}

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "nil error with live context",
			ctx:  context.Background(),
			err:  nil,
			want: false,
		},
		{
			name: "nil error with cancelled context",
			ctx:  cancelledCtx,
			err:  nil,
			want: false,
		},
		{
			name: "generic error with live context",
			ctx:  context.Background(),
			err:  errors.New("mint rejected"),
			want: false,
		},
		{
			name: "generic error with cancelled context is still context-done",
			ctx:  cancelledCtx,
			err:  errors.New("mint rejected"),
			want: true,
		},
		{
			name: "DeadlineExceeded with live context is not this call's expiry",
			ctx:  context.Background(),
			err:  liveDE,
			want: false,
		},
		{
			name: "wrapped DeadlineExceeded with live context is not this call's expiry",
			ctx:  context.Background(),
			err:  wrappedDE,
			want: false,
		},
		{
			name: "http Client.Timeout wrapping DeadlineExceeded with live context",
			ctx:  context.Background(),
			err:  httpTimeout,
			want: false,
		},
		{
			name: "retryable Unwrap of http Client.Timeout with live context",
			ctx:  context.Background(),
			err:  retryableTimeout,
			want: false,
		},
		{
			name: "Canceled with live context is not this call's expiry",
			ctx:  context.Background(),
			err:  context.Canceled,
			want: false,
		},
		{
			name: "DeadlineExceeded with expired context",
			ctx:  expiredCtx,
			err:  liveDE,
			want: true,
		},
		{
			name: "wrapped DeadlineExceeded with expired context",
			ctx:  expiredCtx,
			err:  wrappedDE,
			want: true,
		},
		{
			name: "http Client.Timeout with expired context is this call's expiry",
			ctx:  expiredCtx,
			err:  httpTimeout,
			want: true,
		},
		{
			name: "Canceled with cancelled context",
			ctx:  cancelledCtx,
			err:  context.Canceled,
			want: true,
		},
		{
			name: "DeadlineExceeded with cancelled context",
			ctx:  cancelledCtx,
			err:  liveDE,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsDeadlineExceededOrCanceled(tt.ctx, tt.err))
		})
	}
}
