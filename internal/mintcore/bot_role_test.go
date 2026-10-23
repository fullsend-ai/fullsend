package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// botRoleGitHub configures the fake GitHub API used by bot-role tests.
type botRoleGitHub struct {
	// apps maps an App slug to the /apps/{slug} status and body.
	apps map[string]botRoleFakeResponse
	// installs maps "owner/repo" to the installation lookup status.
	installs map[string]int
	// appsAuth, when set, collects the Authorization header of every
	// /apps/{slug} request.
	appsAuth *[]string
	// rejectAppsAuth makes /apps/{slug} answer 401 to any authenticated
	// request, as GitHub does for a rotated or revoked App key.
	rejectAppsAuth bool
}

// appsLookupTestToken is the installation token the fake GitHub API issues
// for the /apps lookup.
const appsLookupTestToken = "ghs_appsLookupToken"

// serveAppsLookupTokenRequest answers POST /app/installations/{id}/access_tokens
// and checks the token is requested with the metadata-only permissions and the
// target repository only, authenticated as an App (JWT).
func serveAppsLookupTokenRequest(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		t.Errorf("installation token request is not authenticated with an App JWT")
	}
	var body struct {
		Permissions  map[string]string `json:"permissions"`
		Repositories []string          `json:"repositories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decoding installation token request: %v", err)
	}
	if len(body.Permissions) != 1 || body.Permissions["metadata"] != "read" {
		t.Errorf("installation token permissions = %v, want metadata read only", body.Permissions)
	}
	if len(body.Repositories) != 1 || body.Repositories[0] != "test-repo" {
		t.Errorf("installation token repositories = %v, want [test-repo]", body.Repositories)
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"token": appsLookupTestToken, "expires_at": "2099-01-01T00:00:00Z"})
}

type botRoleFakeResponse struct {
	status int
	body   string
}

func newBotRoleGitHub(t *testing.T, cfg botRoleGitHub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens" {
			serveAppsLookupTokenRequest(t, w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if slug, ok := strings.CutPrefix(r.URL.Path, "/apps/"); ok {
			if cfg.appsAuth != nil {
				*cfg.appsAuth = append(*cfg.appsAuth, r.Header.Get("Authorization"))
			}
			if r.Header.Get("Accept") != "application/vnd.github+json" {
				t.Errorf("unexpected Accept header %q", r.Header.Get("Accept"))
			}
			if r.Header.Get("User-Agent") == "" {
				t.Errorf("missing User-Agent header")
			}
			// GitHub documents installation tokens for this endpoint and
			// answers 401 to an App JWT, so only the token the fake issued
			// authenticates the request.
			if auth := r.Header.Get("Authorization"); auth != "" &&
				(cfg.rejectAppsAuth || auth != "Bearer "+appsLookupTestToken) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			resp, ok := cfg.apps[slug]
			if !ok {
				t.Errorf("unexpected app lookup: %s", slug)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(resp.status)
			w.Write([]byte(resp.body))
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, "/repos/"); ok && strings.HasSuffix(rest, "/installation") {
			repo := strings.TrimSuffix(rest, "/installation")
			status, ok := cfg.installs[repo]
			if !ok {
				t.Errorf("unexpected installation lookup: %s", repo)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			owner, _, _ := strings.Cut(repo, "/")
			json.NewEncoder(w).Encode(installationResponse{
				ID: 42, Account: struct {
					Login string `json:"login"`
				}{Login: owner},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newBotRoleEnv builds an OIDC test environment with PEMs for every role in
// roles and the fake GitHub API attached.
func newBotRoleEnv(t *testing.T, cfg botRoleGitHub, roles ...string) *testOIDCEnv {
	t.Helper()
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	pems := make(map[string][]byte, len(roles))
	for _, role := range roles {
		pems[PemSecretRole(role)] = pemData
	}
	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: pems})
	env.handler.githubBaseURL = newBotRoleGitHub(t, cfg).URL
	stubBotRoleClock(t, time.Now()) // retries must not really sleep
	return env
}

// stubBotRoleClock replaces the retry clock with a fixed now and a sleep
// that returns at once, and returns the waits requested so far.
func stubBotRoleClock(t *testing.T, now time.Time) *[]time.Duration {
	t.Helper()
	var (
		mu    sync.Mutex
		waits []time.Duration
	)
	oldNow, oldSleep := botRoleNowFn, botRoleSleepFn
	botRoleNowFn = func() time.Time { return now }
	botRoleSleepFn = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { botRoleNowFn, botRoleSleepFn = oldNow, oldSleep })
	return &waits
}

// seqResponse is one scripted GitHub answer.
type seqResponse struct {
	status int
	header map[string]string
	body   string
}

// sequencedGitHub answers /apps/{slug} and /repos/{o}/{r}/installation from
// scripted sequences (the last answer repeats) and counts the requests and
// the Authorization header of each /apps request.
type sequencedGitHub struct {
	mu           sync.Mutex
	apps         []seqResponse
	installs     []seqResponse
	appsCalls    int
	installCalls int
	appsAuth     []string
	// tokenStatus, when set, is the status of every installation token
	// request instead of a created token.
	tokenStatus int
}

func newSequencedGitHub(t *testing.T, env *testOIDCEnv, apps, installs []seqResponse) *sequencedGitHub {
	t.Helper()
	g := &sequencedGitHub{apps: apps, installs: installs}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens") {
			g.mu.Lock()
			status := g.tokenStatus
			g.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			serveAppsLookupTokenRequest(t, w, r)
			return
		}
		g.mu.Lock()
		var seq []seqResponse
		var n int
		switch {
		case strings.HasPrefix(r.URL.Path, "/apps/"):
			g.appsAuth = append(g.appsAuth, r.Header.Get("Authorization"))
			seq, n = g.apps, g.appsCalls
			g.appsCalls++
		case strings.HasSuffix(r.URL.Path, "/installation"):
			seq, n = g.installs, g.installCalls
			g.installCalls++
		default:
			g.mu.Unlock()
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		g.mu.Unlock()
		resp := seq[min(n, len(seq)-1)]
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.status)
		w.Write([]byte(resp.body))
	}))
	t.Cleanup(srv.Close)
	env.handler.githubBaseURL = srv.URL
	return g
}

var (
	seqAppOK      = seqResponse{status: http.StatusOK, body: `{"id":300}`}
	seqInstallOK  = seqResponse{status: http.StatusOK, body: `{"id":42,"account":{"login":"test-org"}}`}
	seqBadGateway = seqResponse{status: http.StatusBadGateway}
)

func postBotRole(t *testing.T, env *testOIDCEnv, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, botRolePath, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	env.handler.ServeHTTP(rec, req)
	return rec
}

func decodeBotRole(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return resp
}

func assertBotRoleOK(t *testing.T, rec *httptest.ResponseRecorder, outcome, role string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	resp := decodeBotRole(t, rec)
	if resp["outcome"] != outcome || resp["role"] != role {
		t.Fatalf("got outcome=%q role=%q, want outcome=%q role=%q", resp["outcome"], resp["role"], outcome, role)
	}
	if _, ok := resp["role"]; !ok && role != "" {
		t.Fatalf("role missing from response")
	}
	if _, ok := resp["role"]; ok && role == "" {
		t.Fatalf("no_match response must not carry a role: %s", rec.Body.String())
	}
}

func assertBotRoleError(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("expected %d, got %d: %s", status, rec.Code, rec.Body.String())
	}
	if got := decodeBotRole(t, rec)["error"]; got != msg {
		t.Fatalf("error = %q, want %q", got, msg)
	}
}

const botRoleBody = `{"actor_login":"fullsend-review[bot]","repo":"test-org/test-repo"}`

func TestBotRole_Recognized(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300,"slug":"fullsend-review"}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK},
	}, "review")
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
}

func TestBotRole_RecognizedCaseInsensitive(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK},
	}, "review")
	body := `{"actor_login":"Fullsend-Review[BOT]","repo":"Test-Org/Test-Repo"}`
	rec := postBotRole(t, env, env.signToken(t, nil), body)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
}

func TestBotRole_NoMatchUnknownAppID(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps: map[string]botRoleFakeResponse{"dependabot": {http.StatusOK, `{"id":29110}`}},
	})
	body := `{"actor_login":"dependabot[bot]","repo":"test-org/test-repo"}`
	rec := postBotRole(t, env, env.signToken(t, nil), body)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
}

func TestBotRole_AppsLookupAuthenticatedAsMintApp(t *testing.T) {
	var auth []string
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK},
		appsAuth: &auth,
	}, "review", "coder")
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if len(auth) != 1 || auth[0] != "Bearer "+appsLookupTestToken {
		t.Fatalf("/apps lookup Authorization = %q, want one installation token bearer", auth)
	}
}

func TestBotRole_AppsLookupAnonymousWithoutPEM(t *testing.T) {
	var auth []string
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"dependabot": {http.StatusOK, `{"id":29110}`}},
		appsAuth: &auth,
	}) // no PEMs registered: the lookup falls back to an anonymous request
	body := `{"actor_login":"dependabot[bot]","repo":"test-org/test-repo"}`
	rec := postBotRole(t, env, env.signToken(t, nil), body)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
	if len(auth) != 1 || auth[0] != "" {
		t.Fatalf("/apps lookup Authorization = %q, want one anonymous request", auth)
	}
}

func TestBotRole_AppsLookupAnonymousWhenAuthAppNotInstalled(t *testing.T) {
	// The App that would authenticate /apps (coder sorts first) has no
	// installation on the target repo, so there is no installation token to
	// use: the lookup degrades to an anonymous request instead of failing.
	env := newBotRoleEnv(t, botRoleGitHub{}, "review", "coder")
	g := newSequencedGitHub(t, env, []seqResponse{seqAppOK},
		[]seqResponse{{status: http.StatusNotFound}, seqInstallOK})
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if len(g.appsAuth) != 1 || g.appsAuth[0] != "" {
		t.Fatalf("/apps lookup Authorization = %q, want one anonymous request", g.appsAuth)
	}
}

func TestBotRole_AppsLookupAnonymousWhenTokenCreationFails(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review", "coder")
	g := newSequencedGitHub(t, env, []seqResponse{seqAppOK}, []seqResponse{seqInstallOK})
	g.tokenStatus = http.StatusForbidden // e.g. a suspended installation
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if len(g.appsAuth) != 1 || g.appsAuth[0] != "" {
		t.Fatalf("/apps lookup Authorization = %q, want one anonymous request", g.appsAuth)
	}
}

func TestBotRole_AppsLookupFallsBackWhenCredentialRejected(t *testing.T) {
	var auth []string
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:           map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs:       map[string]int{"test-org/test-repo": http.StatusOK},
		appsAuth:       &auth,
		rejectAppsAuth: true,
	}, "review", "coder") // coder sorts first, so its JWT authenticates /apps and is rejected
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if len(auth) != 2 || auth[0] == "" || auth[1] != "" {
		t.Fatalf("/apps lookup Authorization = %q, want an installation token then an anonymous retry", auth)
	}
}

func TestBotRole_RecognizedNonCanonicalRegisteredAppID(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK},
	}, "review")
	env.handler.roleAppIDs["review"] = "00300"
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
}

func TestBotRole_NoMatchAppNotFound(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps: map[string]botRoleFakeResponse{"fullsend-review": {http.StatusNotFound, `{"message":"Not Found"}`}},
	})
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
}

func TestBotRole_NoMatchNotInstalledOnRepo(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusNotFound},
	}, "review")
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
}

func TestBotRole_LookupFailures(t *testing.T) {
	tests := []struct {
		name    string
		app     botRoleFakeResponse
		install int
	}{
		{name: "apps 500", app: botRoleFakeResponse{http.StatusInternalServerError, ""}},
		{name: "apps 403", app: botRoleFakeResponse{http.StatusForbidden, `{"message":"rate limited"}`}},
		{name: "apps bad json", app: botRoleFakeResponse{http.StatusOK, `not json`}},
		{name: "apps missing id", app: botRoleFakeResponse{http.StatusOK, `{"slug":"fullsend-review"}`}},
		{name: "apps non-numeric id", app: botRoleFakeResponse{http.StatusOK, `{"id":"300"}`}},
		{name: "installation 500", app: botRoleFakeResponse{http.StatusOK, `{"id":300}`}, install: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := botRoleGitHub{apps: map[string]botRoleFakeResponse{"fullsend-review": tt.app}}
			if tt.install != 0 {
				cfg.installs = map[string]int{"test-org/test-repo": tt.install}
			}
			env := newBotRoleEnv(t, cfg, "review")
			rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
			assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
		})
	}
}

func TestBotRole_LookupFailureTransport(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	// The server drops the connection without answering.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	env.handler.githubBaseURL = srv.URL
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
}

func TestBotRole_LookupFailurePEM(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps: map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
	}) // no PEMs registered
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
}

func TestBotRole_LookupFailureBadPEM(t *testing.T) {
	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"review": []byte("not a pem")}})
	env.handler.githubBaseURL = newBotRoleGitHub(t, botRoleGitHub{
		apps: map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
	}).URL
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
}

func TestBotRole_LookupFailureInvalidRegisteredAppID(t *testing.T) {
	for _, tt := range []struct{ name, ids string }{
		{name: "empty", ids: `{"review":""}`},
		{name: "malformed", ids: `{"review":"not-an-id"}`},
		{name: "zero", ids: `{"review":"0"}`},
		{name: "negative", ids: `{"review":"-5"}`},
		{name: "other role malformed", ids: `{"review":"300","coder":"abc"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ROLE_APP_IDS", tt.ids)
			env := newBotRoleEnv(t, botRoleGitHub{
				apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
				installs: map[string]int{"test-org/test-repo": http.StatusOK},
			}, "review", "coder")
			rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
			assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
		})
	}
}

