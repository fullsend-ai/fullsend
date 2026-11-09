package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/randutil"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Jira in the sandbox with a service account's OAuth 2.0 client
// credentials (fullsend#9220). The builtin jira provider (profile
// fullsend-jira) normally expands a static JIRA_TOKEN like any provider.
// When that expands empty and the runner has FULLSEND_JIRA_CLIENT_ID and
// FULLSEND_JIRA_CLIENT_SECRET, the runner mints the access token itself,
// gives it to a run-scoped provider instance, and refreshes it before it
// expires, as ensureOpenAIProvider does for OpenAI (ADR 0092). The client
// secret stays in this process: it never reaches a provider value, the
// sandbox, or a log.
//
// A running process keeps the placeholder it started with, and OpenShell
// resolves that one to the generation it was issued for, so a refresh
// alone never reaches a running agent. The runner therefore keeps the
// current placeholder in sandbox.SandboxJiraTokenFile, which the Jira
// skills read per call (runtime.JiraTokenSeed).

// jiraProviderType is the provider profile id of the builtin jira provider
// (internal/scaffold/fullsend-repo/profiles/fullsend-jira.yaml).
const jiraProviderType = "fullsend-jira"

// jiraCredentialKey is the credential the provider carries and the env var
// its placeholder reaches the sandbox in.
const jiraCredentialKey = "JIRA_TOKEN"

// jiraAPIHost is the Atlassian API gateway, the only host a minted access
// token works on.
const jiraAPIHost = "api.atlassian.com"

// Test seams.
var (
	// jiraMintFn performs one client_credentials grant.
	jiraMintFn = func(ctx context.Context, creds jira.Credentials) (jira.AccessToken, error) {
		return jira.MintAccessToken(ctx, nil, "", creds)
	}
	ensureJiraProviderLiteralFn = sandbox.EnsureProviderLiteral
	updateJiraProviderFn        = sandbox.UpdateProviderLiteralWithExpiry
	// reseedJiraFn hands a new generation to the token file
	// (reseedCredential).
	reseedJiraFn = reseedCredential
	// seedJiraTokenFileFn writes the token file once the sandbox is up.
	seedJiraTokenFileFn = seedJiraTokenFile
)

// errJiraUnredactable is a refresh failure no retry can fix.
var errJiraUnredactable = errors.New("minted Jira access token is too short to redact reliably; refusing to use it")

// jiraMintCredentials decides how the jira provider pd gets its value. It
// returns mint=false when the definition's JIRA_TOKEN expands to a value
// (a static token, unchanged behaviour) or when no client credentials are
// set (the provider is created as it always was). It returns the client
// credentials and mint=true when JIRA_TOKEN expands empty and both client
// variables are set, and an error when only one of them is.
func jiraMintCredentials(pd harness.ProviderDef, getenv func(string) string) (jira.Credentials, bool, error) {
	v, declared := pd.Credentials[jiraCredentialKey]
	if !declared || !sandbox.ProviderCredentialEmpty(v) {
		return jira.Credentials{}, false, nil
	}
	creds := jira.Credentials{
		ClientID:     strings.TrimSpace(getenv(jira.ClientIDEnvVar)),
		ClientSecret: strings.TrimSpace(getenv(jira.ClientSecretEnvVar)),
	}
	switch {
	case creds.ClientID == "" && creds.ClientSecret == "":
		return jira.Credentials{}, false, nil
	case creds.ClientID == "" || creds.ClientSecret == "":
		return jira.Credentials{}, false, fmt.Errorf("provider %q: %s and %s must both be set to mint the sandbox's Jira access token", pd.Name, jira.ClientIDEnvVar, jira.ClientSecretEnvVar)
	}
	return creds, true, nil
}

// jiraSeedState is the placeholder the token file names, shared by every
// copy of a handle and by the refresher across restarts.
type jiraSeedState struct {
	mu          sync.Mutex
	placeholder string
}

func (s *jiraSeedState) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.placeholder
}

func (s *jiraSeedState) set(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.placeholder = p
}

// jiraProviderHandle describes a run-scoped jira provider instance created
// by ensureJiraProvider.
type jiraProviderHandle struct {
	name    string
	sandbox string
	// creds are the client credentials, held in memory so a refresh never
	// reads the environment.
	creds     jira.Credentials
	expiresAt time.Time
	// maskActions registers each minted token with the GitHub Actions log
	// mask as well as the process redactor.
	maskActions bool
	// sandboxUp is set once the sandbox is Ready and the token file has
	// been seeded: from then a refresh also re-seeds the file.
	sandboxUp  *atomic.Bool
	state      *jiraSeedState
	unrecorded *seedGenerations
}

