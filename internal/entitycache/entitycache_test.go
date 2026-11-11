package entitycache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/normentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustID(t *testing.T, sourceID string) normentity.ID {
	t.Helper()
	id, err := normentity.NewID("github", "github.com", "work_item", sourceID)
	require.NoError(t, err)
	return id
}

// doc returns a minimal valid document for the work item with the given
// source ID, carrying title.
func doc(sourceID, title string) *normentity.Document {
	id, err := normentity.NewID("github", "github.com", "work_item", sourceID)
	if err != nil {
		panic(err)
	}
	return &normentity.Document{Entity: normentity.Entity{
		Kind:     normentity.KindWorkItem,
		Source:   normentity.Source{System: normentity.SystemGitHub, Host: "github.com"},
		ID:       id,
		SourceID: sourceID,
		Key:      "#1",
		Number:   1,
		URL:      "https://github.com/o/r/issues/1",
		Title:    normentity.Known(title),
	}}
}

func titleOf(t *testing.T, d *normentity.Document) string {
	t.Helper()
	require.NotNil(t, d)
	title, ok := d.Entity.Title.Value()
	require.True(t, ok)
	return title
}

func TestGetPut_MissThenCreate(t *testing.T) {
	m := NewMemory()
	id := mustID(t, "I_1")

	got, r0, ok := m.Get(id)
	assert.False(t, ok)
	assert.Nil(t, got)

	r1, err := m.Put(id, doc("I_1", "first"), r0)
	require.NoError(t, err)
	assert.Greater(t, r1, r0)

	got, rev, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, r1, rev)
	assert.Equal(t, "first", titleOf(t, got))
}

func TestPut_StaleRevisionConflicts(t *testing.T) {
	m := NewMemory()
	id := mustID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1, err := m.Put(id, doc("I_1", "first"), r0)
	require.NoError(t, err)

	cur, err := m.Put(id, doc("I_1", "stale"), r0)
	require.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, r1, cur)

	got, rev, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, r1, rev)
	assert.Equal(t, "first", titleOf(t, got), "conflicting Put must not change the value")

	r2, err := m.Put(id, doc("I_1", "second"), r1)
	require.NoError(t, err)
	assert.Greater(t, r2, r1)
}

func TestInvalidate_RejectsPreInvalidationSnapshots(t *testing.T) {
	m := NewMemory()
	id := mustID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1, err := m.Put(id, doc("I_1", "first"), r0)
	require.NoError(t, err)

	r2, err := m.Invalidate(id)
	require.NoError(t, err)
	assert.Greater(t, r2, r1)

	got, rev, ok := m.Get(id)
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Equal(t, r2, rev)

	for _, stale := range []Revision{r0, r1} {
		cur, err := m.Put(id, doc("I_1", "stale"), stale)
		require.ErrorIs(t, err, ErrConflict, "revision %d", stale)
		assert.Equal(t, r2, cur)
	}
	_, _, ok = m.Get(id)
	assert.False(t, ok, "stale data must not be restored")

	r3, err := m.Put(id, doc("I_1", "fresh"), r2)
	require.NoError(t, err)
	assert.Greater(t, r3, r2)
}

func TestInvalidate_AbsentKeyAdvancesRevision(t *testing.T) {
	m := NewMemory()
	id := mustID(t, "I_1")
	_, r0, ok := m.Get(id)
	require.False(t, ok)

	r1, err := m.Invalidate(id)
	require.NoError(t, err)
	assert.Greater(t, r1, r0)

	_, rev, ok := m.Get(id)
	assert.False(t, ok)
	assert.Equal(t, r1, rev)

	_, err = m.Put(id, doc("I_1", "stale"), r0)
	require.ErrorIs(t, err, ErrConflict)
}

func TestInvalidate_DoesNotAffectOtherKeys(t *testing.T) {
	m := NewMemory()
	a, b := mustID(t, "I_a"), mustID(t, "I_b")
	_, ra, _ := m.Get(a)
	ra1, err := m.Put(a, doc("I_a", "a"), ra)
	require.NoError(t, err)
	_, rb, _ := m.Get(b)

	_, err = m.Invalidate(b)
	require.NoError(t, err)

	got, rev, ok := m.Get(a)
	require.True(t, ok)
	assert.Equal(t, ra1, rev)
	assert.Equal(t, "a", titleOf(t, got))

	_, err = m.Put(b, doc("I_b", "b"), rb)
	require.ErrorIs(t, err, ErrConflict)
}

