package gitlab

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const deployKeysPath = "/api/v4/projects/myorg%2Fmyrepo/deploy_keys"

func TestCreateProjectDeployKey(t *testing.T) {
	client, mux := setupTest(t)
	called := false
	mux.HandleFunc(deployKeysPath, func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "fullsend-trigger", body["title"])
		assert.Equal(t, "ssh-ed25519 AAAAexample", body["key"])
		assert.Equal(t, true, body["can_push"])
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id": 12, "title": "fullsend-trigger", "can_push": true,
			"fingerprint_sha256": "SHA256:abc", "expires_at": nil,
		})
	})

	key, err := client.CreateProjectDeployKey(context.Background(), "myorg", "myrepo", "fullsend-trigger", "ssh-ed25519 AAAAexample", true)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, &DeployKey{ID: 12, Title: "fullsend-trigger", CanPush: true, FingerprintSHA256: "SHA256:abc"}, key)
}

func TestCreateProjectDeployKey_RejectsBadInputAndResponses(t *testing.T) {
	client, mux := setupTest(t)
	_, err := client.CreateProjectDeployKey(context.Background(), "myorg", "myrepo", " ", "ssh-ed25519 AAAA", false)
	require.Error(t, err)
	_, err = client.CreateProjectDeployKey(context.Background(), "myorg", "myrepo", "t", "", false)
	require.Error(t, err)

	responses := []struct {
		status int
		body   string
	}{
		{http.StatusCreated, `{}`},
		{http.StatusCreated, `{"id":0}`},
		{http.StatusCreated, `not json`},
		{http.StatusUnprocessableEntity, `{"message":"key has already been taken"}`},
	}
	n := 0
	mux.HandleFunc(deployKeysPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(responses[n].status)
		_, err := io.WriteString(w, responses[n].body)
		require.NoError(t, err)
	})
	for n = range responses {
		_, err := client.CreateProjectDeployKey(context.Background(), "myorg", "myrepo", "t", "ssh-ed25519 AAAA", false)
		assert.Error(t, err, responses[n].body)
	}
}

func TestListProjectDeployKeys(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc(deployKeysPath, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		if r.URL.Query().Get("page") == "1" {
			keys := make([]map[string]any, deployKeyPerPage)
			for i := range keys {
				keys[i] = map[string]any{"id": i + 1, "title": fmt.Sprintf("k%d", i+1)}
			}
			writeJSON(t, w, http.StatusOK, keys)
			return
		}
		assert.Equal(t, "2", r.URL.Query().Get("page"))
		writeJSON(t, w, http.StatusOK, []map[string]any{{"id": 500, "title": "last", "can_push": true}})
	})

	got, err := client.ListProjectDeployKeys(context.Background(), "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, got, deployKeyPerPage+1)
	assert.Equal(t, DeployKey{ID: 500, Title: "last", CanPush: true}, got[deployKeyPerPage])
}

func TestListProjectDeployKeys_RejectsMalformedInventory(t *testing.T) {
	for _, body := range []string{`null`, `[{}]`, `[{"id":-1}]`, `{`} {
		t.Run(body, func(t *testing.T) {
			client, mux := setupTest(t)
			called := false
			mux.HandleFunc(deployKeysPath, func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, body)
				require.NoError(t, err)
			})
			_, err := client.ListProjectDeployKeys(context.Background(), "myorg", "myrepo")
			assert.Error(t, err)
			assert.True(t, called, "handler was not called — URL path mismatch")
		})
	}

	client, mux := setupTest(t)
	called := false
	mux.HandleFunc(deployKeysPath, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusBadRequest)
	})
	_, err := client.ListProjectDeployKeys(context.Background(), "myorg", "myrepo")
	assert.Error(t, err)
	assert.True(t, called, "handler was not called — URL path mismatch")
}

func TestListProjectDeployKeys_PaginationLimit(t *testing.T) {
	client, mux := setupTest(t)
	full := make([]map[string]any, deployKeyPerPage)
	for i := range full {
		full[i] = map[string]any{"id": i + 1}
	}
	mux.HandleFunc(deployKeysPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, full)
	})
	_, err := client.ListProjectDeployKeys(context.Background(), "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination exceeded")
}

func TestDeleteProjectDeployKey(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{http.StatusNoContent, false},
		{http.StatusNotFound, false},
		{http.StatusBadRequest, true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			client, mux := setupTest(t)
			called := false
			mux.HandleFunc(deployKeysPath+"/12", func(w http.ResponseWriter, r *http.Request) {
				called = true
				assert.Equal(t, http.MethodDelete, r.Method)
				w.WriteHeader(tc.status)
			})
			err := client.DeleteProjectDeployKey(context.Background(), "myorg", "myrepo", 12)
			assert.Equal(t, tc.wantErr, err != nil, err)
			assert.True(t, called)
		})
	}

	client, _ := setupTest(t)
	assert.Error(t, client.DeleteProjectDeployKey(context.Background(), "myorg", "myrepo", 0))
}

func TestGetProtectedBranchReportsDeployKeyGrants(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"name": "main",
			"push_access_levels": []map[string]any{
				{"id": 5, "access_level": 40, "deploy_key_id": 12, "user_id": nil, "group_id": nil},
			},
		})
	})

	rule, err := client.GetProtectedBranch(context.Background(), "myorg", "myrepo", "main")
	require.NoError(t, err)
	require.Len(t, rule.PushAccessLevels, 1)
	assert.Equal(t, 12, rule.PushAccessLevels[0].DeployKeyID)
	assert.Equal(t, 5, rule.PushAccessLevels[0].ID)
	assert.Zero(t, rule.PushAccessLevels[0].UserID)
}

