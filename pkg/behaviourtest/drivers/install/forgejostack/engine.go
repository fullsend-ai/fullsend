package forgejostack

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Engine is the subset of a container engine the stack needs. The
// production implementation, SocketEngine, speaks the Docker Engine API
// over the host engine's Unix socket, which Docker and Podman both serve.
type Engine interface {
	// PullImage pulls ref (registry/repository:tag).
	PullImage(ctx context.Context, ref string) error
	// CreateContainer creates a container named name and returns its ID.
	CreateContainer(ctx context.Context, name string, spec ContainerSpec) (string, error)
	// StartContainer starts a created container.
	StartContainer(ctx context.Context, id string) error
	// InspectContainer reports whether the container is running and,
	// once it has exited, its exit code.
	InspectContainer(ctx context.Context, id string) (ContainerState, error)
	// Exec runs cmd in a running container and waits for it to exit.
	Exec(ctx context.Context, id string, cmd []string) (ExecResult, error)
	// ContainerLogs returns the last tail lines of the container's
	// combined stdout and stderr.
	ContainerLogs(ctx context.Context, id string, tail int) (string, error)
	// StopContainer stops a container, waiting up to timeout before the
	// engine kills it. A container that is gone or stopped is not an
	// error.
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	// RemoveContainer force-removes a container and its anonymous
	// volumes. A container that is already gone is not an error.
	RemoveContainer(ctx context.Context, id string) error
	// ListContainers returns the IDs of all containers, running or not,
	// that carry label key=value.
	ListContainers(ctx context.Context, key, value string) ([]string, error)
	// RootlessDocker reports whether the engine is a rootless Docker
	// daemon. Rootless Podman is not reported: its host network
	// namespace is the invoking user's own.
	RootlessDocker(ctx context.Context) (bool, error)
}

// ContainerSpec is the configuration of a container the stack creates.
type ContainerSpec struct {
	Image       string
	Env         []string
	Labels      map[string]string
	Entrypoint  []string
	Cmd         []string
	User        string
	WorkingDir  string
	NetworkMode string
	Binds       []string
	SecurityOpt []string
}

// ContainerState is the part of a container's inspect output the stack
// reads.
type ContainerState struct {
	Status   string
	Running  bool
	ExitCode int
}

// ExecResult is the outcome of Engine.Exec.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Environment variables that configure the host engine socket, in the
// order ResolveSocket consults them.
const (
	DockerHostEnv    = "DOCKER_HOST"
	ContainerHostEnv = "CONTAINER_HOST"
)

// ResolveSocket returns the host path of the container engine's Unix
// socket. DOCKER_HOST, then CONTAINER_HOST (Podman's variable), select
// it when set; they must be unix:// URLs, since the socket is also
// mounted into the runner container. With neither set, the first
// existing default socket is used: Docker's, then the rootless and the
// rootful Podman socket.
func ResolveSocket(getenv func(string) string, exists func(string) bool) (string, error) {
	for _, name := range []string{DockerHostEnv, ContainerHostEnv} {
		raw := getenv(name)
		if raw == "" {
			continue
		}
		path, ok := strings.CutPrefix(raw, "unix://")
		if !ok || path == "" {
			return "", fmt.Errorf("%s=%q: the forgejo-dev stack needs a unix:// engine socket it can mount into the runner", name, raw)
		}
		return path, nil
	}
	candidates := []string{"/var/run/docker.sock"}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "podman", "podman.sock"))
	}
	candidates = append(candidates, "/run/podman/podman.sock")
	for _, c := range candidates {
		if exists(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("no Docker or Podman socket found (tried %s); start the engine or set %s or %s to a unix:// socket",
		strings.Join(candidates, ", "), DockerHostEnv, ContainerHostEnv)
}

// SocketExists reports whether path exists and is a Unix socket. It is
// the exists function ResolveSocket is given outside tests.
func SocketExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// SocketEngine is an Engine speaking the Docker Engine API (which
// Podman serves as its compatibility API) over a Unix socket. Requests
// use unversioned paths, so the engine applies its current API version.
type SocketEngine struct {
	client *http.Client
}

var _ Engine = (*SocketEngine)(nil)

// NewSocketEngine returns a SocketEngine for the socket at path.
func NewSocketEngine(path string) *SocketEngine {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &SocketEngine{client: &http.Client{Transport: transport}}
}

// engineError is a non-success engine API response.
type engineError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *engineError) Error() string {
	return fmt.Sprintf("container engine: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// do sends a request and returns the response when its status is one of
// ok; otherwise it returns an *engineError carrying the engine's
// message. The caller closes the returned body.
func (e *SocketEngine) do(ctx context.Context, method, path string, query url.Values, body any, ok ...int) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("container engine: encoding %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(data)
	}
	u := "http://engine" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, fmt.Errorf("container engine: %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("container engine: %s %s: %w", method, path, err)
	}
	if slices.Contains(ok, resp.StatusCode) {
		return resp, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := strings.TrimSpace(string(data))
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Message != "" {
		msg = payload.Message
	}
	return nil, &engineError{Method: method, Path: path, Status: resp.StatusCode, Message: msg}
}

// doDiscard is do for requests whose response body is not needed.
func (e *SocketEngine) doDiscard(ctx context.Context, method, path string, query url.Values, body any, ok ...int) error {
	resp, err := e.do(ctx, method, path, query, body, ok...)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// doJSON is do that decodes a JSON response body into out.
func (e *SocketEngine) doJSON(ctx context.Context, method, path string, query url.Values, body, out any, ok ...int) error {
	resp, err := e.do(ctx, method, path, query, body, ok...)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("container engine: decoding %s %s: %w", method, path, err)
	}
	return nil
}

// splitImageRef splits ref into the repository and tag the pull API
// takes. A ref without a tag pulls "latest".
func splitImageRef(ref string) (repo, tag string) {
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

// PullImage pulls ref. The engine streams progress as JSON messages and
// reports a failed pull in the stream, not the status, so the whole
// stream is read and checked.
func (e *SocketEngine) PullImage(ctx context.Context, ref string) error {
	repo, tag := splitImageRef(ref)
	resp, err := e.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {repo}, "tag": {tag}}, nil, http.StatusOK)
	if err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("pulling %s: reading progress: %w", ref, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("pulling %s: %s", ref, msg.Error)
		}
		if msg.ErrorDetail.Message != "" {
			return fmt.Errorf("pulling %s: %s", ref, msg.ErrorDetail.Message)
		}
	}
}

