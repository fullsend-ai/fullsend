package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient creates a Client whose httpClient is wired to the given
// test server, and whose methods are callable with host = srv address
// (the helper patches https:// → http:// via a custom transport).
func newTestClient(srv *httptest.Server) (*Client, string) {
	c := NewClient("test@example.com", "test-token")
	c.httpClient = srv.Client()
	// Strip scheme — methods prepend "https://", but httptest uses http.
	// We swap the transport so it hits the test server regardless.
	c.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}
	host := strings.TrimPrefix(srv.URL, "http://")
	return c, host
}

// rewriteTransport rewrites https:// requests to the test server URL.
type rewriteTransport struct {
	base   http.RoundTripper
	srvURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	parts := strings.SplitN(t.srvURL, "://", 2)
	req.URL.Host = parts[1]
	return t.base.RoundTrip(req)
}

// --- GetCloudID ---

func TestGetCloudID_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_edge/tenant_info" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(TenantInfo{CloudID: "cloud-abc-123"})
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	cloudID, err := client.GetCloudID(context.Background(), host)
	if err != nil {
		t.Fatalf("GetCloudID: %v", err)
	}
	if cloudID != "cloud-abc-123" {
		t.Errorf("cloudID = %q, want cloud-abc-123", cloudID)
	}
}

func TestGetCloudID_EmptyCloudID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(TenantInfo{CloudID: ""})
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.GetCloudID(context.Background(), host)
	if err == nil {
		t.Error("expected error for empty cloudId")
	}
	if !strings.Contains(err.Error(), "missing cloudId") {
		t.Errorf("error = %q, want 'missing cloudId'", err)
	}
}

func TestGetCloudID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.GetCloudID(context.Background(), host)
	if err == nil {
		t.Error("expected error for 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want HTTP 500", err)
	}
}

// --- ValidateProject ---

func TestValidateProject_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/project/TESTPROJ" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(ProjectInfo{ID: "12345", Key: "TESTPROJ"})
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	info, err := client.ValidateProject(context.Background(), host, "TESTPROJ")
	if err != nil {
		t.Fatalf("ValidateProject: %v", err)
	}
	if info.ID != "12345" {
		t.Errorf("ID = %q, want 12345", info.ID)
	}
	if info.Key != "TESTPROJ" {
		t.Errorf("Key = %q, want TESTPROJ", info.Key)
	}
}

func TestValidateProject_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.ValidateProject(context.Background(), host, "MISSING")
	if err == nil {
		t.Error("expected error for missing project")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

func TestValidateProject_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.ValidateProject(context.Background(), host, "PROJ")
	if err == nil {
		t.Error("expected error for 401")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("error = %q, want 'authentication failed'", err)
	}
}

func TestValidateProject_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.ValidateProject(context.Background(), host, "PROJ")
	if err == nil {
		t.Error("expected error for 403")
	}
	if !strings.Contains(err.Error(), "no permission") {
		t.Errorf("error = %q, want 'no permission'", err)
	}
}

func TestValidateProject_MissingID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ProjectInfo{Key: "PROJ"})
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.ValidateProject(context.Background(), host, "PROJ")
	if err == nil {
		t.Error("expected error for missing ID")
	}
}

// --- GetCurrentUser ---

func TestGetCurrentUser_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(AccountInfo{AccountID: "user-abc-123"})
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	accountID, err := client.GetCurrentUser(context.Background(), host)
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if accountID != "user-abc-123" {
		t.Errorf("accountID = %q, want user-abc-123", accountID)
	}
}

func TestGetCurrentUser_EmptyAccountID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(AccountInfo{})
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.GetCurrentUser(context.Background(), host)
	if err == nil {
		t.Error("expected error for empty accountId")
	}
}

func TestGetCurrentUser_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.GetCurrentUser(context.Background(), host)
	if err == nil {
		t.Error("expected error for 500")
	}
}

// --- CreateAutomationRule ---

func TestCreateAutomationRule_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/rule") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}

		var req CreateRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if req.Rule.Name != "test rule" {
			t.Errorf("rule name = %q", req.Rule.Name)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateRuleResponse{RuleUUID: "uuid-789"})
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	rule := CreateRuleRequest{
		Rule: RulePayload{
			Name:  "test rule",
			State: "ENABLED",
			Components: []RuleComponent{
				{Component: "ACTION", Type: "jira.issue.edit", Value: json.RawMessage(`{"operations":[]}`)},
			},
		},
		Connections: []RuleConnection{},
	}

	uuid, err := client.CreateAutomationRule(context.Background(), "cloud-123", rule)
	if err != nil {
		t.Fatalf("CreateAutomationRule: %v", err)
	}
	if uuid != "uuid-789" {
		t.Errorf("uuid = %q, want uuid-789", uuid)
	}
}

