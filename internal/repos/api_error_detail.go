package repos

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Verbose API error detail (#9364).
//
// By default safeAPIError withholds the server's error text, because a
// credential-bearing call can fail before its credential values are known
// to a credentialRedactor. With verbose API errors enabled (fullsend repos
// --verbose) the underlying error text, such as GitLab's HTTP status and
// response message, is appended after it has been scrubbed of:
//
//   - every credential value any credentialRedactor registered while
//     verbose errors were enabled;
//   - credential shapes matched by verboseCredentialPatterns (GitLab token
//     prefixes, token query parameters, URL user info, long hex secrets);
//   - whatever the scrubber passed to EnableVerboseAPIErrors removes (the
//     CLI supplies its known flag values and the shared pattern redactor).
//
// Only failure text is ever shown; successful responses, which may carry
// newly minted credentials, never reach an error.

var verbose struct {
	mu      sync.RWMutex
	scrub   func(string) string
	enabled bool
	secrets []string
}

// EnableVerboseAPIErrors makes safeAPIError and the role provisioning
// failure reasons include the scrubbed underlying error text. scrub runs
// before the package's own credential scrubbing; nil means no extra pass.
// The returned function restores the default (withheld) behavior.
func EnableVerboseAPIErrors(scrub func(string) string) (restore func()) {
	verbose.mu.Lock()
	defer verbose.mu.Unlock()
	verbose.enabled = true
	verbose.scrub = scrub
	return func() {
		verbose.mu.Lock()
		defer verbose.mu.Unlock()
		verbose.enabled = false
		verbose.scrub = nil
		verbose.secrets = nil
	}
}

// rememberVerboseSecrets records credential values so verbose error text
// is scrubbed of them. It keeps nothing unless verbose errors are enabled.
func rememberVerboseSecrets(values ...string) {
	verbose.mu.Lock()
	defer verbose.mu.Unlock()
	if !verbose.enabled {
		return
	}
	for _, v := range values {
		if v != "" {
			verbose.secrets = append(verbose.secrets, v)
		}
	}
}

// verboseCredentialPatterns match credential shapes that can appear in
// GitLab error text. Each match is replaced with credentialRedacted; the
// optional "pre" and "post" groups are kept around it.
var verboseCredentialPatterns = []*regexp.Regexp{
	// GitLab token prefixes: personal/project/group access, OAuth, deploy,
	// runner, CI job, pipeline trigger, feed, incoming mail, agent, service
	// account, feature flag client, SCIM, and runner registration tokens.
	regexp.MustCompile(`\b(?:glpat|gloas|gldt|glrt|glrtr|glcbt|glptt|glft|glimt|glagent|glsoat|glffct|gltst|glwt)-[A-Za-z0-9_.\-]{8,}`),
	regexp.MustCompile(`\bGR1348941[A-Za-z0-9_\-]{8,}`),
	// Token-bearing query parameters, e.g. a pipeline trigger URL or
	// private_token, access_token, job_token, or any other *token parameter.
	regexp.MustCompile(`(?i)(?P<pre>[?&][A-Za-z0-9_.\-]*token=)[^&\s"'<>]+`),
	// User info embedded in a URL.
	regexp.MustCompile(`(?P<pre>://)[^/@\s"'<>]+(?P<post>@)`),
	// Long hex strings: webhook secrets and legacy trigger tokens.
	regexp.MustCompile(`\b[0-9A-Fa-f]{32,}\b`),
}

// scrubVerboseDetail removes credential values and shapes from text that
// is about to be shown in verbose mode.
func scrubVerboseDetail(text string, scrub func(string) string, secrets []string) string {
	if scrub != nil {
		text = scrub(text)
	}
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, credentialRedacted)
	}
	for _, re := range verboseCredentialPatterns {
		text = re.ReplaceAllString(text, "${pre}"+credentialRedacted+"${post}")
	}
	return text
}

// verboseDetail returns err's scrubbed text when verbose API errors are
// enabled, and "" otherwise. extra lists credential values the caller
// submitted in the failed call, scrubbed in addition to the remembered ones.
func verboseDetail(err error, extra ...string) string {
	if err == nil {
		return ""
	}
	verbose.mu.RLock()
	enabled, scrub := verbose.enabled, verbose.scrub
	secrets := append(slices.Clone(verbose.secrets), extra...)
	verbose.mu.RUnlock()
	if !enabled {
		return ""
	}
	secrets = slices.DeleteFunc(secrets, func(s string) bool { return s == "" })
	return scrubVerboseDetail(err.Error(), scrub, secrets)
}

// failureReason returns reason, followed in verbose mode by the scrubbed
// text of the error that caused it. submitted lists credential values sent
// in the failed call so an error echoing one never shows it.
func failureReason(reason string, err error, submitted ...string) string {
	if detail := verboseDetail(err, submitted...); detail != "" {
		return reason + ": " + detail
	}
	return reason
}

// suppliedUnresolvedText returns the text of the error in err's chain that
// directly wraps ErrSuppliedCredentialUnresolved, or "" when there is
// none. Every such error in this package and internal/gitlablifecycle is
// built from static text, resource names, and causes already passed
// through safeAPIError, and carries recovery instructions (for example
// "re-enroll the credential with --gitlab-role-token"), so it is shown
// even when server error text is withheld. Wrappers above it are not
// shown, since they may carry unsanitized text.
func suppliedUnresolvedText(err error) string {
	if err == nil {
		return ""
	}
	if err == ErrSuppliedCredentialUnresolved { //nolint:errorlint // identity check on the sentinel itself
		return err.Error()
	}
	var children []error
	switch u := err.(type) { //nolint:errorlint // walking the chain one level at a time
	case interface{ Unwrap() error }:
		if c := u.Unwrap(); c != nil {
			children = []error{c}
		}
	case interface{ Unwrap() []error }:
		children = u.Unwrap()
	}
	if slices.Contains(children, ErrSuppliedCredentialUnresolved) {
		return err.Error()
	}
	for _, c := range children {
		if text := suppliedUnresolvedText(c); text != "" {
			return text
		}
	}
	return ""
}
