package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/randutil"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Jira in the sandbox with a service account's OAuth 2.0 client
// credentials (fullsend#9220). The builtin jira provider (profile
// fullsend-jira) normally expands a static token from FULLSEND_JIRA_TOKEN
// like any provider. When that expands empty and the runner has
// FULLSEND_JIRA_CLIENT_ID and FULLSEND_JIRA_CLIENT_SECRET, the runner mints
// the access token itself, gives it to a run-scoped provider instance, and
// refreshes it before it expires, as ensureOpenAIProvider does for OpenAI
// (ADR 0092). The client secret never reaches a provider value, the
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

// jiraSiteProfileIDPrefix starts the id of every per-site profile rendered
// for a minted token (renderJiraSiteProfile) (#9492).
const jiraSiteProfileIDPrefix = jiraProviderType + "-"

var (
	// jiraCloudIDPattern is a Jira Cloud ID as rendered into a per-site
	// profile: a lowercase UUID, so it can carry no path, YAML or glob
	// character.
	jiraCloudIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// jiraSiteProfileIDPattern matches every id renderJiraSiteProfile can
	// produce, in any case. Only these ids are reserved: the agents repo's
	// own fullsend-jira-ro profile shares the prefix and stays listable.
	jiraSiteProfileIDPattern = regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(jiraSiteProfileIDPrefix) + `[0-9a-f]{` + strconv.Itoa(gatewayProfileIDHashLen) + `}$`)
)

// Test seams.
var (
	// jiraMintFn performs one client_credentials grant. Override in tests
	// to return fixed tokens or errors without a token endpoint.
	jiraMintFn = func(ctx context.Context, creds jira.Credentials) (jira.AccessToken, error) {
		return jira.MintAccessToken(ctx, nil, "", creds)
	}
	// ensureJiraProviderLiteralFn creates the run-scoped provider with the
	// minted value. Override in tests to record the call without openshell.
	ensureJiraProviderLiteralFn = sandbox.EnsureProviderLiteral
	// updateJiraProviderFn stores a token and its expiry on the provider.
	// Override in tests to record or block the update without openshell.
	updateJiraProviderFn = sandbox.UpdateProviderLiteralWithExpiry
	// reseedJiraFn hands a new generation to the token file
	// (reseedCredential). Override in tests to return a placeholder or
	// error without a sandbox.
	reseedJiraFn = reseedCredential
	// seedJiraTokenFileFn writes the token file once the sandbox is up.
	// Override in tests to set the recorded placeholder without a sandbox.
	seedJiraTokenFileFn = seedJiraTokenFile
	// jiraBaselineFn reads the placeholder the sandbox hands out now
	// (baselinePlaceholder). Override in tests to return a placeholder
	// without a sandbox.
	jiraBaselineFn = baselinePlaceholder
	// jiraMintGatewayDiscoverFn looks up the gateway URL for a minting run
	// that is not Jira-sourced. Override in tests to return a fixed URL or
	// error without Atlassian.
	jiraMintGatewayDiscoverFn jiraGatewayDiscoverer = discoverJiraGatewayURL
	// importJiraProfileFn imports the rendered per-site profile. Override
	// in tests to record the profile without openshell.
	importJiraProfileFn = importProfileBytes
)

// errJiraUnredactable and errJiraUnsafeToken are refresh failures no
// retry can fix.
var (
	errJiraUnredactable = errors.New("minted Jira access token is too short to redact reliably; refusing to use it")
	errJiraUnsafeToken  = errors.New("minted Jira access token has characters outside printable ASCII; refusing to use it")
)

// jiraMintCredentials decides how the jira provider pd gets its value. It
// returns mint=false when the definition's JIRA_TOKEN expands to a value
// (a static token: the builtin definition reads FULLSEND_JIRA_TOKEN) or
// when no client credentials are set (the provider is created as it always
// was). It returns the client credentials and mint=true when JIRA_TOKEN
// expands empty and both client variables are set, and an error when only
// one of them is.
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

