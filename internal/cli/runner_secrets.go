package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// runnerSecretsEnv carries user secrets for host-side scripts as one JSON
// object of name → value (ADR 0136). On GitHub the reusable workflow
// passes the FULLSEND_RUNNER_SECRETS repository or organization secret to
// the fullsend run step only. fullsend run unpacks it, resolves ${NAME}
// references in the harness's env.runner from it, and removes it from the
// process environment so no child process ever inherits the bundle.
const runnerSecretsEnv = "FULLSEND_RUNNER_SECRETS"

// runnerSecretRefusedPrefixes are variable families owned by the runner
// or the CI platform. A runner secret may not shadow any of them.
var runnerSecretRefusedPrefixes = []string{
	"FULLSEND_",
	"GITHUB_",
	"ACTIONS_",
	"RUNNER_",
	"CI_",
	"LD_",
}

// runnerSecretRefusedNames are minted role tokens and other runner-owned
// names outside the refused families. oidcDenyKeys, providerOnlyKeys and
// reservedSandboxKeys are refused as well (runnerSecretNameRefused), which
// keeps GH_WORKFLOW_TOKEN (ADR 0114) unforgeable.
var runnerSecretRefusedNames = map[string]bool{
	"GH_TOKEN":     true,
	"PUSH_TOKEN":   true,
	"REVIEW_TOKEN": true,
	"GITLAB_TOKEN": true,
	"PATH":         true,
}

// runnerSecretNameRefused reports whether name is runner-owned and so may
// neither be a key in FULLSEND_RUNNER_SECRETS nor receive a runner secret
// value as an env.runner key.
func runnerSecretNameRefused(name string) bool {
	if harnessExpansionDenied(name) || reservedSandboxKeys[name] || runnerSecretRefusedNames[name] {
		return true
	}
	for _, p := range runnerSecretRefusedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// loadRunnerSecrets reads FULLSEND_RUNNER_SECRETS, removes it from the
// process environment, masks and registers every value for redaction, and
// returns the validated name → value map. An unset or blank variable
// returns a nil map: the run behaves as it did before the channel existed.
//
// The variable is removed before anything else, even when parsing fails,
// so a malformed bundle still never reaches a child process. Values are
// masked before validation so a refused entry's value is hidden too.
// Errors never quote values.
func loadRunnerSecrets() (map[string]string, error) {
	raw, present := os.LookupEnv(runnerSecretsEnv)
	if !present {
		return nil, nil
	}
	if err := os.Unsetenv(runnerSecretsEnv); err != nil {
		return nil, fmt.Errorf("removing %s from the environment: %w", runnerSecretsEnv, err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	maskActionsValue(raw)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s must be a JSON object of name → string value", runnerSecretsEnv)
	}
	secrets := make(map[string]string, len(fields))
	var problems []string
	for name, rawValue := range fields {
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			problems = append(problems, fmt.Sprintf("%s: value must be a string", name))
			continue
		}
		maskRunnerSecretValue(value)
		security.RegisterRuntimeSecret(value)
		switch {
		case !validEnvKeyRe.MatchString(name):
			problems = append(problems, fmt.Sprintf("%q: not a valid environment variable name", name))
		case runnerSecretNameRefused(name):
			problems = append(problems, fmt.Sprintf("%s: name is reserved for the runner", name))
		default:
			secrets[name] = value
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%s has %d invalid entr(ies):\n    %s", runnerSecretsEnv, len(problems), strings.Join(problems, "\n    "))
	}
	return secrets, nil
}

// maskRunnerSecretValue masks a runner secret in the GitHub Actions log.
// The workflow command masks a single line, so a multi-line value is
// masked line by line.
func maskRunnerSecretValue(value string) {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			maskActionsValue(line)
		}
	}
}

// runnerSecretNameSet returns the names of secrets as a set, or nil.
func runnerSecretNameSet(secrets map[string]string) map[string]bool {
	if len(secrets) == 0 {
		return nil
	}
	names := make(map[string]bool, len(secrets))
	for n := range secrets {
		names[n] = true
	}
	return names
}

// runnerSecretRefs returns the runner secret names value references,
// sorted and de-duplicated.
func runnerSecretRefs(value string, secrets map[string]string) []string {
	seen := make(map[string]bool)
	for _, n := range harness.EnvRefNames(value) {
		if _, ok := secrets[n]; ok {
			seen[n] = true
		}
	}
	refs := make([]string, 0, len(seen))
	for n := range seen {
		refs = append(refs, n)
	}
	sort.Strings(refs)
	return refs
}

// runnerSecretErrors collects reference violations and formats them as one
// error.
type runnerSecretErrors []string

func (e *runnerSecretErrors) refuse(source string, refs []string) {
	for _, n := range refs {
		*e = append(*e, fmt.Sprintf("%s references runner secret %s; runner secrets may be referenced only from env.runner", source, n))
	}
}

