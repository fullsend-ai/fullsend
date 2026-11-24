package gitlab

import (
	"context"
	"strings"
)

// RequestLogger receives one call per GitLab API request attempt: the HTTP
// method, the API path without its query string, and the response status
// (0 when no response was received). It never sees headers, query
// parameters, or request and response bodies, so it cannot be handed a
// credential. Implementations must be safe for concurrent use.
type RequestLogger func(method, path string, status int)

type requestLoggerKey struct{}

// WithRequestLogger returns a context that makes every LiveClient request
// made with it, including clients built later for other credentials,
// report to l. A nil l disables logging.
func WithRequestLogger(ctx context.Context, l RequestLogger) context.Context {
	return context.WithValue(ctx, requestLoggerKey{}, l)
}

func requestLoggerFrom(ctx context.Context) RequestLogger {
	l, _ := ctx.Value(requestLoggerKey{}).(RequestLogger)
	return l
}

// logRequest reports a request attempt to the context's RequestLogger.
func logRequest(ctx context.Context, method, path string, status int) {
	l := requestLoggerFrom(ctx)
	if l == nil {
		return
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	l(method, "/api/v4"+path, status)
}
