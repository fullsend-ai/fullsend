package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// The inference gateway route (#8280, ADR 0137) reaches a host the run
// configures, so its OpenShell profile cannot be a fixed embedded file the
// way fullsend-openai is. The scaffold ships a template
// (profiles/fullsend-inference-gateway.yaml) whose host and id the runner
// fills in per configured host.
const (
	// gatewayProfileTemplateID is the id the scaffold template declares and
	// the prefix of every rendered per-host profile id.
	gatewayProfileTemplateID = "fullsend-inference-gateway"
	// gatewayProfileHostPlaceholder is the template's stand-in for the host.
	gatewayProfileHostPlaceholder = "__INFERENCE_GATEWAY_HOST__"
	// gatewayProviderBase is the harness-facing provider name the run-scoped
	// instance name derives from (gatewayRunScopedProviderName).
	gatewayProviderBase = "inference-gateway"
	// gatewayCredentialKey is the only credential the gateway profile
	// declares; the run-scoped provider carries the forge OIDC token under it.
	gatewayCredentialKey = "INFERENCE_GATEWAY_API_KEY"
	// gatewayProfileIDHashLen is how many hex digits of the host's SHA-256
	// the per-host profile id carries.
	gatewayProfileIDHashLen = 12
)

// validateGatewayHost checks that host is a bare DNS host name the profile
// can bind: no scheme, userinfo, port, path, query, wildcard or trailing dot,
// and RFC 1123 labels. It returns the host lower-cased.
func validateGatewayHost(host string) (string, error) {
	if host == "" || strings.TrimSpace(host) == "" {
		return "", errors.New("inference gateway host is empty")
	}
	if host != strings.TrimSpace(host) {
		return "", fmt.Errorf("inference gateway host %q has surrounding whitespace", host)
	}
	if strings.Contains(host, "://") {
		return "", fmt.Errorf("inference gateway host %q must be a bare host name, not a URL", host)
	}
	if strings.ContainsAny(host, "/?#@:[]*") {
		return "", fmt.Errorf("inference gateway host %q must be a bare host name (no scheme, credentials, port, path, query or wildcard)", host)
	}
	if len(host) > 253 {
		return "", fmt.Errorf("inference gateway host %q is longer than 253 characters", host)
	}
	h := strings.ToLower(host)
	for _, label := range strings.Split(h, ".") {
		if label == "" {
			return "", fmt.Errorf("inference gateway host %q has an empty DNS label", host)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("inference gateway host %q has a DNS label longer than 63 characters", host)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("inference gateway host %q has a DNS label that starts or ends with '-'", host)
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", fmt.Errorf("inference gateway host %q contains %q, which is not valid in a host name", host, c)
			}
		}
	}
	return h, nil
}

// gatewayProfileID returns the per-host profile id for an already-validated
// host: the template id plus a short hash of the host, so two gateways on one
// shared OpenShell gateway never import over each other's profile.
func gatewayProfileID(host string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(host)))
	return gatewayProfileTemplateID + "-" + hex.EncodeToString(sum[:])[:gatewayProfileIDHashLen]
}

// The Claude Code rendering (#8294) is a template of its own: Claude Code
// calls only the Messages API, from its own binary, and the header the
// credential travels in depends on the auth mode. OpenShell stores a static
// credential's auth_style and header_name as placement metadata and still
// resolves the env placeholder wherever the client puts it, so the
// metadata records the header Claude Code sends in each mode. Each mode
// gets its own id, so two runs on one shared OpenShell gateway never import
// over each other's profile.
const (
	gatewayClaudeProfileTemplateID = "fullsend-inference-gateway-claude"
	gatewayProfileAuthStyle        = "__INFERENCE_GATEWAY_AUTH_STYLE__"
	gatewayProfileHeaderName       = "__INFERENCE_GATEWAY_HEADER_NAME__"
)

// gatewayProfile selects the rendering of the per-host gateway profile.
type gatewayProfile struct {
	// host is the gateway host the profile binds.
	host string
	// claude selects the Claude Code template.
	claude bool
	// apiKey is the block's api-key mode; it changes the Claude Code
	// rendering only (pi presents the credential as a bearer token in
	// both modes).
	apiKey bool
}

