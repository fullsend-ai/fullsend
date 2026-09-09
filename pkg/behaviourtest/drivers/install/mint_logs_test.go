package install

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	eventsJSON := `{"result":[{"scriptName":"bt-mint","outcome":"ok"}],"success":true}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/accounts/test-account/workers/observability/telemetry/events")
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, eventsJSON)
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

	// Verify the log file was written.
	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.JSONEq(t, eventsJSON, string(data))
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
		fmt.Fprint(w, `{"result":[],"success":true}`)
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

	// File should still be written (empty result is valid).
	logPath := filepath.Join(artifactDir, "debug-mint-logs", "mint-events.json")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"result":[]`)
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
