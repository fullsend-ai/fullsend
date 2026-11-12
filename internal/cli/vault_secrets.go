package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/vaultsecrets"
)

// vaultIDTokenEnv names the GitLab CI ID token a Vault connection
// authenticates with (ADR 0155). The job must declare it under id_tokens
// with the connection's vault_audience; Fullsend cannot change the
// audience of an already-issued token.
const vaultIDTokenEnv = "FULLSEND_VAULT_ID_TOKEN"

// Seams for tests.
var (
	// vaultEnvFn reads environment variables. Override in tests to stub the
	// environment.
	vaultEnvFn = os.Getenv
	// vaultHTTPClient overrides the HTTP client for Vault login and KV
	// reads. Nil keeps the resolver's default; set it in tests to point at
	// a fake Vault server.
	vaultHTTPClient *http.Client
	// vaultOIDCClient overrides the HTTP client for the GitHub Actions OIDC
	// token request. Nil keeps the default; set it in tests to stub the
	// token endpoint.
	vaultOIDCClient *http.Client
	// vaultGitLabAPIClient is the client for the GitLab project lookup that
	// decides whether {{org}} is available. Nil uses a client with a timeout.
	vaultGitLabAPIClient *http.Client
)

// resolveVaultRunnerSecrets resolves the runner secrets the trusted
// configuration selects from a Vault connection (ADR 0155) and returns
// name → value. It returns nil with no network access when no secret
// selects a non-native connection, so native delivery is unchanged.
//
// An explicit Vault selection fails closed: the run must have a supported
// job OIDC identity (GitHub Actions or GitLab CI), every path must expand
// and validate before Vault is contacted, and a lookup failure never falls
// back to the native value. Which runs may obtain Vault values is decided
// on the Vault side, by the WIF policy on the job's OIDC token claims; the
// client does not restrict secret resolution by event or workflow type.
func resolveVaultRunnerSecrets(ctx context.Context, cfg config.ConfigReader, forgePlatform string) (map[string]string, error) {
	if cfg == nil {
		return nil, nil
	}
	reader, ok := cfg.(config.SecretSourcesReader)
	if !ok {
		return nil, nil
	}
	if err := config.ValidateRuntimeSecretSources(cfg); err != nil {
		return nil, err
	}
	sources := vaultSelectedSources(reader)
	if len(sources) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(sources))
	for _, src := range sources {
		names = append(names, src.Name)
	}
	platform, err := vaultJobPlatform(forgePlatform)
	if err != nil {
		return nil, fmt.Errorf("refusing Vault resolution for %s: %w", strings.Join(names, ", "), err)
	}
	repo, err := vaultRepoContext(ctx, platform, sourcesUseOrg(sources))
	if err != nil {
		return nil, fmt.Errorf("Vault resolution for %s: %w", strings.Join(names, ", "), err)
	}
	resolver := vaultsecrets.Resolver{
		Repo:       repo,
		Token:      vaultTokenSource(platform),
		HTTPClient: vaultHTTPClient,
	}
	values, err := resolver.Resolve(ctx, sources)
	if err != nil {
		return nil, fmt.Errorf("Vault resolution: %w", err)
	}
	return values, nil
}

