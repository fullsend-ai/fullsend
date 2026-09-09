package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestExtractErrorMessage(t *testing.T) {
	t.Run("valid error response", func(t *testing.T) {
		body := []byte(`{"error":{"message":"Permission denied on resource"}}`)
		msg := ExtractErrorMessage(body)
		assert.Equal(t, "Permission denied on resource", msg)
	})

	t.Run("empty message", func(t *testing.T) {
		body := []byte(`{"error":{"message":""}}`)
		msg := ExtractErrorMessage(body)
		assert.Equal(t, "(error details unavailable)", msg)
	})

	t.Run("invalid json", func(t *testing.T) {
		body := []byte(`not json`)
		msg := ExtractErrorMessage(body)
		assert.Equal(t, "(error details unavailable)", msg)
	})

	t.Run("missing error field", func(t *testing.T) {
		body := []byte(`{"status":"error"}`)
		msg := ExtractErrorMessage(body)
		assert.Equal(t, "(error details unavailable)", msg)
	})
}

func TestNewClient(t *testing.T) {
	c := NewClient()
	assert.NotNil(t, c)
	assert.NotNil(t, c.httpClient)
}

func TestDoRequest(t *testing.T) {
	t.Run("GET request with auth", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
			assert.Empty(t, r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}))
		defer srv.Close()

		c := NewClient()
		// Override the token function for testing.
		c.tokenFunc = func(_ context.Context) (string, error) {
			return "test-token", nil
		}

		resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("POST request with body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")

			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "value", body["key"])

			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) {
			return "test-token", nil
		}

		resp, err := c.DoRequest(context.Background(), http.MethodPost, srv.URL+"/test", `{"key":"value"}`)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAccessToken_WithTokenFunc(t *testing.T) {
	c := NewClient()
	c.tokenFunc = func(_ context.Context) (string, error) {
		return "custom-token", nil
	}

	token, err := c.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "custom-token", token)
}

func TestDoRequest_QuotaProjectHeader(t *testing.T) {
	t.Run("sets x-goog-user-project when QuotaProject is non-empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "target-project", r.Header.Get("x-goog-user-project"))
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.QuotaProject = "target-project"

		resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("omits x-goog-user-project when QuotaProject is empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.Header.Get("x-goog-user-project"))
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }

		resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAccessToken_ErrorPropagation(t *testing.T) {
	c := NewClient()
	c.tokenFunc = func(_ context.Context) (string, error) {
		return "", fmt.Errorf("finding GCP credentials: no credentials found")
	}

	_, err := c.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding GCP credentials")
}

func TestAdcToken_CachesTokenSource(t *testing.T) {
	t.Parallel()

	// Verify that credential discovery runs exactly once even when
	// multiple goroutines call AccessToken concurrently.
	var discoveryCount atomic.Int32
	var tokenCount atomic.Int32

	c := NewClient()
	c.tokenFunc = c.adcToken
	// Pre-populate the cached TokenSource via sync.Once to simulate
	// a successful FindDefaultCredentials without hitting real GCP.
	c.adcOnce.Do(func() {
		discoveryCount.Add(1)
		c.adcTS = oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "cached-token",
			Expiry:      time.Now().Add(time.Hour),
		})
	})

	// Wrap the cached token source to count Token() calls.
	inner := c.adcTS
	c.adcTS = tokenSourceFunc(func() (*oauth2.Token, error) {
		tokenCount.Add(1)
		return inner.Token()
	})

	const goroutines = 12
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := c.AccessToken(context.Background())
			assert.NoError(t, err)
			assert.Equal(t, "cached-token", tok)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), discoveryCount.Load(),
		"credential discovery should happen exactly once")
	assert.Equal(t, int32(goroutines), tokenCount.Load(),
		"Token() should be called on every AccessToken invocation")
}

