package gcf

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/gcp"
)

var operationNamePattern = regexp.MustCompile(`^[a-zA-Z0-9/_-]+$`)

// secretResourcePattern validates Secret Manager resource paths like
// "projects/{project}/secrets/{secret}".
var secretResourcePattern = regexp.MustCompile(`^projects/[a-z][a-z0-9-]+/secrets/[a-zA-Z0-9_-]+$`)

// secretVersionPattern validates Secret Manager version resource paths like
// "projects/{project_number}/secrets/{secret}/versions/{version_number}".
var secretVersionPattern = regexp.MustCompile(`^projects/[0-9]+/secrets/[a-zA-Z0-9_-]+/versions/[0-9]+$`)

// ErrSecretNotFound is returned when a Secret Manager secret does not exist.
var ErrSecretNotFound = errors.New("secret not found")

// OIDCProviderConfig holds configuration for a WIF OIDC provider.
type OIDCProviderConfig struct {
	IssuerURI          string
	AttributeCondition string
	AllowedAudiences   []string
}

// WIFProviderInfo holds metadata about a WIF OIDC provider.
type WIFProviderInfo struct {
	AttributeCondition string
	AllowedAudiences   []string
}

// FunctionInfo holds metadata about a deployed Cloud Function.
type FunctionInfo struct {
	Name    string
	State   string
	URI     string
	Region  string
	EnvVars map[string]string
}

// ServiceRevisionInfo holds Cloud Run service details including which
// revision is serving traffic, recent revision history, and whether
// the service template diverges from the traffic-serving revision.
type ServiceRevisionInfo struct {
	// TrafficRevision is the full resource name of the revision currently serving traffic.
	TrafficRevision string
	// TrafficRevisionShort is the short revision name (e.g., "fullsend-mint-00114-fm9").
	TrafficRevisionShort string
	// TrafficAllocType is the traffic allocation type (e.g., "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST").
	TrafficAllocType string
	// TrafficPercent is the percentage of traffic the serving revision receives.
	TrafficPercent int
	// TemplateRevision is the revision name from the service template (latest created).
	TemplateRevision string
	// TemplateMatchesTraffic is true when the template's latest revision matches
	// the traffic-serving revision.
	TemplateMatchesTraffic bool
	// RecentRevisions lists the most recent revisions (up to 5), newest first.
	RecentRevisions []RevisionSummary
	// TrafficEnvVars holds the env vars from the traffic-serving revision.
	TrafficEnvVars map[string]string
}

// RevisionSummary is a brief snapshot of a Cloud Run revision.
type RevisionSummary struct {
	Name       string // short name, e.g. "fullsend-mint-00114-fm9"
	CreateTime string // RFC3339 timestamp
	Active     bool   // true if the revision has conditions indicating it is active/ready
}

// FunctionConfig holds parameters for creating a Cloud Function.
type FunctionConfig struct {
	ServiceAccount string
	EnvVars        map[string]string
	StorageSource  json.RawMessage // structured object from generateUploadUrl response
	EntryPoint     string
	Runtime        string
}

// GCFClient abstracts GCP API operations needed by the provisioner.
type GCFClient interface {
	// Service account operations
	CreateServiceAccount(ctx context.Context, projectID, saName, displayName string) error

	// WIF operations
	CreateWIFPool(ctx context.Context, projectNumber, poolID, displayName string) error
	CreateWIFProvider(ctx context.Context, projectNumber, poolID, providerID string, cfg OIDCProviderConfig) error
	GetWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) (*WIFProviderInfo, error)
	UpdateWIFProvider(ctx context.Context, projectNumber, poolID, providerID string, cfg OIDCProviderConfig) error
	DisableWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) error
	DeleteWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) error

	// Secret Manager
	GetSecret(ctx context.Context, projectID, secretID string) error
	CreateSecret(ctx context.Context, projectID, secretID string) error
	AddSecretVersion(ctx context.Context, projectID, secretID string, data []byte) error
	AccessSecretVersion(ctx context.Context, projectID, secretID string) ([]byte, error)
	DisableSecretVersion(ctx context.Context, projectID, secretID string) error
	EnableSecretVersion(ctx context.Context, projectID, secretID string) error
	DeleteSecret(ctx context.Context, projectID, secretID string) error

	// IAM bindings
	SetSecretIAMBinding(ctx context.Context, resource, member, role string) error
	SetProjectIAMBinding(ctx context.Context, projectID, member, role string) error

	// Cloud Run IAM (for function invoker policy)
	SetCloudRunInvoker(ctx context.Context, projectID, region, serviceName string) error

	// Cloud Functions v2
	GetFunction(ctx context.Context, projectID, region, functionName string) (*FunctionInfo, error)
	UploadFunctionSource(ctx context.Context, projectID, region string, sourceZip []byte) (storageSource json.RawMessage, err error)
	CreateFunction(ctx context.Context, projectID, region, functionName string, cfg FunctionConfig) (string, error)
	UpdateFunction(ctx context.Context, projectID, region, functionName string, cfg FunctionConfig) (string, error)
	UpdateFunctionEnvVars(ctx context.Context, projectID, region, functionName string, envVars map[string]string) (string, error)
	// UpdateServiceEnvVars updates environment variables on the underlying
	// Cloud Run service directly, bypassing the Cloud Functions API. This
	// avoids triggering a source rebuild — only a new revision is created
	// reusing the existing container image. Uses a multi-step approach:
	// GETs the current service, PATCHes the template to create a new
	// revision, GETs the service again to discover the revision name,
	// then PATCHes traffic to pin 100% to that revision (REVISION-pinned,
	// matching Cloud Functions deploy behavior). Returns the new revision
	// name.
	UpdateServiceEnvVars(ctx context.Context, projectID, region, serviceName string, envVars map[string]string) (string, error)

	// GetServiceRevisionInfo returns Cloud Run service details including the
	// traffic-serving revision, template revision, allocation type, and recent
	// revision history. Used by mint status for revision awareness.
	GetServiceRevisionInfo(ctx context.Context, projectID, region, serviceName string) (*ServiceRevisionInfo, error)

	// GetServiceTrafficEnvVars reads environment variables from the Cloud Run
	// revision currently serving traffic, rather than from the service template.
	// This avoids reading stale data when the template and traffic-serving
	// revision have diverged.
	GetServiceTrafficEnvVars(ctx context.Context, projectID, region, serviceName string) (map[string]string, error)

	WaitForOperation(ctx context.Context, operationName string) error

	// Project number lookup
	GetProjectNumber(ctx context.Context, projectID string) (string, error)
}

// LiveGCFClient implements GCFClient using GCP REST APIs.
// It embeds *gcp.Client for shared ADC auth.
type LiveGCFClient struct {
	*gcp.Client
	skipUploadURLCheck bool // testing only: skip googleapis.com domain validation
}

