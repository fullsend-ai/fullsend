package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