// CreateContainer creates a container named name.
func (e *SocketEngine) CreateContainer(ctx context.Context, name string, spec ContainerSpec) (string, error) {
	type hostConfig struct {
		NetworkMode string   `json:"NetworkMode,omitempty"`
		Binds       []string `json:"Binds,omitempty"`
		SecurityOpt []string `json:"SecurityOpt,omitempty"`
	}
	body := struct {
		Image      string            `json:"Image"`
		Env        []string          `json:"Env,omitempty"`
		Labels     map[string]string `json:"Labels,omitempty"`
		Entrypoint []string          `json:"Entrypoint,omitempty"`
		Cmd        []string          `json:"Cmd,omitempty"`
		User       string            `json:"User,omitempty"`
		WorkingDir string            `json:"WorkingDir,omitempty"`
		HostConfig hostConfig        `json:"HostConfig"`
	}{
		Image:      spec.Image,
		Env:        spec.Env,
		Labels:     spec.Labels,
		Entrypoint: spec.Entrypoint,
		Cmd:        spec.Cmd,
		User:       spec.User,
		WorkingDir: spec.WorkingDir,
		HostConfig: hostConfig{NetworkMode: spec.NetworkMode, Binds: spec.Binds, SecurityOpt: spec.SecurityOpt},
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := e.doJSON(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, &out, http.StatusCreated, http.StatusOK); err != nil {
		return "", fmt.Errorf("creating container %s: %w", name, err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("creating container %s: engine returned no container ID", name)
	}
	return out.ID, nil
}

// StartContainer starts a container; one already running is not an
// error.
func (e *SocketEngine) StartContainer(ctx context.Context, id string) error {
	if err := e.doDiscard(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, http.StatusNoContent, http.StatusNotModified, http.StatusOK); err != nil {
		return fmt.Errorf("starting container %s: %w", id, err)
	}
	return nil
}

// InspectContainer reads the container's state.
func (e *SocketEngine) InspectContainer(ctx context.Context, id string) (ContainerState, error) {
	var out struct {
		State ContainerState `json:"State"`
	}
	if err := e.doJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out, http.StatusOK); err != nil {
		return ContainerState{}, fmt.Errorf("inspecting container %s: %w", id, err)
	}
	return out.State, nil
}

// execExitPollInterval and execExitPolls bound how long Exec waits for
// the engine to record an exec's exit code after its output stream
// closed.
const (
	execExitPollInterval = 100 * time.Millisecond
	execExitPolls        = 50
)

// Exec runs cmd in container id as the container's user, collecting its
// stdout and stderr.
func (e *SocketEngine) Exec(ctx context.Context, id string, cmd []string) (ExecResult, error) {
	label := execLabel(cmd)
	var created struct {
		ID string `json:"Id"`
	}
	createBody := map[string]any{"AttachStdout": true, "AttachStderr": true, "Cmd": cmd}
	if err := e.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", nil, createBody, &created, http.StatusCreated, http.StatusOK); err != nil {
		return ExecResult{}, fmt.Errorf("exec %q in %s: %w", label, id, err)
	}
	resp, err := e.do(ctx, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", nil, map[string]any{"Detach": false, "Tty": false}, http.StatusOK)
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec %q in %s: %w", label, id, err)
	}
	stdout, stderr, readErr := demux(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return ExecResult{}, fmt.Errorf("exec %q in %s: reading output: %w", label, id, readErr)
	}
	result := ExecResult{Stdout: string(stdout), Stderr: string(stderr)}
	for range execExitPolls {
		var inspect struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if err := e.doJSON(ctx, http.MethodGet, "/exec/"+url.PathEscape(created.ID)+"/json", nil, nil, &inspect, http.StatusOK); err != nil {
			return result, fmt.Errorf("exec %q in %s: %w", label, id, err)
		}
		if !inspect.Running {
			result.ExitCode = inspect.ExitCode
			return result, nil
		}
		if err := sleepCtx(ctx, execExitPollInterval); err != nil {
			return result, fmt.Errorf("exec %q in %s: %w", label, id, err)
		}
	}
	return result, fmt.Errorf("exec %q in %s: still running after its output closed", label, id)
}

