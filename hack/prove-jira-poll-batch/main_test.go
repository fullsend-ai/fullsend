package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

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

// utf16Bytes is the size GitHub counts for a job output: it approximates
// output size from the UTF-16 encoding.
func utf16Bytes(s string) int {
	return 2 * len(utf16.Encode([]rune(s)))
}

// Every case is a batch the real driver stopped at one of its limits, with
// no room left for another record.
func TestRunFillsABatchToTheLimits(t *testing.T) {
	for name, want := range map[string]struct {
		records  int
		overOneM bool // larger than 1 MB in UTF-8
	}{
		"records": {records: config.MaxJiraPollMaxDispatchesPerCycle},
		"size":    {records: 30},
		"wide":    {records: 30, overOneM: true},
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "dispatches.json")
			var stdout, stderr strings.Builder
			require.Zero(t, run([]string{"-case", name, "-out", out}, &stdout, &stderr), stderr.String())

			m, n := matrix(t, out)
			assert.Equal(t, want.records, n)
			assert.Contains(t, stdout.String(), fmt.Sprintf("case %s: %d records in ", name, n), "run reports the records it wrote")

			size := utf16Bytes(m)
			assert.LessOrEqual(t, size, sizeLimit, "the driver keeps the batch within its size limit")
			// One more record of the average size would not fit, so the batch
			// is at the size limit as well as, for the records case, at the
			// record limit.
			assert.Greater(t, size+size/n, sizeLimit, "the batch has no room for another record")

			if want.overOneM {
				assert.Greater(t, len(m), 1<<20, "the wide batch is over 1 MB in UTF-8 while within the limit in UTF-16")
			} else {
				assert.Less(t, len(m), 1<<20)
			}
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
