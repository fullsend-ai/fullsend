package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransitions(t *testing.T) {
	c, mux := setupTest(t)
	gets, posts := 0, 0
	mux.HandleFunc("/rest/api/3/issue/PROJ-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			assert.Equal(t, "transitions.fields", r.URL.Query().Get("expand"))
			writeJSON(t, w, 200, map[string]any{"transitions": []Transition{{ID: "21", Name: "Review", Fields: map[string]TransitionField{"resolution": {Required: true, AllowedValues: []json.RawMessage{json.RawMessage(`{"id":"1","name":"Fixed"}`)}}}}}})
			return
		}
		posts++
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, map[string]any{"id": "21"}, body["transition"])
		assert.Equal(t, map[string]any{"resolution": map[string]any{"id": "1"}}, body["fields"])
		updates := body["update"].(map[string]any)["comment"].([]any)
		adf := updates[0].(map[string]any)["add"].(map[string]any)["body"]
		assert.Contains(t, ADFToMarkdown(adf), "Ready")
		w.WriteHeader(204)
	})
	tr, err := c.ListTransitions(context.Background(), "PROJ-1")
	require.NoError(t, err)
	require.Len(t, tr, 1)
	assert.True(t, tr[0].Fields["resolution"].Required)
	require.NoError(t, c.ApplyTransition(context.Background(), "PROJ-1", "21", map[string]any{"resolution": map[string]string{"id": "1"}}, "**Ready**"))
	assert.Equal(t, 1, gets)
	assert.Equal(t, 1, posts)
}

func TestTransitionErrorsAndNoRetry(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, mux := setupTest(t)
			calls := 0
			mux.HandleFunc("/rest/api/3/issue/P-1/transitions", func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"errorMessages":["denied"]}`))
			})
			err := c.ApplyTransition(context.Background(), "P-1", "1", nil, "")
			require.Error(t, err)
			assert.Equal(t, 1, calls)
			if status < 500 {
				_, err = c.ListTransitions(context.Background(), "P-1")
				require.Error(t, err)
			}
		})
	}
	c, _ := setupTest(t)
	require.ErrorContains(t, c.ApplyTransition(context.Background(), "P-1", "1", map[string]any{"bad": make(chan int)}, ""), "encode transition")
}
