package vaultsecrets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var testRepo = RepoContext{
	Forge:    "gitlab",
	Host:     "gitlab.example.com",
	Owner:    "top",
	Name:     "proj",
	FullName: "top/sub group/proj",
	Org:      "top",
}

func TestExpandPath(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		rc      RepoContext
		want    string
		wantErr string
	}{
		{name: "literal", tmpl: "kv/data/fullsend/jira", rc: testRepo, want: "kv/data/fullsend/jira"},
		{name: "repo path keeps nested groups", tmpl: "kv/data/repositories/{{repo.host}}/{{repo.full_name}}/jira", rc: testRepo, want: "kv/data/repositories/gitlab.example.com/top/sub%20group/proj/jira"},
		{name: "org path", tmpl: "kv/data/organizations/{{repo.host}}/{{org}}/shared/jira", rc: testRepo, want: "kv/data/organizations/gitlab.example.com/top/shared/jira"},
		{name: "forge owner name", tmpl: "kv/data/{{forge}}/{{repo.owner}}/{{repo.name}}", rc: testRepo, want: "kv/data/gitlab/top/proj"},
		{name: "embedded variable", tmpl: "kv/data/x-{{repo.name}}", rc: testRepo, want: "kv/data/x-proj"},
		{name: "org unavailable", tmpl: "kv/data/{{org}}", rc: RepoContext{Host: "h"}, wantErr: `variable "org" is not available`},
		{name: "full_name unavailable", tmpl: "kv/data/{{repo.full_name}}", rc: RepoContext{}, wantErr: "not available"},
		{name: "unknown variable", tmpl: "kv/data/{{repo.id}}", rc: testRepo, wantErr: "unknown variable"},
		{name: "shell syntax is not expanded", tmpl: "kv/data/${HOME}", rc: testRepo, want: "kv/data/${HOME}"},
		{name: "unterminated", tmpl: "kv/data/{{org", rc: testRepo, wantErr: "unterminated"},
		{name: "stray close", tmpl: "kv/data/org}}", rc: testRepo, wantErr: "unmatched"},
		{name: "stray close before open", tmpl: "kv/}}data/{{org}}", rc: testRepo, wantErr: "unmatched"},
		{name: "dotdot value", tmpl: "kv/data/{{repo.name}}", rc: RepoContext{Name: ".."}, wantErr: "not allowed"},
		{name: "slash value", tmpl: "kv/data/{{repo.owner}}", rc: RepoContext{Owner: "a/b"}, wantErr: "slash"},
		{name: "backslash value", tmpl: "kv/data/{{repo.owner}}", rc: RepoContext{Owner: `a\b`}, wantErr: "slash"},
		{name: "control value", tmpl: "kv/data/{{repo.owner}}", rc: RepoContext{Owner: "a\nb"}, wantErr: "control"},
		{name: "empty full_name component", tmpl: "kv/data/{{repo.full_name}}", rc: RepoContext{FullName: "a//b"}, wantErr: "component 2"},
		{name: "dot full_name component", tmpl: "kv/data/{{repo.full_name}}", rc: RepoContext{FullName: "a/./b"}, wantErr: "not allowed"},
		{name: "query chars are encoded", tmpl: "kv/data/{{repo.name}}", rc: RepoContext{Name: "a?b#c"}, want: "kv/data/a%3Fb%23c"},
		{name: "leading slash", tmpl: "/kv/data/x", rc: testRepo, wantErr: "leading"},
		{name: "literal dotdot", tmpl: "kv/data/../sys", rc: testRepo, wantErr: `".."`},
		{name: "encoded dotdot", tmpl: "kv/data/%2e%2e/sys", rc: testRepo, wantErr: `".."`},
		{name: "encoded slash", tmpl: "kv/data/a%2fb", rc: testRepo, wantErr: "encoded separator"},
		{name: "scheme", tmpl: "https://evil/x", rc: testRepo, wantErr: "scheme"},
		{name: "query", tmpl: "kv/data/x?y=1", rc: testRepo, wantErr: "query"},
		{name: "empty component", tmpl: "kv/data//x", rc: testRepo, wantErr: "empty component"},
		{name: "bad escape", tmpl: "kv/data/%zz", rc: testRepo, wantErr: "percent"},
		{name: "empty", tmpl: "", rc: testRepo, wantErr: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPath(tt.tmpl, tt.rc)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ExpandPath(%q) error = %v, want containing %q", tt.tmpl, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandPath(%q): %v", tt.tmpl, err)
			}
			if got != tt.want {
				t.Fatalf("ExpandPath(%q) = %q, want %q", tt.tmpl, got, tt.want)
			}
		})
	}
}

