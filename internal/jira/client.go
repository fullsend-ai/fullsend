package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client provides access to Jira Cloud and its Automation REST API.
type Client struct {
	httpClient *http.Client
	email      string
	apiToken   string
}

// NewClient creates a Jira API client authenticated with Basic auth.
func NewClient(email, apiToken string) *Client {
	return &Client{
		httpClient: http.DefaultClient,
		email:      email,
		apiToken:   apiToken,
	}
}

func (c *Client) basicAuth() string {
	return base64.StdEncoding.EncodeToString([]byte(c.email + ":" + c.apiToken))
}

// GetCloudID fetches the Jira Cloud ID from the tenant info endpoint.
// The host parameter is the Jira hostname (e.g., "myorg.atlassian.net").
func (c *Client) GetCloudID(ctx context.Context, host string) (string, error) {
	url := fmt.Sprintf("https://%s/_edge/tenant_info", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating tenant info request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching tenant info from %s: %w", host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tenant info returned HTTP %d for %s", resp.StatusCode, host)
	}

	var info TenantInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decoding tenant info: %w", err)
	}
	if info.CloudID == "" {
		return "", fmt.Errorf("tenant info response missing cloudId for %s", host)
	}
	return info.CloudID, nil
}

// ProjectInfo holds the key fields returned by the Jira project API.
type ProjectInfo struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// ValidateProject checks that a Jira project exists and is accessible.
// Returns the project's numeric ID (needed for Automation API URLs).
func (c *Client) ValidateProject(ctx context.Context, host, projectKey string) (*ProjectInfo, error) {
	url := fmt.Sprintf("https://%s/rest/api/3/project/%s", host, projectKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating project validation request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("validating project %s: %w", projectKey, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var info ProjectInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return nil, fmt.Errorf("decoding project response: %w", err)
		}
		if info.ID == "" {
			return nil, fmt.Errorf("project response missing id for %s", projectKey)
		}
		return &info, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("authentication failed for %s — check JIRA_EMAIL and JIRA_API_TOKEN", host)
	case http.StatusForbidden:
		return nil, fmt.Errorf("no permission to access project %s on %s", projectKey, host)
	case http.StatusNotFound:
		return nil, fmt.Errorf("project %s not found on %s", projectKey, host)
	default:
		return nil, fmt.Errorf("project validation returned HTTP %d for %s", resp.StatusCode, projectKey)
	}
}

// AccountInfo holds the current user's Jira account details.
type AccountInfo struct {
	AccountID string `json:"accountId"`
}

// GetCurrentUser returns the account ID of the authenticated user.
func (c *Client) GetCurrentUser(ctx context.Context, host string) (string, error) {
	url := fmt.Sprintf("https://%s/rest/api/3/myself", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating myself request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching current user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("current user request returned HTTP %d", resp.StatusCode)
	}

	var info AccountInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decoding current user: %w", err)
	}
	if info.AccountID == "" {
		return "", fmt.Errorf("current user response missing accountId")
	}
	return info.AccountID, nil
}

// automationBaseURL returns the Jira Automation public REST API base URL.
func automationBaseURL(cloudID string) string {
	return fmt.Sprintf("https://api.atlassian.com/automation/public/jira/%s/rest/v1", cloudID)
}

// CreateAutomationRule creates a Jira Automation rule and returns its UUID.
func (c *Client) CreateAutomationRule(ctx context.Context, cloudID string, rule CreateRuleRequest) (string, error) {
	body, err := json.Marshal(rule)
	if err != nil {
		return "", fmt.Errorf("marshaling automation rule: %w", err)
	}

	url := automationBaseURL(cloudID) + "/rule"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating automation rule request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating automation rule: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("automation rule creation returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result CreateRuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding automation rule response: %w", err)
	}
	return result.RuleUUID, nil
}

// ListAutomationRules lists automation rules for the given cloud instance.
// Used for idempotency checks — skip creation if a rule with the same
// name already exists.
func (c *Client) ListAutomationRules(ctx context.Context, cloudID string) ([]RuleSummary, error) {
	url := automationBaseURL(cloudID) + "/rule/summary"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating list rules request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing automation rules: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list automation rules returned HTTP %d", resp.StatusCode)
	}

	var rules []RuleSummary
	if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil {
		return nil, fmt.Errorf("decoding automation rules list: %w", err)
	}
	return rules, nil
}

// RuleExistsByName checks whether an automation rule with the given
// name already exists.
func (c *Client) RuleExistsByName(ctx context.Context, cloudID, name string) (bool, error) {
	rules, err := c.ListAutomationRules(ctx, cloudID)
	if err != nil {
		return false, err
	}
	for _, r := range rules {
		if r.Name == name {
			return true, nil
		}
	}
	return false, nil
}
