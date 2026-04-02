package appsetup

import (
	"context"
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
	openedURLs []string
}

func (f *fakeBrowser) Open(_ context.Context, url string) error {
	f.openedURLs = append(f.openedURLs, url)
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
			expected: "fullsend-myorg",
		},
		{
			name:     "triage role appends role suffix",
			org:      "myorg",
			role:     "triage",
			expected: "fullsend-myorg-triage",
		},
		{
			name:     "coder role appends role suffix",
			org:      "acme",
			role:     "coder",
			expected: "fullsend-acme-coder",
		},
		{
			name:     "review role appends role suffix",
			org:      "acme",
			role:     "review",
			expected: "fullsend-acme-review",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := expectedAppSlug(tc.org, tc.role)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestSetup_ExistingApp_SecretExists_Reuse(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-myorg"},
		},
	}
	prompter := &fakePrompter{confirmResult: true}
	browser := &fakeBrowser{}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	creds, err := s.Run(context.Background(), "myorg", "fullsend")
	require.NoError(t, err)

	// Should return credentials signaling reuse (empty PEM).
	assert.Equal(t, 10, creds.AppID)
	assert.Equal(t, "fullsend-myorg", creds.Slug)
	assert.Empty(t, creds.PEM, "PEM should be empty to signal reuse")
	assert.True(t, prompter.confirmCalled, "should have asked to confirm reuse")
}

func TestSetup_ExistingApp_SecretExists_DeclineReuse(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-myorg"},
		},
	}
	prompter := &fakePrompter{confirmResult: false}
	browser := &fakeBrowser{}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithSecretExists(func(_ string) (bool, error) {
			return true, nil
		})

	// When the user declines reuse, an error is returned telling them
	// to delete the app first.
	_, err := s.Run(context.Background(), "myorg", "fullsend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declined")
	assert.True(t, prompter.confirmCalled, "should have asked to confirm reuse")
}

func TestSetup_ExistingApp_NoSecret(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 100, AppID: 10, AppSlug: "fullsend-myorg-triage"},
		},
	}
	prompter := &fakePrompter{}
	browser := &fakeBrowser{}
	printer := ui.New(&discardWriter{})

	s := NewSetup(client, prompter, browser, printer).
		WithSecretExists(func(_ string) (bool, error) {
			return false, nil
		})

	_, err := s.Run(context.Background(), "myorg", "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private key")
}

func TestSetup_KnownSlug_Match(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{
			{ID: 200, AppID: 20, AppSlug: "custom-slug-name"},
		},
	}
	prompter := &fakePrompter{confirmResult: true}
	browser := &fakeBrowser{}
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
	assert.Empty(t, creds.PEM)
}

func TestSetup_NoExistingApp(t *testing.T) {
	client := &forge.FakeClient{
		Installations: []forge.Installation{},
	}
	prompter := &fakePrompter{}
	browser := &fakeBrowser{}
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
	assert.NotEmpty(t, browser.openedURLs, "should have tried to open browser")
}

// discardWriter implements io.Writer, discarding all output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
