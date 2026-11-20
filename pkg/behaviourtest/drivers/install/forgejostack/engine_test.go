package forgejostack

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveEngine serves handler on a Unix socket and returns a SocketEngine
// connected to it.
func serveEngine(t *testing.T, handler http.Handler) *SocketEngine {
	t.Helper()
	// Unix socket paths are length-limited; t.TempDir can be too long.
	dir, err := os.MkdirTemp("", "fjse")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "engine.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return NewSocketEngine(path)
}

// frame encodes one multiplexed engine stream frame.
func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestResolveSocket(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     map[string]string
		exists  []string
		want    string
		wantErr string
	}{
		{name: "DOCKER_HOST unix socket", env: map[string]string{"DOCKER_HOST": "unix:///tmp/d.sock", "CONTAINER_HOST": "unix:///tmp/p.sock"}, want: "/tmp/d.sock"},
		{name: "CONTAINER_HOST unix socket", env: map[string]string{"CONTAINER_HOST": "unix:///run/user/1000/podman/podman.sock"}, want: "/run/user/1000/podman/podman.sock"},
		{name: "DOCKER_HOST tcp rejected", env: map[string]string{"DOCKER_HOST": "tcp://127.0.0.1:2375"}, wantErr: "DOCKER_HOST=\"tcp://127.0.0.1:2375\""},
		{name: "CONTAINER_HOST ssh rejected", env: map[string]string{"CONTAINER_HOST": "ssh://host/run/podman.sock"}, wantErr: "CONTAINER_HOST"},
		{name: "empty unix path rejected", env: map[string]string{"DOCKER_HOST": "unix://"}, wantErr: "unix://"},
		{name: "docker default first", env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, exists: []string{"/var/run/docker.sock", "/run/user/1000/podman/podman.sock"}, want: "/var/run/docker.sock"},
		{name: "rootless podman default", env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, exists: []string{"/run/user/1000/podman/podman.sock", "/run/podman/podman.sock"}, want: "/run/user/1000/podman/podman.sock"},
		{name: "rootful podman default", exists: []string{"/run/podman/podman.sock"}, want: "/run/podman/podman.sock"},
		{name: "no socket", env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, wantErr: "no Docker or Podman socket found (tried /var/run/docker.sock, /run/user/1000/podman/podman.sock, /run/podman/podman.sock)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tc.env[k] }
			exists := func(p string) bool { return slices.Contains(tc.exists, p) }
			got, err := ResolveSocket(getenv, exists)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSocketExists(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "fjsx")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	file := filepath.Join(dir, "plain")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	assert.True(t, SocketExists(sock))
	assert.False(t, SocketExists(file))
	assert.False(t, SocketExists(filepath.Join(dir, "missing")))
}

func TestSplitImageRef(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string][2]string{
		"codeberg.org/forgejo/forgejo:17.0.1-rootless": {"codeberg.org/forgejo/forgejo", "17.0.1-rootless"},
		"docker.io/library/node:22-bookworm":           {"docker.io/library/node", "22-bookworm"},
		"localhost:5000/runner":                        {"localhost:5000/runner", "latest"},
		"alpine":                                       {"alpine", "latest"},
	} {
		repo, tag := splitImageRef(ref)
		assert.Equal(t, want, [2]string{repo, tag}, ref)
	}
}

func TestSocketEngine_PullImage(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("fromImage") {
		case "example.org/ok":
			assert.Equal(t, "1.0", r.URL.Query().Get("tag"))
			_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Done"}`+"\n")
		case "example.org/denied":
			_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"denied: access forbidden"}`+"\n")
		case "example.org/detail":
			_, _ = io.WriteString(w, `{"errorDetail":{"message":"manifest unknown"}}`)
		case "example.org/garbled":
			_, _ = io.WriteString(w, `{"status":`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"no such image"}`)
		}
	})
	e := serveEngine(t, mux)
	ctx := context.Background()

	require.NoError(t, e.PullImage(ctx, "example.org/ok:1.0"))
	require.ErrorContains(t, e.PullImage(ctx, "example.org/denied:1.0"), "pulling example.org/denied:1.0: denied: access forbidden")
	require.ErrorContains(t, e.PullImage(ctx, "example.org/detail:1.0"), "manifest unknown")
	require.ErrorContains(t, e.PullImage(ctx, "example.org/garbled:1.0"), "reading progress")
	err := e.PullImage(ctx, "example.org/missing:1.0")
	var ee *engineError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, http.StatusNotFound, ee.Status)
	assert.Equal(t, "no such image", ee.Message)
	assert.ErrorContains(t, err, "container engine: POST /images/create: HTTP 404: no such image")
}

