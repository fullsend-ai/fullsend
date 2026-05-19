// Package otp generates TOTP codes for GitHub 2FA automation in e2e tests.
package otp

import (
	"fmt"
	"time"

	"github.com/pquerna/otp/totp"
)

// GenerateCode produces a 6-digit TOTP code from a base32-encoded secret.
func GenerateCode(secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("TOTP secret is empty")
	}
	return totp.GenerateCode(secret, time.Now())
}