func TestBotRole_BadRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
		msg  string
	}{
		{"bad json", `{not json`, "invalid JSON body"},
		{"empty body", ``, "invalid JSON body"},
		{"missing login", `{"repo":"test-org/test-repo"}`, "invalid actor_login"},
		{"human login", `{"actor_login":"octocat","repo":"test-org/test-repo"}`, "invalid actor_login"},
		{"bare suffix", `{"actor_login":"[bot]","repo":"test-org/test-repo"}`, "invalid actor_login"},
		{"leading hyphen", `{"actor_login":"-app[bot]","repo":"test-org/test-repo"}`, "invalid actor_login"},
		{"path in slug", `{"actor_login":"a/b[bot]","repo":"test-org/test-repo"}`, "invalid actor_login"},
		{"missing repo", `{"actor_login":"fullsend-review[bot]"}`, "invalid repo"},
		{"repo without owner", `{"actor_login":"fullsend-review[bot]","repo":"test-repo"}`, "invalid repo"},
		{"repo bad owner", `{"actor_login":"fullsend-review[bot]","repo":"bad--org/test-repo"}`, "invalid repo"},
		{"repo extra segment", `{"actor_login":"fullsend-review[bot]","repo":"test-org/test-repo/x"}`, "invalid repo"},
		{"repo dotdot", `{"actor_login":"fullsend-review[bot]","repo":"test-org/.."}`, "invalid repo"},
	}
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	token := env.signToken(t, nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postBotRole(t, env, token, tt.body)
			assertBotRoleError(t, rec, http.StatusBadRequest, tt.msg)
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestBotRole_UnreadableBody(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, botRolePath, errReader{})
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, nil))
	env.handler.ServeHTTP(rec, req)
	assertBotRoleError(t, rec, http.StatusBadRequest, "invalid JSON body")
}

