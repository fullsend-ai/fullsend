package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// withVaultEnv replaces the Vault environment lookup with env for the test.
func withVaultEnv(t *testing.T, env map[string]string) {
	t.Helper()
	orig := vaultEnvFn
	vaultEnvFn = func(k string) string { return env[k] }
	t.Cleanup(func() { vaultEnvFn = orig })
}

func loadVaultTestConfig(t *testing.T, yaml string) config.ConfigReader {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(dir, config.LoadOpts{})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

// cliFakeVault serves JWT login and KV v2 reads.
type cliFakeVault struct {
	mu      sync.Mutex
	secrets map[string]map[string]any
	logins  int
	jwt     string
}

func (f *cliFakeVault) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/login"):
			f.logins++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.jwt = body["jwt"]
			_, _ = w.Write([]byte(`{"auth":{"client_token":"short-lived-vault-token"}}`))
		case r.URL.Path == "/v1/auth/token/revoke-self":
			w.WriteHeader(http.StatusNoContent)
		default:
			data, ok := f.secrets[strings.TrimPrefix(r.URL.EscapedPath(), "/v1/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data}})
		}
	}))
	t.Cleanup(srv.Close)
	orig := vaultHTTPClient
	vaultHTTPClient = srv.Client()
	t.Cleanup(func() { vaultHTTPClient = orig })
	return srv.URL
}

func vaultConfigYAML(addr string) string {
	return `connections:
  corp:
    kind: vault
    vault_address: ` + addr + `
    vault_audience: https://vault.example.com/fullsend
secrets:
  JIRA_TOKEN:
    connection: corp
    vault_secrets:
      - kv/data/fullsend/repositories/{{repo.host}}/{{repo.full_name}}/jira
      - kv/data/fullsend/organizations/{{repo.host}}/{{org}}/shared/jira
  OTEL_EXPORTER_OTLP_TRACES_HEADERS:
    connection: corp
    vault_secrets: [kv/data/fullsend/otel]
  OTEL_EXPORTER_OTLP_HEADERS:
    connection: native
`
}

func gitlabVaultEnv(token string) map[string]string {
	return map[string]string{
		"GITLAB_CI":                    "true",
		"CI_COMMIT_REF_PROTECTED":      "true",
		"CI_PIPELINE_SOURCE":           "trigger",
		"FULLSEND_PINNED_GITLAB_URL":   "https://gitlab.example.com",
		"FULLSEND_PINNED_PROJECT_PATH": "top/nested/proj",
		vaultIDTokenEnv:                token,
	}
}

func TestResolveVaultRunnerSecrets_GitLab(t *testing.T) {
	// The project belongs to a group, so {{org}} is available. The fake
	// GitLab is the pinned instance, so its host fills {{repo.host}}.
	ns := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"namespace":{"kind":"group"}}`))
	}))
	defer ns.Close()
	nsURL, err := url.Parse(ns.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := &cliFakeVault{secrets: map[string]map[string]any{
		"kv/data/fullsend/organizations/" + nsURL.Hostname() + "/top/shared/jira": {"JIRA_TOKEN": "org-shared-jira-token"},
		"kv/data/fullsend/otel": {"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "authorization=Bearer traces-token"},
	}}
	addr := f.start(t)
	token := testJWT(t, map[string]any{"aud": []string{"other", "https://vault.example.com/fullsend"}})
	env := gitlabVaultEnv(token)
	origNS := vaultGitLabAPIClient
	vaultGitLabAPIClient = ns.Client()
	t.Cleanup(func() { vaultGitLabAPIClient = origNS })
	env["FULLSEND_PINNED_GITLAB_URL"], env["FULLSEND_PINNED_PROJECT_ID"], env["CI_JOB_TOKEN"] = ns.URL, "7", "job-token-value"
	withVaultEnv(t, env)
	cfg := loadVaultTestConfig(t, vaultConfigYAML(addr))

	got, err := resolveVaultRunnerSecrets(context.Background(), cfg, "gitlab")
	if err != nil {
		t.Fatalf("resolveVaultRunnerSecrets: %v", err)
	}
	if got["JIRA_TOKEN"] != "org-shared-jira-token" || got["OTEL_EXPORTER_OTLP_TRACES_HEADERS"] != "authorization=Bearer traces-token" {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["OTEL_EXPORTER_OTLP_HEADERS"]; ok {
		t.Fatal("native-selected secret was resolved from Vault")
	}
	if f.logins != 1 || f.jwt != token {
		t.Fatalf("logins = %d, jwt forwarded = %v", f.logins, f.jwt == token)
	}
}

func TestResolveVaultRunnerSecrets_NoVaultSelection(t *testing.T) {
	withVaultEnv(t, map[string]string{})
	for name, yaml := range map[string]string{
		"no secrets":        "version: \"1\"\n",
		"omitted":           "secrets:\n  JIRA_TOKEN: {}\n",
		"explicit native":   "secrets:\n  JIRA_TOKEN:\n    connection: native\n",
		"unused connection": "connections:\n  v:\n    kind: vault\n    vault_address: https://v.example.com\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resolveVaultRunnerSecrets(context.Background(), loadVaultTestConfig(t, yaml), "local")
			if err != nil || got != nil {
				t.Fatalf("got %v, %v; want nil, nil", got, err)
			}
		})
	}
	if got, err := resolveVaultRunnerSecrets(context.Background(), nil, "github"); err != nil || got != nil {
		t.Fatalf("nil config: %v, %v", got, err)
	}
}

func TestResolveVaultRunnerSecrets_FailClosed(t *testing.T) {
	const cfgYAML = `connections:
  v:
    kind: vault
    vault_address: https://vault.invalid
