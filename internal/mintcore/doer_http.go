//go:build !js

package mintcore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPClientDoer wraps an http.Client as a Doer.
type HTTPClientDoer struct {
	Client HTTPDoer
}

// NewHTTPClientDoer creates a Doer backed by a standard http.Client
// with the given timeout.
func NewHTTPClientDoer(timeout time.Duration) *HTTPClientDoer {
	return &HTTPClientDoer{Client: &http.Client{Timeout: timeout}}
}

// Do performs an HTTP request using the underlying http.Client.
func (d *HTTPClientDoer) Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, []byte, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("creating request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := d.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	// Limit response body to 1 MiB to prevent OOM on unexpected responses.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading response body: %w", err)
	}

	return resp.StatusCode, respBody, nil
}
