package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/security"
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
		"GITLAB_CI":                 "true",
		"CI_COMMIT_REF_PROTECTED":   "true",
		"CI_PIPELINE_SOURCE":        "trigger",
		"CI_SERVER_HOST":            "gitlab.example.com",
		"CI_PROJECT_PATH":           "top/nested/proj",
		"CI_PROJECT_ROOT_NAMESPACE": "top",
		vaultIDTokenEnv:             token,
	}
}

func TestResolveVaultRunnerSecrets_GitLab(t *testing.T) {
	f := &cliFakeVault{secrets: map[string]map[string]any{
		"kv/data/fullsend/organizations/gitlab.example.com/top/shared/jira": {"JIRA_TOKEN": "org-shared-jira-token"},
		"kv/data/fullsend/otel": {"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "authorization=Bearer traces-token"},
	}}
	addr := f.start(t)
	token := testJWT(t, map[string]any{"aud": []string{"other", "https://vault.example.com/fullsend"}})
	env := gitlabVaultEnv(token)
	// The project belongs to a group, so {{org}} is available.
	ns := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"namespace":{"kind":"group"}}`))
	}))
	defer ns.Close()
	origNS := vaultGitLabAPIClient
	vaultGitLabAPIClient = ns.Client()
	t.Cleanup(func() { vaultGitLabAPIClient = origNS })
	env["CI_API_V4_URL"], env["CI_PROJECT_ID"], env["CI_JOB_TOKEN"] = ns.URL+"/api/v4", "7", "job-token-value"
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
		{name: "gitlab missing project", env: map[string]string{"GITLAB_CI": "true", "CI_COMMIT_REF_PROTECTED": "true", "CI_PIPELINE_SOURCE": "push"}, forge: "gitlab", wantErr: "CI_PROJECT_PATH"},
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
			"CI_PROJECT_PATH": "top/nested/p", "CI_SERVER_HOST": "gl.example.com", "CI_PROJECT_ROOT_NAMESPACE": "top",
			"CI_API_V4_URL": srv.URL + "/api/v4", "CI_PROJECT_ID": projectID, "CI_JOB_TOKEN": "job-token-value",
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
			e["CI_API_V4_URL"] = strings.Replace(srv.URL, "https://", "http://", 1) + "/api/v4"
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
	withVaultEnv(t, map[string]string{"CI_SERVER_HOST": "gl"})
	if _, err := vaultRepoContext(context.Background(), "gitlab", false); err == nil {
		t.Fatal("missing CI_PROJECT_PATH accepted")
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
				"CI_API_V4_URL": api.URL + "/api/v4", "CI_PROJECT_ID": "1", "CI_JOB_TOKEN": "job-token-value",
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

func TestOTLPHeaderValueForms(t *testing.T) {
	got := otlpHeaderValueForms("x-collector-key=abc%2Fdef-credential, tenant=acme ,novalue,empty=")
	want := []string{"abc%2Fdef-credential", "abc/def-credential", "acme"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("otlpHeaderValueForms = %v, want %v", got, want)
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

func TestVaultIDTokenIsRunnerOnly(t *testing.T) {
	if !oidcDenyKeys[vaultIDTokenEnv] {
		t.Fatalf("%s must be in oidcDenyKeys", vaultIDTokenEnv)
	}
}
