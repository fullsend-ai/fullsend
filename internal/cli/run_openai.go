package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"

	"gopkg.in/yaml.v3"
)

// OpenAI on pi (#6689, ADR 0092): the credential is a short-lived access
// token the runner obtains itself — through OpenAI Workload Identity
// Federation from the job's GitHub OIDC token, or from OPENAI_API_KEY in
// the runner environment for local runs — and hands to a run-scoped
// OpenShell provider. The sandbox only ever sees the gateway's placeholder.

// openAIProviderType is the provider profile id that selects this path
// (internal/scaffold/fullsend-repo/profiles/fullsend-openai.yaml).
const openAIProviderType = "fullsend-openai"

// openAIDefaultCredentialKey is used when the provider definition declares
// no credential keys at all.
const openAIDefaultCredentialKey = "OPENAI_API_KEY"

// Runner environment variables for the WIF path. All three are non-secret
// identifiers; they are also in oidcDenyKeys so they never reach the
// sandbox or user scripts.
const (
	openAIAudienceEnv           = "FULLSEND_OPENAI_AUDIENCE"
	openAIIdentityProviderIDEnv = "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID"
	openAIServiceAccountIDEnv   = "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"
	openAIStaticKeyEnv          = "OPENAI_API_KEY"
)

// openAIExchange is the WIF exchange; tests substitute it.
var openAIExchange = openaiwif.Exchange

// openAIStaticKeyLifetime bounds a provider instance backed by a static
// OPENAI_API_KEY. The key itself does not expire, but the run-scoped
// provider must: if the runner dies before the deferred delete, the gateway
// fails placeholder resolution closed after this long instead of serving the
// key indefinitely. The refresher extends it while the run is alive.
const openAIStaticKeyLifetime = time.Hour

// Refresh schedule (#6464 Track D). A WIF token lives at most an hour and
// never longer than the GitHub assertion it came from — minutes in
// practice — and OpenAI issues no refresh token, so the runner re-exchanges
// a fresh assertion before expiry and updates the run-scoped provider; a
// static key only needs its provider expiry pushed out. The running agent
// process follows every update because the runner re-seeds the credential
// file that process re-reads per request — the file and the shell fragment
// that writes it come from the selected backend
// (runtime.OpenAICredentialSeeder), so this path is not pi-specific.
// Variables, not constants, so tests can shrink them.
var (
	// openAIRefreshMargin is how long before expiry a refresh is attempted,
	// capped at half the remaining lifetime for short-lived tokens.
	openAIRefreshMargin = 5 * time.Minute
	// openAIRefreshJitter spreads concurrent runs' refreshes apart.
	openAIRefreshJitter = time.Minute
	// openAIRefreshMinDelay keeps a short-lived token from spinning the loop.
	openAIRefreshMinDelay = 30 * time.Second
	// openAIDeleteRetries/Backoff wait out the gateway releasing a deleted
	// sandbox's reference to the provider at cleanup (OpenShell 0.0.115 still
	// reports the provider attached for a while after `sandbox delete`
	// returns; 18 s was measured as not enough, so allow a minute).
	openAIDeleteRetries  = 20
	openAIRefreshRetries = 3
	openAIRefreshBackoff = 15 * time.Second
)

// openAIProviderHandle describes a run-scoped provider instance created by
// ensureOpenAIProvider: what to refresh and what to clean up.
type openAIProviderHandle struct {
	name      string
	keys      []string
	source    string
	expiresAt time.Time
	// sandbox is the run's sandbox name; authSeed is the shell fragment
	// that writes the sandbox's current OPENAI_API_KEY placeholder into the
	// selected runtime's credential file (runtime.OpenAICredentialSeeder),
	// re-run after a refresh. Empty when the backend has no seeder: the
	// provider is still created and refreshed, nothing is re-seeded.
	sandbox  string
	authSeed string
	// ids is the committed inference.openai block, the fallback when the
	// FULLSEND_OPENAI_* variables are unset (also used by refresh).
	ids config.OpenAIWIFConfig
	// sandboxUp is set by the run once the sandbox is Ready: before that a
	// refresh only updates the provider (the agent has not started, so its
	// launch seed will pick up the current placeholder); after it a refresh
	// must also re-seed the runtime's credential file.
	sandboxUp *atomic.Bool
	// authFile is that credential file inside the sandbox
	// (runtime.OpenAICredentialSeeder.OpenAIAuthFile), checked after a
	// re-seed.
	authFile string
}

// sandboxReady reports whether a refresh has a running sandbox to re-seed.
func (h openAIProviderHandle) sandboxReady() bool {
	return h.sandboxUp != nil && h.sandboxUp.Load() && h.sandbox != "" && h.authSeed != ""
}

// openAIPlaceholderSettle bounds how long a refresh waits for the sandbox
// to observe the new credential generation (a fresh `sandbox exec`
// environment carries the new placeholder; ~20s was measured on OpenShell
// 0.0.115) before re-seeding the runtime's credential file with whatever it
// holds.
var openAIPlaceholderSettle = 90 * time.Second

// openAIPlaceholderPoll is the interval between those checks.
var openAIPlaceholderPoll = 5 * time.Second

// openAIPlaceholderExecTimeout bounds each `sandbox exec` the refresh runs.
const openAIPlaceholderExecTimeout = 30 * time.Second

// openAIBaselineAttempts is how many times the placeholder the agent
// currently holds is read before a rotation; without it the settle wait
// below could not tell a new generation from the old one.
const openAIBaselineAttempts = 3

