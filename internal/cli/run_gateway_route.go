package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// gatewayRoutePlan is the inference gateway route for one run (ADR 0137,
// #8280), decided before any provider is created.
type gatewayRoutePlan struct {
	// block is the resolved, complete inference.gateway block.
	block config.InferenceGatewayConfig
	// host is the gateway's host name, bound by the per-host profile.
	host string
	// seed is the runtime's gateway credential seed; zero when the runtime
	// has none (the dummy runtime), so a refresh only updates the provider.
	seed runtime.CredentialSeed
	// prepared is the runtime that registered a gateway run for the
	// sandbox (PrepareGatewayRun); nil when none did.
	prepared runtime.GatewayRouteRuntime
	// apiKeyLifetime bounds the api-key mode's provider instance
	// (gatewayAPIKeyLifetimeFor); zero means gatewayAPIKeyLifetime.
	apiKeyLifetime time.Duration
}

// isDummyRuntime reports whether name is a test runtime the gateway
// provider is attached under when a block applies (behaviour tests,
// Tier A).
func isDummyRuntime(name string) bool { return name == "dummy" || name == "dummy-playback" }

// codexGatewayNeedsBlock is the error for a gateway/ model on codex when no
// inference.gateway block applies, or nil.
func codexGatewayNeedsBlock(runtimeName string, needsGateway bool) error {
	if runtimeName != "codex" || !needsGateway {
		return nil
	}
	return fmt.Errorf("gateway/ models on codex need an inference.gateway block that applies to this run: " +
		"add one to .fullsend/config.yaml (with auth: oidc it applies on a run with a forge OIDC endpoint, with auth: api-key on every run)")
}

// planGatewayRoute decides whether the runner owns the gateway route for
// this run and, when it does, registers the gateway run with the runtime.
// models are the gateway-relevant model specs the run resolves (the
// parent's and its configured children's); needsGateway is whether any of
// them is on the gateway provider by the runtime's own resolution.
//
// A gateway/ model on a runtime without the route is an error, and so is
// a partial block. With no block, or on a run with no forge OIDC endpoint,
// the runner adds nothing, so the harness-plugin setup in the local guide
// keeps working. codex has no plugin fallback, so a gateway/ model on codex
// with no applying block fails here, before the sandbox is created. Under the dummy runtime the provider is attached whenever
// a block applies, so behaviour tests can probe the route.
func planGatewayRoute(rc runConfig, backend runtime.Backend, sandboxName string, models []string, needsGateway bool) (*gatewayRoutePlan, error) {
	name := backend.Runtime.Name()
	if err := validateGatewayRuntime(name, models); err != nil {
		return nil, err
	}
	if rc.perRepo == nil {
		return nil, codexGatewayNeedsBlock(name, needsGateway)
	}
	block := rc.perRepo.ConfigInferenceGateway().Trimmed()
	applies, err := gatewayBlockApplies(block)
	if err != nil {
		return nil, err
	}
	if !applies {
		return nil, codexGatewayNeedsBlock(name, needsGateway)
	}
	if !needsGateway && !isDummyRuntime(name) {
		return nil, nil
	}
	u, err := url.Parse(block.URL)
	if err != nil {
		return nil, fmt.Errorf("inference.gateway.url: %w", err)
	}
	plan := &gatewayRoutePlan{block: block, host: u.Hostname()}
	gr, ok := backend.Runtime.(runtime.GatewayRouteRuntime)
	if !ok {
		return plan, nil
	}
	modelsFile, err := readGatewayModelsFile(rc, block)
	if err != nil {
		return nil, err
	}
	if err := gr.PrepareGatewayRun(sandboxName, runtime.GatewayRun{
		Block:      block,
		ModelsFile: modelsFile,
		BaseURL:    u.Scheme + "://" + u.Host,
	}); err != nil {
		return nil, err
	}
	plan.prepared = gr
	plan.seed = gr.GatewayCredentialSeed()
	return plan, nil
}

// gatewayRepoRoot returns the repository root config.yaml was read from:
// the parent of a .fullsend directory, or the config file's own directory.
func gatewayRepoRoot(source string) string {
	dir := filepath.Dir(source)
	if filepath.Base(dir) == ".fullsend" {
		return filepath.Dir(dir)
	}
	return dir
}