func TestAdcToken_DiscoveryErrorSurfaces(t *testing.T) {
	c := NewClient()
	// Force a discovery error through the lazy init path.
	c.adcOnce.Do(func() {
		c.adcErr = fmt.Errorf("finding GCP credentials: %w",
			fmt.Errorf("no credentials found"))
	})
	c.tokenFunc = c.adcToken

	_, err := c.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding GCP credentials")

	// Second call should return the same cached error.
	_, err = c.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding GCP credentials")
}

func TestAdcToken_TokenFetchError(t *testing.T) {
	c := NewClient()
	c.tokenFunc = c.adcToken
	// Pre-populate a TokenSource that always returns an error on Token().
	c.adcOnce.Do(func() {
		c.adcTS = tokenSourceFunc(func() (*oauth2.Token, error) {
			return nil, fmt.Errorf("token refresh failed")
		})
	})

	_, err := c.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obtaining GCP access token")
}

func TestAdcToken_EmptyAccessToken(t *testing.T) {
	c := NewClient()
	c.tokenFunc = c.adcToken
	// Pre-populate a TokenSource that returns an empty access token.
	c.adcOnce.Do(func() {
		c.adcTS = oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "",
			Expiry:      time.Now().Add(time.Hour),
		})
	})

	_, err := c.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP credentials returned empty access token")
}

