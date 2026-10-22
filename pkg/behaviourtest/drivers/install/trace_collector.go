// trace_collector.go runs a job-scoped OpenTelemetry Collector for the
// behaviour suite. The tested Fullsend workflows run on separate
// GitHub-hosted runners, so the Collector's OTLP/HTTP receiver is
// published on the CI worker's loopback and exposed to them through a
// Cloudflare quick tunnel (TLS, no inbound address needed). A fresh
// random bearer token, required by the receiver, is delivered to each
// leased repository as the OTEL_EXPORTER_OTLP_TRACES_HEADERS secret
// next to the OTEL_EXPORTER_OTLP_TRACES_ENDPOINT variable the reusable
// workflows already read. The preview mint driver starts the collector
// before deploying its mint and passes the same endpoint and token to
// `fullsend mint deploy`, so mint spans land in the same trace files as
// the workflow spans that called the mint. The Collector writes one
// JSONL file per trace ID; Finalize copies those files and the
// Collector log under BEHAVIOUR_ARTIFACT_DIR before the job's redaction
// and upload steps run.
package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/security"
)

const (
	// envTraceCollector controls the job-scoped trace collector:
	// unset (auto) enables it in GitHub Actions when
	// BEHAVIOUR_ARTIFACT_DIR is set; true/1/on forces it on (still
	// requires BEHAVIOUR_ARTIFACT_DIR); false/0/off disables it.
	envTraceCollector = "BEHAVIOUR_TRACE_COLLECTOR"

	// otelcolImage is the OpenTelemetry Collector (contrib
	// distribution, for the bearertokenauth extension, the transform and
	// groupbyattrs processors and the file exporter) run under Podman.
	otelcolImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.120.0"

	// cloudflaredImage runs the quick tunnel that exposes the
	// receiver to the tested workflows' runners.
	cloudflaredImage = "docker.io/cloudflare/cloudflared:2025.2.0"

	// otelTracesEndpointVar and otelTracesHeadersSecret are the
	// repository variable and secret the reusable workflows read
	// (vars.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT,
	// secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS).
	otelTracesEndpointVar   = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	otelTracesHeadersSecret = "OTEL_EXPORTER_OTLP_TRACES_HEADERS"

	// collectorTokenEnv is the environment variable that carries the
	// bearer token into the Collector container. podman reads its
	// value from its own environment (-e NAME), so the token is never
	// on a command line or in the config file.
	collectorTokenEnv = "BT_OTELCOL_BEARER_TOKEN"

	// collectorArtifactSubdir is the directory under
	// BEHAVIOUR_ARTIFACT_DIR that receives the trace files and the
	// Collector log.
	collectorArtifactSubdir = "otel-collector"

	// traceIDAttribute carries the trace ID through the Collector's
	// traces pipeline so the file exporter can route by it, and
	// traceFilePattern is the per-trace file name the exporter derives
	// from it (the * is the trace ID).
	traceIDAttribute = "bt.trace_id"
	traceFilePattern = "trace-*.jsonl"

	collectorContainerPort     = 4318
	traceCollectorStartTimeout = 10 * time.Minute
	collectorStopSeconds       = "30"
	tunnelStopSeconds          = "10"

	// traceCollectorStopTimeout bounds one cleanup attempt (the podman
	// stop/logs/rm calls); it comfortably exceeds the two graceful
	// shutdown periods above.
	traceCollectorStopTimeout = 3 * time.Minute

	// maxScrubFileSize bounds the files the token scrub reads into
	// memory. Behaviour artifacts are far smaller in practice.
	maxScrubFileSize = 512 << 20
)

// collectorConfig is the Collector configuration. The token is
// resolved from the container environment at start-up and never
// written to disk.
const collectorConfig = `extensions:
  bearertokenauth:
    token: "${env:` + collectorTokenEnv + `}"

receivers:
  otlp:
    protocols:
      http:
        endpoint: "0.0.0.0:4318"
        auth:
          authenticator: bearertokenauth

processors:
  # The file exporter can only route by a resource attribute, but the
  # trace ID is a span field. Copy it into a temporary span attribute;
  # groupbyattrs then moves it to the resource, regrouping the spans of
  # each request by trace ID.
  transform/trace_id:
    trace_statements:
      - context: span
        statements:
          - set(attributes["` + traceIDAttribute + `"], trace_id.string)
  groupbyattrs/trace_id:
    keys:
      - ` + traceIDAttribute + `

exporters:
  # One file per trace ID: the * in path is replaced by the attribute
  # value. Files are opened for append, so spans of a trace that arrive
  # in different requests (or after the file was evicted from the open
  # file set) land in the same file.
  file:
    path: /traces/` + traceFilePattern + `
    group_by:
      enabled: true
      resource_attribute: ` + traceIDAttribute + `
      max_open_files: 100

service:
  extensions: [bearertokenauth]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [transform/trace_id, groupbyattrs/trace_id]
      exporters: [file]
`

