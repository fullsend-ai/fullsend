package mintcore

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

const (
	webhookTestSecret = "webhook-test-secret"
	// webhookTestToken is the dispatch token the fake GitHub API issues.
	webhookTestToken = "ghs_webhookDispatchToken"
	// webhookTestAppID is the fullsend role's App ID in TestMain's
	// ROLE_APP_IDS.
	webhookTestAppID = "500"
)

// webhookGitHub is a fake GitHub API for the webhook route. It records
// every request and checks the token and dispatch requests.
type webhookGitHub struct {
	t *testing.T

	installStatus  int // 0 means 200
	tokenStatus    int // 0 means 201
	dispatchStatus int // 0 means 204

	mu            sync.Mutex
	calls         []string
	tokenPerms    map[string]string
	tokenRepos    []string
	dispatchBody  map[string]any
	dispatchToken string
}

func (g *webhookGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/test-org/test-repo/installation":
		if g.installStatus != 0 {
			w.WriteHeader(g.installStatus)
			return
		}
		json.NewEncoder(w).Encode(installationResponse{
			ID: 42, Account: struct {
				Login string `json:"login"`
			}{Login: "test-org"},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			g.t.Errorf("token request is not authenticated with an App JWT")
		}
		var body struct {
			Permissions  map[string]string `json:"permissions"`
			Repositories []string          `json:"repositories"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			g.t.Errorf("decoding token request: %v", err)
		}
		g.tokenPerms, g.tokenRepos = body.Permissions, body.Repositories
		if g.tokenStatus != 0 {
			w.WriteHeader(g.tokenStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": webhookTestToken, "expires_at": "2099-01-01T00:00:00Z"})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/test-org/test-repo/actions/workflows/fullsend-poll-prs.yml/dispatches":
		g.dispatchToken = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&g.dispatchBody); err != nil {
			g.t.Errorf("decoding dispatch request: %v", err)
		}
		status := g.dispatchStatus
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	default:
		g.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (g *webhookGitHub) recordedCalls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// newWebhookHandler builds a Handler with the webhook route configured with
// secret (off when empty), a fullsend PEM, and the fake GitHub API.
func newWebhookHandler(t *testing.T, secret string, gh *webhookGitHub) *Handler {
	t.Helper()
	t.Setenv("FULLSEND_WEBHOOK_SECRET", secret)
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	h, err := NewHandler(&fakePEMAccessor{pems: map[string][]byte{"fullsend": pemData}}, &fakeOIDCVerifier{})
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}
	gh.t = t
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	h.githubBaseURL = srv.URL
	return h
}

// captureWebhookLogs redirects the standard logger for the test.
func captureWebhookLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func signWebhook(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// prPayload builds a pull_request payload. An empty head omits the head
// repository (a deleted fork).
func prPayload(action, head, base string) []byte {
	pr := map[string]any{
		"base": map[string]any{"repo": map[string]any{"full_name": base, "default_branch": "main"}},
		"head": map[string]any{"repo": nil},
	}
	if head != "" {
		pr["head"] = map[string]any{"repo": map[string]any{"full_name": head, "default_branch": "main"}}
	}
	b, _ := json.Marshal(map[string]any{"action": action, "pull_request": pr})
	return b
}

// webhookRequest builds a signed orchestrator-App delivery of event.
func webhookRequest(event string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	req.Header.Set("X-GitHub-Hook-Installation-Target-ID", webhookTestAppID)
	req.Header.Set("X-Hub-Signature-256", signWebhook([]byte(webhookTestSecret), body))
	return req
}

func serveWebhook(h *Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeWebhookResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return resp
}

func TestWebhook_OffWithoutSecret(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, "", gh)
	rec := serveWebhook(h, webhookRequest("pull_request", prPayload("opened", "fork/test-repo", "test-org/test-repo")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if calls := gh.recordedCalls(); len(calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", calls)
	}
}

func TestWebhook_RejectsUnauthenticated(t *testing.T) {
	body := prPayload("opened", "fork/test-repo", "test-org/test-repo")
	sha1Mac := hmac.New(sha1.New, []byte(webhookTestSecret))
	sha1Mac.Write(body)

	tests := []struct {
		name      string
		signature string
	}{
		{"missing", ""},
		{"wrong secret", signWebhook([]byte("other-secret"), body)},
		{"wrong algorithm", "sha1=" + hex.EncodeToString(sha1Mac.Sum(nil))},
		{"not hex", "sha256=zz"},
		{"tampered body", signWebhook([]byte(webhookTestSecret), append([]byte(nil), body[1:]...))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := &webhookGitHub{}
			h := newWebhookHandler(t, webhookTestSecret, gh)
			req := webhookRequest("pull_request", body)
			if tc.signature == "" {
				req.Header.Del("X-Hub-Signature-256")
			} else {
				req.Header.Set("X-Hub-Signature-256", tc.signature)
			}
			rec := serveWebhook(h, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if calls := gh.recordedCalls(); len(calls) != 0 {
				t.Errorf("GitHub calls = %v, want none", calls)
			}
		})
	}
}

func TestWebhook_OversizedBody(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	body := bytes.Repeat([]byte("a"), maxWebhookBodyBytes+1)
	rec := serveWebhook(h, webhookRequest("pull_request", body))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestWebhook_BodyReadError(t *testing.T) {
	h := newWebhookHandler(t, webhookTestSecret, &webhookGitHub{})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", errReader{})
	if rec := serveWebhook(h, req); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWebhook_MethodNotAllowed(t *testing.T) {
	h := newWebhookHandler(t, webhookTestSecret, &webhookGitHub{})
	req := httptest.NewRequest(http.MethodGet, "/v1/webhooks/github", nil)
	if rec := serveWebhook(h, req); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestWebhook_Ping(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	rec := serveWebhook(h, webhookRequest("ping", []byte(`{"zen":"Keep it logically awesome."}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls := gh.recordedCalls(); len(calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", calls)
	}
}

func TestWebhook_InvalidJSON(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	if rec := serveWebhook(h, webhookRequest("pull_request", []byte("{"))); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if calls := gh.recordedCalls(); len(calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", calls)
	}
}

func TestWebhook_DropsWithoutCallingGitHub(t *testing.T) {
	fork := prPayload("opened", "fork/test-repo", "test-org/test-repo")
	tests := []struct {
		name       string
		event      string
		body       []byte
		header     map[string]string
		env        map[string]string
		wantReason string
	}{
		{name: "wrong app ID", event: "pull_request", body: fork,
			header: map[string]string{"X-GitHub-Hook-Installation-Target-ID": "999"}, wantReason: "not_orchestrator_app"},
		{name: "non-numeric app ID", event: "pull_request", body: fork,
			header: map[string]string{"X-GitHub-Hook-Installation-Target-ID": "abc"}, wantReason: "not_orchestrator_app"},
		{name: "repository hook", event: "pull_request", body: fork,
			header: map[string]string{"X-GitHub-Hook-Installation-Target-Type": "repository"}, wantReason: "not_orchestrator_app"},
		{name: "no fullsend role", event: "pull_request", body: fork,
			env: map[string]string{"ROLE_APP_IDS": `{"triage":"100"}`}, wantReason: "not_orchestrator_app"},
		{name: "ping from another app", event: "ping", body: []byte(`{}`),
			header: map[string]string{"X-GitHub-Hook-Installation-Target-ID": "999"}, wantReason: "not_orchestrator_app"},
		{name: "push event", event: "push", body: []byte(`{}`), wantReason: "event_ignored"},
		{name: "closed", event: "pull_request", body: prPayload("closed", "fork/test-repo", "test-org/test-repo"), wantReason: "action_ignored"},
		{name: "edited", event: "pull_request", body: prPayload("edited", "fork/test-repo", "test-org/test-repo"), wantReason: "action_ignored"},
		{name: "labeled", event: "pull_request", body: prPayload("labeled", "fork/test-repo", "test-org/test-repo"), wantReason: "action_ignored"},
		{name: "converted to draft", event: "pull_request", body: prPayload("converted_to_draft", "fork/test-repo", "test-org/test-repo"), wantReason: "action_ignored"},
		{name: "same-repo PR", event: "pull_request", body: prPayload("opened", "test-org/test-repo", "test-org/test-repo"), wantReason: "not_fork"},
		{name: "same-repo PR differing in case", event: "pull_request", body: prPayload("synchronize", "Test-Org/Test-Repo", "test-org/test-repo"), wantReason: "not_fork"},
		{name: "deleted fork", event: "pull_request", body: prPayload("synchronize", "", "test-org/test-repo"), wantReason: "not_fork"},
		{name: "invalid base repository", event: "pull_request", body: prPayload("opened", "fork/x", "../etc"), wantReason: "invalid_repository"},
		{name: "not enrolled", event: "pull_request", body: prPayload("opened", "fork/other-repo", "test-org/other-repo"), wantReason: "repo_not_enrolled"},
		{name: "no default branch", event: "pull_request",
			body:       []byte(`{"action":"opened","pull_request":{"head":{"repo":{"full_name":"fork/test-repo"}},"base":{"repo":{"full_name":"test-org/test-repo"}}}}`),
			wantReason: "no_default_branch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			gh := &webhookGitHub{}
			h := newWebhookHandler(t, webhookTestSecret, gh)
			req := webhookRequest(tc.event, tc.body)
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			rec := serveWebhook(h, req)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
			}
			resp := decodeWebhookResponse(t, rec)
			if resp["dispatched"] != false || resp["reason"] != tc.wantReason {
				t.Errorf("response = %v, want dispatched false, reason %q", resp, tc.wantReason)
			}
			if calls := gh.recordedCalls(); len(calls) != 0 {
				t.Errorf("GitHub calls = %v, want none", calls)
			}
		})
	}
}

