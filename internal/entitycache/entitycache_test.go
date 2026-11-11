package entitycache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustEntityID(t *testing.T, sourceID string) EntityID {
	t.Helper()
	id, err := NewEntityID("github", "github.com", "work_item", sourceID)
	require.NoError(t, err)
	return id
}

// doc returns a minimal entity document for id carrying the given title value.
func doc(id EntityID, title string) Entity {
	return Entity(`{"schema_version":"normalized-entity/v1","entity":{"id":"` +
		string(id) + `","title":` + title + `},"coverage":{},"view":{}}`)
}

func TestNewEntityID(t *testing.T) {
	id, err := NewEntityID("github", "GitHub.com", "work_item", "I_kwDOExample53")
	require.NoError(t, err)
	assert.Equal(t, EntityID("fullsend:entity:v1:github:github.com:work_item:I_kwDOExample53"), id)
	assert.True(t, id.Valid())

	id, err = NewEntityID("gitlab", "gitlab.example.com:8443", "change_proposal", "a:b/c é~")
	require.NoError(t, err)
	assert.Equal(t, EntityID("fullsend:entity:v1:gitlab:gitlab.example.com%3A8443:change_proposal:a%3Ab%2Fc%20%C3%A9~"), id)
	assert.True(t, id.Valid())

	for _, tc := range []struct{ system, host, kind, source string }{
		{"", "github.com", "work_item", "1"},
		{"github", "", "work_item", "1"},
		{"github", "github.com", "", "1"},
		{"github", "github.com", "work_item", ""},
	} {
		_, err := NewEntityID(tc.system, tc.host, tc.kind, tc.source)
		assert.Error(t, err, "%+v", tc)
	}
}

func TestEntityIDValid(t *testing.T) {
	for _, id := range []EntityID{
		"",
		"#123",
		"fullsend:actor:v1:github:github.com:12345",
		"fullsend:entity:v1:github:github.com:work_item",
		"fullsend:entity:v1:github::work_item:I_1",
		"fullsend:entity:v1:github:github.com:work_item:I_1:2",
		// Non-canonical spellings of otherwise well-formed IDs.
		"fullsend:entity:v1:github:GitHub.com:work_item:I_1",
		"fullsend:entity:v1:github:%67ithub.com:work_item:I_1",
		"fullsend:entity:v1:github:github.com:work_item:a/b",
		"fullsend:entity:v1:github:github.com:work_item:a b",
		"fullsend:entity:v1:github:github.com:work_item:a%2fb",
		"fullsend:entity:v1:github:github.com:work_item:a%2",
		"fullsend:entity:v1:github:github.com:work_item:a%ZZb",
	} {
		assert.False(t, id.Valid(), "%q", id)
	}
	assert.True(t, EntityID("fullsend:entity:v1:github:github.com:work_item:a%2Fb").Valid())
}

func TestGetPut_MissThenCreate(t *testing.T) {
	m := NewMemory()
	id := mustEntityID(t, "I_1")

	got, r0, ok := m.Get(id)
	assert.False(t, ok)
	assert.Nil(t, got)

	r1, err := m.Put(id, doc(id, `"first"`), r0)
	require.NoError(t, err)
	assert.Greater(t, r1, r0)

	got, rev, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, r1, rev)
	assert.JSONEq(t, string(doc(id, `"first"`)), string(got))
}

func TestPut_StaleRevisionConflicts(t *testing.T) {
	m := NewMemory()
	id := mustEntityID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1, err := m.Put(id, doc(id, `"first"`), r0)
	require.NoError(t, err)

	cur, err := m.Put(id, doc(id, `"stale"`), r0)
	require.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, r1, cur)

	got, rev, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, r1, rev)
	assert.Equal(t, string(doc(id, `"first"`)), string(got), "conflicting Put must not change the value")

	r2, err := m.Put(id, doc(id, `"second"`), r1)
	require.NoError(t, err)
	assert.Greater(t, r2, r1)
}