func TestCreateAutomationRule_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	rule := CreateRuleRequest{Rule: RulePayload{Name: "test", Components: []RuleComponent{{}}}}
	_, err := client.CreateAutomationRule(context.Background(), "cloud-123", rule)
	if err == nil {
		t.Error("expected error for 403")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error = %q, want HTTP 403", err)
	}
}

// --- ListAutomationRules ---

func TestListAutomationRules_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rule/summary") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		rules := []RuleSummary{
			{ID: 1, Name: "fullsend: auto-triage on issue creation", State: "ENABLED"},
			{ID: 2, Name: "fullsend: /fs-triage command", State: "ENABLED"},
			{ID: 3, Name: "other rule", State: "DISABLED"},
		}
		json.NewEncoder(w).Encode(rules)
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	rules, err := client.ListAutomationRules(context.Background(), "cloud-123")
	if err != nil {
		t.Fatalf("ListAutomationRules: %v", err)
	}
	if len(rules) != 3 {
		t.Errorf("rules len = %d, want 3", len(rules))
	}
	if rules[0].Name != "fullsend: auto-triage on issue creation" {
		t.Errorf("rules[0].Name = %q", rules[0].Name)
	}
}

func TestListAutomationRules_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	_, err := client.ListAutomationRules(context.Background(), "cloud-123")
	if err == nil {
		t.Error("expected error for 500")
	}
}

// --- RuleExistsByName ---

func TestRuleExistsByName_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rules := []RuleSummary{
			{ID: 1, Name: "fullsend: auto-triage on issue creation"},
		}
		json.NewEncoder(w).Encode(rules)
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	exists, err := client.RuleExistsByName(context.Background(), "cloud-123", "fullsend: auto-triage on issue creation")
	if err != nil {
		t.Fatalf("RuleExistsByName: %v", err)
	}
	if !exists {
		t.Error("expected rule to exist")
	}
}

func TestRuleExistsByName_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]RuleSummary{})
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	exists, err := client.RuleExistsByName(context.Background(), "cloud-123", "nonexistent")
	if err != nil {
		t.Fatalf("RuleExistsByName: %v", err)
	}
	if exists {
		t.Error("expected rule to not exist")
	}
}

// --- BasicAuth ---

func TestBasicAuth_Format(t *testing.T) {
	client := NewClient("user@example.com", "my-token")
	auth := client.basicAuth()
	decoded, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if string(decoded) != "user@example.com:my-token" {
		t.Errorf("decoded auth = %q, want user@example.com:my-token", decoded)
	}
}

// --- Malformed JSON responses ---

func TestGetCloudID_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.GetCloudID(context.Background(), host)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestValidateProject_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{invalid`))
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.ValidateProject(context.Background(), host, "PROJ")
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestGetCurrentUser_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.GetCurrentUser(context.Background(), host)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestCreateAutomationRule_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	rule := CreateRuleRequest{Rule: RulePayload{Name: "test", Components: []RuleComponent{{}}}}
	_, err := client.CreateAutomationRule(context.Background(), "cloud-123", rule)
	if err == nil {
		t.Error("expected error for malformed JSON response")
	}
}

func TestListAutomationRules_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{bad`))
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	_, err := client.ListAutomationRules(context.Background(), "cloud-123")
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// --- automationBaseURL ---

func TestAutomationBaseURL(t *testing.T) {
	url := automationBaseURL("cloud-123")
	want := "https://api.atlassian.com/automation/public/jira/cloud-123/rest/v1"
	if url != want {
		t.Errorf("automationBaseURL = %q, want %q", url, want)
	}
}

// --- RuleExistsByName error propagation ---

func TestRuleExistsByName_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	_, err := client.RuleExistsByName(context.Background(), "cloud-123", "anything")
	if err == nil {
		t.Error("expected error to propagate from ListAutomationRules")
	}
}

// --- ValidateProject other status codes ---

func TestValidateProject_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client, host := newTestClient(srv)
	_, err := client.ValidateProject(context.Background(), host, "PROJ")
	if err == nil {
		t.Error("expected error for 503")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("error = %q, want HTTP 503", err)
	}
}

// --- CreateAutomationRule verifies auth header ---

func TestCreateAutomationRule_SetsAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("Authorization header = %q, want Basic prefix", auth)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateRuleResponse{RuleUUID: "test-uuid"})
	}))
	defer srv.Close()

	client := NewClient("test@example.com", "token")
	client.httpClient = srv.Client()
	client.httpClient.Transport = &rewriteTransport{base: srv.Client().Transport, srvURL: srv.URL}

	rule := CreateRuleRequest{Rule: RulePayload{Name: "test", Components: []RuleComponent{{}}}}
	_, err := client.CreateAutomationRule(context.Background(), "cloud-123", rule)
	if err != nil {
		t.Fatalf("CreateAutomationRule: %v", err)
	}
}
