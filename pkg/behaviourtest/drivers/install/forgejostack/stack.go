package forgejostack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StackLabel is the container label every container of a stack carries,
// with the stack's name as its value: the Forgejo and runner containers
// and the job containers the runner starts. Teardown removes every
// container with the label.
const StackLabel = "ai.fullsend.behaviour.forgejo-stack"

// Readiness probe repository and workflow, created by the stack's admin
// user once the runner is up.
const (
	adminUser        = "fullsend-admin"
	adminEmail       = "fullsend-admin@example.com"
	readinessRepo    = "fullsend-stack-readiness"
	readinessFile    = ".forgejo/workflows/readiness.yaml"
	defaultBranch    = "main"
	logTailLines     = 50
	diagnoseTimeout  = 30 * time.Second
	runnerStopPeriod = 10 * time.Second
)

// Default readiness timeouts and poll interval (see Config).
const (
	DefaultHealthTimeout = 3 * time.Minute
	DefaultJobTimeout    = 10 * time.Minute
	DefaultPollInterval  = 2 * time.Second

	// TeardownTimeout bounds the teardown Start runs after a failed
	// startup; callers may use it for Teardown too.
	TeardownTimeout = 2 * time.Minute
)

// Config configures Start.
type Config struct {
	// Engine is the host container engine. Required.
	Engine Engine
	// SocketPath is the engine socket's path on the host, mounted into
	// the runner so it starts job containers on the same engine.
	// Required.
	SocketPath string
	// Port is the loopback port Forgejo listens on. 0 picks a free one.
	Port int
	// Name prefixes the stack's container names and is the value of
	// StackLabel. Empty generates a unique name.
	Name string
	// Logf receives progress messages. Nil discards them.
	Logf func(string, ...any)
	// HTTPClient calls the Forgejo API. Nil uses a client with a 30s
	// request timeout.
	HTTPClient *http.Client
	// HealthTimeout bounds the wait for Forgejo's health endpoint.
	// Zero uses DefaultHealthTimeout.
	HealthTimeout time.Duration
	// JobTimeout bounds the wait for the readiness job. Zero uses
	// DefaultJobTimeout.
	JobTimeout time.Duration
	// PollInterval is the readiness poll interval. Zero uses
	// DefaultPollInterval.
	PollInterval time.Duration
}

// Stack is a running managed Forgejo and Forgejo Runner stack.
//
// Forgejo, the runner, and the runner's job containers all use the host
// network namespace, and Forgejo listens on the loopback interface
// only. The canonical URL, http://127.0.0.1:<port>/, therefore names the
// same server from the behaviour-test process, the runner, and every job
// container, under rootful Docker and under Podman (rootful or
// rootless), without publishing Forgejo beyond the host. Rootless Docker
// is not supported: its host network is the daemon's own namespace, not
// the test process's, so Start rejects it.
type Stack struct {
	cfg           Config
	name          string
	url           string
	adminPassword string

	mu         sync.Mutex
	containers []startedContainer
	tornDown   bool
}

type startedContainer struct {
	role string
	id   string
}

