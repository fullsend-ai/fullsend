package appsetup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// --- fakes ---

type fakePrompter struct {
	confirmResult bool
	waitCalled    bool
	confirmCalled bool
}

func (f *fakePrompter) WaitForEnter(_ string) error {
	f.waitCalled = true
	return nil
}

func (f *fakePrompter) Confirm(_ string) (bool, error) {
	f.confirmCalled = true
	return f.confirmResult, nil
}

type fakeBrowser struct {
	urlCh chan string
}

func newFakeBrowser() *fakeBrowser {
	return &fakeBrowser{urlCh: make(chan string, 1)}
}

func (f *fakeBrowser) Open(_ context.Context, url string) error {
	f.urlCh <- url
	return nil
}

// --- tests ---

func TestExpectedAppSlug(t *testing.T) {
	tests := []struct {
		name     string
		org      string
		role     string
		expected string
	}{
		{
			name:     "fullsend role uses org only",
			org:      "myorg",
			role:     "fullsend",
			expected: "myorg-fullsend",
		},
		{
			name:     "triage role appends role suffix",
			org:      "myorg",
			role:     "triage",
			expected: "myorg-triage",
		},
		{
			name:     "coder role appends role suffix",
			org:      "acme",
			role:     "coder",
			expected: "acme-coder",
		},
		{
			name:     "review role appends role suffix",
			org:      "acme",
			role:     "review",
			expected: "acme-review",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpectedAppSlug(tc.org, tc.role)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestSetup_ExistingApp_SecretExists_AutoReuse(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "myorg-fullsend"},
		},
		AppClientIDs: map[string]string{
			"myorg-fullsend": "Iv1.fullsend123",
		},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	// Should return credentials signaling reuse (empty PEM).
	assert.Equal(t, 10, creds.AppID)
	assert.Equal(t, "myorg-fullsend", creds.Slug)
	assert.Equal(t, "Iv1.fullsend123", creds.ClientID)
	assert.Empty(t, creds.PEM, "PEM should be empty to signal reuse")
	// Should NOT have prompted — auto-reuse is silent.
	assert.False(t, prompter.confirmCalled, "should not prompt for reuse")
}

func TestSetup_ExistingApp_NoSecret(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "myorg-triage"},
		},
		AppClientIDs: map[string]string{
			"myorg-triage": "Iv1.triage123",
		},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithSecretExists(func(_ string) (bool, error) {
			return false, nil
		})

	_, err := s.Run(context.Background(), "myorg", "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private key")
}

func TestSetup_ExistingApp_ClientIDLookupFails(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "myorg-fullsend"},
		},
		// No AppClientIDs entry — GetAppClientID will return ErrNotFound.
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	_, err := s.Run(context.Background(), "myorg", "fullsend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client ID")
}

func TestSetup_KnownSlug_Match(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 200, AppID: 20, AppSlug: "custom-slug-name"},
		},
		AppClientIDs: map[string]string{
			"custom-slug-name": "Iv1.custom123",
		},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithKnownSlugs(map[string]string{"coder": "custom-slug-name"}).
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	creds, err := s.Run(context.Background(), "myorg", "coder")
	require.NoError(t, err)

	assert.Equal(t, 20, creds.AppID)
	assert.Equal(t, "custom-slug-name", creds.Slug)
	assert.Equal(t, "Iv1.custom123", creds.ClientID)
	assert.Empty(t, creds.PEM)
	assert.False(t, prompter.confirmCalled, "should not prompt for reuse")
}

func TestSetup_NoExistingApp(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer)

	// No existing app → manifest flow is started. Use a short context
	// timeout so the test doesn't hang waiting for a GitHub callback.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := s.Run(ctx, "myorg", "fullsend")
	require.Error(t, err)
	// The error should come from the manifest flow (context deadline),
	// not from the "existing app" checks.
	assert.NotContains(t, err.Error(), "private key")
	// Browser should have been asked to open a URL.
	select {
	case url := <-browser.urlCh:
		assert.NotEmpty(t, url, "should have tried to open browser")
	default:
		t.Error("browser.Open was never called")
	}
}