// openAIDeleteBackoff is the wait between cleanup delete attempts while the
// gateway still lists the deleted sandbox as attached (see openAIDeleteRetries).
var openAIDeleteBackoff = 3 * time.Second

// sandboxOpenAIPlaceholder returns the OPENAI_API_KEY placeholder a new
// process in the sandbox receives right now.
func sandboxOpenAIPlaceholder(ctx context.Context, sandboxName string) (string, error) {
	// ExecContext already wraps the command in `sh -c`. Held under the
	// sandbox lock so the between-iteration sweep never kills this exec.
	var out, stderr string
	var code int
	err := withSandboxLock(ctx, nil, func() error {
		var execErr error
		out, stderr, code, execErr = sandbox.ExecContext(ctx, sandboxName, `printf %s "${OPENAI_API_KEY:-}"`, openAIPlaceholderExecTimeout)
		return execErr
	})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("reading the sandbox placeholder: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(out), nil
}

// baselineOpenAIPlaceholder reads the placeholder the agent currently
// holds, with bounded retries; it runs before the provider is rotated.
func baselineOpenAIPlaceholder(ctx context.Context, sandboxName string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < openAIBaselineAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(openAIPlaceholderPoll):
			}
		}
		p, err := sandboxOpenAIPlaceholder(ctx, sandboxName)
		if err == nil && p != "" {
			return p, nil
		}
		if err == nil {
			err = errors.New("the sandbox environment has no OPENAI_API_KEY placeholder")
		}
		lastErr = err
	}
	return "", lastErr
}

// reseedOpenAIAuth waits until the sandbox hands new processes a placeholder
// other than previous (the generation the runtime's credential file names),
// then re-runs the backend's seed fragment so the agent's next request
// carries the refreshed credential, and returns the placeholder that was
// seeded. previous must be known: with no baseline the wait could not tell
// the new generation from the old one. A settle timeout is an error —
// re-seeding the old placeholder would tell the refresher the rotation
// reached the agent when it did not — and so is a re-seed whose result
// cannot be verified after two attempts: the caller then keeps the old
// placeholder and retries, rather than recording a generation the agent
// may not hold.
func reseedOpenAIAuth(ctx context.Context, h openAIProviderHandle, previous string, printer *ui.Printer) (string, error) {
	if previous == "" {
		return "", errors.New("re-seeding the OpenAI credential file: the placeholder the agent currently holds is unknown")
	}
	deadline := time.Now().Add(openAIPlaceholderSettle)
	var current string
	for {
		p, err := sandboxOpenAIPlaceholder(ctx, h.sandbox)
		if err != nil {
			return "", err
		}
		if p != "" && p != previous {
			current = p
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the sandbox still hands out the previous OpenAI placeholder after %s; the agent keeps the generation it holds", openAIPlaceholderSettle)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(openAIPlaceholderPoll):
		}
	}
	// Seed, then confirm the file names the new generation: an iteration
	// starting at this very moment seeds too (from its own exec
	// environment, which may still carry the previous placeholder), and
	// whichever write lands last wins. One re-seed closes that window.
	// Only the write is taken under the sandbox lock, and one exec at a
	// time: the seed writes atomically (mv -f) but the between-iteration
	// sweep must not kill it mid-run, whereas the grep below only reads and
	// costs nothing if it is killed. Each hold is one exec (30s + slack), so
	// a sweep never waits on the whole retry loop — see sandboxMu's
	// hold budget.
	// lastErr carries the reason the last verification failed: a re-seed
	// whose result cannot be confirmed must not report success, or the
	// refresher records the new placeholder while the file may still name
	// the old one — and the next settle wait would then compare against a
	// generation the agent never held.
	seed := func() error {
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			var stderr string
			var code int
			err := withSandboxLock(ctx, nil, func() error {
				var execErr error
				_, stderr, code, execErr = sandbox.ExecContext(ctx, h.sandbox, h.authSeed, openAIPlaceholderExecTimeout)
				return execErr
			})
			if err != nil {
				return fmt.Errorf("re-seeding the OpenAI credential file: %w", err)
			}
			if code != 0 {
				return fmt.Errorf("re-seeding the OpenAI credential file: exit %d: %s", code, strings.TrimSpace(stderr))
			}
			if h.authFile == "" {
				return nil
			}
			_, _, code, err = sandbox.ExecContext(ctx, h.sandbox, "command -p grep -qF "+shellQuote(current)+" "+shellQuote(h.authFile), openAIPlaceholderExecTimeout)
			if err == nil && code == 0 {
				return nil
			}
			if err != nil {
				lastErr = fmt.Errorf("verifying the re-seeded OpenAI credential file: %w", err)
				continue
			}
			lastErr = fmt.Errorf("verifying the re-seeded OpenAI credential file: %s does not name the refreshed placeholder (grep exit %d)", h.authFile, code)
		}
		return lastErr
	}
	if err := seed(); err != nil {
		return "", err
	}
	printer.StepInfo("the runtime's OpenAI credential file was re-seeded with the refreshed placeholder")
	return current, nil
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// openAICredential is a resolved OpenAI credential and where it came from.
type openAICredential struct {
	value string
	// expiresAt is zero for a static key until ensureOpenAIProvider bounds it.
	expiresAt time.Time
	// source is "wif" or "static"; detail is a printable, secret-free
	// description for the run log; warnings are printed with it.
	source   string
	detail   string
	warnings []string
}

