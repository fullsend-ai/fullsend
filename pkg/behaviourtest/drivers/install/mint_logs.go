// mint_logs.go implements mint log collection for Cloudflare Worker
// preview mints. The collector queries the Cloudflare Workers
// Observability Telemetry Query API (POST .../telemetry/query, view
// "events") for ordinary Worker log/event records (e.g. "cf-worker-log",
// "cf-worker-event") emitted by a Worker script within a time window,
// and writes the raw event records to the behaviour test artifact
// directory. These are plain log/event records, not distributed traces.
//
// Reference: https://developers.cloudflare.com/api/resources/workers/subresources/observability/subresources/telemetry/methods/query/
//
// Collection is best-effort: missing credentials, an unexpected API
// response shape, or API errors are logged but never fail the suite.
// Workers Observability must be enabled on the Cloudflare account for
// events to be available.
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
	"strings"
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

	// cfTelemetryQueryPath is the Cloudflare Workers Observability
	// Telemetry Query API endpoint path template. %s is the account
	// ID. This is the documented query endpoint — NOT
	// "/telemetry/events", which does not exist and returns 404.
	cfTelemetryQueryPath = "/accounts/%s/workers/observability/telemetry/query"

	// cfTelemetryDataset is the dataset queried for Worker log/event
	// records.
	cfTelemetryDataset = "cloudflare-workers"

	// cfTelemetryQueryID is an ad-hoc identifier for the inline query.
	// The API only uses a saved query's ID to look up its parameters
	// when parameters are omitted; since we always provide parameters
	// inline, any identifier is acceptable here.
	cfTelemetryQueryID = "fullsend-mint-log-collection"
)

// cfWorkerLogCollector queries the Cloudflare Workers Observability
// Telemetry Query API for log/event records from a Worker script.
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

// telemetryQueryRequest is the JSON body for the Workers Observability
// Telemetry Query API, scoped to the "events" view.
//
// See: https://developers.cloudflare.com/api/resources/workers/subresources/observability/subresources/telemetry/methods/query/
type telemetryQueryRequest struct {
	QueryID    string                   `json:"queryId"`
	View       string                   `json:"view"`
	Timeframe  telemetryTimeframe       `json:"timeframe"`
	Limit      int                      `json:"limit,omitempty"`
	Parameters telemetryQueryParameters `json:"parameters"`
}

// telemetryTimeframe bounds the query to a time window, expressed as
// Unix timestamps in milliseconds (per the documented API).
type telemetryTimeframe struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// telemetryQueryParameters defines what data the query retrieves.
type telemetryQueryParameters struct {
	Datasets []string          `json:"datasets,omitempty"`
	Filters  []telemetryFilter `json:"filters,omitempty"`
}

// telemetryFilter is a single filter condition, e.g. matching events
// for a specific Worker script via the "$metadata.service" key.
type telemetryFilter struct {
	Key       string `json:"key"`
	Operation string `json:"operation"`
	Type      string `json:"type"`
	Value     string `json:"value"`
}

// cfAPIQueryResponse is the standard Cloudflare API envelope wrapping
// a Workers Observability telemetry query result.
type cfAPIQueryResponse struct {
	Success bool                 `json:"success"`
	Errors  []cfAPIMessage       `json:"errors"`
	Result  telemetryQueryResult `json:"result"`
}

// cfAPIMessage is a single entry in the Cloudflare API envelope's
// "errors" or "messages" array.
type cfAPIMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// telemetryQueryResult is the subset of the Telemetry Query API
// response relevant to the "events" view. Other views (calculations,
// invocations, traces, agents) are not used by this collector.
type telemetryQueryResult struct {
	Events *telemetryEventsResult `json:"events"`
}

// telemetryEventsResult holds the individual event records matched by
// an "events"-view query. Each entry is an ordinary Worker log or
// event record (e.g. type "cf-worker-log" or "cf-worker-event") — not
// a distributed trace.
type telemetryEventsResult struct {
	Count  int               `json:"count"`
	Events []json.RawMessage `json:"events"`
}

// Collect queries log/event records for the given script name since
// the given time and writes them to a JSON file in artifactDir.
func (c *cfWorkerLogCollector) Collect(ctx context.Context, scriptName string, since time.Time, artifactDir string) error {
	fetchCtx, cancel := context.WithTimeout(ctx, mintLogFetchTimeout)
	defer cancel()

	events, count, err := c.fetchLogEvents(fetchCtx, scriptName, since)
	if err != nil {
		return fmt.Errorf("fetching mint log events for %s: %w", scriptName, err)
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

	c.logf("[mint-logs] collected %d event record(s) (%d bytes) to %s", count, len(events), logPath)
	if count == 0 {
		c.logf("[mint-logs] warning: 0 event records returned for %s; check the script name, timeframe, and that Workers Observability is enabled on the account", scriptName)
	}
	return nil
}

// fetchLogEvents queries the Cloudflare Workers Observability
// Telemetry Query API (view "events") for log/event records from a
// specific Worker script, and returns the raw event records as a JSON
// array along with the number of records returned.
func (c *cfWorkerLogCollector) fetchLogEvents(ctx context.Context, scriptName string, since time.Time) ([]byte, int, error) {
	reqURL := fmt.Sprintf(
		"%s"+cfTelemetryQueryPath,
		c.cfBaseURL(),
		url.PathEscape(c.accountID),
	)

	body := telemetryQueryRequest{
		QueryID: cfTelemetryQueryID,
		View:    "events",
		Timeframe: telemetryTimeframe{
			From: since.UTC().UnixMilli(),
			To:   time.Now().UTC().UnixMilli(),
		},
		Limit: mintLogEventLimit,
		Parameters: telemetryQueryParameters{
			Datasets: []string{cfTelemetryDataset},
			Filters: []telemetryFilter{
				{
					Key:       "$metadata.service",
					Operation: "eq",
					Type:      "string",
					Value:     scriptName,
				},
			},
		},
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calling Cloudflare API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, mintLogMaxResponse))
	if err != nil {
		return nil, 0, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...[truncated]"
		}
		return nil, 0, fmt.Errorf("Cloudflare API returned %d: %s", resp.StatusCode, bodyStr)
	}

	var envelope cfAPIQueryResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...[truncated]"
		}
		return nil, 0, fmt.Errorf("decoding Cloudflare telemetry response: %w (body: %s)", err, bodyStr)
	}

	if !envelope.Success {
		return nil, 0, fmt.Errorf("Cloudflare telemetry query reported failure: %s", formatCFMessages(envelope.Errors))
	}

	if envelope.Result.Events == nil {
		return nil, 0, fmt.Errorf("Cloudflare telemetry response did not include an %q view result; the response shape may not match the documented API", "events")
	}

	eventsJSON, err := json.Marshal(envelope.Result.Events.Events)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling mint event records: %w", err)
	}

	return eventsJSON, len(envelope.Result.Events.Events), nil
}

// formatCFMessages renders a Cloudflare API envelope's error/message
// list into a single human-readable string for logging.
func formatCFMessages(msgs []cfAPIMessage) string {
	if len(msgs) == 0 {
		return "no error details provided"
	}
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = fmt.Sprintf("[%d] %s", m.Code, m.Message)
	}
	return strings.Join(parts, "; ")
}
