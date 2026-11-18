package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otlpSpan is a span in an OTLP/JSON export request or trace file.
type otlpSpan struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Name    string `json:"name"`
}

// otlpTraceData is one line of a trace file.
type otlpTraceData struct {
	ResourceSpans []struct {
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// otlpRequest builds an OTLP/JSON export request with one resource and
// one scope holding the given spans in order.
func otlpRequest(service string, spans []otlpSpan) string {
	parts := make([]string, 0, len(spans))
	for _, s := range spans {
		parts = append(parts, fmt.Sprintf(
			`{"traceId":%q,"spanId":%q,"name":%q,"kind":1,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000"}`,
			s.TraceID, s.SpanID, s.Name))
	}
	return fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":%q}}]},`+
		`"scopeSpans":[{"scope":{"name":"bt-test"},"spans":[%s]}]}]}`, service, strings.Join(parts, ","))
}

// TestCollectorConfig_PerTraceFilesWithRealCollector runs the real
// Collector image with collectorConfig and sends it interleaved spans
// of two trace IDs across several export requests. It checks that each
// trace ID ends up in its own file, that no span is lost or lands in
// another trace's file, and that the receiver still enforces the token.
// It needs Podman and the Collector image, so it only runs when
// BT_OTELCOL_INTEGRATION=1.
func TestCollectorConfig_PerTraceFilesWithRealCollector(t *testing.T) {
	if os.Getenv("BT_OTELCOL_INTEGRATION") != "1" {
		t.Skip("set BT_OTELCOL_INTEGRATION=1 to run the Collector against the real image")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	token, err := randomHex(16)
	require.NoError(t, err)
	suffix, err := randomHex(4)
	require.NoError(t, err)
	name := "bt-otelcol-it-" + suffix
	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(collectorConfig), 0o644))
	tracesDir := filepath.Join(workDir, "traces")
	require.NoError(t, os.Mkdir(tracesDir, 0o700))
	port, err := freeLocalPort()
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = execCommandRunner(context.Background(), nil, "podman", "rm", "-f", name)
	})
	out, err := execCommandRunner(ctx, []string{collectorTokenEnv + "=" + token}, "podman", "run", "-d",
		"--name", name, "--user", "0:0", "-e", collectorTokenEnv,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, collectorContainerPort),
		"-v", configPath+":/etc/bt-otelcol/config.yaml:ro,Z",
		"-v", tracesDir+":/traces:Z",
		otelcolImage, "--config=/etc/bt-otelcol/config.yaml")
	require.NoError(t, err, string(out))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port)
	post := func(token, body string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	require.Eventually(t, func() bool { return post(token, "{}") == http.StatusOK },
		2*time.Minute, time.Second, "collector receiver did not come up")
	assert.Equal(t, http.StatusUnauthorized, post("", "{}"))

	span := func(trace string, n int) otlpSpan {
		return otlpSpan{TraceID: trace, SpanID: fmt.Sprintf("%016x", n), Name: fmt.Sprintf("%s-span-%d", trace[:4], n)}
	}
	// Spans of both traces interleaved within and across requests.
	requests := [][]otlpSpan{
		{span(testTraceA, 1), span(testTraceB, 2), span(testTraceA, 3)},
		{span(testTraceB, 4), span(testTraceA, 5)},
		{span(testTraceB, 6), span(testTraceB, 7), span(testTraceA, 8), span(testTraceB, 9)},
		{span(testTraceA, 10)},
	}
	want := map[string][]string{}
	for i, spans := range requests {
		require.Equal(t, http.StatusOK, post(token, otlpRequest(fmt.Sprintf("svc-%d", i), spans)))
		for _, s := range spans {
			want[s.TraceID] = append(want[s.TraceID], s.Name)
		}
	}

	// Stopping flushes and closes the files.
	out, err = execCommandRunner(ctx, nil, "podman", "stop", "--time", collectorStopSeconds, name)
	require.NoError(t, err, string(out))

	entries, err := os.ReadDir(tracesDir)
	require.NoError(t, err)
	var files []string
	for _, e := range entries {
		files = append(files, e.Name())
	}
	require.ElementsMatch(t, []string{"trace-" + testTraceA + ".jsonl", "trace-" + testTraceB + ".jsonl"}, files)

	for trace, wantNames := range want {
		data, err := os.ReadFile(filepath.Join(tracesDir, "trace-"+trace+".jsonl"))
		require.NoError(t, err)
		var gotNames []string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var td otlpTraceData
			require.NoError(t, json.Unmarshal([]byte(line), &td))
			for _, rs := range td.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, s := range ss.Spans {
						assert.Equal(t, trace, s.TraceID, "span in the wrong trace's file")
						gotNames = append(gotNames, s.Name)
					}
				}
			}
		}
		assert.ElementsMatch(t, wantNames, gotNames, "spans of trace %s", trace)
	}
}