func (e runnerSecretErrors) err() error {
	if len(e) == 0 {
		return nil
	}
	sort.Strings(e)
	return fmt.Errorf("%d runner secret reference error(s):\n    %s", len(e), strings.Join(e, "\n    "))
}

// validateRunnerSecretRefs enforces that the resolved harness references
// runner secrets only from env.runner values (ADR 0136), so a passthrough
// value stays on the host: env.sandbox, runner_env, host_files sources and
// validation_loop fields may not reference one. An env.runner key that is
// runner-owned may not receive one either, so a passthrough value cannot
// shadow a runner variable in a host-side script.
func validateRunnerSecretRefs(h *harness.Harness, secrets map[string]string) error {
	if len(secrets) == 0 {
		return nil
	}
	var errs runnerSecretErrors
	for k, v := range h.RunnerEnv {
		errs.refuse(fmt.Sprintf("runner_env[%s]", k), runnerSecretRefs(v, secrets))
	}
	if h.Env != nil {
		for k, v := range h.Env.Sandbox {
			errs.refuse(fmt.Sprintf("env.sandbox[%s]", k), runnerSecretRefs(v, secrets))
		}
		for k, v := range h.Env.Runner {
			if refs := runnerSecretRefs(v, secrets); len(refs) > 0 && runnerSecretNameRefused(k) {
				errs = append(errs, fmt.Sprintf("env.runner[%s] receives runner secret %s, but %s is reserved for the runner", k, strings.Join(refs, ", "), k))
			}
		}
	}
	for i, hf := range h.HostFiles {
		errs.refuse(fmt.Sprintf("host_files[%d].src", i), runnerSecretRefs(hf.Src, secrets))
	}
	if h.ValidationLoop != nil {
		errs.refuse("validation_loop.schema", runnerSecretRefs(h.ValidationLoop.Schema, secrets))
		errs.refuse("validation_loop.preflight_check", runnerSecretRefs(h.ValidationLoop.PreflightCheck, secrets))
	}
	return errs.err()
}

// validateRunnerSecretHostFiles refuses a host_files entry with expand:
// true whose content references a runner secret: the expanded file is
// copied into the sandbox. Files that do not resolve or cannot be read are
// skipped here; bootstrapEnv reports them when it copies host files.
func validateRunnerSecretHostFiles(h *harness.Harness, secrets map[string]string) error {
	if len(secrets) == 0 {
		return nil
	}
	var errs runnerSecretErrors
	for i, hf := range h.HostFiles {
		if !hf.Expand {
			continue
		}
		path, ok := hostFileSource(hf)
		if !ok {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		errs.refuse(fmt.Sprintf("host_files[%d] (expand: true) content", i), runnerSecretRefs(string(raw), secrets))
	}
	return errs.err()
}

// validateRunnerSecretProviders refuses a provider definition whose
// credential or config value references a runner secret: provider
// credentials are delivered to the sandbox.
func validateRunnerSecretProviders(defs []harness.ProviderDef, secrets map[string]string) error {
	if len(secrets) == 0 {
		return nil
	}
	var errs runnerSecretErrors
	for _, d := range defs {
		for k, v := range d.Credentials {
			errs.refuse(fmt.Sprintf("provider %s credentials[%s]", d.Name, k), runnerSecretRefs(v, secrets))
		}
		for k, v := range d.Config {
			errs.refuse(fmt.Sprintf("provider %s config[%s]", d.Name, k), runnerSecretRefs(v, secrets))
		}
	}
	return errs.err()
}

// withRunnerSecretExpander returns an expander that resolves runner secret
// names from secrets and every other name through fallback. Use it only
// for env.runner values.
func withRunnerSecretExpander(secrets map[string]string, fallback func(string) string) func(string) string {
	if len(secrets) == 0 {
		return fallback
	}
	return func(key string) string {
		if v, ok := secrets[key]; ok {
			return v
		}
		return fallback(key)
	}
}

// withRunnerSecretLookup is withRunnerSecretExpander for the validation
// lookup. It is safe to apply to every site ValidateRunnerEnvWith checks
// because validateRunnerSecretRefs has already refused runner secret
// references outside env.runner.
func withRunnerSecretLookup(secrets map[string]string, fallback func(string) (string, bool)) func(string) (string, bool) {
	if len(secrets) == 0 {
		return fallback
	}
	return func(key string) (string, bool) {
		if v, ok := secrets[key]; ok {
			return v, true
		}
		return fallback(key)
	}
}

// sortedRunnerSecretNames returns the secret names in sorted order for
// display. Names are not secret; values never leave this file unmasked.
func sortedRunnerSecretNames(secrets map[string]string) []string {
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