// openAIAllowedScopes are the WIF permissions an agent run may hold. ADR
// 0092's accepted residual (a static placeholder is not endpoint-bound)
// assumes the token can do nothing but model requests, so a mapping that
// grants anything else is refused; a mapping that narrows nothing (empty
// scope) is allowed with a warning, because OpenAI returns no scope at all
// in that case and the run cannot tell what the service account holds.
var openAIAllowedScopes = map[string]bool{
	"api.model.request": true,
	"api.model.read":    true,
}

// checkOpenAIScope validates the scope string of an exchanged token.
func checkOpenAIScope(scope string) (warning string, err error) {
	fields := strings.Fields(scope)
	if len(fields) == 0 {
		return "the service-account mapping does not narrow permissions (no scope in the exchange response); grant api.model.request only on the mapping so the token can do nothing but model requests", nil
	}
	var extra []string
	for _, f := range fields {
		if !openAIAllowedScopes[f] {
			extra = append(extra, f)
		}
	}
	if len(extra) > 0 {
		return "", fmt.Errorf("the service-account mapping grants %s; an agent run may hold only api.model.request (and api.model.read) — narrow the mapping's permissions", strings.Join(extra, ", "))
	}
	return "", nil
}

// resolveOpenAICredential picks the credential source for a fullsend-openai
// provider, in order:
//
//  1. WIF — all three FULLSEND_OPENAI_* ids present: exchange the job's
//     GitHub OIDC token (ACTIONS_ID_TOKEN_REQUEST_URL/_TOKEN) for an OpenAI
//     access token.
//  2. Static — OPENAI_API_KEY present in the runner environment (local runs).
//  3. Neither — an error naming the variables, before any gateway work.
//
// A partially configured WIF trio is an error rather than a silent fall
// through to a static key, so a typo in one variable cannot switch the run
// onto a different credential.
func resolveOpenAICredential(ctx context.Context, getenv func(string) string, fromConfig config.OpenAIWIFConfig) (openAICredential, error) {
	audience := strings.TrimSpace(getenv(openAIAudienceEnv))
	identityProviderID := strings.TrimSpace(getenv(openAIIdentityProviderIDEnv))
	serviceAccountID := strings.TrimSpace(getenv(openAIServiceAccountIDEnv))
	idSource := "variables"

	// The runner variables win when any of them is set; otherwise the
	// committed inference.openai block supplies the trio. The two sources
	// are never merged: a complete trio comes from one place, so a
	// partially set source is an error rather than a silent fallback.
	fromConfig = fromConfig.Trimmed()
	// A committed block applies where an exchange is possible (a GitHub
	// OIDC endpoint) or where nothing else is available; a developer's
	// OPENAI_API_KEY on a laptop is not overridden by the repository's
	// CI configuration.
	configApplies := !fromConfig.IsZero() && (getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "" || getenv(openAIStaticKeyEnv) == "")
	configIgnored := !fromConfig.IsZero() && !configApplies
	if audience == "" && identityProviderID == "" && serviceAccountID == "" && configApplies {
		if missing := fromConfig.Missing(); len(missing) > 0 {
			return openAICredential{}, fmt.Errorf("inference.openai in config.yaml is partially configured: missing %s", strings.Join(missing, ", "))
		}
		audience, identityProviderID, serviceAccountID = strings.TrimSpace(fromConfig.Audience), strings.TrimSpace(fromConfig.IdentityProviderID), strings.TrimSpace(fromConfig.ServiceAccountID)
		idSource = "config.yaml"
	}

	if audience != "" || identityProviderID != "" || serviceAccountID != "" {
		var missing []string
		for _, kv := range []struct{ k, v string }{
			{openAIAudienceEnv, audience},
			{openAIIdentityProviderIDEnv, identityProviderID},
			{openAIServiceAccountIDEnv, serviceAccountID},
		} {
			if kv.v == "" {
				missing = append(missing, kv.k)
			}
		}
		if len(missing) > 0 {
			return openAICredential{}, fmt.Errorf("OpenAI WIF is partially configured: missing %s", strings.Join(missing, ", "))
		}
		oidcURL := getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
		oidcToken := getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
		if oidcURL == "" || oidcToken == "" {
			return openAICredential{}, fmt.Errorf("OpenAI WIF is configured but the job has no GitHub OIDC endpoint (ACTIONS_ID_TOKEN_REQUEST_URL/ACTIONS_ID_TOKEN_REQUEST_TOKEN unset): the exchange is GitHub Actions only — grant the workflow `permissions: id-token: write`; on GitLab CI or a local run use OPENAI_API_KEY instead")
		}
		tok, err := openAIExchange(ctx, openaiwif.Config{
			Audience:           audience,
			IdentityProviderID: identityProviderID,
			ServiceAccountID:   serviceAccountID,
			OIDCRequestURL:     oidcURL,
			OIDCRequestToken:   oidcToken,
		})
		if err != nil {
			return openAICredential{}, fmt.Errorf("OpenAI WIF exchange failed: %w", err)
		}
		warning, err := checkOpenAIScope(tok.Scope)
		if err != nil {
			return openAICredential{}, fmt.Errorf("OpenAI WIF token refused: %w", err)
		}
		scope := tok.Scope
		if scope == "" {
			scope = "(not narrowed)"
		}
		cred := openAICredential{
			value:     tok.Value,
			expiresAt: tok.ExpiresAt,
			source:    "wif",
			detail: fmt.Sprintf("WIF: identity provider %s, service account %s, audience %s (from %s), expires in %s, scope %s",
				identityProviderID, serviceAccountID, audience, idSource, time.Until(tok.ExpiresAt).Round(time.Second), scope),
		}
		if warning != "" {
			cred.warnings = append(cred.warnings, warning)
		}
		return cred, nil
	}

	if key := getenv(openAIStaticKeyEnv); key != "" {
		detail := openAIStaticKeyEnv + " from the runner environment"
		if configIgnored {
			detail += " (inference.openai in config.yaml not used: no GitHub OIDC endpoint here)"
		}
		return openAICredential{
			value:  key,
			source: "static",
			detail: detail,
		}, nil
	}

	return openAICredential{}, fmt.Errorf("no OpenAI credential: set %s, %s and %s (or inference.openai in config.yaml) for Workload Identity Federation (the job needs `permissions: id-token: write`), or %s in the runner environment for a local run",
		openAIAudienceEnv, openAIIdentityProviderIDEnv, openAIServiceAccountIDEnv, openAIStaticKeyEnv)
}

