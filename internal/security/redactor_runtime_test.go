package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretRedactor_RuntimeSecrets(t *testing.T) {
	resetRuntimeSecrets()
	t.Cleanup(resetRuntimeSecrets)

	const token = "opaque-token-value-with-no-known-prefix-$1"
	assert.True(t, RegisterRuntimeSecret(token))
	assert.True(t, RegisterRuntimeSecret(token), "idempotent")
	assert.False(t, RegisterRuntimeSecret("short"), "below the minimum length: refused")
	assert.Len(t, runtimeSecretSnapshot(), 1)

	r := NewSecretRedactor()
	res := r.Scan("Authorization: Bearer " + token + " and again " + token + " short short")
	assert.False(t, res.Safe)
	assert.NotContains(t, res.Sanitized, token)
	assert.Contains(t, res.Sanitized, "short short", "values under the minimum length are never masked")
	assert.Equal(t, "Authorization: Bearer *** and again *** short short", res.Sanitized, "masked whole, no prefix")
	if assert.Len(t, res.Findings, 1, "one finding per unique secret, not per occurrence") {
		assert.Equal(t, "runtime_secret", res.Findings[0].Name)
		assert.Equal(t, "critical", res.Findings[0].Severity)
		assert.NotContains(t, res.Findings[0].Detail, token)
	}

	clean := r.Scan("nothing to see")
	assert.True(t, clean.Safe)
	assert.Empty(t, clean.Sanitized)
}
