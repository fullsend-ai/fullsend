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
	allowedOrgs := splitCSV(os.Getenv("ALLOWED_ORGS"))
	allowedWorkflows := splitCSV(os.Getenv("ALLOWED_WORKFLOW_FILES"))

	perRepoWIFRepos := make(map[string]bool)
	for _, entry := range splitCSV(os.Getenv("PER_REPO_WIF_REPOS")) {
		perRepoWIFRepos[strings.ToLower(entry)] = true
	}

	if len(allowedWorkflows) == 0 {
		log.Printf("warning: ALLOWED_WORKFLOW_FILES is not set; all token requests will be rejected")
	}

	verifier := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
		IssuerURL:            "https://token.actions.githubusercontent.com",
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		HTTPClient:           &http.Client{Timeout: 30 * time.Second},
		AllowedOrgs:          allowedOrgs,
		AllowedWorkflowFiles: allowedWorkflows,
		PerRepoWIFRepos:      perRepoWIFRepos,
	})

	if err := registerCustomPermissions(); err != nil {
		return nil, err
	}

	pemAccessor, err := mintcore.NewFilesystemPEMAccessor(os.Getenv("PEM_DIR"))
	if err != nil {
		return nil, fmt.Errorf("initializing PEM accessor: %w", err)
	}
	if err := mintcore.WarnAllPEMsInDir(os.Getenv("PEM_DIR")); err != nil {
		log.Printf("warning: scanning PEM directory: %v", err)
	}

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
	if err != nil {
		return nil, fmt.Errorf("initializing handler: %w", err)
	}

	var serverHandler http.Handler = http.HandlerFunc(handler.ServeHTTP)

	if fallbackURL := os.Getenv("FALLBACK_MINT_URL"); fallbackURL != "" {
		parsed, parseErr := url.Parse(fallbackURL)
		if parseErr != nil || strings.ToLower(parsed.Scheme) != "https" || parsed.Host == "" {
			return nil, fmt.Errorf("FALLBACK_MINT_URL must be a valid https:// URL, got %q", fallbackURL)
		}
		localRoles, err := parseLocalRoles(os.Getenv("ROLE_APP_IDS"))
		if err != nil {
			return nil, err
		}
		serverHandler = newFallbackHandler(serverHandler, localRoles, fallbackURL)
		log.Printf("fallback mint configured: %s (local roles: %v)", fallbackURL, sortedKeys(localRoles))
	}

	return serverHandler, nil
}

func run(ctx context.Context) error {
	missing := checkRequired("ALLOWED_ORGS", "ROLE_APP_IDS", "OIDC_AUDIENCE", "PEM_DIR")
	if len(missing) > 0 {
		return fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	serverHandler, err := buildHandler()
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

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("fullsend-mint starting on :%s (standalone mode)", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
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

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, entry := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
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

func registerCustomPermissions() error {
	raw := os.Getenv("CUSTOM_ROLE_PERMISSIONS")
	if raw == "" {
		return nil
	}
	var perms map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &perms); err != nil {
		return fmt.Errorf("failed to parse CUSTOM_ROLE_PERMISSIONS: %w", err)
	}
	if err := mintcore.RegisterCustomRolePermissions(perms); err != nil {
		return fmt.Errorf("registering custom role permissions: %w", err)
	}
	roles := make([]string, 0, len(perms))
	for r := range perms {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	log.Printf("custom role permissions registered: %v", roles)
	return nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