// runScopedProviderName derives the provider instance name for this run
// from the harness provider name and the sandbox name's hash suffix, e.g.
// "openai-3f9c2a7b1d0e". Two concurrent runs on one gateway therefore never
// share (or overwrite) a provider instance.
func runScopedProviderName(base, sandboxName string) string {
	const suffixLen = 12
	suffix := sandboxName
	if i := strings.LastIndex(sandboxName, "-"); i >= 0 {
		suffix = sandboxName[i+1:]
	}
	if len(suffix) > suffixLen {
		suffix = suffix[:suffixLen]
	}
	return base + "-" + suffix
}

// applyRunScopedProviderNames substitutes run-scoped instance names into
// the list of providers the sandbox attaches, keeping order.
func applyRunScopedProviderNames(names []string, scoped map[string]string) []string {
	if len(scoped) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if s, ok := scoped[n]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, n)
	}
	return out
}

// dropSkippedProviders removes the harness provider names the run decided
// not to materialize (see runtime.NeedsOpenAIProvider) from the list the
// sandbox is created with. applyRunScopedProviderNames only substitutes
// names it created an instance for, so without this a skipped entry would
// still be attached to the sandbox under its bare harness name.
func dropSkippedProviders(names []string, skipped map[string]struct{}) []string {
	if len(skipped) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := skipped[n]; ok {
			continue
		}
		out = append(out, n)
	}
	return out
}

// ensureOpenAIProfile imports the provider profile for a fullsend-openai
// provider from the scaffold embedded in this binary. The profile is not
// layered into .fullsend/profiles at run time — importing that directory
// wholesale would replace the canonical profiles the fleet resolves from
// fullsend-ai/agents — and a repository install ships only a .gitkeep, so
// the runner brings its own, version-matched copy. ImportProfile is a
// delete-and-reimport with a per-id lock and content cache, so a profile a
// running sandbox still references stays in place and an unchanged one is
// not re-sent.
func ensureOpenAIProfile(ctx context.Context, profileID string, printer *ui.Printer) error {
	data, err := scaffold.FullsendRepoFile("profiles/" + profileID + ".yaml")
	if err != nil {
		return fmt.Errorf("provider profile %q is not shipped by this fullsend build: %w", profileID, err)
	}
	tmp, err := os.CreateTemp("", profileID+"-*.yaml")
	if err != nil {
		return fmt.Errorf("writing provider profile %q: %w", profileID, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing provider profile %q: %w", profileID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing provider profile %q: %w", profileID, err)
	}
	start := time.Now()
	printer.StepStart("Importing provider profile: " + profileID)
	// Never trust ImportProfile's content cache for this profile: the cache
	// says whether *these bytes* were sent once, not what the gateway holds
	// now (a same-id profile imported earlier in this run, or another
	// gateway entirely). Re-send every run; it is one small import.
	sandbox.ForgetProfileCache(profileID)
	if err := sandbox.ImportProfile(ctx, profileID, tmp.Name()); err != nil {
		printer.StepFail("Failed to import provider profile " + profileID)
		return fmt.Errorf("importing provider profile %q: %w", profileID, err)
	}
	// ImportProfile's content cache can outlive the gateway it was written
	// against (a recreated gateway, a cache from another machine's run);
	// the provider create below would then fail with "unsupported provider
	// type or profile". Confirm the gateway lists it and re-send otherwise.
	present, err := sandbox.ProfileExists(ctx, profileID)
	if err != nil {
		printer.StepFail("Failed to list provider profiles")
		return fmt.Errorf("checking provider profile %q: %w", profileID, err)
	}
	if !present {
		sandbox.ForgetProfileCache(profileID)
		if err := sandbox.ImportProfile(ctx, profileID, tmp.Name()); err != nil {
			printer.StepFail("Failed to import provider profile " + profileID)
			return fmt.Errorf("importing provider profile %q: %w", profileID, err)
		}
		if present, err = sandbox.ProfileExists(ctx, profileID); err != nil || !present {
			printer.StepFail("Provider profile missing after import: " + profileID)
			return fmt.Errorf("provider profile %q is not on the gateway after import (err=%v)", profileID, err)
		}
	}
	printer.StepDone(fmt.Sprintf("Provider profile ready: %s (%.1fs)", profileID, time.Since(start).Seconds()))
	return nil
}