// jiraRunMints reports whether any fullsend-jira definition in defs mints
// its token. An incomplete client pair counts as not minting; the provider
// loop reports it.
func jiraRunMints(defs []harness.ProviderDef, getenv func(string) string) bool {
	for _, pd := range defs {
		if !strings.EqualFold(pd.Type, jiraProviderType) {
			continue
		}
		if _, mint, err := jiraMintCredentials(pd, getenv); err == nil && mint {
			return true
		}
	}
	return false
}

// rejectMultipleMintedJiraProviders fails when a fullsend-jira definition
// mints its token and the run lists any other definition that declares the
// JIRA_TOKEN credential, whatever its type (for example the agents repo's
// fullsend-jira-ro): every attached provider exports JIRA_TOKEN into the
// sandbox, and a second minted one would also rewrite the one token file.
func rejectMultipleMintedJiraProviders(defs []harness.ProviderDef, getenv func(string) string) error {
	var all, minted []string
	for _, pd := range defs {
		if _, declared := pd.Credentials[jiraCredentialKey]; !declared {
			continue
		}
		all = append(all, pd.Name)
		if !strings.EqualFold(pd.Type, jiraProviderType) {
			continue
		}
		// An incomplete pair is reported by the provider loop.
		if _, mint, err := jiraMintCredentials(pd, getenv); err == nil && mint {
			minted = append(minted, pd.Name)
		}
	}
	if len(minted) == 0 || len(all) < 2 {
		return nil
	}
	return fmt.Errorf("providers %q all declare %s and %q mints its token, but they would share %s in the sandbox and the one Jira token file (%s); list only the %s provider", all, jiraCredentialKey, minted[0], jiraCredentialKey, sandbox.SandboxJiraTokenFile, minted[0])
}

// The gateway serves every Jira Cloud site at /ex/jira/{cloudId}/..., so a
// credential valid on several sites reaches all of them through one host.
// When the runner mints the token, the run-scoped provider is governed by a
// per-site rendering of the embedded fullsend-jira profile whose gateway
// endpoint carries path /ex/jira/<cloudId>/** (#9492). Static-token mode
// keeps the embedded profile as it is.

// rejectJiraSiteProfileIDs fails when the run lists a profile under an id
// renderJiraSiteProfile can produce, so a repository or URL-resolved
// profile can never stand on the gateway in place of a rendered one. It
// applies in every mode.
func rejectJiraSiteProfileIDs(resolved []resolve.ResolvedProfile) error {
	for _, rp := range resolved {
		if jiraSiteProfileIDPattern.MatchString(rp.ID) {
			return fmt.Errorf("provider profile %q is reserved for the per-site Jira profile fullsend renders at run time; remove it from the harness/openshell profiles", rp.ID)
		}
	}
	return nil
}

// jiraSiteProfileID returns the per-site profile id for a validated cloud
// ID: fullsend-jira plus a short hash of it, so runs for different sites on
// one shared OpenShell gateway never import over each other's profile.
func jiraSiteProfileID(cloudID string) string {
	sum := sha256.Sum256([]byte(cloudID))
	return jiraSiteProfileIDPrefix + hex.EncodeToString(sum[:])[:gatewayProfileIDHashLen]
}

// validateJiraCloudID refuses a cloud ID that is not a lowercase UUID. The
// value comes from a remote response and is rendered into YAML and a path
// rule, so nothing else may pass. The error never carries the value.
func validateJiraCloudID(cloudID string) error {
	if !jiraCloudIDPattern.MatchString(cloudID) {
		return errors.New("the Jira cloud ID is not a lowercase UUID; refusing to render it into the Jira profile")
	}
	return nil
}

// renderJiraSiteProfile renders the embedded fullsend-jira profile for the
// site cloudID and returns its bytes and per-site id.
func renderJiraSiteProfile(cloudID string) ([]byte, string, error) {
	tmpl, err := scaffold.FullsendRepoFile("profiles/" + jiraProviderType + ".yaml")
	if err != nil {
		return nil, "", fmt.Errorf("provider profile %q is not shipped by this fullsend build: %w", jiraProviderType, err)
	}
	return renderJiraSiteProfileFrom(tmpl, cloudID)
}

