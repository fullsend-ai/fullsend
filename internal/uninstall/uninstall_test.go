package uninstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstallationsPath = "/orgs/my-org/installations"

type fakePrompter struct {
	responses []bool
	idx       int
}

func (f *fakePrompter) ConfirmWithInput(_, _ string) (bool, error) {
	if f.idx < len(f.responses) {
		r := f.responses[f.idx]
		f.idx++
		return r, nil
	}
	return true, nil
}

type fakeBrowser struct {
	opened []string
}

func (f *fakeBrowser) Open(_ context.Context, url string) error {
	f.opened = append(f.opened, url)
	return nil
}

func newTestUninstaller(t *testing.T, client *forge.FakeClient, apiSrv *httptest.Server, confirmed bool) (*Uninstaller, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	printer := ui.NewPrinter(&buf)

	prompt := &fakePrompter{responses: []bool{confirmed}}
	browser := &fakeBrowser{}

	opts := []Option{}
	if apiSrv != nil {
		opts = append(opts, WithBaseURL(apiSrv.URL))
	}
	opts = append(opts, WithWebURL("https://github.com"))

	un := New(client, printer, prompt, browser, "test-token", opts...)
	return un, &buf
}

func TestUninstall_FullFlow(t *testing.T) {
	client := forge.NewFakeClient()
	// Pre-populate .fullsend/config.yaml
	err := client.CreateFile(context.Background(), "my-org", ".fullsend", "config.yaml", "init",
		[]byte("version: '1'\napp:\n  name: fullsend-my-org\n  slug: fullsend-my-org\n"))
	require.NoError(t, err)

	// Mock the GitHub API for installations
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == testInstallationsPath {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"installations": []map[string]any{
					{"app_slug": "fullsend-my-org", "id": 42},
				},
			})
			return
		}

		if r.Method == http.MethodDelete && r.URL.Path == "/user/installations/42" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer apiSrv.Close()

	un, output := newTestUninstaller(t, client, apiSrv, true)

	runErr := un.Run(context.Background(), Options{Org: "my-org"})
	require.NoError(t, runErr)

	// Should have read config
	assert.Contains(t, output.String(), "fullsend-my-org")

	// Should have deleted the repo
	assert.Len(t, client.DeletedRepos, 1)
	assert.Equal(t, ".fullsend", client.DeletedRepos[0].Repo)

	// Should show completion
	assert.Contains(t, output.String(), "Uninstall complete")
}

func TestUninstall_Aborted(t *testing.T) {
	client := forge.NewFakeClient()

	un, output := newTestUninstaller(t, client, nil, false)

	err := un.Run(context.Background(), Options{Org: "my-org"})
	require.NoError(t, err)

	assert.Contains(t, output.String(), "Aborted")
	assert.Empty(t, client.DeletedRepos)
}

func TestUninstall_Yolo(t *testing.T) {
	client := forge.NewFakeClient()
	err := client.CreateFile(context.Background(), "my-org", ".fullsend", "config.yaml", "init",
		[]byte("version: '1'\napp:\n  name: my-app\n  slug: my-app\n"))
	require.NoError(t, err)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == testInstallationsPath {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"installations": []map[string]any{
					{"app_slug": "my-app", "id": 99},
				},
			})
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiSrv.Close()

	// Pass confirmed=false, but Yolo=true should skip the prompt
	un, _ := newTestUninstaller(t, client, apiSrv, false)

	runErr := un.Run(context.Background(), Options{Org: "my-org", Yolo: true})
	require.NoError(t, runErr)

	// Should have deleted despite no confirmation
	assert.Len(t, client.DeletedRepos, 1)
}

func TestUninstall_ConfigReadFails_FallsBackToScan(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetFileContent"] = errors.New("not found")

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == testInstallationsPath {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"installations": []map[string]any{
					{"app_slug": "fullsend-my-org", "id": 10},
				},
			})
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiSrv.Close()

	un, output := newTestUninstaller(t, client, apiSrv, true)

	err := un.Run(context.Background(), Options{Org: "my-org", Yolo: true})
	require.NoError(t, err)

	// Should have warned about config read failure
	assert.Contains(t, output.String(), "Could not read app slug")
	// But still found and removed the app
	assert.Contains(t, output.String(), "fullsend-my-org")
}

func TestUninstall_DeleteRepoFails_Continues(t *testing.T) {
	client := forge.NewFakeClient()
	err := client.CreateFile(context.Background(), "my-org", ".fullsend", "config.yaml", "init",
		[]byte("version: '1'\napp:\n  name: my-app\n  slug: my-app\n"))
	require.NoError(t, err)
	client.Errors["DeleteRepo"] = errors.New("permission denied")

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == testInstallationsPath {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"installations": []map[string]any{
					{"app_slug": "my-app", "id": 7},
				},
			})
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiSrv.Close()

	un, output := newTestUninstaller(t, client, apiSrv, true)

	runErr := un.Run(context.Background(), Options{Org: "my-org", Yolo: true})
	require.NoError(t, runErr)

	assert.Contains(t, output.String(), "Failed to delete .fullsend repo")
	// Should still proceed to remove app installation
	assert.Contains(t, output.String(), "Removed my-app installation")
}

func TestReadAppSlug(t *testing.T) {
	client := forge.NewFakeClient()
	err := client.CreateFile(context.Background(), "org", ".fullsend", "config.yaml", "init",
		[]byte("version: '1'\napp:\n  name: test-app\n  slug: test-app\n"))
	require.NoError(t, err)

	var buf bytes.Buffer
	un := New(client, ui.NewPrinter(&buf), nil, nil, "tok")

	slug, readErr := un.readAppSlug(context.Background(), "org")
	require.NoError(t, readErr)
	assert.Equal(t, "test-app", slug)
}

func TestReadAppSlug_NoSlug(t *testing.T) {
	client := forge.NewFakeClient()
	err := client.CreateFile(context.Background(), "org", ".fullsend", "config.yaml", "init",
		[]byte("version: '1'\n"))
	require.NoError(t, err)

	var buf bytes.Buffer
	un := New(client, ui.NewPrinter(&buf), nil, nil, "tok")

	_, readErr := un.readAppSlug(context.Background(), "org")
	assert.Error(t, readErr)
	assert.Contains(t, readErr.Error(), "no app slug")
}