// readGatewayModelsFile reads the block's models_file from the repository
// config.yaml came from, so it is read at the same ref as config.yaml (the
// base branch on pull-request events), whichever layer named it. It
// returns nil when the block has no models_file. The read is confined to
// the repository root (os.Root), so a symlink cannot reach outside it.
func readGatewayModelsFile(rc runConfig, block config.InferenceGatewayConfig) ([]byte, error) {
	if block.ModelsFile == "" {
		return nil, nil
	}
	if rc.source == "" {
		return nil, fmt.Errorf("inference.gateway.models_file %q: no config.yaml location to resolve it against", block.ModelsFile)
	}
	if err := config.ValidateGatewayModelsFilePath(block.ModelsFile); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(gatewayRepoRoot(rc.source))
	if err != nil {
		return nil, fmt.Errorf("inference.gateway.models_file: opening the repository: %w", err)
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(block.ModelsFile))
	if err != nil {
		return nil, fmt.Errorf("inference.gateway.models_file %q: %w", block.ModelsFile, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, runtime.MaxPiGatewayModelsFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("inference.gateway.models_file %q: %w", block.ModelsFile, err)
	}
	if len(data) > runtime.MaxPiGatewayModelsFileBytes {
		return nil, fmt.Errorf("inference.gateway.models_file %q exceeds %d bytes", block.ModelsFile, runtime.MaxPiGatewayModelsFileBytes)
	}
	return data, nil
}

// gatewayProviderHandle describes the run-scoped gateway provider: what to
// refresh, what to re-seed, and what to clean up. It is independent of
// every openAIProviderHandle, so a run on both routes keeps two refresh
// handoffs.
type gatewayProviderHandle struct {
	name    string
	block   config.InferenceGatewayConfig
	sandbox string
	seed    runtime.CredentialSeed
	// apiKey is set in the api-key mode: the credential is not rotated, so
	// the refresher does nothing.
	apiKey bool
	// issuedAt/expiresAt are the current token's iat and exp.
	issuedAt  time.Time
	expiresAt time.Time
	// sandboxUp is set once the sandbox is Ready: from then a refresh also
	// re-seeds the runtime's token file.
	sandboxUp *atomic.Bool
	// state is the refresher's progress, shared across restarts.
	state *gatewayRefreshState
}

// sandboxReady reports whether a refresh has a running sandbox to re-seed.
func (h gatewayProviderHandle) sandboxReady() bool {
	return h.sandboxUp != nil && h.sandboxUp.Load() && h.sandbox != "" && !h.seed.IsZero()
}

// ensureGatewayProviderFn creates the run-scoped gateway provider
// (ensureGatewayProvider). Override in tests to create no provider and
// return a fixed name or an error.
var ensureGatewayProviderFn = ensureGatewayProvider

// startGatewayRoute fetches the job's OIDC assertion for the block's
// audience and creates the run-scoped gateway provider carrying it. A
// failure fails the run: the route never falls back to the openai
// provider, WIF or a static key.
func startGatewayRoute(ctx context.Context, plan *gatewayRoutePlan, sandboxName string, printer *ui.Printer) (gatewayProviderHandle, error) {
	if plan.block.IsAPIKey() {
		return startGatewayAPIKeyRoute(ctx, plan, sandboxName, printer)
	}
	printer.StepStart("Fetching the OIDC assertion for the inference gateway")
	a, err := fetchGatewayToken(ctx, plan.block)
	if err != nil {
		printer.StepFail("Inference gateway credential unavailable")
		return gatewayProviderHandle{}, err
	}
	printer.StepDone(fmt.Sprintf("Inference gateway assertion ready (lifetime %s)", a.Lifetime().Round(time.Second)))
	name, _, err := ensureGatewayProviderFn(ctx, plan.host, sandboxName, a.Value, a.ExpiresAt, printer)
	if err != nil {
		return gatewayProviderHandle{}, err
	}
	return gatewayProviderHandle{
		name:      name,
		block:     plan.block,
		sandbox:   sandboxName,
		seed:      plan.seed,
		issuedAt:  a.IssuedAt,
		expiresAt: a.ExpiresAt,
		sandboxUp: &atomic.Bool{},
		state:     &gatewayRefreshState{issuedAt: a.IssuedAt, expiresAt: a.ExpiresAt, heldExpiresAt: a.ExpiresAt},
	}, nil
}

// gatewayAPIKeyLifetime is the floor of the bound on the api-key mode's
// run-scoped provider instance. The key itself does not expire, but the
// instance must: if the runner dies before the deferred delete, placeholder
// resolution fails closed after the bound instead of serving the key
// indefinitely. The bound is set once, at creation, and never extended:
// ADR 0092 observed on OpenShell 0.0.115 that even an expiry update mints a
// new placeholder generation the running agent would have to be re-seeded
// with, and the api-key mode has no re-seed loop. A variable so tests can
// shrink it.
var gatewayAPIKeyLifetime = 24 * time.Hour

// gatewayAPIKeyRunSlack is added to a run's own agent budget when sizing
// the api-key bound, for the pre-script, sandbox setup, validation and the
// post-script.
const gatewayAPIKeyRunSlack = 2 * time.Hour

// gatewayAPIKeyLifetimeFor sizes the api-key bound for a run whose agent
// budget is iterations of timeout each: at least gatewayAPIKeyLifetime,
// and longer when the run's own budget plus gatewayAPIKeyRunSlack is (a
// long local or self-hosted run must not outlive its credential).
func gatewayAPIKeyLifetimeFor(iterations int, timeout time.Duration) time.Duration {
	if iterations < 1 {
		iterations = 1
	}
	if need := time.Duration(iterations)*timeout + gatewayAPIKeyRunSlack; need > gatewayAPIKeyLifetime {
		return need
	}
	return gatewayAPIKeyLifetime
}

// ensureGatewayAPIKeyProviderFn creates the api-key mode's run-scoped
// provider (ensureGatewayAPIKeyProvider). Override in tests.
var ensureGatewayAPIKeyProviderFn = ensureGatewayAPIKeyProvider

// startGatewayAPIKeyRoute reads the api-key mode's credential
// (FULLSEND_INFERENCE_GATEWAY_API_KEY) and creates the run-scoped gateway
// provider carrying it. A missing key fails the run; there is no fallback
// to the oidc mode. The key is not rotated, so there is no re-seed: the
// placeholder the agent is seeded with stays valid for the run.
func startGatewayAPIKeyRoute(ctx context.Context, plan *gatewayRoutePlan, sandboxName string, printer *ui.Printer) (gatewayProviderHandle, error) {
	printer.StepStart("Reading the inference gateway API key")
	key, err := gatewayAPIKey()
	if err != nil {
		printer.StepFail("Inference gateway credential unavailable")
		return gatewayProviderHandle{}, err
	}
	printer.StepDone("Inference gateway API key ready (" + gatewayAPIKeyEnv + ")")
	printer.StepWarn("inference.gateway.auth is api-key: the route relies on a long-lived gateway API key; prefer auth: oidc when the gateway can validate forge OIDC tokens")
	lifetime := plan.apiKeyLifetime
	if lifetime <= 0 {
		lifetime = gatewayAPIKeyLifetime
	}
	printer.StepInfo(fmt.Sprintf("Inference gateway provider bounded at %s; a run that outlasts it fails closed", lifetime.Round(time.Minute)))
	expiresAt := time.Now().Add(lifetime)
	name, _, err := ensureGatewayAPIKeyProviderFn(ctx, plan.host, sandboxName, key, expiresAt, printer)
	if err != nil {
		return gatewayProviderHandle{}, err
	}
	return gatewayProviderHandle{
		name:      name,
		block:     plan.block,
		sandbox:   sandboxName,
		seed:      plan.seed,
		apiKey:    true,
		expiresAt: expiresAt,
		sandboxUp: &atomic.Bool{},
		state:     &gatewayRefreshState{expiresAt: expiresAt, heldExpiresAt: expiresAt},
	}, nil
}

// gatewayRefreshState is a gateway refresher's progress. It lives on the
// handle, so a refresher restarted around a remint resumes from the token
// the provider holds now rather than the one minted at start. Only one
// refresher per handle runs at a time (a restart follows stop-and-wait),
// so it needs no lock.
type gatewayRefreshState struct {
	// issuedAt/expiresAt are the provider's current token's iat and exp.
	issuedAt  time.Time
	expiresAt time.Time
	// heldExpiresAt is the exp of the token the running agent holds: the
	// provider's, unless a hand-off is pending.
	heldExpiresAt time.Time
	// placeholder is the generation the agent's token file names; "" until
	// the first refresh after the sandbox is up reads it.
	placeholder string
	// handOffPending is set when the provider holds a newer token than the
	// agent: the refresher then retries the hand-off alone.
	handOffPending bool
}

// refreshState returns the handle's refresh state, creating it from the
// handle's token when the handle carries none.
func (h *gatewayProviderHandle) refreshState() *gatewayRefreshState {
	if h.state == nil {
		h.state = &gatewayRefreshState{issuedAt: h.issuedAt, expiresAt: h.expiresAt, heldExpiresAt: h.expiresAt}
	}
	return h.state
}

// gatewayBaselineError is a refresh that could not read the placeholder
// the agent holds, so it rotated nothing.
type gatewayBaselineError struct{ err error }

func (e *gatewayBaselineError) Error() string {
	return "reading the placeholder the agent holds before rotating: " + e.err.Error()
}

func (e *gatewayBaselineError) Unwrap() error { return e.err }

// gatewayReseedError is a refresh whose provider update landed but whose
// hand-off to the running agent did not: the provider holds the new token
// while the agent still holds the previous placeholder.
type gatewayReseedError struct{ err error }

func (e *gatewayReseedError) Error() string {
	return "the provider holds the new token but the running agent was not re-seeded: " + e.err.Error()
}

func (e *gatewayReseedError) Unwrap() error { return e.err }

// rotateGatewayToken fetches a fresh assertion and hot-updates it into the
// provider with its own exp, retrying the fetch and update (and only
// those) up to gatewayRefreshRetries times. Each attempt is bounded by
// gatewayFetchTimeout, so the retries stay inside the fetch share of
// gatewayRefreshWork.
func rotateGatewayToken(ctx context.Context, h gatewayProviderHandle, printer *ui.Printer) (*actionsoidc.Assertion, error) {
	var lastErr error
	for attempt := 0; attempt <= gatewayRefreshRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(gatewayRefreshBackoff):
			}
		}
		a, err := rotateGatewayTokenOnce(ctx, h)
		if err == nil {
			return a, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		printer.StepWarn(fmt.Sprintf("Inference gateway token refresh attempt %d/%d failed: %v", attempt+1, gatewayRefreshRetries+1, err))
	}
	return nil, lastErr
}