// sandboxReady reports whether a refresh has a seeded token file to update.
func (h jiraProviderHandle) sandboxReady() bool {
	return h.sandboxUp != nil && h.sandboxUp.Load() && h.sandbox != "" && h.state != nil && h.state.get() != ""
}

// registerJiraToken adds a minted token to the process redactor (the
// authoritative control: the token is opaque, so no pattern can match it)
// and, in GitHub Actions, the log mask.
func registerJiraToken(value string, maskActions bool) bool {
	if !security.RegisterRuntimeSecret(value) {
		return false
	}
	if maskActions {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", value)
	}
	return true
}

// ensureJiraProvider mints the first access token and creates the
// run-scoped provider instance carrying it, with the token's expiry.
func ensureJiraProvider(ctx context.Context, pd harness.ProviderDef, sandboxName string, creds jira.Credentials, printer *ui.Printer) (jiraProviderHandle, error) {
	maskActions := os.Getenv("GITHUB_ACTIONS") == "true"
	// The client secret is redacted too, should any error ever quote it.
	security.RegisterRuntimeSecret(creds.ClientSecret)
	name := runScopedProviderName(pd.Name, sandboxName)
	printer.StepStart("Minting the Jira access token for provider: " + pd.Name)
	tok, err := jiraMintFn(ctx, creds)
	if err != nil {
		printer.StepFail("Jira access token unavailable for provider " + pd.Name)
		return jiraProviderHandle{}, fmt.Errorf("provider %q: %w", pd.Name, err)
	}
	if !registerJiraToken(tok.Value, maskActions) {
		printer.StepFail("Jira access token unusable for provider " + pd.Name)
		return jiraProviderHandle{}, fmt.Errorf("provider %q: %w", pd.Name, errJiraUnredactable)
	}
	printer.StepDone(fmt.Sprintf("Jira access token ready (OAuth 2.0 client credentials, lifetime %s)", time.Until(tok.ExpiresAt).Round(time.Minute)))
	var ignored []string
	for k := range pd.Credentials {
		if k != jiraCredentialKey {
			ignored = append(ignored, k)
		}
	}
	for k := range pd.Config {
		ignored = append(ignored, k)
	}
	if len(ignored) > 0 {
		sort.Strings(ignored)
		printer.StepWarn(fmt.Sprintf("provider %q: %s ignored; the run-scoped Jira provider carries only %s", pd.Name, strings.Join(ignored, ", "), jiraCredentialKey))
	}

	values := map[string]string{jiraCredentialKey: tok.Value}
	start := time.Now()
	printer.StepStart("Ensuring run-scoped provider: " + name)
	// OpenShell requires a declared credential's value on create and takes
	// an expiry only on update, so the value is stored first and its expiry
	// in the very next call.
	if err := ensureJiraProviderLiteralFn(ctx, name, jiraProviderType, values); err != nil {
		printer.StepFail("Failed to create run-scoped provider " + name)
		return jiraProviderHandle{}, fmt.Errorf("ensuring provider %q: %w", name, err)
	}
	if err := updateJiraProviderFn(ctx, name, values, tok.ExpiresAt); err != nil {
		printer.StepFail("Failed to store the credential on " + name)
		discardRunScopedProvider(name, []string{jiraCredentialKey}, printer)
		return jiraProviderHandle{}, fmt.Errorf("storing credential on provider %q: %w", name, err)
	}
	printer.StepDone(fmt.Sprintf("Provider ready: %s (minted, expires in %s, %.1fs)", name, time.Until(tok.ExpiresAt).Round(time.Minute), time.Since(start).Seconds()))
	return jiraProviderHandle{
		name:        name,
		sandbox:     sandboxName,
		creds:       creds,
		expiresAt:   tok.ExpiresAt,
		maskActions: maskActions,
		sandboxUp:   &atomic.Bool{},
		state:       &jiraSeedState{},
		unrecorded:  &seedGenerations{},
	}, nil
}

