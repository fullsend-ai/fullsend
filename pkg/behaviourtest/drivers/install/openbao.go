// openbao.go runs a feature-scoped OpenBao dev-mode server for the
// behaviour suite's Vault-backed runner secret scenarios (ADR 0155).
// Like the trace collector, the tested Fullsend workflows run on
// separate GitHub-hosted runners, so the server's API is published on
// the CI worker's loopback and exposed to them through a Cloudflare
// quick tunnel (TLS, which Vault connections require). The server
// authenticates the tested workflows with their job OIDC token through
// a JWT auth mount bound to the GitHub Actions issuer, the default
// Fullsend Vault audience and the leased repositories, and serves
// synthetic per-scenario KV v2 secrets under a dedicated prefix through
// a read-only policy. The random root token never leaves the CI worker:
// it reaches the container through podman's environment (never a
// command line) and is registered with the process redactor.
//
// A feature that exercises Vault starts one server, reuses it across
// its scenarios and stops it at feature or job cleanup; features that
// do not exercise Vault never start it.
package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/vaultsecrets"
)

const (
	// envOpenBao controls the Vault behaviour scenarios: unset (auto)
	// enables them in GitHub Actions when podman is available;
	// false/0/off disables them; true/1/on requires them, so a missing
	// prerequisite fails instead of skipping.
	envOpenBao = "BEHAVIOUR_OPENBAO"

	// openBaoImage is the OpenBao server run under Podman in dev mode
	// (in-memory storage, unsealed, KV v2 mounted at secret/).
	openBaoImage = "quay.io/openbao/openbao:2.1.0"

	// openBaoRootTokenEnv carries the dev-mode root token into the
	// container. podman reads its value from its own environment
	// (-e NAME), so the token is never on a command line.
	openBaoRootTokenEnv = "BAO_DEV_ROOT_TOKEN_ID"

	// githubActionsOIDCIssuer is the issuer of the tested workflows' job
	// OIDC tokens. OpenBao fetches its signing keys through discovery.
	githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"

	// openBaoKVMount is the dev-mode KV v2 mount, and openBaoSecretPrefix
	// the prefix under it that holds the synthetic scenario secrets. The
	// runner policy grants read on that prefix only.
	openBaoKVMount      = "secret"
	openBaoSecretPrefix = "fullsend-bt"
	openBaoPolicy       = "fullsend-bt-runner"

	// openBaoTokenTTL bounds the tokens the runner obtains at login.
	openBaoTokenTTL = "10m"

	openBaoContainerPort = 8200
	openBaoStartTimeout  = 10 * time.Minute
	openBaoStopSeconds   = "10"
	openBaoStopTimeout   = 3 * time.Minute
)

// getenvFunc is os.Getenv, replaceable in tests.
var getenvFunc = os.Getenv

// lookPathFunc is exec.LookPath and newOpenBaoFunc is newOpenBao,
// replaceable in tests.
var (
	lookPathFunc   = exec.LookPath
	newOpenBaoFunc = newOpenBao
)

// openBaoRunnerPolicy is the least-privilege ACL policy the JWT role
// attaches: read on the scenario secret prefix and nothing else.
const openBaoRunnerPolicy = `path "` + openBaoKVMount + `/data/` + openBaoSecretPrefix + `/*" {
  capabilities = ["read"]
}
`

// openBaoEnabled decides from the environment whether the Vault
// behaviour scenarios run. skip is a human-readable reason when they do
// not; a non-nil error means the configuration requires them but a
// prerequisite is missing, or the value is unsupported.
func openBaoEnabled(getenv func(string) string, lookPath func(string) (string, error)) (enabled bool, skip string, err error) {
	required := false
	switch v := strings.ToLower(strings.TrimSpace(getenv(envOpenBao))); v {
	case "":
	case "1", "true", "on":
		required = true
	case "0", "false", "off":
		return false, envOpenBao + " disables the Vault scenarios", nil
	default:
		return false, "", fmt.Errorf("%s: unsupported value %q (use true or false)", envOpenBao, v)
	}
	missing := ""
	if getenv("GITHUB_ACTIONS") != "true" {
		missing = "the tested workflows need a job-issued OIDC token, which only GitHub Actions provides"
	} else if _, lerr := lookPath("podman"); lerr != nil {
		missing = "podman not found"
	}
	if missing == "" {
		return true, "", nil
	}
	if required {
		return false, "", fmt.Errorf("%s requires the Vault scenarios, but %s", envOpenBao, missing)
	}
	return false, missing, nil
}