// Start pulls the pinned images, starts Forgejo and a registered runner
// with capacity RunnerCapacity, and waits until the stack is ready:
// Forgejo answers its health check at the canonical URL, and an Actions
// job, picked up by the runner and run in a job container, reached the
// canonical URL too. On failure it tears down whatever it started and
// returns the startup error joined with any teardown error; the error
// includes the tail of each started container's log.
func Start(ctx context.Context, cfg Config) (*Stack, error) {
	if cfg.Engine == nil {
		return nil, errors.New("forgejo stack: no container engine configured")
	}
	if cfg.SocketPath == "" {
		return nil, errors.New("forgejo stack: no engine socket path configured")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.HealthTimeout == 0 {
		cfg.HealthTimeout = DefaultHealthTimeout
	}
	if cfg.JobTimeout == 0 {
		cfg.JobTimeout = DefaultJobTimeout
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Name == "" {
		suffix, err := randomHex(4)
		if err != nil {
			return nil, err
		}
		cfg.Name = "fullsend-forgejo-dev-" + suffix
	}
	if cfg.Port == 0 {
		port, err := freeLoopbackPort()
		if err != nil {
			return nil, fmt.Errorf("forgejo stack: picking a loopback port: %w", err)
		}
		cfg.Port = port
	}
	password, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	s := &Stack{
		cfg:           cfg,
		name:          cfg.Name,
		url:           "http://127.0.0.1:" + strconv.Itoa(cfg.Port) + "/",
		adminPassword: password,
	}
	if err := s.start(ctx); err != nil {
		err = fmt.Errorf("forgejo stack %s: %w%s", s.name, err, s.diagnostics(ctx))
		tdCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TeardownTimeout)
		defer cancel()
		if tdErr := s.Teardown(tdCtx); tdErr != nil {
			err = errors.Join(err, tdErr)
		}
		return nil, err
	}
	return s, nil
}

// URL returns the stack's canonical Forgejo URL, with a trailing slash.
func (s *Stack) URL() string { return s.url }

// Name returns the stack's name: its container-name prefix and the
// value of StackLabel on its containers.
func (s *Stack) Name() string { return s.name }

func (s *Stack) start(ctx context.Context) error {
	logf := s.cfg.Logf
	rootless, err := s.cfg.Engine.RootlessDocker(ctx)
	if err != nil {
		return fmt.Errorf("startup: checking the container engine: %w", err)
	}
	if rootless {
		return errors.New("startup: rootless Docker is not supported: host networking there uses the daemon's " +
			"network namespace, so Forgejo would not be reachable at the canonical loopback URL from the test process; " +
			"use rootful Docker or Podman")
	}
	for _, ref := range []string{ForgejoImage, RunnerImage} {
		logf("forgejo-dev: pulling %s", ref)
		if err := s.cfg.Engine.PullImage(ctx, ref); err != nil {
			return fmt.Errorf("startup: %w", err)
		}
	}

	secretKey, err := randomHex(32)
	if err != nil {
		return err
	}
	if err := s.run(ctx, "forgejo", s.forgejoSpec(secretKey)); err != nil {
		return fmt.Errorf("startup: %w", err)
	}
	logf("forgejo-dev: waiting for Forgejo at %s", s.url)
	if err := s.awaitHealthy(ctx); err != nil {
		return fmt.Errorf("readiness: %w", err)
	}
	forgejoID := s.containerID("forgejo")
	if _, err := s.exec(ctx, forgejoID, "forgejo", "admin", "user", "create", "--admin",
		"--username", adminUser, "--password", s.adminPassword, "--email", adminEmail,
		"--must-change-password=false"); err != nil {
		return fmt.Errorf("startup: creating the admin user: %w", err)
	}
	token, err := s.exec(ctx, forgejoID, "forgejo", "actions", "generate-runner-token")
	if err != nil {
		return fmt.Errorf("startup: generating a runner registration token: %w", err)
	}
	token = lastLine(token)
	if token == "" {
		return errors.New("startup: generating a runner registration token: no token in the command output")
	}
	if err := s.run(ctx, "runner", s.runnerSpec(token)); err != nil {
		return fmt.Errorf("startup: %w", err)
	}
	logf("forgejo-dev: waiting for an Actions job to reach %s from a job container", s.url)
	if err := s.awaitJob(ctx); err != nil {
		return fmt.Errorf("readiness: %w", err)
	}
	version := s.serverVersion(ctx)
	logf("forgejo-dev: stack %s ready at %s: Forgejo %s (%s, reports %q), Forgejo Runner %s (%s), runner capacity %d",
		s.name, s.url, ForgejoVersion, ForgejoImage, version, RunnerVersion, RunnerImage, RunnerCapacity)
	return nil
}

// forgejoSpec configures the Forgejo server: SQLite, installed and
// locked, Actions enabled, self-registration and SSH disabled, listening
// on the canonical URL's loopback port.
func (s *Stack) forgejoSpec(secretKey string) ContainerSpec {
	port := strconv.Itoa(s.cfg.Port)
	return ContainerSpec{
		Image:  ForgejoImage,
		Labels: map[string]string{StackLabel: s.name},
		Env: []string{
			"FORGEJO__database__DB_TYPE=sqlite3",
			"FORGEJO__server__PROTOCOL=http",
			"FORGEJO__server__HTTP_ADDR=127.0.0.1",
			"FORGEJO__server__HTTP_PORT=" + port,
			"FORGEJO__server__ROOT_URL=" + s.url,
			"FORGEJO__server__DISABLE_SSH=true",
			"FORGEJO__server__START_SSH_SERVER=false",
			"FORGEJO__server__OFFLINE_MODE=true",
			"FORGEJO__security__INSTALL_LOCK=true",
			"FORGEJO__security__SECRET_KEY=" + secretKey,
			"FORGEJO__service__DISABLE_REGISTRATION=true",
			"FORGEJO__actions__ENABLED=true",
		},
		NetworkMode: "host",
	}
}

// runnerScript registers the runner, then runs its daemon. Values come
// from the container environment so nothing is interpolated into the
// script itself.
const runnerScript = `set -eu
printf '%s\n' "$FULLSEND_RUNNER_CONFIG" > /data/config.yml
forgejo-runner register --no-interactive --config /data/config.yml \
  --instance "$FULLSEND_FORGEJO_URL" --token "$FULLSEND_RUNNER_TOKEN" \
  --name "$FULLSEND_RUNNER_NAME" --labels "$FULLSEND_RUNNER_LABELS"
exec forgejo-runner daemon --config /data/config.yml`

// runnerSpec configures the runner: it reaches the engine through the
// mounted host socket and runs job containers on the host network,
// labelled with the stack's name so teardown finds them. The socket is
// not mounted into job containers. It runs as root so it can use the
// mounted socket under rootful Docker; under rootless Podman, root is
// the invoking user.
func (s *Stack) runnerSpec(token string) ContainerSpec {
	return ContainerSpec{
		Image:  RunnerImage,
		Labels: map[string]string{StackLabel: s.name},
		Env: []string{
			"DOCKER_HOST=unix:///var/run/docker.sock",
			"FULLSEND_RUNNER_CONFIG=" + s.runnerConfig(),
			"FULLSEND_FORGEJO_URL=" + s.url,
			"FULLSEND_RUNNER_TOKEN=" + token,
			"FULLSEND_RUNNER_NAME=" + s.name,
			"FULLSEND_RUNNER_LABELS=" + strings.Join(runnerLabels, ","),
		},
		Entrypoint:  []string{"/bin/sh", "-c"},
		Cmd:         []string{runnerScript},
		User:        "0:0",
		WorkingDir:  "/data",
		NetworkMode: "host",
		Binds:       []string{s.cfg.SocketPath + ":/var/run/docker.sock"},
		SecurityOpt: []string{"label=disable"},
	}
}

// runnerConfig is the runner's config.yml.
func (s *Stack) runnerConfig() string {
	return fmt.Sprintf(`log:
  level: info
runner:
  file: /data/.runner
  capacity: %d
  timeout: 1h
  fetch_interval: 2s
container:
  network: host
  options: "--label %s=%s"
  docker_host: "-"
`, RunnerCapacity, StackLabel, s.name)
}

// run creates and starts a container of the stack. It is recorded
// before it starts, so teardown removes it even if starting fails.
func (s *Stack) run(ctx context.Context, role string, spec ContainerSpec) error {
	name := s.name + "-" + role
	s.cfg.Logf("forgejo-dev: starting %s container %s", role, name)
	id, err := s.cfg.Engine.CreateContainer(ctx, name, spec)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.containers = append(s.containers, startedContainer{role: role, id: id})
	s.mu.Unlock()
	return s.cfg.Engine.StartContainer(ctx, id)
}

func (s *Stack) containerID(role string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.containers {
		if c.role == role {
			return c.id
		}
	}
	return ""
}

// exec runs cmd in container id and returns its stdout; a non-zero exit
// is an error carrying stderr.
func (s *Stack) exec(ctx context.Context, id string, cmd ...string) (string, error) {
	res, err := s.cfg.Engine.Exec(ctx, id, cmd)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s exited %d: %s", execLabel(cmd), res.ExitCode, strings.TrimSpace(res.Stderr+"\n"+res.Stdout))
	}
	return res.Stdout, nil
}

