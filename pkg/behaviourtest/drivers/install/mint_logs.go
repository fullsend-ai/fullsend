// mint_logs.go implements mint log collection for Cloudflare Worker
// preview mints. The collector queries the Cloudflare Workers
// Observability telemetry API for historical trace events from a
// Worker script within a time window and writes them to the behaviour
// test artifact directory.
//
// Collection is best-effort: missing credentials or API errors are
// logged but never fail the suite. Workers Observability must be
// enabled on the Cloudflare account for events to be available.
package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	// mintLogMaxResponse limits the response body read from the
	// Cloudflare API to prevent unbounded memory consumption.
	mintLogMaxResponse = 10 << 20 // 10 MB

	// mintLogFetchTimeout bounds the API call so a hanging request
	// does not block Finalize indefinitely.
	mintLogFetchTimeout = 30 * time.Second

	// mintLogEventLimit is the maximum number of events to request.
	mintLogEventLimit = 10000

	// cfTelemetryEventsPath is the Cloudflare Workers Observability
	// telemetry events endpoint path template. %s is the account ID.
	cfTelemetryEventsPath = "/accounts/%s/workers/observability/telemetry/events"
)

// cfWorkerLogCollector queries the Cloudflare Workers Observability API
// for trace events from a Worker script.
type cfWorkerLogCollector struct {
	accountID  string
	apiToken   string
	baseURL    string // overridden in tests; empty uses production URL
	httpClient *http.Client
	logf       func(string, ...any)
}

// newCFWorkerLogCollector creates a log collector from environment
// variables. Returns nil if the required credentials (CLOUDFLARE_ACCOUNT_ID,
// CLOUDFLARE_API_TOKEN) are not available — the caller should treat this
// as a graceful skip.
func newCFWorkerLogCollector(logf func(string, ...any)) *cfWorkerLogCollector {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	apiToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	if accountID == "" || apiToken == "" {
		logf("[mint-logs] Cloudflare credentials not available; skipping log collection")
		return nil
	}
	return &cfWorkerLogCollector{
		accountID:  accountID,
		apiToken:   apiToken,
		httpClient: http.DefaultClient,
		logf:       logf,
	}
}

// cfBaseURL returns the Cloudflare API base URL. When baseURL is set
// (e.g. in tests), it is used instead of the production URL.
func (c *cfWorkerLogCollector) cfBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return "https://api.cloudflare.com/client/v4"
}

// telemetryRequest is the JSON body for the Workers Observability
// telemetry events query.
type telemetryRequest struct {
	Limit     int                `json:"limit"`
	TimeRange telemetryTimeRange `json:"timeRange"`
	Filters   []telemetryFilter  `json:"filters"`
}

type telemetryTimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type telemetryFilter struct {
	Key      string `json:"key"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// Collect queries trace events for the given script name since the
// given time and writes them to a JSON file in artifactDir.
func (c *cfWorkerLogCollector) Collect(ctx context.Context, scriptName string, since time.Time, artifactDir string) error {
	fetchCtx, cancel := context.WithTimeout(ctx, mintLogFetchTimeout)
	defer cancel()

	events, err := c.fetchTraceEvents(fetchCtx, scriptName, since)
	if err != nil {
		return fmt.Errorf("fetching mint trace events for %s: %w", scriptName, err)
	}

	// Create debug directory for mint logs.
	debugDir := filepath.Join(artifactDir, "debug-mint-logs")
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return fmt.Errorf("creating mint log directory: %w", err)
	}

	logPath := filepath.Join(debugDir, "mint-events.json")
	if err := os.WriteFile(logPath, events, 0o644); err != nil {
		return fmt.Errorf("writing mint events: %w", err)
	}

	c.logf("[mint-logs] collected %d bytes of mint trace events to %s", len(events), logPath)
	return nil
}

// fetchTraceEvents queries the Cloudflare Workers Observability
// telemetry API for events from a specific Worker script.
func (c *cfWorkerLogCollector) fetchTraceEvents(ctx context.Context, scriptName string, since time.Time) ([]byte, error) {
	reqURL := fmt.Sprintf(
		"%s"+cfTelemetryEventsPath,
		c.cfBaseURL(),
		url.PathEscape(c.accountID),
	)

	body := telemetryRequest{
		Limit: mintLogEventLimit,
		TimeRange: telemetryTimeRange{
			From: since.UTC().Format(time.RFC3339),
			To:   time.Now().UTC().Format(time.RFC3339),
		},
		Filters: []telemetryFilter{
			{
				Key:      "scriptName",
				Operator: "eq",
				Value:    scriptName,
			},
		},
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Cloudflare API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, mintLogMaxResponse))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...[truncated]"
		}
		return nil, fmt.Errorf("Cloudflare API returned %d: %s", resp.StatusCode, bodyStr)
	}

	return respBody, nil
}