func TestValidateTemplate(t *testing.T) {
	if err := ValidateTemplate("kv/data/repositories/{{repo.host}}/{{repo.full_name}}/jira"); err != nil {
		t.Fatalf("valid template: %v", err)
	}
	for _, bad := range []string{"kv/data/{{nope}}", "/kv/data", "kv/data/../x", "kv/{{org}}?x"} {
		if err := ValidateTemplate(bad); err == nil {
			t.Errorf("ValidateTemplate(%q) = nil, want error", bad)
		}
	}
}

func TestValidateAddressAndAuthPath(t *testing.T) {
	for addr, ok := range map[string]bool{
		"https://vault.example.com":         true,
		"https://vault.example.com:8200/":   true,
		"https://vault.example.com/prefix":  true,
		"http://vault.example.com":          false,
		"https://":                          false,
		"https://u:p@vault.example.com":     false,
		"https://vault.example.com?x=1":     false,
		"https://vault.example.com#frag":    false,
		"https://vault.example.com/%zz":     false,
		"vault.example.com":                 false,
		"https://vault.example.com/?":       false,
		"https://vault.example.com/path#xx": false,
	} {
		if err := ValidateAddress(addr); (err == nil) != ok {
			t.Errorf("ValidateAddress(%q) error = %v, want ok=%v", addr, err, ok)
		}
	}
	for p, ok := range map[string]bool{"": true, "jwt": true, "gitlab/jwt": true, "auth/jwt": false, "../x": false, "/jwt": false} {
		if err := ValidateAuthPath(p); (err == nil) != ok {
			t.Errorf("ValidateAuthPath(%q) error = %v, want ok=%v", p, err, ok)
		}
	}
}

// fakeVault is a minimal Vault KV v2 and JWT auth server.
type fakeVault struct {
	mu         sync.Mutex
	loginRole  string
	loginJWT   string
	namespace  string
	loginCode  int
	secrets    map[string]any // path → response body for 200, or int status
	reads      []string
	revoked    bool
	tokenSeen  string
	loginPath  string
	loginCount int
}

func (f *fakeVault) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.namespace = r.Header.Get("X-Vault-Namespace")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/login"):
			f.loginPath = r.URL.Path
			f.loginCount++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.loginRole, f.loginJWT = body["role"], body["jwt"]
			if f.loginCode != 0 {
				w.WriteHeader(f.loginCode)
				return
			}
			_, _ = w.Write([]byte(`{"auth":{"client_token":"vault-client-token-123"}}`))
		case r.URL.Path == "/v1/auth/token/revoke-self":
			f.revoked = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet:
			f.tokenSeen = r.Header.Get("X-Vault-Token")
			p := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/")
			f.reads = append(f.reads, p)
			v, ok := f.secrets[p]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if code, isCode := v.(int); isCode {
				w.WriteHeader(code)
				return
			}
			if raw, isRaw := v.(string); isRaw {
				_, _ = w.Write([]byte(raw))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": v}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
}

func newFakeVault(t *testing.T, f *fakeVault) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(f.handler(t))
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

func staticToken(gotAudience *string) TokenSource {
	return func(_ context.Context, audience string) (string, error) {
		if gotAudience != nil {
			*gotAudience = audience
		}
		return "job-oidc-jwt", nil
	}
}

