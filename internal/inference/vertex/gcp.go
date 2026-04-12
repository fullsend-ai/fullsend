package vertex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

// gcpIDPattern validates GCP project IDs and service account names.
var gcpIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// LiveGCPClient implements GCPClient using the GCP IAM REST API.
// It obtains access tokens via Application Default Credentials.
type LiveGCPClient struct {
	httpClient *http.Client
}

// NewLiveGCPClient creates a new LiveGCPClient.
func NewLiveGCPClient() *LiveGCPClient {
	return &LiveGCPClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// accessToken obtains a GCP access token using Application Default Credentials.
func (c *LiveGCPClient) accessToken(ctx context.Context) (string, error) {
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

// doRequest creates and executes an authenticated HTTP request.
func (c *LiveGCPClient) doRequest(ctx context.Context, method, reqURL, body string) (*http.Response, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

// extractGCPErrorMessage parses a GCP API error response and returns only
// the error message, avoiding leakage of sensitive metadata.
func extractGCPErrorMessage(body []byte) string {
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

// GetServiceAccount checks that a service account exists in the project.
func (c *LiveGCPClient) GetServiceAccount(ctx context.Context, projectID, saName string) error {
	if !gcpIDPattern.MatchString(projectID) {
		return fmt.Errorf("invalid GCP project ID %q", projectID)
	}
	if !gcpIDPattern.MatchString(saName) {
		return fmt.Errorf("invalid service account name %q", saName)
	}

	email := saName + "@" + projectID + ".iam.gserviceaccount.com"
	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/serviceAccounts/%s",
		url.PathEscape(projectID), url.PathEscape(email))

	resp, err := c.doRequest(ctx, http.MethodGet, reqURL, "")
	if err != nil {
		return fmt.Errorf("checking service account: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("service account %s not found in project %s", saName, projectID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d checking service account: %s", resp.StatusCode, extractGCPErrorMessage(body))
	}
	return nil
}

// CreateServiceAccount creates a new service account in the project.
func (c *LiveGCPClient) CreateServiceAccount(ctx context.Context, projectID, saName, displayName string) error {
	if !gcpIDPattern.MatchString(projectID) {
		return fmt.Errorf("invalid GCP project ID %q", projectID)
	}
	if !gcpIDPattern.MatchString(saName) {
		return fmt.Errorf("invalid service account name %q", saName)
	}

	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/serviceAccounts",
		url.PathEscape(projectID))
	payload := fmt.Sprintf(`{"accountId":%q,"serviceAccount":{"displayName":%q}}`, saName, displayName)

	resp, err := c.doRequest(ctx, http.MethodPost, reqURL, payload)
	if err != nil {
		return fmt.Errorf("creating service account: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		// SA already exists — treat as success for idempotency.
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d creating service account: %s", resp.StatusCode, extractGCPErrorMessage(body))
	}
	return nil
}

// CreateServiceAccountKey generates a new JSON key for the service account.
func (c *LiveGCPClient) CreateServiceAccountKey(ctx context.Context, projectID, saEmail string) ([]byte, error) {
	if !gcpIDPattern.MatchString(projectID) {
		return nil, fmt.Errorf("invalid GCP project ID %q", projectID)
	}

	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/serviceAccounts/%s/keys",
		url.PathEscape(projectID), url.PathEscape(saEmail))
	payload := `{"keyAlgorithm":"KEY_ALG_RSA_2048","privateKeyType":"TYPE_GOOGLE_CREDENTIALS_FILE"}`

	resp, err := c.doRequest(ctx, http.MethodPost, reqURL, payload)
	if err != nil {
		return nil, fmt.Errorf("creating service account key: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d creating key: %s", resp.StatusCode, extractGCPErrorMessage(body))
	}

	var result struct {
		PrivateKeyData string `json:"privateKeyData"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding key response: %w", err)
	}

	// privateKeyData is base64-encoded JSON credentials.
	decoded, err := base64.StdEncoding.DecodeString(result.PrivateKeyData)
	if err != nil {
		return nil, fmt.Errorf("decoding private key data: %w", err)
	}

	return decoded, nil
}