func TestWebhook_DispatchesForkPR(t *testing.T) {
	for _, action := range []string{"opened", "synchronize", "reopened", "ready_for_review"} {
		t.Run(action, func(t *testing.T) {
			logs := captureWebhookLogs(t)
			gh := &webhookGitHub{}
			h := newWebhookHandler(t, webhookTestSecret, gh)
			rec := serveWebhook(h, webhookRequest("pull_request", prPayload(action, "fork/test-repo", "test-org/test-repo")))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
			}
			if resp := decodeWebhookResponse(t, rec); resp["dispatched"] != true {
				t.Errorf("response = %v, want dispatched true", resp)
			}

			wantCalls := []string{
				"GET /repos/test-org/test-repo/installation",
				"POST /app/installations/42/access_tokens",
				"POST /repos/test-org/test-repo/actions/workflows/fullsend-poll-prs.yml/dispatches",
			}
			if calls := gh.recordedCalls(); strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
				t.Errorf("GitHub calls = %v, want %v", calls, wantCalls)
			}
			if len(gh.tokenPerms) != 2 || gh.tokenPerms["actions"] != "write" || gh.tokenPerms["metadata"] != "read" {
				t.Errorf("token permissions = %v, want actions write and metadata read only", gh.tokenPerms)
			}
			if len(gh.tokenRepos) != 1 || gh.tokenRepos[0] != "test-repo" {
				t.Errorf("token repositories = %v, want [test-repo]", gh.tokenRepos)
			}
			if len(gh.dispatchBody) != 1 || gh.dispatchBody["ref"] != "main" {
				t.Errorf("dispatch body = %v, want only ref main", gh.dispatchBody)
			}
			if gh.dispatchToken != "Bearer "+webhookTestToken {
				t.Errorf("dispatch Authorization = %q, want the dispatch token", gh.dispatchToken)
			}

			if strings.Contains(rec.Body.String(), webhookTestToken) {
				t.Errorf("response body contains the token")
			}
			out := logs.String()
			if strings.Contains(out, webhookTestToken) {
				t.Errorf("logs contain the token: %s", out)
			}
			if strings.Contains(out, "sha256=") || strings.Contains(out, "fork/test-repo") {
				t.Errorf("logs contain the signature or body: %s", out)
			}
			for _, want := range []string{`delivery="delivery-1"`, `event="pull_request"`, `action="` + action + `"`, `repo="test-org/test-repo"`, "outcome=dispatched"} {
				if !strings.Contains(out, want) {
					t.Errorf("logs missing %s: %s", want, out)
				}
			}
		})
	}
}