// NewLiveGCFClient creates a new LiveGCFClient. The quotaProject is
// set as x-goog-user-project on every request so API quota is billed
// to the target project, not the shared ADC OAuth client project.
func NewLiveGCFClient(quotaProject string) *LiveGCFClient {
	c := gcp.NewClient()
	c.QuotaProject = quotaProject
	return &LiveGCFClient{Client: c}
}

// CreateServiceAccount creates a new service account.
func (c *LiveGCFClient) CreateServiceAccount(ctx context.Context, projectID, saName, displayName string) error {
	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/serviceAccounts",
		url.PathEscape(projectID))

	payloadObj := struct {
		AccountID      string `json:"accountId"`
		ServiceAccount struct {
			DisplayName string `json:"displayName"`
		} `json:"serviceAccount"`
	}{AccountID: saName}
	payloadObj.ServiceAccount.DisplayName = displayName
	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}
	payload := string(payloadBytes)

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, payload)
	if err != nil {
		return fmt.Errorf("creating service account: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil // already exists
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d creating service account: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// CreateWIFPool creates a new WIF pool.
func (c *LiveGCFClient) CreateWIFPool(ctx context.Context, projectNumber, poolID, displayName string) error {
	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools?workloadIdentityPoolId=%s",
		url.PathEscape(projectNumber), url.QueryEscape(poolID))
	payloadBytes, err := json.Marshal(map[string]string{"displayName": displayName})
	if err != nil {
		return fmt.Errorf("marshaling WIF pool payload: %w", err)
	}
	payload := string(payloadBytes)

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, payload)
	if err != nil {
		return fmt.Errorf("creating WIF pool: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil // already exists
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d creating WIF pool: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	if err := c.waitForIAMOperation(ctx, resp.Body); err != nil {
		return fmt.Errorf("waiting for WIF pool creation: %w", err)
	}
	return nil
}

// CreateWIFProvider creates a WIF OIDC provider.
func (c *LiveGCFClient) CreateWIFProvider(ctx context.Context, projectNumber, poolID, providerID string, cfg OIDCProviderConfig) error {
	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers?workloadIdentityPoolProviderId=%s",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.QueryEscape(providerID))

	oidcConfig := map[string]interface{}{
		"issuerUri": cfg.IssuerURI,
	}
	if len(cfg.AllowedAudiences) > 0 {
		oidcConfig["allowedAudiences"] = cfg.AllowedAudiences
	}

	payloadObj := map[string]interface{}{
		"oidc":               oidcConfig,
		"attributeCondition": cfg.AttributeCondition,
		"attributeMapping": map[string]string{
			"google.subject":             "assertion.sub",
			"attribute.repository_owner": "assertion.repository_owner",
			"attribute.repository":       "assertion.repository",
			"attribute.actor":            "assertion.actor",
		},
	}

	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return fmt.Errorf("marshaling WIF provider payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, string(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating WIF provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if err := c.undeleteWIFProvider(ctx, projectNumber, poolID, providerID); err != nil {
			log.Printf("undelete attempt during conflict recovery: %v", err)
		}
		if err := c.UpdateWIFProvider(ctx, projectNumber, poolID, providerID, cfg); err != nil {
			return err
		}
		return c.enableWIFProvider(ctx, projectNumber, poolID, providerID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d creating WIF provider: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	if err := c.waitForIAMOperation(ctx, resp.Body); err != nil {
		return fmt.Errorf("waiting for WIF provider creation: %w", err)
	}
	return nil
}

// GetWIFProvider reads an existing WIF OIDC provider's configuration.
func (c *LiveGCFClient) GetWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) (*WIFProviderInfo, error) {
	getURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.PathEscape(providerID))

	resp, err := c.Client.DoRequest(ctx, http.MethodGet, getURL, "")
	if err != nil {
		return nil, fmt.Errorf("getting WIF provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("getting WIF provider returned %d: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var provider struct {
		AttributeCondition string `json:"attributeCondition"`
		OIDC               struct {
			AllowedAudiences []string `json:"allowedAudiences"`
		} `json:"oidc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return nil, fmt.Errorf("decoding WIF provider: %w", err)
	}

	return &WIFProviderInfo{
		AttributeCondition: provider.AttributeCondition,
		AllowedAudiences:   provider.OIDC.AllowedAudiences,
	}, nil
}

// UpdateWIFProvider patches an existing WIF OIDC provider's attribute condition
// and allowed audiences.
func (c *LiveGCFClient) UpdateWIFProvider(ctx context.Context, projectNumber, poolID, providerID string, cfg OIDCProviderConfig) error {
	patchURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s?updateMask=attributeCondition,oidc.allowedAudiences",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.PathEscape(providerID))

	payloadObj := map[string]interface{}{
		"attributeCondition": cfg.AttributeCondition,
	}
	if len(cfg.AllowedAudiences) > 0 {
		payloadObj["oidc"] = map[string]interface{}{
			"allowedAudiences": cfg.AllowedAudiences,
		}
	}
	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return fmt.Errorf("marshaling WIF provider update: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPatch, patchURL, string(payloadBytes))
	if err != nil {
		return fmt.Errorf("updating WIF provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d updating WIF provider: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	if err := c.waitForIAMOperation(ctx, resp.Body); err != nil {
		return fmt.Errorf("waiting for WIF provider update: %w", err)
	}
	return nil
}

// undeleteWIFProvider restores a soft-deleted WIF provider.
// GCP WIF providers are soft-deleted with a 30-day grace period; creating a
// provider with the same ID during this window returns 409. Undeleting first
// allows the subsequent update to succeed.
func (c *LiveGCFClient) undeleteWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) error {
	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s:undelete",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.PathEscape(providerID))

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, "{}")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("undelete returned %d", resp.StatusCode)
	}
	return c.waitForIAMOperation(ctx, resp.Body)
}

// GetSecret checks that a Secret Manager secret exists.
func (c *LiveGCFClient) GetSecret(ctx context.Context, projectID, secretID string) error {
	reqURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s",
		url.PathEscape(projectID), url.PathEscape(secretID))

	resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
	if err != nil {
		return fmt.Errorf("checking secret: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("secret %s: %w", secretID, ErrSecretNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d checking secret: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// CreateSecret creates a Secret Manager secret.
func (c *LiveGCFClient) CreateSecret(ctx context.Context, projectID, secretID string) error {
	reqURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets?secretId=%s",
		url.PathEscape(projectID), url.QueryEscape(secretID))
	payload := `{"replication":{"automatic":{}}}`

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, payload)
	if err != nil {
		return fmt.Errorf("creating secret: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil // already exists
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d creating secret: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// AddSecretVersion adds a new version with the given data to an existing secret.
func (c *LiveGCFClient) AddSecretVersion(ctx context.Context, projectID, secretID string, data []byte) error {
	reqURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s:addVersion",
		url.PathEscape(projectID), url.PathEscape(secretID))

	payloadObj := map[string]interface{}{
		"payload": map[string]string{
			"data": encodeBase64(data),
		},
	}
	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return fmt.Errorf("marshaling secret version payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, string(payloadBytes))
	if err != nil {
		return fmt.Errorf("adding secret version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d adding secret version: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// AccessSecretVersion reads the latest version of a Secret Manager secret.
func (c *LiveGCFClient) AccessSecretVersion(ctx context.Context, projectID, secretID string) ([]byte, error) {
	reqURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s/versions/latest:access",
		url.PathEscape(projectID), url.PathEscape(secretID))

	resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
	if err != nil {
		return nil, fmt.Errorf("accessing secret version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("secret %s: %w", secretID, ErrSecretNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("unexpected status %d accessing secret version: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading secret version response: %w", err)
	}

	var result struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing secret version response: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(result.Payload.Data)
	if err != nil {
		return nil, fmt.Errorf("decoding secret payload: %w", err)
	}
	return data, nil
}

// DisableSecretVersion disables the latest version of a Secret Manager secret.
// The disable API rejects the "latest" alias, so we first resolve it to a
// numeric version via GET, then disable that version.
func (c *LiveGCFClient) DisableSecretVersion(ctx context.Context, projectID, secretID string) error {
	// Resolve "latest" to a numeric version name.
	getURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s/versions/latest",
		url.PathEscape(projectID), url.PathEscape(secretID))

	getResp, err := c.Client.DoRequest(ctx, http.MethodGet, getURL, "")
	if err != nil {
		return fmt.Errorf("resolving latest secret version: %w", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == http.StatusNotFound {
		return nil // No versions to disable.
	}
	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d resolving latest secret version: %s", getResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var version struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(getResp.Body, 1<<20)).Decode(&version); err != nil {
		return fmt.Errorf("decoding secret version metadata: %w", err)
	}
	if !secretVersionPattern.MatchString(version.Name) {
		return fmt.Errorf("secret version name %q does not match expected pattern", version.Name)
	}
	if version.State == "DISABLED" || version.State == "DESTROYED" {
		return nil
	}

	// Disable the resolved version.
	disableURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/%s:disable", version.Name)

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, disableURL, "{}")
	if err != nil {
		return fmt.Errorf("disabling secret version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Version deleted between resolve and disable; treat as success.
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d disabling secret version: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// EnableSecretVersion enables the latest version of a Secret Manager secret.
// Mirrors DisableSecretVersion: resolves "latest" to a numeric version, then
// enables it. No-op if the version is already enabled or does not exist.
func (c *LiveGCFClient) EnableSecretVersion(ctx context.Context, projectID, secretID string) error {
	getURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s/versions/latest",
		url.PathEscape(projectID), url.PathEscape(secretID))

	getResp, err := c.Client.DoRequest(ctx, http.MethodGet, getURL, "")
	if err != nil {
		return fmt.Errorf("resolving latest secret version: %w", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == http.StatusNotFound {
		return nil
	}
	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d resolving latest secret version: %s", getResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var version struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(getResp.Body, 1<<20)).Decode(&version); err != nil {
		return fmt.Errorf("decoding secret version metadata: %w", err)
	}
	if !secretVersionPattern.MatchString(version.Name) {
		return fmt.Errorf("secret version name %q does not match expected pattern", version.Name)
	}
	if version.State == "ENABLED" {
		return nil
	}
	if version.State == "DESTROYED" {
		return fmt.Errorf("secret version %s is destroyed and cannot be re-enabled", version.Name)
	}

	enableURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/%s:enable", version.Name)

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, enableURL, "{}")
	if err != nil {
		return fmt.Errorf("enabling secret version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d enabling secret version: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// DeleteSecret permanently deletes a Secret Manager secret and all its versions.
func (c *LiveGCFClient) DeleteSecret(ctx context.Context, projectID, secretID string) error {
	reqURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s",
		url.PathEscape(projectID), url.PathEscape(secretID))

	resp, err := c.Client.DoRequest(ctx, http.MethodDelete, reqURL, "")
	if err != nil {
		return fmt.Errorf("deleting secret: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted.
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d deleting secret: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// DisableWIFProvider sets a WIF provider's disabled state to true.
func (c *LiveGCFClient) DisableWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) error {
	patchURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s?updateMask=disabled",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.PathEscape(providerID))

	payloadBytes, err := json.Marshal(map[string]interface{}{
		"disabled": true,
	})
	if err != nil {
		return fmt.Errorf("marshaling disable payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPatch, patchURL, string(payloadBytes))
	if err != nil {
		return fmt.Errorf("disabling WIF provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted or never existed.
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d disabling WIF provider: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	return c.waitForIAMOperation(ctx, resp.Body)
}

// enableWIFProvider sets a WIF provider's disabled state to false.
func (c *LiveGCFClient) enableWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) error {
	patchURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s?updateMask=disabled",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.PathEscape(providerID))

	payloadBytes, err := json.Marshal(map[string]interface{}{
		"disabled": false,
	})
	if err != nil {
		return fmt.Errorf("marshaling enable payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPatch, patchURL, string(payloadBytes))
	if err != nil {
		return fmt.Errorf("enabling WIF provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d enabling WIF provider: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	return c.waitForIAMOperation(ctx, resp.Body)
}

// DeleteWIFProvider permanently deletes a WIF provider.
func (c *LiveGCFClient) DeleteWIFProvider(ctx context.Context, projectNumber, poolID, providerID string) error {
	deleteURL := fmt.Sprintf("https://iam.googleapis.com/v1/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		url.PathEscape(projectNumber), url.PathEscape(poolID), url.PathEscape(providerID))

	resp, err := c.Client.DoRequest(ctx, http.MethodDelete, deleteURL, "")
	if err != nil {
		return fmt.Errorf("deleting WIF provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted.
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d deleting WIF provider: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	return c.waitForIAMOperation(ctx, resp.Body)
}

// SetSecretIAMBinding sets an IAM binding on a Secret Manager resource.
// Uses read-modify-write with retry on 409 Conflict (etag mismatch).
func (c *LiveGCFClient) SetSecretIAMBinding(ctx context.Context, resource, member, role string) error {
	if !secretResourcePattern.MatchString(resource) {
		return fmt.Errorf("invalid secret resource path %q", resource)
	}
	const maxRetries = 3
	getURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/%s:getIamPolicy", resource)
	setURL := fmt.Sprintf("https://secretmanager.googleapis.com/v1/%s:setIamPolicy", resource)

	for attempt := range maxRetries {
		err := c.trySetIAMBinding(ctx, http.MethodGet, "", getURL, setURL, member, role)
		if err == nil {
			return nil
		}
		if !isConflict(err) || attempt == maxRetries-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(200*(attempt+1)) * time.Millisecond):
		}
	}
	return fmt.Errorf("IAM policy update failed after %d retries", maxRetries)
}

type conflictError struct{ status int }

func (e *conflictError) Error() string {
	return fmt.Sprintf("IAM policy conflict (status %d)", e.status)
}

func isConflict(err error) bool {
	var ce *conflictError
	return errors.As(err, &ce)
}

// SetProjectIAMBinding sets an IAM binding on a GCP project.
// Uses read-modify-write with retry on 409 Conflict (etag mismatch).
func (c *LiveGCFClient) SetProjectIAMBinding(ctx context.Context, projectID, member, role string) error {
	const maxRetries = 3
	getURL := fmt.Sprintf("https://cloudresourcemanager.googleapis.com/v1/projects/%s:getIamPolicy",
		url.PathEscape(projectID))
	setURL := fmt.Sprintf("https://cloudresourcemanager.googleapis.com/v1/projects/%s:setIamPolicy",
		url.PathEscape(projectID))

	for attempt := range maxRetries {
		err := c.trySetIAMBinding(ctx, http.MethodPost, "{}", getURL, setURL, member, role)
		if err == nil {
			return nil
		}
		if !isConflict(err) || attempt == maxRetries-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(200*(attempt+1)) * time.Millisecond):
		}
	}
	return fmt.Errorf("project IAM policy update failed after %d retries", maxRetries)
}

// trySetIAMBinding performs a single read-modify-write IAM policy update.
// getMethod/getBody control the getIamPolicy request (GET+"" for Secret Manager,
// POST+"{}" for Cloud Resource Manager). setIamPolicy always uses POST.
func (c *LiveGCFClient) trySetIAMBinding(ctx context.Context, getMethod, getBody, getURL, setURL, member, role string) error {
	resp, err := c.Client.DoRequest(ctx, getMethod, getURL, getBody)
	if err != nil {
		return fmt.Errorf("getting IAM policy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("getting IAM policy returned %d: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var policy map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&policy); err != nil {
		return fmt.Errorf("decoding IAM policy: %w", err)
	}

	bindings, _ := policy["bindings"].([]interface{})

	found := false
	for _, b := range bindings {
		binding, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		if binding["role"] != role {
			continue
		}
		members, _ := binding["members"].([]interface{})
		for _, m := range members {
			if m == member {
				return nil
			}
		}
		binding["members"] = append(members, member)
		found = true
		break
	}

	if !found {
		bindings = append(bindings, map[string]interface{}{
			"role":    role,
			"members": []string{member},
		})
		policy["bindings"] = bindings
	}

	policyPayload := map[string]interface{}{"policy": policy}
	payloadBytes, err := json.Marshal(policyPayload)
	if err != nil {
		return fmt.Errorf("marshaling IAM policy: %w", err)
	}

	setResp, err := c.Client.DoRequest(ctx, http.MethodPost, setURL, string(payloadBytes))
	if err != nil {
		return fmt.Errorf("setting IAM policy: %w", err)
	}
	defer setResp.Body.Close()

	if setResp.StatusCode == http.StatusConflict {
		io.Copy(io.Discard, io.LimitReader(setResp.Body, 1<<20))
		return &conflictError{status: setResp.StatusCode}
	}
	if setResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(setResp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d setting IAM policy: %s", setResp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return nil
}

// SetCloudRunInvoker ensures allUsers has roles/run.invoker on the Cloud Run
// service backing a Cloud Function. Uses read-modify-write with retry on 409
// (etag conflict) to preserve existing bindings. The function's own OIDC
// validation is the security boundary.
func (c *LiveGCFClient) SetCloudRunInvoker(ctx context.Context, projectID, region, serviceName string) error {
	baseURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/services/%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(serviceName))

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		done, err := c.trySetCloudRunInvoker(ctx, baseURL)
		if done || err == nil {
			return err
		}
		lastErr = err
	}
	return lastErr
}

func (c *LiveGCFClient) trySetCloudRunInvoker(ctx context.Context, baseURL string) (done bool, _ error) {
	getResp, err := c.Client.DoRequest(ctx, http.MethodGet, baseURL+":getIamPolicy", "")
	if err != nil {
		return true, fmt.Errorf("getting Cloud Run IAM policy: %w", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
		return true, fmt.Errorf("getting Cloud Run IAM policy returned %d: %s", getResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var policy map[string]interface{}
	if err := json.NewDecoder(getResp.Body).Decode(&policy); err != nil {
		return true, fmt.Errorf("decoding Cloud Run IAM policy: %w", err)
	}

	const role = "roles/run.invoker"
	const member = "allUsers"
	bindings, _ := policy["bindings"].([]interface{})

	found := false
	for _, b := range bindings {
		binding, ok := b.(map[string]interface{})
		if !ok || binding["role"] != role {
			continue
		}
		members, _ := binding["members"].([]interface{})
		for _, m := range members {
			if m == member {
				return true, nil
			}
		}
		binding["members"] = append(members, member)
		found = true
		break
	}
	if !found {
		bindings = append(bindings, map[string]interface{}{
			"role":    role,
			"members": []string{member},
		})
		policy["bindings"] = bindings
	}

	policyPayload := map[string]interface{}{"policy": policy}
	payloadBytes, err := json.Marshal(policyPayload)
	if err != nil {
		return true, fmt.Errorf("marshaling Cloud Run IAM policy: %w", err)
	}

	setResp, err := c.Client.DoRequest(ctx, http.MethodPost, baseURL+":setIamPolicy", string(payloadBytes))
	if err != nil {
		return true, fmt.Errorf("setting Cloud Run invoker: %w", err)
	}
	defer setResp.Body.Close()

	if setResp.StatusCode == http.StatusConflict {
		io.Copy(io.Discard, io.LimitReader(setResp.Body, 1<<20))
		return false, fmt.Errorf("IAM policy conflict (will retry)")
	}
	if setResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(setResp.Body, 1<<20))
		return true, fmt.Errorf("unexpected status %d setting invoker: %s", setResp.StatusCode, gcp.ExtractErrorMessage(body))
	}
	return true, nil
}

// GetFunction checks if a Cloud Function exists and returns its info.
func (c *LiveGCFClient) GetFunction(ctx context.Context, projectID, region, functionName string) (*FunctionInfo, error) {
	reqURL := fmt.Sprintf("https://cloudfunctions.googleapis.com/v2/projects/%s/locations/%s/functions/%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(functionName))

	resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
	if err != nil {
		return nil, fmt.Errorf("checking function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("unexpected status %d checking function: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var result struct {
		Name          string `json:"name"`
		State         string `json:"state"`
		ServiceConfig struct {
			URI                  string            `json:"uri"`
			EnvironmentVariables map[string]string `json:"environmentVariables"`
		} `json:"serviceConfig"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding function info: %w", err)
	}

	return &FunctionInfo{
		Name:    result.Name,
		State:   result.State,
		URI:     result.ServiceConfig.URI,
		Region:  region,
		EnvVars: result.ServiceConfig.EnvironmentVariables,
	}, nil
}

// UploadFunctionSource generates a signed upload URL and uploads the source zip.
// Returns the storage source URI for use in CreateFunction.
func (c *LiveGCFClient) UploadFunctionSource(ctx context.Context, projectID, region string, sourceZip []byte) (json.RawMessage, error) {
	reqURL := fmt.Sprintf("https://cloudfunctions.googleapis.com/v2/projects/%s/locations/%s/functions:generateUploadUrl",
		url.PathEscape(projectID), url.PathEscape(region))

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, "{}")
	if err != nil {
		return nil, fmt.Errorf("generating upload URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("unexpected status %d generating upload URL: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var result struct {
		UploadURL     string          `json:"uploadUrl"`
		StorageSource json.RawMessage `json:"storageSource"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding upload URL response: %w", err)
	}
	if len(result.StorageSource) == 0 {
		return nil, fmt.Errorf("empty storage source returned from upload API")
	}

	if !c.skipUploadURLCheck {
		parsedURL, err := url.Parse(result.UploadURL)
		if err != nil || parsedURL.Scheme != "https" ||
			(parsedURL.Host != "storage.googleapis.com" && !strings.HasSuffix(parsedURL.Host, ".storage.googleapis.com")) {
			host := ""
			if parsedURL != nil {
				host = parsedURL.Host
			}
			return nil, fmt.Errorf("upload URL has unexpected host %q (expected *.storage.googleapis.com)", host)
		}
	}

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPut, result.UploadURL, bytes.NewReader(sourceZip))
	if err != nil {
		return nil, fmt.Errorf("creating upload request: %w", err)
	}
	uploadReq.Header.Set("Content-Type", "application/zip")

	uploadClient := &http.Client{Timeout: 5 * time.Minute}
	uploadResp, err := uploadClient.Do(uploadReq)
	if err != nil {
		return nil, fmt.Errorf("uploading source: %w", err)
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 4096))
		return nil, fmt.Errorf("upload returned %d: %s", uploadResp.StatusCode, string(body))
	}

	return result.StorageSource, nil
}

// CreateFunction creates a Cloud Function v2.
func (c *LiveGCFClient) CreateFunction(ctx context.Context, projectID, region, functionName string, cfg FunctionConfig) (string, error) {
	reqURL := fmt.Sprintf("https://cloudfunctions.googleapis.com/v2/projects/%s/locations/%s/functions?functionId=%s",
		url.PathEscape(projectID), url.PathEscape(region), url.QueryEscape(functionName))

	resourceName := fmt.Sprintf("projects/%s/locations/%s/functions/%s",
		projectID, region, functionName)

	payloadObj := map[string]interface{}{
		"name": resourceName,
		"buildConfig": map[string]interface{}{
			"runtime":    cfg.Runtime,
			"entryPoint": cfg.EntryPoint,
			"source": map[string]interface{}{
				"storageSource": cfg.StorageSource,
			},
		},
		"serviceConfig": map[string]interface{}{
			"serviceAccountEmail":           cfg.ServiceAccount,
			"environmentVariables":          cfg.EnvVars,
			"availableMemory":               "256Mi",
			"availableCpu":                  "1",
			"maxInstanceCount":              10,
			"maxInstanceRequestConcurrency": 80,
		},
	}

	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return "", fmt.Errorf("marshaling function payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, string(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("creating function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d creating function: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	// The response is a long-running operation. For now, return a placeholder.
	// The actual URL is retrieved after the operation completes.
	var result struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding create function response: %w", err)
	}

	return result.Name, nil
}

// UpdateFunction updates an existing Cloud Function v2 using the PATCH API.
// It returns a long-running operation name that can be polled with WaitForOperation.
func (c *LiveGCFClient) UpdateFunction(ctx context.Context, projectID, region, functionName string, cfg FunctionConfig) (string, error) {
	reqURL := fmt.Sprintf("https://cloudfunctions.googleapis.com/v2/projects/%s/locations/%s/functions/%s?updateMask=%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(functionName),
		url.QueryEscape("buildConfig.source,buildConfig.runtime,buildConfig.entryPoint,serviceConfig.environmentVariables,serviceConfig.serviceAccountEmail,serviceConfig.availableMemory,serviceConfig.availableCpu,serviceConfig.maxInstanceCount,serviceConfig.maxInstanceRequestConcurrency"))

	resourceName := fmt.Sprintf("projects/%s/locations/%s/functions/%s",
		projectID, region, functionName)

	payloadObj := map[string]interface{}{
		"name": resourceName,
		"buildConfig": map[string]interface{}{
			"runtime":    cfg.Runtime,
			"entryPoint": cfg.EntryPoint,
			"source": map[string]interface{}{
				"storageSource": cfg.StorageSource,
			},
		},
		"serviceConfig": map[string]interface{}{
			"serviceAccountEmail":           cfg.ServiceAccount,
			"environmentVariables":          cfg.EnvVars,
			"availableMemory":               "256Mi",
			"availableCpu":                  "1",
			"maxInstanceCount":              10,
			"maxInstanceRequestConcurrency": 80,
		},
	}

	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return "", fmt.Errorf("marshaling function update payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPatch, reqURL, string(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("updating function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d updating function: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding update function response: %w", err)
	}

	return result.Name, nil
}

// UpdateFunctionEnvVars updates only the environment variables of a Cloud
// Function v2, leaving build config and other service config untouched.
func (c *LiveGCFClient) UpdateFunctionEnvVars(ctx context.Context, projectID, region, functionName string, envVars map[string]string) (string, error) {
	reqURL := fmt.Sprintf("https://cloudfunctions.googleapis.com/v2/projects/%s/locations/%s/functions/%s?updateMask=%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(functionName),
		url.QueryEscape("serviceConfig.environmentVariables"))

	resourceName := fmt.Sprintf("projects/%s/locations/%s/functions/%s",
		projectID, region, functionName)

	payloadObj := map[string]interface{}{
		"name": resourceName,
		"serviceConfig": map[string]interface{}{
			"environmentVariables": envVars,
		},
	}

	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return "", fmt.Errorf("marshaling env vars update payload: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPatch, reqURL, string(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("updating function env vars: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d updating function env vars: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding env vars update response: %w", err)
	}

	return result.Name, nil
}

// UpdateServiceEnvVars updates environment variables on the underlying
// Cloud Run service directly via the Cloud Run Admin API v2, bypassing the
// Cloud Functions API entirely. This avoids triggering a source rebuild —
// only a new Cloud Run revision is created reusing the existing container
// image.
//
// Uses a multi-step approach to match Cloud Functions deploy behavior:
//  1. GET current service to preserve container config
//  2. PATCH template to create a new revision with updated env vars
//  3. GET service to discover the new revision name
//  4. PATCH traffic to pin 100% to the new revision (REVISION-pinned)
//
// This ensures explicit revision-pinned traffic routing. If the traffic
// PATCH fails, the old revision continues serving — no traffic is lost.
// Returns the new revision name for CLI reporting and rollback reference.
//
// See https://github.com/fullsend-ai/fullsend/issues/1844.
func (c *LiveGCFClient) UpdateServiceEnvVars(ctx context.Context, projectID, region, serviceName string, envVars map[string]string) (string, error) {
	serviceURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/services/%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(serviceName))

	// Step 1: GET current service to preserve container config (image, ports, etc.).
	getResp, err := c.Client.DoRequest(ctx, http.MethodGet, serviceURL, "")
	if err != nil {
		return "", fmt.Errorf("getting Cloud Run service: %w", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d getting Cloud Run service: %s", getResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var service map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(getResp.Body, 10<<20)).Decode(&service); err != nil {
		return "", fmt.Errorf("decoding Cloud Run service: %w", err)
	}

	// Build the env var array in Cloud Run format: [{name, value}, ...].
	// Sort keys for deterministic PATCH payloads.
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	envArray := make([]map[string]string, 0, len(envVars))
	for _, k := range keys {
		envArray = append(envArray, map[string]string{"name": k, "value": envVars[k]})
	}

	// Navigate to template.containers[0] and replace its env field.
	template, _ := service["template"].(map[string]interface{})
	if template == nil {
		return "", fmt.Errorf("Cloud Run service has no template")
	}
	// Remove revision name so Cloud Run auto-generates one; re-sending the
	// existing name causes a 409 "revision already exists" conflict.
	delete(template, "revision")
	containers, _ := template["containers"].([]interface{})
	if len(containers) == 0 {
		return "", fmt.Errorf("Cloud Run service has no containers")
	}
	container, _ := containers[0].(map[string]interface{})
	if container == nil {
		return "", fmt.Errorf("Cloud Run service container is not an object")
	}
	container["env"] = envArray

	// Remove traffic from the payload — we handle it in a separate PATCH
	// below to ensure explicit revision-pinned routing.
	delete(service, "traffic")

	payloadBytes, err := json.Marshal(service)
	if err != nil {
		return "", fmt.Errorf("marshaling Cloud Run service update: %w", err)
	}

	// Step 2: PATCH the template to create a new revision.
	// updateMask covers template.revision (cleared so Cloud Run
	// auto-generates a new name) and template.containers (env vars).
	patchResp, err := c.Client.DoRequest(ctx, http.MethodPatch, serviceURL+"?updateMask=template.revision,template.containers", string(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("patching Cloud Run service template: %w", err)
	}
	defer patchResp.Body.Close()

	if patchResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(patchResp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d patching Cloud Run service: %s", patchResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	if err := c.handleCloudRunLRO(ctx, patchResp); err != nil {
		return "", fmt.Errorf("waiting for template update: %w", err)
	}

	// Step 3: GET service again to discover the new revision name.
	getResp2, err := c.Client.DoRequest(ctx, http.MethodGet, serviceURL, "")
	if err != nil {
		return "", fmt.Errorf("getting Cloud Run service after template update: %w", err)
	}
	defer getResp2.Body.Close()

	if getResp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp2.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d getting Cloud Run service after update: %s", getResp2.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var updatedService struct {
		LatestCreatedRevision string `json:"latestCreatedRevision"`
	}
	if err := json.NewDecoder(io.LimitReader(getResp2.Body, 10<<20)).Decode(&updatedService); err != nil {
		return "", fmt.Errorf("decoding Cloud Run service after update: %w", err)
	}
	newRevision := updatedService.LatestCreatedRevision
	if newRevision == "" {
		return "", fmt.Errorf("Cloud Run service has no latestCreatedRevision after template update")
	}

	// The Cloud Run v2 API returns fully qualified revision names from GET
	// (projects/P/locations/L/services/S/revisions/R) but the traffic PATCH
	// expects short names (e.g., "fullsend-mint-00115-qp5"). Extract the
	// short name for the traffic payload.
	revisionShort := newRevision
	if parts := strings.Split(newRevision, "/"); len(parts) > 1 {
		revisionShort = parts[len(parts)-1]
	}
	if !revisionShortNamePattern.MatchString(revisionShort) {
		return "", fmt.Errorf("unexpected revision name format in latestCreatedRevision: %q", newRevision)
	}

	// Step 4: PATCH traffic to pin 100% to the new revision.
	trafficPayload, err := json.Marshal(map[string]interface{}{
		"traffic": []map[string]interface{}{
			{
				"type":     "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
				"revision": revisionShort,
				"percent":  100,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshaling traffic update: %w", err)
	}

	trafficResp, err := c.Client.DoRequest(ctx, http.MethodPatch, serviceURL+"?updateMask=traffic", string(trafficPayload))
	if err != nil {
		return newRevision, fmt.Errorf("patching Cloud Run traffic: %w", err)
	}
	defer trafficResp.Body.Close()

	if trafficResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(trafficResp.Body, 1<<20))
		return newRevision, fmt.Errorf("unexpected status %d patching Cloud Run traffic: %s", trafficResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	if err := c.handleCloudRunLRO(ctx, trafficResp); err != nil {
		return newRevision, fmt.Errorf("waiting for traffic update: %w", err)
	}

	return newRevision, nil
}

// handleCloudRunLRO reads the LRO response from a Cloud Run PATCH and
// waits for it to complete if not already done.
func (c *LiveGCFClient) handleCloudRunLRO(ctx context.Context, resp *http.Response) error {
	var op struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&op); err != nil {
		return fmt.Errorf("decoding Cloud Run patch response: %w", err)
	}

	if op.Done {
		if op.Error != nil {
			return fmt.Errorf("Cloud Run service update failed: %s", op.Error.Message)
		}
		return nil
	}
	if op.Name == "" {
		return fmt.Errorf("Cloud Run PATCH returned incomplete response: done=false with no operation name")
	}

	return c.waitForCloudRunOperation(ctx, op.Name)
}

// revisionNamePattern validates Cloud Run revision resource names before
// they are interpolated into URLs. Prevents SSRF-adjacent issues if the
// API ever returns unexpected data.
var revisionNamePattern = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/services/[^/]+/revisions/[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// revisionShortNamePattern validates short Cloud Run revision names
// (e.g., "fullsend-mint-00114-fm9"). Used to sanitize revision names
// from list responses before displaying in terminal output.
var revisionShortNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// GetServiceTrafficEnvVars reads environment variables from the Cloud Run
// revision that is currently serving traffic, rather than from the service
// template. Although UpdateServiceEnvVars now pins traffic to the new
// revision, divergence can still occur if the traffic PATCH fails (partial
// failure) or from historical deployments before traffic pinning was added.
// This method resolves that by finding the revision that has traffic allocated
// and reading its container env vars directly.
//
// Uses the trafficStatuses field (observed state) rather than the traffic
// field (desired config) so that revision names are always resolved — even
// for TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST entries — and the data reflects
// actual serving state rather than desired configuration.
func (c *LiveGCFClient) GetServiceTrafficEnvVars(ctx context.Context, projectID, region, serviceName string) (map[string]string, error) {
	serviceURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/services/%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(serviceName))

	getResp, err := c.Client.DoRequest(ctx, http.MethodGet, serviceURL, "")
	if err != nil {
		return nil, fmt.Errorf("getting Cloud Run service: %w", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
		return nil, fmt.Errorf("unexpected status %d getting Cloud Run service: %s", getResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var service struct {
		Template struct {
			Containers []struct {
				Env []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
		} `json:"template"`
		// trafficStatuses is the observed state (output-only), with resolved
		// revision names. Preferred over the traffic field (desired/input)
		// which may have empty revision names for LATEST-type entries.
		TrafficStatuses []struct {
			Type     string `json:"type"`
			Revision string `json:"revision"`
			Percent  int    `json:"percent"`
		} `json:"trafficStatuses"`
	}
	if err := json.NewDecoder(io.LimitReader(getResp.Body, 10<<20)).Decode(&service); err != nil {
		return nil, fmt.Errorf("decoding Cloud Run service: %w", err)
	}

	// Find the revision currently serving the most traffic. When traffic is
	// split across revisions (e.g., canary), pick the one with the highest
	// percent to read the authoritative env vars.
	var trafficRevision string
	var maxPercent int
	for _, t := range service.TrafficStatuses {
		if t.Percent > maxPercent && t.Revision != "" {
			maxPercent = t.Percent
			trafficRevision = t.Revision
		}
	}

	// If no traffic statuses are reported (e.g., service is still
	// reconciling after initial create, or the API returned an empty list),
	// fall back to reading from the service template. This covers the
	// common case where template and traffic are aligned.
	if trafficRevision == "" {
		envVars := make(map[string]string)
		if len(service.Template.Containers) > 0 {
			for _, e := range service.Template.Containers[0].Env {
				envVars[e.Name] = e.Value
			}
		}
		return envVars, nil
	}

	// The Cloud Run v2 API may return either a fully qualified resource name
	// (projects/P/locations/L/services/S/revisions/R) or a short revision
	// name (e.g., "fullsend-mint-00114-fm9"). Validate whichever format we
	// receive and construct the full URL accordingly.
	var revisionResourceName string
	if revisionNamePattern.MatchString(trafficRevision) {
		revisionResourceName = trafficRevision
	} else if revisionShortNamePattern.MatchString(trafficRevision) {
		revisionResourceName = fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/%s",
			url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(serviceName), url.PathEscape(trafficRevision))
	} else {
		return nil, fmt.Errorf("unexpected traffic revision name format: %q", trafficRevision)
	}

	// GET the specific traffic-serving revision.
	revisionURL := fmt.Sprintf("https://run.googleapis.com/v2/%s", revisionResourceName)
	revResp, err := c.Client.DoRequest(ctx, http.MethodGet, revisionURL, "")
	if err != nil {
		return nil, fmt.Errorf("getting traffic-serving revision: %w", err)
	}
	defer revResp.Body.Close()

	if revResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(revResp.Body, 1<<20))
		return nil, fmt.Errorf("unexpected status %d getting revision %s: %s", revResp.StatusCode, trafficRevision, gcp.ExtractErrorMessage(body))
	}

	var revision struct {
		Containers []struct {
			Env []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"containers"`
	}
	if err := json.NewDecoder(io.LimitReader(revResp.Body, 10<<20)).Decode(&revision); err != nil {
		return nil, fmt.Errorf("decoding revision: %w", err)
	}

	if len(revision.Containers) == 0 {
		return nil, fmt.Errorf("traffic-serving revision %s has no containers", trafficRevision)
	}

	envVars := make(map[string]string)
	for _, e := range revision.Containers[0].Env {
		envVars[e.Name] = e.Value
	}
	return envVars, nil
}

// waitForCloudRunOperation polls a Cloud Run LRO until it completes.
// Cloud Run operations are polled at run.googleapis.com, not
// cloudfunctions.googleapis.com.
func (c *LiveGCFClient) waitForCloudRunOperation(ctx context.Context, operationName string) error {
	if !operationNamePattern.MatchString(operationName) {
		return fmt.Errorf("invalid Cloud Run operation name format: %q", operationName)
	}
	reqURL := fmt.Sprintf("https://run.googleapis.com/v2/%s", operationName)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for {
		resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
		if err != nil {
			return fmt.Errorf("polling Cloud Run operation: %w", err)
		}

		var op struct {
			Done  bool `json:"done"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&op)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decoding Cloud Run operation status: %w", decodeErr)
		}

		if op.Done {
			if op.Error != nil {
				return fmt.Errorf("Cloud Run operation failed: %s", op.Error.Message)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// GetServiceRevisionInfo returns Cloud Run service details including the
// traffic-serving revision, template revision, allocation type, and recent
// revision history.
func (c *LiveGCFClient) GetServiceRevisionInfo(ctx context.Context, projectID, region, serviceName string) (*ServiceRevisionInfo, error) {
	serviceURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/services/%s",
		url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(serviceName))

	// 1. GET the service.
	getResp, err := c.Client.DoRequest(ctx, http.MethodGet, serviceURL, "")
	if err != nil {
		return nil, fmt.Errorf("getting Cloud Run service: %w", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
		return nil, fmt.Errorf("unexpected status %d getting Cloud Run service: %s", getResp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var service struct {
		Template struct {
			Revision   string `json:"revision"`
			Containers []struct {
				Env []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
		} `json:"template"`
		// trafficStatuses is the observed state (output-only), with resolved
		// revision names. Preferred over the traffic field (desired/input)
		// which may have empty revision names for LATEST-type entries.
		TrafficStatuses []struct {
			Type     string `json:"type"`
			Revision string `json:"revision"`
			Percent  int    `json:"percent"`
		} `json:"trafficStatuses"`
		LatestReadyRevision string `json:"latestReadyRevision"`
	}
	svcBody, _ := io.ReadAll(io.LimitReader(getResp.Body, 10<<20))
	if err := json.Unmarshal(svcBody, &service); err != nil {
		return nil, fmt.Errorf("decoding Cloud Run service: %w", err)
	}

	info := &ServiceRevisionInfo{
		TemplateRevision: service.Template.Revision,
	}

	// Find the revision currently serving the most traffic. Uses
	// trafficStatuses (observed state) rather than traffic (desired config)
	// so revision names are always resolved and the data reflects actual
	// serving state. When traffic is split, pick the highest-percent entry.
	var maxPercent int
	for _, t := range service.TrafficStatuses {
		if t.Percent > maxPercent && t.Revision != "" {
			maxPercent = t.Percent
			info.TrafficRevision = t.Revision
			info.TrafficAllocType = t.Type
		}
	}

	info.TrafficPercent = maxPercent

	// Extract short revision name from the full resource name.
	if info.TrafficRevision != "" {
		parts := strings.Split(info.TrafficRevision, "/")
		info.TrafficRevisionShort = parts[len(parts)-1]
	}

	// Determine if template matches traffic.
	latestReadyShort := ""
	if service.LatestReadyRevision != "" {
		lParts := strings.Split(service.LatestReadyRevision, "/")
		latestReadyShort = lParts[len(lParts)-1]
	}
	if info.TrafficRevisionShort == "" {
		// Cannot determine traffic state — treat as not matching to avoid false confidence.
		info.TemplateMatchesTraffic = false
	} else {
		info.TemplateMatchesTraffic = info.TrafficRevisionShort == latestReadyShort
	}

	// 2. List recent revisions.
	revisionsURL := fmt.Sprintf("%s/revisions?pageSize=5", serviceURL)
	revListResp, err := c.Client.DoRequest(ctx, http.MethodGet, revisionsURL, "")
	if err != nil {
		// Non-fatal: we can still return partial info.
		return info, nil
	}
	defer revListResp.Body.Close()

	if revListResp.StatusCode == http.StatusOK {
		var revList struct {
			Revisions []struct {
				Name       string `json:"name"`
				CreateTime string `json:"createTime"`
				Conditions []struct {
					Type   string `json:"type"`
					State  string `json:"state"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"revisions"`
		}
		revBody, _ := io.ReadAll(io.LimitReader(revListResp.Body, 10<<20))
		if err := json.Unmarshal(revBody, &revList); err == nil {
			for _, rev := range revList.Revisions {
				parts := strings.Split(rev.Name, "/")
				shortName := parts[len(parts)-1]
				// Validate revision short name to prevent terminal injection from
				// malformed API responses. Skip entries that don't match the expected
				// Cloud Run revision name format.
				if !revisionShortNamePattern.MatchString(shortName) {
					continue
				}
				active := false
				for _, cond := range rev.Conditions {
					if cond.Type == "Ready" && (cond.State == "CONDITION_SUCCEEDED" || cond.Status == "True") {
						active = true
						break
					}
				}
				// Mark the traffic-serving revision as active regardless.
				if shortName == info.TrafficRevisionShort {
					active = true
				}
				info.RecentRevisions = append(info.RecentRevisions, RevisionSummary{
					Name:       shortName,
					CreateTime: rev.CreateTime,
					Active:     active,
				})
			}
		}
	}

	// 3. Read traffic revision's env vars.
	// Accept both fully qualified resource names and short revision names.
	var revResourceName string
	if info.TrafficRevision != "" {
		if revisionNamePattern.MatchString(info.TrafficRevision) {
			revResourceName = info.TrafficRevision
		} else if revisionShortNamePattern.MatchString(info.TrafficRevision) {
			revResourceName = fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/%s",
				url.PathEscape(projectID), url.PathEscape(region), url.PathEscape(serviceName), url.PathEscape(info.TrafficRevision))
		}
		// When format matches neither pattern, revResourceName stays empty
		// and the env var read below is skipped. This is intentional:
		// GetServiceRevisionInfo returns partial data on non-fatal errors,
		// matching the pattern at the revision list step above.
	}
	if revResourceName != "" {
		revisionURL := fmt.Sprintf("https://run.googleapis.com/v2/%s", revResourceName)
		revResp, err := c.Client.DoRequest(ctx, http.MethodGet, revisionURL, "")
		if err == nil {
			defer revResp.Body.Close()
			if revResp.StatusCode == http.StatusOK {
				var revision struct {
					Containers []struct {
						Env []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				}
				revBody, _ := io.ReadAll(io.LimitReader(revResp.Body, 10<<20))
				if err := json.Unmarshal(revBody, &revision); err == nil {
					envVars := make(map[string]string)
					if len(revision.Containers) > 0 {
						for _, e := range revision.Containers[0].Env {
							envVars[e.Name] = e.Value
						}
					}
					info.TrafficEnvVars = envVars
				}
			}
		}
	}

	// Fall back to template env vars if we couldn't read traffic revision.
	if info.TrafficEnvVars == nil && len(service.Template.Containers) > 0 {
		envVars := make(map[string]string)
		for _, e := range service.Template.Containers[0].Env {
			envVars[e.Name] = e.Value
		}
		info.TrafficEnvVars = envVars
	}

	return info, nil
}

// WaitForOperation polls a long-running operation until it completes or
// the context is canceled. Polls every 5 seconds for up to 10 minutes.
func (c *LiveGCFClient) WaitForOperation(ctx context.Context, operationName string) error {
	if !operationNamePattern.MatchString(operationName) {
		return fmt.Errorf("invalid operation name format: %q", operationName)
	}
	reqURL := fmt.Sprintf("https://cloudfunctions.googleapis.com/v2/%s", operationName)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	for {
		resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
		if err != nil {
			return fmt.Errorf("polling operation: %w", err)
		}

		var op struct {
			Done  bool `json:"done"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&op)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decoding operation status: %w", decodeErr)
		}

		if op.Done {
			if op.Error != nil {
				return fmt.Errorf("operation failed: %s", op.Error.Message)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// waitForIAMOperation parses the LRO response from IAM API calls (WIF
// pool/provider creation) and polls until done. If the operation is already
// complete in the initial response, returns immediately.
func (c *LiveGCFClient) waitForIAMOperation(ctx context.Context, body io.Reader) error {
	var op struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&op); err != nil {
		return fmt.Errorf("decoding operation response: %w", err)
	}
	if op.Done {
		if op.Error != nil {
			return fmt.Errorf("operation failed: %s", op.Error.Message)
		}
		return nil
	}
	if op.Name == "" {
		return nil
	}
	if !operationNamePattern.MatchString(op.Name) {
		return fmt.Errorf("invalid IAM operation name format: %q", op.Name)
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	reqURL := fmt.Sprintf("https://iam.googleapis.com/v1/%s", op.Name)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
		if err != nil {
			return fmt.Errorf("polling IAM operation: %w", err)
		}

		var pollOp struct {
			Done  bool `json:"done"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&pollOp)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decoding IAM operation status: %w", decodeErr)
		}

		if pollOp.Done {
			if pollOp.Error != nil {
				return fmt.Errorf("operation failed: %s", pollOp.Error.Message)
			}
			return nil
		}
	}
}

// GetProjectNumber looks up the project number for a project ID.
func (c *LiveGCFClient) GetProjectNumber(ctx context.Context, projectID string) (string, error) {
	reqURL := fmt.Sprintf("https://cloudresourcemanager.googleapis.com/v1/projects/%s",
		url.PathEscape(projectID))

	resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
	if err != nil {
		return "", fmt.Errorf("looking up project number: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status %d looking up project: %s", resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	var result struct {
		ProjectNumber string `json:"projectNumber"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding project info: %w", err)
	}

	if result.ProjectNumber == "" {
		return "", fmt.Errorf("empty project number for %s", projectID)
	}

	return result.ProjectNumber, nil
}

// iamAudience returns the IAM-format audience URI for a WIF provider.
// google-github-actions/auth@v3 uses this format for STS token exchange.
func iamAudience(projectNumber, poolID, providerID string) string {
	return fmt.Sprintf("https://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, poolID, providerID)
}

// encodeBase64 encodes data as standard base64.
func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
