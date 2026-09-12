package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCFWorkerLogCollector_MissingCredentials(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_API_TOKEN", "")

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	c := newCFWorkerLogCollector(logf)
	assert.Nil(t, c, "collector should be nil when credentials are missing")
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "Cloudflare credentials not available")
}

func TestNewCFWorkerLogCollector_MissingAccountID(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	c := newCFWorkerLogCollector(logf)
	assert.Nil(t, c)
	assert.Contains(t, logged[0], "credentials not available")
}

func TestNewCFWorkerLogCollector_MissingAPIToken(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "")

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	c := newCFWorkerLogCollector(logf)
	assert.Nil(t, c)
	assert.Contains(t, logged[0], "credentials not available")
}

func TestNewCFWorkerLogCollector_OK(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	c := newCFWorkerLogCollector(t.Logf)
	require.NotNil(t, c)
	assert.Equal(t, "test-account", c.accountID)
	assert.Equal(t, "test-token", c.apiToken)
}

func TestCFWorkerLogCollector_Collect_Success(t *testing.T) {
	// Response shape per the documented Telemetry Query API: a
	// standard Cloudflare envelope wrapping a "result.events" object
	// for the "events" view.
	// See: https://developers.cloudflare.com/api/resources/workers/subresources/observability/subresources/telemetry/methods/query/
	responseJSON := `{
		"success": true,
		"errors": [],
		"result": {
			"events": {
				"count": 2,
				"events": [
					{
						"dataset": "cloudflare-workers",
						"timestamp": 1700000000000,
						"source": {"message": "hello from worker"},
						"$metadata": {"id": "evt-1", "service": "bt-mint", "type": "cf-worker-log"}
					},
					{
						"dataset": "cloudflare-workers",
						"timestamp": 1700000001000,
						"source": {"message": "request handled"},
						"$metadata": {"id": "evt-2", "service": "bt-mint", "type": "cf-worker-event"}
					}
				]
			}
		}
	}`

	var capturedBody telemetryQueryRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/accounts/test-account/workers/observability/telemetry/query", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		rawBody, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(rawBody, &capturedBody))

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, responseJSON)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "test-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       t.Logf,
	}

	err := c.Collect(context.Background(), "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.NoError(t, err)

	// Verify the request matches the documented query flow: the
	// "events" view, a bounded timeframe, and a filter scoping the
	// query to the requested Worker script.
	assert.Equal(t, "events", capturedBody.View)
	assert.NotZero(t, capturedBody.Timeframe.From)
	assert.NotZero(t, capturedBody.Timeframe.To)
	require.Len(t, capturedBody.Parameters.Filters, 1)
	assert.Equal(t, "$metadata.service", capturedBody.Parameters.Filters[0].Key)
	assert.Equal(t, "eq", capturedBody.Parameters.Filters[0].Operation)
	assert.Equal(t, "bt-mint", capturedBody.Parameters.Filters[0].Value)

	// Verify the log file contains the event records extracted from
	// the "result.events.events" array.
	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"dataset": "cloudflare-workers", "timestamp": 1700000000000, "source": {"message": "hello from worker"}, "$metadata": {"id": "evt-1", "service": "bt-mint", "type": "cf-worker-log"}},
		{"dataset": "cloudflare-workers", "timestamp": 1700000001000, "source": {"message": "request handled"}, "$metadata": {"id": "evt-2", "service": "bt-mint", "type": "cf-worker-event"}}
	]`, string(data))
}

func TestCFWorkerLogCollector_Collect_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"success":false,"errors":[{"message":"authentication error"}]}`)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "bad-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       t.Logf,
	}

	err := c.Collect(context.Background(), "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cloudflare API returned 403")

	// No file should be created on error.
	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	_, statErr := os.Stat(logPath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCFWorkerLogCollector_Collect_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"success":false,"errors":[{"message":"not found"}]}`)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "test-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       t.Logf,
	}

	err := c.Collect(context.Background(), "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cloudflare API returned 404")
}

func TestCFWorkerLogCollector_Collect_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"success":true,"errors":[],"result":{"events":{"count":0,"events":[]}}}`)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	var logged []string
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "test-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	err := c.Collect(context.Background(), "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.NoError(t, err)

	// File should still be written (zero matching events is valid).
	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(data))

	// A zero-event result is logged explicitly so it's diagnosable,
	// rather than silently producing an empty artifact.
	found := false
	for _, l := range logged {
		if strings.Contains(l, "0 event records") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a warning log about 0 event records, got: %v", logged)
}

func TestCFWorkerLogCollector_Collect_APIReportsFailure(t *testing.T) {
	// HTTP 200 but the Cloudflare API envelope reports success=false.
	// This must surface as an explicit error, not a silently empty
	// artifact.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":1003,"message":"invalid query parameters"}],"result":{}}`)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "test-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       t.Logf,
	}

	err := c.Collect(context.Background(), "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid query parameters")

	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	_, statErr := os.Stat(logPath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCFWorkerLogCollector_Collect_MissingEventsView(t *testing.T) {
	// HTTP 200, success=true, but the response doesn't include an
	// "events" view result (e.g. the query or view param was wrong).
	// This is a response-shape mismatch and must be explicit rather
	// than silently writing an empty/garbage artifact.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"success":true,"errors":[],"result":{}}`)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "test-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       t.Logf,
	}

	err := c.Collect(context.Background(), "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events")

	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	_, statErr := os.Stat(logPath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCFWorkerLogCollector_Collect_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Delay response to trigger context cancellation.
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	c := &cfWorkerLogCollector{
		accountID:  "test-account",
		apiToken:   "test-token",
		baseURL:    server.URL,
		httpClient: server.Client(),
		logf:       t.Logf,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.Collect(ctx, "bt-mint", time.Now().Add(-10*time.Minute), artifactDir)
	require.Error(t, err)
}

func TestCFWorkerLogCollector_BaseURL_DefaultsToProduction(t *testing.T) {
	c := &cfWorkerLogCollector{}
	assert.Equal(t, "https://api.cloudflare.com/client/v4", c.cfBaseURL())
}

func TestCFWorkerLogCollector_BaseURL_OverrideInTests(t *testing.T) {
	c := &cfWorkerLogCollector{baseURL: "http://localhost:8080"}
	assert.Equal(t, "http://localhost:8080", c.cfBaseURL())
}
