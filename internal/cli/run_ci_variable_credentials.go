package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// GitLab CI/CD-variable credential report (#9511).
//
// On GitLab, a provider credential that comes from a CI/CD variable is a
// long-lived value whose masking is exact-string log redaction and whose
// lifetime is decided by whoever rotates it. fullsend cannot change those
// semantics, but it can notice which route a run took and say so. The
// report is advisory: it never blocks the run and never prints a value.

// ciVariableCredentialPolicy carries what the report needs to know about
// the run: the effective forge platform and the harness opt-out
// (allow_ci_variable_credential), which downgrades the warning to an info
// line.
type ciVariableCredentialPolicy struct {
	forge string
	allow bool
}

// gitLab reports whether the run's forge resolved to GitLab.
func (p ciVariableCredentialPolicy) gitLab() bool {
	return p.forge == "gitlab"
}

// noKeylessReplacementOnGitLab ends a report line whose provider type has
// no entry in gitLabKeylessReplacements.
const noKeylessReplacementOnGitLab = "no keyless replacement on GitLab yet"

// gitLabKeylessReplacements maps a provider type to the keyless route that
// replaces a CI/CD-variable credential on GitLab. Provider types without an
// entry have no such route yet. Keep in sync with the table in
// docs/contributing/gitlab-role-credentials.md.
var gitLabKeylessReplacements = map[string]string{
	"fullsend-vertex-ai": "an `id_tokens:` OIDC token (FULLSEND_ID_TOKEN) exchanged through the gitlab-oidc Workload Identity provider",
}

// ciVariableRef matches a credential value that is exactly one ${VAR} or
// $VAR reference.
var ciVariableRef = regexp.MustCompile(`^\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))$`)

// credentialVariable returns the variable a credential value references
// when the value is exactly one ${VAR} or $VAR reference.
func credentialVariable(value string) (string, bool) {
	m := ciVariableRef.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	if m[1] != "" {
		return m[1], true
	}
	return m[2], true
}

// isGitLabIDToken reports whether v is shaped like a GitLab `id_tokens`
// JWT: three base64url segments whose claims carry an `iss` equal to
// ciServerURL. Only the claims are decoded — there is no signature check
// and no network call — because the question is which route the value
// took, not whether it is valid.
func isGitLabIDToken(v, ciServerURL string) bool {
	if ciServerURL == "" {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return false
	}
	var header map[string]any
	if !decodeJWTSegment(parts[0], &header) {
		return false
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if !decodeJWTSegment(parts[1], &claims) {
		return false
	}
	return claims.Issuer == ciServerURL
}

// decodeJWTSegment decodes one base64url JWT segment into out.
func decodeJWTSegment(seg string, out any) bool {
	data, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}

// fromCIVariable reports whether variable holds a CI/CD-variable credential:
// set, non-empty, and not a GitLab `id_tokens` JWT.
func fromCIVariable(variable string, getenv func(string) string) bool {
	v := getenv(variable)
	if v == "" {
		return false
	}
	return !isGitLabIDToken(v, getenv("CI_SERVER_URL"))
}

// ciVariableCredentialLine is the report line for one credential. It names
// the provider, the credential key and the variable, never the value.
func ciVariableCredentialLine(provider, providerType, key, variable string) string {
	replacement := noKeylessReplacementOnGitLab
	if r, ok := gitLabKeylessReplacements[strings.ToLower(providerType)]; ok {
		replacement = "keyless replacement: " + r
	}
	return fmt.Sprintf("provider %q: credential %s comes from CI/CD variable %s, a long-lived value masked by exact match only; %s", provider, key, variable, replacement)
}

// ciVariableCredentialLines returns the report lines for the provider
// definitions a run ensures, in definition order and then by credential
// key. Literal values, unset or empty variables and `id_tokens` JWTs
// produce nothing.
func ciVariableCredentialLines(defs []harness.ProviderDef, getenv func(string) string) []string {
	var lines []string
	for _, pd := range defs {
		keys := make([]string, 0, len(pd.Credentials))
		for k := range pd.Credentials {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			variable, ok := credentialVariable(pd.Credentials[k])
			if !ok || !fromCIVariable(variable, getenv) {
				continue
			}
			lines = append(lines, ciVariableCredentialLine(pd.Name, pd.Type, k, variable))
		}
	}
	return lines
}

// reportCIVariableLine prints one report line: a warning by default, an
// info line under the allow_ci_variable_credential opt-out.
func reportCIVariableLine(policy ciVariableCredentialPolicy, line string, printer *ui.Printer) {
	if policy.allow {
		printer.StepInfo(line)
		return
	}
	printer.StepWarn(line)
}

// reportCIVariableCredentials prints a line for each provider credential
// that comes from a CI/CD variable on a GitLab run. Other forges print
// nothing. The run continues either way.
func reportCIVariableCredentials(policy ciVariableCredentialPolicy, defs []harness.ProviderDef, getenv func(string) string, printer *ui.Printer) {
	if !policy.gitLab() {
		return
	}
	for _, line := range ciVariableCredentialLines(defs, getenv) {
		reportCIVariableLine(policy, line, printer)
	}
}
