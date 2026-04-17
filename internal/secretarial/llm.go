package secretarial

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// LLMClient calls Claude on Vertex AI for intelligent topic extraction.
//
// Authentication reuses the same service-account JSON as the Drive client,
// obtaining an OAuth2 token with the cloud-platform scope. No additional
// SDK dependency is required — it's a single REST call to the rawPredict
// endpoint.
type LLMClient struct {
	httpClient *http.Client
	projectID  string
	region     string
	model      string
}

// NewLLMClient creates a Claude-on-Vertex client from a service-account key.
func NewLLMClient(ctx context.Context, credentialsJSON []byte, projectID, region, model string) (*LLMClient, error) {
	creds, err := google.CredentialsFromJSON(ctx, credentialsJSON,
		"https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("parsing credentials for Vertex AI: %w", err)
	}

	return &LLMClient{
		httpClient: oauth2.NewClient(ctx, creds.TokenSource),
		projectID:  projectID,
		region:     region,
		model:      model,
	}, nil
}

type vertexRequest struct {
	AnthropicVersion string          `json:"anthropic_version"`
	MaxTokens        int             `json:"max_tokens"`
	System           string          `json:"system"`
	Messages         []vertexMessage `json:"messages"`
}

type vertexMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type vertexResponse struct {
	Content    []vertexContentBlock `json:"content"`
	StopReason string               `json:"stop_reason"`
}

type vertexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ExtractTopics sends sanitized meeting text and the open-issue backlog to
// Claude and returns structured topics with issue matching and
// public-appropriateness filtering applied by the model.
//
// cutoffDate tells the LLM to only extract from the most recent meeting
// section (critical for rolling docs that accumulate multiple meetings).
func (c *LLMClient) ExtractTopics(ctx context.Context, docText, notesURL, issueContext, cutoffDate string) ([]Topic, error) {
	userPrompt := buildUserPrompt(docText, notesURL, issueContext, cutoffDate)

	reqBody := vertexRequest{
		AnthropicVersion: "vertex-2023-10-16",
		MaxTokens:        16384,
		System:           SystemPrompt,
		Messages: []vertexMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		c.region, c.projectID, c.region, c.model,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Info("calling Vertex AI", "model", c.model, "region", c.region)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Vertex AI: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Vertex AI returned %d: %s", resp.StatusCode, truncateStr(string(respBody), 500))
	}

	var vResp vertexResponse
	if err := json.Unmarshal(respBody, &vResp); err != nil {
		return nil, fmt.Errorf("parsing Vertex AI response: %w", err)
	}

	if len(vResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from Vertex AI")
	}

	if vResp.StopReason == "max_tokens" {
		slog.Warn("LLM response was truncated (hit max_tokens limit)")
	}

	var jsonText string
	for _, block := range vResp.Content {
		if block.Type == "text" {
			jsonText = block.Text
			break
		}
	}
	if jsonText == "" {
		return nil, fmt.Errorf("no text block in Vertex AI response")
	}

	topics, err := parseTopicsJSON(jsonText)
	if err != nil {
		slog.Warn("LLM returned non-JSON, retrying with reinforced prompt", "err", err)
		topics, err = c.retryExtract(ctx, reqBody, jsonText)
		if err != nil {
			return nil, fmt.Errorf("parsing extracted topics after retry: %w (raw: %s)", err, truncateStr(jsonText, 300))
		}
	}

	return topics, nil
}

// retryExtract makes a second LLM call using the failed response as context
// and a firm instruction to return only JSON.
func (c *LLMClient) retryExtract(ctx context.Context, origReq vertexRequest, badResponse string) ([]Topic, error) {
	origReq.Messages = append(origReq.Messages,
		vertexMessage{Role: "assistant", Content: badResponse},
		vertexMessage{Role: "user", Content: "Your response was not valid JSON. Return ONLY the raw JSON array as described in the system prompt. No explanation, no reasoning, no markdown fences — just the JSON array."},
	)

	bodyJSON, err := json.Marshal(origReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling retry request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		c.region, c.projectID, c.region, c.model,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("creating retry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Info("retrying Vertex AI call", "model", c.model)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("retry call to Vertex AI: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading retry response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Vertex AI retry returned %d: %s", resp.StatusCode, truncateStr(string(respBody), 500))
	}

	var vResp vertexResponse
	if err := json.Unmarshal(respBody, &vResp); err != nil {
		return nil, fmt.Errorf("parsing retry response: %w", err)
	}

	if len(vResp.Content) == 0 {
		return nil, fmt.Errorf("empty retry response from Vertex AI")
	}

	var jsonText string
	for _, block := range vResp.Content {
		if block.Type == "text" {
			jsonText = block.Text
			break
		}
	}

	return parseTopicsJSON(jsonText)
}

func buildUserPrompt(docText, notesURL, issueContext, cutoffDate string) string {
	return fmt.Sprintf(`Here are the currently open GitHub issues in the repository:

%s

Below are the meeting notes. This may be a ROLLING document containing notes
from multiple meetings. ONLY extract topics from the meeting on or after: %s
Ignore all content from earlier meetings.

Extract actionable topics and produce the JSON array described in your system instructions.

Link back to the meeting notes using this URL: %s

---
%s
---`, issueContext, cutoffDate, notesURL, docText)
}

// parseTopicsJSON strips optional markdown fences and parses the JSON array.
func parseTopicsJSON(raw string) ([]Topic, error) {
	s := strings.TrimSpace(raw)

	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s[3:], "\n"); idx != -1 {
			s = s[3+idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	var topics []Topic
	if err := json.Unmarshal([]byte(s), &topics); err != nil {
		return nil, err
	}
	return topics, nil
}

// ExpandNewIssueBody takes a brief topic summary and the full meeting notes,
// and returns a rich markdown issue body. The LLM outputs raw markdown (no
// JSON), avoiding the escaping problems that plague JSON-embedded markdown.
func (c *LLMClient) ExpandNewIssueBody(ctx context.Context, topicTitle, briefSummary, docText, notesURL, issueContext string) (string, error) {
	userPrompt := fmt.Sprintf(`Topic to expand into a full GitHub issue body:
Title: %s
Brief summary: %s

Open issues in the repository (reference relevant ones in your Related section):
%s

Meeting notes URL (use in your Related section): %s

Full meeting notes for context:
---
%s
---`, topicTitle, briefSummary, issueContext, notesURL, docText)

	reqBody := vertexRequest{
		AnthropicVersion: "vertex-2023-10-16",
		MaxTokens:        4096,
		System:           ExpandIssueBodyPrompt,
		Messages: []vertexMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling expand request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		c.region, c.projectID, c.region, c.model,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", fmt.Errorf("creating expand request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Info("expanding new issue body", "topic", topicTitle)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("expand call to Vertex AI: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading expand response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Vertex AI expand returned %d: %s", resp.StatusCode, truncateStr(string(respBody), 500))
	}

	var vResp vertexResponse
	if err := json.Unmarshal(respBody, &vResp); err != nil {
		return "", fmt.Errorf("parsing expand response: %w", err)
	}

	for _, block := range vResp.Content {
		if block.Type == "text" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("no text block in expand response")
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