func TestSocketEngine_CreateContainer(t *testing.T) {
	t.Parallel()
	var got map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("name") {
		case "ok":
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&got)) {
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"abc123"}`)
		case "noid":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case "badjson":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `not json`)
		default:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, "name in use")
		}
	})
	e := serveEngine(t, mux)
	ctx := context.Background()

	id, err := e.CreateContainer(ctx, "ok", ContainerSpec{
		Image:       "img:1",
		Env:         []string{"A=b"},
		Labels:      map[string]string{"k": "v"},
		Entrypoint:  []string{"/bin/sh", "-c"},
		Cmd:         []string{"true"},
		User:        "0:0",
		WorkingDir:  "/data",
		NetworkMode: "host",
		Binds:       []string{"/s:/var/run/docker.sock"},
		SecurityOpt: []string{"label=disable"},
	})
	require.NoError(t, err)
	assert.Equal(t, "abc123", id)
	assert.Equal(t, "img:1", got["Image"])
	assert.Equal(t, []any{"A=b"}, got["Env"])
	assert.Equal(t, map[string]any{"k": "v"}, got["Labels"])
	assert.Equal(t, []any{"/bin/sh", "-c"}, got["Entrypoint"])
	assert.Equal(t, "0:0", got["User"])
	assert.Equal(t, "/data", got["WorkingDir"])
	assert.Equal(t, map[string]any{
		"NetworkMode": "host",
		"Binds":       []any{"/s:/var/run/docker.sock"},
		"SecurityOpt": []any{"label=disable"},
	}, got["HostConfig"])

	_, err = e.CreateContainer(ctx, "noid", ContainerSpec{Image: "img"})
	require.ErrorContains(t, err, "no container ID")
	_, err = e.CreateContainer(ctx, "badjson", ContainerSpec{Image: "img"})
	require.ErrorContains(t, err, "decoding POST /containers/create")
	_, err = e.CreateContainer(ctx, "taken", ContainerSpec{Image: "img"})
	require.ErrorContains(t, err, "creating container taken: container engine: POST /containers/create: HTTP 409: name in use")
}

func TestSocketEngine_RootlessDocker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		info    string
		version string
		want    bool
		wantErr string
	}{
		{name: "rootful docker", info: `{"SecurityOptions":["name=seccomp,profile=builtin"]}`, want: false},
		{name: "rootless docker", info: `{"SecurityOptions":["name=seccomp,profile=builtin","name=rootless"]}`,
			version: `{"Components":[{"Name":"Engine"}]}`, want: true},
		{name: "rootless podman", info: `{"SecurityOptions":["name=rootless"]}`,
			version: `{"Components":[{"Name":"Podman Engine"}]}`, want: false},
		{name: "info error", info: "", wantErr: "reading engine info"},
		{name: "version error", info: `{"SecurityOptions":["name=rootless"]}`, version: "", wantErr: "reading engine version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
				if tt.info == "" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = io.WriteString(w, tt.info)
			})
			mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
				if tt.version == "" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = io.WriteString(w, tt.version)
			})
			got, err := serveEngine(t, mux).RootlessDocker(context.Background())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSocketEngine_Lifecycle(t *testing.T) {
	t.Parallel()
	var startCalled, inspectCalled, stopCalled, removeCalled, listCalled, logsCalled atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		startCalled.Store(true)
		switch r.PathValue("id") {
		case "running":
			w.WriteHeader(http.StatusNotModified)
		case "gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"no such container"}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		inspectCalled.Store(true)
		if r.PathValue("id") == "gone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false,"ExitCode":2}}`)
	})
	mux.HandleFunc("POST /containers/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		stopCalled.Store(true)
		assert.Equal(t, "10", r.URL.Query().Get("t"))
		switch r.PathValue("id") {
		case "gone":
			w.WriteHeader(http.StatusNotFound)
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("DELETE /containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		removeCalled.Store(true)
		assert.Equal(t, "true", r.URL.Query().Get("force"))
		assert.Equal(t, "true", r.URL.Query().Get("v"))
		switch r.PathValue("id") {
		case "gone":
			w.WriteHeader(http.StatusNotFound)
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"device busy"}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		listCalled.Store(true)
		assert.Equal(t, "true", r.URL.Query().Get("all"))
		var filters map[string][]string
		if !assert.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)) {
			return
		}
		if filters["label"][0] == "k=broken" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assert.Equal(t, map[string][]string{"label": {"k=v"}}, filters)
		_, _ = io.WriteString(w, `[{"Id":"a"},{"Id":"b"}]`)
	})
	mux.HandleFunc("GET /containers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		logsCalled.Store(true)
		assert.Equal(t, "50", r.URL.Query().Get("tail"))
		assert.Equal(t, "true", r.URL.Query().Get("stdout"))
		assert.Equal(t, "true", r.URL.Query().Get("stderr"))
		if r.PathValue("id") == "gone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(append(frame(1, "out line\n"), frame(2, "err line\n")...))
	})
	e := serveEngine(t, mux)
	ctx := context.Background()

	require.NoError(t, e.StartContainer(ctx, "new"))
	require.NoError(t, e.StartContainer(ctx, "running"))
	require.ErrorContains(t, e.StartContainer(ctx, "gone"), "starting container gone")

	state, err := e.InspectContainer(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, ContainerState{Status: "exited", Running: false, ExitCode: 2}, state)
	_, err = e.InspectContainer(ctx, "gone")
	require.ErrorContains(t, err, "inspecting container gone")

	require.NoError(t, e.StopContainer(ctx, "c", 10*time.Second))
	require.NoError(t, e.StopContainer(ctx, "gone", 10*time.Second))
	require.ErrorContains(t, e.StopContainer(ctx, "broken", 10*time.Second), "stopping container broken")

	require.NoError(t, e.RemoveContainer(ctx, "c"))
	require.NoError(t, e.RemoveContainer(ctx, "gone"))
	require.ErrorContains(t, e.RemoveContainer(ctx, "broken"), "removing container broken: container engine: DELETE /containers/broken: HTTP 500: device busy")

	ids, err := e.ListContainers(ctx, "k", "v")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids)
	_, err = e.ListContainers(ctx, "k", "broken")
	require.ErrorContains(t, err, "listing containers labelled k=broken")

	logs, err := e.ContainerLogs(ctx, "c", 50)
	require.NoError(t, err)
	assert.Equal(t, "out line\nerr line\n", logs)
	_, err = e.ContainerLogs(ctx, "gone", 50)
	require.ErrorContains(t, err, "reading logs of gone")

	assert.True(t, startCalled.Load(), "start handler was not called — URL path mismatch")
	assert.True(t, inspectCalled.Load(), "inspect handler was not called — URL path mismatch")
	assert.True(t, stopCalled.Load(), "stop handler was not called — URL path mismatch")
	assert.True(t, removeCalled.Load(), "remove handler was not called — URL path mismatch")
	assert.True(t, listCalled.Load(), "list handler was not called — URL path mismatch")
	assert.True(t, logsCalled.Load(), "logs handler was not called — URL path mismatch")
}

