package appsetup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	confirmResult  bool
	waitCalled     bool
	confirmCalled  bool
	readLineResult string
	readLineCalled bool
}

func (f *fakePrompter) WaitForEnter(_ string) error {
	f.waitCalled = true
	return nil
}

func (f *fakePrompter) Confirm(_ string) (bool, error) {
	f.confirmCalled = true
	return f.confirmResult, nil
}

func (f *fakePrompter) ReadLine(_ string) (string, error) {
	f.readLineCalled = true
	return f.readLineResult, nil
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

func TestAppSlug(t *testing.T) {
	assert.Equal(t, "fullsend-ai-fullsend", AppSlug("fullsend-ai", "fullsend"))
	assert.Equal(t, "fullsend-ai-triage", AppSlug("fullsend-ai", "triage"))
	assert.Equal(t, "custom-coder", AppSlug("custom", "coder"))
	assert.Equal(t, "fullsend-review", AppSlug("fullsend", "review"))
}

func TestValidateAppSet(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "fullsend", false},
		{"valid with hyphen", "fullsend-ai", false},
		{"valid multi hyphen", "my-custom-app", false},
		{"valid numeric", "app2", false},
		{"valid starts with number", "42apps", false},
		{"empty", "", true},
		{"uppercase", "FullSend", true},
		{"leading hyphen", "-fullsend", true},
		{"trailing hyphen", "fullsend-", true},
		{"consecutive hyphens", "full--send", true},
		{"underscore", "full_send", true},
		{"space", "full send", true},
		{"special chars", "special!chars", true},
		{"too long", "a2345678901234567890123x", true},
		{"max length ok", "a234567890123456789012x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAppSet(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetup_NonDefaultAppSet_FlowsThrough(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 200, AppID: 20, AppSlug: "custom-prefix-fullsend"},
		},
		AppClientIDs: map[string]string{
			"custom-prefix-fullsend": "Iv1.custom123",
		},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithAppSet("custom-prefix").
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	assert.Equal(t, 20, creds.AppID)
	assert.Equal(t, "custom-prefix-fullsend", creds.Slug)
	assert.Equal(t, "Iv1.custom123", creds.ClientID)
}

func TestSetup_ExistingApp_SecretExists_AutoReuse(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-fullsend"},
		},
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.fullsend123",
		},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	// Should return credentials signaling reuse (empty PEM).
	assert.Equal(t, 10, creds.AppID)
	assert.Equal(t, "fullsend-fullsend", creds.Slug)
	assert.Equal(t, "Iv1.fullsend123", creds.ClientID)
	assert.Empty(t, creds.PEM, "PEM should be empty to signal reuse")
	// Should NOT have prompted — auto-reuse is silent.
	assert.False(t, prompter.confirmCalled, "should not prompt for reuse")
}

func TestSetup_ExistingApp_Reuse_StoreSecretNotCalled(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-fullsend"},
		},
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.fullsend123",
		},
	}
	printer := ui.New(&discardWriter{})

	storeCalled := false
	s := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil }).
		WithStoreSecret(func(_ context.Context, _, _ string) error {
			storeCalled = true
			return nil
		})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)
	assert.Empty(t, creds.PEM)
	assert.False(t, storeCalled, "storeSecret should not be called on reuse")
}

func TestSetup_RecoverCreatedApp_PEMExists(t *testing.T) {
	client := &forge.FakeClient{
		AppClientIDs: map[string]string{
			"fullsend-coder": "Iv1.coder456",
		},
	}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	creds, err := s.recoverCreatedApp(context.Background(), "myorg", "coder", "fullsend-coder")
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "fullsend-coder", creds.Slug)
	assert.Equal(t, "Iv1.coder456", creds.ClientID)
	assert.Empty(t, creds.PEM, "PEM should be empty — already stored")
}