// ContainerLogs returns the last tail lines of the container's output.
func (e *SocketEngine) ContainerLogs(ctx context.Context, id string, tail int) (string, error) {
	q := url.Values{"stdout": {"true"}, "stderr": {"true"}, "tail": {strconv.Itoa(tail)}}
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("reading logs of %s: %w", id, err)
	}
	defer resp.Body.Close()
	var combined bytes.Buffer
	if err := demuxTo(resp.Body, &combined, &combined); err != nil {
		return combined.String(), fmt.Errorf("reading logs of %s: %w", id, err)
	}
	return combined.String(), nil
}

// StopContainer stops a container.
func (e *SocketEngine) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(timeout.Seconds()))}}
	if err := e.doDiscard(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil,
		http.StatusNoContent, http.StatusNotModified, http.StatusNotFound, http.StatusOK); err != nil {
		return fmt.Errorf("stopping container %s: %w", id, err)
	}
	return nil
}

// RemoveContainer force-removes a container with its anonymous volumes.
func (e *SocketEngine) RemoveContainer(ctx context.Context, id string) error {
	q := url.Values{"force": {"true"}, "v": {"true"}}
	if err := e.doDiscard(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil,
		http.StatusNoContent, http.StatusNotFound, http.StatusOK); err != nil {
		return fmt.Errorf("removing container %s: %w", id, err)
	}
	return nil
}

// ListContainers lists the containers labelled key=value.
func (e *SocketEngine) ListContainers(ctx context.Context, key, value string) ([]string, error) {
	filters, err := json.Marshal(map[string][]string{"label": {key + "=" + value}})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	var out []struct {
		ID string `json:"Id"`
	}
	if err := e.doJSON(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"true"}, "filters": {string(filters)}}, nil, &out, http.StatusOK); err != nil {
		return nil, fmt.Errorf("listing containers labelled %s=%s: %w", key, value, err)
	}
	ids := make([]string, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// RootlessDocker reports whether the engine is a rootless Docker daemon.
// Both rootless Docker and rootless Podman list "name=rootless" among the
// security options, so Podman is told apart by its "Podman Engine"
// version component.
func (e *SocketEngine) RootlessDocker(ctx context.Context) (bool, error) {
	var info struct {
		SecurityOptions []string `json:"SecurityOptions"`
	}
	if err := e.doJSON(ctx, http.MethodGet, "/info", nil, nil, &info, http.StatusOK); err != nil {
		return false, fmt.Errorf("reading engine info: %w", err)
	}
	if !slices.Contains(info.SecurityOptions, "name=rootless") {
		return false, nil
	}
	var version struct {
		Components []struct {
			Name string `json:"Name"`
		} `json:"Components"`
	}
	if err := e.doJSON(ctx, http.MethodGet, "/version", nil, nil, &version, http.StatusOK); err != nil {
		return false, fmt.Errorf("reading engine version: %w", err)
	}
	for _, c := range version.Components {
		if strings.Contains(strings.ToLower(c.Name), "podman") {
			return false, nil
		}
	}
	return true, nil
}

// execLabel names an exec command in errors by its first two words
// only: later arguments may carry credentials.
func execLabel(cmd []string) string {
	return strings.Join(cmd[:min(len(cmd), 2)], " ")
}

// demux splits an engine output stream into stdout and stderr.
func demux(r io.Reader) (stdout, stderr []byte, err error) {
	var out, errOut bytes.Buffer
	err = demuxTo(r, &out, &errOut)
	return out.Bytes(), errOut.Bytes(), err
}

// demuxTo copies an engine output stream to stdout and stderr. Without
// a TTY the engine multiplexes both streams into frames, each with an
// 8-byte header: the stream (0 stdin, 1 stdout, 2 stderr), three zero
// bytes, and the big-endian payload size. A stream that does not start
// with such a header is raw TTY output and goes to stdout as-is.
func demuxTo(r io.Reader, stdout, stderr io.Writer) error {
	br := bufio.NewReader(r)
	header := make([]byte, 8)
	first := true
	for {
		if first {
			peek, err := br.Peek(8)
			if len(peek) < 8 || !isFrameHeader(peek) {
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				_, err = io.Copy(stdout, br)
				return err
			}
			first = false
		}
		if _, err := io.ReadFull(br, header); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		dst := stdout
		if header[0] == 2 {
			dst = stderr
		}
		if _, err := io.CopyN(dst, br, size); err != nil {
			return err
		}
	}
}

func isFrameHeader(b []byte) bool {
	return b[0] <= 2 && b[1] == 0 && b[2] == 0 && b[3] == 0
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
