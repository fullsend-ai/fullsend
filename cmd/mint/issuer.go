package main

import (
	"errors"
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
// mintcore.CheckServiceBaseURL. The value is kept as given, apart from
// surrounding space: the verifier compares it to each token's iss exactly.
//
// MINT_OIDC_ISSUER_URL and MINT_GITHUB_API_URL name one GitHub instance, so
// they are set together or not at all. With only the API set, the mint would
// trust github.com's Actions tokens while minting on another server, where
// the same owner and repository names can belong to someone else.
func oidcIssuerURL() (string, error) {
	issuer := strings.TrimSpace(os.Getenv("MINT_OIDC_ISSUER_URL"))
	api := strings.TrimSpace(os.Getenv("MINT_GITHUB_API_URL"))
	if (issuer == "") != (api == "") {
		return "", errors.New("MINT_OIDC_ISSUER_URL and MINT_GITHUB_API_URL name one GitHub server: set both or neither")
	}
	if issuer == "" {
		return defaultOIDCIssuerURL, nil
	}
	if err := mintcore.CheckServiceBaseURL("MINT_OIDC_ISSUER_URL", issuer); err != nil {
		return "", err
	}
	return issuer, nil
}
