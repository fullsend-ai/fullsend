package harness

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
)

func strp(s string) *string { return &s }

// A map-form child entry whose directory has no SKILL.md overlays its file
// overrides on the base skill instead of replacing it (#6158).
func TestMergeSkills_MapFormChildWithoutSkillInheritsBaseSource(t *testing.T) {
	dir := t.TempDir()
	baseSkill := writeSkill(t, filepath.Join(dir, "cache", "pr-review"), "pr-review")
	missing := filepath.Join(dir, "dotfs", "skills", "pr-review")

	got := mergeSkills(
		[]SkillEntry{
			{Source: "/cache/lint"},
			{Source: baseSkill, Overrides: map[string]*string{
				"sub-agents/a.md": strp("/base/a.md"),
				"sub-agents/b.md": strp("/base/b.md"),
			}},
		},
		[]SkillEntry{{Source: missing, Overrides: map[string]*string{
			"sub-agents/b.md":               strp("personas/b.md"),
			"sub-agents/user-experience.md": nil,
		}}},
	)

	require.Len(t, got, 2)
	assert.Equal(t, baseSkill, got[1].Source, "the base's resolved source is kept")
	assert.Equal(t, map[string]*string{
		"sub-agents/a.md":               strp("/base/a.md"),
		"sub-agents/b.md":               strp("personas/b.md"),
		"sub-agents/user-experience.md": nil,
	}, got[1].Overrides, "child overrides win over the base's")
}

// A directory without a marker (override files only) is not a skill either.
func TestMergeSkills_MapFormChildDirWithoutMarkerInheritsBaseSource(t *testing.T) {
	dir := t.TempDir()
	baseSkill := writeSkill(t, filepath.Join(dir, "cache", "pr-review"), "pr-review")
	child := filepath.Join(dir, "dotfs", "skills", "pr-review")
	require.NoError(t, os.MkdirAll(filepath.Join(child, "sub-agents"), 0o755))

	got := mergeSkills(
		[]SkillEntry{{Source: baseSkill}},
		[]SkillEntry{{Source: child, Overrides: map[string]*string{}}},
	)
	assert.Equal(t, []string{baseSkill}, SkillSources(got))
}

// A child skill that exists still replaces the base skill.
func TestMergeSkills_MapFormChildWithSkillKeepsChildSource(t *testing.T) {
	dir := t.TempDir()
	baseSkill := writeSkill(t, filepath.Join(dir, "cache", "pr-review"), "pr-review")
	childSkill := writeSkill(t, filepath.Join(dir, "dotfs", "skills", "pr-review"), "pr-review")

	got := mergeSkills(
		[]SkillEntry{{Source: baseSkill, Overrides: map[string]*string{"a.md": strp("/base/a.md")}}},
		[]SkillEntry{{Source: childSkill, Overrides: map[string]*string{"b.md": nil}}},
	)
	require.Len(t, got, 1)
	assert.Equal(t, childSkill, got[0].Source)
	assert.Equal(t, map[string]*string{"a.md": strp("/base/a.md"), "b.md": nil}, got[0].Overrides)
}

// The string form keeps today's behaviour: the child replaces the base.
func TestMergeSkills_StringFormMissingChildKeepsChildSource(t *testing.T) {
	dir := t.TempDir()
	baseSkill := writeSkill(t, filepath.Join(dir, "cache", "pr-review"), "pr-review")
	missing := filepath.Join(dir, "dotfs", "skills", "pr-review")

	got := mergeSkills([]SkillEntry{{Source: baseSkill}}, []SkillEntry{{Source: missing}})
	assert.Equal(t, []string{missing}, SkillSources(got))
}

// Two different pinned URL skills that share a leaf directory stay
// separate entries; neither replaces the other or takes its overrides.
func TestMergeSkills_PinnedURLsSharingLeafDoNotCollide(t *testing.T) {
	base := "https://github.com/fullsend-ai/agents/tree/abc123/skills/issue-labels/github#sha256=" + strings.Repeat("a", 64)
	child := "https://github.com/fullsend-ai/agents/tree/abc123/skills/pr-review/github#sha256=" + strings.Repeat("b", 64)
	baseOverrides := map[string]*string{"x.md": nil}

	got := mergeSkills(
		[]SkillEntry{{Source: base, Overrides: baseOverrides}},
		[]SkillEntry{{Source: child, Overrides: map[string]*string{"sub-agents/y.md": nil}}},
	)
	require.Len(t, got, 2)
	assert.Equal(t, base, got[0].Source)
	assert.Equal(t, baseOverrides, got[0].Overrides)
	assert.Equal(t, child, got[1].Source)
	assert.Equal(t, map[string]*string{"sub-agents/y.md": nil}, got[1].Overrides)
}

func TestSkillSourcePresent(t *testing.T) {
	dir := t.TempDir()
	assert.True(t, skillSourcePresent(""))
	assert.True(t, skillSourcePresent("skills/pr-review"), "relative: not checkable at merge time")
	assert.True(t, skillSourcePresent("https://example.com/skills/x#sha256=abc"))
	assert.False(t, skillSourcePresent(filepath.Join(dir, "missing")))

	lower := filepath.Join(dir, "lower")
	require.NoError(t, os.MkdirAll(lower, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lower, "skill.md"), []byte("---\nname: lower\n---\n"), 0o644))
	assert.True(t, skillSourcePresent(lower), "any marker spelling counts")

	markerDir := filepath.Join(dir, "marker-dir")
	require.NoError(t, os.MkdirAll(filepath.Join(markerDir, "SKILL.md"), 0o755))
	assert.False(t, skillSourcePresent(markerDir), "a directory named SKILL.md is not a marker")
}

