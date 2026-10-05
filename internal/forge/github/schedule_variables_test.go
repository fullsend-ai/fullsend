package github

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/require"
)

func TestScheduleVariablesUnsupported(t *testing.T) {
	client := new(LiveClient)
	_, err := client.GetPipelineSchedule(context.Background(), "o", "r", 1)
	require.ErrorIs(t, err, forge.ErrNotSupported)
	require.ErrorIs(t, client.DeletePipelineScheduleVariable(context.Background(), "o", "r", 1, "MODE"), forge.ErrNotSupported)
}
