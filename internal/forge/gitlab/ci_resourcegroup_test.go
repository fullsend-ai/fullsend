package gitlab

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListResourceGroups(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"key": "fullsend-poll", "process_mode": "unordered"},
			{"key": "fullsend-triage-mr-1", "process_mode": "newest_first"},
			{"key": "production", "process_mode": "oldest_first"},
		})
	})

	groups, err := client.ListResourceGroups(ctx, "mygroup", "myproject")
	require.NoError(t, err)
	require.Len(t, groups, 3)
	assert.Equal(t, "fullsend-poll", groups[0].Key)
	assert.Equal(t, "unordered", groups[0].ProcessMode)
	assert.Equal(t, "fullsend-triage-mr-1", groups[1].Key)
	assert.Equal(t, "newest_first", groups[1].ProcessMode)
	assert.Equal(t, "production", groups[2].Key)
}

func TestListResourceGroups_Empty(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})

	groups, err := client.ListResourceGroups(ctx, "mygroup", "myproject")
	require.NoError(t, err)
	assert.Empty(t, groups)
}

func TestListResourceGroups_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "forbidden"})
	})

	_, err := client.ListResourceGroups(ctx, "mygroup", "myproject")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list resource groups")
}

func TestUpdateResourceGroupProcessMode(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups/fullsend-poll", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)

		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "newest_first", body["process_mode"])

		writeJSON(t, w, http.StatusOK, map[string]any{
			"key":          "fullsend-poll",
			"process_mode": "newest_first",
		})
	})

	err := client.UpdateResourceGroupProcessMode(ctx, "mygroup", "myproject", "fullsend-poll", "newest_first")
	require.NoError(t, err)
}

func TestUpdateResourceGroupProcessMode_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups/fullsend-poll", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "not found"})
	})

	err := client.UpdateResourceGroupProcessMode(ctx, "mygroup", "myproject", "fullsend-poll", "newest_first")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update resource group")
}
