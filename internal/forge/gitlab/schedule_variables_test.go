package gitlab

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineScheduleVariables(t *testing.T) {
	for _, response := range []string{`{"id":7,"description":"fullsend slash poll","variables":[{"key":"FULLSEND_POLL_MODE","value":"slash"}]}`, `invalid`, `denied`, `{"id":7}`} {
		t.Run(response, func(t *testing.T) {
			client, mux := setupTest(t)
			called := false
			mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules/7", func(w http.ResponseWriter, r *http.Request) {
				called = true
				assert.Equal(t, http.MethodGet, r.Method)
				if response == "denied" {
					w.WriteHeader(http.StatusForbidden)
				}
				_, _ = w.Write([]byte(response))
			})
			schedule, err := client.GetPipelineSchedule(context.Background(), "myorg", "myrepo", 7)
			assert.True(t, called)
			if response == "invalid" || response == "denied" || response == `{"id":7}` {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "slash", schedule.Variables["FULLSEND_POLL_MODE"])
		})
	}
	client, mux := setupTest(t)
	called := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules/7/variables/FULLSEND_POLL_MODE", func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	require.NoError(t, client.DeletePipelineScheduleVariable(context.Background(), "myorg", "myrepo", 7, "FULLSEND_POLL_MODE"))
	assert.True(t, called)
}