// checkRunning fails when a started stack container is no longer
// running, so a crashed component ends the wait at once.
func (s *Stack) checkRunning(ctx context.Context) error {
	s.mu.Lock()
	containers := append([]startedContainer(nil), s.containers...)
	s.mu.Unlock()
	for _, c := range containers {
		state, err := s.cfg.Engine.InspectContainer(ctx, c.id)
		if err != nil {
			return err
		}
		if !state.Running {
			return fmt.Errorf("%s container exited (status %q, exit code %d)", c.role, state.Status, state.ExitCode)
		}
	}
	return nil
}

// awaitHealthy polls Forgejo's health endpoint at the canonical URL.
func (s *Stack) awaitHealthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.HealthTimeout)
	defer cancel()
	last := errors.New("not checked yet")
	timedOut := func() error {
		return fmt.Errorf("forgejo not healthy at %sapi/healthz within %s: %w", s.url, s.cfg.HealthTimeout, last)
	}
	for {
		if err := s.checkRunning(ctx); err != nil {
			if ctx.Err() != nil {
				return timedOut()
			}
			return err
		}
		status, _, err := s.api(ctx, http.MethodGet, "api/healthz", nil, false)
		if err == nil && status == http.StatusOK {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("HTTP %d", status)
		}
		// Keep the last real failure rather than the deadline itself.
		if ctx.Err() == nil {
			last = err
		}
		if sleepCtx(ctx, s.cfg.PollInterval) != nil {
			return timedOut()
		}
	}
}