secrets:
  JIRA_TOKEN:
    connection: v
    vault_secrets: [kv/data/x]
`
	// Any OIDC or Vault request is a failure: every case must stop first.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
	}))
	defer srv.Close()
	origOIDC, origVault := vaultOIDCClient, vaultHTTPClient
	vaultOIDCClient, vaultHTTPClient = srv.Client(), srv.Client()
	t.Cleanup(func() { vaultOIDCClient, vaultHTTPClient = origOIDC, origVault })

	tests := []struct {
		name    string
		env     map[string]string
		forge   string
		yaml    string
		wantErr string
	}{
		{name: "local run", env: map[string]string{}, forge: "github", wantErr: "supported only on GitHub Actions and GitLab CI"},
		{name: "forgejo", env: map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "issues", "FORGEJO_ACTIONS": "true"}, forge: "forgejo", wantErr: "supported only"},
		{name: "forgejo actions claiming github", env: map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "issues", "FORGEJO_ACTIONS": "true"}, forge: "github", wantErr: "supported only"},
		{name: "gitlab missing id token", env: gitlabVaultEnv(""), forge: "gitlab", wantErr: vaultIDTokenEnv + " is not set"},
		{name: "gitlab wrong audience", env: gitlabVaultEnv(testJWT(t, map[string]any{"aud": "something-else"})), forge: "gitlab", wantErr: "was not issued for audience"},
		{name: "gitlab missing project", env: map[string]string{"GITLAB_CI": "true", "CI_COMMIT_REF_PROTECTED": "true", "CI_PIPELINE_SOURCE": "push"}, forge: "gitlab", wantErr: "FULLSEND_PINNED_PROJECT_PATH"},
		{name: "github missing repository", env: map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "issues"}, forge: "github", wantErr: "GITHUB_REPOSITORY"},
		{name: "github org unavailable", env: map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "issues", "GITHUB_REPOSITORY": "alice/repo"}, forge: "github",
			yaml: strings.Replace(cfgYAML, "kv/data/x", "\"kv/data/{{org}}/x\"", 1), wantErr: `variable "org" is not available`},
		{name: "invalid config", env: map[string]string{}, forge: "github", yaml: "secrets:\n  JIRA_TOKEN:\n    connection: missing\n", wantErr: "not declared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withVaultEnv(t, tt.env)
			y := tt.yaml
			if y == "" {
				y = cfgYAML
			}
			_, err := resolveVaultRunnerSecrets(context.Background(), loadVaultTestConfig(t, y), tt.forge)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveVaultRunnerSecrets_GitHub(t *testing.T) {
	f := &cliFakeVault{secrets: map[string]map[string]any{
		"kv/data/fullsend/repos/github.com/acme/repo/jira": {"JIRA_TOKEN": "repo-scoped-jira-token"},
	}}
	addr := f.start(t)
	oidcJWT := testJWT(t, map[string]any{"aud": "https://vault.example.com/fullsend", "iat": 1700000000, "exp": 1700000600})
	var gotAuth, gotAudience string
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAudience = r.URL.Query().Get("audience")
		_ = json.NewEncoder(w).Encode(map[string]string{"value": oidcJWT})
	}))
	defer oidc.Close()
	withVaultEnv(t, map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_REPOSITORY":              "acme/repo",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   oidc.URL + "/token?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	})
	cfg := loadVaultTestConfig(t, `connections:
  corp:
    kind: vault
    vault_address: `+addr+`
    vault_role: fullsend
    vault_audience: https://vault.example.com/fullsend
