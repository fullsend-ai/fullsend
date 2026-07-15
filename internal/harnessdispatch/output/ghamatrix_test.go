package output

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
)

func TestWriteGHAMatrix_Empty(t *testing.T) {
	data, err := WriteGHAMatrix(nil)
	require.NoError(t, err)
	var m GHAMatrix
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Empty(t, m.Include)
}

func TestWriteGHAMatrix_Refs(t *testing.T) {
	refs := []harnessdispatch.ExecutionRef{{Agent: "issue-ping", Role: "triage"}}
	data, err := WriteGHAMatrix(refs)
	require.NoError(t, err)
	assert.Contains(t, string(data), "issue-ping")
}
