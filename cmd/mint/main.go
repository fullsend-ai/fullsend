// Binary fullsend-mint runs the token mint as a standalone HTTP server,
// using direct JWKS validation and filesystem PEM storage instead of GCP.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// buildHandler creates the HTTP handler chain from environment variables.
func buildHandler() (http.Handler, error) {
	h, _, err := buildHandlerAndTracer()
	return h, err
}

// buildHandlerAndTracer is buildHandler that also returns the tracer (nil
// when tracing is disabled) so the caller can drain pending exports on
// shutdown.
func buildHandlerAndTracer() (http.Handler, *mintcore.Tracer, error) {
	if len(mintcore.SplitCSV(os.Getenv("ALLOWED_WORKFLOW_FILES"))) == 0 {
		log.Printf("warning: ALLOWED_WORKFLOW_FILES is not set; all token requests will be rejected")
	}

	githubAPIURL, err := githubAPIBaseURL(os.Getenv("GITHUB_API_URL"))
	if err != nil {
		return nil, nil, err
	}
	issuerURL, err := oidcIssuerForAPI(githubAPIURL)
	if err != nil {
		return nil, nil, err
	}

	verifier, err := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
		IssuerURL: issuerURL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating OIDC verifier: %w", err)
	}

	pemAccessor, err := mintcore.NewFilesystemPEMAccessor(os.Getenv("PEM_DIR"))
	if err != nil {
		return nil, nil, fmt.Errorf("initializing PEM accessor: %w", err)
	}

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
	if err != nil {
		return nil, nil, fmt.Errorf("initializing handler: %w", err)
	}
	if githubAPIURL != "" {
		handler.SetGitHubBaseURL(githubAPIURL)
		log.Printf("GitHub API configured: %s (OIDC issuer: %s)", githubAPIURL, issuerURL)
	}

	tracer, err := buildTracer()
	if err != nil {
		return nil, nil, err
	}

	var serverHandler http.Handler = http.HandlerFunc(handler.ServeHTTP)

	if fallbackURL := os.Getenv("FALLBACK_MINT_URL"); fallbackURL != "" {
		if !isSecureOrLoopbackURL(fallbackURL) {
			return nil, nil, fmt.Errorf("FALLBACK_MINT_URL must be a valid https:// URL (or http:// on localhost or 127.0.0.1), got %q", fallbackURL)
		}
		localRoles, err := parseLocalRoles(os.Getenv("ROLE_APP_IDS"))
		if err != nil {
			return nil, nil, err
		}
		serverHandler = newFallbackHandler(serverHandler, localRoles, fallbackURL)
		log.Printf("fallback mint configured: %s (local roles: %v)", fallbackURL, sortedKeys(localRoles))
	}

	// Trace the whole chain, including fallback routing and proxy errors,
	// not just the local mintcore handler. A nil tracer returns the
	// handler unchanged.
	return tracer.Wrap(serverHandler), tracer, nil
}

// defaultOIDCIssuer is the GitHub Actions OIDC issuer on github.com.
const defaultOIDCIssuer = "https://token.actions.githubusercontent.com"

// isSecureOrLoopbackURL reports whether raw is an absolute https URL, or
// an http URL whose host is localhost or 127.0.0.1. This is the same
// rule mintclient applies to mint URLs, so a mint on the local machine
// (tests, emulators) can be reached without TLS while every remote
// host still requires https.
func isSecureOrLoopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return true
	case "http":
		host := parsed.Hostname()
		return host == "localhost" || host == "127.0.0.1"
	default:
		return false
	}
}

// githubAPIBaseURL validates the optional GITHUB_API_URL value and
// returns it without a trailing slash. An empty value returns "" (the
// mint keeps the github.com defaults).
func githubAPIBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !isSecureOrLoopbackURL(raw) {
		return "", fmt.Errorf("GITHUB_API_URL must be a valid https:// URL (or http:// on localhost or 127.0.0.1), got %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// oidcIssuerForAPI returns the GitHub Actions OIDC issuer that belongs
// to the GitHub server whose REST API is apiURL:
//
//   - "" or api.github.com: https://token.actions.githubusercontent.com
//   - GHE.com (https://api.SUBDOMAIN.ghe.com):
//     https://token.actions.SUBDOMAIN.ghe.com
//   - GitHub Enterprise Server (https://HOST/api/v3):
//     https://HOST/_services/token
//   - any other server: apiURL itself
//
// apiURL must already be validated by githubAPIBaseURL.
func oidcIssuerForAPI(apiURL string) (string, error) {
	if apiURL == "" {
		return defaultOIDCIssuer, nil
	}
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("parsing GITHUB_API_URL: %w", err)
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case host == "api.github.com" && path == "":
		return defaultOIDCIssuer, nil
	case strings.HasPrefix(host, "api.") && strings.HasSuffix(host, ".ghe.com") && path == "":
		return "https://token.actions." + strings.TrimPrefix(host, "api."), nil
	case path == "/api/v3":
		return parsed.Scheme + "://" + parsed.Host + "/_services/token", nil
	default:
		return apiURL, nil
	}
}

func run(ctx context.Context) error {
	missing := checkRequired("ROLE_APP_IDS", "PEM_DIR")
	if len(missing) > 0 {
		return fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	serverHandler, tracer, err := buildHandlerAndTracer()
	if err != nil {
		return err
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           serverHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// stopped is closed once shutdown, including the drain of pending
	// trace exports, has finished, so run does not return (and the
	// process exit) while exports are still in flight.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		if err := tracer.Shutdown(shutdownCtx); err != nil {
			log.Printf("trace export drain incomplete: %v", err)
		}
	}()

	log.Printf("fullsend-mint starting on :%s (standalone mode)", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	<-stopped
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func checkRequired(names ...string) []string {
	var missing []string
	for _, name := range names {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func parseLocalRoles(raw string) (map[string]bool, error) {
	roles := make(map[string]bool)
	if raw == "" {
		return roles, nil
	}
	var ids map[string]string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("failed to parse ROLE_APP_IDS: %w", err)
	}
	for key := range mintcore.RoleOnlyAppIDs(ids) {
		role := strings.ToLower(key)
		if mintcore.HasRole(role) {
			roles[role] = true
		} else {
			log.Printf("warning: ROLE_APP_IDS key %q has no permission entry (not in built-in or CUSTOM_ROLE_PERMISSIONS); will not be handled locally", key)
		}
	}
	return roles, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
