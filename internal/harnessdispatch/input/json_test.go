package input_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestLoadJSONEvent_FromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	data := []byte(`{
  "repo": "o/r",
  "entity": {"kind": "work_item", "id": 1, "url": "https://github.com/o/r/issues/1"},
  "transition": {"kind": "opened"},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": true},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "issues"}
}`)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	ev, err := input.LoadJSONEvent(path)
	require.NoError(t, err)
	assert.Equal(t, normevent.EntityWorkItem, ev.Entity.Kind)
}

func TestLoadJSONEvent_MissingFile(t *testing.T) {
	_, err := input.LoadJSONEvent(filepath.Join(t.TempDir(), "missing.json"))
	require.Error(t, err)
}