// A forge block shares mergeSkills, so a map-form forge entry with no skill
// directory overlays the top-level skill too.
func TestMergeForgeConfig_MapFormSkillInheritsTopLevelSource(t *testing.T) {
	dir := t.TempDir()
	topSkill := writeSkill(t, filepath.Join(dir, "cache", "pr-review"), "pr-review")
	h := &Harness{Skills: []SkillEntry{{Source: topSkill}}}
	mergeForgeConfig(h, &ForgeConfig{Skills: []SkillEntry{{
		Source:    filepath.Join(dir, "skills", "pr-review"),
		Overrides: map[string]*string{"sub-agents/x.md": nil},
	}}})
	require.Len(t, h.Skills, 1)
	assert.Equal(t, topSkill, h.Skills[0].Source)
	assert.Equal(t, map[string]*string{"sub-agents/x.md": nil}, h.Skills[0].Overrides)
}

// The reported route: a local child overlays a URL base's skill with file
// overrides and has no skills/ directory of its own (#6158).
func TestLoadWithBase_URLBase_MapFormChildOverlaysBaseSkill(t *testing.T) {
	dotfs := t.TempDir()

	// The base skill stands in for a fetched one: a fullsend cache path.
	cacheHashDir, err := fetch.CachePath(dotfs, strings.Repeat("e", 64))
	require.NoError(t, err)
	baseSkill := writeSkill(t, filepath.Join(cacheHashDir, "pr-review"), "pr-review")

	baseContent := []byte(fmt.Sprintf("agent: agents/base.md\nrole: review\nskills:\n  - %s\n", baseSkill))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/review.yaml":
			w.Write(baseContent)
		case "/agents/base.md":
			w.Write([]byte("# base agent"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	path := writeTestHarness(t, dotfs, "review.yaml", fmt.Sprintf(`base: %s/review.yaml#sha256=%s
skills:
  - skills/pr-review:
      sub-agents/user-experience.md: null
      sub-agents/rbac-review.md: personas/rbac-review.md
`, server.URL, computeHash(baseContent)))

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: dotfs,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)
	require.Len(t, h.Skills, 1)
	assert.Equal(t, baseSkill, h.Skills[0].Source)
	assert.Equal(t, map[string]*string{
		"sub-agents/user-experience.md": nil,
		"sub-agents/rbac-review.md":     strp("personas/rbac-review.md"),
	}, h.Skills[0].Overrides)

	// Override sources still resolve against the child's fullsend dir.
	require.NoError(t, h.ResolveRelativeTo(dotfs))
	assert.Equal(t, filepath.Join(dotfs, "personas", "rbac-review.md"), *h.Skills[0].Overrides["sub-agents/rbac-review.md"])
}

// A local base that pins the skill by URL: the child's relative entry
// matches it by name and keeps the URL, which resolution fetches later.
func TestLoadWithBase_LocalBase_URLSkillOverlaidByMapFormChild(t *testing.T) {
	dotfs := t.TempDir()
	skillURL := "https://github.com/fullsend-ai/agents/tree/abc123/skills/pr-review#sha256=" + strings.Repeat("d", 64)
	secURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/skills/pr-review/sub-agents/security.md#sha256=" + strings.Repeat("9", 64)
	writeTestHarness(t, dotfs, "base.yaml", fmt.Sprintf(`agent: agents/base.md
role: triage
skills:
  - %s:
      sub-agents/security.md: %s
`, skillURL, secURL))
	path := writeTestHarness(t, dotfs, "child.yaml", `base: base.yaml
skills:
  - skills/pr-review:
      sub-agents/user-experience.md: null
      sub-agents/rbac-review.md: personas/rbac-review.md
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{WorkspaceRoot: dotfs})
	require.NoError(t, err)
	require.Len(t, h.Skills, 1)
	assert.Equal(t, skillURL, h.Skills[0].Source)
	assert.Equal(t, map[string]*string{
		"sub-agents/security.md":        strp(secURL),
		"sub-agents/user-experience.md": nil,
		"sub-agents/rbac-review.md":     strp("personas/rbac-review.md"),
	}, h.Skills[0].Overrides)
}

// A missing map-form child does not inherit when two pinned-URL base
// skills share its name.
func TestMergeSkills_MissingChildAmbiguousURLBaseDoesNotInherit(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "skills", "github")
	a := "https://github.com/o/r/tree/abc/skills/issue-labels/github#sha256=" + strings.Repeat("a", 64)
	b := "https://github.com/o/r/tree/abc/skills/pr-review/github#sha256=" + strings.Repeat("b", 64)

	got := mergeSkills(
		[]SkillEntry{{Source: a}, {Source: b}},
		[]SkillEntry{{Source: missing, Overrides: map[string]*string{"x.md": nil}}},
	)
	require.Len(t, got, 3)
	assert.Equal(t, missing, got[2].Source)
}
