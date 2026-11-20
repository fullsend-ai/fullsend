package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_InputSource(t *testing.T) {
	for _, tc := range []struct {
		value   string
		wantErr string
	}{
		{value: ""},
		{value: "external"},
		{value: "maintainer"},
		{value: "External", wantErr: `input_source "External" is not valid (allowed: external, maintainer)`},
		{value: "untrusted", wantErr: `input_source "untrusted" is not valid (allowed: external, maintainer)`},
	} {
		t.Run(tc.value, func(t *testing.T) {
			h := &Harness{Agent: "agents/test.md", Role: "test", InputSource: tc.value}
			err := h.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.wantErr, err.Error())
		})
	}
}

func TestLoad_InputSourceFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent: agents/test.md\nrole: test\ninput_source: external\nallow_forge_write: true\n"), 0o644))
	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, InputSourceExternal, h.InputSource)
	assert.True(t, h.AllowForgeWrite)

	require.NoError(t, os.WriteFile(path, []byte("agent: agents/test.md\nrole: test\ninput_source: nobody\n"), 0o644))
	_, err = Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `input_source "nobody" is not valid (allowed: external, maintainer)`)
}

// input_source inherits like other scalars; allow_forge_write does not, so
// a base cannot grant a child a forge-write provider.
func TestMergeBaseIntoChild_InputSource(t *testing.T) {
	for _, tc := range []struct {
		name                string
		base, child         Harness
		wantSource          string
		wantAllowForgeWrite bool
	}{
		{name: "child inherits base input_source",
			base: Harness{InputSource: InputSourceExternal}, wantSource: InputSourceExternal},
		{name: "base maintainer is not inherited, so FULLSEND_INPUT_SOURCE still applies",
			base: Harness{InputSource: InputSourceMaintainer}, wantSource: ""},
		{name: "child input_source wins",
			base: Harness{InputSource: InputSourceExternal}, child: Harness{InputSource: InputSourceMaintainer}, wantSource: InputSourceMaintainer},
		{name: "base allow_forge_write is not inherited",
			base: Harness{InputSource: InputSourceExternal, AllowForgeWrite: true}, wantSource: InputSourceExternal},
		{name: "child allow_forge_write is kept",
			base: Harness{InputSource: InputSourceExternal}, child: Harness{AllowForgeWrite: true}, wantSource: InputSourceExternal, wantAllowForgeWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, child := tc.base, tc.child
			require.NoError(t, mergeBaseIntoChild(&base, &child))
			assert.Equal(t, tc.wantSource, child.InputSource)
			assert.Equal(t, tc.wantAllowForgeWrite, child.AllowForgeWrite)
		})
	}
}
