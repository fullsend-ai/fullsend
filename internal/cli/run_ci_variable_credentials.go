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

// ciVariableCredentialPolicy decides whether a run reports provider
// credentials that come from GitLab CI/CD variables, and at which level.
// The report never blocks the run and never prints a value.
type ciVariableCredentialPolicy struct {
	forge    string // effective forge platform
	gitlabCI bool   // GITLAB_CI=true: the runner is a GitLab CI job
	allow    bool   // allow_ci_variable_credential: info line, not a warning
}

// gitLab reports whether the run's forge resolved to GitLab.
func (p ciVariableCredentialPolicy) gitLab() bool {
	return p.forge == "gitlab"
}

// reports reports whether CI/CD-variable credentials are reported: a
// GitLab run inside a GitLab CI job. A local run has no CI/CD variables.
func (p ciVariableCredentialPolicy) reports() bool {
	return p.gitLab() && p.gitlabCI
}

// noKeylessReplacementOnGitLab ends a report line whose provider type has
// no entry in gitLabKeylessReplacements.
const noKeylessReplacementOnGitLab = "no keyless replacement on GitLab yet"

// gitLabKeylessReplacements maps a provider type to the keyless route that
// replaces a CI/CD-variable credential on GitLab. Provider types without an
// entry have no such route yet. The shipped vertex-ai definition declares
// no credentials, so the entry applies to custom Vertex definitions. Keep
// in sync with the table in docs/contributing/gitlab-role-credentials.md.
var gitLabKeylessReplacements = map[string]string{
	"fullsend-vertex-ai": "an `id_tokens:` OIDC token (FULLSEND_ID_TOKEN) exchanged through the gitlab-oidc Workload Identity provider",
}

// ciVariableSource returns the CI/CD variable a runner variable is set
// from. The GitLab agent job exports two credentials under a different
// name: OPENAI_API_KEY from FULLSEND_OPENAI_API_KEY (run-agent-job.sh), and
// GITLAB_TOKEN from the selected role secret, which fullsend run publishes
// as FULLSEND_GITLAB_ROLE_SECRET. Other variables are their own source.
func ciVariableSource(variable string, getenv func(string) string) string {
	switch variable {
	case openAIStaticKeyEnv:
		return "FULLSEND_OPENAI_API_KEY"
	case "GITLAB_TOKEN":
		if s := getenv(envGitLabRoleSecret); envVarName.MatchString(s) {
			return s
		}
	}
	return variable
}

// envVarName matches a shell environment variable name.
var envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	parts := strings.Split(strings.TrimSpace(v), ".")
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
// the provider, the credential key, the CI/CD variable and, when it
// differs, the runner variable, never the value.
func ciVariableCredentialLine(provider, providerType, key, variable string, getenv func(string) string) string {
	replacement := noKeylessReplacementOnGitLab
	if r, ok := gitLabKeylessReplacements[strings.ToLower(providerType)]; ok {
		replacement = "keyless replacement: " + r
	}
	from := ciVariableSource(variable, getenv)
	if from != variable {
		from += " (runner variable " + variable + ")"
	}
	return fmt.Sprintf("provider %q: credential %s comes from CI/CD variable %s, a long-lived value that masking hides only as an exact string; %s", provider, key, from, replacement)
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
			// A key that is not a variable name is never printed.
			variable, ok := credentialVariable(pd.Credentials[k])
			if !ok || !envVarName.MatchString(k) || !fromCIVariable(variable, getenv) {
				continue
			}
			lines = append(lines, ciVariableCredentialLine(pd.Name, pd.Type, k, variable, getenv))
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
// that comes from a CI/CD variable when policy.reports().
func reportCIVariableCredentials(policy ciVariableCredentialPolicy, defs []harness.ProviderDef, getenv func(string) string, printer *ui.Printer) {
	if !policy.reports() {
		return
	}
	for _, line := range ciVariableCredentialLines(defs, getenv) {
		reportCIVariableLine(policy, line, printer)
	}
}
