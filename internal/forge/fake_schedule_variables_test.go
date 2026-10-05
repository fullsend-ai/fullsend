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
	require.NoError(t, fc.DeletePipelineScheduleVariable(ctx, "o", "r", 1, "MODE"))
	assert.Empty(t, fc.PipelineSchedules["o/r"][0].Variables)
	_, err = fc.GetPipelineSchedule(ctx, "o", "r", 2)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, fc.DeletePipelineScheduleVariable(ctx, "o", "r", 2, "MODE"), ErrNotFound)
	fc.Errors["GetPipelineSchedule"] = errors.New("denied")
	_, err = fc.GetPipelineSchedule(ctx, "o", "r", 1)
	require.ErrorContains(t, err, "denied")
	fc.Errors["DeletePipelineScheduleVariable"] = errors.New("denied")
	require.ErrorContains(t, fc.DeletePipelineScheduleVariable(ctx, "o", "r", 1, "MODE"), "denied")
}