func TestResolveCandidates(t *testing.T) {
	f := &fakeVault{secrets: map[string]any{
		"kv/data/denied":                     http.StatusForbidden,
		"kv/data/nofield":                    map[string]any{"OTHER": "x"},
		"kv/data/emptyfield":                 map[string]any{"JIRA_TOKEN": ""},
		"kv/data/nonstring":                  map[string]any{"JIRA_TOKEN": 5},
		"kv/data/nodata":                     `{"data":null}`,
		"kv/data/repos/top/sub%20group/proj": map[string]any{"JIRA_TOKEN": "repo-jira-token"},
		"kv/data/org/top":                    map[string]any{"JIRA_TOKEN": "org-jira-token"},
	}}
	srv, client := newFakeVault(t, f)
	var aud string
	r := Resolver{Repo: testRepo, Token: staticToken(&aud), HTTPClient: client}
	got, err := r.Resolve(context.Background(), []Source{{
		Name:           "JIRA_TOKEN",
		ConnectionName: "vault",
		Connection:     Connection{Address: srv.URL, Namespace: "ns1"},
		Paths: []string{
			"kv/data/missing", "kv/data/denied", "kv/data/nofield", "kv/data/emptyfield",
			"kv/data/nonstring", "kv/data/nodata",
			"kv/data/repos/{{repo.full_name}}", "kv/data/org/{{org}}",
		},
	}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["JIRA_TOKEN"] != "repo-jira-token" {
		t.Fatalf("JIRA_TOKEN = %q, want repo-jira-token", got["JIRA_TOKEN"])
	}
	if aud != DefaultAudience || f.loginRole != DefaultRole || f.loginJWT != "job-oidc-jwt" || f.loginPath != "/v1/auth/jwt/login" {
		t.Fatalf("login used audience %q role %q jwt %q path %q", aud, f.loginRole, f.loginJWT, f.loginPath)
	}
	if f.namespace != "ns1" || f.tokenSeen != "vault-client-token-123" || !f.revoked {
		t.Fatalf("namespace %q token %q revoked %v", f.namespace, f.tokenSeen, f.revoked)
	}
	if len(f.reads) != 7 {
		t.Fatalf("reads = %v, want 7 (stops at first hit)", f.reads)
	}
}

func TestResolveFailures(t *testing.T) {
	tests := []struct {
		name      string
		vault     *fakeVault
		paths     []string
		token     TokenSource
		repo      RepoContext
		wantErr   string
		wantLogin int
		vaultErr  bool
	}{
		{name: "exhausted candidates", vault: &fakeVault{secrets: map[string]any{}}, paths: []string{"kv/data/a", "kv/data/b"}, wantErr: "not falling back", wantLogin: 1},
		{name: "server error aborts", vault: &fakeVault{secrets: map[string]any{"kv/data/a": http.StatusInternalServerError, "kv/data/b": map[string]any{"JIRA_TOKEN": "v"}}}, paths: []string{"kv/data/a", "kv/data/b"}, wantErr: "HTTP 500", wantLogin: 1, vaultErr: true},
		{name: "auth error on read aborts", vault: &fakeVault{secrets: map[string]any{"kv/data/a": http.StatusUnauthorized}}, paths: []string{"kv/data/a"}, wantErr: "HTTP 401", wantLogin: 1, vaultErr: true},
		{name: "invalid json aborts", vault: &fakeVault{secrets: map[string]any{"kv/data/a": `{`}}, paths: []string{"kv/data/a"}, wantErr: "invalid JSON", wantLogin: 1, vaultErr: true},
		{name: "login rejected", vault: &fakeVault{loginCode: http.StatusBadRequest}, paths: []string{"kv/data/a"}, wantErr: "JWT login", wantLogin: 1, vaultErr: true},
		{name: "token source fails", vault: &fakeVault{}, paths: []string{"kv/data/a"}, token: func(context.Context, string) (string, error) { return "", errors.New("no oidc") }, wantErr: "no oidc"},
		{name: "path expansion fails before oidc", vault: &fakeVault{}, paths: []string{"kv/data/{{org}}"}, repo: RepoContext{Host: "h"}, token: func(context.Context, string) (string, error) {
			t.Fatal("token requested before path validation")
			return "", nil
		}, wantErr: "not available"},
		{name: "empty paths", vault: &fakeVault{}, paths: nil, wantErr: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, client := newFakeVault(t, tt.vault)
			token := tt.token
			if token == nil {
				token = staticToken(nil)
			}
			repo := tt.repo
			if repo == (RepoContext{}) {
				repo = testRepo
			}
			r := Resolver{Repo: repo, Token: token, HTTPClient: client}
			_, err := r.Resolve(context.Background(), []Source{{Name: "JIRA_TOKEN", ConnectionName: "vault", Connection: Connection{Address: srv.URL}, Paths: tt.paths}})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Resolve error = %v, want containing %q", err, tt.wantErr)
			}
			if tt.vaultErr != errors.Is(err, ErrVault) {
				t.Fatalf("errors.Is(err, ErrVault) = %v, want %v", errors.Is(err, ErrVault), tt.vaultErr)
			}
			if tt.vault.loginCount != tt.wantLogin {
				t.Fatalf("logins = %d, want %d", tt.vault.loginCount, tt.wantLogin)
			}
			if strings.Contains(err.Error(), "job-oidc-jwt") || strings.Contains(err.Error(), "vault-client-token-123") {
				t.Fatalf("error leaks a credential: %v", err)
			}
		})
	}
}