// renderGatewayProfile renders the scaffold's inference gateway profile
// template for host (the pi rendering) and returns its bytes and per-host
// profile id.
func renderGatewayProfile(host string) ([]byte, string, error) {
	return renderGatewayProfileFor(gatewayProfile{host: host})
}

// renderGatewayProfileFor renders the profile p selects and returns its
// bytes and per-host id.
func renderGatewayProfileFor(p gatewayProfile) ([]byte, string, error) {
	h, err := validateGatewayHost(p.host)
	if err != nil {
		return nil, "", err
	}
	templateID := gatewayProfileTemplateID
	var auth map[string]string
	id := gatewayProfileID(h)
	if p.claude {
		templateID = gatewayClaudeProfileTemplateID
		mode, style, header := "oidc", "bearer", "authorization"
		if p.apiKey {
			mode, style, header = "apikey", "header", "x-api-key"
		}
		auth = map[string]string{gatewayProfileAuthStyle: style, gatewayProfileHeaderName: header}
		sum := sha256.Sum256([]byte(h))
		id = templateID + "-" + mode + "-" + hex.EncodeToString(sum[:])[:gatewayProfileIDHashLen]
	}
	tmpl, err := scaffold.FullsendRepoFile("profiles/" + templateID + ".yaml")
	if err != nil {
		return nil, "", fmt.Errorf("provider profile %q is not shipped by this fullsend build: %w", templateID, err)
	}
	s := string(tmpl)
	idLine := "\nid: " + templateID + "\n"
	if strings.Count(s, idLine) != 1 || strings.Count(s, gatewayProfileHostPlaceholder) != 1 {
		return nil, "", fmt.Errorf("embedded provider profile %q is not a valid gateway template (one id line and one host placeholder expected)", templateID)
	}
	for placeholder, value := range auth {
		if strings.Count(s, placeholder) != 1 {
			return nil, "", fmt.Errorf("embedded provider profile %q is not a valid gateway template (one %s placeholder expected)", templateID, placeholder)
		}
		s = strings.Replace(s, placeholder, value, 1)
	}
	s = strings.Replace(s, idLine, "\nid: "+id+"\n", 1)
	s = strings.Replace(s, gatewayProfileHostPlaceholder, h, 1)
	return []byte(s), id, nil
}

// ensureGatewayProfile renders the gateway profile p selects and imports it
// (importProfileBytes, the same verified import ensureEmbeddedProfile uses).
// It returns the per-host profile id the run-scoped provider must use as its
// type.
func ensureGatewayProfile(ctx context.Context, p gatewayProfile, printer *ui.Printer) (string, error) {
	data, id, err := renderGatewayProfileFor(p)
	if err != nil {
		return "", err
	}
	if err := importProfileBytes(ctx, id, data, printer); err != nil {
		return "", err
	}
	return id, nil
}

// gatewayRunScopedProviderName is the run-scoped instance name of the
// inference gateway provider for this run, e.g.
// "inference-gateway-3f9c2a7b1d0e" (see runScopedProviderName).
func gatewayRunScopedProviderName(sandboxName string) string {
	return runScopedProviderName(gatewayProviderBase, sandboxName)
}

// checkGatewayEgressInspected is checkOpenAIEgressInspected for the
// configured inference gateway host.
func checkGatewayEgressInspected(ctx context.Context, sandboxName, host string) error {
	h, err := validateGatewayHost(host)
	if err != nil {
		return err
	}
	return checkEgressInspected(ctx, sandboxName, h, "inference gateway",
		"Give the harness a `policy:` without that rule (the fleet uses policies/base.yaml) or make that endpoint `protocol: rest`")
}

