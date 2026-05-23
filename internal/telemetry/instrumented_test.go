package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestInstrumentedPrinter_StepStartDone(t *testing.T) {
	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)
	ip.AttachRecorder(rec, ctx)

	ip.StepStart("my-step", "Doing something")
	ip.StepDone("my-step", "Done doing something")
	require.NoError(t, rec.Close())

	output := buf.String()
	assert.Contains(t, output, "Doing something")
	assert.Contains(t, output, "Done doing something")

	events := readEvents(t, filepath.Join(dir, "run-events.jsonl"))
	stepStarts := filterEvents(events, EventStepStart, "my-step")
	stepDones := filterEvents(events, EventStepDone, "my-step")
	assert.Len(t, stepStarts, 1)
	assert.Len(t, stepDones, 1)
}

func TestInstrumentedPrinter_StepFail(t *testing.T) {
	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)
	ip.AttachRecorder(rec, ctx)

	ip.StepStart("failing-step", "About to fail")
	ip.StepFail("failing-step", "It failed", assert.AnError)
	require.NoError(t, rec.Close())

	events := readEvents(t, filepath.Join(dir, "run-events.jsonl"))
	fails := filterEvents(events, EventStepFail, "failing-step")
	assert.Len(t, fails, 1)
	assert.Equal(t, StatusError, fails[0].Status)
	assert.Contains(t, fails[0].Error, "assert.AnError")
}

func TestInstrumentedPrinter_StepWarn(t *testing.T) {
	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)
	ip.AttachRecorder(rec, ctx)

	ip.StepStart("warn-step", "Trying something risky")
	ip.StepWarn("warn-step", "It kinda worked")
	require.NoError(t, rec.Close())

	events := readEvents(t, filepath.Join(dir, "run-events.jsonl"))
	warns := filterEvents(events, EventStepWarn, "warn-step")
	assert.Len(t, warns, 1)
	assert.Equal(t, StatusWarning, warns[0].Status)
}

func TestInstrumentedPrinter_BufferReplay(t *testing.T) {
	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	// Steps before recorder is attached get buffered.
	ip.StepStart("early-step", "Loading config")
	ip.StepDone("early-step", "Config loaded")
	ip.StepStart("fail-early", "Validating")
	ip.StepFail("fail-early", "Validation failed", assert.AnError)

	// Now attach the recorder — buffered steps should replay.
	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)
	ip.AttachRecorder(rec, ctx)

	// Additional step after attach.
	ip.StepStart("late-step", "Running agent")
	ip.StepDone("late-step", "Agent done")
	require.NoError(t, rec.Close())

	events := readEvents(t, filepath.Join(dir, "run-events.jsonl"))

	// All three steps should be in the JSONL: early-step, fail-early, late-step.
	earlyStarts := filterEvents(events, EventStepStart, "early-step")
	earlyDones := filterEvents(events, EventStepDone, "early-step")
	failStarts := filterEvents(events, EventStepStart, "fail-early")
	failFails := filterEvents(events, EventStepFail, "fail-early")
	lateStarts := filterEvents(events, EventStepStart, "late-step")
	lateDones := filterEvents(events, EventStepDone, "late-step")

	assert.Len(t, earlyStarts, 1, "buffered step.start should replay")
	assert.Len(t, earlyDones, 1, "buffered step.done should replay")
	assert.Len(t, failStarts, 1, "buffered fail step.start should replay")
	assert.Len(t, failFails, 1, "buffered step.fail should replay")
	assert.Len(t, lateStarts, 1, "post-attach step.start should record")
	assert.Len(t, lateDones, 1, "post-attach step.done should record")
}

func TestInstrumentedPrinter_NoRecorder(t *testing.T) {
	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	// Without attaching a recorder, steps should still print (no panic).
	ip.StepStart("orphan-step", "Doing work")
	ip.StepDone("orphan-step", "Work done")
	ip.StepFail("other-step", "Oops", assert.AnError)
	ip.StepWarn("yet-another", "Hmm")
	ip.Warn("Standalone warning")

	output := buf.String()
	assert.Contains(t, output, "Doing work")
	assert.Contains(t, output, "Work done")
	assert.Contains(t, output, "Oops")
	assert.Contains(t, output, "Standalone warning")
}

