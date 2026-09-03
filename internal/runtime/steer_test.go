package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SteerResult is written to metrics.json under RunMetrics.Steers, whose
// sibling fields are snake_case.
func TestSteerResultJSON(t *testing.T) {
	got, err := json.Marshal(SteerResult{
		FollowUpRunID: 101,
		DeliveredAt:   time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Mode:          "live",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"follow_up_run_id":101,"delivered_at":"2026-09-16T12:00:00Z","mode":"live"}`, string(got))
}
