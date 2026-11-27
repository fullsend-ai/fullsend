package forgejostack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEngine is an in-memory Engine.
type fakeEngine struct {
	mu         sync.Mutex
	pulled     []string
	created    map[string]ContainerSpec // name -> spec
	ids        map[string]string        // id -> name
	running    map[string]bool          // id -> running
	execs      [][]string
	stopped    []string
	removed    []string
	jobLabeled []string // extra labelled containers (job containers)

	pullErr   error
	createErr map[string]error // by role suffix
	execFn    func(cmd []string) (ExecResult, error)
	listErr   error
	removeErr error
	stopErr   error
	logsErr   error
	// rootless and rootlessErr are RootlessDocker's answer.
	rootless    bool
	rootlessErr error
	// onRemove, when set, runs (under the engine lock) as each
	// container is removed.
	onRemove func(id string)
	// exitAfter marks a role's container as exited after that many
	// inspects.
	exitAfter map[string]int
	inspects  map[string]int
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		created:   map[string]ContainerSpec{},
		ids:       map[string]string{},
		running:   map[string]bool{},
		createErr: map[string]error{},
		exitAfter: map[string]int{},
		inspects:  map[string]int{},
		execFn: func(cmd []string) (ExecResult, error) {
			if slices.Contains(cmd, "generate-runner-token") {
				return ExecResult{Stdout: "2024/01/01 info: loading config\nREGTOKEN123\n"}, nil
			}
			return ExecResult{Stdout: "New user 'fullsend-admin' has been successfully created!\n"}, nil
		},
	}
}

func role(name string) string { return name[strings.LastIndex(name, "-")+1:] }

func (f *fakeEngine) RootlessDocker(context.Context) (bool, error) {
	return f.rootless, f.rootlessErr
}

func (f *fakeEngine) PullImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	return f.pullErr
}

func (f *fakeEngine) CreateContainer(_ context.Context, name string, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.createErr[role(name)]; err != nil {
		return "", err
	}
	id := "id-" + name
	f.created[name] = spec
	f.ids[id] = name
	return id, nil
}

func (f *fakeEngine) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[id] = true
	return nil
}

func (f *fakeEngine) InspectContainer(_ context.Context, id string) (ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := role(f.ids[id])
	f.inspects[r]++
	if n, ok := f.exitAfter[r]; ok && f.inspects[r] > n {
		f.running[id] = false
		return ContainerState{Status: "exited", ExitCode: 1}, nil
	}
	return ContainerState{Status: "running", Running: f.running[id]}, nil
}

func (f *fakeEngine) Exec(_ context.Context, _ string, cmd []string) (ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	fn := f.execFn
	f.mu.Unlock()
	return fn(cmd)
}

func (f *fakeEngine) ContainerLogs(_ context.Context, id string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logsErr != nil {
		return "", f.logsErr
	}
	return "log of " + f.ids[id] + "\n", nil
}

func (f *fakeEngine) StopContainer(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return f.stopErr
}

func (f *fakeEngine) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	if f.onRemove != nil {
		f.onRemove(id)
	}
	return f.removeErr
}