func TestNewClientWithHTTP(t *testing.T) {
	httpClient := &http.Client{Timeout: 5 * time.Second}
	c := NewClientWithHTTP(httpClient)
	assert.NotNil(t, c)

	token, err := c.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestDoRequest_TokenError(t *testing.T) {
	c := NewClient()
	c.tokenFunc = func(_ context.Context) (string, error) {
		return "", fmt.Errorf("auth failure")
	}

	_, err := c.DoRequest(context.Background(), http.MethodGet, "http://example.com", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth failure")
}

func TestAdcToken_RealDiscoverySuccess(t *testing.T) {
	// Exercise the real FindDefaultCredentials code path inside the
	// sync.Once closure by providing a fake authorized_user credential
	// file.  FindDefaultCredentials will parse it successfully (covering
	// the closure's happy path), but Token() will fail because the
	// refresh token is bogus — that error is already a covered path.
	credsJSON := `{
		"type": "authorized_user",
		"client_id": "fake-client-id.apps.googleusercontent.com",
		"client_secret": "fake-secret",
		"refresh_token": "fake-refresh-token"
	}`
	tmpFile := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(tmpFile, []byte(credsJSON), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tmpFile)

	c := NewClient() // tokenFunc defaults to adcToken; adcOnce is fresh
	_, err := c.AccessToken(context.Background())
	require.Error(t, err)
	// Discovery succeeded, but the bogus refresh token causes Token() to fail.
	assert.Contains(t, err.Error(), "obtaining GCP access token")
	// Verify the TokenSource was cached and no discovery error was recorded.
	assert.NotNil(t, c.adcTS, "TokenSource should be cached after successful discovery")
	assert.NoError(t, c.adcErr, "discovery error should be nil on success")
}

func TestAdcToken_RealDiscoveryFailure(t *testing.T) {
	// Exercise the error branch inside the sync.Once closure by
	// pointing GOOGLE_APPLICATION_CREDENTIALS at a file containing
	// invalid credential JSON.
	tmpFile := filepath.Join(t.TempDir(), "bad-creds.json")
	require.NoError(t, os.WriteFile(tmpFile, []byte(`not-json`), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tmpFile)

	c := NewClient()
	_, err := c.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding GCP credentials")
	// Verify discovery error was cached.
	assert.Error(t, c.adcErr, "discovery error should be cached")
}

func TestDoRequest_InvalidMethod(t *testing.T) {
	c := NewClient()
	c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }

	// A method containing a space is rejected by http.NewRequestWithContext.
	_, err := c.DoRequest(context.Background(), "BAD METHOD", "http://example.com", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating request")
}

// tokenSourceFunc adapts a plain function to the oauth2.TokenSource
// interface, allowing Token() call counting in tests.
type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

// --- Retry behaviour tests ---

func TestDoRequest_RetriesTransientTransportErrors(t *testing.T) {
	t.Parallel()

	t.Run("retries connection reset then succeeds", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		// Use a custom transport that returns ECONNRESET on the first
		// two attempts, then forwards to the real transport.
		transport := &retryTestTransport{
			errCount: 2,
			err:      fmt.Errorf("read tcp 10.1.1.112:43310->172.217.112.4:443: read: %w", syscall.ECONNRESET),
			inner:    http.DefaultTransport,
			target:   srv.URL,
		}

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.httpClient = &http.Client{Transport: transport}
		c.retryDelayFn = func(_ int) time.Duration { return 0 }

		resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(1), calls.Load(), "server should be hit once after 2 transport failures")
		assert.Equal(t, int32(3), transport.totalCalls.Load(), "should have made 3 total attempts")
	})

	t.Run("retries connection refused then succeeds", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		transport := &retryTestTransport{
			errCount: 1,
			err:      fmt.Errorf("dial tcp 127.0.0.1:443: %w", syscall.ECONNREFUSED),
			inner:    http.DefaultTransport,
			target:   srv.URL,
		}

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.httpClient = &http.Client{Transport: transport}
		c.retryDelayFn = func(_ int) time.Duration { return 0 }

		resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(2), transport.totalCalls.Load())
	})

	t.Run("exhausts retries on persistent connection reset", func(t *testing.T) {
		t.Parallel()
		transport := &retryTestTransport{
			errCount: maxRetries + 1, // more errors than retries
			err:      fmt.Errorf("read: %w", syscall.ECONNRESET),
			inner:    http.DefaultTransport,
		}

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.httpClient = &http.Client{Transport: transport}
		c.retryDelayFn = func(_ int) time.Duration { return 0 }

		_, err := c.DoRequest(context.Background(), http.MethodGet, "http://localhost/test", "")
		require.Error(t, err)
		assert.ErrorIs(t, err, syscall.ECONNRESET)
		assert.Equal(t, int32(maxRetries+1), transport.totalCalls.Load(),
			"should have attempted 1 initial + 3 retries")
	})

	t.Run("does not retry on context cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.retryDelayFn = func(_ int) time.Duration { return 0 }

		_, err := c.DoRequest(ctx, http.MethodGet, "http://localhost/test", "")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("does not retry non-retryable transport error", func(t *testing.T) {
		t.Parallel()
		transport := &retryTestTransport{
			errCount: 3,
			err:      fmt.Errorf("dns lookup failed: no such host"),
			inner:    http.DefaultTransport,
		}

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.httpClient = &http.Client{Transport: transport}
		c.retryDelayFn = func(_ int) time.Duration { return 0 }

		_, err := c.DoRequest(context.Background(), http.MethodGet, "http://localhost/test", "")
		require.Error(t, err)
		assert.Equal(t, int32(1), transport.totalCalls.Load(), "should not retry non-retryable error")
	})

	t.Run("does not retry non-idempotent method on transport error", func(t *testing.T) {
		t.Parallel()

		nonIdempotent := []string{http.MethodPost, http.MethodPatch}
		for _, method := range nonIdempotent {
			t.Run(method, func(t *testing.T) {
				t.Parallel()
				transport := &retryTestTransport{
					errCount: 3,
					err:      fmt.Errorf("read tcp: read: %w", syscall.ECONNRESET),
					inner:    http.DefaultTransport,
				}

				c := NewClient()
				c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
				c.httpClient = &http.Client{Transport: transport}
				c.retryDelayFn = func(_ int) time.Duration { return 0 }

				_, err := c.DoRequest(context.Background(), method, "http://localhost/test", "")
				require.Error(t, err)
				assert.ErrorIs(t, err, syscall.ECONNRESET)
				assert.Equal(t, int32(1), transport.totalCalls.Load(),
					"%s should not retry transport errors (non-idempotent)", method)
			})
		}
	})
}

