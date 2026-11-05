package runtime

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DiscoverPersonas returns exactly what discoverPersonas registers and
// skips, with the file location and bytes the runner's manifest needs.
func TestDiscoverPersonas_ExportedMatchesDiscovery(t *testing.T) {
	t.Parallel()
	a := filepath.Join(t.TempDir(), "alpha")
	b := filepath.Join(t.TempDir(), "beta")
	const security = "---\nname: security\nrole: dimension\n---\nalpha\n"
	writePersonaFile(t, a, "security", security)
	writePersonaFile(t, a, "mismatch", "---\nname: other\n---\n")
	writePersonaFile(t, b, "security", "---\nname: security\n---\nbeta\n")
	writePersonaFile(t, b, "style", "---\nname: style\n---\n")

	got, skipped := DiscoverPersonas([]string{a, b}, "review")
	inner, innerSkipped, err := discoverPersonas([]string{a, b}, "review")
	require.NoError(t, err)

	require.Len(t, got, len(inner))
	for i := range got {
		assert.Equal(t, inner[i].Name, got[i].Name)
	}
	assert.Equal(t, RegisteredPersona{
		Name:        "security",
		SkillDir:    a,
		Path:        filepath.Join(a, "sub-agents", "security.md"),
		Data:        []byte(security),
		Frontmatter: []byte("name: security\nrole: dimension\n"),
	}, got[0])
	assert.Equal(t, "style", got[1].Name)
	assert.Equal(t, b, got[1].SkillDir)

	require.Len(t, skipped, len(innerSkipped))
	assert.Equal(t, []SkippedPersona{
		{Path: filepath.Join(a, "sub-agents", "mismatch.md"), Reason: `frontmatter name "other" must equal file basename "mismatch"`},
		{Path: filepath.Join(b, "sub-agents", "security.md"), Reason: `name already declared by skill "alpha"`},
	}, skipped)
	assert.Equal(t, "persona "+filepath.Join(b, "sub-agents", "security.md")+` skipped: name already declared by skill "alpha"`, skipped[1].String())
}