func TestSetup_RecoverCreatedApp_KnownSlug(t *testing.T) {
	client := &forge.FakeClient{
		AppClientIDs: map[string]string{
			"custom-coder-app": "Iv1.custom789",
		},
	}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithKnownSlugs(map[string]string{"coder": "custom-coder-app"}).
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	creds, err := s.recoverCreatedApp(context.Background(), "myorg", "coder", "fullsend-coder")
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "custom-coder-app", creds.Slug)
	assert.Equal(t, "Iv1.custom789", creds.ClientID)
}

func TestSetup_RecoverCreatedApp_NoPEM(t *testing.T) {
	client := &forge.FakeClient{}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return false, nil })

	creds, err := s.recoverCreatedApp(context.Background(), "myorg", "coder", "fullsend-coder")
	require.NoError(t, err)
	assert.Nil(t, creds, "should return nil when no PEM exists")
}

func TestSetup_RecoverCreatedApp_NoSecretExistsFunc(t *testing.T) {
	client := &forge.FakeClient{}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend")

	creds, err := s.recoverCreatedApp(context.Background(), "myorg", "coder", "fullsend-coder")
	require.NoError(t, err)
	assert.Nil(t, creds, "should return nil when no secretExists func")
}

func TestSetup_ExistingApp_NoSecret(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-triage"},
		},
		AppClientIDs: map[string]string{
			"fullsend-triage": "Iv1.triage123",
		},
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithAppSet("fullsend").
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
			{ID: 100, AppID: 10, AppSlug: "fullsend-fullsend"},
		},
		// No AppClientIDs entry — GetAppClientID will return ErrNotFound.
	}
	prompter := &fakePrompter{}
	browser := newFakeBrowser()
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithAppSet("fullsend").
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
		WithAppSet("fullsend").
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

	s := NewSetup(client, prompter, browser, printer).
		WithAppSet("fullsend")

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

	s := NewSetup(client, &fakePrompter{}, browser, printer).
		WithAppSet("fullsend")

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
			"fullsend-fullsend": "Iv1.abc",
			"fullsend-triage":   "Iv1.def",
		},
		Installations: []forge.Installation{
			{
				ID: 100, AppID: 10, AppSlug: "fullsend-fullsend",
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
				ID: 101, AppID: 11, AppSlug: "fullsend-triage",
				Permissions: map[string]string{
					// has issues:read but needs issues:write
					"issues": "read",
				},
			},
		},
	}
	printer := ui.New(&discardWriter{})

	setup := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	// Run both roles — each should succeed individually.
	_, err := setup.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)
	_, err = setup.Run(context.Background(), "myorg", "triage")
	require.NoError(t, err)

	// PermissionErrors should report both apps.
	permErr := setup.PermissionErrors()
	require.Error(t, permErr)
	assert.Contains(t, permErr.Error(), "fullsend-fullsend")
	assert.Contains(t, permErr.Error(), "fullsend-triage")
}

func TestSetup_StalePermissions_IncludesInstallationURL(t *testing.T) {
	client := &forge.FakeClient{
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.abc",
		},
		Installations: []forge.Installation{
			{
				ID: 12345, AppID: 10, AppSlug: "fullsend-fullsend",
				Permissions: map[string]string{
					"contents":      "write",
					"issues":        "read",
					"pull_requests": "write",
					"checks":        "read",
					// missing some expected permissions
				},
			},
		},
	}
	printer := ui.New(&discardWriter{})

	setup := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	_, err := setup.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	permErr := setup.PermissionErrors()
	require.Error(t, permErr)
	errMsg := permErr.Error()
	assert.Contains(t, errMsg, "/settings/apps/fullsend-fullsend/permissions",
		"error should contain app permissions URL")
	assert.Contains(t, errMsg, "/settings/installations/12345",
		"error should contain installation approval URL")
	assert.Contains(t, errMsg, "organizations/myorg",
		"both URLs should reference the correct org")
}

