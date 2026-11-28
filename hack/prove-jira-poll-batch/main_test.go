package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// sizeLimit is the driver's limit on the matrix output of one cycle on the
// GitHub target: three quarters of GitHub's 1 MB limit on a job's outputs.
const sizeLimit = 768 * 1024

// matrix returns the poll job's matrix output for the dispatch file at path,
// as the managed workflow builds it with jq -c '{include: .}', and the number
// of records in it.
func matrix(t *testing.T, path string) (string, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var records []json.RawMessage
	require.NoError(t, json.Unmarshal(data, &records))
	out, err := json.Marshal(map[string][]json.RawMessage{"include": records})
	require.NoError(t, err)
	return string(out), len(records)
}

// counted is the size GitHub counts for a job output: two bytes for each
// byte of its UTF-8 text.
func counted(s string) int {
	return 2 * len(s)
}

// Every case is a batch the real driver stopped at one of its limits, with
// no room left for another record.
func TestRunFillsABatchToTheLimits(t *testing.T) {
	for name, wantRecords := range map[string]int{
		"records": config.MaxJiraPollMaxDispatchesPerCycle,
		"size":    30,
		// A three-byte character counts three times an ASCII one, so a
		// third as many of the largest records fit.
		"wide": 10,
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "dispatches.json")
			var stdout, stderr strings.Builder
			require.Zero(t, run([]string{"-case", name, "-out", out}, &stdout, &stderr), stderr.String())

			m, n := matrix(t, out)
			assert.Equal(t, wantRecords, n)
			assert.Contains(t, stdout.String(), fmt.Sprintf("case %s: %d records in ", name, n), "run reports the records it wrote")

			size := counted(m)
			assert.LessOrEqual(t, size, sizeLimit, "the driver keeps the batch within its size limit")
			// One more record of the average size would not fit, so the batch
			// is at the size limit as well as, for the records case, at the
			// record limit.
			assert.Greater(t, size+size/n, sizeLimit, "the batch has no room for another record")
		})
	}
}

func TestRunRejectsAnUnknownCaseOrFlag(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dispatches.json")

	var stderr strings.Builder
	assert.Equal(t, 1, run([]string{"-case", "huge", "-out", out}, io.Discard, &stderr))
	assert.Contains(t, stderr.String(), `unknown case "huge": want one of records, size, wide`)
	assert.NoFileExists(t, out)

	stderr.Reset()
	assert.Equal(t, 2, run([]string{"-cases", "size"}, io.Discard, &stderr))
	assert.Contains(t, stderr.String(), "flag provided but not defined: -cases")
}
