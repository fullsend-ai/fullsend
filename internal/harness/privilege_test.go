package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivilegeLevelForStage_OmittedDefaultsToWrite(t *testing.T) {
	var h *Harness
	assert.Equal(t, DefaultPrivilegeLevel, h.PrivilegeLevelForStage(PrivilegeStageRuntime))

	h = &Harness{}
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePreScript))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePostScript))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStageValidationLoop))
}

func TestPrivilegeLevelForStage_ExplicitAndDefault(t *testing.T) {
	h := &Harness{PrivilegeLevels: map[string]string{
		PrivilegeStageRuntime: "read",
		PrivilegeStageDefault: "write",
	}}
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePreScript))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePostScript))
}

func TestPrivilegeLevelForStage_ValidationLoopInheritsRuntime(t *testing.T) {
	h := &Harness{PrivilegeLevels: map[string]string{
		PrivilegeStageRuntime: "read",
		PrivilegeStageDefault: "write",
	}}
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStageValidationLoop))
}

func TestPrivilegeLevelForStage_OnlyRuntimeRead(t *testing.T) {
	// Typical least-privilege config: only runtime is listed, so scripts
	// fall through to the omitted-field default of write.
	h := &Harness{PrivilegeLevels: map[string]string{
		PrivilegeStageRuntime: "read",
	}}
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePreScript))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePostScript))
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStageValidationLoop))
}

func TestPrivilegeLevelForStage_DefaultRead(t *testing.T) {
	h := &Harness{PrivilegeLevels: map[string]string{
		PrivilegeStageDefault: "read",
	}}
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStagePreScript))
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStagePostScript))
}

func TestLoad_PrivilegeLevels(t *testing.T) {
	content := `
agent: agents/test.md
role: coder
privilege_levels:
  pre_script: write
  runtime: read
  post_script: write
`
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "write", h.PrivilegeLevels[PrivilegeStagePreScript])
	assert.Equal(t, "read", h.PrivilegeLevels[PrivilegeStageRuntime])
	assert.Equal(t, "write", h.PrivilegeLevels[PrivilegeStagePostScript])
	assert.Equal(t, "read", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
}

func TestLoad_PrivilegeLevelsUnknownStage(t *testing.T) {
	content := `
agent: agents/test.md
role: coder
privilege_levels:
  validation_loop: read
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown run-stage")
	assert.Contains(t, err.Error(), "validation_loop")
}

func TestLoad_PrivilegeLevelsInvalidName(t *testing.T) {
	content := `
agent: agents/test.md
role: coder
privilege_levels:
  runtime: WRITE
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid level name")
	assert.Contains(t, err.Error(), "WRITE")
}

func TestLoad_PrivilegeLevelsEmptyValue(t *testing.T) {
	content := `
agent: agents/test.md
role: coder
privilege_levels:
  runtime: ""
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "level is required")
}

func TestLoad_PrivilegeLevelsEmptyMap(t *testing.T) {
	content := `
agent: agents/test.md
role: coder
privilege_levels: {}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
}

func TestLoad_PrivilegeLevelsCustomLevel(t *testing.T) {
	content := `
agent: agents/test.md
role: coder
privilege_levels:
  runtime: contents-read
  default: write
`
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "contents-read", h.PrivilegeLevelForStage(PrivilegeStageRuntime))
	assert.Equal(t, "write", h.PrivilegeLevelForStage(PrivilegeStagePreScript))
}