secrets:
  JIRA_TOKEN:
    connection: corp
    vault_secrets:
      - kv/data/fullsend/repos/{{repo.host}}/{{repo.full_name}}/jira
`)

	got, err := resolveVaultRunnerSecrets(context.Background(), cfg, "github")
	if err != nil {
		t.Fatalf("resolveVaultRunnerSecrets: %v", err)
	}
	if got["JIRA_TOKEN"] != "repo-scoped-jira-token" {
		t.Fatalf("got %v", got)
	}
	if gotAuth != "bearer request-token" || gotAudience != "https://vault.example.com/fullsend" {
		t.Fatalf("OIDC request: auth = %q, audience = %q", gotAuth, gotAudience)
	}
	if f.logins != 1 || f.jwt != oidcJWT {
		t.Fatalf("logins = %d, job OIDC token forwarded to Vault = %v", f.logins, f.jwt == oidcJWT)
	}
}

func TestResolveVaultRunnerSecrets_GitHubOIDCRequestRefused(t *testing.T) {
	// The GitHub path requests the token from the runner's OIDC endpoint;
	// an endpoint outside GitHub's OIDC host is refused before any request.
	withVaultEnv(t, map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_EVENT_NAME":              "pull_request_review",
		"GITHUB_REF":                     "refs/pull/3/merge",
		"GITHUB_REPOSITORY":              "acme/repo",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://attacker.example.com/token",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	})
	cfg := loadVaultTestConfig(t, "connections:\n  v:\n    kind: vault\n    vault_address: https://vault.invalid\nsecrets:\n  JIRA_TOKEN:\n    connection: v\n    vault_secrets: [kv/data/x]\n")
	_, err := resolveVaultRunnerSecrets(context.Background(), cfg, "github")
	if err == nil || !strings.Contains(err.Error(), "requesting job OIDC token") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "request-token") {
		t.Fatalf("error leaks the OIDC request token: %v", err)
	}
}

func TestVaultRepoContext_GitHub(t *testing.T) {
	dir := t.TempDir()
	orgEvent := filepath.Join(dir, "org.json")
	userEvent := filepath.Join(dir, "user.json")
	_ = os.WriteFile(orgEvent, []byte(`{"repository":{"owner":{"type":"Organization"}}}`), 0o600)
	_ = os.WriteFile(userEvent, []byte(`{"repository":{"owner":{"type":"User"}}}`), 0o600)

	withVaultEnv(t, map[string]string{"GITHUB_REPOSITORY": "acme/widgets", "GITHUB_SERVER_URL": "https://github.example.com", "GITHUB_EVENT_PATH": orgEvent})
	rc, err := vaultRepoContext(context.Background(), "github", true)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Host != "github.example.com" || rc.Owner != "acme" || rc.Name != "widgets" || rc.FullName != "acme/widgets" || rc.Org != "acme" || rc.Forge != "github" {
		t.Fatalf("rc = %+v", rc)
	}

	for name, path := range map[string]string{"user": userEvent, "missing": filepath.Join(dir, "nope.json"), "unset": ""} {
		withVaultEnv(t, map[string]string{"GITHUB_REPOSITORY": "alice/widgets", "GITHUB_EVENT_PATH": path})
		rc, err = vaultRepoContext(context.Background(), "github", true)
		if err != nil || rc.Org != "" || rc.Host != "github.com" {
			t.Fatalf("%s: rc = %+v, err = %v", name, rc, err)
		}
	}
	withVaultEnv(t, map[string]string{"GITHUB_REPOSITORY": "a/b", "GITHUB_SERVER_URL": "::bad"})
	if _, err := vaultRepoContext(context.Background(), "github", true); err == nil {
		t.Fatal("bad GITHUB_SERVER_URL accepted")
	}
}

func TestVaultRepoContext_GitLabOrg(t *testing.T) {
	kinds := map[string]string{"1": "group", "2": "user"}
	var gotToken string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("JOB-TOKEN")
		id := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/")
		kind, ok := kinds[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"namespace": map[string]any{"kind": kind}})
	}))
	defer srv.Close()
	orig := vaultGitLabAPIClient
	vaultGitLabAPIClient = srv.Client()
	t.Cleanup(func() { vaultGitLabAPIClient = orig })

	env := func(projectID string) map[string]string {
		return map[string]string{
			"FULLSEND_PINNED_PROJECT_PATH": "top/nested/p", "FULLSEND_PINNED_GITLAB_URL": srv.URL,
			"FULLSEND_PINNED_PROJECT_ID": projectID, "CI_JOB_TOKEN": "job-token-value",
		}
	}
	tests := []struct {
		name    string
		env     map[string]string
		needOrg bool
		wantOrg string
	}{
		{name: "group-owned project", env: env("1"), needOrg: true, wantOrg: "top"},
		{name: "user-owned project", env: env("2"), needOrg: true, wantOrg: ""},
		{name: "unknown project", env: env("3"), needOrg: true, wantOrg: ""},
		{name: "org not referenced", env: env("1"), needOrg: false, wantOrg: ""},
		{name: "no job token", env: func() map[string]string { e := env("1"); delete(e, "CI_JOB_TOKEN"); return e }(), needOrg: true, wantOrg: ""},
		{name: "plain http API", env: func() map[string]string {
			e := env("1")
			e["FULLSEND_PINNED_GITLAB_URL"] = strings.Replace(srv.URL, "https://", "http://", 1)
			return e
		}(), needOrg: true, wantOrg: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotToken = ""
			withVaultEnv(t, tt.env)
			rc, err := vaultRepoContext(context.Background(), "gitlab", tt.needOrg)
			if err != nil || rc.Owner != "top" || rc.Name != "p" || rc.FullName != "top/nested/p" || rc.Org != tt.wantOrg {
				t.Fatalf("rc = %+v, err = %v", rc, err)
			}
		})
	}
	withVaultEnv(t, env("1"))
	if _, err := vaultRepoContext(context.Background(), "gitlab", true); err != nil || gotToken != "job-token-value" {
		t.Fatalf("job token not sent to the project lookup: %q, %v", gotToken, err)
	}
	withVaultEnv(t, map[string]string{"FULLSEND_PINNED_GITLAB_URL": "https://gl.example.com"})
	if _, err := vaultRepoContext(context.Background(), "gitlab", false); err == nil {
		t.Fatal("missing FULLSEND_PINNED_PROJECT_PATH accepted")
	}
	// Overridable CI variables cannot redirect the pinned identity.
	overridden := env("1")
	overridden["CI_PROJECT_PATH"] = "other/victim"
	overridden["CI_PROJECT_ROOT_NAMESPACE"] = "other"
	overridden["CI_SERVER_HOST"] = "evil.example.com"
	overridden["CI_API_V4_URL"] = "https://evil.example.com/api/v4"
	overridden["CI_PROJECT_ID"] = "2"
	withVaultEnv(t, overridden)
	rc, err := vaultRepoContext(context.Background(), "gitlab", true)
	if err != nil || rc.Owner != "top" || rc.FullName != "top/nested/p" || rc.Org != "top" || rc.Host == "evil.example.com" {
		t.Fatalf("overridden CI variables changed the context: %+v, %v", rc, err)
	}
	// Overridable CI variables never stand in for the pinned identity.
	withVaultEnv(t, map[string]string{"CI_PROJECT_PATH": "top/nested/p", "CI_SERVER_HOST": "gl.example.com"})
	if _, err := vaultRepoContext(context.Background(), "gitlab", false); err == nil {
		t.Fatal("overridable CI_PROJECT_PATH accepted without a pinned identity")
	}
}

func TestGitLabNamespaceLookupRefusesRedirects(t *testing.T) {
	var mu sync.Mutex
	var leaked []string
	record := func(label string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("JOB-TOKEN") != "" {
				mu.Lock()
				leaked = append(leaked, label)
				mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"namespace": map[string]any{"kind": "group"}})
		}
	}
	otherTLS := httptest.NewTLSServer(record("other TLS host"))
	defer otherTLS.Close()
	plain := httptest.NewServer(record("plain HTTP"))
	defer plain.Close()

	for name, target := range map[string]string{"other host": otherTLS.URL, "plain http": plain.URL} {
		t.Run(name, func(t *testing.T) {
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target+r.URL.Path, http.StatusFound)
			}))
			defer api.Close()
			orig := vaultGitLabAPIClient
			// The injected client has no redirect policy of its own.
			vaultGitLabAPIClient = api.Client()
			t.Cleanup(func() { vaultGitLabAPIClient = orig })
			withVaultEnv(t, map[string]string{
				"FULLSEND_PINNED_GITLAB_URL": api.URL, "FULLSEND_PINNED_PROJECT_ID": "1", "CI_JOB_TOKEN": "job-token-value",
			})
			if gitlabNamespaceIsGroup(context.Background()) {
				t.Fatal("redirected lookup reported a group namespace")
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 {
		t.Fatalf("JOB-TOKEN forwarded to redirect target: %v", leaked)
	}
}

func TestJWTHasAudience(t *testing.T) {
	cases := map[string]struct {
		token string
		want  bool
	}{
		"string match":  {testJWT(t, map[string]any{"aud": "a"}), true},
		"list match":    {testJWT(t, map[string]any{"aud": []string{"x", "a"}}), true},
		"no match":      {testJWT(t, map[string]any{"aud": []string{"x"}}), false},
		"no aud":        {testJWT(t, map[string]any{}), false},
		"bad aud type":  {testJWT(t, map[string]any{"aud": 5}), false},
		"not a jwt":     {"abc", false},
		"bad base64":    {"a.!!!.c", false},
		"bad json body": {"a." + base64.RawURLEncoding.EncodeToString([]byte("{")) + ".c", false},
	}
	for name, c := range cases {
		if got := jwtHasAudience(c.token, "a"); got != c.want {
			t.Errorf("%s: jwtHasAudience = %v, want %v", name, got, c.want)
		}
	}
}

func TestApplyVaultRunnerSecrets(t *testing.T) {
	t.Setenv("JIRA_TOKEN", "native-jira-token-value")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "authorization=native")
	native := map[string]string{"NATIVE_ONLY": "native-bundle-value"}
	got, err := applyVaultRunnerSecrets(map[string]string{
		"JIRA_TOKEN":                        "vault-jira-token-value",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "authorization=Bearer vault-traces",
		"FULLSEND_GITHUB_RUNNER_SECRETS":    `{"DEPLOY_KEY":"vault-bundle-deploy-key"}`,
	}, native)
	if err != nil {
		t.Fatalf("applyVaultRunnerSecrets: %v", err)
	}
	if os.Getenv("JIRA_TOKEN") != "vault-jira-token-value" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS") != "authorization=Bearer vault-traces" {
		t.Fatal("Vault values not handed to their environment consumers")
	}
	if _, ok := os.LookupEnv("FULLSEND_GITHUB_RUNNER_SECRETS"); ok {
		t.Fatal("the runner secrets bundle must not be placed in the environment")
	}
	if len(got) != 1 || got["DEPLOY_KEY"] != "vault-bundle-deploy-key" {
		t.Fatalf("runner secrets = %v, want the Vault bundle replacing the native one", got)
	}
	// Bundle entries keep the native host-side-only rules.
	if _, err := applyVaultRunnerSecrets(map[string]string{"FULLSEND_GITHUB_RUNNER_SECRETS": `{"GITHUB_TOKEN":"some-long-token-value"}`}, nil); err == nil {
		t.Fatal("a reserved bundle key was accepted")
	}
	// Without a Vault bundle the native bundle is kept.
	got, err = applyVaultRunnerSecrets(map[string]string{"JIRA_TOKEN": "vault-jira-token-value"}, native)
	if err != nil || got["NATIVE_ONLY"] != "native-bundle-value" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestApplyVaultRunnerSecrets_RejectsShortStandaloneValue(t *testing.T) {
	for name, value := range map[string]string{
		"short":         "abc",
		"padded short":  "  abc  ",
		"blank":         "   ",
		"empty":         "",
		"short with nl": "\nab\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("JIRA_TOKEN", "native-jira-token-value")
			_, err := applyVaultRunnerSecrets(map[string]string{"JIRA_TOKEN": value}, nil)
			if err == nil {
				t.Fatal("a value too short to redact was accepted")
			}
			if strings.Contains(err.Error(), "abc") {
				t.Fatalf("error quotes the value: %v", err)
			}
			if got := os.Getenv("JIRA_TOKEN"); got != "native-jira-token-value" {
				t.Fatalf("JIRA_TOKEN = %q, want the environment untouched", got)
			}
		})
	}
}

func TestOTLPHeaderEntries(t *testing.T) {
	got := otlpHeaderEntries("x-collector-key=abc%2Fdef-credential, tenant=acme ,novalue,empty=")
	want := []otlpHeaderEntry{
		{key: "x-collector-key", forms: []string{"abc%2Fdef-credential", "abc/def-credential"}},
		{key: "tenant", forms: []string{"acme"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("otlpHeaderEntries = %v, want %v", got, want)
	}
}

func TestApplyVaultRunnerSecrets_RegistersOTLPHeaderValues(t *testing.T) {
	const credential = "otlp-entry-credential-value-9f3a"
	_, err := applyVaultRunnerSecrets(map[string]string{
		"OTEL_EXPORTER_OTLP_HEADERS": "x-collector-key=" + credential + ",tenant=acme-tenant-name",
	}, nil)
	if err != nil {
		t.Fatalf("applyVaultRunnerSecrets: %v", err)
	}
	if !slices.Contains(security.RuntimeSecrets(), credential) {
		t.Fatal("bare header credential was not registered for redaction")
	}
}

func TestApplyVaultRunnerSecrets_OTLPShortEntryValues(t *testing.T) {
	const long = "otlp-entry-credential-value-9f3a"
	t.Run("short credential entry is rejected", func(t *testing.T) {
		_, err := applyVaultRunnerSecrets(map[string]string{
			"OTEL_EXPORTER_OTLP_HEADERS": "x-api-key=abc123,tenant=" + long,
		}, nil)
		if err == nil {
			t.Fatal("a header entry too short to redact was accepted")
		}
		if strings.Contains(err.Error(), "abc123") {
			t.Fatalf("error quotes the value: %v", err)
		}
		if !strings.Contains(err.Error(), "x-api-key") {
			t.Fatalf("error does not name the entry key: %v", err)
		}
	})
	t.Run("short non-secret routing entry is accepted", func(t *testing.T) {
		_, err := applyVaultRunnerSecrets(map[string]string{
			"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "x-collector-key=" + long + ",Tenant=acme",
		}, nil)
		if err != nil {
			t.Fatalf("applyVaultRunnerSecrets: %v", err)
		}
	})
}

func TestVaultIDTokenIsRunnerOnly(t *testing.T) {
	if !oidcDenyKeys[vaultIDTokenEnv] {
		t.Fatalf("%s must be in oidcDenyKeys", vaultIDTokenEnv)
	}
}

// runAgent resolves Vault selections before parsing the native bundle: a
// Vault-supplied bundle replaces a malformed native one and standalone
// values reach their environment consumer, while a malformed native bundle
// still fails the run when Vault supplies only a standalone value.
func TestRunAgent_VaultResolveThenApply(t *testing.T) {
	const (
		jiraToken = "vault-run-agent-jira-token"
		repoPath  = "kv/data/fullsend/repos/github.com/acme/repo/"
	)
	for _, tc := range []struct {
		name        string
		secrets     map[string]map[string]any
		selectYAML  string
		wantErr     string
		wantNoErr   string
		wantMessage string
		wantJira    string
	}{
		{
			name: "Vault bundle replaces a malformed native bundle",
			secrets: map[string]map[string]any{
				repoPath + "jira":   {"JIRA_TOKEN": jiraToken},
				repoPath + "bundle": {"FULLSEND_GITHUB_RUNNER_SECRETS": `{"DEPLOY_KEY":"vault-run-agent-deploy-key"}`},
			},
			selectYAML: `  FULLSEND_GITHUB_RUNNER_SECRETS:
    connection: corp
    vault_secrets: [` + repoPath + `bundle]
