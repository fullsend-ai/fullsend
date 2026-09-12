package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeTransientErr implements the transientReporter interface used by
// forge.IsTransient to let forge-specific API errors self-report
// transient-ness.
type fakeTransientErr struct {
	transient bool
}

func (e *fakeTransientErr) Error() string { return "fake error" }
func (e *fakeTransientErr) IsTransient() bool {
	return e.transient
}

// fakeTimeoutErr implements the Timeout() interface to simulate HTTP
// client timeout errors.
type fakeTimeoutErr struct {
	timeout bool
}

func (e *fakeTimeoutErr) Error() string   { return "timeout error" }
func (e *fakeTimeoutErr) Timeout() bool   { return e.timeout }
func (e *fakeTimeoutErr) Temporary() bool { return e.timeout }

// unwrapTimeoutErr mimics net/http Client.Timeout: Unwrap to
// DeadlineExceeded plus Timeout() bool. errors.Is against the
// DeadlineExceeded sentinel matches it even when ctx is still live.
type unwrapTimeoutErr struct {
	error
}

func (e *unwrapTimeoutErr) Timeout() bool { return true }
func (e *unwrapTimeoutErr) Unwrap() error { return e.error }

func TestIsTransient(t *testing.T) {
	t.Parallel()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	expiredCtx, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(expire)

	live := context.Background()
	httpTimeout := &unwrapTimeoutErr{error: context.DeadlineExceeded}

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "nil error",
			ctx:  live,
			err:  nil,
			want: false,
		},
		{
			name: "ErrNonFastForward",
			ctx:  live,
			err:  ErrNonFastForward,
			want: true,
		},
		{
			name: "wrapped ErrNonFastForward",
			ctx:  live,
			err:  fmt.Errorf("commit failed: %w", ErrNonFastForward),
			want: true,
		},
		{
			name: "transient reporter true",
			ctx:  live,
			err:  &fakeTransientErr{transient: true},
			want: true,
		},
		{
			name: "transient reporter false",
			ctx:  live,
			err:  &fakeTransientErr{transient: false},
			want: false,
		},
		{
			name: "wrapped transient reporter",
			ctx:  live,
			err:  fmt.Errorf("api call: %w", &fakeTransientErr{transient: true}),
			want: true,
		},
		{
			name: "DeadlineExceeded with live context is a nested timeout",
			ctx:  live,
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped DeadlineExceeded with live context is a nested timeout",
			ctx:  live,
			err:  fmt.Errorf("timed out: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "DeadlineExceeded with expired context is not transient",
			ctx:  expiredCtx,
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "wrapped DeadlineExceeded with expired context is not transient",
			ctx:  expiredCtx,
			err:  fmt.Errorf("timed out: %w", context.DeadlineExceeded),
			want: false,
		},
		{
			name: "http Client.Timeout wrapping DeadlineExceeded with live context",
			ctx:  live,
			err:  httpTimeout,
			want: true,
		},
		{
			name: "http Client.Timeout wrapping DeadlineExceeded with expired context",
			ctx:  expiredCtx,
			err:  httpTimeout,
			want: false,
		},
		{
			name: "context.Canceled with live context is not transient",
			ctx:  live,
			err:  context.Canceled,
			want: false,
		},
		{
			name: "wrapped context.Canceled with live context is not transient",
			ctx:  live,
			err:  fmt.Errorf("canceled: %w", context.Canceled),
			want: false,
		},
		{
			name: "context.Canceled with cancelled context is not transient",
			ctx:  cancelledCtx,
			err:  context.Canceled,
			want: false,
		},
		{
			name: "timeout error",
			ctx:  live,
			err:  &fakeTimeoutErr{timeout: true},
			want: true,
		},
		{
			name: "non-timeout error with Timeout method",
			ctx:  live,
			err:  &fakeTimeoutErr{timeout: false},
			want: false,
		},
		{
			name: "io.EOF",
			ctx:  live,
			err:  io.EOF,
			want: true,
		},
		{
			name: "wrapped io.EOF",
			ctx:  live,
			err:  fmt.Errorf("read body: %w", io.EOF),
			want: true,
		},
		{
			name: "io.ErrUnexpectedEOF",
			ctx:  live,
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "ErrNotFound is not transient",
			ctx:  live,
			err:  ErrNotFound,
			want: false,
		},
		{
			name: "ErrForbidden is not transient",
			ctx:  live,
			err:  ErrForbidden,
			want: false,
		},
		{
			name: "ErrBranchProtected is not transient",
			ctx:  live,
			err:  ErrBranchProtected,
			want: false,
		},
		{
			name: "generic error is not transient",
			ctx:  live,
			err:  errors.New("something broke"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsTransient(tt.ctx, tt.err))
		})
	}
}
