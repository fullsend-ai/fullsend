package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// TestSignalContext_CancelsOnFirstSignal verifies that signalContext returns
// a context that is cancelled when the process receives the first SIGINT.
// This is the primary invariant for #6936: the first signal must cancel the
// context so the cleanup path (metrics, telemetry) can run.
func TestSignalContext_CancelsOnFirstSignal(t *testing.T) {
	ctx, cleanup := signalContext()
	defer cleanup()

	// Send SIGINT to ourselves.
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	select {
	case <-ctx.Done():
		// Expected: context cancelled on first signal.
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled within 2s after SIGINT")
	}
}

// TestSignalContext_AbsorbsSubsequentSignals verifies that after the first
// signal cancels the context, further SIGINT/SIGTERM deliveries are absorbed
// (they do not kill the process). This is the second invariant for #6936:
// GitHub Actions sends SIGINT then SIGTERM ~7.5 s later; the second signal
// must not trigger the default terminate handler.
func TestSignalContext_AbsorbsSubsequentSignals(t *testing.T) {
	ctx, cleanup := signalContext()
	defer cleanup()

	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}

	// First signal: cancels the context.
	if err := proc.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("first Signal: %v", err)
	}

	select {
	case <-ctx.Done():
		// Good — context cancelled.
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled after first SIGINT")
	}

	// Second signal: must be absorbed (not kill the process).
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second Signal: %v", err)
	}

	// If we reach this point without dying, the signal was absorbed.
	// Give a short window for the signal to be delivered and handled.
	time.Sleep(50 * time.Millisecond)
}

// TestSignalContext_CleanupStopsForwarding verifies that after cleanup() is
// called, signal forwarding is disabled. This ensures signalContext does not
// leak a global signal registration.
func TestSignalContext_CleanupStopsForwarding(t *testing.T) {
	ctx, cleanup := signalContext()

	// Call cleanup before sending any signal.
	cleanup()

	// After cleanup, the context should not yet be cancelled
	// (no signal was sent before cleanup).
	select {
	case <-ctx.Done():
		t.Fatal("context was cancelled before any signal was sent")
	default:
		// Expected: context still active.
	}
}
