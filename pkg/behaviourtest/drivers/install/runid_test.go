package install

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRunID_FormatAndUniqueness(t *testing.T) {
	seen := make(map[RunID]bool)
	for range 100 {
		id, err := NewRunID()
		require.NoError(t, err)
		assert.Len(t, id.String(), RunIDLen, "run id must be fixed width")
		for _, c := range id.String() {
			assert.Contains(t, "0123456789abcdef", string(c), "run id must be lowercase hex")
		}
		assert.False(t, seen[id], "run ids must be unique")
		seen[id] = true
	}
}

func TestNewRunID_TimeSortable(t *testing.T) {
	var ids []string
	for range 20 {
		id, err := NewRunID()
		require.NoError(t, err)
		ids = append(ids, id.String())
		time.Sleep(2 * time.Millisecond)
	}

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	assert.Equal(t, ids, sorted,
		"run ids generated in order must already be in lexicographic order (prune relies on this)")
}