func (f *fakeEngine) ListContainers(_ context.Context, key, value string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var ids []string
	for id, name := range f.ids {
		if f.created[name].Labels[key] == value {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return append(f.jobLabeled, ids...), nil
}

// fakeForgejo is a Forgejo API stand-in listening on 127.0.0.1.
type fakeForgejo struct {
	t           *testing.T
	healthAfter int32 // healthz fails this many times first
	healthCalls atomic.Int32
	runStatuses []string // successive run statuses; "" means no run yet
	runCalls    atomic.Int32
	workflow    atomic.Value
	failCreate  bool
	password    atomic.Value
	// seedFail selects a failure of the agent-definition seeding: "org",
	// "repo", "commit", "garbled", or "no-sha".
	seedFail string
	// seeded holds the fixture files committed to the agents repository.
	seeded atomic.Value
}

// fakeAgentsCommit is the commit the fake reports for the seeded fixture.
const fakeAgentsCommit = "89abcdef0123456789abcdef0123456789abcdef"

func (f *fakeForgejo) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if f.healthCalls.Add(1) <= f.healthAfter {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"17.0.1+gitea-1.22.0"}`))
	})
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		if !ok || user != adminUser || pass == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		f.password.Store(pass)
		return true
	}
	mux.HandleFunc("POST /api/v1/user/repos", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if f.failCreate {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"database is locked"}`))
			return
		}
		var body map[string]any
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal(f.t, readinessRepo, body["name"])
		assert.Equal(f.t, true, body["auto_init"])
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/v1/orgs", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body map[string]any
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal(f.t, AgentsOwner, body["username"])
		assert.Equal(f.t, "public", body["visibility"])
		if f.seedFail == "org" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"user already exists"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/v1/orgs/"+AgentsOwner+"/repos", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body map[string]any
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal(f.t, AgentsRepo, body["name"])
		assert.Equal(f.t, true, body["auto_init"])
		assert.Equal(f.t, false, body["private"])
		if f.seedFail == "repo" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/v1/repos/"+AgentsOwner+"/"+AgentsRepo+"/contents", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body struct {
			Branch string
			Files  []struct{ Operation, Path, Content string }
		}
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal(f.t, defaultBranch, body.Branch)
		files := map[string]string{}
		for _, file := range body.Files {
			assert.Equal(f.t, "create", file.Operation)
			data, err := base64.StdEncoding.DecodeString(file.Content)
			assert.NoError(f.t, err)
			files[file.Path] = string(data)
		}
		f.seeded.Store(files)
		switch f.seedFail {
		case "commit":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"database is locked"}`))
		case "garbled":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"commit":`))
		case "no-sha":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"commit":{"sha":"main"}}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"commit":{"sha":"` + fakeAgentsCommit + `"}}`))
		}
	})
	repo := "/api/v1/repos/" + adminUser + "/" + readinessRepo
	mux.HandleFunc("PATCH "+repo, func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body map[string]any
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal(f.t, true, body["has_actions"])
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST "+repo+"/contents/"+readinessFile, func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body struct{ Content, Branch string }
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		data, err := base64.StdEncoding.DecodeString(body.Content)
		if !assert.NoError(f.t, err) {
			return
		}
		f.workflow.Store(string(data))
		assert.Equal(f.t, defaultBranch, body.Branch)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET "+repo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		i := int(f.runCalls.Add(1)) - 1
		status := f.runStatuses[min(i, len(f.runStatuses)-1)]
		switch status {
		case "":
			_, _ = w.Write([]byte(`{"workflow_runs":[],"total_count":0}`))
		case "http-500":
			w.WriteHeader(http.StatusInternalServerError)
		case "garbled":
			_, _ = w.Write([]byte(`{"workflow_runs":`))
		default:
			fmt.Fprintf(w, `{"workflow_runs":[{"id":1,"status":"waiting"},{"id":2,"status":%q,"html_url":"http://forgejo/run/2"}],"total_count":2}`, status)
		}
	})
	return mux
}

// forgejoOpts configures a fakeForgejo in table tests.
type forgejoOpts struct {
	healthAfter int32
	runStatuses []string
	failCreate  bool
	seedFail    string
}

// startFakeForgejo serves f on 127.0.0.1 and returns its port and client.
// The server's client has its own transport, so another parallel test's
// httptest.Server.Close cannot interrupt Start's requests via the shared
// http.DefaultTransport.
func startFakeForgejo(t *testing.T, f *fakeForgejo) (int, *http.Client) {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	client := srv.Client()
	client.Timeout = 30 * time.Second
	return port, client
}

func testConfig(engine Engine, port int, client *http.Client) Config {
	return Config{
		Engine:        engine,
		HTTPClient:    client,
		SocketPath:    "/run/user/1000/podman/podman.sock",
		Port:          port,
		Name:          "stack-test",
		PollInterval:  time.Millisecond,
		HealthTimeout: 2 * time.Second,
		JobTimeout:    2 * time.Second,
	}
}

func TestStart_ReadyAndTeardown(t *testing.T) {
	t.Parallel()
	ff := &fakeForgejo{healthAfter: 2, runStatuses: []string{"", "http-500", "running", "success"}}
	port, client := startFakeForgejo(t, ff)
	engine := newFakeEngine()
	var logs []string
	var logMu sync.Mutex
	cfg := testConfig(engine, port, client)
	cfg.Logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	s, err := Start(context.Background(), cfg)
	require.NoError(t, err)
	wantURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	assert.Equal(t, wantURL, s.URL())
	assert.Equal(t, "stack-test", s.Name())

	assert.Equal(t, []string{ForgejoImage, RunnerImage}, engine.pulled)

	forgejo := engine.created["stack-test-forgejo"]
	assert.Equal(t, ForgejoImage, forgejo.Image)
	assert.Equal(t, "host", forgejo.NetworkMode)
	assert.Equal(t, map[string]string{StackLabel: "stack-test"}, forgejo.Labels)
	assert.Contains(t, forgejo.Env, "FORGEJO__server__ROOT_URL="+wantURL)
	assert.Contains(t, forgejo.Env, "FORGEJO__server__HTTP_ADDR=127.0.0.1")
	assert.Contains(t, forgejo.Env, "FORGEJO__server__HTTP_PORT="+strconv.Itoa(port))
	assert.Contains(t, forgejo.Env, "FORGEJO__actions__ENABLED=true")
	assert.Contains(t, forgejo.Env, "FORGEJO__security__INSTALL_LOCK=true")

	runner := engine.created["stack-test-runner"]
	assert.Equal(t, RunnerImage, runner.Image)
	assert.Equal(t, "host", runner.NetworkMode)
	assert.Equal(t, []string{"/run/user/1000/podman/podman.sock:/var/run/docker.sock"}, runner.Binds)
	assert.Equal(t, map[string]string{StackLabel: "stack-test"}, runner.Labels)
	assert.Contains(t, runner.Env, "FULLSEND_RUNNER_TOKEN=REGTOKEN123", "the token is the last line of the command output")
	assert.Contains(t, runner.Env, "FULLSEND_FORGEJO_URL="+wantURL)
	assert.Contains(t, runner.Env, "FULLSEND_RUNNER_LABELS="+strings.Join(runnerLabels, ","))
	var config string
	for _, e := range runner.Env {
		if v, ok := strings.CutPrefix(e, "FULLSEND_RUNNER_CONFIG="); ok {
			config = v
		}
	}
	assert.Contains(t, config, "capacity: 2\n")
	assert.Contains(t, config, "network: host\n")
	assert.Contains(t, config, `options: "--label `+StackLabel+`=stack-test"`)
	assert.Contains(t, config, `docker_host: "-"`)

	require.Len(t, engine.execs, 2)
	assert.Equal(t, []string{"forgejo", "admin", "user", "create", "--admin", "--username", adminUser}, engine.execs[0][:7])
	assert.Contains(t, engine.execs[0], ff.password.Load(), "the API is called with the password the admin was created with")
	assert.Equal(t, []string{"forgejo", "actions", "generate-runner-token"}, engine.execs[1])

	assert.Equal(t, fakeAgentsCommit, s.AgentsRef())
	assert.Equal(t, AgentFixtureFiles(), ff.seeded.Load(), "the whole fixture is committed")
	assert.Equal(t, map[string]string{
		"FULLSEND_FORGEJO_URL":         strings.TrimSuffix(wantURL, "/"),
		"FULLSEND_FORGEJO_AGENTS_REPO": "fullsend-ai/agents",
		"FULLSEND_FORGEJO_AGENTS_REF":  fakeAgentsCommit,
	}, s.AgentSourceEnv())

	workflow, _ := ff.workflow.Load().(string)
	assert.Contains(t, workflow, "runs-on: docker")
	assert.Contains(t, workflow, "curl -fsS --max-time 10 -o /dev/null "+wantURL+"api/healthz")

	logMu.Lock()
	ready := logs[len(logs)-1]
	logMu.Unlock()
	assert.Contains(t, ready, "Forgejo "+ForgejoVersion)
	assert.Contains(t, ready, "Forgejo Runner "+RunnerVersion)
	assert.Contains(t, ready, `reports "17.0.1+gitea-1.22.0"`)

	engine.jobLabeled = []string{"id-job-1"}
	require.NoError(t, s.Teardown(context.Background()))
	assert.Equal(t, []string{"id-stack-test-runner"}, engine.stopped)
	assert.Equal(t, []string{"id-job-1", "id-stack-test-runner", "id-stack-test-forgejo"}, engine.removed,
		"job containers go first, then the stack's own in reverse start order")

	require.NoError(t, s.Teardown(context.Background()), "a second teardown is a no-op")
	assert.Len(t, engine.removed, 3)
}

func TestStart_Validation(t *testing.T) {
	t.Parallel()
	_, err := Start(context.Background(), Config{SocketPath: "/s"})
	require.ErrorContains(t, err, "no container engine")
	_, err = Start(context.Background(), Config{Engine: newFakeEngine()})
	require.ErrorContains(t, err, "no engine socket path")
}

func TestStart_DefaultsNameAndPort(t *testing.T) {
	t.Parallel()
	engine := newFakeEngine()
	engine.pullErr = errors.New("registry unreachable")
	_, err := Start(context.Background(), Config{Engine: engine, SocketPath: "/s"})
	require.ErrorContains(t, err, "forgejo stack fullsend-forgejo-dev-")
	require.ErrorContains(t, err, "startup: registry unreachable")
	assert.Empty(t, engine.created)
}

func TestStart_RejectsRootlessDocker(t *testing.T) {
	t.Parallel()
	engine := newFakeEngine()
	engine.rootless = true
	_, err := Start(context.Background(), Config{Engine: engine, SocketPath: "/s"})
	require.ErrorContains(t, err, "startup: rootless Docker is not supported")
	assert.Empty(t, engine.pulled)
	assert.Empty(t, engine.created)
}

func TestStart_EngineCheckFails(t *testing.T) {
	t.Parallel()
	engine := newFakeEngine()
	engine.rootlessErr = errors.New("socket closed")
	_, err := Start(context.Background(), Config{Engine: engine, SocketPath: "/s"})
	require.ErrorContains(t, err, "startup: checking the container engine: socket closed")
	assert.Empty(t, engine.pulled)
}

func TestStart_Failures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		forgejo     forgejoOpts
		setup       func(*fakeEngine)
		wantErr     []string
		wantRemoved []string
	}{
		{
			name:        "forgejo container fails to create",
			setup:       func(e *fakeEngine) { e.createErr["forgejo"] = errors.New("name in use") },
			wantErr:     []string{"startup: name in use"},
			wantRemoved: nil,
		},
		{
			name:        "forgejo exits before it is healthy",
			forgejo:     forgejoOpts{healthAfter: 1000},
			setup:       func(e *fakeEngine) { e.exitAfter["forgejo"] = 1 },
			wantErr:     []string{"readiness: forgejo container exited", "--- forgejo container log (last 50 lines) ---\nlog of stack-test-forgejo"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "forgejo never healthy",
			forgejo:     forgejoOpts{healthAfter: 1 << 30},
			wantErr:     []string{"readiness: forgejo not healthy at", "HTTP 503"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name: "admin user creation fails",
			setup: func(e *fakeEngine) {
				e.execFn = func([]string) (ExecResult, error) {
					return ExecResult{ExitCode: 1, Stderr: "user already exists"}, nil
				}
			},
			wantErr:     []string{"startup: creating the admin user: forgejo admin exited 1: user already exists"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name: "exec transport fails",
			setup: func(e *fakeEngine) {
				e.execFn = func([]string) (ExecResult, error) { return ExecResult{}, errors.New("socket closed") }
			},
			wantErr:     []string{"creating the admin user: socket closed"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name: "runner token missing",
			setup: func(e *fakeEngine) {
				e.execFn = func([]string) (ExecResult, error) { return ExecResult{Stdout: "\n"}, nil }
			},
			wantErr:     []string{"no token in the command output"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name: "runner token generation fails",
			setup: func(e *fakeEngine) {
				e.execFn = func(cmd []string) (ExecResult, error) {
					if slices.Contains(cmd, "generate-runner-token") {
						return ExecResult{ExitCode: 1, Stderr: "actions disabled"}, nil
					}
					return ExecResult{}, nil
				}
			},
			wantErr:     []string{"generating a runner registration token: forgejo actions exited 1: actions disabled"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "agents organization cannot be created",
			forgejo:     forgejoOpts{seedFail: "org"},
			wantErr:     []string{"startup: seeding the agent-definition repository: creating organization fullsend-ai: POST api/v1/orgs: HTTP 422"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "agents repository cannot be created",
			forgejo:     forgejoOpts{seedFail: "repo"},
			wantErr:     []string{"creating repository fullsend-ai/agents: POST api/v1/orgs/fullsend-ai/repos: HTTP 409"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "agents fixture cannot be committed",
			forgejo:     forgejoOpts{seedFail: "commit"},
			wantErr:     []string{"committing the fixture: HTTP 500: {\"message\":\"database is locked\"}"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "agents commit response unreadable",
			forgejo:     forgejoOpts{seedFail: "garbled"},
			wantErr:     []string{"committing the fixture: unexpected end of JSON input"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "agents commit response has no SHA",
			forgejo:     forgejoOpts{seedFail: "no-sha"},
			wantErr:     []string{"committing the fixture: response has no commit SHA"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "runner container fails to create",
			setup:       func(e *fakeEngine) { e.createErr["runner"] = errors.New("no such image") },
			wantErr:     []string{"startup: no such image"},
			wantRemoved: []string{"id-stack-test-forgejo"},
		},
		{
			name:        "runner exits",
			forgejo:     forgejoOpts{runStatuses: []string{""}},
			setup:       func(e *fakeEngine) { e.exitAfter["runner"] = 0 },
			wantErr:     []string{"readiness: runner container exited", "--- runner container log (last 50 lines) ---\nlog of stack-test-runner"},
			wantRemoved: []string{"id-stack-test-runner", "id-stack-test-forgejo"},
		},
		{
			name:        "readiness repository cannot be created",
			forgejo:     forgejoOpts{failCreate: true},
			wantErr:     []string{"creating the readiness repository: POST api/v1/user/repos: HTTP 500: {\"message\":\"database is locked\"}"},
			wantRemoved: []string{"id-stack-test-runner", "id-stack-test-forgejo"},
		},
		{
			name:        "readiness job fails",
			forgejo:     forgejoOpts{runStatuses: []string{"running", "failure"}},
			wantErr:     []string{"readiness: readiness job http://forgejo/run/2 ended failure: the runner or a job container could not use http://127.0.0.1:"},
			wantRemoved: []string{"id-stack-test-runner", "id-stack-test-forgejo"},
		},
		{
			name:        "readiness job never runs",
			forgejo:     forgejoOpts{runStatuses: []string{""}},
			wantErr:     []string{"readiness job did not succeed within 2s (last: no workflow run yet)"},
			wantRemoved: []string{"id-stack-test-runner", "id-stack-test-forgejo"},
		},
		{
			name:        "runs API unreadable",
			forgejo:     forgejoOpts{runStatuses: []string{"garbled"}},
			wantErr:     []string{"(last: listing workflow runs: unexpected end of JSON input)"},
			wantRemoved: []string{"id-stack-test-runner", "id-stack-test-forgejo"},
		},
		{
			name:    "startup failure with teardown failure",
			forgejo: forgejoOpts{runStatuses: []string{"cancelled"}},
			setup: func(e *fakeEngine) {
				e.removeErr = errors.New("device busy")
				e.logsErr = errors.New("logs gone")
			},
			wantErr:     []string{"ended cancelled", "--- forgejo container log unavailable: logs gone", "teardown: removing", "device busy"},
			wantRemoved: []string{"id-stack-test-runner", "id-stack-test-forgejo"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ff := &fakeForgejo{healthAfter: tc.forgejo.healthAfter, runStatuses: tc.forgejo.runStatuses, failCreate: tc.forgejo.failCreate, seedFail: tc.forgejo.seedFail}
			if ff.runStatuses == nil {
				ff.runStatuses = []string{"success"}
			}
			port, client := startFakeForgejo(t, ff)
			engine := newFakeEngine()
			if tc.setup != nil {
				tc.setup(engine)
			}
			if engine.removeErr != nil {
				// Exercise the teardown error path with a wrapped error.
				engine.removeErr = fmt.Errorf("removing: %w", engine.removeErr)
			}
			s, err := Start(context.Background(), testConfig(engine, port, client))
			require.Error(t, err)
			assert.Nil(t, s)
			for _, want := range tc.wantErr {
				assert.ErrorContains(t, err, want)
			}
			assert.Equal(t, tc.wantRemoved, engine.removed, "a failed start removes every container it started")
		})
	}
}

func TestTeardown_ReportsEveryError(t *testing.T) {
	t.Parallel()
	ff := &fakeForgejo{runStatuses: []string{"success"}}
	port, client := startFakeForgejo(t, ff)
	engine := newFakeEngine()
	s, err := Start(context.Background(), testConfig(engine, port, client))
	require.NoError(t, err)

	engine.stopErr = errors.New("stop timed out")
	engine.listErr = errors.New("list refused")
	engine.removeErr = errors.New("remove refused")
	err = s.Teardown(context.Background())
	require.ErrorContains(t, err, "forgejo stack stack-test: teardown")
	assert.ErrorContains(t, err, "stop timed out")
	assert.ErrorContains(t, err, "list refused")
	assert.ErrorContains(t, err, "remove refused")
	assert.Equal(t, []string{"id-stack-test-runner", "id-stack-test-forgejo"}, engine.removed,
		"a failed list still removes the containers the stack knows")
}

func TestTeardown_StopFailureRemovesRunnerBeforeListing(t *testing.T) {
	t.Parallel()
	ff := &fakeForgejo{runStatuses: []string{"success"}}
	port, client := startFakeForgejo(t, ff)
	engine := newFakeEngine()
	s, err := Start(context.Background(), testConfig(engine, port, client))
	require.NoError(t, err)

	// A runner that survives a failed stop can start one more job
	// container until it is removed.
	engine.stopErr = errors.New("stop timed out")
	engine.onRemove = func(id string) {
		if id == "id-stack-test-runner" {
			engine.jobLabeled = append(engine.jobLabeled, "id-late-job")
		}
	}
	err = s.Teardown(context.Background())
	require.ErrorContains(t, err, "stop timed out")
	assert.Equal(t, []string{"id-stack-test-runner", "id-late-job", "id-stack-test-forgejo"}, engine.removed,
		"the runner goes first, so its last job container is listed and removed")
}

func TestLastLineAndTruncate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "tok", lastLine("a\nb\n tok \n\n"))
	assert.Empty(t, lastLine(""))
	long := strings.Repeat("x", 600)
	assert.Len(t, []rune(truncate([]byte(long))), 513)
	assert.Equal(t, "short", truncate([]byte(" short\n")))
}

func TestServerVersion_Unknown(t *testing.T) {
	t.Parallel()
	var handlerCalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled.Store(true)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	s := &Stack{url: srv.URL + "/", cfg: Config{HTTPClient: srv.Client()}}
	assert.Equal(t, "unknown", s.serverVersion(context.Background()))
	assert.True(t, handlerCalled.Load(), "handler was not called — the request never reached the test server")
	s.url = "http://127.0.0.1:1/"
	assert.Equal(t, "unknown", s.serverVersion(context.Background()))
}
