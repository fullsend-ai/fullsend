package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRecorder_WritesEvents(t *testing.T) {
	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run",
		[]Attr{StringAttr("agent", "triage")})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.NotNil(t, ctx)

	rec.StepStart(ctx, "load-harness", StringAttr("path", "/foo/bar.yaml"))
	rec.StepDone("load-harness", StringAttr("duration", "1.2s"))
	rec.StepStart(ctx, "create-sandbox")
	rec.StepFail("create-sandbox", errors.New("timeout"))
	require.NoError(t, rec.Close())

	data, err := os.ReadFile(filepath.Join(dir, eventsFile))
	require.NoError(t, err)

	var events []RunEvent
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e RunEvent
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		events = append(events, e)
	}

	require.Len(t, events, 6) // run.start, step.start, step.done, step.start, step.fail, run.done
	assert.Equal(t, EventRunStart, events[0].Event)
	assert.Equal(t, EventStepStart, events[1].Event)
	assert.Equal(t, "load-harness", events[1].Step)
	assert.Equal(t, "/foo/bar.yaml", events[1].Attrs["path"])
	assert.Equal(t, EventStepDone, events[2].Event)
	assert.Equal(t, StatusOK, events[2].Status)
	assert.Equal(t, EventStepStart, events[3].Event)
	assert.Equal(t, "create-sandbox", events[3].Step)
	assert.Equal(t, EventStepFail, events[4].Event)
	assert.Equal(t, StatusError, events[4].Status)
	assert.Equal(t, "timeout", events[4].Error)
	assert.Equal(t, EventRunDone, events[5].Event)
}

func TestRecorder_WriteSummary(t *testing.T) {
	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)

	rec.StepStart(ctx, "bootstrap")
	rec.StepDone("bootstrap")

	err = rec.WriteSummary(RunSummary{
		Agent:           "triage",
		Harness:         "harness/triage.yaml",
		SecurityTraceID: "abc-123",
		ExitCode:        0,
		Iterations:      1,
	})
	require.NoError(t, err)
	require.NoError(t, rec.Close())

	data, err := os.ReadFile(filepath.Join(dir, summaryFile))
	require.NoError(t, err)

	var summary RunSummary
	require.NoError(t, json.Unmarshal(data, &summary))
	assert.Equal(t, SchemaVersion, summary.SchemaVersion)
	assert.Equal(t, "triage", summary.Agent)
	assert.Equal(t, "abc-123", summary.SecurityTraceID)
	assert.Len(t, summary.Steps, 1)
	assert.Equal(t, "bootstrap", summary.Steps[0].Name)
	assert.Equal(t, StatusOK, summary.Steps[0].Status)
}

func TestRecorder_StepWarn(t *testing.T) {
	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)

	rec.StepStart(ctx, "scan")
	rec.StepWarn("scan", "3 findings")
	require.NoError(t, rec.Close())

	steps := rec.Steps()
	require.Len(t, steps, 1)
	assert.Equal(t, StatusWarning, steps[0].Status)
}

func TestRecorder_DoubleClose(t *testing.T) {
	dir := t.TempDir()
	rec, _, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)

	require.NoError(t, rec.Close())
	require.NoError(t, rec.Close())
}

func TestRecorder_SummaryIncludesTraceparent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	dir := t.TempDir()
	rec, _, err := NewRecorder(context.Background(), dir, tracer, "test-run", nil)
	require.NoError(t, err)

	err = rec.WriteSummary(RunSummary{
		Agent:    "triage",
		Harness:  "harness/triage.yaml",
		ExitCode: 0,
	})
	require.NoError(t, err)
	require.NoError(t, rec.Close())

	data, err := os.ReadFile(filepath.Join(dir, summaryFile))
	require.NoError(t, err)

	var summary RunSummary
	require.NoError(t, json.Unmarshal(data, &summary))

	assert.NotEmpty(t, summary.TraceID, "TraceID should be populated with a real tracer")
	assert.NotEmpty(t, summary.Traceparent, "Traceparent should be populated with a real tracer")
	assert.True(t, IsValidTraceparent(summary.Traceparent),
		"Traceparent %q should be valid W3C format", summary.Traceparent)
}

