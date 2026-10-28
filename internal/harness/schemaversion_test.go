package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaVersionPtr returns a pointer to a schemaVersionValue of v.
// Schema-version tests use it to declare an explicit schema_version, where
// the nil-versus-zero distinction matters (ADR 0127 Decision 1).
func schemaVersionPtr(v int) *schemaVersionValue {
	sv := schemaVersionValue(v)
	return &sv
}

func TestValidateSchemaVersion(t *testing.T) {
	tests := []struct {
		name     string
		version  *schemaVersionValue
		wantErrs []string // substrings the rejection must name; empty means accepted
	}{
		{
			name:    "absent is version 1 and valid",
			version: nil,
		},
		{
			name:    "explicit 1 is valid",
			version: schemaVersionPtr(1),
		},
		{
			name:     "zero is rejected",
			version:  schemaVersionPtr(0),
			wantErrs: []string{"schema_version must be a positive integer, got 0"},
		},
		{
			name:     "negative is rejected",
			version:  schemaVersionPtr(-3),
			wantErrs: []string{"schema_version must be a positive integer, got -3"},
		},
		{
			name:    "unsupported 2 names the declared version, the maximum, and the remedy",
			version: schemaVersionPtr(2),
			wantErrs: []string{
				"schema_version 2 is not supported by this version of fullsend",
				"maximum supported schema_version: 1",
				"upgrade fullsend to a release that supports schema_version 2",
			},
		},
		{
			name:    "unsupported 99 names the declared version, the maximum, and the remedy",
			version: schemaVersionPtr(99),
			wantErrs: []string{
				"schema_version 99 is not supported by this version of fullsend",
				"maximum supported schema_version: 1",
				"upgrade fullsend to a release that supports schema_version 99",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSchemaVersion(tt.version)
			if len(tt.wantErrs) == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErrs {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

func TestEffectiveSchemaVersion(t *testing.T) {
	t.Run("absent defaults to 1", func(t *testing.T) {
		h := &Harness{}
		assert.Equal(t, 1, h.EffectiveSchemaVersion())
	})
	t.Run("explicit 1 stays 1", func(t *testing.T) {
		h := &Harness{SchemaVersion: schemaVersionPtr(1)}
		assert.Equal(t, 1, h.EffectiveSchemaVersion())
	})
}

func TestSchemaVersionsCompose(t *testing.T) {
	t.Run("equal versions compose", func(t *testing.T) {
		assert.True(t, schemaVersionsCompose(1, 1))
	})
	t.Run("mixed versions do not compose", func(t *testing.T) {
		assert.False(t, schemaVersionsCompose(1, 2))
	})
}

func TestCheckSchemaVersionsCompose(t *testing.T) {
	t.Run("composable pair returns nil", func(t *testing.T) {
		child := &Harness{}                                  // absent — effective version 1
		base := &Harness{SchemaVersion: schemaVersionPtr(1)} // explicit version 1
		assert.NoError(t, checkSchemaVersionsCompose(child, base, "bases/parent.yaml"))
	})
	t.Run("mixed pair names both versions and the layer label", func(t *testing.T) {
		child := &Harness{SchemaVersion: schemaVersionPtr(1)}
		base := &Harness{SchemaVersion: schemaVersionPtr(2)}
		err := checkSchemaVersionsCompose(child, base, "bases/parent.yaml")
		require.Error(t, err)
		assert.ErrorContains(t, err, "base layer bases/parent.yaml declares schema_version 2")
		assert.ErrorContains(t, err, "incompatible with schema_version 1 of the harness composing it")
		assert.ErrorContains(t, err, "mixed-version base composition is not supported")
	})
}