func TestInvalidate_RejectsPreInvalidationSnapshots(t *testing.T) {
	m := NewMemory()
	id := mustEntityID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1, err := m.Put(id, doc(id, `"first"`), r0)
	require.NoError(t, err)

	r2, err := m.Invalidate(id)
	require.NoError(t, err)
	assert.Greater(t, r2, r1)

	got, rev, ok := m.Get(id)
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Equal(t, r2, rev)

	for _, stale := range []Revision{r0, r1} {
		cur, err := m.Put(id, doc(id, `"stale"`), stale)
		require.ErrorIs(t, err, ErrConflict, "revision %d", stale)
		assert.Equal(t, r2, cur)
	}
	_, _, ok = m.Get(id)
	assert.False(t, ok, "stale data must not be restored")

	r3, err := m.Put(id, doc(id, `"fresh"`), r2)
	require.NoError(t, err)
	assert.Greater(t, r3, r2)
}

func TestInvalidate_AbsentKeyAdvancesRevision(t *testing.T) {
	m := NewMemory()
	id := mustEntityID(t, "I_1")
	_, r0, ok := m.Get(id)
	require.False(t, ok)

	r1, err := m.Invalidate(id)
	require.NoError(t, err)
	assert.Greater(t, r1, r0)

	_, rev, ok := m.Get(id)
	assert.False(t, ok)
	assert.Equal(t, r1, rev)

	_, err = m.Put(id, doc(id, `"stale"`), r0)
	require.ErrorIs(t, err, ErrConflict)
}

func TestInvalidate_DoesNotAffectOtherKeys(t *testing.T) {
	m := NewMemory()
	a, b := mustEntityID(t, "I_a"), mustEntityID(t, "I_b")
	_, ra, _ := m.Get(a)
	ra1, err := m.Put(a, doc(a, `"a"`), ra)
	require.NoError(t, err)
	_, rb, _ := m.Get(b)

	_, err = m.Invalidate(b)
	require.NoError(t, err)

	got, rev, ok := m.Get(a)
	require.True(t, ok)
	assert.Equal(t, ra1, rev)
	assert.Equal(t, string(doc(a, `"a"`)), string(got))

	_, err = m.Put(b, doc(b, `"b"`), rb)
	require.ErrorIs(t, err, ErrConflict)
}

func TestTombstoneLimit_BoundsMemoryAndKeepsSnapshotsStale(t *testing.T) {
	m := NewMemory()
	m.maxTombstones = 3

	present := mustEntityID(t, "I_present")
	_, r, _ := m.Get(present)
	rPresent, err := m.Put(present, doc(present, `"kept"`), r)
	require.NoError(t, err)

	// A miss-time snapshot of an entity that is never invalidated.
	untouched := mustEntityID(t, "I_untouched")
	_, rUntouched, _ := m.Get(untouched)

	ids := []EntityID{mustEntityID(t, "I_0"), mustEntityID(t, "I_1"), mustEntityID(t, "I_2")}
	before := make([]Revision, len(ids))
	for i, id := range ids {
		_, before[i], _ = m.Get(id)
		_, err := m.Invalidate(id)
		require.NoError(t, err)
	}
	assert.Empty(t, m.tombstones, "reaching the limit collapses tombstones")

	for i, id := range ids {
		_, rev, ok := m.Get(id)
		assert.False(t, ok)
		assert.Greater(t, rev, before[i])
		_, err := m.Put(id, doc(id, `"stale"`), before[i])
		require.ErrorIs(t, err, ErrConflict)
	}
	// The last invalidation reports the post-collapse revision it caused.
	_, rev, _ := m.Get(ids[2])
	assert.Equal(t, m.absent, rev)

	// Present entries are unaffected.
	got, rev, ok := m.Get(present)
	require.True(t, ok)
	assert.Equal(t, rPresent, rev)
	assert.Equal(t, string(doc(present, `"kept"`)), string(got))

	// Unrelated absent entities conflict conservatively and can retry.
	_, err = m.Put(untouched, doc(untouched, `"x"`), rUntouched)
	require.ErrorIs(t, err, ErrConflict)
	_, rRetry, _ := m.Get(untouched)
	_, err = m.Put(untouched, doc(untouched, `"x"`), rRetry)
	require.NoError(t, err)
}

