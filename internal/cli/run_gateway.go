package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
)

// Inference gateway credential route (ADR 0137). The runner fetches the
// job's forge OIDC assertion with the block's audience and hands it to the
// gateway as a bearer token: there is no exchange, so the assertion is the
// credential and its exp is the JWT's own. GitHub's assertions are
// short-lived (exp - iat = 300 s in GitHub's documented example and on
// real tokens), but GitHub does not promise that value, so every schedule
// below is derived from each token's own iat and exp.
//
// The route is selected per model by the gateway/ provider prefix. It has
// no precedence against the OpenAI routes: openai/ models keep resolving
// through resolveOpenAICredential, unchanged, even when a block is set.
const gatewayModelProvider = "gateway"

// Gateway refresh budget. Variables, not constants, so tests can shrink
// them. A 300 s token leaves far less room than the OpenAI route's
// one-hour ceiling, so the settle wait and retries are sized to finish
// inside the time left after the refresh fires (see gatewayRefreshDelay).
var (
	// gatewayPlaceholderSettle bounds how long a refresh waits for the
	// sandbox to observe the new credential generation before re-seeding
	// the token file (~20 s measured on OpenShell 0.0.115).
	gatewayPlaceholderSettle = 45 * time.Second
	// gatewayRefreshRetries/Backoff retry a failed assertion fetch or
	// provider update.
	gatewayRefreshRetries = 2
	gatewayRefreshBackoff = 5 * time.Second
	// gatewayFetchTimeout bounds one assertion fetch plus provider update.
	gatewayFetchTimeout = 10 * time.Second
	// gatewayRefreshSafety is kept free after the refresh work completes,
	// so the new token is in place before the old one expires.
	gatewayRefreshSafety = 15 * time.Second
	// gatewayRefreshJitter caps the spread of concurrent runs' refreshes.
	gatewayRefreshJitter = 30 * time.Second
	// gatewayRefreshMinDelay keeps a very short-lived token from spinning
	// the refresh loop.
	gatewayRefreshMinDelay = 5 * time.Second
)

// gatewayRefreshWork is the worst-case time one refresh takes: every
// fetch attempt with its backoff, then the settle wait.
func gatewayRefreshWork() time.Duration {
	attempts := time.Duration(gatewayRefreshRetries + 1)
	return attempts*gatewayFetchTimeout + time.Duration(gatewayRefreshRetries)*gatewayRefreshBackoff + gatewayPlaceholderSettle
}

// gatewayRefreshDelay returns how long to wait before refreshing a token
// issued at iat that expires at exp, and whether the refresh work fits in
// the time left before exp. The lead before exp is half the token's
// lifetime, but never less than the refresh work plus a safety margin.
// jitterFrac (in [0,1)) spreads runs apart by up to gatewayRefreshJitter,
// capped at a quarter of the slack before the lead so it never eats into
// the work budget.
func gatewayRefreshDelay(iat, exp, now time.Time, jitterFrac float64) (time.Duration, bool) {
	remaining := exp.Sub(now)
	need := gatewayRefreshWork() + gatewayRefreshSafety
	lead := exp.Sub(iat) / 2
	if lead < need {
		lead = need
	}
	slack := remaining - lead
	if slack <= 0 {
		return gatewayRefreshMinDelay, remaining-gatewayRefreshMinDelay >= need
	}
	jitter := gatewayRefreshJitter
	if q := slack / 4; jitter > q {
		jitter = q
	}
	if jitterFrac < 0 || jitterFrac >= 1 {
		jitterFrac = 0
	}
	d := slack - time.Duration(float64(jitter)*jitterFrac)
	if d < gatewayRefreshMinDelay {
		d = gatewayRefreshMinDelay
	}
	return d, remaining-d >= need
}

// isGatewayModel reports whether model selects the gateway route: a
// provider/id with provider "gateway", case-folded.
func isGatewayModel(model string) bool {
	provider, _, ok := strings.Cut(strings.TrimSpace(model), "/")
	return ok && strings.EqualFold(provider, gatewayModelProvider)
}

// anyGatewayModel reports whether any of models selects the gateway route.
func anyGatewayModel(models []string) bool {
	for _, m := range models {
		if isGatewayModel(m) {
			return true
		}
	}
	return false
}

// gatewayRouteRuntimes are the runtimes that implement the gateway route.
var gatewayRouteRuntimes = []string{"pi"}

// validateGatewayRuntime refuses a gateway/ model on a runtime without the
// route (Claude Code, Codex): the model would otherwise fail later with an
// unknown provider, or worse reach a different credential.
func validateGatewayRuntime(runtimeName string, models []string) error {
	if !anyGatewayModel(models) {
		return nil
	}
	for _, r := range gatewayRouteRuntimes {
		if runtimeName == r {
			return nil
		}
	}
	return fmt.Errorf("gateway/ models need the inference gateway route, which runtime %q does not implement (supported: %s)", runtimeName, strings.Join(gatewayRouteRuntimes, ", "))
}

// gatewayOIDCEnv is the forge OIDC endpoint the runner fetches assertions
// from. Variables so tests can stub the environment.
var gatewayOIDCEnv = func() (requestURL, requestToken string) {
	return os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
}

// gatewayBlockApplies reports whether the runner owns the gateway route
// for this run: a complete inference.gateway block and a run with a forge
// OIDC endpoint. A partial block is an error. With no block, or without an
// OIDC endpoint (a local run), the runner adds nothing for gateway/ models,
// so the harness-plugin setup in the local guide keeps working.
func gatewayBlockApplies(g config.InferenceGatewayConfig) (bool, error) {
	g = g.Trimmed()
	if g.IsZero() {
		return false, nil
	}
	if missing := g.Missing(); len(missing) > 0 {
		return false, fmt.Errorf("inference.gateway is partial: missing %s (url and audience are all or none)", strings.Join(missing, " and "))
	}
	if err := g.Validate(); err != nil {
		return false, err
	}
	reqURL, _ := gatewayOIDCEnv()
	return reqURL != "", nil
}

// fetchGatewayAssertion is the assertion fetch, a variable for tests.
var fetchGatewayAssertion = openaiwif.FetchAssertion

// fetchGatewayToken fetches the job's OIDC assertion for the block's
// audience. A failure fails the run: the route never falls back to the
// openai provider, WIF or a static key. Errors never carry the token.
func fetchGatewayToken(ctx context.Context, g config.InferenceGatewayConfig) (*openaiwif.Assertion, error) {
	reqURL, reqToken := gatewayOIDCEnv()
	a, err := fetchGatewayAssertion(ctx, openaiwif.AssertionConfig{
		Audience:         strings.TrimSpace(g.Audience),
		OIDCRequestURL:   reqURL,
		OIDCRequestToken: reqToken,
	})
	if err != nil {
		return nil, fmt.Errorf("inference gateway: fetching the OIDC assertion: %w", err)
	}
	return a, nil
}
