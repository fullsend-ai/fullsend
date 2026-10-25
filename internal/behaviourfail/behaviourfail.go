// Package behaviourfail classifies behaviour-suite failures as
// infrastructure (environment: setup, provisioning, rate limits, quotas)
// or scenario (the assertion itself) failures.
//
// It is the leaf of the classification layout: producers whose errors
// are context-dependent (install drivers, e2etest bootstrap) mark errors
// with Mark, and consumers detect the mark with Is. The package imports
// only the standard library so every producer can import it without
// dependency cycles; the composite classifier that recognizes structural
// error types lives in pkg/behaviourtest/failclass.
//
// Recorder accumulates classified failures for one suite run; the suite
// shares a single recorder across concurrent scenarios and renders a
// Report at completion.
package behaviourfail

import (
	"errors"
	"fmt"
	"sync"
)

// Class is the classification of a behaviour-suite failure: an
// environment condition (Infrastructure) or a product assertion failure
// (Scenario).
type Class string

const (
	// Infrastructure marks a failure caused by the environment rather
	// than the system under test: setup, provisioning, rate limiting, or
	// quota exhaustion.
	Infrastructure Class = "infrastructure"

	// Scenario marks a failure of the scenario's own assertion — the
	// observed state did not match the expected state.
	Scenario Class = "scenario"

	// Success is the run-level class of a clean run: no failure was
	// recorded and the suite runner exited zero. It appears only as the
	// completion report's class, never as a per-failure record class,
	// which is why Recorder.Counts does not count it.
	Success Class = "success"
)

// InfraError marks an error as an infrastructure failure. Producers
// create it with Mark; consumers detect it with Is or errors.As into a
// *InfraError to read Reason.
type InfraError struct {
	// Reason names the infrastructure failure mode that caused the mark,
	// e.g. "setup: repo never visible". It is included in the error
	// message for diagnosis; classification only needs the mark itself.
	Reason string

	// err is the wrapped cause, reachable through Unwrap.
	err error
}

// Error returns the marked error's message: the infrastructure label,
// the reason, and the wrapped error's message.
func (e *InfraError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("infrastructure failure (%s)", e.Reason)
	}
	return fmt.Sprintf("infrastructure failure (%s): %v", e.Reason, e.err)
}

// Unwrap returns the wrapped error so errors.Is and errors.As traverse
// the cause chain.
func (e *InfraError) Unwrap() error { return e.err }

// Mark wraps err as an infrastructure failure with the given reason,
// e.g. Mark(err, "setup: repo never visible"). It returns nil when err
// is nil, so marking can never invent a failure. The wrapped error's
// message is preserved and the cause stays reachable through errors.Is
// and errors.As; detect the mark with Is.
func Mark(err error, reason string) error {
	if err == nil {
		return nil
	}
	return &InfraError{Reason: reason, err: err}
}

// Is reports whether err is, or wraps, an InfraError — that is, whether
// a producer marked its cause as an infrastructure failure. It returns
// false for nil and unmarked errors.
func Is(err error) bool {
	var infra *InfraError
	return errors.As(err, &infra)
}

// FailureRecord is one classified failure in a run's Report.
type FailureRecord struct {
	// Source labels where the failure originated: "scenario" for a
	// failed scenario, "bootstrap" for a suite-startup failure.
	Source string `json:"source"`

	// Scenario is the failing scenario's name, empty for bootstrap
	// failures that occur before any scenario runs.
	Scenario string `json:"scenario"`

	// Class is the failure's classification.
	Class Class `json:"class"`

	// Message is the failure's redacted message. Callers must redact
	// before recording — this package is stdlib-only and cannot import
	// the suite's redaction utilities.
	Message string `json:"message"`
}

// Recorder accumulates classified failures for one suite run. Scenario
// worlds share a single Recorder by pointer and scenarios run
// concurrently, so every method is concurrency-safe. Every method is
// also nil-receiver-safe: an uninitialized Recorder records nothing and
// reports zero counts, so hand-built Worlds keep working.
type Recorder struct {
	mu       sync.Mutex
	failures []FailureRecord
}

// RecordFailure appends one classified failure. msg must already be
// redacted; the recorder stores it verbatim.
func (r *Recorder) RecordFailure(source, scenario string, class Class, msg string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, FailureRecord{
		Source:   source,
		Scenario: scenario,
		Class:    class,
		Message:  msg,
	})
}

// Snapshot returns a copy of the recorded failures in recording order,
// or nil when nothing has been recorded.
func (r *Recorder) Snapshot() []FailureRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failures) == 0 {
		return nil
	}
	out := make([]FailureRecord, len(r.failures))
	copy(out, r.failures)
	return out
}

// Counts returns the number of recorded infrastructure and scenario
// failures.
func (r *Recorder) Counts() (infrastructure, scenario int) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.failures {
		switch f.Class {
		case Infrastructure:
			infrastructure++
		case Scenario:
			scenario++
		}
	}
	return infrastructure, scenario
}

// Report is the machine-readable failure-class report written when a
// suite run completes. The infra-retry wrapper script reads the class
// field to decide whether a failed run may be retried once.
type Report struct {
	// Class is the run classification reported for the whole run: a
	// failed run is Scenario when any recorded failure is
	// scenario-classified and Infrastructure when at least one failure
	// is recorded with none scenario-classified; a clean run reports
	// Success.
	Class Class `json:"class"`

	// InfrastructureFailures is the count of recorded infrastructure
	// failures.
	InfrastructureFailures int `json:"infrastructure_failures"`

	// ScenarioFailures is the count of recorded scenario failures.
	ScenarioFailures int `json:"scenario_failures"`

	// Failures lists every recorded failure in recording order. It is
	// never nil, so it marshals as an empty JSON array rather than null.
	Failures []FailureRecord `json:"failures"`
}

// Report builds the completion report for a run classified as runClass.
// The run class is the caller's decision — the recorder observes
// failures, not the run outcome — so the runner derives it and the
// recorder contributes the counts and failure records. Pass Success for
// a clean run; the runner writes that report too, so a fresh marker
// always exists at completion and staleness stays detectable.
func (r *Recorder) Report(runClass Class) Report {
	infrastructure, scenario := r.Counts()
	failures := r.Snapshot()
	if failures == nil {
		failures = []FailureRecord{}
	}
	return Report{
		Class:                  runClass,
		InfrastructureFailures: infrastructure,
		ScenarioFailures:       scenario,
		Failures:               failures,
	}
}