func TestSocketEngine_Exec(t *testing.T) {
	t.Parallel()
	var inspects atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Cmd          []string
			AttachStdout bool
			AttachStderr bool
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.True(t, body.AttachStdout)
		assert.True(t, body.AttachStderr)
		if r.PathValue("id") == "gone" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"no such container"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"exec-`+r.PathValue("id")+`"}`)
	})
	mux.HandleFunc("POST /exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Detach, Tty bool }
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.False(t, body.Detach)
		assert.False(t, body.Tty)
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		_, _ = w.Write(bytes.Join([][]byte{frame(1, "tok"), frame(2, "warn\n"), frame(1, "en\n")}, nil))
	})
	mux.HandleFunc("GET /exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "exec-stuck":
			_, _ = io.WriteString(w, `{"Running":true}`)
		case "exec-badinspect":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			// The first inspect still reports the exec running.
			if inspects.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"Running":true,"ExitCode":0}`)
				return
			}
			_, _ = io.WriteString(w, `{"Running":false,"ExitCode":3}`)
		}
	})
	e := serveEngine(t, mux)
	ctx := context.Background()

	res, err := e.Exec(ctx, "c", []string{"forgejo", "actions", "generate-runner-token"})
	require.NoError(t, err)
	assert.Equal(t, ExecResult{Stdout: "token\n", Stderr: "warn\n", ExitCode: 3}, res)

	_, err = e.Exec(ctx, "gone", []string{"forgejo", "admin", "user", "create", "--password", "s3cret"})
	require.ErrorContains(t, err, `exec "forgejo admin" in gone`)
	assert.NotContains(t, err.Error(), "s3cret", "exec errors must not echo arguments that may be credentials")

	_, err = e.Exec(ctx, "badinspect", []string{"true"})
	require.ErrorContains(t, err, "GET /exec/exec-badinspect/json")

	shortCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = e.Exec(shortCtx, "stuck", []string{"sleep"})
	require.Error(t, err)
}

func TestDemuxTo_RawStream(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"plain tty output\n", "short", ""} {
		stdout, stderr, err := demux(bytes.NewBufferString(raw))
		require.NoError(t, err)
		assert.Equal(t, raw, string(stdout))
		assert.Empty(t, stderr)
	}
}

func TestDemuxTo_TruncatedFrame(t *testing.T) {
	t.Parallel()
	data := frame(1, "complete")
	data = append(data, frame(1, "truncated")[:12]...)
	_, _, err := demux(bytes.NewReader(data))
	require.Error(t, err)
}

func TestSleepCtx(t *testing.T) {
	t.Parallel()
	require.NoError(t, sleepCtx(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepCtx(ctx, time.Hour), context.Canceled)
}