// rotateGatewayTokenOnce is one fetch-and-update attempt.
func rotateGatewayTokenOnce(ctx context.Context, h gatewayProviderHandle) (*actionsoidc.Assertion, error) {
	updateCtx, cancel := context.WithTimeout(ctx, gatewayFetchTimeout)
	defer cancel()
	a, err := fetchGatewayToken(updateCtx, h.block)
	if err != nil {
		return nil, err
	}
	if err := validateGatewayAssertion(a.Value); err != nil {
		return nil, err
	}
	if !security.RegisterRuntimeSecret(a.Value) {
		return nil, errors.New("inference gateway: the refreshed token is too short to redact reliably; refusing to use it")
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", a.Value)
	}
	creds := map[string]string{gatewayCredentialKey: a.Value}
	if err := sandbox.UpdateProviderLiteralWithExpiry(updateCtx, h.name, creds, a.ExpiresAt); err != nil {
		return nil, err
	}
	return a, nil
}

// setProviderCredentialExpiryFn records a provider credential's expiry
// (sandbox.SetProviderCredentialExpiry). Override in tests to record the
// call without a gateway.
var setProviderCredentialExpiryFn = sandbox.SetProviderCredentialExpiry

// failGatewayClosed stops the route after a hand-off whose new generation
// never reached the sandbox. OpenShell's placeholder carries no evidence
// of which rotation a generation belongs to, so a later rotation could not
// tell a late generation from its own and might hand the agent a token
// whose expiry it does not know. Instead the provider's expiry is moved
// back to the token the agent holds, so placeholder resolution fails
// closed then, and the refresher stops.
func failGatewayClosed(h gatewayProviderHandle, st *gatewayRefreshState, cause error, printer *ui.Printer) {
	printer.StepWarn(fmt.Sprintf("Inference gateway token refresh for %s stopped: %v. The route fails closed at the expiry of the token the running agent holds, %s", h.name, cause, st.heldExpiresAt.UTC().Format(time.RFC3339)))
	if err := setProviderCredentialExpiryFn(context.Background(), h.name, gatewayCredentialKey, st.heldExpiresAt); err != nil {
		printer.StepWarn(fmt.Sprintf("Inference gateway provider %s: moving its expiry back to %s failed: %v", h.name, st.heldExpiresAt.UTC().Format(time.RFC3339), err))
		return
	}
	st.expiresAt = st.heldExpiresAt
}