// gatewayCompactJWTPattern is a compact-serialized JWT: three base64url
// segments, the signature possibly empty. Anchored, so no newline or other
// character can ride along.
var gatewayCompactJWTPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`)

// validateGatewayAssertion refuses a gateway token that is not a compact
// JWT before it is registered for redaction, masked or stored. The
// `::add-mask::` line interpolates the value, so a newline in it would
// start a second GitHub Actions workflow command (`::error::`,
// `::add-mask::` of something else) and leave the rest of the token
// unmasked. The error never carries the value.
func validateGatewayAssertion(token string) error {
	if !gatewayCompactJWTPattern.MatchString(token) {
		return errors.New("inference gateway: the OIDC assertion is not a compact JWT; refusing to use it")
	}
	return nil
}

// ensureGatewayProvider imports the per-host gateway profile and creates the
// run-scoped provider instance carrying token (the forge OIDC assertion; the
// caller fetches it) under gatewayCredentialKey, with expiresAt as the
// credential expiry — the same OpenShell provider lifecycle as
// ensureOpenAIProvider. The token is registered for redaction first. It
// returns the instance name the sandbox must attach and the profile id; the
// caller registers cleanupRunScopedProvider for the name on success.
func ensureGatewayProvider(ctx context.Context, profile gatewayProfile, sandboxName, token string, expiresAt time.Time, printer *ui.Printer) (name, profileID string, err error) {
	if err := validateGatewayAssertion(token); err != nil {
		return "", "", err
	}
	return storeGatewayProvider(ctx, profile, sandboxName, token, expiresAt, printer)
}

// ensureGatewayAPIKeyProvider is ensureGatewayProvider for the api-key
// mode: the credential is the gateway API key (gatewayAPIKey has already
// refused one with a line break, so it cannot break the `::add-mask::`
// line), behind the same run-scoped placeholder, per-host profile and
// guards. expiresAt bounds the provider instance, not the key.
func ensureGatewayAPIKeyProvider(ctx context.Context, profile gatewayProfile, sandboxName, key string, expiresAt time.Time, printer *ui.Printer) (name, profileID string, err error) {
	if strings.ContainsAny(key, "\r\n\x00") || strings.TrimSpace(key) == "" {
		return "", "", errors.New("inference gateway: the API key is empty or holds a control character; refusing to use it")
	}
	return storeGatewayProvider(ctx, profile, sandboxName, key, expiresAt, printer)
}

// gatewayMaskData percent-encodes a value for a workflow command's data
// part ("%" first by construction, then CR and LF), which the runner
// decodes, so the registered mask equals the value even when it holds a
// "%25" or "%0A" sequence.
var gatewayMaskData = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

// storeGatewayProvider registers token for redaction, imports the per-host
// profile and creates the run-scoped provider carrying token with
// expiresAt as its credential expiry.
func storeGatewayProvider(ctx context.Context, profile gatewayProfile, sandboxName, token string, expiresAt time.Time, printer *ui.Printer) (name, profileID string, err error) {
	if !security.RegisterRuntimeSecret(token) {
		return "", "", errors.New("inference gateway: the token is too short to redact reliably; refusing to use it")
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", gatewayMaskData.Replace(token))
	}
	profileID, err = ensureGatewayProfile(ctx, profile, printer)
	if err != nil {
		return "", "", err
	}
	name = gatewayRunScopedProviderName(sandboxName)
	creds := map[string]string{gatewayCredentialKey: token}

	start := time.Now()
	printer.StepStart("Ensuring run-scoped provider: " + name)
	// OpenShell takes the declared credential's value on create and an
	// expiry only on update (see ensureOpenAIProvider).
	if err := sandbox.EnsureProviderLiteral(ctx, name, profileID, creds); err != nil {
		printer.StepFail("Failed to create run-scoped provider " + name)
		return "", "", fmt.Errorf("ensuring provider %q: %w", name, err)
	}
	if err := sandbox.UpdateProviderLiteralWithExpiry(ctx, name, creds, expiresAt); err != nil {
		printer.StepFail("Failed to store the credential on " + name)
		if delErr := sandbox.DeleteProvider(name); delErr != nil && !errors.Is(delErr, sandbox.ErrProviderNotFound) {
			if expErr := sandbox.SetProviderCredentialExpiry(context.Background(), name, gatewayCredentialKey, time.Now()); expErr != nil {
				printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could be neither deleted (%v) nor expired (%v); remove it with `openshell provider delete %s`", name, delErr, expErr, name))
			} else {
				printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could not be deleted (%v); its credential was expired", name, delErr))
			}
		}
		return "", "", fmt.Errorf("storing credential on provider %q: %w", name, err)
	}
	printer.StepDone(fmt.Sprintf("Provider ready: %s (%s, expires in %s, %.1fs)", name, profileID, time.Until(expiresAt).Round(time.Second), time.Since(start).Seconds()))
	return name, profileID, nil
}