// discardRunScopedProvider removes a run-scoped provider whose creation did
// not complete (the caller registers no deferred delete for it); when the
// delete fails, its credentials are expired now instead.
func discardRunScopedProvider(name string, keys []string, printer *ui.Printer) {
	delErr := sandbox.DeleteProvider(name)
	if delErr == nil || errors.Is(delErr, sandbox.ErrProviderNotFound) {
		return
	}
	for _, k := range keys {
		if err := setProviderCredentialExpiryFn(context.Background(), name, k, time.Now()); err != nil {
			printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could be neither deleted (%v) nor expired (%v); remove it with `openshell provider delete %s`", name, delErr, err, name))
			return
		}
	}
	printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could not be deleted (%v); its credential was expired", name, delErr))
}

// seedJiraTokenFile writes the sandbox's current JIRA_TOKEN placeholder to
// the token file once the sandbox is up, before the agent starts, and
// records the placeholder the file names. The record comes from the file
// itself, not from a separate read of the environment, so a generation
// that changed between two execs cannot leave the record behind the file.
func seedJiraTokenFile(ctx context.Context, h jiraProviderHandle, printer *ui.Printer) error {
	seed := runtime.JiraTokenSeed()
	var stdout, stderr string
	var code int
	err := withSandboxLock(ctx, nil, func() error {
		var execErr error
		_, stderr, code, execErr = sandbox.ExecContext(ctx, h.sandbox, seed.Seed, openAIPlaceholderExecTimeout)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("seeding the Jira token file: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("seeding the Jira token file: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	stdout, stderr, code, err = sandbox.ExecContext(ctx, h.sandbox, "command -p cat "+shellQuote(seed.File), openAIPlaceholderExecTimeout)
	if err != nil {
		return fmt.Errorf("reading back the Jira token file: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("reading back the Jira token file: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	p := strings.TrimSpace(stdout)
	if p == "" {
		return fmt.Errorf("reading back the Jira token file: %s is empty", seed.File)
	}
	h.state.set(p)
	printer.StepDone("Jira token file seeded: " + seed.File)
	return nil
}

// refreshJiraProvider mints a new access token, stores it on the provider
// with its expiry, and, once the sandbox is up, hands the new placeholder
// to the token file. It returns the new expiry. The mint and the update are
// bounded by budget; the hand-off waits for the sandbox to observe the new
// generation and is bounded by handOffTimeout instead.
func refreshJiraProvider(ctx context.Context, h jiraProviderHandle, budget time.Duration, printer *ui.Printer) (time.Time, error) {
	updateCtx, cancelUpdate := context.WithTimeout(ctx, budget)
	defer cancelUpdate()
	reseed := h.sandboxReady()
	tok, err := jiraMintFn(updateCtx, h.creds)
	if err != nil {
		return time.Time{}, err
	}
	if !registerJiraToken(tok.Value, h.maskActions) {
		return time.Time{}, errJiraUnredactable
	}
	// One update carries the value and its expiry, so the new token never
	// sits behind the old expiry between two calls.
	if err := updateJiraProviderFn(updateCtx, h.name, map[string]string{jiraCredentialKey: tok.Value}, tok.ExpiresAt); err != nil {
		return time.Time{}, err
	}
	if !reseed {
		// The agent has not started: the token file is seeded at sandbox
		// start from whatever placeholder the sandbox carries then.
		return tok.ExpiresAt, nil
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, handOffTimeout())
	defer cancelSettle()
	seeded, err := reseedJiraFn(settleCtx, h.sandbox, "Jira", runtime.JiraTokenSeed(), h.state.get(), h.unrecorded, nil, printer)
	if err != nil {
		return time.Time{}, fmt.Errorf("the provider holds the new token but the sandbox's Jira token file was not re-seeded: %w", err)
	}
	h.state.set(seeded)
	return tok.ExpiresAt, nil
}

// failJiraClosed stops the Jira refresher. The provider's expiry moves
// back to heldExpiresAt, the expiry of the token the token file names, so
// a generation handed out after this point stops resolving then; each
// earlier generation keeps the expiry of its own token, and Atlassian
// refuses every minted token once it expires.
func failJiraClosed(h jiraProviderHandle, heldExpiresAt time.Time, cause error, printer *ui.Printer) {
	printer.StepWarn(fmt.Sprintf("Jira access token refresh for %s stopped: %v. Jira access from the sandbox ends when the token the agent holds expires, %s", h.name, cause, heldExpiresAt.UTC().Format(time.RFC3339)))
	if err := setProviderCredentialExpiryFn(context.Background(), h.name, jiraCredentialKey, heldExpiresAt); err != nil {
		printer.StepWarn(fmt.Sprintf("Jira provider %s: moving its expiry back to %s failed: %v", h.name, heldExpiresAt.UTC().Format(time.RFC3339), err))
	}
}

// runJiraRefresh keeps a run-scoped jira provider's token valid for the
// life of the run, on the same schedule and retry policy as
// runOpenAIRefresh: shortly before each expiry it mints a new token with
// bounded retries, and after an exhausted round it waits
// (openAIRefreshRoundWait) and tries again until one succeeds or the run
// ends. A token too short to redact, a new generation that never reached
// the sandbox, or too many unrecorded generations stop it through
// failJiraClosed. Otherwise it runs until ctx is cancelled.
func runJiraRefresh(ctx context.Context, h jiraProviderHandle, printer *ui.Printer) {
	expiresAt := h.expiresAt.Round(0)
	for {
		jitter := time.Duration(0)
		if openAIRefreshJitter > 0 {
			jitter = time.Duration(randutil.Int64N(int64(openAIRefreshJitter)))
		}
		now := wallNowFn()
		delay := openAIRefreshDelay(expiresAt, now, jitter)
		if !waitWallClock(ctx, delay, now.Add(delay)) {
			return
		}
		var next time.Time
		failedRounds := 0
		for {
			var err error
			for attempt := 0; attempt < openAIRefreshRetries; attempt++ {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(openAIRefreshBackoff):
					}
				}
				// See runOpenAIRefresh: the unrecorded set cannot be
				// trimmed safely, so stop before it grows past the cap.
				if n := h.unrecorded.len(); n >= openAIUnrecordedMax {
					failJiraClosed(h, expiresAt, fmt.Errorf("%d new generations were seen in the sandbox but never handed off (limit %d)", n, openAIUnrecordedMax), printer)
					return
				}
				budget := expiresAt.Sub(wallNowFn())
				if budget < time.Minute {
					budget = time.Minute
				}
				next, err = refreshJiraProvider(ctx, h, budget, printer)
				if err == nil {
					break
				}
				if ctx.Err() != nil {
					return
				}
				var notObserved *generationNotObservedError
				if errors.As(err, &notObserved) || errors.Is(err, errJiraUnredactable) {
					failJiraClosed(h, expiresAt, err, printer)
					return
				}
				printer.StepWarn(fmt.Sprintf("Jira access token refresh attempt %d/%d failed: %v", attempt+1, openAIRefreshRetries, err))
			}
			if err == nil {
				break
			}
			failedRounds++
			now := wallNowFn()
			wait := openAIRefreshRoundWait(failedRounds, expiresAt, now)
			printer.StepWarn(fmt.Sprintf("Jira access token refresh for %s failed %d attempts; retrying in %s (the token the agent holds expires %s)", h.name, openAIRefreshRetries, wait, expiresAt.UTC().Format(time.RFC3339)))
			if !waitWallClock(ctx, wait, now.Add(wait)) {
				return
			}
		}
		expiresAt = next.Round(0)
		printer.StepDone(fmt.Sprintf("Jira access token refreshed for %s (next expiry in %s)", h.name, expiresAt.Sub(wallNowFn()).Round(time.Minute)))
	}
}

// startJiraRefreshers launches one refresh goroutine per handle and
// returns one stop-and-wait func per handle (see startOpenAIRefreshers).
func startJiraRefreshers(handles []jiraProviderHandle, printer *ui.Printer) []func() {
	stops := make([]func(), 0, len(handles))
	for _, handle := range handles {
		refreshCtx, stopRefresh := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func(handle jiraProviderHandle) {
			defer wg.Done()
			runJiraRefresh(refreshCtx, handle, printer)
		}(handle)
		stops = append(stops, func() {
			stopRefresh()
			wg.Wait()
		})
	}
	return stops
}

// checkJiraEgressInspected fails when the sandbox policy admits the
// Atlassian API gateway over a route the proxy cannot inspect: OpenShell
// would not inject the minted token there.
func checkJiraEgressInspected(ctx context.Context, sandboxName string) error {
	return checkEgressInspected(ctx, sandboxName, jiraAPIHost, "Jira", "Make that endpoint `protocol: rest`, as the fullsend-jira profile does")
}
