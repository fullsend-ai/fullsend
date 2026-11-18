package gitlablifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// The bootstrap client creates the account's schedules without pipeline
// variables, and a disabled schedule is created disabled in the create request
// itself, never active first (#8508).
func TestBootstrapClientCreatesVariableFreeSchedule(t *testing.T) {
	var created map[string]any
	var variablePosts, otherRequests int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, http.MethodPost, r.Method) {
			return
		}
		assert.Equal(t, "schedule fixture credential", r.Header.Get("PRIVATE-TOKEN"))
		created = nil
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&created))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, err := w.Write([]byte(`{"id":31}`))
		assert.NoError(t, err)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/pipeline_schedules/31/variables", func(http.ResponseWriter, *http.Request) {
		variablePosts++
	})
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {
		otherRequests++
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := gitlab.New("schedule fixture credential", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	b := bootstrapClient{c: c}

	id, err := b.CreatePipelineSchedule(context.Background(), "g", "p", "main", "fullsend slash poll", "*/5 * * * *", true)
	require.NoError(t, err)
	assert.Equal(t, int64(31), id)
	assert.Equal(t, "main", created["ref"])
	assert.Equal(t, "fullsend slash poll", created["description"])
	assert.Equal(t, "*/5 * * * *", created["cron"])
	assert.NotContains(t, created, "active", "an active schedule keeps GitLab's default")

	id, err = b.CreatePipelineSchedule(context.Background(), "g", "p", "main", "fullsend event poll", "*/5 * * * *", false)
	require.NoError(t, err)
	assert.Equal(t, int64(31), id)
	assert.Equal(t, false, created["active"], "a disabled schedule is created disabled")

	assert.Zero(t, variablePosts, "no pipeline variable is set on the account's schedule")
	assert.Zero(t, otherRequests, "no separate request edits the schedule")
}
