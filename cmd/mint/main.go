// Binary fullsend-mint runs the token mint as a standalone HTTP server,
// using direct JWKS validation and filesystem PEM storage instead of GCP.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
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
	h, _, _, err := buildHandlerAndTracer()
	return h, err
}

// buildHandlerAndTracer is buildHandler that also returns the tracer (nil
// when tracing is disabled) so the caller can drain pending exports on
// shutdown, and the mint core, whose Tick the mint timer calls.
func buildHandlerAndTracer() (http.Handler, *mintcore.Tracer, *mintcore.Handler, error) {
	if len(mintcore.SplitCSV(os.Getenv("ALLOWED_WORKFLOW_FILES"))) == 0 {
		log.Printf("warning: ALLOWED_WORKFLOW_FILES is not set; all token requests will be rejected")
	}

	verifier, err := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
		IssuerURL: "https://token.actions.githubusercontent.com",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating OIDC verifier: %w", err)
	}

	pemAccessor, err := mintcore.NewFilesystemPEMAccessor(os.Getenv("PEM_DIR"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initializing PEM accessor: %w", err)
	}

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initializing handler: %w", err)
	}

	tracer, err := buildTracer()
	if err != nil {
		return nil, nil, nil, err
	}

	var serverHandler http.Handler = http.HandlerFunc(handler.ServeHTTP)

	if fallbackURL := os.Getenv("FALLBACK_MINT_URL"); fallbackURL != "" {
		parsed, parseErr := url.Parse(fallbackURL)
		if parseErr != nil || strings.ToLower(parsed.Scheme) != "https" || parsed.Host == "" {
			return nil, nil, nil, fmt.Errorf("FALLBACK_MINT_URL must be a valid https:// URL, got %q", fallbackURL)
		}
		localRoles, err := parseLocalRoles(os.Getenv("ROLE_APP_IDS"))
		if err != nil {
			return nil, nil, nil, err
		}
		serverHandler = newFallbackHandler(serverHandler, localRoles, fallbackURL)
		log.Printf("fallback mint configured: %s (local roles: %v)", fallbackURL, sortedKeys(localRoles))
	}

	// Trace the whole chain, including fallback routing and proxy errors,
	// not just the local mintcore handler. A nil tracer returns the
	// handler unchanged.
	return tracer.Wrap(serverHandler), tracer, handler, nil
}

// tickRunner is the mint timer core, mintcore.Handler.Tick.
type tickRunner interface {
	Tick(ctx context.Context) mintcore.TickResult
}

// tickJitter is the random delay before the first tick, up to a tenth of
// the interval, so replicas started together do not tick together.
var tickJitter = func(interval time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(interval/10)+1))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

// runTicker calls r.Tick after initialDelay and then every interval, until
// ctx is done. Ticks run one at a time; each gets a context that ends
// after one interval, so a slow tick cannot pile up behind the next one.
// Several replicas each run their own ticker, which is harmless: the
// poll's concurrency group coalesces their dispatches, and a delivery
// redelivered twice is answered from the webhook route's debounce or by
// the same coalescing.
func runTicker(ctx context.Context, r tickRunner, interval, initialDelay time.Duration) {
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		tickCtx, cancel := context.WithTimeout(ctx, interval)
		r.Tick(tickCtx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// startTicker starts the mint timer when the core has work for it
// (redelivery or the sweep) and returns a channel closed once it has
// stopped; the channel is already closed when the timer is off.
func startTicker(ctx context.Context, mint *mintcore.Handler) <-chan struct{} {
	done := make(chan struct{})
	if !mint.TickEnabled() {
		log.Printf("mint tick: disabled (redelivery needs FULLSEND_WEBHOOK_SECRET; sweep is off or the fullsend App is not configured)")
		close(done)
		return done
	}
	interval := mint.TickInterval()
	delay := tickJitter(interval)
	log.Printf("mint tick: every %s, first in %s", interval, delay.Round(time.Second))
	go func() {
		defer close(done)
		runTicker(ctx, mint, interval, delay)
	}()
	return done
}

func run(ctx context.Context) error {
	// Cancel on any return, so the mint timer stops even when the server
	// fails to start.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	missing := checkRequired("ROLE_APP_IDS", "PEM_DIR")
	if len(missing) > 0 {
		return fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	serverHandler, tracer, mint, err := buildHandlerAndTracer()
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
	tickerStopped := startTicker(ctx, mint)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	<-stopped
	<-tickerStopped
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
