package jira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCloudID = "11111111-2222-3333-4444-555555555555"

func TestGatewayAPIURL(t *testing.T) {
	t.Parallel()
	u, err := GatewayAPIURL(DefaultGatewayURL, testCloudID)
	require.NoError(t, err)
	assert.Equal(t, "https://api.atlassian.com/ex/jira/"+testCloudID, u)

	_, err = GatewayAPIURL(DefaultGatewayURL, "../admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Atlassian cloud ID")

	_, err = GatewayAPIURL("http://gateway.example.com", testCloudID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway URL")
}

func TestFetchCloudID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	serve := func(t *testing.T, status int, body string) string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/_edge/tenant_info", r.URL.Path)
			assert.Empty(t, r.Header.Get("Authorization"))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}

	t.Run("parses cloudId", func(t *testing.T) {
		t.Parallel()
		id, err := FetchCloudID(ctx, nil, serve(t, http.StatusOK, `{"cloudId":"`+testCloudID+`"}`)+"/")
		require.NoError(t, err)
		assert.Equal(t, testCloudID, id)
	})
	t.Run("discovers the gateway API URL", func(t *testing.T) {
		t.Parallel()
		u, err := DiscoverAPIURL(ctx, nil, serve(t, http.StatusOK, `{"cloudId":"`+testCloudID+`"}`), "https://gateway.example.com/")
		require.NoError(t, err)
		assert.Equal(t, "https://gateway.example.com/ex/jira/"+testCloudID, u)
	})
	errorCases := []struct {
		name, body, wantErr string
		status              int
	}{
		{"non-200", `{}`, "unexpected status 404", http.StatusNotFound},
		{"bad JSON", `not json`, "decode Jira cloud ID", http.StatusOK},
		{"missing cloudId", `{}`, "no cloudId", http.StatusOK},
		{"invalid cloudId", `{"cloudId":"a/b"}`, "invalid Atlassian cloud ID", http.StatusOK},
	}
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := FetchCloudID(ctx, nil, serve(t, tc.status, tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
	t.Run("rejects insecure site URL", func(t *testing.T) {
		t.Parallel()
		_, err := FetchCloudID(ctx, nil, "http://acme.example.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "insecure scheme")
	})
	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		_, err := FetchCloudID(ctx, &http.Client{Timeout: time.Second}, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetch Jira cloud ID")
	})
}