`,
			wantNoErr:   "runner secrets bundle",
			wantMessage: "Resolved runner secrets from Vault: FULLSEND_GITHUB_RUNNER_SECRETS, JIRA_TOKEN",
			wantJira:    jiraToken,
		},
		{
			name:        "malformed native bundle still fails without a Vault bundle",
			secrets:     map[string]map[string]any{repoPath + "jira": {"JIRA_TOKEN": jiraToken}},
			wantErr:     "FULLSEND_GITHUB_RUNNER_SECRETS",
			wantMessage: "Invalid runner secrets bundle",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
			t.Setenv("FULLSEND_RUNTIME", "")
			t.Setenv("JIRA_TOKEN", "")
			t.Setenv(runnerSecretsEnv, "not a json bundle")
			t.Setenv(runnerSecretsFileEnv, "")

			f := &cliFakeVault{secrets: tc.secrets}
			addr := f.start(t)
			oidcJWT := testJWT(t, map[string]any{"aud": "https://vault.example.com/fullsend", "iat": 1700000000, "exp": 1700000600})
			oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"value": oidcJWT})
			}))
			defer oidc.Close()
			withVaultEnv(t, map[string]string{
				"GITHUB_ACTIONS":                 "true",
				"GITHUB_REPOSITORY":              "acme/repo",
				"ACTIONS_ID_TOKEN_REQUEST_URL":   oidc.URL + "/token?api-version=2.0",
				"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
			})

			marker := filepath.Join(t.TempDir(), "pre-script-ran")
			dir := newVertexChildDir(t, marker, "dummy", "openai/gpt-5.6-luna", "", "")
			cfgPath := filepath.Join(dir, "config.yaml")
			base, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			vaultYAML := `connections:
  corp:
    kind: vault
    vault_address: ` + addr + `
    vault_audience: https://vault.example.com/fullsend