// renderJiraSiteProfileFrom renders tmpl, a fullsend-jira profile, for the
// site cloudID: the one api.atlassian.com endpoint gets path
// /ex/jira/<cloudId>/** and the id becomes jiraSiteProfileID(cloudID). It
// fails unless tmpl declares id fullsend-jira and exactly one endpoint for
// that host, with no path of its own, and no wildcard endpoint that would
// also admit it.
func renderJiraSiteProfileFrom(tmpl []byte, cloudID string) ([]byte, string, error) {
	if err := validateJiraCloudID(cloudID); err != nil {
		return nil, "", err
	}
	invalid := func(why string) error {
		return fmt.Errorf("embedded provider profile %q cannot be limited to one Jira site: %s", jiraProviderType, why)
	}
	// One document only, and no merge, alias or duplicate keys: OpenShell
	// applies those, and the scan below sees only literal keys.
	doc, err := parseSingleProfileDocument(tmpl)
	if err != nil {
		return nil, "", invalid(err.Error())
	}
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, "", invalid("not a mapping")
	}
	root := doc.Content[0]
	if err := checkProfileKeys(root); err != nil {
		return nil, "", invalid(err.Error())
	}
	idNode := yamlMappingValue(root, "id")
	if idNode == nil || idNode.Kind != yaml.ScalarNode || idNode.Value != jiraProviderType {
		return nil, "", invalid("id " + jiraProviderType + " expected")
	}
	endpoints := yamlMappingValue(root, "endpoints")
	if endpoints == nil || endpoints.Kind != yaml.SequenceNode {
		return nil, "", invalid("no endpoints list")
	}
	var gateway *yaml.Node
	matches := 0
	for _, ep := range endpoints.Content {
		if ep.Kind != yaml.MappingNode {
			return nil, "", invalid("an endpoint is not a mapping")
		}
		hostNode := yamlMappingValue(ep, "host")
		if hostNode == nil || hostNode.Kind != yaml.ScalarNode {
			return nil, "", invalid("an endpoint has no literal host")
		}
		host := strings.ToLower(strings.TrimSpace(hostNode.Value))
		if host == jiraAPIHost {
			gateway = ep
			matches++
			continue
		}
		// Any glob that matches the gateway host (*.atlassian.com,
		// api.atlassian.*, api.**) would admit it without the path. A
		// leading-* suffix match also covers a bare * or **, which
		// policyHostMatches reads as a single label.
		if policyHostMatches(host, jiraAPIHost) ||
			(strings.HasPrefix(host, "*") && strings.HasSuffix(jiraAPIHost, strings.TrimLeft(host, "*"))) {
			return nil, "", invalid("endpoint " + host + " also admits " + jiraAPIHost)
		}
	}
	if matches != 1 {
		return nil, "", invalid(fmt.Sprintf("exactly one %s endpoint expected, found %d", jiraAPIHost, matches))
	}
	if yamlMappingValue(gateway, "path") != nil {
		return nil, "", invalid("the " + jiraAPIHost + " endpoint already sets a path")
	}
	id := jiraSiteProfileID(cloudID)
	idNode.Value = id
	idNode.Style = 0
	gateway.Content = append(gateway.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "/ex/jira/" + cloudID + "/**", Style: yaml.DoubleQuotedStyle},
	)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, "", invalid(err.Error())
	}
	if err := enc.Close(); err != nil {
		return nil, "", invalid(err.Error())
	}
	return out.Bytes(), id, nil
}

// ensureJiraSiteProfile renders the per-site fullsend-jira profile for
// cloudID and imports it (importProfileBytes). It returns the profile id
// the run-scoped provider must use as its type.
func ensureJiraSiteProfile(ctx context.Context, cloudID string, printer *ui.Printer) (string, error) {
	data, id, err := renderJiraSiteProfile(cloudID)
	if err != nil {
		return "", err
	}
	if err := importJiraProfileFn(ctx, id, data, printer); err != nil {
		return "", err
	}
	return id, nil
}