// handOffGateway re-seeds the running agent's token file once the sandbox
// hands out a placeholder other than previous, the one the agent holds.
// It returns the placeholder the agent now holds.
func handOffGateway(ctx context.Context, h gatewayProviderHandle, previous string, printer *ui.Printer) (string, error) {
	settleCtx, cancel := context.WithTimeout(ctx, handOffTimeout())
	defer cancel()
	return reseedCredential(settleCtx, h.sandbox, "inference gateway", h.seed, previous, printer)
}

// refreshGatewayProvider rotates the provider's token (rotateGatewayToken)
// and, once the sandbox is up, hands the new placeholder to the running
// agent — once, since the settle wait alone takes most of a short token's
// refresh lead. placeholder is the generation the agent holds, "" when not
// yet known. It returns the new token's iat and exp and the placeholder
// the agent now holds. A failed baseline read rotates nothing and returns
// a *gatewayBaselineError; a failed hand-off returns the new iat and exp
// with a *gatewayReseedError and the placeholder the agent still holds.
func refreshGatewayProvider(ctx context.Context, h gatewayProviderHandle, placeholder string, printer *ui.Printer) (time.Time, time.Time, string, error) {
	reseed := h.sandboxReady()
	if reseed && placeholder == "" {
		baseCtx, cancel := context.WithTimeout(ctx, gatewayFetchTimeout)
		p, err := baselinePlaceholder(baseCtx, h.sandbox, h.seed.PlaceholderEnv)
		cancel()
		if err != nil {
			return time.Time{}, time.Time{}, "", &gatewayBaselineError{err: err}
		}
		placeholder = p
	}
	a, err := rotateGatewayToken(ctx, h, printer)
	if err != nil {
		return time.Time{}, time.Time{}, placeholder, err
	}
	if !reseed {
		return a.IssuedAt, a.ExpiresAt, placeholder, nil
	}
	seeded, err := handOffGateway(ctx, h, placeholder, printer)
	if err != nil {
		return a.IssuedAt, a.ExpiresAt, placeholder, &gatewayReseedError{err: err}
	}
	return a.IssuedAt, a.ExpiresAt, seeded, nil
}

