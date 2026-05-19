package otp

import (
	"testing"

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

	// Calling GenerateCode twice in quick succession should produce the
	// same code, unless we happen to straddle a 30s TOTP window boundary.
	// To avoid flakes, accept a match with either the first or second code.
	code1, err := GenerateCode(secret)
	require.NoError(t, err)

	code2, err := GenerateCode(secret)
	require.NoError(t, err)

	if code1 != code2 {
		// We straddled a window boundary. Verify the new code is valid
		// (it should be the next period's code).
		valid := totp.Validate(code2, secret)
		assert.True(t, valid, "second code should still be a valid TOTP code")
	}
}