func TestDoRequest_RetriesTransientStatusCodes(t *testing.T) {
	t.Parallel()

	retryableCodes := []struct {
		name string
		code int
	}{
		{"500 Internal Server Error", http.StatusInternalServerError},
		{"502 Bad Gateway", http.StatusBadGateway},
		{"503 Service Unavailable", http.StatusServiceUnavailable},
		{"504 Gateway Timeout", http.StatusGatewayTimeout},
	}

	for _, tc := range retryableCodes {
		t.Run(tc.name+" retries then succeeds", func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := calls.Add(1)
				if n <= 2 {
					w.WriteHeader(tc.code)
					return
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			}))
			defer srv.Close()

			c := NewClient()
			c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
			c.retryDelayFn = func(_ int) time.Duration { return 0 }

			resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, int32(3), calls.Load(), "should retry twice then succeed")
		})
	}

	t.Run("returns last response after exhausting retries on 503", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		c := NewClient()
		c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
		c.retryDelayFn = func(_ int) time.Duration { return 0 }

		resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, int32(maxRetries+1), calls.Load(),
			"should have attempted 1 initial + 3 retries")
	})

	nonRetryableCodes := []struct {
		name string
		code int
	}{
		{"400 Bad Request", http.StatusBadRequest},
		{"401 Unauthorized", http.StatusUnauthorized},
		{"403 Forbidden", http.StatusForbidden},
		{"404 Not Found", http.StatusNotFound},
		{"409 Conflict", http.StatusConflict},
		{"429 Too Many Requests", http.StatusTooManyRequests}, // handled by doWIFRequestWithRetry, not DoRequest
	}

	for _, tc := range nonRetryableCodes {
		t.Run(tc.name+" is not retried", func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			c := NewClient()
			c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
			c.retryDelayFn = func(_ int) time.Duration { return 0 }

			resp, err := c.DoRequest(context.Background(), http.MethodGet, srv.URL+"/test", "")
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tc.code, resp.StatusCode)
			assert.Equal(t, int32(1), calls.Load(), "should not retry non-retryable status")
		})
	}
}

func TestDoRequest_DoesNotRetryNonIdempotentOnStatusCode(t *testing.T) {
	t.Parallel()

	nonIdempotent := []string{http.MethodPost, http.MethodPatch}
	retryableCodes := []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	for _, method := range nonIdempotent {
		for _, code := range retryableCodes {
			t.Run(fmt.Sprintf("%s %d", method, code), func(t *testing.T) {
				t.Parallel()
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.WriteHeader(code)
				}))
				defer srv.Close()

				c := NewClient()
				c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
				c.retryDelayFn = func(_ int) time.Duration { return 0 }

				resp, err := c.DoRequest(context.Background(), method, srv.URL+"/test", `{"key":"value"}`)
				require.NoError(t, err)
				defer resp.Body.Close()
				assert.Equal(t, code, resp.StatusCode)
				assert.Equal(t, int32(1), calls.Load(),
					"%s should not retry %d (non-idempotent)", method, code)
			})
		}
	}
}

func TestDoRequest_RetryPreservesRequestBody(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		lastBody = body["key"]
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient()
	c.tokenFunc = func(_ context.Context) (string, error) { return "test-token", nil }
	c.retryDelayFn = func(_ int) time.Duration { return 0 }

	// Use PUT (idempotent) — non-idempotent methods like POST are not
	// retried on 5xx status codes to avoid duplicating side effects.
	resp, err := c.DoRequest(context.Background(), http.MethodPut, srv.URL+"/test", `{"key":"value"}`)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "value", lastBody, "request body should be present on retry")
	assert.Equal(t, int32(2), calls.Load())
}