// quickTunnelHostRe matches the hostname cloudflared reports for a
// quick tunnel; anything else is rejected rather than written into
// the leased repositories' configuration.
var quickTunnelHostRe = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)*\.trycloudflare\.com$`)

// commandRunner runs name with args, adding extraEnv to the current
// environment, and returns combined output.
type commandRunner func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error)

func execCommandRunner(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.CombinedOutput()
}

// traceSink is the composed driver's view of the trace collector.
type traceSink interface {
	// ConfigureRepo points a freshly installed repository's workflows
	// at the collector, using client (the repository org's
	// credentials).
	ConfigureRepo(ctx context.Context, client forge.Client, org, repo string) error
	// Stop flushes and stops the collector, copies the trace files
	// under the artifact dir and removes the token from every
	// artifact. Safe to call more than once.
	Stop(ctx context.Context) error
}

// traceCollector owns the Collector and tunnel containers.
type traceCollector struct {
	logf        func(string, ...any)
	run         commandRunner
	httpClient  *http.Client
	freePort    func() (int, error)
	artifactDir string

	readyTimeout time.Duration
	pollInterval time.Duration

	token         string
	endpoint      string
	workDir       string
	collectorName string
	tunnelName    string
	collectorUp   bool
	tunnelUp      bool

	// stopMu serializes Stop; stopped is set only once every step of the
	// cleanup succeeded, so a failed cleanup can be retried.
	stopMu  sync.Mutex
	stopped bool
}

var _ traceSink = (*traceCollector)(nil)

// traceCollectorEnabled decides from the environment whether to run the
// collector. A non-nil error means the configuration is unusable; the
// caller logs it and runs without the collector.
func traceCollectorEnabled(getenv func(string) string) (bool, error) {
	artifactDir := strings.TrimSpace(getenv("BEHAVIOUR_ARTIFACT_DIR"))
	switch v := strings.ToLower(strings.TrimSpace(getenv(envTraceCollector))); v {
	case "":
		return getenv("GITHUB_ACTIONS") == "true" && artifactDir != "", nil
	case "1", "true", "on":
		if artifactDir == "" {
			return false, fmt.Errorf("%s=%s requires BEHAVIOUR_ARTIFACT_DIR", envTraceCollector, v)
		}
		return true, nil
	case "0", "false", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s: unsupported value %q (use true or false)", envTraceCollector, v)
	}
}

// newTraceCollector returns a collector with production dependencies.
func newTraceCollector(artifactDir string, logf func(string, ...any)) *traceCollector {
	return &traceCollector{
		logf:         logf,
		run:          execCommandRunner,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		freePort:     freeLocalPort,
		artifactDir:  artifactDir,
		readyTimeout: 3 * time.Minute,
		pollInterval: time.Second,
	}
}

// traceStart is the outcome of starting the job-scoped collector before
// the driver that will own it exists, so the suite's mint can be
// deployed with the collector as its trace destination. started is the
// running collector; pending is a collector that failed to start and
// whose cleanup Finalize must retry. Both are nil when collection is off.
type traceStart struct {
	started *traceCollector
	pending interface{ Stop(context.Context) error }
}

// startTraceCollector starts the collector when the environment enables
// it. Failures are logged and leave started nil: trace collection is
// diagnostic and must not fail the suite.
func startTraceCollector(logf func(string, ...any)) traceStart {
	enabled, err := traceCollectorEnabled(os.Getenv)
	if err != nil {
		logf("[trace-collector] WARNING: not collecting traces: %v", err)
		return traceStart{}
	}
	if !enabled {
		return traceStart{}
	}
	if _, err := exec.LookPath("podman"); err != nil {
		logf("[trace-collector] WARNING: not collecting traces: podman not found: %v", err)
		return traceStart{}
	}
	tc := newTraceCollector(strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR")), logf)
	// Bounds image pulls and readiness waits.
	ctx, cancel := context.WithTimeout(context.Background(), traceCollectorStartTimeout)
	defer cancel()
	if err := tc.Start(ctx); err != nil {
		logf("[trace-collector] WARNING: not collecting traces: %v", err)
		// Start already tried to clean up. If that failed too, the
		// containers (and the token) are still around: have Finalize
		// retry, without pointing any repository at the collector.
		if tc.cleanupPending() {
			return traceStart{pending: tc}
		}
		return traceStart{}
	}
	return traceStart{started: tc}
}

// attach hands the collector to a composed driver, which configures each
// leased repository and stops the collector at Finalize. A driver that
// is not composed cannot own it, so the collector is stopped instead.
func (s traceStart) attach(d Driver) Driver {
	cd, ok := d.(*composedDriver)
	if !ok {
		s.abort()
		return d
	}
	if s.started != nil {
		cd.traces = s.started
	}
	if s.pending != nil {
		cd.pendingTraces = s.pending
	}
	return d
}

// abort stops a collector that will not be attached (the factory
// failed), so its containers and token do not outlive the suite.
func (s traceStart) abort() {
	if s.started != nil {
		if err := s.started.Stop(context.Background()); err != nil {
			s.started.logf("[trace-collector] cleanup after failed driver setup: %v", err)
		}
	}
	if s.pending != nil {
		_ = s.pending.Stop(context.Background())
	}
}

// mintTracing writes the collector headers to a private temporary file
// for `fullsend mint deploy --otlp-headers-file`, so the token never
// appears on a command line, and returns the traces endpoint and the
// file path. It returns empty values when no collector is running. The
// caller must call cleanup once the deploy has finished.
func (s traceStart) mintTracing() (endpoint, headersFile string, cleanup func(), err error) {
	cleanup = func() {}
	if s.started == nil {
		return "", "", cleanup, nil
	}
	f, err := os.CreateTemp("", "bt-otlp-headers-*")
	if err != nil {
		return "", "", cleanup, fmt.Errorf("creating mint headers file: %w", err)
	}
	path := f.Name()
	cleanup = func() { _ = os.Remove(path) }
	// CreateTemp already uses 0600; make it explicit for the CLI check.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return "", "", func() {}, fmt.Errorf("protecting mint headers file: %w", err)
	}
	_, werr := f.WriteString(s.started.collectorHeaders() + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("writing mint headers file: %w", werr)
	}
	return s.started.endpoint, path, cleanup, nil
}

// collectorHeaders returns the receiver's bearer-token header in
// OTEL_EXPORTER_OTLP_HEADERS syntax.
func (c *traceCollector) collectorHeaders() string {
	return "Authorization=Bearer%20" + c.token
}

// Start generates the token, starts the Collector and the tunnel, and
// checks that the receiver enforces the token. On failure everything
// started so far is stopped (including the artifact scrub).
func (c *traceCollector) Start(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			if stopErr := c.Stop(context.Background()); stopErr != nil {
				c.logf("[trace-collector] cleanup after failed start: %v", stopErr)
			}
		}
	}()

	if c.token, err = randomHex(32); err != nil {
		return fmt.Errorf("generating token: %w", err)
	}
	// The token is deliberately not announced with a GitHub Actions
	// add-mask command: the step log is tee'd into the artifact dir, and
	// a suite killed by the test timeout never runs the scrub in Stop,
	// so the command would leave the raw token in the uploaded log. The
	// process redactor keeps the token out of this process's own output,
	// and the token is never printed.
	security.RegisterRuntimeSecret(c.token)

	suffix, err := randomHex(4)
	if err != nil {
		return fmt.Errorf("generating container suffix: %w", err)
	}
	c.collectorName = "bt-otelcol-" + suffix
	c.tunnelName = "bt-cloudflared-" + suffix

	if c.workDir, err = os.MkdirTemp("", "bt-otelcol-"); err != nil {
		return fmt.Errorf("creating work dir: %w", err)
	}
	configPath := filepath.Join(c.workDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(collectorConfig), 0o644); err != nil {
		return fmt.Errorf("writing collector config: %w", err)
	}
	tracesDir := filepath.Join(c.workDir, "traces")
	if err := os.Mkdir(tracesDir, 0o700); err != nil {
		return fmt.Errorf("creating traces dir: %w", err)
	}

	receiverPort, err := c.freePort()
	if err != nil {
		return fmt.Errorf("choosing receiver port: %w", err)
	}
	metricsPort, err := c.freePort()
	if err == nil && metricsPort == receiverPort {
		// The receiver's port is not bound yet, so it can be handed out again.
		metricsPort, err = c.freePort()
	}
	if err != nil {
		return fmt.Errorf("choosing tunnel metrics port: %w", err)
	}
	if metricsPort == receiverPort {
		return fmt.Errorf("choosing tunnel metrics port: got the receiver port %d twice", receiverPort)
	}

	// --user 0:0: under rootless Podman container root is the CI user,
	// so the trace files are readable by this process afterwards.
	c.collectorUp = true
	if out, err := c.run(ctx, []string{collectorTokenEnv + "=" + c.token}, "podman", "run", "-d",
		"--name", c.collectorName,
		"--user", "0:0",
		"-e", collectorTokenEnv,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", receiverPort, collectorContainerPort),
		"-v", configPath+":/etc/bt-otelcol/config.yaml:ro,Z",
		"-v", tracesDir+":/traces:Z",
		otelcolImage,
		"--config=/etc/bt-otelcol/config.yaml",
	); err != nil {
		return fmt.Errorf("starting collector container: %w: %s", err, strings.TrimSpace(string(out)))
	}

	receiverURL := fmt.Sprintf("http://127.0.0.1:%d/v1/traces", receiverPort)
	if err := c.verifyReceiverAuth(ctx, receiverURL); err != nil {
		return err
	}

	c.tunnelUp = true
	if out, err := c.run(ctx, nil, "podman", "run", "-d",
		"--name", c.tunnelName,
		"--network", "host",
		cloudflaredImage,
		"tunnel",
		"--metrics", fmt.Sprintf("127.0.0.1:%d", metricsPort),
		"--url", fmt.Sprintf("http://127.0.0.1:%d", receiverPort),
	); err != nil {
		return fmt.Errorf("starting tunnel container: %w: %s", err, strings.TrimSpace(string(out)))
	}

	host, err := c.awaitTunnel(ctx, fmt.Sprintf("http://127.0.0.1:%d", metricsPort))
	if err != nil {
		return err
	}
	c.endpoint = "https://" + host + "/v1/traces"
	c.logf("[trace-collector] collecting traces at %s into %s", c.endpoint,
		filepath.Join(c.artifactDir, collectorArtifactSubdir))
	return nil
}

// verifyReceiverAuth waits for the receiver to answer, then checks that
// it rejects a missing and a wrong token and accepts the real one.
func (c *traceCollector) verifyReceiverAuth(ctx context.Context, receiverURL string) error {
	var status int
	err := c.poll(ctx, "collector receiver", func() (bool, error) {
		s, err := c.postTraces(ctx, receiverURL, "")
		if err != nil {
			return false, nil // not listening yet
		}
		status = s
		return true, nil
	})
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("collector receiver accepted a request without a token (HTTP %d)", status)
	}

	wrong, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("generating probe token: %w", err)
	}
	if s, err := c.postTraces(ctx, receiverURL, wrong); err != nil {
		return fmt.Errorf("probing collector receiver: %w", err)
	} else if s != http.StatusUnauthorized {
		return fmt.Errorf("collector receiver accepted a wrong token (HTTP %d)", s)
	}

	if s, err := c.postTraces(ctx, receiverURL, c.token); err != nil {
		return fmt.Errorf("probing collector receiver: %w", err)
	} else if s != http.StatusOK {
		return fmt.Errorf("collector receiver rejected the job token (HTTP %d)", s)
	}
	return nil
}

// postTraces sends an empty OTLP/JSON export request and returns the
// HTTP status.
func (c *traceCollector) postTraces(ctx context.Context, url, token string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// awaitTunnel waits until cloudflared has registered a connection and
// reports its quick-tunnel hostname.
func (c *traceCollector) awaitTunnel(ctx context.Context, metricsURL string) (string, error) {
	var host string
	err := c.poll(ctx, "cloudflared quick tunnel", func() (bool, error) {
		if host == "" {
			body, status, err := c.get(ctx, metricsURL+"/quicktunnel")
			if err != nil || status != http.StatusOK {
				return false, nil
			}
			var qt struct {
				Hostname string `json:"hostname"`
			}
			if json.Unmarshal(body, &qt) != nil || qt.Hostname == "" {
				return false, nil
			}
			if !quickTunnelHostRe.MatchString(qt.Hostname) {
				return false, fmt.Errorf("cloudflared reported an unexpected tunnel hostname")
			}
			host = qt.Hostname
		}
		_, status, err := c.get(ctx, metricsURL+"/ready")
		return err == nil && status == http.StatusOK, nil
	})
	return host, err
}

func (c *traceCollector) get(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return body, resp.StatusCode, err
}

// poll calls check every pollInterval until it reports done, returns an
// error, or readyTimeout elapses.
func (c *traceCollector) poll(ctx context.Context, what string, check func() (bool, error)) error {
	deadline := time.Now().Add(c.readyTimeout)
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", c.readyTimeout, what)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}

// ConfigureRepo sets the traces endpoint variable and the bearer-token
// headers secret on a leased repository. The repository is deleted at
// deallocation, which removes both. client holds the repository org's
// credentials.
func (c *traceCollector) ConfigureRepo(ctx context.Context, client forge.Client, org, repo string) error {
	if err := client.CreateOrUpdateRepoVariable(ctx, org, repo, otelTracesEndpointVar, c.endpoint); err != nil {
		return fmt.Errorf("setting variable %s: %w", otelTracesEndpointVar, err)
	}
	if err := client.CreateRepoSecret(ctx, org, repo, otelTracesHeadersSecret, c.collectorHeaders()); err != nil {
		return fmt.Errorf("setting secret %s: %w", otelTracesHeadersSecret, err)
	}
	return nil
}

// cleanupPending reports whether Stop has not yet completed a cleanup.
func (c *traceCollector) cleanupPending() bool {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	return !c.stopped
}

// Stop stops the tunnel first (no new traffic), then the Collector
// with a grace period so it flushes, saves its log and trace files
// under the artifact dir, removes the containers and the work dir, and
// scrubs the token from every artifact file. Cleanup runs under its own
// bounded context rather than the caller's, so a cancelled or expired
// caller context cannot skip it and a hung podman call cannot block it
// forever. If any step fails the error is returned and a later call
// retries the steps that did not complete.
func (c *traceCollector) Stop(_ context.Context) error {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	if c.stopped {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), traceCollectorStopTimeout)
	defer cancel()
	err := c.stop(ctx)
	if err == nil {
		c.stopped = true
	}
	return err
}

func (c *traceCollector) stop(ctx context.Context) error {
	var errs []error
	if c.tunnelUp {
		if out, err := c.run(ctx, nil, "podman", "stop", "--time", tunnelStopSeconds, c.tunnelName); err != nil {
			errs = append(errs, fmt.Errorf("stopping tunnel: %w: %s", err, strings.TrimSpace(string(out))))
		}
		if err := c.removeContainer(ctx, c.tunnelName); err != nil {
			errs = append(errs, err)
		} else {
			c.tunnelUp = false
		}
	}

	destDir := filepath.Join(c.artifactDir, collectorArtifactSubdir)
	if c.collectorUp {
		if out, err := c.run(ctx, nil, "podman", "stop", "--time", collectorStopSeconds, c.collectorName); err != nil {
			errs = append(errs, fmt.Errorf("stopping collector: %w: %s", err, strings.TrimSpace(string(out))))
		}
		if logs, err := c.run(ctx, nil, "podman", "logs", c.collectorName); err == nil && len(logs) > 0 {
			if err := c.writeArtifact(destDir, "collector.log", logs); err != nil {
				errs = append(errs, err)
			}
		}
		if err := c.removeContainer(ctx, c.collectorName); err != nil {
			errs = append(errs, err)
		} else {
			c.collectorUp = false
		}
	}

	if c.workDir != "" {
		// The work dir holds the only copy of the traces: remove it only
		// once they are saved, so a failed copy can be retried.
		if err := c.copyTraces(filepath.Join(c.workDir, "traces"), destDir); err != nil {
			errs = append(errs, err)
		} else if err := os.RemoveAll(c.workDir); err != nil {
			errs = append(errs, fmt.Errorf("removing work dir: %w", err))
		} else {
			c.workDir = ""
		}
	}

	if c.token != "" && c.artifactDir != "" {
		if err := scrubTokenInTree(c.artifactDir, c.token, c.logf); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		c.token = ""
	}
	return errors.Join(errs...)
}

// removeContainer force-removes a container. A container that no longer
// exists is not an error; any other failure is, because a container
// left behind keeps the bearer token in its configuration.
func (c *traceCollector) removeContainer(ctx context.Context, name string) error {
	out, err := c.run(ctx, nil, "podman", "rm", "-f", name)
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "no such container") {
		return fmt.Errorf("removing container %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyTraces copies the Collector's trace files into destDir.
func (c *traceCollector) copyTraces(srcDir, destDir string) error {
	entries, err := os.ReadDir(srcDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading traces dir: %w", err)
	}
	var errs []error
	copied := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("reading trace file: %w", err))
			continue
		}
		if err := c.writeArtifact(destDir, e.Name(), data); err != nil {
			errs = append(errs, err)
			continue
		}
		copied++
	}
	c.logf("[trace-collector] saved %d trace file(s) under %s", copied, destDir)
	return errors.Join(errs...)
}

// writeArtifact writes data, with the token masked, as destDir/name
// (0600, like the suite's other debug artifacts). It never writes
// through a symlink: destDir must be a real directory, and an existing
// file at the destination is removed and re-created, so a symlink or a
// file with looser permissions left there is not followed or reused.
func (c *traceCollector) writeArtifact(destDir, name string, data []byte) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", destDir, err)
	}
	if info, err := os.Lstat(destDir); err != nil {
		return fmt.Errorf("checking %s: %w", destDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory (refusing to follow a symlink)", destDir)
	}
	if c.token != "" {
		data = bytes.ReplaceAll(data, []byte(c.token), maskFor(c.token))
	}
	path := filepath.Join(destDir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replacing %s: %w", name, err)
	}
	// O_EXCL also refuses to follow a symlink planted after the removal.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("writing %s: %w", name, werr)
	}
	return nil
}

// scrubTokenInTree replaces token in every regular file under root.
func scrubTokenInTree(root, token string, logf func(string, ...any)) error {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var errs []error
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := scrubFileInPlace(path, token); err != nil {
			logf("[trace-collector] scrubbing %s: %v", path, err)
			errs = append(errs, err)
			// Fail closed: a file that could not be scrubbed may still
			// hold the token and would be uploaded, so empty it. If the
			// file cannot be written, delete it instead: removal only
			// needs write access to the parent directory.
			if stubErr := stubUnscrubbedFile(path); stubErr != nil {
				logf("[trace-collector] stubbing unscrubbed %s: %v", path, stubErr)
				errs = append(errs, stubErr)
				if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
					logf("[trace-collector] removing unscrubbed %s: %v", path, rmErr)
					errs = append(errs, rmErr)
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		errs = append(errs, walkErr)
	}
	if len(errs) > 0 {
		return fmt.Errorf("scrubbing token from artifacts: %w", errors.Join(errs...))
	}
	return nil
}

// stubUnscrubbedFile replaces the contents of a file that could not be
// scrubbed with a short stub. A file that no longer exists is fine.
func stubUnscrubbedFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, werr := f.WriteString("[content removed: the job token could not be scrubbed from this file]\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// scrubFileInPlace overwrites each occurrence of token in path with a
// mask of the same length. Writing in place over the bytes read keeps
// the file size and offsets unchanged, so a writer that is still
// appending to the file (the job's tee of the step log) is unaffected.
func scrubFileInPlace(path, token string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxScrubFileSize {
		return fmt.Errorf("file larger than %d bytes, not scanned", maxScrubFileSize)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte(token)) {
		return nil
	}
	data = bytes.ReplaceAll(data, []byte(token), maskFor(token))
	if _, err := f.WriteAt(data, 0); err != nil {
		return err
	}
	return f.Sync()
}

func maskFor(token string) []byte {
	return bytes.Repeat([]byte("*"), len(token))
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// freeLocalPort returns a currently unused loopback TCP port.
func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