// awaitJob creates the readiness repository with a workflow whose job
// fetches the canonical URL, then waits for the run to succeed. Success
// shows the runner registered and picks up jobs at the canonical URL,
// and that the URL is reachable from a job container.
func (s *Stack) awaitJob(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.JobTimeout)
	defer cancel()
	repoPath := "api/v1/repos/" + adminUser + "/" + readinessRepo
	if err := s.apiExpect(ctx, http.MethodPost, "api/v1/user/repos", map[string]any{
		"name": readinessRepo, "auto_init": true, "default_branch": defaultBranch, "private": true,
	}, http.StatusCreated); err != nil {
		return fmt.Errorf("creating the readiness repository: %w", err)
	}
	if err := s.apiExpect(ctx, http.MethodPatch, repoPath, map[string]any{"has_actions": true}, http.StatusOK); err != nil {
		return fmt.Errorf("enabling Actions on the readiness repository: %w", err)
	}
	workflow := fmt.Sprintf(`name: readiness
on: push
jobs:
  canonical-url:
    runs-on: docker
    steps:
      - name: Reach the canonical Forgejo URL from the job container
        run: curl -fsS --max-time 10 -o /dev/null %sapi/healthz
`, s.url)
	if err := s.apiExpect(ctx, http.MethodPost, repoPath+"/contents/"+readinessFile, map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(workflow)),
		"message": "Add stack readiness workflow",
		"branch":  defaultBranch,
	}, http.StatusCreated); err != nil {
		return fmt.Errorf("adding the readiness workflow: %w", err)
	}
	last := "not checked yet"
	timedOut := func() error {
		return fmt.Errorf("readiness job did not succeed within %s (last: %s)", s.cfg.JobTimeout, last)
	}
	for {
		if err := s.checkRunning(ctx); err != nil {
			if ctx.Err() != nil {
				return timedOut()
			}
			return err
		}
		run, err := s.latestRun(ctx, repoPath)
		switch {
		case err != nil:
			// Keep the last real failure rather than the deadline itself.
			if ctx.Err() == nil {
				last = err.Error()
			}
		case run == nil:
			last = "no workflow run yet"
		default:
			switch run.Status {
			case "success":
				return nil
			case "failure", "cancelled", "skipped":
				return fmt.Errorf("readiness job %s ended %s: the runner or a job container could not use %s", run.HTMLURL, run.Status, s.url)
			}
			last = "workflow run status " + run.Status
		}
		if sleepCtx(ctx, s.cfg.PollInterval) != nil {
			return timedOut()
		}
	}
}

type workflowRun struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	HTMLURL string `json:"html_url"`
}

