package appsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePrompter records prompts and immediately returns.
type fakePrompter struct {
	prompts []string
}

func (f *fakePrompter) WaitForEnter(prompt string) error {
	f.prompts = append(f.prompts, prompt)
	return nil
}

// fakeBrowser records opened URLs and optionally visits them.
type fakeBrowser struct {
	visitFn func(url string) // Optional: actually make an HTTP request
	opened  []string
}

func (f *fakeBrowser) Open(_ context.Context, url string) error {
	f.opened = append(f.opened, url)
	if f.visitFn != nil {
		f.visitFn(url)
	}
	return nil
}

func newTestSetup(t *testing.T, ghAPI *httptest.Server, ghWeb *httptest.Server) (*Setup, *fakePrompter, *fakeBrowser, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	printer := ui.NewPrinter(&buf)

	prompt := &fakePrompter{}
	browser := &fakeBrowser{}

	opts := []Option{}
	if ghAPI != nil {
		opts = append(opts, WithBaseURL(ghAPI.URL))
	}
	if ghWeb != nil {
		opts = append(opts, WithWebURL(ghWeb.URL))
	}

	s := New(printer, prompt, browser, "test-token", opts...)
	return s, prompt, browser, &buf
}

func TestBuildManifest(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.NewPrinter(&buf)
	s := New(printer, nil, nil, "tok")

	manifest := s.buildManifest("my-org", "http://localhost:9999/callback")

	assert.Equal(t, "fullsend-my-org", manifest["name"])
	assert.Equal(t, "http://localhost:9999/callback", manifest["redirect_url"])
	assert.Equal(t, false, manifest["public"])

	perms := manifest["default_permissions"].(map[string]string)
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "write", perms["pull_requests"])
	assert.Equal(t, "read", perms["checks"])
	assert.Equal(t, "write", perms["contents"])

	events := manifest["default_events"].([]string)
	assert.Contains(t, events, "issues")
	assert.Contains(t, events, "pull_request")
}

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/app-manifests/")
		assert.Contains(t, r.URL.Path, "/conversions")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(AppCredentials{
			ID:       12345,
			Slug:     "fullsend-my-org",
			Name:     "fullsend-my-org",
			ClientID: "Iv1.abc123",
			PEM:      "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----",
			HTMLURL:  "https://github.com/apps/fullsend-my-org",
		})
	}))
	defer srv.Close()

	s, _, _, _ := newTestSetup(t, srv, nil)

	creds, err := s.exchangeCode(context.Background(), "test-code")
	require.NoError(t, err)

	assert.Equal(t, 12345, creds.ID)
	assert.Equal(t, "fullsend-my-org", creds.Slug)
	assert.Equal(t, "Iv1.abc123", creds.ClientID)
	assert.Contains(t, creds.PEM, "RSA PRIVATE KEY")
}

func TestExchangeCode_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	s, _, _, _ := newTestSetup(t, srv, nil)

	_, err := s.exchangeCode(context.Background(), "bad-code")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestVerifyInstallation_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/orgs/my-org/installations")
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"installations": []map[string]any{
				{"app_slug": "other-app"},
				{"app_slug": "fullsend-my-org"},
			},
		})
	}))
	defer srv.Close()

	s, _, _, _ := newTestSetup(t, srv, nil)

	found, err := s.verifyInstallation(context.Background(), "my-org", "fullsend-my-org")
	require.NoError(t, err)
	assert.True(t, found)
}

func TestVerifyInstallation_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"installations": []map[string]any{
				{"app_slug": "other-app"},
			},
		})
	}))
	defer srv.Close()

	s, _, _, _ := newTestSetup(t, srv, nil)

	found, err := s.verifyInstallation(context.Background(), "my-org", "fullsend-my-org")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestVerifyInstallation_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"Forbidden"}`)
	}))
	defer srv.Close()

	s, _, _, _ := newTestSetup(t, srv, nil)

	_, err := s.verifyInstallation(context.Background(), "my-org", "fullsend-my-org")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestFormPage(t *testing.T) {
	html := formPage("https://github.com/organizations/my-org/settings/apps/new", `{"name":"test"}`)

	assert.Contains(t, html, "fullsend")
	assert.Contains(t, html, "manifest-form")
	assert.Contains(t, html, "organizations/my-org/settings/apps/new")
	assert.Contains(t, html, `{&quot;name&quot;:&quot;test&quot;}`)
}

func TestSuccessPage(t *testing.T) {
	html := successPage()
	assert.Contains(t, html, "App created")
	assert.Contains(t, html, "close this tab")
}

func TestErrorPage(t *testing.T) {
	html := errorPage("something broke")
	assert.Contains(t, html, "something broke")
	assert.Contains(t, html, "Error")
}

func TestDefaultBrowser(t *testing.T) {
	// Just verify the type implements the interface and doesn't panic on construction
	var b BrowserOpener = DefaultBrowser{}
	_ = b
}

func TestStdinPrompter(t *testing.T) {
	// Just verify the type implements the interface
	var p Prompter = StdinPrompter{}
	_ = p
}
