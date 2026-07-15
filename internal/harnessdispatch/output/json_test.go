package output

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
)

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	refs := []harnessdispatch.ExecutionRef{{Agent: "issue-ping", Role: "triage"}}
	require.NoError(t, WriteJSON(&buf, refs))
	assert.Contains(t, buf.String(), "issue-ping")
}