func TestWebhook_PublicModeDispatches(t *testing.T) {
	t.Setenv("PER_REPO_WIF_REPOS", "*")
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	rec := serveWebhook(h, webhookRequest("pull_request", prPayload("opened", "fork/test-repo", "test-org/test-repo")))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if resp := decodeWebhookResponse(t, rec); resp["dispatched"] != true {
		t.Errorf("response = %v, want dispatched true", resp)
	}
}

func TestWebhook_DispatchFailures(t *testing.T) {
	tests := []struct {
		name           string
		installStatus  int
		tokenStatus    int
		dispatchStatus int
		wantStatus     int
		wantReason     string
	}{
		{name: "dispatch 404", dispatchStatus: http.StatusNotFound,
			wantStatus: http.StatusAccepted, wantReason: "workflow_not_dispatchable"},
		{name: "dispatch 422", dispatchStatus: http.StatusUnprocessableEntity,
			wantStatus: http.StatusAccepted, wantReason: "workflow_not_dispatchable"},
		{name: "app not installed", installStatus: http.StatusNotFound,
			wantStatus: http.StatusAccepted, wantReason: "app_not_installed"},
		{name: "dispatch 500", dispatchStatus: http.StatusInternalServerError,
			wantStatus: http.StatusBadGateway},
		{name: "installation lookup 500", installStatus: http.StatusInternalServerError,
			wantStatus: http.StatusBadGateway},
		{name: "token request 403", tokenStatus: http.StatusForbidden,
			wantStatus: http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureWebhookLogs(t)
			gh := &webhookGitHub{installStatus: tc.installStatus, tokenStatus: tc.tokenStatus, dispatchStatus: tc.dispatchStatus}
			h := newWebhookHandler(t, webhookTestSecret, gh)
			rec := serveWebhook(h, webhookRequest("pull_request", prPayload("synchronize", "fork/test-repo", "test-org/test-repo")))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusAccepted {
				resp := decodeWebhookResponse(t, rec)
				if resp["dispatched"] != false || resp["reason"] != tc.wantReason {
					t.Errorf("response = %v, want dispatched false, reason %q", resp, tc.wantReason)
				}
			}
			if strings.Contains(rec.Body.String(), webhookTestToken) || strings.Contains(logs.String(), webhookTestToken) {
				t.Errorf("token leaked: body %s logs %s", rec.Body.String(), logs.String())
			}
			if n := strings.Count(logs.String(), "github webhook:"); n != 1 {
				t.Errorf("webhook log lines = %d, want 1: %s", n, logs.String())
			}
		})
	}
}

func TestWebhook_CredentialFailure(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	h.pemAccessor = &fakePEMAccessor{err: errors.New("no PEM")}
	rec := serveWebhook(h, webhookRequest("pull_request", prPayload("opened", "fork/test-repo", "test-org/test-repo")))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if calls := gh.recordedCalls(); len(calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", calls)
	}
}

func TestWebhook_DispatchTransportError(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	if _, err := postWorkflowDispatch(t.Context(), "http://127.0.0.1:0", webhookTestToken, "test-org", "test-repo", "main"); err == nil {
		t.Fatal("postWorkflowDispatch to an unreachable host succeeded")
	}
	// An unreachable GitHub API answers 502 so GitHub can redeliver.
	h.githubBaseURL = "http://127.0.0.1:0"
	rec := serveWebhook(h, webhookRequest("pull_request", prPayload("opened", "fork/test-repo", "test-org/test-repo")))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}
