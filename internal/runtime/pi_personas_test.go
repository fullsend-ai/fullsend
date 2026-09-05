package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePersonaFile(t *testing.T, skillDir, name, frontmatter string) {
	t.Helper()
	dir := filepath.Join(skillDir, "sub-agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(frontmatter), 0o644))
}

func TestDiscoverPersonas_Basic(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", "---\nname: correctness\nmodel: opus\n---\nReview for correctness.\n")
	writePersonaFile(t, skill, "style", "---\nname: style\n---\nReview for style.\n")

	personas, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err)
	require.Len(t, personas, 2)
	// Sorted by name.
	assert.Equal(t, "correctness", personas[0].Name)
	assert.Equal(t, "opus", personas[0].Model)
	assert.Equal(t, "style", personas[1].Name)
	assert.Empty(t, personas[1].Model)
}

func TestDiscoverPersonas_NoSubagentsDir(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	personas, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err)
	assert.Empty(t, personas)
}

func TestDiscoverPersonas_ReservedName(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "default", "---\nname: default\n---\n")

	_, err := discoverPersonas([]string{skill}, "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestDiscoverPersonas_AgentNameCollision(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "code", "---\nname: code\n---\n")

	_, err := discoverPersonas([]string{skill}, "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with the agent's own name")
}

func TestDiscoverPersonas_BuiltinAgentNameCollision(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "triage", "---\nname: triage\n---\n")

	_, err := discoverPersonas([]string{skill}, "review")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "built-in agent name")
}

func TestDiscoverPersonas_NameMismatch(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "style", "---\nname: other-name\n---\n")

	_, err := discoverPersonas([]string{skill}, "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must equal file basename")
}

func TestDiscoverPersonas_MissingName(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "style", "---\ndescription: no name\n---\n")

	_, err := discoverPersonas([]string{skill}, "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name: is required")
}

func TestDiscoverPersonas_DuplicateAcrossSkills(t *testing.T) {
	t.Parallel()
	skill1 := t.TempDir()
	skill2 := t.TempDir()
	writePersonaFile(t, skill1, "correctness", "---\nname: correctness\n---\n")
	writePersonaFile(t, skill2, "correctness", "---\nname: correctness\n---\n")

	_, err := discoverPersonas([]string{skill1, skill2}, "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already declared")
}

func TestDiscoverPersonas_InvalidNameShape(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "Bad", "---\nname: Bad\n---\n")

	_, err := discoverPersonas([]string{skill}, "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lowercase alphanumeric")
}

func TestResolvePersonaModels_Basic(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "correctness", Model: "opus"},
		{Name: "style"},
	}
	parentSpec := "anthropic-vertex/claude-sonnet-4-6"
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-opus-4-6":   "anthropic-vertex/claude-opus-4-6",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, err := resolvePersonaModels(personas, nil, parentSpec, nil, trustedSpecs)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model)
	assert.Equal(t, parentSpec, result["style"].Model, "no model → inherits parent")
}

func TestResolvePersonaModels_ConfigOverride(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "correctness", Model: "opus"},
	}
	parentSpec := "anthropic-vertex/claude-sonnet-4-6"
	cfg := map[string]*string{
		"correctness": strp("haiku"),
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-haiku-4-5":  "anthropic-vertex/claude-haiku-4-5",
		"anthropic-vertex/claude-opus-4-6":   "anthropic-vertex/claude-opus-4-6",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, err := resolvePersonaModels(personas, cfg, parentSpec, nil, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", result["correctness"].Model, "config overrides frontmatter")
}

func TestResolvePersonaModels_DefaultFallback(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "style"},
	}
	parentSpec := "anthropic-vertex/claude-sonnet-4-6"
	cfg := map[string]*string{
		"default": strp("haiku"),
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-haiku-4-5":  "anthropic-vertex/claude-haiku-4-5",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, err := resolvePersonaModels(personas, cfg, parentSpec, nil, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", result["style"].Model, "default fills in")
}

func TestResolvePersonaModels_UnknownConfigKey(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "style"},
	}
	parentSpec := "anthropic-vertex/claude-sonnet-4-6"
	cfg := map[string]*string{
		"nonexistent": strp("haiku"),
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	_, err := resolvePersonaModels(personas, cfg, parentSpec, nil, trustedSpecs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "no persona")
}

func TestResolvePersonaModels_UntrustedModel(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "style", Model: "unknown-model-999"},
	}
	parentSpec := "anthropic-vertex/claude-sonnet-4-6"
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	_, err := resolvePersonaModels(personas, nil, parentSpec, nil, trustedSpecs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func strp(s string) *string { return &s }