// vaultSelectedSources returns, sorted by name, the secrets whose
// connection is a declared Vault connection.
func vaultSelectedSources(r config.SecretSourcesReader) []vaultsecrets.Source {
	conns := r.ConfigConnections()
	var out []vaultsecrets.Source
	for name, s := range r.ConfigSecrets() {
		if s.Connection == "" || s.Connection == config.NativeSecretConnection {
			continue
		}
		c, ok := conns[s.Connection]
		if !ok || c.Kind != config.SecretConnectionKindVault {
			continue
		}
		out = append(out, vaultsecrets.Source{
			Name:           name,
			ConnectionName: s.Connection,
			Connection: vaultsecrets.Connection{
				Address:   c.VaultAddress,
				Namespace: c.VaultNamespace,
				AuthPath:  c.VaultAuthPath,
				Role:      c.VaultRole,
				Audience:  c.VaultAudience,
			},
			Paths: s.VaultSecrets,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sourcesUseOrg reports whether any selected path references {{org}}.
func sourcesUseOrg(sources []vaultsecrets.Source) bool {
	for _, s := range sources {
		for _, p := range s.Paths {
			if strings.Contains(p, "{{org}}") {
				return true
			}
		}
	}
	return false
}

// vaultJobPlatform returns the CI platform whose job OIDC token
// authenticates to Vault: github (GitHub Actions) or gitlab (GitLab CI).
// Forgejo and local runs have no supported job OIDC integration and
// fail closed; there is no static Vault token fallback.
func vaultJobPlatform(forgePlatform string) (string, error) {
	switch {
	case forgePlatform == repos.ForgeGitHub && vaultEnvFn("GITHUB_ACTIONS") == "true" &&
		vaultEnvFn("GITEA_ACTIONS") == "" && vaultEnvFn("FORGEJO_ACTIONS") == "":
		return repos.ForgeGitHub, nil
	case forgePlatform == repos.ForgeGitLab && vaultEnvFn("GITLAB_CI") == "true":
		return repos.ForgeGitLab, nil
	}
	return "", fmt.Errorf("an explicit Vault selection needs a job OIDC identity, which is supported only on GitHub Actions and GitLab CI (forge %q); select the native connection or run in a supported CI job", forgePlatform)
}

// vaultTokenSource returns the platform's job OIDC token source.
func vaultTokenSource(platform string) vaultsecrets.TokenSource {
	if platform == repos.ForgeGitLab {
		return func(_ context.Context, audience string) (string, error) {
			token := strings.TrimSpace(vaultEnvFn(vaultIDTokenEnv))
			if token == "" {
				return "", fmt.Errorf("%s is not set; declare it under id_tokens with aud %q in the GitLab job", vaultIDTokenEnv, audience)
			}
			if !jwtHasAudience(token, audience) {
				return "", fmt.Errorf("%s was not issued for audience %q; set its id_tokens aud to the connection's vault_audience", vaultIDTokenEnv, audience)
			}
			return token, nil
		}
	}
	return func(ctx context.Context, audience string) (string, error) {
		a, err := actionsoidc.FetchAssertion(ctx, actionsoidc.AssertionConfig{
			Audience:         audience,
			OIDCRequestURL:   vaultEnvFn("ACTIONS_ID_TOKEN_REQUEST_URL"),
			OIDCRequestToken: vaultEnvFn("ACTIONS_ID_TOKEN_REQUEST_TOKEN"),
			HTTPClient:       vaultOIDCClient,
		})
		if err != nil {
			return "", err
		}
		return a.Value, nil
	}
}

// jwtHasAudience reports whether the unverified payload of token carries
// audience in its aud claim (a string or a list). Vault verifies the
// token; this only gives an early, clear error for a mismatched job
// declaration.
func jwtHasAudience(token, audience string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	if json.Unmarshal(payload, &claims) != nil || len(claims.Aud) == 0 {
		return false
	}
	var single string
	if json.Unmarshal(claims.Aud, &single) == nil {
		return single == audience
	}
	var list []string
	if json.Unmarshal(claims.Aud, &list) == nil {
		for _, a := range list {
			if a == audience {
				return true
			}
		}
	}
	return false
}

// vaultRepoContext builds the path-template context from the CI
// platform's own job variables, never from PR-head files or event-supplied
// repository names. needsOrg says whether any selected path references
// {{org}}, which lets GitLab skip the namespace lookup otherwise.
func vaultRepoContext(ctx context.Context, platform string, needsOrg bool) (vaultsecrets.RepoContext, error) {
	if platform == repos.ForgeGitLab {
		full := vaultEnvFn("CI_PROJECT_PATH")
		host := vaultEnvFn("CI_SERVER_HOST")
		if full == "" || host == "" {
			return vaultsecrets.RepoContext{}, fmt.Errorf("CI_PROJECT_PATH and CI_SERVER_HOST are required to expand Vault paths")
		}
		comps := strings.Split(full, "/")
		owner := vaultEnvFn("CI_PROJECT_ROOT_NAMESPACE")
		if owner == "" {
			owner = comps[0]
		}
		rc := vaultsecrets.RepoContext{
			Forge:    repos.ForgeGitLab,
			Host:     host,
			Owner:    owner,
			Name:     comps[len(comps)-1],
			FullName: full,
		}
		// {{org}} is available only when the project belongs to a group;
		// a personal namespace leaves it unavailable. The job variables do
		// not say which, so ask GitLab, and fail closed when it cannot tell.
		if needsOrg && gitlabNamespaceIsGroup(ctx) {
			rc.Org = owner
		}
		return rc, nil
	}
	full := vaultEnvFn("GITHUB_REPOSITORY")
	owner, name, ok := strings.Cut(full, "/")
	if !ok || owner == "" || name == "" {
		return vaultsecrets.RepoContext{}, fmt.Errorf("GITHUB_REPOSITORY is required to expand Vault paths")
	}
	host := "github.com"
	if server := vaultEnvFn("GITHUB_SERVER_URL"); server != "" {
		u, err := url.Parse(server)
		if err != nil || u.Hostname() == "" {
			return vaultsecrets.RepoContext{}, fmt.Errorf("GITHUB_SERVER_URL is not a valid URL")
		}
		host = u.Hostname()
	}
	rc := vaultsecrets.RepoContext{
		Forge:    repos.ForgeGitHub,
		Host:     host,
		Owner:    owner,
		Name:     name,
		FullName: full,
	}
	if githubOwnerIsOrganization() {
		rc.Org = owner
	}
	return rc, nil
}

// gitlabNamespaceIsGroup reports whether the Projects API, queried with the
// job's own token, says the running project belongs to a group. Any failure
// to tell reports false, which leaves {{org}} unavailable and so fails
// closed for templates that use it.
func gitlabNamespaceIsGroup(ctx context.Context) bool {
	apiURL := strings.TrimRight(vaultEnvFn("CI_API_V4_URL"), "/")
	projectID := vaultEnvFn("CI_PROJECT_ID")
	jobToken := vaultEnvFn("CI_JOB_TOKEN")
	if apiURL == "" || projectID == "" || jobToken == "" {
		return false
	}
	// The job token is sent only over HTTPS.
	if u, err := url.Parse(apiURL); err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/projects/"+url.PathEscape(projectID), nil)
	if err != nil {
		return false
	}
	req.Header.Set("JOB-TOKEN", jobToken)
	client := vaultGitLabAPIClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Go forwards a custom JOB-TOKEN header on redirects, so refuse them on
	// a copy of the client (the injected client is left untouched).
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var project struct {
		Namespace struct {
			Kind string `json:"kind"`
		} `json:"namespace"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&project) != nil {
		return false
	}
	return project.Namespace.Kind == "group"
}

// githubOwnerIsOrganization reports whether the GitHub-written event
// payload says the repository owner is an organization. An unreadable
// payload leaves {{org}} unavailable, which fails closed for templates
// that use it.
func githubOwnerIsOrganization() bool {
	path := vaultEnvFn("GITHUB_EVENT_PATH")
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var ev struct {
		Repository struct {
			Owner struct {
				Type string `json:"type"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return false
	}
	return ev.Repository.Owner.Type == "Organization"
}

// applyVaultRunnerSecrets hands Vault-resolved values to their registered
// consumers. JIRA_TOKEN and the OTLP header variables are set in this
// process's environment, where the native source puts them: the trace
// exporter reads the OTLP headers there. Fullsend's own Jira client does not
// read JIRA_TOKEN (it uses FULLSEND_JIRA_TOKEN or OAuth credentials), so a
// Vault-resolved JIRA_TOKEN reaches only host-side scripts that declare it
// in env.runner; isInheritedScriptDenyKey keeps all three out of host-side
// scripts unless a harness declares them. sandboxDeniedKeys keeps them out of
// every sandbox-bound harness site. FULLSEND_GITHUB_RUNNER_SECRETS is
// parsed with the native bundle rules and replaces any native bundle; it
// is never placed in the environment. Every value is masked and
// registered for redaction first.
func applyVaultRunnerSecrets(values map[string]string, native map[string]string) (map[string]string, error) {
	runnerSecrets := native
	for _, name := range sortedStringKeys(values) {
		v := values[name]
		maskRunnerSecretValue(v)
		// A bundle's own entries are checked by parseRunnerSecrets; every
		// other value must be long enough to redact from logs. The error
		// never quotes the value.
		if !registerRunnerSecretValue(v) && name != forge.SecretGitHubRunnerSecrets {
			return nil, fmt.Errorf("%s from Vault: value is too short to redact from logs", name)
		}
		if isOTLPHeadersSecret(name) {
			for _, part := range otlpHeaderValueForms(v) {
				maskRunnerSecretValue(part)
				registerRunnerSecretValue(part)
			}
		}
		if name == forge.SecretGitHubRunnerSecrets {
			parsed, err := parseRunnerSecrets("Vault-resolved "+name, v)
			if err != nil {
				return nil, err
			}
			runnerSecrets = parsed
			continue
		}
		if err := os.Setenv(name, v); err != nil {
			return nil, fmt.Errorf("setting %s: %w", name, err)
		}
	}
	return runnerSecrets, nil
}

// isOTLPHeadersSecret reports whether name is one of the OTLP header
// variables, whose value is a comma-separated list of key=value entries.
func isOTLPHeadersSecret(name string) bool {
	return name == "OTEL_EXPORTER_OTLP_HEADERS" || name == "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
}

// otlpHeaderValueForms returns the value of each key=value entry in an OTLP
// headers string, raw and URL-decoded, so a credential extracted from the
// header string can be masked and redacted on its own.
func otlpHeaderValueForms(headers string) []string {
	var out []string
	for _, entry := range strings.Split(headers, ",") {
		_, val, ok := strings.Cut(entry, "=")
		val = strings.TrimSpace(val)
		if !ok || val == "" {
			continue
		}
		out = append(out, val)
		if decoded, err := url.QueryUnescape(val); err == nil && decoded != val {
			out = append(out, decoded)
		}
	}
	return out
}

func sortedStringKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