func TestIsRetryableTransportError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		ctxCancel bool
		want      bool
	}{
		{
			name: "connection reset by peer",
			err:  fmt.Errorf("read tcp 10.1.1.112:43310->172.217.112.4:443: read: %w", syscall.ECONNRESET),
			want: true,
		},
		{
			name: "connection refused",
			err:  fmt.Errorf("dial tcp 127.0.0.1:443: %w", syscall.ECONNREFUSED),
			want: true,
		},
		{
			name: "EOF",
			err:  fmt.Errorf("reading body: %w", fmt.Errorf("unexpected: %w", io.EOF)),
			want: true,
		},
		{
			name: "unexpected EOF",
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "network timeout",
			err:  &net.DNSError{IsTimeout: true},
			want: true,
		},
		{
			name:      "context canceled — not retryable",
			err:       context.Canceled,
			ctxCancel: true,
			want:      false,
		},
		{
			name:      "context deadline exceeded — not retryable",
			err:       context.DeadlineExceeded,
			ctxCancel: true,
			want:      false,
		},
		{
			name: "generic error — not retryable",
			err:  fmt.Errorf("something broke"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if tt.ctxCancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got := isRetryableTransportError(ctx, tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsIdempotentMethod(t *testing.T) {
	t.Parallel()
	assert.True(t, isIdempotentMethod(http.MethodGet))
	assert.True(t, isIdempotentMethod(http.MethodHead))
	assert.True(t, isIdempotentMethod(http.MethodPut))
	assert.True(t, isIdempotentMethod(http.MethodDelete))
	assert.False(t, isIdempotentMethod(http.MethodPost))
	assert.False(t, isIdempotentMethod(http.MethodPatch))
}

func TestIsRetryableStatusCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code int
		want bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusConflict, false},
		{http.StatusTooManyRequests, false}, // handled by doWIFRequestWithRetry
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status %d", tt.code), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isRetryableStatusCode(tt.code))
		})
	}
}

func TestDefaultRetryDelay(t *testing.T) {
	t.Parallel()

	// Verify delay bounds: each attempt should produce a delay between
	// half and full of the computed backoff (50-100% jitter).
	bounds := []struct {
		attempt int
		minMs   int64
		maxMs   int64
	}{
		{0, 500, 1000},   // base 1s: jitter [500ms, 1s]
		{1, 1000, 2000},  // base 2s: jitter [1s, 2s]
		{2, 2000, 4000},  // base 4s: jitter [2s, 4s]
		{3, 4000, 8000},  // base 8s (1s<<3): jitter [4s, 8s]
		{4, 5000, 10000}, // capped at 10s: jitter [5s, 10s]
	}

	for _, b := range bounds {
		t.Run(fmt.Sprintf("attempt %d", b.attempt), func(t *testing.T) {
			t.Parallel()
			// Run multiple iterations to check jitter range.
			for range 20 {
				d := defaultRetryDelay(b.attempt)
				ms := d.Milliseconds()
				assert.GreaterOrEqual(t, ms, b.minMs,
					"delay too short for attempt %d", b.attempt)
				assert.LessOrEqual(t, ms, b.maxMs,
					"delay too long for attempt %d", b.attempt)
			}
		})
	}
}

// retryTestTransport is an http.RoundTripper that returns an error for
// the first errCount calls, then forwards to the inner transport.
type retryTestTransport struct {
	errCount   int
	err        error
	inner      http.RoundTripper
	target     string // optional: rewrite URL to target for httptest server
	mu         sync.Mutex
	callCount  int
	totalCalls atomic.Int32
}

func (t *retryTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.totalCalls.Add(1)
	t.mu.Lock()
	n := t.callCount
	t.callCount++
	t.mu.Unlock()

	if n < t.errCount {
		return nil, t.err
	}
	// Forward to the inner transport (real HTTP to test server).
	if t.target != "" && t.inner != nil {
		return t.inner.RoundTrip(req)
	}
	// Fallback: return a simple 200.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
	}, nil
}