// openBao owns the OpenBao and tunnel containers and the server's
// configuration.
type openBao struct {
	logf         func(string, ...any)
	run          commandRunner
	httpClient   *http.Client
	freePort     func() (int, error)
	readyTimeout time.Duration
	pollInterval time.Duration
	now          func() time.Time

	rootToken     string
	localURL      string
	address       string
	containerName string
	tunnelName    string
	containerUp   bool
	tunnelUp      bool

	// reposMu guards repos, the owner/name values the JWT role binds to
	// the repository claim.
	reposMu sync.Mutex
	repos   []string

	// stopMu serializes Stop; stopped is set only once every cleanup
	// step succeeded, so a failed cleanup can be retried.
	stopMu  sync.Mutex
	stopped bool
}

// newOpenBao returns a server with production dependencies.
func newOpenBao(logf func(string, ...any)) *openBao {
	return &openBao{
		logf:         logf,
		run:          execCommandRunner,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		freePort:     freeLocalPort,
		readyTimeout: 3 * time.Minute,
		pollInterval: time.Second,
		now:          time.Now,
	}
}

// VaultServer is a running feature-scoped OpenBao server, as the Vault
// feature steps see it.
type VaultServer interface {
	// Address is the HTTPS URL the tested workflows reach the server at;
	// it belongs in trusted base configuration as vault_address.
	Address() string
	// AllowRepo lets org/repo's workflows log in with the default
	// Fullsend Vault role and audience.
	AllowRepo(ctx context.Context, org, repo string) error
	// PutSecret writes a synthetic secret and returns its KV v2 read
	// path for vault_secrets.
	PutSecret(ctx context.Context, name string, data map[string]string) (string, error)
	// Stop stops and removes the server. Safe to call more than once.
	Stop(ctx context.Context) error
}

var _ VaultServer = (*openBao)(nil)

// StartVaultServer starts the OpenBao server for a Vault feature when
// the environment enables the Vault scenarios. It returns a nil server
// and a non-empty skip reason when they are disabled or a prerequisite
// is missing (outside GitHub Actions, or without podman), so local runs
// can skip them. Unlike the trace collector, a start failure is
// returned: the feature's scenarios cannot run without the server. When
// the failed start's own cleanup also failed, the partially started
// server is returned with the error, so the caller can retry Stop at
// feature or job cleanup; otherwise the server is nil.
func StartVaultServer(logf func(string, ...any)) (server VaultServer, skip string, err error) {
	enabled, skip, err := openBaoEnabled(getenvFunc, lookPathFunc)
	if err != nil || !enabled {
		return nil, skip, err
	}
	b := newOpenBaoFunc(logf)
	ctx, cancel := context.WithTimeout(context.Background(), openBaoStartTimeout)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		err = fmt.Errorf("starting OpenBao: %w", err)
		if b.cleanupPending() {
			return b, "", err
		}
		return nil, "", err
	}
	return b, "", nil
}

// cleanupPending reports whether Stop still has containers to remove,
// as after a failed start whose cleanup also failed.
func (b *openBao) cleanupPending() bool {
	b.stopMu.Lock()
	defer b.stopMu.Unlock()
	return !b.stopped && (b.containerUp || b.tunnelUp)
}

// Address is the HTTPS tunnel URL the tested workflows reach the server
// at. It belongs in trusted base configuration as the connection's
// vault_address.
func (b *openBao) Address() string { return b.address }