func TestTombstoneLimit_BoundsMemoryAndKeepsSnapshotsStale(t *testing.T) {
	m := NewMemory()
	m.maxTombstones = 3

	present := mustID(t, "I_present")
	_, r, _ := m.Get(present)
	rPresent, err := m.Put(present, doc("I_present", "kept"), r)
	require.NoError(t, err)

	// A miss-time snapshot of an entity that is never invalidated.
	untouched := mustID(t, "I_untouched")
	_, rUntouched, _ := m.Get(untouched)

	srcs := []string{"I_0", "I_1", "I_2"}
	before := make([]Revision, len(srcs))
	for i, src := range srcs {
		id := mustID(t, src)
		_, before[i], _ = m.Get(id)
		_, err := m.Invalidate(id)
		require.NoError(t, err)
	}
	assert.Empty(t, m.tombstones, "reaching the limit collapses tombstones")

	for i, src := range srcs {
		id := mustID(t, src)
		_, rev, ok := m.Get(id)
		assert.False(t, ok)
		assert.Greater(t, rev, before[i])
		_, err := m.Put(id, doc(src, "stale"), before[i])
		require.ErrorIs(t, err, ErrConflict)
	}
	// The last invalidation reports the post-collapse revision it caused.
	_, rev, _ := m.Get(mustID(t, srcs[2]))
	assert.Equal(t, m.absent, rev)

	// Present entries are unaffected.
	got, rev, ok := m.Get(present)
	require.True(t, ok)
	assert.Equal(t, rPresent, rev)
	assert.Equal(t, "kept", titleOf(t, got))

	// Unrelated absent entities conflict conservatively and can retry.
	_, err = m.Put(untouched, doc("I_untouched", "x"), rUntouched)
	require.ErrorIs(t, err, ErrConflict)
	_, rRetry, _ := m.Get(untouched)
	_, err = m.Put(untouched, doc("I_untouched", "x"), rRetry)
	require.NoError(t, err)
}

func TestInvalidate_ReturnsCollapsedRevision(t *testing.T) {
	m := NewMemory()
	m.maxTombstones = 1
	id := mustID(t, "I_1")
	rev, err := m.Invalidate(id)
	require.NoError(t, err)
	_, cur, _ := m.Get(id)
	assert.Equal(t, cur, rev)
	_, err = m.Put(id, doc("I_1", "x"), rev)
	require.NoError(t, err)
}

func TestDocumentsAreCopied(t *testing.T) {
	m := NewMemory()
	id := mustID(t, "I_1")
	in := doc("I_1", "aaaa")
	in.Entity.Labels = normentity.List[string]{
		Known:    []string{"bug"},
		Coverage: normentity.Coverage{Status: normentity.CoveragePartial},
	}
	_, r0, _ := m.Get(id)
	_, err := m.Put(id, in, r0)
	require.NoError(t, err)

	// Mutating the caller's input after Put must not change cached state.
	in.Entity.Title = normentity.Known("changed")
	in.Entity.Labels.Known[0] = "changed"
	got, _, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, "aaaa", titleOf(t, got))
	assert.Equal(t, []string{"bug"}, got.Entity.Labels.Known)

	// Mutating a returned snapshot must not change cached state either.
	got.Entity.Title = normentity.Known("changed")
	got.Entity.Labels.Known[0] = "changed"
	again, _, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, "aaaa", titleOf(t, again))
	assert.Equal(t, []string{"bug"}, again.Entity.Labels.Known)
}