// resolveJiraMintCloudID returns the cloud ID of the site a minting run
// belongs to, failing closed (ADR 0156) rather than leaving the gateway
// unscoped. apiURL is the JIRA_API_URL resolveJiraRunEnv returned for a
// Jira-sourced run ("" otherwise), which is reused; any other run looks
// the gateway URL up from tracker.jira.base_url with discover. It is
// called only when the run mints. Errors never include a raw URL.
func resolveJiraMintCloudID(ctx context.Context, apiURL string, tracking *config.TrackingConfig, offline bool, discover jiraGatewayDiscoverer) (string, error) {
	const what = "minting the Jira access token"
	if tracking == nil || tracking.Jira == nil || tracking.Jira.BaseURL == "" {
		return "", fmt.Errorf("%s: tracker.jira.base_url is not configured, so the Jira gateway cannot be limited to the run's own site", what)
	}
	// The profile host is fixed to api.atlassian.com.
	if strings.TrimRight(tracking.Jira.EffectiveAPIURL(), "/") != jira.DefaultAPIURL {
		return "", fmt.Errorf("%s: tracker.jira.api_url must be unset or %s, the gateway the fullsend-jira profile allows", what, jira.DefaultAPIURL)
	}
	if apiURL == "" {
		conn := *tracking.Jira
		conn.BaseURL = strings.TrimRight(conn.BaseURL, "/")
		if err := conn.ValidateConnection(); err != nil {
			return "", fmt.Errorf("%s: invalid Jira connection settings: %w", what, err)
		}
		if offline {
			return "", fmt.Errorf("%s: limiting the Jira gateway to the run's own site needs a cloud-ID lookup, which --offline does not allow", what)
		}
		host := conn.BaseURL
		if u, err := url.Parse(conn.BaseURL); err == nil {
			host = u.Host
		}
		dctx, cancel := context.WithTimeout(ctx, jiraGatewayDiscoveryTimeout)
		defer cancel()
		var err error
		apiURL, err = discover(dctx, jira.ScopedConfig{BaseURL: conn.BaseURL, APIURL: jira.DefaultAPIURL})
		if err == nil && apiURL == "" {
			err = errors.New("empty gateway URL")
		}
		if err != nil {
			return "", fmt.Errorf("%s: could not resolve the Jira cloud ID for %s: %w", what, host, err)
		}
	}
	cloudID, ok := strings.CutPrefix(apiURL, jira.DefaultAPIURL+"/ex/jira/")
	if !ok {
		return "", fmt.Errorf("%s: the resolved Jira gateway URL is not under %s/ex/jira/", what, jira.DefaultAPIURL)
	}
	if err := validateJiraCloudID(cloudID); err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	return cloudID, nil
}

// jiraSeedState is the placeholder the token file names, shared by every
// copy of a handle and by the refresher across restarts.
type jiraSeedState struct {
	mu          sync.Mutex
	placeholder string
	// rotateMu is held across the initial seed and marking the sandbox up,
	// and across a refresh's provider update and its re-seed decision, so
	// a rotation that lands after the seed always re-seeds.
	rotateMu sync.Mutex
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
// and, in GitHub Actions, the log mask (maskActionsValue escapes it). A
// token with a byte outside printable ASCII (0x21-0x7E) is refused first:
// such a token is not what Atlassian issues, and refusing it keeps the
// mask line from ever carrying a control character.
func registerJiraToken(value string, maskActions bool) error {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return errJiraUnsafeToken
		}
	}
	if !security.RegisterRuntimeSecret(value) {
		return errJiraUnredactable
	}
	if maskActions {
		maskActionsValue(value)
	}
	return nil
}