// Start generates the root token, starts the server and the tunnel, and
// configures JWT auth, the runner policy and the role. On failure
// everything started so far is stopped.
func (b *openBao) Start(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			if stopErr := b.Stop(context.Background()); stopErr != nil {
				b.logf("[openbao] cleanup after failed start: %v", stopErr)
			}
		}
	}()
	started := b.now()

	if b.rootToken, err = randomHex(32); err != nil {
		return fmt.Errorf("generating root token: %w", err)
	}
	// Not announced with an add-mask command, for the reason given in
	// traceCollector.Start; the process redactor covers this process.
	security.RegisterRuntimeSecret(b.rootToken)

	suffix, err := randomHex(4)
	if err != nil {
		return fmt.Errorf("generating container suffix: %w", err)
	}
	b.containerName = "bt-openbao-" + suffix
	b.tunnelName = "bt-openbao-tunnel-" + suffix

	apiPort, err := b.freePort()
	if err != nil {
		return fmt.Errorf("choosing API port: %w", err)
	}
	metricsPort, err := b.freePort()
	if err == nil && metricsPort == apiPort {
		// The API port is not bound yet, so it can be handed out again.
		metricsPort, err = b.freePort()
	}
	if err != nil {
		return fmt.Errorf("choosing tunnel metrics port: %w", err)
	}
	if metricsPort == apiPort {
		return fmt.Errorf("choosing tunnel metrics port: got the API port %d twice", apiPort)
	}

	b.containerUp = true
	if out, err := b.run(ctx, []string{openBaoRootTokenEnv + "=" + b.rootToken}, "podman", "run", "-d",
		"--name", b.containerName,
		"-e", openBaoRootTokenEnv,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", apiPort, openBaoContainerPort),
		openBaoImage,
		"server", "-dev", fmt.Sprintf("-dev-listen-address=0.0.0.0:%d", openBaoContainerPort),
	); err != nil {
		return fmt.Errorf("starting OpenBao container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	b.localURL = fmt.Sprintf("http://127.0.0.1:%d", apiPort)

	if err := b.poll(ctx, "OpenBao API", func() (bool, error) {
		_, status, err := b.request(ctx, http.MethodGet, "/v1/sys/health", "", nil)
		return err == nil && status == http.StatusOK, nil
	}); err != nil {
		return err
	}
	if err := b.configure(ctx); err != nil {
		return err
	}

	b.tunnelUp = true
	if out, err := b.run(ctx, nil, "podman", "run", "-d",
		"--name", b.tunnelName,
		"--network", "host",
		cloudflaredImage,
		"tunnel",
		"--metrics", fmt.Sprintf("127.0.0.1:%d", metricsPort),
		"--url", b.localURL,
	); err != nil {
		return fmt.Errorf("starting tunnel container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	host, err := b.awaitTunnel(ctx, fmt.Sprintf("http://127.0.0.1:%d", metricsPort))
	if err != nil {
		return err
	}
	b.address = "https://" + host
	b.logf("[openbao] serving Vault scenarios at %s (startup %s, memory %s)",
		b.address, b.now().Sub(started).Round(time.Millisecond), b.memoryUsage(ctx))
	return nil
}

// configure enables JWT auth for the GitHub Actions issuer and writes
// the runner policy. The role is written by AllowRepo, because a role
// must bind the repositories whose workflows may log in.
func (b *openBao) configure(ctx context.Context) error {
	steps := []struct {
		what   string
		method string
		path   string
		body   any
	}{
		{"enabling JWT auth", http.MethodPost, "/v1/sys/auth/" + vaultsecrets.DefaultAuthPath, map[string]any{"type": "jwt"}},
		{"configuring JWT auth", http.MethodPost, "/v1/auth/" + vaultsecrets.DefaultAuthPath + "/config", map[string]any{
			"oidc_discovery_url": githubActionsOIDCIssuer,
			"bound_issuer":       githubActionsOIDCIssuer,
		}},
		{"writing runner policy", http.MethodPut, "/v1/sys/policies/acl/" + openBaoPolicy, map[string]any{"policy": openBaoRunnerPolicy}},
	}
	for _, s := range steps {
		if err := b.admin(ctx, s.method, s.path, s.body); err != nil {
			return fmt.Errorf("%s: %w", s.what, err)
		}
	}
	return nil
}

// AllowRepo lets the workflows of org/repo log in with the default
// Fullsend role and audience. The role binds the job token's repository
// claim to every allowed repository and attaches only the runner policy.
func (b *openBao) AllowRepo(ctx context.Context, org, repo string) error {
	if org == "" || repo == "" || strings.ContainsAny(org+repo, "/*") {
		return fmt.Errorf("allowing repository: invalid name %q/%q", org, repo)
	}
	b.reposMu.Lock()
	defer b.reposMu.Unlock()
	full := org + "/" + repo
	repos := b.repos
	if !slices.Contains(repos, full) {
		repos = append(slices.Clone(repos), full)
		slices.Sort(repos)
	}
	if err := b.admin(ctx, http.MethodPost, "/v1/auth/"+vaultsecrets.DefaultAuthPath+"/role/"+vaultsecrets.DefaultRole, map[string]any{
		"role_type":       "jwt",
		"user_claim":      "repository",
		"bound_audiences": []string{vaultsecrets.DefaultAudience},
		"bound_claims":    map[string]any{"repository": repos},
		"token_policies":  []string{openBaoPolicy},
		"token_ttl":       openBaoTokenTTL,
		"token_max_ttl":   openBaoTokenTTL,
	}); err != nil {
		return fmt.Errorf("writing role %s: %w", vaultsecrets.DefaultRole, err)
	}
	b.repos = repos
	return nil
}

// PutSecret writes a synthetic scenario secret at name under the
// scenario prefix and returns its KV v2 read path, as a vault_secrets
// entry expects it. Values are registered with the process redactor.
func (b *openBao) PutSecret(ctx context.Context, name string, data map[string]string) (string, error) {
	if err := validateOpenBaoSecretName(name); err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("writing secret %s: no fields", name)
	}
	for _, v := range data {
		security.RegisterRuntimeSecret(v)
	}
	path := openBaoKVMount + "/data/" + openBaoSecretPrefix + "/" + name
	if err := b.admin(ctx, http.MethodPost, "/v1/"+path, map[string]any{"data": data}); err != nil {
		return "", fmt.Errorf("writing secret %s: %w", name, err)
	}
	return path, nil
}

// validateOpenBaoSecretName accepts slash-separated components of
// letters, digits, '-', '_' and '.', excluding "." and "..".
func validateOpenBaoSecretName(name string) error {
	if name == "" {
		return errors.New("secret name is empty")
	}
	for c := range strings.SplitSeq(name, "/") {
		if c == "" || c == "." || c == ".." {
			return fmt.Errorf("secret name %q has an empty, \".\" or \"..\" component", name)
		}
		for _, r := range c {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return fmt.Errorf("secret name %q contains %q", name, r)
			}
		}
	}
	return nil
}

// admin sends a root-token request and fails on any non-2xx status.
// Response bodies are not included in errors: they can echo secrets.
func (b *openBao) admin(ctx context.Context, method, path string, body any) error {
	_, status, err := b.request(ctx, method, path, b.rootToken, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("%s %s: HTTP %d", method, path, status)
	}
	return nil
}

func (b *openBao) request(ctx context.Context, method, path, token string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.localURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return out, resp.StatusCode, err
}

// awaitTunnel waits until cloudflared has registered a connection and
// reports its quick-tunnel hostname.
func (b *openBao) awaitTunnel(ctx context.Context, metricsURL string) (string, error) {
	var host string
	err := b.poll(ctx, "cloudflared quick tunnel", func() (bool, error) {
		if host == "" {
			body, status, err := b.get(ctx, metricsURL+"/quicktunnel")
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
		_, status, err := b.get(ctx, metricsURL+"/ready")
		return err == nil && status == http.StatusOK, nil
	})
	return host, err
}

func (b *openBao) get(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return body, resp.StatusCode, err
}

// memoryUsage reports the server container's memory use for the
// startup log line, or "unknown" when podman cannot tell.
func (b *openBao) memoryUsage(ctx context.Context) string {
	out, err := b.run(ctx, nil, "podman", "stats", "--no-stream", "--format", "{{.MemUsage}}", b.containerName)
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return "unknown"
}

// poll calls check every pollInterval until it reports done, returns an
// error, or readyTimeout elapses.
func (b *openBao) poll(ctx context.Context, what string, check func() (bool, error)) error {
	deadline := time.Now().Add(b.readyTimeout)
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", b.readyTimeout, what)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-time.After(b.pollInterval):
		}
	}
}

