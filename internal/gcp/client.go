// Package gcp provides authenticated HTTP access to GCP APIs using
// Application Default Credentials. It is a shared foundation used by
// the Vertex AI inference provider and the GCF dispatch provisioner.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

// Client provides authenticated HTTP access to GCP APIs using
// Application Default Credentials.
type Client struct {
	httpClient *http.Client
	// tokenFunc is the function used to obtain access tokens.
	// It defaults to ADC but can be overridden for testing.
	tokenFunc    func(ctx context.Context) (string, error)
	QuotaProject string
}

// NewClient creates a new Client with default settings.
func NewClient() *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	c.tokenFunc = c.adcToken
	return c
}

// NewClientWithHTTP creates a Client that uses the given HTTP client and a
// static "test-token" for auth. Intended for unit tests that redirect GCP
// API calls to httptest servers.
func NewClientWithHTTP(httpClient *http.Client) *Client {
	return &Client{
		httpClient: httpClient,
		tokenFunc: func(_ context.Context) (string, error) {
			return "test-token", nil
		},
	}
}

// AccessToken obtains a GCP access token using Application Default Credentials.
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	return c.tokenFunc(ctx)
}

// adcToken obtains a token via Application Default Credentials.
func (c *Client) adcToken(ctx context.Context) (string, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", fmt.Errorf("finding GCP credentials: %w (ensure 'gcloud auth application-default login' has been run or GOOGLE_APPLICATION_CREDENTIALS is set)", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("obtaining GCP access token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("GCP credentials returned empty access token")
	}
	return tok.AccessToken, nil
}

// DoRequest creates and executes an authenticated HTTP request.
func (c *Client) DoRequest(ctx context.Context, method, url, body string) (*http.Response, error) {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.QuotaProject != "" {
		req.Header.Set("x-goog-user-project", c.QuotaProject)
	}

	return c.httpClient.Do(req)
}

// ExtractErrorMessage parses a GCP API error response and returns only
// the error message, avoiding leakage of sensitive metadata.
func ExtractErrorMessage(body []byte) string {
	var gcpErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &gcpErr) == nil && gcpErr.Error.Message != "" {
		return gcpErr.Error.Message
	}
	return "(error details unavailable)"
}