func TestInvalidate_ReturnsCollapsedRevision(t *testing.T) {
	m := NewMemory()
	m.maxTombstones = 1
	id := mustEntityID(t, "I_1")
	rev, err := m.Invalidate(id)
	require.NoError(t, err)
	_, cur, _ := m.Get(id)
	assert.Equal(t, cur, rev)
	_, err = m.Put(id, doc(id, `"x"`), rev)
	require.NoError(t, err)
}

func TestEntitiesAreCopied(t *testing.T) {
	m := NewMemory()
	id := mustEntityID(t, "I_1")
	in := doc(id, `"aaaa"`)
	want := string(in)
	_, r0, _ := m.Get(id)
	_, err := m.Put(id, in, r0)
	require.NoError(t, err)

	// Mutating the caller's input after Put must not change cached state.
	for i := range in {
		in[i] = ' '
	}
	got, _, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, want, string(got))

	// Mutating a returned snapshot must not change cached state either.
	for i := range got {
		got[i] = ' '
	}
	again, _, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, want, string(again))
}

func TestPartialDataMarkersRoundTripUnchanged(t *testing.T) {
	m := NewMemory()
	// Real spec examples carrying not_loaded, partial, and unsupported
	// markers, nulls, and not_loaded or unsupported collection coverage.
	for _, name := range []string{
		"valid-partial-change-proposal.json",
		"valid-partial-known-action.json",
		"valid-nested-unknown.json",
		"valid-gitlab-merge-request.json",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative",
				"normalized-entity", "v1", "examples", name))
			require.NoError(t, err)
			var head struct {
				Entity struct {
					ID string `json:"id"`
				} `json:"entity"`
			}
			require.NoError(t, json.Unmarshal(raw, &head))
			id := EntityID(head.Entity.ID)
			require.True(t, id.Valid(), "%q", id)

			_, rev, _ := m.Get(id)
			_, err = m.Put(id, raw, rev)
			require.NoError(t, err)
			got, _, ok := m.Get(id)
			require.True(t, ok)
			assert.Equal(t, string(raw), string(got), "cache must store the document byte-for-byte")
		})
	}

	// Every marker reason, a null, and each non-complete coverage state
	// survive as distinct values.
	id := mustEntityID(t, "I_markers")
	markers := Entity(`{"entity":{"id":"` + string(id) + `",` +
		`"title":{"$unknown":"not_loaded"},"body":null,` +
		`"state_reason":{"$unknown":"unsupported"},"source_status":{"$unknown":"failed"},` +
		`"issue_type":{"$unknown":"partial"},"updated_at":{"$unknown":"consistency_unknown"}},` +
		`"coverage":{"comments":"not_loaded","assignees":"unsupported","reviews":"failed","threads":"unknown"}}`)
	_, rev, _ := m.Get(id)
	_, err := m.Put(id, markers, rev)
	require.NoError(t, err)
	got, _, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, string(markers), string(got))
}

func TestInvalidInput(t *testing.T) {
	m := NewMemory()
	bad := EntityID("fullsend:entity:v1:github:GitHub.com:work_item:I_1")

	got, rev, ok := m.Get(bad)
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Zero(t, rev)

	_, err := m.Put(bad, doc(bad, `"x"`), 0)
	require.ErrorIs(t, err, ErrInvalidID)

	_, err = m.Invalidate(bad)
	require.ErrorIs(t, err, ErrInvalidID)

	id := mustEntityID(t, "I_1")
	other := mustEntityID(t, "I_2")
	for name, e := range map[string]Entity{
		"nil":            nil,
		"empty":          Entity(""),
		"not json":       Entity("{"),
		"null":           Entity("null"),
		"array":          Entity("[]"),
		"no entity":      Entity(`{"coverage":{}}`),
		"entity null":    Entity(`{"entity":null}`),
		"entity scalar":  Entity(`{"entity":"x"}`),
		"missing id":     Entity(`{"entity":{}}`),
		"non-string id":  Entity(`{"entity":{"id":7}}`),
		"case-folded id": Entity(`{"entity":{"ID":"` + string(id) + `"}}`),
		"mismatched id":  doc(other, `"x"`),
	} {
		_, err := m.Put(id, e, 0)
		assert.ErrorIs(t, err, ErrInvalidEntity, name)
	}
	_, rev, ok = m.Get(id)
	assert.False(t, ok)
	assert.Zero(t, rev, "rejected input must not advance the revision")
}
