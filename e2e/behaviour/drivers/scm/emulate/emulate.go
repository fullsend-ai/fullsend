//go:build behaviour

// Package emulate spawns a local vercel-labs/emulate GitHub instance and
// wires it to the existing scm/github driver via LiveClient.WithBaseURL. It
// does not implement scm.Driver itself — it composes the real one, so
// CreateIssue/AddIssueLabels/AddComment/GetIssue/CommitFile/CloseIssue are
// the same code paths exercised against live GitHub.
//
// This package is standalone: it is not registered as a BEHAVIOUR_SCM
// option and is not wired into suite_test.go. emulate's Actions endpoints
// are REST record-level only (list/get/dispatch/cancel/logs as data) — it
// does not execute real workflow YAML, so it can never satisfy ci.Driver
// for scenarios that assert on real agent execution. See
// docs/guides/dev/behaviour-drivers.md for the intended use (direct import
// by Go test code that only needs SCM-level state, not a live Actions run).
//
// There is no HTTP reset endpoint: reset() in vercel-labs/emulate is an
// in-process JS method reachable only through the programmatic
// createEmulator() API, not exposed by the CLI-spawned server. Callers get
// isolation by starting one Instance per test binary (see emulate_test.go's
// TestMain) and letting each test create its own issue — CreateIssue
// returns a fresh, unique number every call, so tests don't collide.
package emulate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm/github"
	forgegithub "github.com/fullsend-ai/fullsend/internal/forge/github"
)

const (
	// emulatePackage pins the exact npm version so a CI run today and one
	// next month spawn identical route behavior. Bump deliberately.
	//
	// This is a version pin, not an integrity/hash pin — npx resolves
	// whatever npm currently serves for this version string, so a
	// compromised republish under this exact version could still run on
	// the CI runner. Accepted risk: the workflow that invokes this grants
	// no secrets and uses the default read-only GITHUB_TOKEN on plain
	// pull_request (not pull_request_target), so there is nothing for a
	// compromised package to exfiltrate. If that trigger or permission
	// scope ever changes, revisit this (e.g. install from a committed
	// package-lock.json via `npm ci` instead of `npx --yes`).
	emulatePackage = "emulate@0.8.0"

	startTimeout       = 15 * time.Second
	healthPoll         = 200 * time.Millisecond
	healthCheckTimeout = 2 * time.Second
)

// healthCheckClient bounds each individual health-check request. Using
// http.DefaultClient (no timeout) would let a stalled request block
// indefinitely if the caller's ctx has no deadline, defeating
// waitHealthy's startTimeout loop.
var healthCheckClient = &http.Client{Timeout: healthCheckTimeout}

// Instance owns a running `emulate` subprocess and a scm.Driver wired to
// it. It satisfies scm.Driver by embedding the real github driver.
type Instance struct {
	scm.Driver

	cmd      *exec.Cmd
	baseURL  string
	client   *forgegithub.LiveClient
	seedPath string
}

// Start launches `npx emulate --service github` with a generated seed file,
// waits for it to become healthy, and returns an Instance ready to use as a
// scm.Driver. Call Close to shut the subprocess down. logf receives the
// subprocess's stderr, one line per call.
func Start(ctx context.Context, opts SeedOptions, logf func(string, ...any)) (*Instance, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("emulate: reserving port: %w", err)
	}

	seedPath, token, err := writeSeedFile(opts)
	if err != nil {
		return nil, fmt.Errorf("emulate: writing seed config: %w", err)
	}

	// exec.Command, not CommandContext: the subprocess must outlive Start.
	// ctx here only bounds the startup health-check below — it may carry a
	// short deadline that has nothing to do with how long the caller wants
	// the emulator to keep running. The process's actual lifetime is
	// controlled by Close.
	cmd := exec.Command("npx", "--yes", emulatePackage,
		"--service", "github",
		"--port", strconv.Itoa(port),
		"--seed", seedPath,
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = os.Remove(seedPath)
		return nil, fmt.Errorf("emulate: attaching stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(seedPath)
		return nil, fmt.Errorf("emulate: starting subprocess (is Node/npx installed?): %w", err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	go streamLog(stderr, logf)

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitHealthy(ctx, baseURL); err != nil {
		killAndReap(cmd)
		_ = os.Remove(seedPath)
		return nil, fmt.Errorf("emulate: did not become healthy: %w", err)
	}

	client := forgegithub.New(token).WithBaseURL(baseURL)

	return &Instance{
		Driver:   github.New(client),
		cmd:      cmd,
		baseURL:  baseURL,
		client:   client,
		seedPath: seedPath,
	}, nil
}

// BaseURL returns the emulator's HTTP address, for pointing other
// components under test (e.g. a dispatch webhook receiver) at it.
func (i *Instance) BaseURL() string { return i.baseURL }

// Client exposes the underlying forge client for callers that need
// operations beyond scm.Driver's minimal surface.
func (i *Instance) Client() *forgegithub.LiveClient { return i.client }

// Close terminates the emulate subprocess and removes the seed file.
func (i *Instance) Close() error {
	defer os.Remove(i.seedPath)
	killAndReap(i.cmd)
	return nil
}

// killAndReap kills cmd and waits for it to exit. Wait's error is
// discarded: on the expected path it's "signal: killed" from our own
// Kill call, not a genuine failure. Skipping Wait entirely would leak
// the process's stderr pipe fd and leave it unreaped, so it must still
// be called — just not surfaced as a failure.
func killAndReap(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func waitHealthy(ctx context.Context, baseURL string) error {
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if healthCheck(ctx, baseURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPoll):
		}
	}
	return fmt.Errorf("timed out after %s", startTimeout)
}

func healthCheck(ctx context.Context, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/rate_limit", nil)
	if err != nil {
		return false
	}
	resp, err := healthCheckClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func streamLog(r io.Reader, logf func(string, ...any)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		logf("[emulate] %s", scanner.Text())
	}
}