func TestManifestFlow_HTMLForm(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{},
	}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, &fakePrompter{}, browser, printer)

	// Use a short timeout — we only need the server to start and serve the
	// HTML page, not complete the full manifest flow.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run the manifest flow in a goroutine; it will block waiting for the
	// GitHub callback until the context expires.
	errCh := make(chan error, 1)
	go func() {
		_, err := s.Run(ctx, "testorg", "coder")
		errCh <- err
	}()

	// Wait for the browser to receive the URL.
	var formURL string
	select {
	case formURL = <-browser.urlCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser.Open to be called")
	}

	// Fetch the HTML page from the local server.
	resp, err := http.Get(formURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	// 1. There must be exactly one hidden input named "manifest".
	manifestInputRe := regexp.MustCompile(`<input[^>]+name="manifest"[^>]*>`)
	manifestInputs := manifestInputRe.FindAllString(html, -1)
	assert.Len(t, manifestInputs, 1, "expected exactly one hidden input named 'manifest'")

	// 2. There must be NO hidden input named "redirect_url".
	redirectInputRe := regexp.MustCompile(`<input[^>]+name="redirect_url"[^>]*>`)
	redirectInputs := redirectInputRe.FindAllString(html, -1)
	assert.Empty(t, redirectInputs, "there must be no hidden input named 'redirect_url'")

	// 3. The manifest JSON must contain redirect_url matching the callback URL.
	valueRe := regexp.MustCompile(`<input[^>]+name="manifest"[^>]+value="([^"]*)"`)
	matches := valueRe.FindStringSubmatch(html)
	require.Len(t, matches, 2, "could not extract manifest value from HTML")

	// The value is HTML-escaped; decode it.
	manifestJSON := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&#34;", "\"",
		"&#39;", "'",
	).Replace(matches[1])

	var manifest map[string]interface{}
	err = json.Unmarshal([]byte(manifestJSON), &manifest)
	require.NoError(t, err, "manifest value must be valid JSON")

	redirectURL, ok := manifest["redirect_url"]
	require.True(t, ok, "manifest JSON must contain redirect_url key")

	// The callback URL should point to the local server's /callback path.
	redirectStr, isString := redirectURL.(string)
	require.True(t, isString, "redirect_url must be a string")
	assert.True(t, strings.HasPrefix(redirectStr, "http://127.0.0.1:"),
		"redirect_url should start with http://127.0.0.1:, got %s", redirectStr)
	assert.True(t, strings.HasSuffix(redirectStr, "/callback"),
		"redirect_url should end with /callback, got %s", redirectStr)

	// Wait for the flow to finish (context timeout).
	<-errCh
}

func TestSetup_StalePermissions_AllRolesChecked(t *testing.T) {
	client := &forge.FakeClient{
		AppClientIDs: map[string]string{
			"myorg-fullsend": "Iv1.abc",
			"myorg-triage":   "Iv1.def",
		},
		Installations: []forge.Installation{
			{
				ID: 100, AppID: 10, AppSlug: "myorg-fullsend",
				Permissions: map[string]string{
					"contents":       "write",
					"issues":         "read",
					"pull_requests":  "write",
					"checks":         "read",
					"administration": "write",
					"members":        "read",
					// missing: workflows
				},
			},
			{
				ID: 101, AppID: 11, AppSlug: "myorg-triage",
				Permissions: map[string]string{
					// has issues:read but needs issues:write
					"issues": "read",
				},
			},
		},
	}
	printer := ui.New(&discardWriter{})

	setup := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	// Run both roles — each should succeed individually.
	_, err := setup.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)
	_, err = setup.Run(context.Background(), "myorg", "triage")
	require.NoError(t, err)

	// PermissionErrors should report both apps.
	permErr := setup.PermissionErrors()
	require.Error(t, permErr)
	assert.Contains(t, permErr.Error(), "myorg-fullsend")
	assert.Contains(t, permErr.Error(), "myorg-triage")
}

func TestSetup_CorrectPermissions_NoError(t *testing.T) {
	client := &forge.FakeClient{
		AppClientIDs: map[string]string{
			"myorg-fullsend": "Iv1.abc",
		},
		Installations: []forge.Installation{
			{
				ID: 100, AppID: 10, AppSlug: "myorg-fullsend",
				Permissions: map[string]string{
					"actions":        "write",
					"contents":       "write",
					"workflows":      "write",
					"issues":         "read",
					"pull_requests":  "write",
					"checks":         "read",
					"administration": "write",
					"members":        "read",
				},
			},
		},
	}
	printer := ui.New(&discardWriter{})

	setup := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	_, err := setup.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	assert.NoError(t, setup.PermissionErrors())
}

// discardWriter implements io.Writer, discarding all output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