// ensureJiraProvider mints the first access token and creates the
// run-scoped provider instance carrying it, with the token's expiry, as a
// provider of type profileID (the per-site profile, ensureJiraSiteProfile).
func ensureJiraProvider(ctx context.Context, pd harness.ProviderDef, profileID, sandboxName string, creds jira.Credentials, printer *ui.Printer) (jiraProviderHandle, error) {
	maskActions := os.Getenv("GITHUB_ACTIONS") == "true"
	// The client secret is redacted too, should any error ever quote it.
	security.RegisterRuntimeSecret(creds.ClientSecret)
	name := runScopedProviderName(pd.Name, sandboxName)
	printer.StepStart("Minting the Jira access token for provider: " + pd.Name)
	tok, err := jiraMintFn(ctx, creds)
	if err != nil {
		if jira.IsPermanentOAuthError(err) {
			cause := jiraRejectedCause(err)
			printer.StepFail(fmt.Sprintf("Jira %s for provider %s; check %s and %s", cause, pd.Name, jira.ClientIDEnvVar, jira.ClientSecretEnvVar))
			return jiraProviderHandle{}, fmt.Errorf("provider %q: %s: %w", pd.Name, cause, err)
		}
		printer.StepFail("Jira access token unavailable for provider " + pd.Name)
		return jiraProviderHandle{}, fmt.Errorf("provider %q: %w", pd.Name, err)
	}
	if err := registerJiraToken(tok.Value, maskActions); err != nil {
		printer.StepFail("Jira access token unusable for provider " + pd.Name)
		return jiraProviderHandle{}, fmt.Errorf("provider %q: %w", pd.Name, err)
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
	if err := ensureJiraProviderLiteralFn(ctx, name, profileID, values); err != nil {
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

// jiraRejectedCause names a permanent token endpoint refusal
// (jira.IsPermanentOAuthError) by its OAuth error code, or its status when
// the answer had none.
func jiraRejectedCause(err error) string {
	detail := "no error code"
	var oe *jira.OAuthError
	if errors.As(err, &oe) {
		detail = fmt.Sprintf("status %d", oe.Status)
		if oe.Code != "" {
			detail = oe.Code
		}
	}
	return "token endpoint rejected the client credentials (" + detail + ")"
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

// seedJiraAndMarkReady seeds the token file and marks the sandbox up
// under the handle's rotation lock, so a concurrent refresh either
// finishes its update before the seed reads the sandbox's placeholder or
// sees the sandbox up and re-seeds.
func seedJiraAndMarkReady(ctx context.Context, h jiraProviderHandle, printer *ui.Printer) error {
	h.state.rotateMu.Lock()
	defer h.state.rotateMu.Unlock()
	if err := seedJiraTokenFileFn(ctx, h, printer); err != nil {
		return err
	}
	h.sandboxUp.Store(true)
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
	tok, err := jiraMintFn(updateCtx, h.creds)
	if err != nil {
		return time.Time{}, err
	}
	if err := registerJiraToken(tok.Value, h.maskActions); err != nil {
		return time.Time{}, err
	}
	// The lock keeps the first seed out from between the readiness check
	// and the update.
	h.state.rotateMu.Lock()
	defer h.state.rotateMu.Unlock()
	reseed := h.sandboxReady()
	var baseline string
	if reseed {
		// The file may name an older generation than the sandbox hands
		// out: a seed right after an earlier update can read the placeholder
		// before the update reached the sandbox. Waiting only for "not what
		// the file names" would then settle on the current generation and
		// stay one behind, so wait for one newer than what the sandbox
		// hands out before this update.
		p, err := jiraBaselineFn(updateCtx, h.sandbox, jiraCredentialKey)
		if err != nil {
			return time.Time{}, fmt.Errorf("reading the Jira placeholder the sandbox holds before rotating: %w", err)
		}
		baseline = p
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
	held := h.state.get()
	var superseded []string
	if held != baseline {
		// Retire what the file names too, so a stale writer cannot put it back.
		superseded = []string{held}
	}
	seeded, err := reseedJiraFn(settleCtx, h.sandbox, "Jira", runtime.JiraTokenSeed(), baseline, h.unrecorded, superseded, printer)
	if err == nil && seeded == held {
		err = errors.New("the sandbox handed out the generation the token file already names")
	}
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
// ends. A token too short to redact or unsafe to mask, a new generation
// that never reached the sandbox, too many unrecorded generations, or a
// token endpoint that rejects the client credentials stop it through
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
				if errors.As(err, &notObserved) || errors.Is(err, errJiraUnredactable) || errors.Is(err, errJiraUnsafeToken) {
					failJiraClosed(h, expiresAt, err, printer)
					return
				}
				// The same client credentials will be refused again.
				if jira.IsPermanentOAuthError(err) {
					failJiraClosed(h, expiresAt, fmt.Errorf("%s: %w", jiraRejectedCause(err), err), printer)
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