secrets:
  JIRA_TOKEN:
    connection: corp
    vault_secrets: [` + repoPath + `jira]
` + tc.selectYAML
			if err := os.WriteFile(cfgPath, append(base, []byte(vaultYAML)...), 0o644); err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			err = runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "github", "",
				resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
			if err == nil {
				t.Fatal("the stub never creates a sandbox, so runAgent must return an error")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if tc.wantNoErr != "" && strings.Contains(err.Error(), tc.wantNoErr) {
				t.Fatalf("error = %v, must not mention %q", err, tc.wantNoErr)
			}
			if !strings.Contains(out.String(), tc.wantMessage) {
				t.Fatalf("output lacks %q:\n%s", tc.wantMessage, out.String())
			}
			if got := os.Getenv("JIRA_TOKEN"); got != tc.wantJira {
				t.Fatalf("JIRA_TOKEN consumer value = %q, want %q", got, tc.wantJira)
			}
			if _, err := os.Stat(marker); tc.wantJira == "" && err == nil {
				t.Fatal("the pre-script ran after the bundle failed validation")
			}
		})
	}
}

// vaultRunAgentRepoPath is the KV prefix the runAgent-level Vault tests
// read for the github.com/acme/repo project.
const vaultRunAgentRepoPath = "kv/data/fullsend/repos/github.com/acme/repo/"

// startGitHubVaultRun points the Vault resolver at a fake Vault serving
// secrets, authenticated by a stub GitHub Actions OIDC endpoint, and returns
// the Vault address for the config's connection.
func startGitHubVaultRun(t *testing.T, secrets map[string]map[string]any) string {
	t.Helper()
	f := &cliFakeVault{secrets: secrets}
	addr := f.start(t)
	oidcJWT := testJWT(t, map[string]any{"aud": "https://vault.example.com/fullsend", "iat": 1700000000, "exp": 1700000600})
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": oidcJWT})
	}))
	t.Cleanup(oidc.Close)
	withVaultEnv(t, map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_REPOSITORY":              "acme/repo",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   oidc.URL + "/token?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	})
	return addr
}

// appendVaultSelections appends a Vault connection and the given secrets
// block entries to the fullsend dir's config.yaml, so runAgent loads and
// resolves them as in a real run.
func appendVaultSelections(t *testing.T, dir, addr, secretsYAML string) {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.yaml")
	base, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultYAML := `connections:
  corp:
    kind: vault
    vault_address: ` + addr + `
    vault_audience: https://vault.example.com/fullsend
