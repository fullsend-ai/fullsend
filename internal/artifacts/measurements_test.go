package artifacts

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripMeasurements_Rows(t *testing.T) {
	skip := `{"name":"health","label":"skip","explanation":"pre-script skipped run; excluded from run_health: PROJ-1 <private>","trace_id":"01","span_id":"02","agent":"code","version":"em-002@1","value":0}`
	pass := `{"name":"trace_fitness","label":"pass","explanation":"a <b> & c","trace_id":"01","span_id":"02","work_item_id":"7","agent":"code","version":"em-001@1","value":0.75,"extra":{"n":9007199254740993}}`
	noExpl := `{"name":"x","label":"skip","trace_id":"01","span_id":"02","agent":"code","version":"v@1","value":0}`

	out, err := StripMeasurements([]byte(skip + "\n\n" + pass + "\n" + noExpl))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 3, "one row per line; blank lines dropped; final newline added")

	var gotSkip map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &gotSkip))
	assert.Equal(t, RemovedSkipExplanation, gotSkip["explanation"])
	assert.NotContains(t, lines[0], "PROJ-1")
	wantSkip := strings.Replace(skip, `"pre-script skipped run; excluded from run_health: PROJ-1 <private>"`, `"`+RemovedSkipExplanation+`"`, 1)
	assert.JSONEq(t, wantSkip, lines[0], "other skip-row fields are kept")

	assert.JSONEq(t, pass, lines[1], "a non-skip row keeps every field and value")
	assert.Contains(t, lines[1], `"a <b> & c"`, "HTML characters are not escaped")
	assert.Contains(t, lines[1], "9007199254740993", "numbers stay exact")

	assert.JSONEq(t, noExpl, lines[2], "a skip row without an explanation gets none")
}

func TestStripMeasurements_Errors(t *testing.T) {
	for name, in := range map[string]string{
		"malformed line": `{"label":"pass"}` + "\n{not json\n",
		"trailing data":  `{"label":"pass"} {"label":"skip"}` + "\n",
		"not an object":  `[1]` + "\n",
		"null":           "null\n",
		"no label":       `{"explanation":"x"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := StripMeasurements([]byte(in))
			assert.Error(t, err)
		})
	}
}

func TestStripMeasurements_PrepareLeavesFileOutOnError(t *testing.T) {
	out := t.TempDir()
	artifactDir := filepath.Join(t.TempDir(), "artifact")
	writeTree(t, out, map[string]string{
		testRunDir + "/metrics.json":            `{}`,
		testRunDir + "/eval-measurements.jsonl": testMeasurements + `{"label":"pass"} trailing` + "\n",
	})
	require.NoError(t, Prepare(out, artifactDir, "metadata"))
	assert.Equal(t, []string{testRunDir + "/metrics.json"}, listTree(t, artifactDir))
}

// Every filter targets an allowlisted file, and every allowlisted file is
// either filtered or explicitly explained as safe to copy as written.
func TestMetadataFilesAllFilteredOrExplained(t *testing.T) {
	allow := map[string]bool{}
	for _, name := range MetadataFiles() {
		allow[name] = true
		_, filtered := metadataFilters[name]
		reason, explained := unfilteredMetadataFiles[name]
		assert.True(t, filtered != explained, "%s must be filtered or explained, not both or neither", name)
		if explained {
			assert.NotEmpty(t, reason, name)
		}
	}
	for name := range metadataFilters {
		assert.True(t, allow[name], "filter for %s, which is not a metadata file", name)
	}
	for name := range unfilteredMetadataFiles {
		assert.True(t, allow[name], "%s is explained but not a metadata file", name)
	}
}
