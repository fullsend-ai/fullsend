// Package otp generates TOTP codes for GitHub 2FA automation in e2e tests.
package otp

import (
	"fmt"
	"time"

	"github.com/pquerna/otp/totp"
)

// GenerateCode produces a 6-digit TOTP code from a base32-encoded secret.
// If the current time is within 4 seconds of a 30-second period boundary,
// it sleeps until the next period to avoid generating a code that expires
// before the recipient can validate it.
func GenerateCode(secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("TOTP secret is empty")
	}
	// Avoid generating a code near a period boundary where it could expire
	// during the ~300ms PressSequentially typing delay plus network round-trip.
	if rem := time.Now().Second() % 30; rem >= 26 {
		time.Sleep(time.Duration(30-rem+1) * time.Second)
	}
	now := time.Now()
	return totp.GenerateCode(secret, now)
}
