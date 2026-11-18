package forge

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeScheduleVariableDetails(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	fc.PipelineSchedules["o/r"] = []PipelineSchedule{{ID: 1, Variables: map[string]string{"MODE": "slash"}}}
	schedule, err := fc.GetPipelineSchedule(ctx, "o", "r", 1)
	require.NoError(t, err)
	schedule.Variables["MODE"] = "other"
	assert.Equal(t, "slash", fc.PipelineSchedules["o/r"][0].Variables["MODE"])
	_, err = fc.GetPipelineSchedule(ctx, "o", "r", 2)
	require.ErrorIs(t, err, ErrNotFound)
	fc.Errors["GetPipelineSchedule"] = errors.New("denied")
	_, err = fc.GetPipelineSchedule(ctx, "o", "r", 1)
	require.ErrorContains(t, err, "denied")
}

func TestFakeCreatePipelineScheduleCopiesVariables(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	in := map[string]string{"MODE": "slash"}
	id, err := fc.CreatePipelineSchedule(ctx, "o", "r", "main", "d", "* * * * *", in)
	require.NoError(t, err)
	stored, err := fc.GetPipelineSchedule(ctx, "o", "r", id)
	require.NoError(t, err)
	assert.Equal(t, "slash", stored.Variables["MODE"])
	assert.Equal(t, "slash", in["MODE"], "fake must not write into the caller's map")
	assert.Equal(t, "slash", fc.CreatedSchedules[0].Variables["MODE"], "CreatedSchedules must keep what was sent")
	in["MODE"] = "changed"
	assert.Equal(t, "slash", fc.CreatedSchedules[0].Variables["MODE"], "caller edits must not reach the fake")
}
