package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// envSandboxSecurityKeys mirrors reservedCredentialKeys in
// internal/sandbox/sandbox.go, which is unexported and therefore unreadable
// from this package.
//
// This is deliberately a copy rather than a live probe. Probing the credential
// path (sandbox.EnsureProvider returns a reserved-key error before exec) would
// need the PATH guard its own test uses to keep a non-refused name from
// executing a real openshell, and t.Setenv in a sequential test leaks the
// mutated PATH to parallel tests elsewhere in this package. Reproducing the
// invariant without touching process state is the better trade here.
//
// The permanent fix is one shared list consumed by both doors; until then a
// name added to reservedCredentialKeys must be added here too. See
// internal/sandbox/reserved_env_drift_test.go, which guards the plugin-env
// door the same way.
var envSandboxSecurityKeys = []string{
	// Process loading / shell behavior — already reserved, kept to pin it.
	"PATH", "HOME", "SHELL", "LD_PRELOAD", "LD_LIBRARY_PATH", "BASH_ENV", "ENV",
	// Proxy — could redirect sandbox HTTP traffic
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	// Subprocess-influencing runtime vars
	"NODE_OPTIONS", "PYTHONPATH", "PROMPT_COMMAND",
	// Word splitting / cd resolution
	"IFS", "CDPATH",
	// Language runtime injection — arbitrary code at process start
	"JAVA_TOOL_OPTIONS", "RUBYOPT", "PERL5OPT", "PYTHONSTARTUP",
	// Dynamic linker audit and macOS interpose — shared-object loading
	"LD_AUDIT", "DYLD_INSERT_LIBRARIES",
	// TLS trust chain — validation redirected to an attacker-controlled CA
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS",
	// Hostname resolution redirect
	"HOSTALIASES",
	// Git config — can inject hooks or redirect operations
	"GIT_CONFIG_GLOBAL", "GIT_EXEC_PATH", "GIT_SSH_COMMAND", "GIT_TEMPLATE_DIR", "GIT_ASKPASS",
}

// TestReservedSandboxKeys_CoversReservedCredentialKeys pins the invariant that
// a variable unsafe as a provider credential key is equally unsafe injected via
// env.sandbox: both paths end up as environment variables in the same sandbox
// child process, so a gap between the two blocklists is a bypass of the
// credential deny-list through harness YAML (#3373).
func TestReservedSandboxKeys_CoversReservedCredentialKeys(t *testing.T) {
	t.Parallel()
	for _, key := range envSandboxSecurityKeys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			assert.Truef(t, reservedSandboxKeys[key],
				"reservedSandboxKeys must include %s: env.sandbox can inject a variable the credential path refuses", key)

			lines := buildSandboxEnvLines(&harness.Harness{
				Env: &harness.EnvConfig{Sandbox: map[string]string{key: "/tmp/evil"}},
			})
			assert.NotContainsf(t, strings.Join(lines, "\n"), "export "+key+"=",
				"buildSandboxEnvLines leaked %s into the sandbox environment", key)
		})
	}
}
