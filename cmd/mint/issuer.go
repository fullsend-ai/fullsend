package main

import (
	"os"
	"strings"
)

// defaultOIDCIssuerURL is GitHub Actions' OIDC issuer.
const defaultOIDCIssuerURL = "https://token.actions.githubusercontent.com"

// oidcIssuerURL is the OIDC issuer the standalone mint trusts. It defaults to
// GitHub Actions; MINT_OIDC_ISSUER_URL points it at another issuer, such as a
// GitHub API emulator in a test. A dedicated name is used rather than a
// generic one so a mint started inside an Actions job never picks up the
// job's own settings by accident.
func oidcIssuerURL() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("MINT_OIDC_ISSUER_URL")), "/"); v != "" {
		return v
	}
	return defaultOIDCIssuerURL
}