func TestSetup_CorrectPermissions_NoError(t *testing.T) {
	client := &forge.FakeClient{
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.abc",
		},
		Installations: []forge.Installation{
			{
				ID: 100, AppID: 10, AppSlug: "fullsend-fullsend",
				Permissions: map[string]string{
					"actions":               "write",
					"contents":              "write",
					"actions_variables":     "read",
					"workflows":             "write",
					"issues":                "read",
					"pull_requests":         "write",
					"checks":                "read",
					"administration":        "write",
					"members":               "read",
					"organization_projects": "read",
				},
			},
		},
	}
	printer := ui.New(&discardWriter{})

	setup := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	_, err := setup.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	assert.NoError(t, setup.PermissionErrors())
}

func TestSetup_DefaultAppSet(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-coder"},
		},
		AppClientIDs: map[string]string{
			"fullsend-coder": "Iv1.default123",
		},
	}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, &fakePrompter{}, newFakeBrowser(), printer).
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	creds, err := s.Run(context.Background(), "myorg", "coder")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-coder", creds.Slug)
	assert.Equal(t, "Iv1.default123", creds.ClientID)
}

// --- PEM recovery tests ---

func generateTestPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func writeTempPEM(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test-key.pem")
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path
}

func existingAppSetup(t *testing.T, prompter *fakePrompter) (*Setup, *forge.FakeClient) {
	t.Helper()
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-triage"},
		},
		AppClientIDs: map[string]string{
			"fullsend-triage": "Iv1.triage123",
		},
	}
	printer := ui.New(&discardWriter{})
	s := NewSetup(client, prompter, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return false, nil })
	return s, client
}

func TestSetup_ExistingApp_PEMRecovery_HappyPath(t *testing.T) {
	pemData := generateTestPEM(t)
	pemPath := writeTempPEM(t, pemData)

	prompter := &fakePrompter{confirmResult: true, readLineResult: pemPath}
	var storedRole, storedPEM string
	s, _ := existingAppSetup(t, prompter)
	s = s.WithStoreSecret(func(_ context.Context, role, p string) error {
		storedRole = role
		storedPEM = p
		return nil
	})

	creds, err := s.Run(context.Background(), "myorg", "triage")
	require.NoError(t, err)
	assert.Equal(t, 10, creds.AppID)
	assert.Equal(t, "fullsend-triage", creds.Slug)
	assert.Equal(t, "Iv1.triage123", creds.ClientID)
	assert.NotEmpty(t, creds.PEM)
	assert.Equal(t, "triage", storedRole)
	assert.NotEmpty(t, storedPEM)
	assert.True(t, prompter.confirmCalled)
	assert.True(t, prompter.readLineCalled)
}

func TestSetup_ExistingApp_PEMRecovery_UserDeclines(t *testing.T) {
	prompter := &fakePrompter{confirmResult: false}
	s, _ := existingAppSetup(t, prompter)

	_, err := s.Run(context.Background(), "myorg", "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private key secret is missing")
	assert.True(t, prompter.confirmCalled)
	assert.False(t, prompter.readLineCalled)
}

func TestSetup_ExistingApp_PEMRecovery_InvalidPEM(t *testing.T) {
	badPath := writeTempPEM(t, []byte("not a valid pem"))
	prompter := &fakePrompter{confirmResult: true, readLineResult: badPath}
	s, _ := existingAppSetup(t, prompter)

	_, err := s.Run(context.Background(), "myorg", "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM file")
}

func TestSetup_ExistingApp_PEMRecovery_FileNotFound(t *testing.T) {
	prompter := &fakePrompter{confirmResult: true, readLineResult: "/nonexistent/path.pem"}
	s, _ := existingAppSetup(t, prompter)

	_, err := s.Run(context.Background(), "myorg", "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking PEM file")
}

