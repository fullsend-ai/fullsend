package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/vaultsecrets"
)

func TestOpenBaoEnabled(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/podman", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	tests := []struct {
		name     string
		env      map[string]string
		lookPath func(string) (string, error)
		want     bool
		wantSkip string
		wantErr  string
	}{
		{name: "auto on in actions with podman", env: map[string]string{"GITHUB_ACTIONS": "true"}, lookPath: found, want: true},
		{name: "auto skips outside actions", lookPath: found, wantSkip: "only GitHub Actions"},
		{name: "auto skips without podman", env: map[string]string{"GITHUB_ACTIONS": "true"}, lookPath: missing, wantSkip: "podman not found"},
		{name: "disabled", env: map[string]string{envOpenBao: "off", "GITHUB_ACTIONS": "true"}, lookPath: found, wantSkip: "disables"},
		{name: "required and available", env: map[string]string{envOpenBao: "TRUE", "GITHUB_ACTIONS": "true"}, lookPath: found, want: true},
		{name: "required fails without podman", env: map[string]string{envOpenBao: "1", "GITHUB_ACTIONS": "true"}, lookPath: missing, wantErr: "podman not found"},
		{name: "required fails outside actions", env: map[string]string{envOpenBao: "on"}, lookPath: found, wantErr: "job-issued OIDC token"},
		{name: "invalid", env: map[string]string{envOpenBao: "maybe"}, lookPath: found, wantErr: "unsupported value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skip, err := openBaoEnabled(func(k string) string { return tt.env[k] }, tt.lookPath)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.False(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			if tt.wantSkip == "" {
				assert.Empty(t, skip)
			} else {
				assert.Contains(t, skip, tt.wantSkip)
			}
		})
	}
}

func TestStartVaultServer_SkipsWhenDisabled(t *testing.T) {
	prev := getenvFunc
	t.Cleanup(func() { getenvFunc = prev })
	getenvFunc = func(k string) string {
		if k == envOpenBao {
			return "false"
		}
		return ""
	}
	server, skip, err := StartVaultServer(t.Logf)
	require.NoError(t, err)
	assert.Nil(t, server)
	assert.NotEmpty(t, skip)

	getenvFunc = func(k string) string {
		if k == envOpenBao {
			return "bogus"
		}
		return ""
	}
	server, _, err = StartVaultServer(t.Logf)
	require.ErrorContains(t, err, "unsupported value")
	assert.Nil(t, server)
}

func TestStartVaultServer_FailedStartKeepsHandleWhenCleanupFails(t *testing.T) {
	prevEnv, prevLook, prevNew := getenvFunc, lookPathFunc, newOpenBaoFunc
	t.Cleanup(func() { getenvFunc, lookPathFunc, newOpenBaoFunc = prevEnv, prevLook, prevNew })
	getenvFunc = func(k string) string {
		if k == "GITHUB_ACTIONS" {
			return "true"
		}
		return ""
	}
	lookPathFunc = func(string) (string, error) { return "/usr/bin/podman", nil }

	setup := func(t *testing.T, failOn string) (*fakePodman, *openBao) {
		f := newFakeBao(t)
		f.failPath = "/v1/auth/jwt/config"
		p := &fakePodman{}
		b, _ := newTestOpenBao(t, f, p)
		newOpenBaoFunc = func(func(string, ...any)) *openBao {
			b.run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if failOn != "" && strings.Contains(strings.Join(args, " "), failOn) {
					failOn = ""
					return []byte("storage busy"), errors.New("exit status 1")
				}
				return p.run(ctx, env, name, args...)
			}
			return b
		}
		return p, b
	}

	t.Run("cleanup fails", func(t *testing.T) {
		p, b := setup(t, "rm -f bt-openbao-")
		server, _, err := StartVaultServer(t.Logf)
		require.ErrorContains(t, err, "configuring JWT auth")
		require.NotNil(t, server, "the handle is kept so a later Stop can retry")
		assert.True(t, b.containerUp)

		require.NoError(t, server.Stop(context.Background()))
		assert.False(t, b.containerUp)
		cmds := p.joined()
		assert.Equal(t, "podman rm -f "+b.containerName, cmds[len(cmds)-1])
	})
	t.Run("cleanup succeeds", func(t *testing.T) {
		_, _ = setup(t, "")
		server, _, err := StartVaultServer(t.Logf)
		require.ErrorContains(t, err, "configuring JWT auth")
		assert.Nil(t, server)
	})
}