func TestPartialDataRoundTripsUnchanged(t *testing.T) {
	m := NewMemory()
	// Real spec examples carrying not_loaded, partial, and unsupported
	// markers, nulls, and every collection coverage state.
	for _, name := range []string{
		"valid-partial-change-proposal.json",
		"valid-partial-known-action.json",
		"valid-nested-unknown.json",
		"valid-gitlab-merge-request.json",
		"valid-jira-extensions.json",
		"valid-github-discussion.json",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative",
				"normalized-entity", "v1", "examples", name))
			require.NoError(t, err)
			var in normentity.Document
			require.NoError(t, json.Unmarshal(raw, &in))

			id := in.Entity.ID
			_, rev, _ := m.Get(id)
			_, err = m.Put(id, &in, rev)
			require.NoError(t, err)
			got, _, ok := m.Get(id)
			require.True(t, ok)
			assert.Equal(t, &in, got, "cache must return the document exactly as stored")
		})
	}
}

func TestMemoryDocumentWithMarkers(t *testing.T) {
	// Every marker reason, a null, and each non-complete coverage state
	// survive as distinct values.
	m := NewMemory()
	id := mustID(t, "I_markers")
	in := doc("I_markers", "")
	e := &in.Entity
	e.Title = normentity.Unknown[string](normentity.ReasonNotLoaded)
	e.Body = normentity.Null[normentity.TextAction]()
	e.UpdatedAt = normentity.Unknown[normentity.Timestamp](normentity.ReasonConsistencyUnknown)
	e.WorkItem = normentity.Known(normentity.WorkItem{
		StateReason:  normentity.Unknown[string](normentity.ReasonUnsupported),
		SourceStatus: normentity.Unknown[normentity.SourceStatus](normentity.ReasonFailed),
		IssueType:    normentity.Unknown[normentity.IssueType](normentity.ReasonPartial),
	})
	// A serialized empty collection decodes as a non-nil empty slice.
	e.Comments = normentity.List[normentity.Comment]{Known: []normentity.Comment{}, Coverage: normentity.Coverage{Status: normentity.CoverageNotLoaded}}
	e.Assignees = normentity.List[normentity.Actor]{Known: []normentity.Actor{}, Coverage: normentity.Coverage{Status: normentity.CoverageUnsupported}}
	e.Labels = normentity.List[string]{Known: []string{}, Coverage: normentity.Coverage{Status: normentity.CoverageUnknown}}
	e.Threads = normentity.List[normentity.Thread]{Known: []normentity.Thread{}, Coverage: normentity.Coverage{Status: normentity.CoverageFailed, Reason: "denied"}}

	_, rev, _ := m.Get(id)
	_, err := m.Put(id, in, rev)
	require.NoError(t, err)
	got, _, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, in, got)
}

func TestInvalidInput(t *testing.T) {
	m := NewMemory()
	bad := normentity.ID("fullsend:entity:v1:github:GitHub.com:work_item:I_1")

	got, rev, ok := m.Get(bad)
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Zero(t, rev)

	_, err := m.Put(bad, doc("I_1", "x"), 0)
	require.ErrorIs(t, err, ErrInvalidID)

	_, err = m.Invalidate(bad)
	require.ErrorIs(t, err, ErrInvalidID)

	id := mustID(t, "I_1")
	notDerived := doc("I_1", "x")
	notDerived.Entity.Source.Host = "gitlab.com"
	badUnknown := doc("I_1", "x")
	badUnknown.Entity.Title = normentity.Unknown[string]("bogus") // not a specified reason

	for name, d := range map[string]*normentity.Document{
		"nil":                nil,
		"mismatched id":      doc("I_2", "x"),
		"id not derived":     notDerived,
		"unencodable marker": badUnknown,
	} {
		_, err := m.Put(id, d, 0)
		assert.ErrorIs(t, err, ErrInvalidEntity, name)
	}
	_, rev, ok = m.Get(id)
	assert.False(t, ok)
	assert.Zero(t, rev, "rejected input must not advance the revision")
}

func TestGet_UndecodableRecordIsAMiss(t *testing.T) {
	m := NewMemory()
	id := mustID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1, err := m.Put(id, doc("I_1", "x"), r0)
	require.NoError(t, err)

	m.mu.Lock()
	m.records[id] = record{doc: []byte("{"), rev: r1}
	m.mu.Unlock()

	got, rev, ok := m.Get(id)
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Equal(t, r1, rev, "the caller can replace the record at its current revision")
	_, err = m.Put(id, doc("I_1", "y"), rev)
	require.NoError(t, err)
}
