package main

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestTOTPCodeGeneration(t *testing.T) {
	// Use a known base32 secret and a fixed time to verify we can generate
	// valid TOTP codes. This validates that the pquerna/otp integration
	// works correctly with the default SHA1/6-digit/30s parameters that
	// GitHub uses.
	secret := "JBSWY3DPEHPK3PXP" // standard test vector base32 secret
	fixedTime := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	code, err := totp.GenerateCode(secret, fixedTime)
	if err != nil {
		t.Fatalf("GenerateCode returned error: %v", err)
	}

	if len(code) != 6 {
		t.Fatalf("expected 6-digit code, got %q (len=%d)", code, len(code))
	}

	// Verify the code validates against the same secret and time.
	valid, err := totp.ValidateCustom(code, secret, fixedTime, totp.ValidateOpts{
		Digits: 6,
		Period: 30,
	})
	if err != nil {
		t.Fatalf("ValidateCustom returned error: %v", err)
	}
	if !valid {
		t.Fatalf("generated code %q did not validate against the same secret and time", code)
	}
}

func TestTOTPCodeGenerationInvalidSecret(t *testing.T) {
	// Verify that an invalid secret produces an error, which maps to the
	// error path in handle2FA.
	_, err := totp.GenerateCode("not-valid-base32!!!", time.Now())
	if err == nil {
		t.Fatal("expected error for invalid base32 secret, got nil")
	}
}
