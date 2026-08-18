//go:build !js

package mintcore

import "strings"

// NewSTSVerifierFromEnv constructs an STSVerifier wired to the
// package-level mintEnv and mintHTTP accessors. It reads all GCP and
// OIDC configuration from the environment and obtains an HTTP client
// from mintHTTP, then delegates to NewSTSVerifier with plain data —
// keeping heavy constructors free of environment coupling.
//
// Use this as the VerifierFactory argument to NewHandler in entrypoints
// that validate tokens via GCP STS (Cloud Function).
func NewSTSVerifierFromEnv() (OIDCVerifier, error) {
	perRepoWIFRepos := make(map[string]bool)
	for _, entry := range SplitCSV(mintEnv("PER_REPO_WIF_REPOS")) {
		perRepoWIFRepos[strings.ToLower(entry)] = true
	}
	return NewSTSVerifier(STSVerifierConfig{
		HTTPClient:         mintHTTP(),
		Audience:           mintEnv("OIDC_AUDIENCE"),
		GCPProjectNum:      mintEnv("GCP_PROJECT_NUMBER"),
		WIFPoolName:        mintEnv("WIF_POOL_NAME"),
		DefaultWIFProvider: mintEnv("WIF_PROVIDER_NAME"),
		PerRepoWIFRepos:    perRepoWIFRepos,
	})
}
