package common

import (
	"strings"
	"sync"
	"time"
)

// cliHeartbeatInterval is how often a long-running fullsend CLI call
// logs that it is still running. A package variable so tests can shorten
// it.
var cliHeartbeatInterval = 30 * time.Second

// cliProgress counts fullsend CLI calls across the whole process, per
// command (for example "github setup"). The behaviour suite runs many
// installs at once across several orgs, and the CLI output is only
// logged when a call ends, so without these counters a long silence
// cannot be told apart from "every call is still running".
type cliProgress struct {
	mu        sync.Mutex
	started   map[string]int
	completed map[string]int
}

var processCLIProgress = &cliProgress{
	started:   make(map[string]int),
	completed: make(map[string]int),
}

// begin records a start and returns the counts for cmd afterwards.
func (p *cliProgress) begin(cmd string) (inFlight, completed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started[cmd]++
	return p.started[cmd] - p.completed[cmd], p.completed[cmd]
}

// end records a completion and returns the counts for cmd afterwards.
func (p *cliProgress) end(cmd string) (inFlight, completed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completed[cmd]++
	return p.started[cmd] - p.completed[cmd], p.completed[cmd]
}

func (p *cliProgress) snapshot(cmd string) (inFlight, completed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started[cmd] - p.completed[cmd], p.completed[cmd]
}

// cliCommandName is the leading subcommand words of args, enough to
// tell "github setup" from "inference status" without the flags.
func cliCommandName(args []string) string {
	n := min(len(args), 2)
	return strings.Join(args[:n], " ")
}

// runCLILogged runs one fullsend CLI call and logs when it starts, every
// cliHeartbeatInterval while it runs, and when it ends, each time with
// the number of calls of the same command in flight and completed in this
// process. target names the repo the call is for.
func runCLILogged(runCLI CLIRunnerFunc, logf func(string, ...any), binary, token, target string, args ...string) (string, error) {
	cmd := cliCommandName(args)
	inFlight, completed := processCLIProgress.begin(cmd)
	start := time.Now()
	logf("[install] fullsend %s for %s started (%d in flight, %d completed in this process)", cmd, target, inFlight, completed)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(cliHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				inFlight, completed := processCLIProgress.snapshot(cmd)
				logf("[install] fullsend %s for %s still running after %s (%d in flight, %d completed in this process)",
					cmd, target, time.Since(start).Round(time.Second), inFlight, completed)
			}
		}
	}()

	out, err := runCLI(binary, token, args...)
	close(stop)
	wg.Wait()

	inFlight, completed = processCLIProgress.end(cmd)
	result := "succeeded"
	if err != nil {
		result = "failed"
	}
	logf("[install] fullsend %s for %s %s after %s (%d in flight, %d completed in this process)",
		cmd, target, result, time.Since(start).Round(time.Millisecond), inFlight, completed)
	return out, err
}
