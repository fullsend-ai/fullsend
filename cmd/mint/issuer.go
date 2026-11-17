package main

import (
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// defaultOIDCIssuerURL is GitHub Actions' OIDC issuer.
const defaultOIDCIssuerURL = "https://token.actions.githubusercontent.com"

// oidcIssuerURL is the OIDC issuer the standalone mint trusts. It defaults to
// GitHub Actions; MINT_OIDC_ISSUER_URL points it at another issuer, such as
// GitHub Enterprise Server or a GitHub API emulator in a test. A dedicated
// name is used rather than a generic one so a mint started inside an Actions
// job never picks up the job's own settings by accident. The mint fetches the
// issuer's signing keys from it, so the override must pass
// mintcore.CheckServiceBaseURL.
func oidcIssuerURL() (string, error) {
	v := strings.TrimRight(strings.TrimSpace(os.Getenv("MINT_OIDC_ISSUER_URL")), "/")
	if v == "" {
		return defaultOIDCIssuerURL, nil
	}
	if err := mintcore.CheckServiceBaseURL("MINT_OIDC_ISSUER_URL", v); err != nil {
		return "", err
	}
	return v, nil
}
