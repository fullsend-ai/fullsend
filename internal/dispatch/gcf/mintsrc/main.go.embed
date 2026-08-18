// Package function implements a Cloud Function token mint that issues
// GitHub App installation tokens to OIDC-authenticated .fullsend workflows.
//
// Callers present a GitHub OIDC JWT. The mint validates it via GCP STS
// (Workload Identity Federation), looks up the requested role's PEM from
// Secret Manager, and returns a scoped installation token.
package function

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

var requiredEnvVars = []string{
	"GCP_PROJECT_NUMBER",
	"WIF_POOL_NAME",
	"WIF_PROVIDER_NAME",
	"ROLE_APP_IDS",
}

func init() {
	if strings.HasSuffix(os.Args[0], ".test") || strings.HasSuffix(os.Args[0], ".test.exe") {
		return
	}

	var missing []string
	for _, v := range requiredEnvVars {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		log.Fatalf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	perRepoWIFRepos := make(map[string]bool)
	if raw := os.Getenv("PER_REPO_WIF_REPOS"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				perRepoWIFRepos[strings.ToLower(trimmed)] = true
			}
		}
	}

	gcpProjectNum := os.Getenv("GCP_PROJECT_NUMBER")
	httpClient := &http.Client{Timeout: 30 * time.Second}
	wifPoolName := os.Getenv("WIF_POOL_NAME")
	defaultWIFProvider := os.Getenv("WIF_PROVIDER_NAME")

	oidcVerifier, err := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
		HTTPClient:         httpClient,
		GCPProjectNum:      gcpProjectNum,
		WIFPoolName:        wifPoolName,
		DefaultWIFProvider: defaultWIFProvider,
		PerRepoWIFRepos:    perRepoWIFRepos,
	})
	if err != nil {
		log.Fatalf("creating OIDC verifier: %v", err)
	}

	pemAccessor := mintcore.NewGCPSecretPEMAccessor(
		&http.Client{Timeout: 10 * time.Second},
		gcpProjectNum,
	)

	handler, err := mintcore.NewHandler(oidcVerifier, pemAccessor, httpClient)
	if err != nil {
		log.Fatalf("initializing handler: %v", err)
	}
	functions.HTTP("ServeHTTP", handler.ServeHTTP)
}