secrets:
` + secretsYAML
	if err := os.WriteFile(cfgPath, append(base, []byte(vaultYAML)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runVaultGitHubAgent(t *testing.T, dir string, out *bytes.Buffer) error {
	t.Helper()
	return runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "github", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(out), false, runOverrideFlags{})
}

// Vault-resolved OTLP headers replace the native value and reach the OTLP
// trace exporter, while host-side scripts that do not declare them in
// env.runner never see them.
func TestRunAgent_VaultOTLPHeadersReachTraceExporter(t *testing.T) {
	const (
		nativeCred = "native-collector-credential-0001"
		vaultCred  = "vault-collector-credential-7c2e"
	)
	for _, name := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS"} {
		t.Run(name, func(t *testing.T) {
			usePreScriptStub(t)
			var mu sync.Mutex
			var got []string
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/traces" {
					mu.Lock()
					got = append(got, r.Header.Get("x-collector-key"))
					mu.Unlock()
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer sink.Close()
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.URL)
			t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-collector-key="+nativeCred)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
			t.Setenv("OTEL_SDK_DISABLED", "")
			t.Setenv("TRACEPARENT", "")
			t.Setenv("TRACESTATE", "")
			t.Setenv(runnerSecretsEnv, "")
			t.Setenv(runnerSecretsFileEnv, "")

			addr := startGitHubVaultRun(t, map[string]map[string]any{
				vaultRunAgentRepoPath + "otel": {name: "x-collector-key=" + vaultCred},
			})
			marker := filepath.Join(t.TempDir(), "ran")
			dir := newRunnerSecretHarnessDir(t, "",
				`[ -z "${OTEL_EXPORTER_OTLP_HEADERS+set}" ] || exit 21`+"\n"+
					`[ -z "${OTEL_EXPORTER_OTLP_TRACES_HEADERS+set}" ] || exit 22`+"\n"+
					"touch "+marker+"\n")
			appendVaultSelections(t, dir, addr, "  "+name+`:
    connection: corp
    vault_secrets: [`+vaultRunAgentRepoPath+`otel]