func TestBotRole_AuthenticationFailed(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	t.Run("missing token", func(t *testing.T) {
		assertBotRoleError(t, postBotRole(t, env, "", botRoleBody), http.StatusUnauthorized, "authentication failed")
	})
	t.Run("invalid token", func(t *testing.T) {
		assertBotRoleError(t, postBotRole(t, env, "not-a-jwt", botRoleBody), http.StatusUnauthorized, "authentication failed")
	})
	t.Run("not enrolled", func(t *testing.T) {
		token := env.signToken(t, map[string]interface{}{
			"repository":       "evil-org/test-repo",
			"repository_owner": "evil-org",
		})
		body := `{"actor_login":"fullsend-review[bot]","repo":"evil-org/test-repo"}`
		assertBotRoleError(t, postBotRole(t, env, token, body), http.StatusUnauthorized, "authentication failed")
	})
}

func TestBotRole_RepoMismatch(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	body := `{"actor_login":"fullsend-review[bot]","repo":"test-org/other-repo"}`
	rec := postBotRole(t, env, env.signToken(t, nil), body)
	assertBotRoleError(t, rec, http.StatusForbidden, "repo must be the requesting repository")
}

func TestBotRole_RequestRoleFieldIgnored(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"dependabot": {http.StatusOK, `{"id":29110}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK}, // coder authenticates /apps
	}, "coder")
	body := `{"actor_login":"dependabot[bot]","repo":"test-org/test-repo","role":"coder","outcome":"recognized"}`
	rec := postBotRole(t, env, env.signToken(t, nil), body)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
}

func TestBotRole_RoleNotAllowedIgnored(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "coder")
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK}, // coder authenticates /apps
	}, "coder", "review")
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
}

func TestBotRole_SharedAppIDPicksFirstRole(t *testing.T) {
	t.Run("distinct roles sharing an app", func(t *testing.T) {
		t.Setenv("ROLE_APP_IDS", `{"triage":"300","review":"300","coder":"200"}`)
		env := newBotRoleEnv(t, botRoleGitHub{
			apps:     map[string]botRoleFakeResponse{"shared-app": {http.StatusOK, `{"id":300}`}},
			installs: map[string]int{"test-org/test-repo": http.StatusOK},
		}, "review", "triage", "coder")
		body := `{"actor_login":"shared-app[bot]","repo":"test-org/test-repo"}`
		assertBotRoleOK(t, postBotRole(t, env, env.signToken(t, nil), body), botRoleOutcomeRecognized, "review")
	})
	t.Run("fix reuses the coder app", func(t *testing.T) {
		t.Setenv("ROLE_APP_IDS", `{"coder":"200","fix":"200"}`)
		env := newBotRoleEnv(t, botRoleGitHub{
			apps:     map[string]botRoleFakeResponse{"fullsend-coder": {http.StatusOK, `{"id":200}`}},
			installs: map[string]int{"test-org/test-repo": http.StatusOK},
		}, "coder", "fix")
		body := `{"actor_login":"fullsend-coder[bot]","repo":"test-org/test-repo"}`
		assertBotRoleOK(t, postBotRole(t, env, env.signToken(t, nil), body), botRoleOutcomeRecognized, "coder")
	})
}

func TestBotRole_MethodNotAllowed(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, botRolePath, nil))
	assertBotRoleError(t, rec, http.StatusMethodNotAllowed, "method not allowed")
}

func TestBotRole_LookupGitHubAppIDBadURL(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	h.githubBaseURL = "://bad"
	if _, err := h.lookupGitHubAppID(context.Background(), "app", ""); err == nil {
		t.Fatal("expected error for invalid base URL")
	}
}

func TestBotRole_RetriesTransientAppsFailure(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	g := newSequencedGitHub(t, env,
		[]seqResponse{seqBadGateway, seqBadGateway, seqAppOK}, []seqResponse{seqInstallOK})
	waits := stubBotRoleClock(t, time.Now())
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if g.appsCalls != 3 {
		t.Errorf("/apps calls = %d, want 3", g.appsCalls)
	}
	if want := []time.Duration{botRoleBackoffBase, 2 * botRoleBackoffBase}; !slicesEqual(*waits, want) {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
}

func TestBotRole_RetriesTransientInstallationFailure(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	g := newSequencedGitHub(t, env,
		[]seqResponse{seqAppOK}, []seqResponse{seqBadGateway, seqInstallOK})
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if g.installCalls != 2 {
		t.Errorf("installation calls = %d, want 2", g.installCalls)
	}
}

func TestBotRole_TransientFailureIsBounded(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	g := newSequencedGitHub(t, env, []seqResponse{seqBadGateway}, []seqResponse{seqInstallOK})
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
	if g.appsCalls != botRoleMaxAttempts {
		t.Errorf("/apps calls = %d, want %d", g.appsCalls, botRoleMaxAttempts)
	}
}

func TestBotRole_RetriesTransportFailure(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		if strings.HasPrefix(r.URL.Path, "/apps/") {
			w.Write([]byte(`{"id":300}`))
			return
		}
		w.Write([]byte(`{"id":42,"account":{"login":"test-org"}}`))
	}))
	t.Cleanup(srv.Close)
	env.handler.githubBaseURL = srv.URL
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
}

func TestBotRole_NoRetryForNoMatchOrCallerErrors(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	g := newSequencedGitHub(t, env,
		[]seqResponse{{status: http.StatusNotFound}}, []seqResponse{seqInstallOK})
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeNoMatch, "")
	if g.appsCalls != 1 {
		t.Errorf("/apps calls = %d, want 1", g.appsCalls)
	}
}

func TestBotRole_RateLimit403IsNotRejectedCredential(t *testing.T) {
	// Authenticated /apps answers a rate-limit 403 once, with Retry-After.
	// The lookup must wait and retry with the same App credential, never
	// fall back to an anonymous request.
	env := newBotRoleEnv(t, botRoleGitHub{}, "review", "coder")
	g := newSequencedGitHub(t, env, []seqResponse{
		{status: http.StatusForbidden, header: map[string]string{"Retry-After": "2"}, body: `{"message":"You have exceeded a secondary rate limit"}`},
		seqAppOK,
	}, []seqResponse{seqInstallOK})
	waits := stubBotRoleClock(t, time.Now())
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if want := []time.Duration{2 * time.Second}; !slicesEqual(*waits, want) {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
	if len(g.appsAuth) != 2 {
		t.Fatalf("/apps calls = %d, want 2", len(g.appsAuth))
	}
	for i, a := range g.appsAuth {
		if a != "Bearer "+appsLookupTestToken {
			t.Errorf("/apps request %d Authorization = %q, want the installation token (no anonymous fallback)", i, a)
		}
	}
}

func TestBotRole_PrimaryRateLimitWaitsForReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := strconv.FormatInt(now.Add(3*time.Second).Unix(), 10)
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{
		{status: http.StatusForbidden, header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, body: `{"message":"API rate limit exceeded"}`},
		seqAppOK,
	}, []seqResponse{seqInstallOK})
	waits := stubBotRoleClock(t, now)
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if want := []time.Duration{4 * time.Second}; !slicesEqual(*waits, want) {
		t.Errorf("waits = %v, want reset delay plus one second %v", *waits, want)
	}
}

func TestBotRole_PrimaryRateLimitBeyondBoundFailsWithoutAnonymousFallback(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)
	env := newBotRoleEnv(t, botRoleGitHub{}, "review", "coder")
	g := newSequencedGitHub(t, env, []seqResponse{
		{status: http.StatusForbidden, header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, body: `{"message":"API rate limit exceeded"}`},
	}, []seqResponse{seqInstallOK})
	waits := stubBotRoleClock(t, now)
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleError(t, rec, http.StatusBadGateway, "bot-role lookup failed")
	if len(*waits) != 0 {
		t.Errorf("waited %v for a reset beyond the retry bound", *waits)
	}
	if len(g.appsAuth) != 1 || g.appsAuth[0] == "" {
		t.Errorf("/apps requests = %q, want one authenticated request and no anonymous fallback", g.appsAuth)
	}
}

func TestBotRole_RateLimitWaitBoundedByRequestDeadline(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	g := newSequencedGitHub(t, env, []seqResponse{
		{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "4"}},
	}, []seqResponse{seqInstallOK})
	waits := stubBotRoleClock(t, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// botRoleNowFn is frozen at the real time, so the deadline is 2s away.
	_, err := env.handler.resolveBotRole(ctx, "fullsend-review[bot]", "test-org", "test-repo")
	if err == nil || errors.Is(err, errBotRoleNoMatch) {
		t.Fatalf("err = %v, want a lookup failure", err)
	}
	if len(*waits) != 0 || g.appsCalls != 1 {
		t.Errorf("waits = %v, /apps calls = %d; want no wait past the deadline and one call", *waits, g.appsCalls)
	}
}

func TestBotRole_RejectedCredential403StillFallsBackAnonymously(t *testing.T) {
	// A 403 that is not a rate limit is a rejected App credential.
	env := newBotRoleEnv(t, botRoleGitHub{}, "review", "coder")
	g := newSequencedGitHub(t, env, nil, []seqResponse{seqInstallOK})
	g.apps = []seqResponse{{status: http.StatusForbidden, body: `{"message":"Bad credentials"}`}, seqAppOK}
	rec := postBotRole(t, env, env.signToken(t, nil), botRoleBody)
	assertBotRoleOK(t, rec, botRoleOutcomeRecognized, "review")
	if len(g.appsAuth) != 2 || g.appsAuth[0] == "" || g.appsAuth[1] != "" {
		t.Errorf("/apps Authorization = %q, want an installation token then an anonymous retry", g.appsAuth)
	}
}

func TestGitHubStatusError_Classification(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		header      map[string]string
		body        string
		rateLimited bool
		transient   bool
	}{
		{"429", 429, nil, "", true, true},
		{"403 retry-after", 403, map[string]string{"Retry-After": "7"}, "", true, true},
		{"403 exhausted", 403, map[string]string{"X-RateLimit-Remaining": "0"}, "", true, true},
		{"403 message", 403, nil, `{"message":"API rate limit exceeded"}`, true, true},
		{"403 bad credentials", 403, map[string]string{"X-RateLimit-Remaining": "4000"}, `{"message":"Bad credentials"}`, false, false},
		{"401", 401, nil, "", false, false},
		{"500", 500, nil, "", false, true},
		{"502", 502, nil, "", false, true},
		{"503", 503, nil, "", false, true},
		{"504", 504, nil, "", false, true},
		{"422", 422, nil, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.header {
				h.Set(k, v)
			}
			resp := &http.Response{StatusCode: tt.status, Header: h, Body: io.NopCloser(strings.NewReader(tt.body))}
			e := newGitHubStatusError(resp, "msg")
			if e.RateLimited != tt.rateLimited || e.Transient() != tt.transient {
				t.Errorf("RateLimited=%v Transient=%v, want %v %v", e.RateLimited, e.Transient(), tt.rateLimited, tt.transient)
			}
			if e.Error() != "msg" {
				t.Errorf("Error() = %q", e.Error())
			}
		})
	}
}

func slicesEqual(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
