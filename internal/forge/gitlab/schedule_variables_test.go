package gitlab

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineScheduleVariables(t *testing.T) {
	for _, response := range []string{`{"id":7,"owner":{"id":42},"description":"fullsend slash poll","variables":[{"key":"FULLSEND_POLL_MODE","value":"slash"}]}`, `invalid`, `denied`, `{"id":7}`, `{"id":7,"owner":{"id":0},"variables":[]}`} {
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
			if response == "invalid" || response == "denied" || response == `{"id":7}` || response == `{"id":7,"owner":{"id":0},"variables":[]}` {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int64(42), schedule.OwnerID)
			assert.Equal(t, "slash", schedule.Variables["FULLSEND_POLL_MODE"])
		})
	}
}
