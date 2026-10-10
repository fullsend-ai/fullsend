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
}

// isDummyRuntime reports whether name is a test runtime the gateway
// provider is attached under when a block applies (behaviour tests,
// Tier A).
func isDummyRuntime(name string) bool { return name == "dummy" || name == "dummy-playback" }

// planGatewayRoute decides whether the runner owns the gateway route for
// this run and, when it does, registers the gateway run with the runtime.
// models are the gateway-relevant model specs the run resolves (the
// parent's and its configured children's); needsGateway is whether any of
// them is on the gateway provider by the runtime's own resolution.
//
// A gateway/ model on a runtime without the route is an error, and so is
// a partial block. With no block, or on a run with no forge OIDC endpoint,
// the runner adds nothing, so the harness-plugin setup in the local guide
// keeps working. Under the dummy runtime the provider is attached whenever
// a block applies, so behaviour tests can probe the route.
func planGatewayRoute(rc runConfig, backend runtime.Backend, sandboxName string, models []string, needsGateway bool) (*gatewayRoutePlan, error) {
	name := backend.Runtime.Name()
	if err := validateGatewayRuntime(name, models); err != nil {
		return nil, err
	}
	if rc.perRepo == nil {
		return nil, nil
	}
	block := rc.perRepo.ConfigInferenceGateway().Trimmed()
	applies, err := gatewayBlockApplies(block)
	if err != nil {
		return nil, err
	}
	if !applies {
		return nil, nil
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
	// issuedAt/expiresAt are the current token's iat and exp.
	issuedAt  time.Time
	expiresAt time.Time
	// sandboxUp is set once the sandbox is Ready: from then a refresh also
	// re-seeds the runtime's token file.
	sandboxUp *atomic.Bool
}

// sandboxReady reports whether a refresh has a running sandbox to re-seed.
func (h gatewayProviderHandle) sandboxReady() bool {
	return h.sandboxUp != nil && h.sandboxUp.Load() && h.sandbox != "" && !h.seed.IsZero()
}

// ensureGatewayProviderFn is ensureGatewayProvider; tests replace it.
var ensureGatewayProviderFn = ensureGatewayProvider

// startGatewayRoute fetches the job's OIDC assertion for the block's
// audience and creates the run-scoped gateway provider carrying it. A
// failure fails the run: the route never falls back to the openai
// provider, WIF or a static key.
func startGatewayRoute(ctx context.Context, plan *gatewayRoutePlan, sandboxName string, printer *ui.Printer) (gatewayProviderHandle, error) {
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
	}, nil
}

// refreshGatewayProvider fetches a fresh assertion, hot-updates it into
// the provider with its own exp, and, once the sandbox is up, re-seeds the
// runtime's token file with the new placeholder. It returns the new
// token's iat and exp and the placeholder the agent now holds.
func refreshGatewayProvider(ctx context.Context, h gatewayProviderHandle, placeholder string, printer *ui.Printer) (time.Time, time.Time, string, error) {
	reseed := h.sandboxReady()
	if reseed && placeholder == "" {
		baseCtx, cancel := context.WithTimeout(ctx, gatewayFetchTimeout)
		p, err := baselinePlaceholder(baseCtx, h.sandbox, h.seed.PlaceholderEnv)
		cancel()
		if err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("reading the placeholder the agent holds before rotating: %w", err)
		}
		placeholder = p
	}
	updateCtx, cancelUpdate := context.WithTimeout(ctx, gatewayFetchTimeout)
	defer cancelUpdate()
	a, err := fetchGatewayToken(updateCtx, h.block)
	if err != nil {
		return time.Time{}, time.Time{}, placeholder, err
	}
	if err := validateGatewayAssertion(a.Value); err != nil {
		return time.Time{}, time.Time{}, placeholder, err
	}
	if !security.RegisterRuntimeSecret(a.Value) {
		return time.Time{}, time.Time{}, placeholder, errors.New("inference gateway: the refreshed token is too short to redact reliably; refusing to use it")
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", a.Value)
	}
	creds := map[string]string{gatewayCredentialKey: a.Value}
	if err := sandbox.UpdateProviderLiteralWithExpiry(updateCtx, h.name, creds, a.ExpiresAt); err != nil {
		return time.Time{}, time.Time{}, placeholder, err
	}
	if !reseed {
		return a.IssuedAt, a.ExpiresAt, placeholder, nil
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, openAIPlaceholderSettle+3*openAIPlaceholderExecTimeout+openAIPlaceholderPoll)
	defer cancelSettle()
	seeded, err := reseedCredential(settleCtx, h.sandbox, "inference gateway", h.seed, placeholder, printer)
	if err != nil {
		return time.Time{}, time.Time{}, placeholder, fmt.Errorf("the provider holds the new token but the running agent was not re-seeded: %w", err)
	}
	return a.IssuedAt, a.ExpiresAt, seeded, nil
}

// runGatewayRefresh keeps the gateway token valid for the life of the
// run, scheduling each refresh from the current token's own iat and exp
// (gatewayRefreshDelay). When the retries are exhausted it stops and says
// so: the provider's recorded expiry makes the proxy fail closed at that
// instant, so the run fails visibly. Runs until ctx is cancelled.
func runGatewayRefresh(ctx context.Context, h gatewayProviderHandle, printer *ui.Printer) {
	iat, exp := h.issuedAt, h.expiresAt
	placeholder := ""
	for {
		delay, fits := gatewayRefreshDelay(iat, exp, time.Now(), rand.Float64())
		if !fits {
			printer.StepWarn(fmt.Sprintf("Inference gateway token lifetime %s leaves less than the %s refresh budget; a refresh may not land before it expires", exp.Sub(iat).Round(time.Second), (gatewayRefreshWork() + gatewayRefreshSafety).Round(time.Second)))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		var nextIat, nextExp time.Time
		var err error
		for attempt := 0; attempt <= gatewayRefreshRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(gatewayRefreshBackoff):
				}
			}
			nextIat, nextExp, placeholder, err = refreshGatewayProvider(ctx, h, placeholder, printer)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			printer.StepWarn(fmt.Sprintf("Inference gateway token refresh attempt %d/%d failed: %v", attempt+1, gatewayRefreshRetries+1, err))
		}
		if err != nil {
			printer.StepWarn(fmt.Sprintf("Inference gateway token refresh for %s gave up; the running agent keeps the token it holds, which expires at %s", h.name, exp.UTC().Format(time.RFC3339)))
			return
		}
		iat, exp = nextIat, nextExp
		printer.StepDone(fmt.Sprintf("Inference gateway token refreshed for %s (next expiry in %s)", h.name, time.Until(exp).Round(time.Second)))
	}
}

// startGatewayRefreshers launches one refresh goroutine per handle and
// returns one stop-and-wait func per handle (see startOpenAIRefreshers).
func startGatewayRefreshers(handles []gatewayProviderHandle, printer *ui.Printer) []func() {
	stops := make([]func(), 0, len(handles))
	for _, handle := range handles {
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