`)

			var out bytes.Buffer
			if err := runVaultGitHubAgent(t, dir, &out); err != nil {
				t.Fatalf("runAgent: %v\n%s", err, out.String())
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("pre-script did not run to completion: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) == 0 {
				t.Fatal("the OTLP exporter sent no trace request")
			}
			for _, v := range got {
				if v != vaultCred {
					t.Fatalf("exporter sent x-collector-key %q, want the Vault-resolved value", v)
				}
			}
			for _, secret := range []string{vaultCred, nativeCred} {
				if strings.Contains(out.String(), secret) {
					t.Fatalf("run output leaks a header credential:\n%s", out.String())
				}
			}
		})
	}
}

// A Vault-resolved runner secrets bundle replaces the native bundle and
// reaches only the host-side scripts that declare a key in env.runner: other
// bundle keys, the native bundle and the bundle variables stay unset, and a
// harness that routes a Vault-bundle key into the sandbox is refused.
func TestRunAgent_VaultBundleReachesDeclaredScriptsOnly(t *testing.T) {
	const (
		deployKey   = "vault-bundle-deploy-key-5b1d"
		otherKey    = "vault-bundle-undeclared-key-9e44"
		nativeKey   = "native-bundle-deploy-key-0002"
		bundleField = "FULLSEND_GITHUB_RUNNER_SECRETS"
	)
	bundle := `{"DEPLOY_KEY":"` + deployKey + `","UNDECLARED_KEY":"` + otherKey + `"}`
	selection := "  " + bundleField + `:
    connection: corp
    vault_secrets: [` + vaultRunAgentRepoPath + `bundle]