// waitGateway waits d, or returns false when ctx ends first.
func waitGateway(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runGatewayRefresh keeps the gateway token valid for the life of the
// run, scheduling each refresh from the provider's current token's own iat
// and exp (gatewayRefreshDelay). Runs until ctx is cancelled.
//
//   - When the fetch retries are exhausted it stops and says so: the
//     provider's recorded expiry makes the proxy fail closed at that
//     instant, so the run fails visibly.
//   - When the placeholder the agent holds cannot be read, nothing is
//     rotated and the refresh is retried shortly, until the provider's
//     token expires.
//   - When the new generation is not observed in the sandbox (the settle
//     wait runs out, or reading the placeholder fails before it changes;
//     generationNotObservedError), the route fails closed (failGatewayClosed): the
//     provider's expiry moves back to the token the agent holds and the
//     refresher stops. OpenShell gives no per-rotation evidence, so a late
//     generation could not be told apart from a later rotation's own.
//   - When the hand-off fails after the new generation was observed (a
//     seed or verify exec), the
//     provider already holds the new token; only the hand-off is retried,
//     every gatewayRefreshBackoff, until the provider's next refresh is
//     due (which hands off again itself).
func runGatewayRefresh(ctx context.Context, h gatewayProviderHandle, printer *ui.Printer) {
	if h.apiKey {
		// The api-key mode rotates nothing and has no re-seed loop.
		return
	}
	st := h.refreshState()
	var warnedFor time.Time
	// warnedHeldExpiry keeps a hand-off that keeps failing from warning
	// on every retry: it warns once per held token, when that token expires.
	var warnedHeldExpiry time.Time
	for {
		delay, fits := gatewayRefreshDelay(st.issuedAt, st.expiresAt, time.Now(), rand.Float64())
		if st.handOffPending && delay > gatewayRefreshBackoff {
			if !waitGateway(ctx, gatewayRefreshBackoff) {
				return
			}
			// Each retry ends when the next rotation is due, so a slow
			// settle cannot hold the rotation past the provider's exp.
			retryCtx, cancel := context.WithTimeout(ctx, delay-gatewayRefreshBackoff)
			held, err := handOffGateway(retryCtx, h, st.placeholder, printer)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				var notObserved *generationNotObservedError
				if errors.As(err, &notObserved) {
					failGatewayClosed(h, st, err, printer)
					return
				}
				if !warnedHeldExpiry.Equal(st.heldExpiresAt) && !time.Now().Before(st.heldExpiresAt) {
					warnedHeldExpiry = st.heldExpiresAt
					printer.StepWarn(fmt.Sprintf("Inference gateway hand-off for %s still failing: %v; the running agent's token expired at %s, still retrying", h.name, err, st.heldExpiresAt.UTC().Format(time.RFC3339)))
				}
				continue
			}
			st.placeholder, st.heldExpiresAt, st.handOffPending = held, st.expiresAt, false
			printer.StepDone(fmt.Sprintf("Inference gateway token handed off for %s (next expiry in %s)", h.name, time.Until(st.expiresAt).Round(time.Second)))
			continue
		}
		if !fits && !warnedFor.Equal(st.expiresAt) {
			warnedFor = st.expiresAt
			budget := gatewayRefreshWork() + gatewayRefreshSafety
			if lifetime := st.expiresAt.Sub(st.issuedAt); lifetime < budget {
				printer.StepWarn(fmt.Sprintf("Inference gateway token lifetime %s leaves less than the %s refresh budget; a refresh may not land before it expires", lifetime.Round(time.Second), budget.Round(time.Second)))
			} else {
				// A long lifetime that still does not fit: the refresh is
				// running late (a deferred refresh, a long pre-script, a
				// suspended host).
				printer.StepWarn(fmt.Sprintf("Inference gateway token for %s expires at %s, inside the %s refresh budget; the refresh is running late", h.name, st.expiresAt.UTC().Format(time.RFC3339), budget.Round(time.Second)))
			}
		}
		if !waitGateway(ctx, delay) {
			return
		}
		// With a hand-off still pending the sandbox already hands out a
		// newer generation than the agent holds; an empty placeholder
		// makes the refresh read that one as its baseline, so the hand-off
		// waits for the generation this rotation creates, not the stale
		// one in between.
		previous := st.placeholder
		if st.handOffPending {
			previous = ""
		}
		iat, exp, held, err := refreshGatewayProvider(ctx, h, previous, printer)
		// A rotation that landed is recorded even when the refresher was
		// stopped meanwhile, so a restart resumes from it.
		var baselineErr *gatewayBaselineError
		var reseedErr *gatewayReseedError
		switch {
		case err != nil && !errors.As(err, &reseedErr) && ctx.Err() != nil:
			return
		case errors.As(err, &baselineErr):
			if !time.Now().Before(st.expiresAt) {
				printer.StepWarn(fmt.Sprintf("Inference gateway token refresh for %s gave up: %v; the provider's token expired at %s", h.name, err, st.expiresAt.UTC().Format(time.RFC3339)))
				return
			}
			printer.StepWarn(fmt.Sprintf("Inference gateway token refresh for %s deferred: %v; retrying", h.name, err))
		case errors.As(err, &reseedErr):
			st.issuedAt, st.expiresAt, st.placeholder, st.handOffPending = iat, exp, held, true
			if ctx.Err() != nil {
				return
			}
			var notObserved *generationNotObservedError
			if errors.As(err, &notObserved) {
				failGatewayClosed(h, st, reseedErr.err, printer)
				return
			}
			printer.StepWarn(fmt.Sprintf("Inference gateway token refreshed for %s, but %v; retrying the hand-off, and the running agent's token expires at %s", h.name, reseedErr.err, st.heldExpiresAt.UTC().Format(time.RFC3339)))
		case err != nil:
			printer.StepWarn(fmt.Sprintf("Inference gateway token refresh for %s gave up: %v; the running agent keeps the token it holds, which expires at %s", h.name, err, st.heldExpiresAt.UTC().Format(time.RFC3339)))
			return
		default:
			st.issuedAt, st.expiresAt, st.heldExpiresAt, st.placeholder, st.handOffPending = iat, exp, exp, held, false
			printer.StepDone(fmt.Sprintf("Inference gateway token refreshed for %s (next expiry in %s)", h.name, time.Until(exp).Round(time.Second)))
		}
	}
}

// startGatewayRefreshers launches one refresh goroutine per handle and
// returns one stop-and-wait func per handle (see startOpenAIRefreshers).
func startGatewayRefreshers(handles []gatewayProviderHandle, printer *ui.Printer) []func() {
	stops := make([]func(), 0, len(handles))
	for i := range handles {
		// The state is created here, on the caller's handle, so a restart
		// of the same handles resumes from it.
		handles[i].refreshState()
		handle := handles[i]
		refreshCtx, stopRefresh := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func(handle gatewayProviderHandle) {
			defer wg.Done()
			runGatewayRefresh(refreshCtx, handle, printer)
		}(handle)
		stops = append(stops, func() {
			stopRefresh()
			wg.Wait()
		})
	}
	return stops
}
