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
	"ALLOWED_ORGS",
	"GCP_PROJECT_NUMBER",
	"WIF_POOL_NAME",
	"WIF_PROVIDER_NAME",
	"ROLE_APP_IDS",
	"OIDC_AUDIENCE",
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

	oidcAudience := os.Getenv("OIDC_AUDIENCE")

	var allowedOrgs []string
	for _, entry := range strings.Split(os.Getenv("ALLOWED_ORGS"), ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			allowedOrgs = append(allowedOrgs, trimmed)
		}
	}

	var allowedWorkflows []string
	if wf := os.Getenv("ALLOWED_WORKFLOW_FILES"); wf != "" {
		for _, entry := range strings.Split(wf, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				allowedWorkflows = append(allowedWorkflows, trimmed)
			}
		}
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

	verifier := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
		HTTPClient:         httpClient,
		GCPProjectNum:      gcpProjectNum,
		WIFPoolName:        os.Getenv("WIF_POOL_NAME"),
		DefaultWIFProvider: os.Getenv("WIF_PROVIDER_NAME"),
		AllowedOrgs:        allowedOrgs,
		AllowedWorkflows:   allowedWorkflows,
		PerRepoWIFRepos:    perRepoWIFRepos,
		OIDCAudience:       oidcAudience,
	})

	pemAccessor := mintcore.NewGCPSecretPEMAccessor(
		&http.Client{Timeout: 10 * time.Second},
		gcpProjectNum,
	)

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
	if err != nil {
		log.Fatalf("initializing handler: %v", err)
	}
	functions.HTTP("ServeHTTP", handler.ServeHTTP)
}
