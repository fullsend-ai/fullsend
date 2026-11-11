package normentity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const examplesDir = "../../docs/normative/normalized-entity/v1/examples"

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(examplesDir, name))
	require.NoError(t, err)
	return raw
}

func validExamples(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(examplesDir, "valid-*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return names
}

// normalize decodes JSON into generic form and sorts the coverage row arrays,
// whose order the specification leaves open.
func normalize(t *testing.T, raw []byte) any {
	t.Helper()
	var v map[string]any
	require.NoError(t, json.Unmarshal(raw, &v))
	if cov, ok := v["coverage"].(map[string]any); ok {
		for _, rows := range cov {
			if arr, ok := rows.([]any); ok {
				sort.Slice(arr, func(i, j int) bool {
					a, _ := json.Marshal(arr[i])
					b, _ := json.Marshal(arr[j])
					return string(a) < string(b)
				})
			}
		}
	}
	return v
}

// TestDocumentRoundTripsSpecExamples decodes every valid spec example into
// the typed model and re-encodes it. The result must equal the original, so
// the model represents every unknown marker, null, absent member, collection
// coverage state, and extension the specification's own examples use.
func TestDocumentRoundTripsSpecExamples(t *testing.T) {
	for _, name := range validExamples(t) {
		t.Run(name, func(t *testing.T) {
			raw := readExample(t, name)
			var d Document
			require.NoError(t, json.Unmarshal(raw, &d))
			require.NoError(t, d.Validate())

			out, err := json.Marshal(d)
			require.NoError(t, err)
			assert.Equal(t, normalize(t, raw), normalize(t, out))
		})
	}
}

func TestDocumentTypedAccess(t *testing.T) {
	var d Document
	require.NoError(t, json.Unmarshal(readExample(t, "valid-partial-change-proposal.json"), &d))
	e := d.Entity

	assert.Equal(t, KindChangeProposal, e.Kind)
	assert.Equal(t, SystemGitHub, e.Source.System)
	cp, ok := e.ChangeProposal.Value()
	require.True(t, ok)

	// A partially loaded collection keeps its known members and coverage
	// together; the coverage is not a separate structure.
	var sawNonComplete bool
	for _, l := range []struct {
		known    int
		coverage Coverage
	}{
		{len(e.Comments.Known), e.Comments.Coverage},
		{len(cp.ChangedFiles.Known), cp.ChangedFiles.Coverage},
		{len(cp.Commits.Known), cp.Commits.Coverage},
	} {
		if l.coverage.Status != CoverageComplete {
			sawNonComplete = true
		}
	}
	assert.True(t, sawNonComplete, "example should exercise non-complete coverage")
}

func TestDocumentUnmarshalErrors(t *testing.T) {
	base := string(readExample(t, "valid-complete-empty.json"))

	for name, tc := range map[string]struct{ from, to string }{
		"wrong schema version":     {`"normalized-entity/v1"`, `"normalized-entity/v2"`},
		"bad unknown reason":       {`"state": "open"`, `"state": {"$unknown": "bogus"}`},
		"orphan collection entry":  {`"coverage": {`, `"coverage": {"reviews": {"status": "complete"},`},
		"orphan thread row":        {`"thread_comments": []`, `"thread_comments": [{"thread_id": "t1", "status": "complete"}]`},
		"orphan extension row":     {`"extension_lists": []`, `"extension_lists": [{"owner_kind": "entity", "owner_id": "x", "category": "properties", "alias": "a", "coverage": {"status": "complete"}}]`},
		"duplicate thread row":     {`"thread_comments": []`, `"thread_comments": [{"thread_id": "t1", "status": "complete"}, {"thread_id": "t1", "status": "partial"}]`},
		"orphan check rows":        {`"coverage": {`, `"coverage": {"check_logs": [{"check_id": "c", "ascending": {"status": "complete"}, "descending": {"status": "complete"}}], "check_annotations": [{"check_id": "c", "coverage": {"status": "complete"}}],`},
		"coverage without commits": {`"coverage": {`, `"coverage": {"commits": {"status": "complete"}, "changed_files": {"status": "complete"},`},
	} {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, base, tc.from, "fixture must contain the text it replaces")
			mutated := strings.Replace(base, tc.from, tc.to, 1)
			var d Document
			assert.Error(t, json.Unmarshal([]byte(mutated), &d))
		})
	}

	var d Document
	assert.Error(t, json.Unmarshal([]byte(`[]`), &d))
}

func TestValidate(t *testing.T) {
	var good Document
	require.NoError(t, json.Unmarshal(readExample(t, "valid-github-issue-source-details.json"), &good))
	require.NoError(t, good.Validate())

	for name, mutate := range map[string]func(*Document){
		"kind":         func(d *Document) { d.Entity.Kind = "pull" },
		"system":       func(d *Document) { d.Entity.Source.System = "svn" },
		"host case":    func(d *Document) { d.Entity.Source.Host = "GitHub.com" },
		"no source id": func(d *Document) { d.Entity.SourceID = "" },
		"id mismatch":  func(d *Document) { d.Entity.ID = "fullsend:entity:v1:github:github.com:work_item:other" },
	} {
		t.Run(name, func(t *testing.T) {
			d := good
			mutate(&d)
			assert.ErrorIs(t, d.Validate(), ErrInvalid)
		})
	}
}

// TestDocumentUnknownChangeProposalKeepsCoverage covers a schema-valid
// document whose change_proposal is an unknown marker: the specification still
// requires coverage.changed_files and coverage.commits, so the model must
// decode and re-encode them.
func TestDocumentUnknownChangeProposalKeepsCoverage(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal(readExample(t, "valid-partial-change-proposal.json"), &raw))
	raw["entity"].(map[string]any)["change_proposal"] = map[string]any{"$unknown": "not_loaded"}
	cov := raw["coverage"].(map[string]any)
	cov["changed_files"] = map[string]any{"status": "not_loaded"}
	cov["commits"] = map[string]any{"status": "partial", "reason": "later_commits_not_requested"}
	in, err := json.Marshal(raw)
	require.NoError(t, err)

	var d Document
	require.NoError(t, json.Unmarshal(in, &d))
	reason, ok := d.Entity.ChangeProposal.UnknownReason()
	require.True(t, ok)
	assert.Equal(t, ReasonNotLoaded, reason)
	assert.Equal(t, CoverageNotLoaded, d.Entity.UnresolvedChangeProposal.ChangedFiles.Status)
	assert.Equal(t, CoveragePartial, d.Entity.UnresolvedChangeProposal.Commits.Status)

	out, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Equal(t, normalize(t, in), normalize(t, out))

	var again Document
	require.NoError(t, json.Unmarshal(out, &again))
	assert.Equal(t, d, again)

	// Without a change proposal the entries describe nothing and are rejected.
	delete(raw["entity"].(map[string]any), "change_proposal")
	orphan, err := json.Marshal(raw)
	require.NoError(t, err)
	assert.Error(t, json.Unmarshal(orphan, &Document{}))
}
