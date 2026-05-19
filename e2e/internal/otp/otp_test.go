package otp

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateCode(t *testing.T) {
	// A valid base32-encoded TOTP secret (test-only).
	const secret = "JBSWY3DPEHPK3PXP"

	code, err := GenerateCode(secret)
	require.NoError(t, err)

	assert.Len(t, code, 6, "TOTP codes are 6 digits")

	// The code we generate should validate against the same secret.
	valid := totp.Validate(code, secret)
	assert.True(t, valid, "generated code should validate against the secret")
}

func TestGenerateCodeRejectsEmpty(t *testing.T) {
	_, err := GenerateCode("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TOTP secret")
}

func TestGenerateCodeRejectsInvalidBase32(t *testing.T) {
	_, err := GenerateCode("not-valid-base32!!!")
	require.Error(t, err)
}

func TestGenerateCodeDeterministic(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"

	// Two calls within the same 30s window should produce the same code.
	code1, err := GenerateCode(secret)
	require.NoError(t, err)

	// Small sleep to stay in the same period (well under 30s).
	time.Sleep(100 * time.Millisecond)

	code2, err := GenerateCode(secret)
	require.NoError(t, err)

	assert.Equal(t, code1, code2, "codes generated in the same period should match")
}