func TestInstrumentedPrinter_WarnDoesNotCreateSpan(t *testing.T) {
	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, nil, "test-run", nil)
	require.NoError(t, err)
	ip.AttachRecorder(rec, ctx)

	ip.Warn("standalone warning that is not a step")
	require.NoError(t, rec.Close())

	events := readEvents(t, filepath.Join(dir, "run-events.jsonl"))
	// Only run.start and run.done — no step events from Warn().
	for _, e := range events {
		if e.Event == EventStepStart || e.Event == EventStepDone || e.Event == EventStepFail || e.Event == EventStepWarn {
			t.Errorf("Warn() should not produce step events, got %s for step %q", e.Event, e.Step)
		}
	}
}

func TestInstrumentedPrinter_AttrsFlowToOTELSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	var buf bytes.Buffer
	ip := NewInstrumentedPrinter(&buf)

	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, tracer, "test-run",
		[]Attr{StringAttr("gen_ai.agent.name", "triage")},
	)
	require.NoError(t, err)
	ip.AttachRecorder(rec, ctx)

	ip.StepStart("sandbox-create", "Creating sandbox",
		StringAttr("sandbox.image", "ubuntu:22.04"),
	)
	ip.StepDone("sandbox-create", "Sandbox ready",
		StringAttr("sandbox.name", "fs-abc123"),
		StringAttr("exit_code", "0"),
	)
	require.NoError(t, rec.Close())
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()

	// Find the sandbox-create span.
	var found *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "sandbox-create" {
			found = &spans[i]
			break
		}
	}
	require.NotNil(t, found, "sandbox-create span should exist")

	attrMap := make(map[string]string)
	for _, a := range found.Attributes {
		if a.Value.Type() == attribute.STRING {
			attrMap[string(a.Key)] = a.Value.AsString()
		}
	}

	assert.Equal(t, "ubuntu:22.04", attrMap["sandbox.image"], "StepStart attr should appear on span")
	assert.Equal(t, "fs-abc123", attrMap["sandbox.name"], "StepDone attr should appear on span")
	assert.Equal(t, "0", attrMap["exit_code"], "StepDone attr should appear on span")
}

func TestRecorder_RootSpanKind(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	dir := t.TempDir()
	rec, _, err := NewRecorder(context.Background(), dir, tracer, "consumer-run",
		[]Attr{StringAttr("test", "1")},
		SpanKindConsumer(),
	)
	require.NoError(t, err)
	require.NoError(t, rec.Close())
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	var root *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "consumer-run" {
			root = &spans[i]
			break
		}
	}
	require.NotNil(t, root, "root span should exist")
	assert.Equal(t, trace.SpanKindConsumer, root.SpanKind, "root span should have SpanKindConsumer")
}

func TestRecorder_RootSpanKindDefault(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	dir := t.TempDir()
	rec, _, err := NewRecorder(context.Background(), dir, tracer, "internal-run", nil)
	require.NoError(t, err)
	require.NoError(t, rec.Close())
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	var root *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "internal-run" {
			root = &spans[i]
			break
		}
	}
	require.NotNil(t, root, "root span should exist")
	assert.Equal(t, trace.SpanKindInternal, root.SpanKind, "default root span should be SpanKindInternal")
}

func TestTimedMsg(t *testing.T) {
	msg := TimedMsg("Operation complete", 3500000000) // 3.5 seconds as Duration
	assert.Equal(t, "Operation complete (3.5s)", msg)
}

// --- helpers ---

func readEvents(t *testing.T, path string) []RunEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var events []RunEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e RunEvent
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		events = append(events, e)
	}
	return events
}

func filterEvents(events []RunEvent, eventType, step string) []RunEvent {
	var out []RunEvent
	for _, e := range events {
		if e.Event == eventType && e.Step == step {
			out = append(out, e)
		}
	}
	return out
}
