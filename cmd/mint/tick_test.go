package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// fakeTicker counts Tick calls and records whether each had a deadline.
type fakeTicker struct {
	mu           sync.Mutex
	ticks        int
	noDeadline   int
	firstTickAt  time.Time
	tickReceived chan struct{}
}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{tickReceived: make(chan struct{}, 100)}
}

func (f *fakeTicker) Tick(ctx context.Context) mintcore.TickResult {
	f.mu.Lock()
	if f.ticks == 0 {
		f.firstTickAt = time.Now()
	}
	f.ticks++
	if _, ok := ctx.Deadline(); !ok {
		f.noDeadline++
	}
	f.mu.Unlock()
	f.tickReceived <- struct{}{}
	return mintcore.TickResult{}
}

func (f *fakeTicker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ticks
}

// captureLogs redirects the standard logger for the test. The buffer is
// guarded because the server and ticker goroutines log concurrently.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunTicker_TicksUntilCancelled(t *testing.T) {
	f := newFakeTicker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		runTicker(ctx, f, 10*time.Millisecond, 30*time.Millisecond)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-f.tickReceived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d ticks", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTicker did not stop after cancel")
	}
	n := f.count()
	time.Sleep(30 * time.Millisecond)
	if f.count() != n {
		t.Error("ticked after it stopped")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.firstTickAt.Sub(start) < 30*time.Millisecond {
		t.Errorf("first tick after %s, want the initial delay of 30ms", f.firstTickAt.Sub(start))
	}
	if f.noDeadline != 0 {
		t.Errorf("%d ticks had no deadline", f.noDeadline)
	}
}

func TestRunTicker_CancelledDuringInitialDelay(t *testing.T) {
	f := newFakeTicker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTicker(ctx, f, time.Millisecond, time.Hour)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTicker did not stop during the initial delay")
	}
	if f.count() != 0 {
		t.Errorf("ticks = %d, want 0", f.count())
	}
}

func TestTickJitter_Bounded(t *testing.T) {
	for i := 0; i < 100; i++ {
		if d := tickJitter(5 * time.Minute); d < 0 || d > 30*time.Second {
			t.Fatalf("jitter %s outside [0, 30s]", d)
		}
	}
}

// runMintBriefly runs the standalone mint for a moment and returns its logs.
func runMintBriefly(t *testing.T) string {
	t.Helper()
	logs := captureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after cancel")
	}
	return logs.String()
}

func setMintEnv(t *testing.T, roleAppIDs string) {
	t.Helper()
	t.Setenv("ROLE_APP_IDS", roleAppIDs)
	t.Setenv("PEM_DIR", setupTestPEMDir(t))
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("PORT", "0")
	t.Setenv("FALLBACK_MINT_URL", "")
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-repo")
	t.Setenv("FULLSEND_WEBHOOK_SECRET", "")
	t.Setenv("FULLSEND_POLL_SWEEP", "")
	t.Setenv("FULLSEND_MINT_TICK_INTERVAL", "")
	t.Setenv("FULLSEND_POLL_SWEEP_PERIOD", "")
}

func TestRun_TickDisabled(t *testing.T) {
	setMintEnv(t, `{"fullsend":"500"}`)
	t.Setenv("FULLSEND_POLL_SWEEP", "off")
	logs := runMintBriefly(t)
	if !strings.Contains(logs, "mint tick: disabled") {
		t.Errorf("logs = %q, want the tick disabled line", logs)
	}
}

func TestRun_TickStartsAndStops(t *testing.T) {
	setMintEnv(t, `{"fullsend":"500"}`)
	t.Setenv("FULLSEND_MINT_TICK_INTERVAL", "1m")
	orig := tickJitter
	// No tick may reach GitHub in this test: the first one is an hour out.
	tickJitter = func(time.Duration) time.Duration { return time.Hour }
	t.Cleanup(func() { tickJitter = orig })
	logs := runMintBriefly(t)
	if !strings.Contains(logs, "mint tick: every 1m0s, first in 1h0m0s") {
		t.Errorf("logs = %q, want the tick start line", logs)
	}
	if strings.Contains(logs, "mint tick: redelivery=") {
		t.Errorf("a tick ran: %q", logs)
	}
}

func TestRun_InvalidTickSetting(t *testing.T) {
	setMintEnv(t, `{"fullsend":"500"}`)
	t.Setenv("FULLSEND_MINT_TICK_INTERVAL", "10s")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx)
	if err == nil || !strings.Contains(err.Error(), "FULLSEND_MINT_TICK_INTERVAL") {
		t.Fatalf("run error = %v, want the tick interval rejected", err)
	}
}