func TestGrantProtectedBranchPushDeployKey(t *testing.T) {
	client, mux := setupTest(t)
	patches := revokeServer(t, mux, map[string]any{
		"name":               "main",
		"push_access_levels": []map[string]any{{"id": 1, "access_level": 40}},
	})

	require.NoError(t, client.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", "main", 12))
	require.Len(t, *patches, 1)
	allowed, ok := (*patches)[0]["allowed_to_push"].([]any)
	require.True(t, ok)
	require.Len(t, allowed, 1)
	assert.Equal(t, map[string]any{"deploy_key_id": float64(12)}, allowed[0])
	assert.NotContains(t, (*patches)[0], "allowed_to_merge")
}

func TestGrantProtectedBranchPushDeployKey_AlreadyGranted(t *testing.T) {
	client, mux := setupTest(t)
	patches := revokeServer(t, mux, map[string]any{
		"name":               "main",
		"push_access_levels": []map[string]any{{"id": 1, "access_level": 40, "deploy_key_id": 12}},
	})

	require.NoError(t, client.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", "main", 12))
	assert.Empty(t, *patches)
}

func TestGrantProtectedBranchPushDeployKey_FailsClosedWithoutExactRule(t *testing.T) {
	client, mux := setupTest(t)
	patches := revokeServer(t, mux, nil)

	err := client.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", "main", 12)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no exact-name protected-branch rule")
	assert.Empty(t, *patches)

	assert.Error(t, client.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", "main", 0))
}

func TestProtectedBranchDeployKey_RejectsWildcardBranch(t *testing.T) {
	for _, branch := range []string{"*", "release/*", "feature-*-x"} {
		t.Run(branch, func(t *testing.T) {
			client, mux := setupTest(t)
			requests := 0
			mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/", func(w http.ResponseWriter, r *http.Request) {
				requests++
				writeJSON(t, w, http.StatusOK, map[string]any{"name": branch})
			})

			err := client.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", branch, 12)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "wildcard")

			err = client.RevokeProtectedBranchPushDeployKeyEntry(context.Background(), "myorg", "myrepo", branch, 5, 12)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "wildcard")

			assert.Zero(t, requests, "wildcard branch must not send any request")
		})
	}
}

func TestGrantProtectedBranchPushDeployKey_Errors(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, map[string]any{"name": "main"})
			return
		}
		writeJSON(t, w, http.StatusUnprocessableEntity, map[string]any{"message": "deploy key is not enabled for this project"})
	})
	err := client.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", "main", 12)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploy key is not enabled")

	failing, failingMux := setupTest(t)
	failingMux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	assert.Error(t, failing.GrantProtectedBranchPushDeployKey(context.Background(), "myorg", "myrepo", "main", 12))
}

func TestRevokeProtectedBranchPushDeployKeyEntry(t *testing.T) {
	client, mux := setupTest(t)
	patches := revokeServer(t, mux, map[string]any{
		"name": "main",
		"push_access_levels": []map[string]any{
			{"id": 1, "access_level": 40},
			{"id": 5, "access_level": 40, "deploy_key_id": 12},
		},
	})

	require.NoError(t, client.RevokeProtectedBranchPushDeployKeyEntry(context.Background(), "myorg", "myrepo", "main", 5, 12))
	require.Len(t, *patches, 1)
	assert.Equal(t, map[string]any{
		"allowed_to_push": []any{map[string]any{"id": float64(5), "_destroy": true}},
	}, (*patches)[0])
}

func TestRevokeProtectedBranchPushDeployKeyEntry_RefusesOrSkips(t *testing.T) {
	exact := map[string]any{
		"name": "main",
		"push_access_levels": []map[string]any{
			{"id": 1, "access_level": 40},
			{"id": 5, "access_level": 40, "deploy_key_id": 99},
		},
	}
	for _, tc := range []struct {
		name    string
		exact   map[string]any
		entryID int
		keyID   int
		wantErr bool
	}{
		{"role entry is not the key", exact, 1, 12, true},
		{"other key's entry", exact, 5, 12, true},
		{"entry already gone", exact, 7, 12, false},
		{"rule already gone", nil, 5, 12, false},
		{"invalid entry ID", exact, 0, 12, true},
		{"invalid key ID", exact, 5, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, mux := setupTest(t)
			patches := revokeServer(t, mux, tc.exact)
			err := client.RevokeProtectedBranchPushDeployKeyEntry(context.Background(), "myorg", "myrepo", "main", tc.entryID, tc.keyID)
			assert.Equal(t, tc.wantErr, err != nil, err)
			assert.Empty(t, *patches)
		})
	}
}

func TestRevokeProtectedBranchPushDeployKeyEntry_Errors(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"name":               "main",
				"push_access_levels": []map[string]any{{"id": 5, "access_level": 40, "deploy_key_id": 12}},
			})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	assert.Error(t, client.RevokeProtectedBranchPushDeployKeyEntry(context.Background(), "myorg", "myrepo", "main", 5, 12))

	failing, failingMux := setupTest(t)
	failingMux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	assert.Error(t, failing.RevokeProtectedBranchPushDeployKeyEntry(context.Background(), "myorg", "myrepo", "main", 5, 12))
}