func TestNewOpenBao_Defaults(t *testing.T) {
	b := newOpenBao(t.Logf)
	assert.NotNil(t, b.run)
	assert.NotNil(t, b.httpClient)
	assert.NotNil(t, b.freePort)
	assert.NotNil(t, b.now)
	assert.Positive(t, b.readyTimeout)
	assert.Positive(t, b.pollInterval)
	assert.Empty(t, b.Address())
}

func TestOpenBaoRunnerPolicy_ReadOnlyPrefix(t *testing.T) {
	assert.Equal(t, "path \"secret/data/fullsend-bt/*\" {\n  capabilities = [\"read\"]\n}\n", openBaoRunnerPolicy)
}

// fakeBaoCall is one request the fake OpenBao API received.
type fakeBaoCall struct {
	Method string
	Path   string
	Token  string
	Body   map[string]any
}

// fakeBao is an httptest OpenBao API and a cloudflared metrics endpoint.
type fakeBao struct {
	api     *httptest.Server
	metrics *httptest.Server

	mu         sync.Mutex
	calls      []fakeBaoCall
	failPath   string
	tunnelHost string
}

func newFakeBao(t *testing.T) *fakeBao {
	t.Helper()
	f := &fakeBao{tunnelHost: "bao-test.trycloudflare.com"}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sys/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.calls = append(f.calls, fakeBaoCall{Method: r.Method, Path: r.URL.Path, Token: r.Header.Get("X-Vault-Token"), Body: body})
		fail := f.failPath == r.URL.Path
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied: echoed-secret"]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.api.Close)
	f.metrics = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/quicktunnel":
			f.mu.Lock()
			host := f.tunnelHost
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"hostname":%q}`, host)
		case "/ready":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.metrics.Close)
	return f
}

func (f *fakeBao) recorded() []fakeBaoCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeBaoCall(nil), f.calls...)
}

// fakePodman records podman invocations. failOn makes the first command
// whose arguments contain the substring fail with output.
type fakePodman struct {
	mu       sync.Mutex
	commands [][]string
	envs     [][]string
	failOn   string
	failOut  string
}

func (p *fakePodman) run(_ context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cmd := append([]string{name}, args...)
	p.commands = append(p.commands, cmd)
	p.envs = append(p.envs, extraEnv)
	joined := strings.Join(cmd, " ")
	if p.failOn != "" && strings.Contains(joined, p.failOn) {
		p.failOn = ""
		return []byte(p.failOut), errors.New("exit status 125")
	}
	if len(args) > 0 && args[0] == "stats" {
		return []byte("12.5MB / 7.7GB\n"), nil
	}
	return nil, nil
}

func (p *fakePodman) joined() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.commands))
	for i, c := range p.commands {
		out[i] = strings.Join(c, " ")
	}
	return out
}

func newTestOpenBao(t *testing.T, f *fakeBao, p *fakePodman) (*openBao, *[]string) {
	t.Helper()
	ports := []int{serverPort(t, f.api), serverPort(t, f.metrics)}
	var logs []string
	var logMu sync.Mutex
	b := &openBao{
		logf: func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		},
		run:        p.run,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		freePort: func() (int, error) {
			if len(ports) == 0 {
				return 0, errors.New("no more ports")
			}
			port := ports[0]
			ports = ports[1:]
			return port, nil
		},
		readyTimeout: 5 * time.Second,
		pollInterval: 10 * time.Millisecond,
		now:          time.Now,
	}
	return b, &logs
}

func TestOpenBaoStart_ConfiguresServerAndTunnel(t *testing.T) {
	f := newFakeBao(t)
	p := &fakePodman{}
	b, logs := newTestOpenBao(t, f, p)

	require.NoError(t, b.Start(context.Background()))
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	assert.Equal(t, "https://bao-test.trycloudflare.com", b.Address())

	cmds := p.joined()
	require.GreaterOrEqual(t, len(cmds), 3)
	assert.Contains(t, cmds[0], "podman run -d --name "+b.containerName+" -e "+openBaoRootTokenEnv+" ")
	assert.Contains(t, cmds[0], fmt.Sprintf("127.0.0.1:%d:8200", serverPort(t, f.api)))
	assert.Contains(t, cmds[0], openBaoImage+" server -dev -dev-listen-address=0.0.0.0:8200")
	assert.NotContains(t, cmds[0], b.rootToken, "the root token must not be on a command line")
	assert.Equal(t, []string{openBaoRootTokenEnv + "=" + b.rootToken}, p.envs[0])
	assert.Contains(t, cmds[1], "--url http://127.0.0.1:")
	assert.Contains(t, cmds[1], cloudflaredImage)
	assert.Contains(t, cmds[2], "podman stats --no-stream")

	calls := f.recorded()
	require.Len(t, calls, 3)
	for _, c := range calls {
		assert.Equal(t, b.rootToken, c.Token)
	}
	assert.Equal(t, "/v1/sys/auth/jwt", calls[0].Path)
	assert.Equal(t, "jwt", calls[0].Body["type"])
	assert.Equal(t, "/v1/auth/jwt/config", calls[1].Path)
	assert.Equal(t, githubActionsOIDCIssuer, calls[1].Body["oidc_discovery_url"])
	assert.Equal(t, githubActionsOIDCIssuer, calls[1].Body["bound_issuer"])
	assert.Equal(t, http.MethodPut, calls[2].Method)
	assert.Equal(t, "/v1/sys/policies/acl/"+openBaoPolicy, calls[2].Path)
	assert.Equal(t, openBaoRunnerPolicy, calls[2].Body["policy"])

	require.NotEmpty(t, *logs)
	last := (*logs)[len(*logs)-1]
	assert.Contains(t, last, "startup ")
	assert.Contains(t, last, "memory 12.5MB / 7.7GB")
	assert.NotContains(t, strings.Join(*logs, "\n"), b.rootToken)
}

func TestOpenBaoAllowRepoAndPutSecret(t *testing.T) {
	f := newFakeBao(t)
	p := &fakePodman{}
	b, _ := newTestOpenBao(t, f, p)
	require.NoError(t, b.Start(context.Background()))
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	ctx := context.Background()

	require.NoError(t, b.AllowRepo(ctx, "bt-org", "repo-b"))
	require.NoError(t, b.AllowRepo(ctx, "bt-org", "repo-a"))
	require.NoError(t, b.AllowRepo(ctx, "bt-org", "repo-b"))
	for _, bad := range [][2]string{{"", "r"}, {"o", ""}, {"o/x", "r"}, {"o", "r*"}} {
		require.Error(t, b.AllowRepo(ctx, bad[0], bad[1]), "%q/%q", bad[0], bad[1])
	}

	path, err := b.PutSecret(ctx, "scenario-1/traces", map[string]string{"value": "Authorization=Bearer%20synthetic"})
	require.NoError(t, err)
	assert.Equal(t, "secret/data/fullsend-bt/scenario-1/traces", path)
	for _, bad := range []string{"", "/x", "x/", "a/../b", ".", "a b", "a?b"} {
		_, err := b.PutSecret(ctx, bad, map[string]string{"value": "v"})
		require.Error(t, err, "name %q", bad)
	}
	_, err = b.PutSecret(ctx, "empty", nil)
	require.ErrorContains(t, err, "no fields")

	calls := f.recorded()[3:]
	require.Len(t, calls, 4)
	rolePath := "/v1/auth/jwt/role/" + vaultsecrets.DefaultRole
	last := calls[2]
	assert.Equal(t, rolePath, last.Path)
	assert.Equal(t, "jwt", last.Body["role_type"])
	assert.Equal(t, "repository", last.Body["user_claim"])
	assert.Equal(t, []any{vaultsecrets.DefaultAudience}, last.Body["bound_audiences"])
	assert.Equal(t, map[string]any{"repository": []any{"bt-org/repo-a", "bt-org/repo-b"}}, last.Body["bound_claims"])
	assert.Equal(t, []any{openBaoPolicy}, last.Body["token_policies"])
	assert.Equal(t, map[string]any{"repository": []any{"bt-org/repo-b"}}, calls[0].Body["bound_claims"])

	secret := calls[3]
	assert.Equal(t, "/v1/secret/data/fullsend-bt/scenario-1/traces", secret.Path)
	assert.Equal(t, map[string]any{"data": map[string]any{"value": "Authorization=Bearer%20synthetic"}}, secret.Body)
	assert.Equal(t, b.rootToken, secret.Token)
}

func TestOpenBaoAllowRepo_FailureKeepsBoundRepos(t *testing.T) {
	f := newFakeBao(t)
	b, _ := newTestOpenBao(t, f, &fakePodman{})
	require.NoError(t, b.Start(context.Background()))
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	require.NoError(t, b.AllowRepo(context.Background(), "o", "a"))
	f.mu.Lock()
	f.failPath = "/v1/auth/jwt/role/" + vaultsecrets.DefaultRole
	f.mu.Unlock()
	err := b.AllowRepo(context.Background(), "o", "b")
	require.ErrorContains(t, err, "HTTP 403")
	assert.NotContains(t, err.Error(), "echoed-secret", "response bodies must stay out of errors")
	assert.Equal(t, []string{"o/a"}, b.repos)
}

func TestOpenBaoStart_FailureCleansUp(t *testing.T) {
	t.Run("container start", func(t *testing.T) {
		f := newFakeBao(t)
		p := &fakePodman{failOn: "server -dev", failOut: "image pull failed"}
		b, _ := newTestOpenBao(t, f, p)
		err := b.Start(context.Background())
		require.ErrorContains(t, err, "starting OpenBao container")
		require.ErrorContains(t, err, "image pull failed")
		cmds := p.joined()
		assert.Contains(t, cmds, "podman rm -f "+b.containerName)
		assert.False(t, b.containerUp)
		assert.Empty(t, b.rootToken)
	})
	t.Run("configuration", func(t *testing.T) {
		f := newFakeBao(t)
		f.failPath = "/v1/auth/jwt/config"
		p := &fakePodman{}
		b, _ := newTestOpenBao(t, f, p)
		err := b.Start(context.Background())
		require.ErrorContains(t, err, "configuring JWT auth")
		cmds := p.joined()
		assert.Contains(t, cmds, "podman rm -f "+b.containerName)
		for _, c := range cmds {
			assert.NotContains(t, c, cloudflaredImage, "no tunnel before the server is configured")
		}
	})
	t.Run("unexpected tunnel host", func(t *testing.T) {
		f := newFakeBao(t)
		f.tunnelHost = "evil.example.com"
		p := &fakePodman{}
		b, _ := newTestOpenBao(t, f, p)
		err := b.Start(context.Background())
		require.ErrorContains(t, err, "unexpected tunnel hostname")
		cmds := p.joined()
		assert.Contains(t, cmds, "podman rm -f "+b.tunnelName)
		assert.Contains(t, cmds, "podman rm -f "+b.containerName)
		assert.Empty(t, b.Address())
	})
	t.Run("port allocation", func(t *testing.T) {
		f := newFakeBao(t)
		p := &fakePodman{}
		b, _ := newTestOpenBao(t, f, p)
		same := serverPort(t, f.api)
		b.freePort = func() (int, error) { return same, nil }
		require.ErrorContains(t, b.Start(context.Background()), "got the API port")
		assert.Empty(t, p.joined())
	})
}

func TestOpenBaoStop_RetriesAndIsIdempotent(t *testing.T) {
	f := newFakeBao(t)
	p := &fakePodman{}
	b, _ := newTestOpenBao(t, f, p)
	require.NoError(t, b.Start(context.Background()))

	p.mu.Lock()
	p.failOn = "rm -f " + b.containerName
	p.failOut = "storage busy"
	p.mu.Unlock()
	err := b.Stop(context.Background())
	require.ErrorContains(t, err, "storage busy")
	assert.False(t, b.tunnelUp)
	assert.True(t, b.containerUp)
	assert.NotEmpty(t, b.rootToken)

	require.NoError(t, b.Stop(context.Background()))
	assert.False(t, b.containerUp)
	assert.Empty(t, b.rootToken)
	n := len(p.joined())
	require.NoError(t, b.Stop(context.Background()))
	assert.Len(t, p.joined(), n, "a completed Stop runs nothing again")
}

func TestOpenBaoStop_MissingContainerIsNotAnError(t *testing.T) {
	p := &fakePodman{}
	b := &openBao{run: func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		_, _ = p.run(ctx, env, name, args...)
		return []byte("Error: no such container bt-openbao-x"), errors.New("exit status 1")
	}, containerName: "bt-openbao-x", containerUp: true}
	require.NoError(t, b.Stop(context.Background()))
	assert.Equal(t, []string{"podman stop --time 10 bt-openbao-x", "podman rm -f bt-openbao-x"}, p.joined())
}

func TestOpenBaoPoll_Timeout(t *testing.T) {
	b := &openBao{readyTimeout: 30 * time.Millisecond, pollInterval: 5 * time.Millisecond}
	err := b.poll(context.Background(), "thing", func() (bool, error) { return false, nil })
	require.ErrorContains(t, err, "timed out")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.readyTimeout = time.Minute
	err = b.poll(ctx, "thing", func() (bool, error) { return false, nil })
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpenBaoMemoryUsage_Unknown(t *testing.T) {
	b := &openBao{run: func(context.Context, []string, string, ...string) ([]byte, error) {
		return nil, errors.New("no stats")
	}}
	assert.Equal(t, "unknown", b.memoryUsage(context.Background()))
}