// emptyCredentialRefusedRe matches the gateway's rejection of an empty
// credential value on create — today "provider.credentials must not be
// empty" (only for an empty map); a future value-level check is expected
// to keep the words. Anything else is a real failure, not a cue to send the
// value without its expiry.
var emptyCredentialRefusedRe = regexp.MustCompile(`(?i)credential[^\n]*(empty|required|missing|invalid)`)

func emptyCredentialRefused(err error) bool {
	return err != nil && emptyCredentialRefusedRe.MatchString(err.Error())
}

// openAICredentialKeys returns the credential keys the run-scoped provider
// carries. The profile declares exactly one, OPENAI_API_KEY, and that is all
// a definition may ask for: the token is never copied under another name
// (a definition naming LD_PRELOAD or PATH as a credential key would
// otherwise put it into the openshell child environment under that name).
// Extra keys in the definition are reported and ignored.
func openAICredentialKeys(pd harness.ProviderDef) ([]string, []string) {
	var ignored []string
	for k := range pd.Credentials {
		if k != openAIDefaultCredentialKey {
			ignored = append(ignored, k)
		}
	}
	sort.Strings(ignored)
	return []string{openAIDefaultCredentialKey}, ignored
}

// ensureOpenAIProvider resolves the credential, registers it for redaction,
// and creates the run-scoped provider instance for a fullsend-openai
// harness provider. It returns the instance name the sandbox must attach
// and the credential keys it carries (for cleanupRunScopedProvider).
//
// The credential value is the same for every key the definition declares
// (normally just OPENAI_API_KEY); literal or ${VAR} values in the
// definition are ignored on purpose — the value is supplied in process.
// openAIAPIHost is the only host the fullsend-openai profile lets the token
// reach; the egress preflight below checks the sandbox policy for it.
const openAIAPIHost = "api.openai.com"

// policyEndpointRule is the subset of a sandbox policy endpoint the egress
// preflight reads. Unknown fields are ignored.
type policyEndpointRule struct {
	Host                        string `yaml:"host"`
	Port                        int    `yaml:"port"`
	Ports                       []int  `yaml:"ports"`
	Protocol                    string `yaml:"protocol"`
	TLS                         string `yaml:"tls"`
	AllowUninspectedCredentials bool   `yaml:"allow_uninspected_credentials"`
}

type policyNetworkRule struct {
	Endpoints []policyEndpointRule `yaml:"endpoints"`
}

// uninspectedEndpointRules returns, sorted, the names of the network_policies
// rules in a sandbox policy that admit host:port over a route the OpenShell
// proxy cannot inspect: no `protocol` (an L4 tunnel) or `tls: skip`, and no
// `allow_uninspected_credentials` opt-in. Since OpenShell 0.0.110 the proxy
// refuses to carry a provider credential over such a route — the sandbox
// only ever sees "credentialed endpoint requires L7 inspection; raw tunnel
// is not explicitly allowed" and the agent sees connection errors — and
// the base sandbox image's default policy ships exactly that shape for
// api.openai.com in its `codex` rule (verified on 0.0.115).
func uninspectedEndpointRules(policy []byte, host string, port int) ([]string, error) {
	var doc struct {
		NetworkPolicies map[string]policyNetworkRule `yaml:"network_policies"`
	}
	if err := yaml.Unmarshal(policy, &doc); err != nil {
		return nil, fmt.Errorf("parsing sandbox policy: %w", err)
	}
	var rules []string
	for name, rule := range doc.NetworkPolicies {
		for _, ep := range rule.Endpoints {
			if !policyHostMatches(ep.Host, host) || !policyPortMatches(ep, port) {
				continue
			}
			// No protocol or `protocol: tcp` is an L4 tunnel; `tls: skip`
			// disables termination. `allow_uninspected_credentials` only
			// stops OpenShell from refusing such a route — the credential is
			// still not injected on it, so it is no better for this run.
			proto := strings.ToLower(strings.TrimSpace(ep.Protocol))
			uninspected := proto == "" || proto == "tcp" || strings.EqualFold(strings.TrimSpace(ep.TLS), "skip")
			if uninspected {
				rules = append(rules, name)
				break
			}
		}
	}
	sort.Strings(rules)
	return rules, nil
}

func policyPortMatches(ep policyEndpointRule, port int) bool {
	if ep.Port == port {
		return true
	}
	if ep.Port == 0 && len(ep.Ports) == 0 {
		return true // no port: the rule covers every port on the host
	}
	for _, p := range ep.Ports {
		if p == port {
			return true
		}
	}
	return false
}