// latestRun returns the readiness repository's newest workflow run, or
// nil when there is none yet.
func (s *Stack) latestRun(ctx context.Context, repoPath string) (*workflowRun, error) {
	status, body, err := s.api(ctx, http.MethodGet, repoPath+"/actions/runs", nil, true)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("listing workflow runs: HTTP %d: %s", status, truncate(body))
	}
	var out struct {
		Runs []workflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("listing workflow runs: %w", err)
	}
	var latest *workflowRun
	for i := range out.Runs {
		if latest == nil || out.Runs[i].ID > latest.ID {
			latest = &out.Runs[i]
		}
	}
	return latest, nil
}

// serverVersion returns the version Forgejo reports, or a note on why
// it could not be read; it is informational only.
func (s *Stack) serverVersion(ctx context.Context) string {
	status, body, err := s.api(ctx, http.MethodGet, "api/v1/version", nil, false)
	if err != nil || status != http.StatusOK {
		return "unknown"
	}
	var out struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &out) != nil || out.Version == "" {
		return "unknown"
	}
	return out.Version
}

// api calls the Forgejo API at path (relative to the canonical URL),
// authenticating as the admin user when auth is set.
func (s *Stack) api(ctx context.Context, method, path string, body any, auth bool) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.url+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.SetBasicAuth(adminUser, s.adminPassword)
	}
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}

// apiExpect is an authenticated api call that must return want.
func (s *Stack) apiExpect(ctx context.Context, method, path string, body any, want int) error {
	status, data, err := s.api(ctx, method, path, body, true)
	if err != nil {
		return err
	}
	if status != want {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, status, truncate(data))
	}
	return nil
}

// diagnostics returns the tail of each started container's log,
// formatted for appending to a startup error.
func (s *Stack) diagnostics(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diagnoseTimeout)
	defer cancel()
	s.mu.Lock()
	containers := append([]startedContainer(nil), s.containers...)
	s.mu.Unlock()
	var b strings.Builder
	for _, c := range containers {
		logs, err := s.cfg.Engine.ContainerLogs(ctx, c.id, logTailLines)
		if err != nil {
			fmt.Fprintf(&b, "\n--- %s container log unavailable: %v", c.role, err)
			continue
		}
		fmt.Fprintf(&b, "\n--- %s container log (last %d lines) ---\n%s", c.role, logTailLines, strings.TrimRight(logs, "\n"))
	}
	return b.String()
}

// Teardown stops the runner, then removes every container of the stack:
// leftover job containers, the runner, and Forgejo, with their anonymous
// volumes. The stack keeps no other engine resources. It returns every
// removal error, and is safe to call more than once; later calls do
// nothing.
func (s *Stack) Teardown(ctx context.Context) error {
	s.mu.Lock()
	if s.tornDown {
		s.mu.Unlock()
		return nil
	}
	s.tornDown = true
	containers := append([]startedContainer(nil), s.containers...)
	s.mu.Unlock()

	var errs []error
	// removed holds the containers already force-removed, so the
	// ordered removal below skips them.
	removed := map[string]bool{}
	for _, c := range containers {
		if c.role == "runner" {
			if err := s.cfg.Engine.StopContainer(ctx, c.id, runnerStopPeriod); err != nil {
				errs = append(errs, err)
				// The runner may still be alive and able to start job
				// containers. Force-remove it before the job-container
				// snapshot so none appears after the listing.
				removed[c.id] = true
				if err := s.cfg.Engine.RemoveContainer(ctx, c.id); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	known := make(map[string]bool, len(containers))
	for _, c := range containers {
		known[c.id] = true
	}
	labelled, err := s.cfg.Engine.ListContainers(ctx, StackLabel, s.name)
	if err != nil {
		errs = append(errs, err)
	}
	// Job containers first, then the stack's own in reverse start order.
	var order []string
	for _, id := range labelled {
		if !known[id] {
			order = append(order, id)
		}
	}
	for i := len(containers) - 1; i >= 0; i-- {
		order = append(order, containers[i].id)
	}
	for _, id := range order {
		if removed[id] {
			continue
		}
		if err := s.cfg.Engine.RemoveContainer(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("forgejo stack %s: teardown: %w", s.name, err)
	}
	s.cfg.Logf("forgejo-dev: stack %s torn down", s.name)
	return nil
}

// freeLoopbackPort returns a TCP port that was free on 127.0.0.1.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("forgejo stack: generating random value: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// lastLine returns the last non-empty line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func truncate(b []byte) string {
	const limit = 512
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