func TestSetup_ExistingApp_StaleAppID_TriggersRecovery(t *testing.T) {
	pemData := generateTestPEM(t)
	pemPath := writeTempPEM(t, pemData)

	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 20, AppSlug: "fullsend-fullsend"},
		},
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.fullsend123",
		},
	}
	prompter := &fakePrompter{confirmResult: true, readLineResult: pemPath}
	printer := ui.New(&discardWriter{})

	var storedPEM string
	s := NewSetup(client, prompter, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil }).
		WithStoredAppIDs(map[string]string{"myorg/fullsend": "10"}).
		WithStoreSecret(func(_ context.Context, _, p string) error {
			storedPEM = p
			return nil
		})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)
	assert.Equal(t, 20, creds.AppID)
	assert.NotEmpty(t, creds.PEM, "should have new PEM from recovery")
	assert.NotEmpty(t, storedPEM, "should have stored new PEM")
	assert.True(t, prompter.confirmCalled, "should prompt for PEM recovery")
}

func TestSetup_ExistingApp_MatchingAppID_Reuses(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-fullsend"},
		},
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.fullsend123",
		},
	}
	prompter := &fakePrompter{}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil }).
		WithStoredAppIDs(map[string]string{"myorg/fullsend": "10"})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)
	assert.Equal(t, 10, creds.AppID)
	assert.Empty(t, creds.PEM, "PEM should be empty to signal reuse")
	assert.False(t, prompter.confirmCalled, "should not prompt — IDs match")
}

func TestSetup_ExistingApp_NoStoredIDs_Reuses(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-fullsend"},
		},
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.fullsend123",
		},
	}
	prompter := &fakePrompter{}
	printer := ui.New(&discardWriter{})

	// No WithStoredAppIDs — backwards compatible behavior.
	s := NewSetup(client, prompter, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil })

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)
	assert.Equal(t, 10, creds.AppID)
	assert.Empty(t, creds.PEM, "PEM should be empty to signal reuse")
	assert.False(t, prompter.confirmCalled, "should not prompt — no stored IDs to compare")
}

func TestIsAppIDStale(t *testing.T) {
	s := &Setup{}

	t.Run("nil map returns false", func(t *testing.T) {
		assert.False(t, s.isAppIDStale("org", "role", 10))
	})

	s.storedAppIDs = map[string]string{
		"myorg/fullsend":   "10",
		"myorg/prioritize": "20",
	}

	t.Run("matching ID returns false", func(t *testing.T) {
		assert.False(t, s.isAppIDStale("myorg", "fullsend", 10))
	})

	t.Run("mismatched ID returns true", func(t *testing.T) {
		assert.True(t, s.isAppIDStale("myorg", "fullsend", 99))
	})

	t.Run("unknown key returns false", func(t *testing.T) {
		assert.False(t, s.isAppIDStale("otherog", "fullsend", 10))
	})
}

func TestSetup_ExistingApp_StaleAppID_UserDeclines(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 20, AppSlug: "fullsend-fullsend"},
		},
		AppClientIDs: map[string]string{
			"fullsend-fullsend": "Iv1.fullsend123",
		},
	}
	prompter := &fakePrompter{confirmResult: false}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, newFakeBrowser(), printer).
		WithAppSet("fullsend").
		WithSecretExists(func(_ string) (bool, error) { return true, nil }).
		WithStoredAppIDs(map[string]string{"myorg/fullsend": "10"})

	_, err := s.Run(context.Background(), "myorg", "fullsend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was recreated")
	assert.True(t, prompter.confirmCalled, "should prompt for PEM recovery")
	assert.False(t, prompter.readLineCalled, "should not ask for file path after decline")
}

func TestValidateRSAPEM_Valid(t *testing.T) {
	assert.NoError(t, ValidateRSAPEM(generateTestPEM(t)))
}

func TestValidateRSAPEM_InvalidBlock(t *testing.T) {
	assert.Error(t, ValidateRSAPEM([]byte("garbage data")))
}

func TestValidateRSAPEM_NonRSAKey(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(ecKey)
	require.NoError(t, err)
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	err = ValidateRSAPEM(ecPEM)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected RSA")
}

// discardWriter implements io.Writer, discarding all output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
