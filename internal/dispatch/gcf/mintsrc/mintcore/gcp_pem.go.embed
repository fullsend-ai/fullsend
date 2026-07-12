package mintcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GCPSecretPEMAccessor reads agent PEMs from GCP Secret Manager via REST API,
// authenticating with the GCE metadata server token.
// Secret naming convention: projects/{num}/secrets/fullsend-{role}-app-pem/versions/latest
type GCPSecretPEMAccessor struct {
	httpClient    HTTPDoer
	gcpProjectNum string
}

// NewGCPSecretPEMAccessor creates a PEM accessor that reads from GCP Secret Manager.
func NewGCPSecretPEMAccessor(httpClient HTTPDoer, gcpProjectNum string) *GCPSecretPEMAccessor {
	return &GCPSecretPEMAccessor{
		httpClient:    httpClient,
		gcpProjectNum: gcpProjectNum,
	}
}

// PemSecretRole returns the Secret Manager role key for a mint request role.
// The fix stage uses the coder PEM.
func PemSecretRole(role string) string {
	if role == "fix" {
		return "coder"
	}
	return role
}

func (s *GCPSecretPEMAccessor) AccessPEM(ctx context.Context, role string) ([]byte, error) {
	secretRole := PemSecretRole(role)
	if err := ValidateRoleName(secretRole); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("projects/%s/secrets/fullsend-%s-app-pem/versions/latest",
		s.gcpProjectNum, secretRole)
	token, err := s.metadataToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting metadata token: %w", err)
	}

	url := fmt.Sprintf("https://secretmanager.googleapis.com/v1/%s:access", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating secret request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accessing secret: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("secret access returned status %d", resp.StatusCode)
	}

	var result struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding secret response: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(result.Payload.Data)
	if err != nil {
		return nil, fmt.Errorf("decoding secret data: %w", err)
	}
	WarnWorkersPEMSize(secretRole, data)
	return data, nil
}

func (s *GCPSecretPEMAccessor) metadataToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata token request returned %d", resp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("metadata returned empty access token")
	}
	return tok.AccessToken, nil
}
