// Package function implements a Cloud Function token mint that issues
// GitHub App installation tokens to OIDC-authenticated .fullsend workflows.
//
// Callers present a GitHub OIDC JWT. The mint validates it via GCP STS
// (Workload Identity Federation), looks up the requested role's PEM from
// Secret Manager, and returns a scoped installation token.
package function

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// traceHeadersTimeout bounds the cold-start read of the collector
// headers secret, so a slow Secret Manager cannot hold up the mint.
const traceHeadersTimeout = 5 * time.Second

// secretReader reads the latest version of a Secret Manager secret.
// Production uses mintcore.AccessGCPSecret.
type secretReader func(ctx context.Context, gcpProjectNum, secretID string) ([]byte, error)

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
	wifPoolName := os.Getenv("WIF_POOL_NAME")
	defaultWIFProvider := os.Getenv("WIF_PROVIDER_NAME")

	verifier, err := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
		GCPProjectNum:      gcpProjectNum,
		WIFPoolName:        wifPoolName,
		DefaultWIFProvider: defaultWIFProvider,
		PerRepoWIFRepos:    perRepoWIFRepos,
	})
	if err != nil {
		log.Fatalf("creating OIDC verifier: %v", err)
	}

	pemAccessor := mintcore.NewGCPSecretPEMAccessor(gcpProjectNum)

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
	if err != nil {
		log.Fatalf("initializing handler: %v", err)
	}
	handler.SetTracer(buildTracer(context.Background(), gcpProjectNum, otlpTracesEndpoint, traceDeployment, otlpHeadersSecret, mintcore.AccessGCPSecret))
	functions.HTTP("ServeHTTP", handler.ServeHTTP)
}

// buildTracer configures tracing from the stamped endpoint and deployment
// identity, reading collector headers from the stamped Secret Manager
// secret. Tracing is fail-open: a bad configuration or an unreadable
// secret is logged (without header values) and leaves tracing disabled
// rather than failing mint initialization. It returns nil when no
// endpoint is stamped, without touching Secret Manager.
func buildTracer(ctx context.Context, gcpProjectNum, endpoint, deployment, headersSecret string, readSecret secretReader) *mintcore.Tracer {
	if endpoint == "" {
		return nil
	}
	rawHeaders := ""
	if headersSecret != "" {
		readCtx, cancel := context.WithTimeout(ctx, traceHeadersTimeout)
		defer cancel()
		data, err := readSecret(readCtx, gcpProjectNum, headersSecret)
		if err != nil {
			log.Printf("warning: mint tracing disabled: reading collector headers secret %s: %v", headersSecret, err)
			return nil
		}
		rawHeaders = string(data)
	}
	tracer, err := mintcore.NewStampedTracer(mintcore.TraceBackendGCF, deployment, endpoint, rawHeaders)
	if err != nil {
		log.Printf("warning: mint tracing disabled: %v", err)
		return nil
	}
	return tracer
}