// Stop stops the tunnel first (no new logins), then the server, and
// removes both containers. The server keeps everything in memory, so
// removing it discards the root token and the synthetic secrets.
// Cleanup runs under its own bounded context, and a failed step is
// retried by a later call. Safe to call more than once.
func (b *openBao) Stop(_ context.Context) error {
	b.stopMu.Lock()
	defer b.stopMu.Unlock()
	if b.stopped {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), openBaoStopTimeout)
	defer cancel()
	var errs []error
	for _, c := range []struct {
		name string
		up   *bool
	}{{b.tunnelName, &b.tunnelUp}, {b.containerName, &b.containerUp}} {
		if !*c.up {
			continue
		}
		if out, err := b.run(ctx, nil, "podman", "stop", "--time", openBaoStopSeconds, c.name); err != nil &&
			!strings.Contains(strings.ToLower(string(out)), "no such container") {
			errs = append(errs, fmt.Errorf("stopping container %s: %w: %s", c.name, err, strings.TrimSpace(string(out))))
		}
		out, err := b.run(ctx, nil, "podman", "rm", "-f", c.name)
		if err != nil && !strings.Contains(strings.ToLower(string(out)), "no such container") {
			errs = append(errs, fmt.Errorf("removing container %s: %w: %s", c.name, err, strings.TrimSpace(string(out))))
			continue
		}
		*c.up = false
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	b.rootToken = ""
	b.stopped = true
	return nil
}
