package resolve

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// reproSkills registers skill B (with sub-agents/x.md) and skill A, whose
// SKILL.md declares B as a transitive dependency. Returns the registry and
// the pinned URLs of A and B.
func reproSkills() (*skillRegistry, string, string) {
	reg := newSkillRegistry()
	bHash := reg.register("skills/b", map[string][]byte{
		"SKILL.md":        []byte("# Skill B"),
		"sub-agents/x.md": []byte("x"),
	})
	aHash := reg.register("skills/a", map[string][]byte{
		"SKILL.md": skillFrontmatter(fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash), "# Skill A"),
	})
	return reg, forgeSkillURL("skills/a", aHash), forgeSkillURL("skills/b", bHash)
}

// skillB returns the resolved h.Skills entry whose directory is named "b".
func skillB(t *testing.T, h *harness.Harness) harness.SkillEntry {
	t.Helper()
	var found []harness.SkillEntry
	for _, s := range h.Skills {
		if filepath.Base(s.Source) == "b" {
			found = append(found, s)
		}
	}
	require.Len(t, found, 1, "expected exactly one resolved entry for skill b; skills=%+v", h.Skills)
	return found[0]
}

func TestDirectOverridesSurviveTransitiveDup(t *testing.T) {
	for name, aFirst := range map[string]bool{"A_then_B": true, "B_then_A": false} {
		t.Run(name, func(t *testing.T) {
			reg, aURL, bURL := reproSkills()
			bEntry := harness.SkillEntry{Source: bURL, Overrides: map[string]*string{"sub-agents/x.md": nil}}
			aEntry := harness.SkillEntry{Source: aURL}
			skills := []harness.SkillEntry{bEntry, aEntry}
			if aFirst {
				skills = []harness.SkillEntry{aEntry, bEntry}
			}
			h := &harness.Harness{Skills: skills, AllowedRemoteResources: []string{testForgeBase}}

			_, err := ResolveHarness(context.Background(), h, ResolveOpts{
				WorkspaceRoot: t.TempDir(),
				TreeFetcher:   reg.fetcher(),
				MaxDepth:      -1,
			})
			require.NoError(t, err)
			require.Len(t, h.Skills, 2)

			b := skillB(t, h)
			v, ok := b.Overrides["sub-agents/x.md"]
			assert.True(t, ok, "override sub-agents/x.md: null was dropped from skill b; got overrides=%v", b.Overrides)
			assert.Nil(t, v)
		})
	}
}

// TestDirectURLOverrideSurvivesTransitiveDup: a URL-valued override on the
// direct entry is fetched and attached to the kept entry for skill b,
// whatever the list order.
func TestDirectURLOverrideSurvivesTransitiveDup(t *testing.T) {
	content := []byte("custom x")
	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(content)
	}))
	overrideURL := srv.URL + "/overrides/x.md#sha256=" + fetch.ComputeSHA256(content)

	for name, aFirst := range map[string]bool{"A_then_B": true, "B_then_A": false} {
		t.Run(name, func(t *testing.T) {
			reg, aURL, bURL := reproSkills()
			val := overrideURL
			bEntry := harness.SkillEntry{Source: bURL, Overrides: map[string]*string{"sub-agents/x.md": &val}}
			aEntry := harness.SkillEntry{Source: aURL}
			skills, bIdx := []harness.SkillEntry{bEntry, aEntry}, 0
			if aFirst {
				skills, bIdx = []harness.SkillEntry{aEntry, bEntry}, 1
			}
			h := &harness.Harness{Skills: skills, AllowedRemoteResources: []string{testForgeBase, srv.URL + "/"}}

			result, err := ResolveHarness(context.Background(), h, ResolveOpts{
				WorkspaceRoot: t.TempDir(),
				TreeFetcher:   reg.fetcher(),
				FetchPolicy:   policy,
				MaxDepth:      -1,
			})
			require.NoError(t, err)
			require.Len(t, h.Skills, 2)

			b := skillB(t, h)
			v := b.Overrides["sub-agents/x.md"]
			require.NotNil(t, v, "URL override was dropped from skill b; got overrides=%v", b.Overrides)
			assert.True(t, strings.HasSuffix(*v, "/content"), "override not resolved to a cache path: %s", *v)

			var fields []string
			for _, d := range result.Deps {
				fields = append(fields, d.Field)
			}
			assert.Contains(t, fields, fmt.Sprintf("skills[%d].overrides[sub-agents/x.md]", bIdx))
		})
	}
}

// TestDuplicateDirectOverrides: two direct entries for skill b (with a
// transitive copy from a) merge overrides for different keys, agree when they
// set the same key to the same value, and fail when the values differ.
func TestDuplicateDirectOverrides(t *testing.T) {
	content := []byte("custom x")
	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(content)
	}))
	overrideURL := srv.URL + "/overrides/x.md#sha256=" + fetch.ComputeSHA256(content)

	run := func(t *testing.T, first, second map[string]*string) (*harness.Harness, error) {
		t.Helper()
		reg, aURL, bURL := reproSkills()
		h := &harness.Harness{
			Skills: []harness.SkillEntry{
				{Source: aURL},
				{Source: bURL, Overrides: first},
				{Source: bURL, Overrides: second},
			},
			AllowedRemoteResources: []string{testForgeBase, srv.URL + "/"},
		}
		_, err := ResolveHarness(context.Background(), h, ResolveOpts{
			WorkspaceRoot: t.TempDir(),
			TreeFetcher:   reg.fetcher(),
			FetchPolicy:   policy,
			MaxDepth:      -1,
		})
		return h, err
	}

	t.Run("different keys merge", func(t *testing.T) {
		h, err := run(t, map[string]*string{"sub-agents/x.md": nil}, map[string]*string{"SKILL.md": nil})
		require.NoError(t, err)
		require.Len(t, h.Skills, 2)
		b := skillB(t, h)
		assert.Len(t, b.Overrides, 2)
		assert.Contains(t, b.Overrides, "sub-agents/x.md")
		assert.Contains(t, b.Overrides, "SKILL.md")
	})

	t.Run("same key same value agrees", func(t *testing.T) {
		h, err := run(t, map[string]*string{"sub-agents/x.md": nil}, map[string]*string{"sub-agents/x.md": nil})
		require.NoError(t, err)
		require.Len(t, h.Skills, 2)
		b := skillB(t, h)
		v, ok := b.Overrides["sub-agents/x.md"]
		assert.True(t, ok, "override sub-agents/x.md: null was dropped; got overrides=%v", b.Overrides)
		assert.Nil(t, v)
	})

	t.Run("same key same URL agrees", func(t *testing.T) {
		u1, u2 := overrideURL, overrideURL
		h, err := run(t, map[string]*string{"sub-agents/x.md": &u1}, map[string]*string{"sub-agents/x.md": &u2})
		require.NoError(t, err)
		b := skillB(t, h)
		v := b.Overrides["sub-agents/x.md"]
		require.NotNil(t, v)
		assert.True(t, strings.HasSuffix(*v, "/content"), "override not resolved to a cache path: %s", *v)
	})

	t.Run("same key different values fails", func(t *testing.T) {
		u := overrideURL
		_, err := run(t, map[string]*string{"sub-agents/x.md": nil}, map[string]*string{"sub-agents/x.md": &u})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `skills[2]: override for "sub-agents/x.md" is also set by another entry for the same skill`)
	})
}