`
	setup := func(t *testing.T) string {
		usePreScriptStub(t)
		t.Setenv(runnerSecretsEnv, `{"DEPLOY_KEY":"`+nativeKey+`","NATIVE_ONLY":"native-only-bundle-value"}`)
		t.Setenv(runnerSecretsFileEnv, "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
		return startGitHubVaultRun(t, map[string]map[string]any{
			vaultRunAgentRepoPath + "bundle": {bundleField: bundle},
		})
	}

	t.Run("declared key reaches the script and nothing else does", func(t *testing.T) {
		addr := setup(t)
		marker := filepath.Join(t.TempDir(), "ran")
		dir := newRunnerSecretHarnessDir(t,
			"env:\n  runner:\n    DEPLOY_KEY: \"${DEPLOY_KEY}\"\n",
			`[ "${DEPLOY_KEY:-}" = "`+deployKey+`" ] || exit 31`+"\n"+
				`[ -z "${UNDECLARED_KEY+set}" ] || exit 32`+"\n"+
				`[ -z "${NATIVE_ONLY+set}" ] || exit 33`+"\n"+
				`[ -z "${`+bundleField+`+set}" ] || exit 34`+"\n"+
				`[ -z "${`+runnerSecretsFileEnv+`+set}" ] || exit 35`+"\n"+
				`[ -z "${`+vaultIDTokenEnv+`+set}" ] || exit 36`+"\n"+
				"touch "+marker+"\n")
		appendVaultSelections(t, dir, addr, selection)

		var out bytes.Buffer
		if err := runVaultGitHubAgent(t, dir, &out); err != nil {
			t.Fatalf("runAgent: %v\n%s", err, out.String())
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("pre-script did not run to completion: %v", err)
		}
		if !strings.Contains(out.String(), "Runner secrets") || !strings.Contains(out.String(), "DEPLOY_KEY") {
			t.Fatalf("run log does not name the declared runner secret:\n%s", out.String())
		}
		for _, leaked := range []string{"UNDECLARED_KEY", deployKey, otherKey, nativeKey} {
			if strings.Contains(out.String(), leaked) {
				t.Fatalf("run output mentions %q:\n%s", leaked, out.String())
			}
		}
		if _, ok := os.LookupEnv(runnerSecretsEnv); ok {
			t.Fatal("the native bundle variable is still set in the runner environment")
		}
	})

	t.Run("sandbox reference is refused", func(t *testing.T) {
		addr := setup(t)
		marker := filepath.Join(t.TempDir(), "ran")
		dir := newRunnerSecretHarnessDir(t,
			"env:\n  runner:\n    DEPLOY_KEY: \"${DEPLOY_KEY}\"\n  sandbox:\n    DEPLOY_KEY: \"${DEPLOY_KEY}\"\n",
			"touch "+marker+"\n")
		appendVaultSelections(t, dir, addr, selection)

		var out bytes.Buffer
		err := runVaultGitHubAgent(t, dir, &out)
		if err == nil || !strings.Contains(err.Error(), "env.sandbox[DEPLOY_KEY] references runner secret DEPLOY_KEY") {
			t.Fatalf("error = %v, want the sandbox reference refused", err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("the pre-script ran after the sandbox reference was refused")
		}
		for _, leaked := range []string{deployKey, otherKey} {
			if strings.Contains(out.String()+err.Error(), leaked) {
				t.Fatalf("output leaks a bundle value:\n%s", out.String())
			}
		}
	})
}