func TestResolveGroupsByConnection(t *testing.T) {
	a := &fakeVault{secrets: map[string]any{"kv/data/a": map[string]any{"JIRA_TOKEN": "ja", "OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer a"}}}
	srvA, client := newFakeVault(t, a)
	r := Resolver{Repo: testRepo, Token: staticToken(nil), HTTPClient: client}
	got, err := r.Resolve(context.Background(), []Source{
		{Name: "JIRA_TOKEN", ConnectionName: "a", Connection: Connection{Address: srvA.URL, Role: "custom", AuthPath: "gitlab/jwt"}, Paths: []string{"kv/data/a"}},
		{Name: "OTEL_EXPORTER_OTLP_HEADERS", ConnectionName: "a", Connection: Connection{Address: srvA.URL, Role: "custom", AuthPath: "gitlab/jwt"}, Paths: []string{"kv/data/a"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["JIRA_TOKEN"] != "ja" || got["OTEL_EXPORTER_OTLP_HEADERS"] != "authorization=Bearer a" {
		t.Fatalf("got %v", got)
	}
	if a.loginCount != 1 || a.loginRole != "custom" || a.loginPath != "/v1/auth/gitlab/jwt/login" {
		t.Fatalf("logins %d role %q path %q", a.loginCount, a.loginRole, a.loginPath)
	}
}

func TestResolveNoSourcesAndNoToken(t *testing.T) {
	got, err := Resolver{}.Resolve(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("Resolve(nil) = %v, %v", got, err)
	}
	if _, err := (Resolver{}).Resolve(context.Background(), []Source{{Name: "X", Paths: []string{"kv/data/a"}}}); err == nil {
		t.Fatal("Resolve without token source succeeded")
	}
	if _, err := (Resolver{Token: staticToken(nil)}).Resolve(context.Background(), []Source{{Name: "X", Connection: Connection{Address: "http://insecure"}, Paths: []string{"kv/data/a"}}}); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("insecure address error = %v", err)
	}
}

func TestClientTransportAndRedirect(t *testing.T) {
	// A redirect is not followed with the default client: the bearer token
	// must not be forwarded.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.com/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, err := NewClient(Connection{Address: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = srv.Client().Transport
	if _, err := c.Login(context.Background(), "jwt"); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("Login error = %v, want HTTP 307", err)
	}
	if _, _, err := c.ReadField(context.Background(), "t", "kv/data/a", "F"); err == nil {
		t.Fatal("ReadField followed a redirect")
	}
	if err := c.Revoke(context.Background(), "t"); err == nil {
		t.Fatal("Revoke accepted a redirect")
	}
	srv.Close()
	if _, err := c.Login(context.Background(), "jwt"); err == nil || !strings.Contains(err.Error(), "transport error") {
		t.Fatalf("Login error after close = %v", err)
	}
	if _, err := NewClient(Connection{Address: "https://v", AuthPath: "../x"}, nil); err == nil {
		t.Fatal("NewClient accepted a bad auth path")
	}
}

func TestReadFieldLargeResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+10)))
	}))
	defer srv.Close()
	c, err := NewClient(Connection{Address: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadField(context.Background(), "t", "kv/data/a", "F"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("ReadField error = %v", err)
	}
}