// policyHostMatches applies OpenShell's host selector semantics: exact,
// case-insensitive match, `*` for exactly one DNS label, `**` for one or
// more labels.
func policyHostMatches(pattern, host string) bool {
	pattern, host = strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(host)
	if !strings.Contains(pattern, "*") {
		return pattern == host
	}
	var b strings.Builder
	b.WriteString("^")
	for i, label := range strings.Split(pattern, ".") {
		if i > 0 {
			b.WriteString(`\.`)
		}
		switch label {
		case "**":
			b.WriteString(`[^.]+(?:\.[^.]+)*`)
		case "*":
			b.WriteString(`[^.]+`)
		default:
			b.WriteString(strings.ReplaceAll(regexp.QuoteMeta(label), `\*`, `[^.]*`))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(host)
}

// checkOpenAIEgressInspected fails the run before the agent starts when the
// sandbox's effective policy would make the OpenAI credential undeliverable
// (see uninspectedEndpointRules), or when that policy cannot be read or
// parsed: the run would only fail later, at pi's first request, with far
// less to go on.
func checkOpenAIEgressInspected(ctx context.Context, sandboxName string) error {
	policy, err := sandbox.EffectivePolicy(ctx, sandboxName)
	if err != nil {
		return fmt.Errorf("checking the sandbox policy for uninspected OpenAI routes: %w", err)
	}
	rules, err := uninspectedEndpointRules(policy, openAIAPIHost, 443)
	if err != nil {
		return fmt.Errorf("checking the sandbox policy for uninspected OpenAI routes: %w", err)
	}
	if len(rules) == 0 {
		return nil
	}
	return fmt.Errorf("sandbox policy rule %s allows %s:443 without L7 inspection (no `protocol`, `protocol: tcp`, or `tls: skip`); OpenShell 0.0.110+ will not inject the OpenAI credential over an uninspected route, so the agent would only see connection errors. The sandbox image's default policy carries such a rule (`codex`): give the harness a `policy:` (the fleet uses policies/base.yaml) or make that endpoint `protocol: rest`",
		strings.Join(rules, ", "), openAIAPIHost)
}

// ensureOpenAIProvider creates the run-scoped provider for one
// fullsend-openai definition. backend is the runtime the run selected: when
// it implements runtime.OpenAICredentialSeeder with a non-empty seed, the
// handle carries that runtime's seed fragment and credential file so a
// refresh can hand the new placeholder to the running agent. A backend
// without one (Claude Code, or a seeder still stubbed out) leaves both
// empty and sandboxReady() stays false, so a refresh only updates the
// provider.
func ensureOpenAIProvider(ctx context.Context, pd harness.ProviderDef, sandboxName string, ids config.OpenAIWIFConfig, backend runtime.Backend, printer *ui.Printer) (openAIProviderHandle, error) {
	name := runScopedProviderName(pd.Name, sandboxName)
	printer.StepStart("Resolving OpenAI credential for provider: " + pd.Name)
	cred, err := resolveOpenAICredential(ctx, os.Getenv, ids)
	if err != nil {
		printer.StepFail("OpenAI credential unavailable for provider " + pd.Name)
		return openAIProviderHandle{}, fmt.Errorf("provider %q: %w", pd.Name, err)
	}
	// Two redaction layers: the exact value in the process-wide redactor
	// (the token is opaque — no prefix pattern can be trusted; this is the
	// authoritative control), and the Actions log mask, which is
	// best-effort defence in depth (GitHub masks the value per line and
	// can miss transformed occurrences).
	if !security.RegisterRuntimeSecret(cred.value) {
		return openAIProviderHandle{}, fmt.Errorf("provider %q: the resolved credential is too short to redact reliably; refusing to use it", pd.Name)
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", cred.value)
	}
	printer.StepDone("OpenAI credential ready (" + cred.detail + ")")
	for _, w := range cred.warnings {
		printer.StepWarn(w)
	}
	if len(pd.Config) > 0 {
		keys := make([]string, 0, len(pd.Config))
		for k := range pd.Config {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		printer.StepWarn(fmt.Sprintf("provider %q: config keys %s are ignored — the run-scoped OpenAI provider carries only the credential", pd.Name, strings.Join(keys, ", ")))
	}
	if cred.source == "static" {
		if os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("CI") != "" {
			printer.StepWarn("OpenAI credential is a static OPENAI_API_KEY in CI; prefer Workload Identity Federation (FULLSEND_OPENAI_AUDIENCE, FULLSEND_OPENAI_IDENTITY_PROVIDER_ID, FULLSEND_OPENAI_SERVICE_ACCOUNT_ID) so no long-lived key is stored")
		}
		cred.expiresAt = time.Now().Add(openAIStaticKeyLifetime)
	}

	keys, ignoredKeys := openAICredentialKeys(pd)
	if len(ignoredKeys) > 0 {
		printer.StepWarn(fmt.Sprintf("Provider %q declares credential keys %s; only %s is used", pd.Name, strings.Join(ignoredKeys, ", "), openAIDefaultCredentialKey))
	}
	creds := make(map[string]string, len(keys))
	for _, k := range keys {
		creds[k] = cred.value
	}

	start := time.Now()
	printer.StepStart("Ensuring run-scoped provider: " + name)
	// Two steps, so the value is never on the gateway without its expiry:
	// create the instance with empty credentials (nothing secret involved),
	// then store value and expiry together in one update.
	empty := make(map[string]string, len(keys))
	for _, k := range keys {
		empty[k] = ""
	}
	if err := sandbox.EnsureProviderLiteral(ctx, name, pd.Type, empty); err != nil {
		// OpenShell 0.0.115 accepts an empty credential value on create
		// (only an empty credential *map* is rejected — the same gap the
		// _NOOP_* providers use). Should a later release validate values,
		// fall back to creating with the value and attaching the expiry in
		// the very next call: a one-call window instead of a hard failure.
		if !emptyCredentialRefused(err) {
			printer.StepFail("Failed to create run-scoped provider " + name)
			return openAIProviderHandle{}, fmt.Errorf("ensuring provider %q: %w", name, err)
		}
		printer.StepWarn("Gateway refused an empty credential on create; creating with the value and attaching the expiry immediately")
		if err := sandbox.EnsureProviderLiteral(ctx, name, pd.Type, creds); err != nil {
			printer.StepFail("Failed to create run-scoped provider " + name)
			return openAIProviderHandle{}, fmt.Errorf("ensuring provider %q: %w", name, err)
		}
	}
	if err := sandbox.UpdateProviderLiteralWithExpiry(ctx, name, creds, cred.expiresAt); err != nil {
		// The caller only registers the deferred delete once this function
		// succeeds, so remove the instance here; if that fails too, blank
		// the credential so nothing usable can be left behind.
		printer.StepFail("Failed to store the credential on " + name)
		if delErr := sandbox.DeleteProvider(name); delErr != nil && !errors.Is(delErr, sandbox.ErrProviderNotFound) {
			if blankErr := sandbox.UpdateProviderLiteral(ctx, name, empty); blankErr != nil {
				printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could be neither deleted (%v) nor blanked (%v); remove it with `openshell provider delete %s`", name, delErr, blankErr, name))
			} else {
				printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could not be deleted (%v); its credential was blanked", name, delErr))
			}
		}
		return openAIProviderHandle{}, fmt.Errorf("storing credential on provider %q: %w", name, err)
	}
	printer.StepDone(fmt.Sprintf("Provider ready: %s (%s, expires in %s, %.1fs)", name, cred.source, time.Until(cred.expiresAt).Round(time.Minute), time.Since(start).Seconds()))
	authSeed, authFile := openAICredentialFiles(backend)
	return openAIProviderHandle{
		sandbox:   sandboxName,
		authSeed:  authSeed,
		ids:       ids,
		sandboxUp: &atomic.Bool{},
		authFile:  authFile,
		name:      name, keys: keys, source: cred.source, expiresAt: cred.expiresAt}, nil
}

// openAICredentialFiles returns the seed fragment and credential file for
// the selected backend, or two empty strings when it has no OpenAI seeder
// (or its seeder is still a stub). Both are empty together: a file with no
// fragment to write it would only make a refresh verify a file nothing
// seeds.
func openAICredentialFiles(backend runtime.Backend) (seed, file string) {
	s, ok := backend.Runtime.(runtime.OpenAICredentialSeeder)
	if !ok {
		return "", ""
	}
	seed = s.OpenAIAuthSeed()
	if seed == "" {
		return "", ""
	}
	return seed, s.OpenAIAuthFile()
}

// openAIRefreshDelay is how long to wait before the next refresh of a
// credential that expires at expiresAt: the margin and a jitter before
// expiry, never less than the minimum delay.
func openAIRefreshDelay(expiresAt, now time.Time, jitter time.Duration) time.Duration {
	remaining := expiresAt.Sub(now)
	lead := openAIRefreshMargin
	if half := remaining / 2; lead > half {
		lead = half
	}
	d := remaining - lead - jitter
	if d < openAIRefreshMinDelay {
		return openAIRefreshMinDelay
	}
	return d
}

// refreshOpenAIProvider renews one run-scoped provider — a WIF credential is
// re-exchanged from a fresh GitHub assertion and hot-updated into the
// provider, a static key has its provider expiry pushed out — and, once the
// sandbox is up, hands the resulting placeholder to the running agent
// through the credential file its backend named. It returns the new expiry
// and the placeholder the agent now holds.
func refreshOpenAIProvider(ctx context.Context, h openAIProviderHandle, placeholder string, budget time.Duration, printer *ui.Printer) (time.Time, string, error) {
	// The exchange and the provider update are bounded by what is left of
	// the credential being renewed; the re-seed is not, because it waits
	// for the sandbox to observe the new generation (~20 s measured, up to
	// openAIPlaceholderSettle) and that wait must not be cut short by a
	// token that is about to expire.
	updateCtx, cancelUpdate := context.WithTimeout(ctx, budget)
	defer cancelUpdate()
	reseed := h.sandboxReady()
	if reseed && placeholder == "" {
		// Learn what the agent holds before rotating: after the update the sandbox
		// may already hand out the new placeholder, and the settle wait
		// needs the old one to recognise the change. Any update — a new
		// value or only a new expiry — is a new generation on OpenShell
		// 0.0.115, and the generation the agent holds keeps the expiry it was
		// built with, so both paths must re-seed.
		p, err := baselineOpenAIPlaceholder(updateCtx, h.sandbox)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("reading the placeholder the agent holds before rotating: %w", err)
		}
		placeholder = p
	}
	var expiresAt time.Time
	if h.source == "wif" {
		cred, err := resolveOpenAICredential(updateCtx, os.Getenv, h.ids)
		if err != nil {
			return time.Time{}, placeholder, err
		}
		if cred.source != "wif" {
			return time.Time{}, placeholder, fmt.Errorf("credential source changed to %s during the run", cred.source)
		}
		if !security.RegisterRuntimeSecret(cred.value) {
			return time.Time{}, placeholder, fmt.Errorf("refreshed credential is too short to redact reliably; refusing to use it")
		}
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			fmt.Fprintf(os.Stderr, "::add-mask::%s\n", cred.value)
		}
		creds := make(map[string]string, len(h.keys))
		for _, k := range h.keys {
			creds[k] = cred.value
		}
		// One update carries the value and its expiry, so the new token
		// never sits behind the old expiry between two calls.
		if err := sandbox.UpdateProviderLiteralWithExpiry(updateCtx, h.name, creds, cred.expiresAt); err != nil {
			return time.Time{}, placeholder, err
		}
		expiresAt = cred.expiresAt
	} else {
		expiresAt = time.Now().Add(openAIStaticKeyLifetime)
		for _, k := range h.keys {
			if err := sandbox.SetProviderCredentialExpiry(updateCtx, h.name, k, expiresAt); err != nil {
				return time.Time{}, placeholder, err
			}
		}
	}
	if !reseed {
		// The agent has not started, or the selected backend has no
		// credential file to seed: a backend that does seeds at launch from
		// whatever placeholder the sandbox carries then.
		return expiresAt, placeholder, nil
	}
	// The running agent keeps the placeholder it launched with, and on
	// OpenShell 0.0.115 that placeholder stays pinned to the old generation,
	// so hand the new one over through the file it re-reads per request.
	settleCtx, cancelSettle := context.WithTimeout(ctx, openAIPlaceholderSettle+3*openAIPlaceholderExecTimeout+openAIPlaceholderPoll)
	defer cancelSettle()
	seeded, err := reseedOpenAIAuth(settleCtx, h, placeholder, printer)
	if err != nil {
		return time.Time{}, placeholder, fmt.Errorf("the provider holds the new credential but the running agent was not re-seeded: %w", err)
	}
	return expiresAt, seeded, nil
}

// runOpenAIRefresh keeps a run-scoped provider's credential valid for the
// life of the run. Each cycle waits until shortly before the current
// expiry, then refreshes with bounded retries. When the retries are
// exhausted it stops and says so: the provider's recorded expiry makes the
// gateway fail closed at that instant, so the run fails visibly instead of
// silently outliving its credential. Runs until ctx is cancelled.
func runOpenAIRefresh(ctx context.Context, h openAIProviderHandle, printer *ui.Printer) {
	expiresAt := h.expiresAt
	// placeholder is the generation the runtime's credential file currently
	// names; learned from the sandbox before the first refresh, then
	// tracked per re-seed.
	placeholder := ""
	for {
		jitter := time.Duration(0)
		if openAIRefreshJitter > 0 {
			jitter = time.Duration(rand.Int64N(int64(openAIRefreshJitter)))
		}
		delay := openAIRefreshDelay(expiresAt, time.Now(), jitter)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		var next time.Time
		var err error
		for attempt := 0; attempt < openAIRefreshRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(openAIRefreshBackoff):
				}
			}
			// An attempt is bounded by what is left of the credential it is
			// renewing — but an already-expired credential is exactly when a
			// refresh is needed most (a suspended laptop), so keep a floor.
			budget := time.Until(expiresAt)
			if budget < time.Minute {
				budget = time.Minute
			}
			next, placeholder, err = refreshOpenAIProvider(ctx, h, placeholder, budget, printer)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			printer.StepWarn(fmt.Sprintf("OpenAI credential refresh attempt %d/%d failed: %v", attempt+1, openAIRefreshRetries, err))
		}
		if err != nil {
			printer.StepWarn(fmt.Sprintf("OpenAI credential refresh for %s gave up; the running agent keeps the credential generation it holds, which stops resolving when that token expires (recorded expiry %s)", h.name, expiresAt.UTC().Format(time.RFC3339)))
			return
		}
		expiresAt = next
		printer.StepDone(fmt.Sprintf("OpenAI credential refreshed for %s (%s, next expiry in %s)", h.name, h.source, time.Until(expiresAt).Round(time.Minute)))
	}
}

