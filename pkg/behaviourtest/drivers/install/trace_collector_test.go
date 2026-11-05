package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	testTraceA = "0102030405060708090a0b0c0d0e0f10"
	testTraceB = "a1a2a3a4a5a6a7a8a9aaabacadaeafb0"
)

func TestTraceCollectorEnabled(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    bool
		wantErr string
	}{
		{name: "auto off outside actions", env: map[string]string{"BEHAVIOUR_ARTIFACT_DIR": "/tmp/a"}},
		{name: "auto off without artifact dir", env: map[string]string{"GITHUB_ACTIONS": "true"}},
		{name: "auto on in actions with artifact dir", env: map[string]string{"GITHUB_ACTIONS": "true", "BEHAVIOUR_ARTIFACT_DIR": "/tmp/a"}, want: true},
		{name: "forced on", env: map[string]string{envTraceCollector: "TRUE", "BEHAVIOUR_ARTIFACT_DIR": "/tmp/a"}, want: true},
		{name: "forced on needs artifact dir", env: map[string]string{envTraceCollector: "on"}, wantErr: "requires BEHAVIOUR_ARTIFACT_DIR"},
		{name: "forced off", env: map[string]string{envTraceCollector: "0", "GITHUB_ACTIONS": "true", "BEHAVIOUR_ARTIFACT_DIR": "/tmp/a"}},
		{name: "invalid", env: map[string]string{envTraceCollector: "maybe"}, wantErr: "unsupported value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := traceCollectorEnabled(func(k string) string { return tt.env[k] })
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.False(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCollectorConfig_RequiresTokenFromEnv(t *testing.T) {
	assert.Contains(t, collectorConfig, "bearertokenauth:")
	assert.Contains(t, collectorConfig, `token: "${env:`+collectorTokenEnv+`}"`)
	assert.Contains(t, collectorConfig, "authenticator: bearertokenauth")
	assert.Contains(t, collectorConfig, "extensions: [bearertokenauth]")
}

func TestCollectorConfig_RoutesFilesByTraceID(t *testing.T) {
	var cfg struct {
		Processors map[string]struct {
			TraceStatements []struct {
				Context    string   `yaml:"context"`
				Statements []string `yaml:"statements"`
			} `yaml:"trace_statements"`
			Keys []string `yaml:"keys"`
		} `yaml:"processors"`
		Exporters struct {
			File struct {
				Path    string `yaml:"path"`
				GroupBy struct {
					Enabled           bool   `yaml:"enabled"`
					ResourceAttribute string `yaml:"resource_attribute"`
					MaxOpenFiles      int    `yaml:"max_open_files"`
				} `yaml:"group_by"`
			} `yaml:"file"`
		} `yaml:"exporters"`
		Service struct {
			Pipelines map[string]struct {
				Processors []string `yaml:"processors"`
				Exporters  []string `yaml:"exporters"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(collectorConfig), &cfg))

	// The transform copies the span's trace ID into a span attribute.
	tr := cfg.Processors["transform/trace_id"]
	require.Len(t, tr.TraceStatements, 1)
	assert.Equal(t, "span", tr.TraceStatements[0].Context)
	assert.Equal(t, []string{`set(attributes["bt.trace_id"], trace_id.string)`}, tr.TraceStatements[0].Statements)

	// groupbyattrs promotes it to a resource attribute.
	assert.Equal(t, []string{"bt.trace_id"}, cfg.Processors["groupbyattrs/trace_id"].Keys)

	// The file exporter writes one file per value of that attribute.
	file := cfg.Exporters.File
	assert.Equal(t, "/traces/trace-*.jsonl", file.Path)
	assert.Equal(t, 1, strings.Count(file.Path, "*"))
	assert.True(t, file.GroupBy.Enabled)
	assert.Equal(t, "bt.trace_id", file.GroupBy.ResourceAttribute)
	assert.Positive(t, file.GroupBy.MaxOpenFiles)

	// The processors run in order, before the exporter.
	p := cfg.Service.Pipelines["traces"]
	assert.Equal(t, []string{"transform/trace_id", "groupbyattrs/trace_id"}, p.Processors)
	assert.Equal(t, []string{"file"}, p.Exporters)
}

// fakeCollectorEnv emulates podman, the Collector receiver and the
// cloudflared metrics endpoint.
type fakeCollectorEnv struct {
	t *testing.T

	mu    sync.Mutex
	token string // token podman passed to the collector container
	calls [][]string

	receiver *httptest.Server
	metrics  *httptest.Server
	ports    []int

	// knobs
	acceptMissingToken bool
	acceptWrongToken   bool
	tunnelHost         string
	runErr             map[string]error // keyed by container name prefix
}

func newFakeCollectorEnv(t *testing.T) *fakeCollectorEnv {
	f := &fakeCollectorEnv{t: t, tunnelHost: "abc-def.trycloudflare.com", runErr: map[string]error{}}
	f.receiver = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		token := f.token
		missingOK, wrongOK := f.acceptMissingToken, f.acceptWrongToken
		f.mu.Unlock()
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "" && missingOK, auth != "" && auth == "Bearer "+token, auth != "" && wrongOK:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	readyCalls := 0
	f.metrics = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/quicktunnel":
			_, _ = fmt.Fprintf(w, `{"hostname":%q}`, f.tunnelHost)
		case "/ready":
			f.mu.Lock()
			readyCalls++
			n := readyCalls
			f.mu.Unlock()
			if n < 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.receiver.Close)
	t.Cleanup(f.metrics.Close)
	f.ports = []int{serverPort(t, f.receiver), serverPort(t, f.metrics)}
	return f
}

func serverPort(t *testing.T, s *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(s.Listener.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return n
}

func (f *fakeCollectorEnv) freePort() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ports) == 0 {
		return 0, errors.New("no ports left")
	}
	p := f.ports[0]
	f.ports = f.ports[1:]
	return p, nil
}

func (f *fakeCollectorEnv) run(_ context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	if len(args) < 1 {
		return nil, nil
	}
	switch args[0] {
	case "run":
		container := argAfter(args, "--name")
		for prefix, err := range f.runErr {
			if strings.HasPrefix(container, prefix) {
				return []byte("pull failed"), err
			}
		}
		if strings.HasPrefix(container, "bt-otelcol-") {
			for _, kv := range extraEnv {
				if v, ok := strings.CutPrefix(kv, collectorTokenEnv+"="); ok {
					f.token = v
				}
			}
			// Simulate the file exporter writing one file per trace
			// ID; the first span (for the test) quotes the token.
			for i, a := range args {
				if a == "-v" && strings.HasSuffix(args[i+1], ":/traces:Z") {
					dir := strings.TrimSuffix(args[i+1], ":/traces:Z")
					first := `{"resourceSpans":[{"name":"span","note":"` + f.token + `"}]}` + "\n"
					second := `{"resourceSpans":[{"name":"other"}]}` + "\n"
					require.NoError(f.t, os.WriteFile(filepath.Join(dir, "trace-"+testTraceA+".jsonl"), []byte(first), 0o600))
					require.NoError(f.t, os.WriteFile(filepath.Join(dir, "trace-"+testTraceB+".jsonl"), []byte(second), 0o600))
				}
			}
		}
	case "logs":
		return []byte("collector started; token=" + f.token + "\n"), nil
	}
	return nil, nil
}

func (f *fakeCollectorEnv) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newTestTraceCollector(t *testing.T, f *fakeCollectorEnv, client forge.Client, artifactDir string) *traceCollector {
	_ = client // repos are configured with the leased org's client (see ConfigureRepo)
	tc := newTraceCollector(artifactDir, t.Logf)
	tc.run = f.run
	tc.freePort = f.freePort
	tc.readyTimeout = 5 * time.Second
	tc.pollInterval = 10 * time.Millisecond
	return tc
}

// captureStdout redirects os.Stdout into buf until the returned restore
// function is called.
func captureStdout(t *testing.T, buf *bytes.Buffer) (restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(done)
	}()
	var once sync.Once
	restore = func() {
		once.Do(func() {
			os.Stdout = orig
			_ = w.Close()
			<-done
			_ = r.Close()
		})
	}
	t.Cleanup(restore)
	return restore
}

func TestTraceCollector_StartConfigureStop(t *testing.T) {
	f := newFakeCollectorEnv(t)
	client := forge.NewFakeClient()
	artifactDir := t.TempDir()
	var stdout bytes.Buffer
	tc := newTestTraceCollector(t, f, client, artifactDir)
	// Capture the process's own stdout: Start must never write the token
	// there (the step log is tee'd into the artifact dir).
	restore := captureStdout(t, &stdout)

	startErr := tc.Start(context.Background())
	restore()
	require.NoError(t, startErr)
	token := tc.token
	require.Len(t, token, 64)
	assert.Equal(t, token, f.token, "token must reach the collector through the environment")
	assert.Equal(t, "https://abc-def.trycloudflare.com/v1/traces", tc.endpoint)
	assert.NotContains(t, stdout.String(), token, "token must not be written to stdout")
	assert.NotContains(t, stdout.String(), "::add-mask::")
	for _, c := range f.commands() {
		assert.NotContains(t, strings.Join(c, " "), token, "token must not be on a command line")
	}
	workDir := tc.workDir
	cfg, err := os.ReadFile(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(cfg), token)

	// Simulate a tee'd step log that (hypothetically) contains the token.
	logPath := filepath.Join(artifactDir, "behaviour-test.log")
	logContent := "start\ntoken=" + token + "\nend\n"
	require.NoError(t, os.WriteFile(logPath, []byte(logContent), 0o600))

	require.NoError(t, tc.ConfigureRepo(context.Background(), client, "org", "test-repo-01"))
	assert.Equal(t, tc.endpoint, client.VariableValues["org/test-repo-01/"+otelTracesEndpointVar])
	require.Len(t, client.CreatedSecrets, 1)
	assert.Equal(t, forge.SecretRecord{Owner: "org", Repo: "test-repo-01", Name: otelTracesHeadersSecret, Value: "Authorization=Bearer%20" + token}, client.CreatedSecrets[0])

	before := len(f.commands())
	require.NoError(t, tc.Stop(context.Background()))
	stopCmds := f.commands()[before:]
	require.Len(t, stopCmds, 5)
	assert.Equal(t, []string{"podman", "stop", "--time", tunnelStopSeconds, tc.tunnelName}, stopCmds[0])
	assert.Equal(t, []string{"podman", "rm", "-f", tc.tunnelName}, stopCmds[1])
	assert.Equal(t, []string{"podman", "stop", "--time", collectorStopSeconds, tc.collectorName}, stopCmds[2])
	assert.Equal(t, []string{"podman", "logs", tc.collectorName}, stopCmds[3])
	assert.Equal(t, []string{"podman", "rm", "-f", tc.collectorName}, stopCmds[4])

	// Every per-trace file is collected, with the token masked.
	traceFileA := filepath.Join(artifactDir, collectorArtifactSubdir, "trace-"+testTraceA+".jsonl")
	traces, err := os.ReadFile(traceFileA)
	require.NoError(t, err)
	assert.Contains(t, string(traces), `"name":"span"`)
	assert.NotContains(t, string(traces), token)
	other, err := os.ReadFile(filepath.Join(artifactDir, collectorArtifactSubdir, "trace-"+testTraceB+".jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(other), `"name":"other"`)
	info, err := os.Stat(traceFileA)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	collectorLog, err := os.ReadFile(filepath.Join(artifactDir, collectorArtifactSubdir, "collector.log"))
	require.NoError(t, err)
	assert.Contains(t, string(collectorLog), "collector started")
	assert.NotContains(t, string(collectorLog), token)

	stepLog, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Len(t, stepLog, len(logContent), "in-place scrub keeps the file size")
	assert.NotContains(t, string(stepLog), token)
	assert.Contains(t, string(stepLog), "token="+strings.Repeat("*", len(token)))

	_, err = os.Stat(workDir)
	assert.True(t, os.IsNotExist(err), "work dir removed")

	// Stop is idempotent.
	require.NoError(t, tc.Stop(context.Background()))
	assert.Len(t, f.commands(), before+5)
}

func TestTraceCollector_StartFailures(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(f *fakeCollectorEnv)
		wantErr string
	}{
		{name: "receiver without auth", setup: func(f *fakeCollectorEnv) { f.acceptMissingToken = true }, wantErr: "without a token"},
		{name: "receiver accepts wrong token", setup: func(f *fakeCollectorEnv) { f.acceptWrongToken = true }, wantErr: "wrong token"},
		{name: "unexpected tunnel host", setup: func(f *fakeCollectorEnv) { f.tunnelHost = "evil.example.com/x" }, wantErr: "unexpected tunnel hostname"},
		{name: "collector container fails", setup: func(f *fakeCollectorEnv) { f.runErr["bt-otelcol-"] = errors.New("exit 125") }, wantErr: "starting collector container: exit 125: pull failed"},
		{name: "tunnel container fails", setup: func(f *fakeCollectorEnv) { f.runErr["bt-cloudflared-"] = errors.New("exit 125") }, wantErr: "starting tunnel container"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeCollectorEnv(t)
			tt.setup(f)
			artifactDir := t.TempDir()
			tc := newTestTraceCollector(t, f, forge.NewFakeClient(), artifactDir)

			err := tc.Start(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			// Failed start cleans up: collector removed, work dir gone.
			var removed bool
			for _, c := range f.commands() {
				if len(c) == 4 && c[1] == "rm" && c[3] == tc.collectorName {
					removed = true
				}
			}
			assert.True(t, removed, "collector container removed after failed start")
			_, statErr := os.Stat(tc.workDir)
			assert.True(t, os.IsNotExist(statErr))
			assert.Empty(t, tc.token)
		})
	}
}

func TestTraceCollector_StartTimesOutWhenReceiverDown(t *testing.T) {
	f := newFakeCollectorEnv(t)
	f.receiver.Close()
	tc := newTestTraceCollector(t, f, forge.NewFakeClient(), t.TempDir())
	tc.readyTimeout = 50 * time.Millisecond

	err := tc.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Contains(t, err.Error(), "collector receiver")
}

func TestTraceCollector_StartHonoursContext(t *testing.T) {
	f := newFakeCollectorEnv(t)
	f.receiver.Close()
	tc := newTestTraceCollector(t, f, forge.NewFakeClient(), t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := tc.Start(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestTraceCollector_StartPortFailure(t *testing.T) {
	f := newFakeCollectorEnv(t)
	f.ports = nil
	tc := newTestTraceCollector(t, f, forge.NewFakeClient(), t.TempDir())

	err := tc.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "choosing receiver port")
}

func TestTraceCollector_ConfigureRepoErrors(t *testing.T) {
	tc := &traceCollector{endpoint: "https://x.trycloudflare.com/v1/traces", token: "tok"}

	client := forge.NewFakeClient()
	client.Errors["CreateOrUpdateRepoVariable"] = errors.New("boom")
	err := tc.ConfigureRepo(context.Background(), client, "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), otelTracesEndpointVar)

	client = forge.NewFakeClient()
	client.Errors["CreateRepoSecret"] = errors.New("boom")
	err = tc.ConfigureRepo(context.Background(), client, "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), otelTracesHeadersSecret)
}

func TestTraceCollector_StopReportsPodmanErrors(t *testing.T) {
	artifactDir := t.TempDir()
	tc := &traceCollector{
		logf:          t.Logf,
		artifactDir:   artifactDir,
		token:         "0123456789abcdef",
		collectorName: "bt-otelcol-x",
		tunnelName:    "bt-cloudflared-x",
		collectorUp:   true,
		tunnelUp:      true,
		run: func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
			if args[0] == "stop" {
				return []byte("no such container"), errors.New("exit 125")
			}
			return nil, errors.New("exit 125")
		},
	}
	err := tc.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopping tunnel")
	assert.Contains(t, err.Error(), "stopping collector")
	_, statErr := os.Stat(filepath.Join(artifactDir, collectorArtifactSubdir))
	assert.True(t, os.IsNotExist(statErr), "no log artifact when podman logs fails")
}

func TestTraceCollector_StopReportsRemoveErrors(t *testing.T) {
	newTC := func(rmOut string) *traceCollector {
		return &traceCollector{
			logf:          t.Logf,
			artifactDir:   t.TempDir(),
			collectorName: "bt-otelcol-x",
			tunnelName:    "bt-cloudflared-x",
			collectorUp:   true,
			tunnelUp:      true,
			run: func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
				if args[0] == "rm" {
					return []byte(rmOut), errors.New("exit 125")
				}
				return nil, nil
			},
		}
	}

	// stop succeeds but rm fails: the leftover container is reported.
	err := newTC("permission denied").Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removing container bt-cloudflared-x")
	assert.Contains(t, err.Error(), "removing container bt-otelcol-x")

	// A container that is already gone is not an error.
	require.NoError(t, newTC("Error: no such container").Stop(context.Background()))
}

func TestTraceCollector_StopRetriesAfterFailedRemoval(t *testing.T) {
	var rmCalls int
	var ctxHasDeadline bool
	tc := &traceCollector{
		logf:          t.Logf,
		artifactDir:   t.TempDir(),
		token:         "retry-token-value",
		collectorName: "bt-otelcol-x",
		tunnelName:    "bt-cloudflared-x",
		collectorUp:   true,
		tunnelUp:      true,
		run: func(ctx context.Context, _ []string, _ string, args ...string) ([]byte, error) {
			_, ctxHasDeadline = ctx.Deadline()
			if args[0] == "rm" {
				rmCalls++
				if rmCalls <= 2 {
					return []byte("permission denied"), errors.New("exit 125")
				}
			}
			return nil, nil
		},
	}

	// A cancelled caller context must not affect cleanup.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, tc.Stop(ctx))
	assert.True(t, ctxHasDeadline, "cleanup runs under its own bounded context")
	assert.False(t, tc.stopped)
	assert.NotEmpty(t, tc.token, "token kept so a retry can still scrub")

	// The retry removes the containers and completes the cleanup.
	require.NoError(t, tc.Stop(context.Background()))
	assert.True(t, tc.stopped)
	assert.Empty(t, tc.token)
	assert.Equal(t, 4, rmCalls)

	// Once done, further calls do nothing.
	require.NoError(t, tc.Stop(context.Background()))
	assert.Equal(t, 4, rmCalls)
}

func TestTraceCollector_StopKeepsTracesUntilCopied(t *testing.T) {
	workDir := t.TempDir()
	tracesDir := filepath.Join(workDir, "traces")
	require.NoError(t, os.Mkdir(tracesDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tracesDir, "trace-"+testTraceA+".jsonl"), []byte("span\n"), 0o600))

	// The artifact dir is a regular file, so the copy cannot create
	// the destination directory.
	blocker := filepath.Join(t.TempDir(), "artifacts")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	tc := &traceCollector{
		logf:        t.Logf,
		artifactDir: blocker,
		workDir:     workDir,
		run:         func(context.Context, []string, string, ...string) ([]byte, error) { return nil, nil },
	}

	require.Error(t, tc.Stop(context.Background()))
	assert.False(t, tc.stopped)
	assert.Equal(t, workDir, tc.workDir, "work dir kept so the copy can be retried")
	_, err := os.Stat(filepath.Join(tracesDir, "trace-"+testTraceA+".jsonl"))
	require.NoError(t, err, "source trace file must survive a failed copy")

	// The retry, with a usable artifact dir, saves the trace and cleans up.
	require.NoError(t, os.Remove(blocker))
	require.NoError(t, os.Mkdir(blocker, 0o700))
	require.NoError(t, tc.Stop(context.Background()))
	assert.True(t, tc.stopped)
	saved, err := os.ReadFile(filepath.Join(blocker, collectorArtifactSubdir, "trace-"+testTraceA+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "span\n", string(saved))
	_, err = os.Stat(workDir)
	assert.True(t, os.IsNotExist(err), "work dir removed once the traces are saved")
}

func TestTraceCollector_WriteArtifactRefusesSymlinks(t *testing.T) {
	tc := &traceCollector{logf: t.Logf}

	t.Run("symlinked file", func(t *testing.T) {
		destDir := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		require.NoError(t, os.WriteFile(victim, []byte("keep"), 0o600))
		require.NoError(t, os.Symlink(victim, filepath.Join(destDir, "collector.log")))

		require.NoError(t, tc.writeArtifact(destDir, "collector.log", []byte("new")))
		got, err := os.ReadFile(victim)
		require.NoError(t, err)
		assert.Equal(t, "keep", string(got), "symlink target must not be written")
		info, err := os.Lstat(filepath.Join(destDir, "collector.log"))
		require.NoError(t, err)
		assert.True(t, info.Mode().IsRegular(), "symlink replaced by a regular file")
	})

	t.Run("symlinked directory", func(t *testing.T) {
		target := t.TempDir()
		destDir := filepath.Join(t.TempDir(), collectorArtifactSubdir)
		require.NoError(t, os.Symlink(target, destDir))

		err := tc.writeArtifact(destDir, "collector.log", []byte("new"))
		require.Error(t, err)
		_, statErr := os.Stat(filepath.Join(target, "collector.log"))
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("existing file permissions tightened", func(t *testing.T) {
		destDir := t.TempDir()
		path := filepath.Join(destDir, "trace.jsonl")
		require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

		require.NoError(t, tc.writeArtifact(destDir, "trace.jsonl", []byte("new")))
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "new", string(got))
	})
}

func TestTraceCollector_FailedStartCleanupPending(t *testing.T) {
	f := newFakeCollectorEnv(t)
	f.acceptMissingToken = true // Start fails after the collector is up
	tc := newTestTraceCollector(t, f, forge.NewFakeClient(), t.TempDir())
	failRm := true
	run := tc.run
	tc.run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if failRm && len(args) > 0 && args[0] == "rm" {
			return []byte("permission denied"), errors.New("exit 125")
		}
		return run(ctx, env, name, args...)
	}

	require.Error(t, tc.Start(context.Background()))
	assert.True(t, tc.cleanupPending(), "a failed cleanup stays pending")

	// A later Stop (from Finalize) completes it.
	failRm = false
	require.NoError(t, tc.Stop(context.Background()))
	assert.False(t, tc.cleanupPending())
}

func TestScrubTokenInTree_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	token := "s3cr3t-token-value"

	big := filepath.Join(dir, "big.log")
	f, err := os.Create(big)
	require.NoError(t, err)
	_, err = f.WriteString("prefix " + token)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxScrubFileSize+1))
	require.NoError(t, f.Close())

	err = scrubTokenInTree(dir, token, t.Logf)
	require.Error(t, err)

	got, err := os.ReadFile(big)
	require.NoError(t, err)
	assert.NotContains(t, string(got), token)
	assert.Contains(t, string(got), "content removed")

	// A file that vanished is not an error for the stub step.
	require.NoError(t, stubUnscrubbedFile(filepath.Join(dir, "missing.log")))
}

func TestScrubTokenInTree_RemovesUnwritableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only files")
	}
	dir := t.TempDir()
	token := "s3cr3t-token-value"

	ro := filepath.Join(dir, "readonly.log")
	require.NoError(t, os.WriteFile(ro, []byte("prefix "+token), 0o400))

	err := scrubTokenInTree(dir, token, t.Logf)
	require.Error(t, err)

	// Neither scrubbing nor stubbing was possible, so the file is deleted.
	_, statErr := os.Stat(ro)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestScrubFileInPlace(t *testing.T) {
	dir := t.TempDir()
	token := "s3cr3t-token-value"

	withToken := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(withToken, []byte("x "+token+" y "+token), 0o600))
	without := filepath.Join(dir, "b.txt")
	require.NoError(t, os.WriteFile(without, []byte("nothing here"), 0o600))
	nested := filepath.Join(dir, "sub", "c.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o700))
	require.NoError(t, os.WriteFile(nested, []byte(`{"h":"Authorization=Bearer%20`+token+`"}`), 0o600))

	require.NoError(t, scrubTokenInTree(dir, token, t.Logf))

	mask := strings.Repeat("*", len(token))
	got, err := os.ReadFile(withToken)
	require.NoError(t, err)
	assert.Equal(t, "x "+mask+" y "+mask, string(got))
	got, err = os.ReadFile(without)
	require.NoError(t, err)
	assert.Equal(t, "nothing here", string(got))
	got, err = os.ReadFile(nested)
	require.NoError(t, err)
	assert.Equal(t, `{"h":"Authorization=Bearer%20`+mask+`"}`, string(got))

	// A missing tree is not an error.
	require.NoError(t, scrubTokenInTree(filepath.Join(dir, "missing"), token, t.Logf))
	// An unreadable file is reported.
	require.Error(t, scrubFileInPlace(filepath.Join(dir, "missing.txt"), token))
}

func TestFreeLocalPort(t *testing.T) {
	p, err := freeLocalPort()
	require.NoError(t, err)
	assert.Positive(t, p)
}

// fakeTraceSink records composed-driver calls.
type fakeTraceSink struct {
	mu           sync.Mutex
	configured   []string
	clients      []forge.Client
	configureErr error
	stopCalls    int
	stopErr      error
}

func (f *fakeTraceSink) ConfigureRepo(_ context.Context, client forge.Client, org, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configured = append(f.configured, org+"/"+repo)
	f.clients = append(f.clients, client)
	return f.configureErr
}

func (f *fakeTraceSink) Stop(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	return f.stopErr
}

func TestComposedDriver_TraceCollector(t *testing.T) {
	sink := &fakeTraceSink{}
	td := newTestPoolDriver(t, StagePoolLayout(2))
	td.traces = sink

	l, err := td.AllocateLease(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{l.Org + "/" + l.Repo}, sink.configured)
	require.Len(t, sink.clients, 1)
	assert.Same(t, td.pool.clients[l.Org], sink.clients[0], "repo configured with the leased org's client")
	require.NoError(t, td.DeallocateLease(context.Background(), l.Org, l.Repo))

	require.NoError(t, td.Finalize(context.Background()))
	assert.Equal(t, 1, sink.stopCalls)
}

func TestComposedDriver_TraceCollectorErrors(t *testing.T) {
	sink := &fakeTraceSink{configureErr: errors.New("api down"), stopErr: errors.New("podman gone")}
	td := newTestPoolDriver(t, StagePoolLayout(1))
	td.traces = sink

	l, err := td.AllocateLease(context.Background())
	require.NoError(t, err, "a repo that cannot be pointed at the collector is still allocated")
	require.NoError(t, td.DeallocateLease(context.Background(), l.Org, l.Repo))

	err = td.Finalize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trace collector: podman gone")
}

func TestComposedDriver_FinalizeRetriesFailedStartCleanup(t *testing.T) {
	pending := &fakeTraceSink{}
	td := newTestPoolDriver(t, StagePoolLayout(1))
	td.pendingTraces = pending

	l, err := td.AllocateLease(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending.configured, "a failed-start collector never configures repos")
	require.NoError(t, td.DeallocateLease(context.Background(), l.Org, l.Repo))

	require.NoError(t, td.Finalize(context.Background()))
	assert.Equal(t, 1, pending.stopCalls)

	pending.stopErr = errors.New("podman gone")
	err = td.Finalize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed-start trace collector cleanup: podman gone")
}

func TestStartTraceCollector_NotStarted(t *testing.T) {
	attach := func(t *testing.T) *composedDriver {
		td := newTestPoolDriver(t, StagePoolLayout(1))
		return startTraceCollector(t.Logf).attach(td.composedDriver).(*composedDriver)
	}

	t.Run("disabled", func(t *testing.T) {
		t.Setenv("GITHUB_ACTIONS", "")
		t.Setenv(envTraceCollector, "")
		assert.Nil(t, attach(t).traces)
	})
	t.Run("invalid setting", func(t *testing.T) {
		t.Setenv(envTraceCollector, "sometimes")
		assert.Nil(t, attach(t).traces)
	})
	t.Run("podman missing", func(t *testing.T) {
		t.Setenv(envTraceCollector, "true")
		t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
		t.Setenv("PATH", t.TempDir())
		assert.Nil(t, attach(t).traces)
	})
	t.Run("non-composed driver", func(t *testing.T) {
		td := newTestPoolDriver(t, StagePoolLayout(1))
		pd := NewPlaybackDriver(td.composedDriver)
		assert.Same(t, pd, traceStart{}.attach(pd))
	})
}
