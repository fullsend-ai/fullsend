package common

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedLog collects log lines from concurrent goroutines.
type lockedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lockedLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *lockedLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestCLICommandName(t *testing.T) {
	assert.Equal(t, "github setup", cliCommandName([]string{"github", "setup", "org/repo", "--direct"}))
	assert.Equal(t, "inference status", cliCommandName([]string{"inference", "status", "org/repo"}))
	assert.Equal(t, "version", cliCommandName([]string{"version"}))
	assert.Equal(t, "", cliCommandName(nil))
}

func TestRunCLILogged_LogsStartAndEnd(t *testing.T) {
	var log lockedLog
	runner := func(_, _ string, _ ...string) (string, error) { return "out", nil }

	out, err := runCLILogged(runner, log.logf, "/bin/fullsend", "tok", "org/repo-start-end", "github", "setup", "org/repo-start-end")
	require.NoError(t, err)
	assert.Equal(t, "out", out)

	got := log.joined()
	assert.Contains(t, got, "fullsend github setup for org/repo-start-end started (1 in flight")
	assert.Contains(t, got, "fullsend github setup for org/repo-start-end succeeded after")
}

func TestRunCLILogged_ReportsFailure(t *testing.T) {
	var log lockedLog
	runner := func(_, _ string, _ ...string) (string, error) { return "partial", errors.New("boom") }

	out, err := runCLILogged(runner, log.logf, "/bin/fullsend", "tok", "org/repo-fail", "github", "setup", "org/repo-fail")
	require.EqualError(t, err, "boom")
	assert.Equal(t, "partial", out)
	assert.Contains(t, log.joined(), "fullsend github setup for org/repo-fail failed after")
}

func TestRunCLILogged_CountsConcurrentCalls(t *testing.T) {
	var log lockedLog
	release := make(chan struct{})
	var started sync.WaitGroup
	runner := func(_, _ string, _ ...string) (string, error) {
		started.Done()
		<-release
		return "", nil
	}

	baseInFlight, baseCompleted := processCLIProgress.snapshot("concurrent probe")
	const n = 3
	started.Add(n)
	var done sync.WaitGroup
	for range n {
		done.Add(1)
		go func() {
			defer done.Done()
			_, err := runCLILogged(runner, log.logf, "/bin/fullsend", "tok", "org/repo-concurrent", "concurrent", "probe")
			assert.NoError(t, err)
		}()
	}
	started.Wait()
	inFlight, _ := processCLIProgress.snapshot("concurrent probe")
	assert.Equal(t, baseInFlight+n, inFlight, "all calls should be counted in flight before any ends")

	close(release)
	done.Wait()
	inFlight, completed := processCLIProgress.snapshot("concurrent probe")
	assert.Equal(t, baseInFlight, inFlight)
	assert.Equal(t, baseCompleted+n, completed)
}

func TestRunCLILogged_HeartbeatWhileRunning(t *testing.T) {
	old := cliHeartbeatInterval
	cliHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { cliHeartbeatInterval = old })

	var log lockedLog
	runner := func(_, _ string, _ ...string) (string, error) {
		time.Sleep(60 * time.Millisecond)
		return "", nil
	}

	_, err := runCLILogged(runner, log.logf, "/bin/fullsend", "tok", "org/repo-heartbeat", "heartbeat", "probe")
	require.NoError(t, err)
	assert.Contains(t, log.joined(), "fullsend heartbeat probe for org/repo-heartbeat still running after")

	// No heartbeat may be logged after the call returns.
	before := log.joined()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, before, log.joined())
}