// cleanupRunScopedProvider removes a run-scoped provider at the end of the
// run. OpenShell refuses to delete a provider that a sandbox still
// references (FAILED_PRECONDITION), which is exactly the --keep-sandbox
// case, so when the delete fails the credential is expired in place
// instead: the gateway then fails placeholder resolution closed for the
// kept sandbox while the record stays deletable later. Uses a background
// context because the run's context is usually cancelled by the time the
// deferred cleanup runs.
func cleanupRunScopedProvider(name string, keys []string, sandboxKept bool, printer *ui.Printer) {
	delErr := sandbox.DeleteProvider(name)
	// The sandbox delete that ran just before this can still hold the
	// reference for a few seconds; when the sandbox is gone for good, wait
	// it out instead of leaving an expired record behind.
	for attempt := 0; delErr != nil && !sandboxKept && attempt < openAIDeleteRetries && strings.Contains(delErr.Error(), "attached to sandbox"); attempt++ {
		if attempt == 0 {
			printer.StepInfo("Waiting for the gateway to release the deleted sandbox's provider reference")
		}
		time.Sleep(openAIDeleteBackoff)
		delErr = sandbox.DeleteProvider(name)
	}
	if delErr == nil {
		printer.StepDone("Run-scoped provider deleted: " + name)
		return
	}
	if errors.Is(delErr, sandbox.ErrProviderNotFound) {
		printer.StepDone("Run-scoped provider already gone: " + name)
		return
	}
	now := time.Now()
	var expireErr error
	for _, k := range keys {
		if err := sandbox.SetProviderCredentialExpiry(context.Background(), name, k, now); err != nil {
			expireErr = err
			break
		}
	}
	if expireErr != nil {
		printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could not be deleted (%v) or expired in place (%v); expiry set at creation is the backstop", name, delErr, expireErr))
		return
	}
	if sandboxKept {
		printer.StepWarn(fmt.Sprintf("Run-scoped provider %s expired in place instead of deleted (the kept sandbox still references it: %v); remove it with `openshell provider delete %s` once the sandbox is gone", name, delErr, name))
		return
	}
	printer.StepWarn(fmt.Sprintf("Run-scoped provider %s expired in place instead of deleted (still reported attached after the sandbox was deleted: %v); remove it with `openshell provider delete %s`", name, delErr, name))
}
