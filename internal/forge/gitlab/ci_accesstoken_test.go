package gitlab

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateProjectAccessToken(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "fullsend-bot", body["name"])
		assert.Equal(t, []any{"api"}, body["scopes"])
		assert.Equal(t, float64(40), body["access_level"])
		assert.Equal(t, "2027-07-21", body["expires_at"])

		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":     1,
			"name":   "fullsend-bot",
			"active": true,
			"token":  "glpat-xxxxxxxxxxxxxxxxxxxx",
		})
	})

	token, err := client.CreateProjectAccessToken(ctx, "mygroup", "myproject", "fullsend-bot",
		[]string{"api"}, 40, "2027-07-21")
	require.NoError(t, err)
	assert.Equal(t, 1, token.ID)
	assert.Equal(t, "fullsend-bot", token.Name)
	assert.True(t, token.Active)
	assert.Equal(t, "glpat-xxxxxxxxxxxxxxxxxxxx", token.Token)
}

func TestCreateProjectAccessToken_FreeTierError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{
			"message": "403 Forbidden",
		})
	})

	_, err := client.CreateProjectAccessToken(ctx, "mygroup", "myproject", "fullsend-bot",
		[]string{"api"}, 40, "2027-07-21")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create project access token")
}

func TestListProjectAccessTokens(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "name": "fullsend-bot", "active": true},
			{"id": 2, "name": "deploy-token", "active": true},
			{"id": 3, "name": "fullsend-bot", "active": false},
		})
	})

	tokens, err := client.ListProjectAccessTokens(ctx, "mygroup", "myproject")
	require.NoError(t, err)
	require.Len(t, tokens, 3)
	assert.Equal(t, "fullsend-bot", tokens[0].Name)
	assert.True(t, tokens[0].Active)
	assert.Equal(t, "deploy-token", tokens[1].Name)
	assert.False(t, tokens[2].Active)
}

func TestRevokeProjectAccessToken(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/access_tokens/1", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.RevokeProjectAccessToken(ctx, "mygroup", "myproject", 1)
	require.NoError(t, err)
}

func TestRevokeProjectAccessToken_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/access_tokens/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"message": "404 Not Found",
		})
	})

	err := client.RevokeProjectAccessToken(ctx, "mygroup", "myproject", 999)
	require.Error(t, err)
}
