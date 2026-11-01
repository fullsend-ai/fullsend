package behaviourfail

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkWrapsError(t *testing.T) {
	t.Parallel()

	cause := errors.New("repo acme/widget not visible after 30 attempts")
	marked := Mark(cause, "setup: repo never visible")
	require.Error(t, marked)

	var infra *InfraError
	require.True(t, errors.As(marked, &infra))
	assert.Equal(t, "setup: repo never visible", infra.Reason)
	assert.ErrorIs(t, marked, cause)
	assert.Equal(t, cause, errors.Unwrap(marked))

	msg := marked.Error()
	assert.Contains(t, msg, "infrastructure failure")
	assert.Contains(t, msg, "setup: repo never visible")
	assert.Contains(t, msg, "repo acme/widget not visible after 30 attempts")
}

func TestIsThroughWrapChains(t *testing.T) {
	t.Parallel()

	marked := Mark(errors.New("iam 429"), "setup: gcp wif quota")

	assert.True(t, Is(fmt.Errorf("provision: %w", marked)))
	double := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", marked))
	assert.True(t, Is(double))

	var infra *InfraError
	require.True(t, errors.As(double, &infra))
	assert.Equal(t, "setup: gcp wif quota", infra.Reason)
}

func TestIsNegative(t *testing.T) {
	t.Parallel()

	assert.False(t, Is(nil))
	assert.False(t, Is(errors.New("issue 42 missing label")))
	assert.False(t, Is(fmt.Errorf("wrapped: %w", errors.New("boom"))))
	assert.NoError(t, Mark(nil, "setup: nothing to mark"))
}

func TestInfraErrorWithoutCause(t *testing.T) {
	t.Parallel()

	e := &InfraError{Reason: "setup: gcp wif quota"}
	assert.Equal(t, "infrastructure failure (setup: gcp wif quota)", e.Error())
	assert.NoError(t, e.Unwrap())
}

func TestRecorderNilReceiverSafe(t *testing.T) {
	t.Parallel()

	var r *Recorder
	assert.NotPanics(t, func() {
		r.RecordFailure("scenario", "adding label", Scenario, "boom")
	})
	assert.Nil(t, r.Snapshot())

	infrastructure, scenario := r.Counts()
	assert.Zero(t, infrastructure)
	assert.Zero(t, scenario)

	report := r.Report(Scenario)
	assert.Equal(t, Scenario, report.Class)
	assert.Zero(t, report.InfrastructureFailures)
	assert.Zero(t, report.ScenarioFailures)
	assert.Empty(t, report.Failures)
	assert.NotNil(t, report.Failures)
}

func TestRecordFailureSnapshotAndCounts(t *testing.T) {
	t.Parallel()

	r := &Recorder{}
	r.RecordFailure("scenario", "adding label", Scenario, "issue 42 missing label")
	r.RecordFailure("bootstrap", "", Infrastructure, "mint download timeout")
	r.RecordFailure("scenario", "dispatch", Infrastructure, "workflow not visible")

	infrastructure, scenario := r.Counts()
	assert.Equal(t, 2, infrastructure)
	assert.Equal(t, 1, scenario)

	snap := r.Snapshot()
	require.Len(t, snap, 3)
	assert.Equal(t, FailureRecord{
		Source:   "scenario",
		Scenario: "adding label",
		Class:    Scenario,
		Message:  "issue 42 missing label",
	}, snap[0])
	assert.Equal(t, FailureRecord{
		Source:   "bootstrap",
		Scenario: "",
		Class:    Infrastructure,
		Message:  "mint download timeout",
	}, snap[1])

	snap[0].Message = "tampered"
	assert.Equal(t, "issue 42 missing label", r.Snapshot()[0].Message)
}

func TestRecorderConcurrentRecordFailure(t *testing.T) {
	t.Parallel()

	const goroutines = 12
	const perGoroutine = 25

	r := &Recorder{}
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			class := Scenario
			if i%2 == 0 {
				class = Infrastructure
			}
			for range perGoroutine {
				r.RecordFailure("scenario", fmt.Sprintf("scenario-%d", i), class, "boom")
			}
			r.Counts()
			r.Snapshot()
		}()
	}
	wg.Wait()

	infrastructure, scenario := r.Counts()
	require.Equal(t, 6*perGoroutine, infrastructure)
	require.Equal(t, 6*perGoroutine, scenario)
	snap := r.Snapshot()
	require.Len(t, snap, goroutines*perGoroutine)
	require.Equal(t, len(snap), infrastructure+scenario)
}

func TestReportJSONRoundTrip(t *testing.T) {
	t.Parallel()

	r := &Recorder{}
	r.RecordFailure("scenario", "adding label", Scenario, "issue 42 missing label")
	r.RecordFailure("bootstrap", "", Infrastructure, "mint download timeout")
	report := r.Report(Infrastructure)

	data, err := json.Marshal(report)
	require.NoError(t, err)

	var shape map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &shape))
	assert.Len(t, shape, 4)
	for _, key := range []string{"class", "infrastructure_failures", "scenario_failures", "failures"} {
		assert.Contains(t, shape, key)
	}
	assert.Equal(t, `"infrastructure"`, string(shape["class"]))

	var failures []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(shape["failures"], &failures))
	require.Len(t, failures, 2)
	assert.Len(t, failures[0], 4)
	for _, key := range []string{"source", "scenario", "class", "message"} {
		assert.Contains(t, failures[0], key)
	}

	var back Report
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, report, back)
}

func TestReportCleanRecorderMarshalsEmptyFailures(t *testing.T) {
	t.Parallel()

	report := new(Recorder).Report(Scenario)
	data, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"failures":[]`)

	var back Report
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, report, back)
}

func TestReportSuccessRunClass(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Class("success"), Success)

	report := new(Recorder).Report(Success)
	assert.Equal(t, Success, report.Class)
	assert.Zero(t, report.InfrastructureFailures)
	assert.Zero(t, report.ScenarioFailures)
	assert.Empty(t, report.Failures)
	assert.NotNil(t, report.Failures)

	data, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"class":"success"`)

	var back Report
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, report, back)
}
