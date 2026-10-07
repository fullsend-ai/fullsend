package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// runnerSecretsFileEnv names a file that holds the runner secrets bundle,
// a JSON object of name → value (ADR 0136). In GitHub Actions the
// composite action writes the FULLSEND_RUNNER_SECRETS secret to a mode
// 0600 file under RUNNER_TEMP and passes only this path, so the bundle is
// never in the environment of fullsend run or of any process it starts.
// fullsend run deletes the file as soon as it has read it.
const runnerSecretsFileEnv = "FULLSEND_RUNNER_SECRETS_FILE"

// runnerSecretsEnv carries the same bundle inline. It is a fallback for
// local runs only: in CI the value would stay readable in
// /proc/<pid>/environ even after fullsend run unsets it. fullsend run
// removes it from its environment before any child process starts.
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
// names outside the refused families: GH_TOKEN, every role token in
// roleTokenVars, GITLAB_TOKEN and PATH. oidcDenyKeys, providerOnlyKeys and
// reservedSandboxKeys are refused as well (runnerSecretNameRefused), which
// keeps GH_WORKFLOW_TOKEN (ADR 0114) unforgeable.
var runnerSecretRefusedNames = func() map[string]bool {
	names := map[string]bool{
		"GH_TOKEN":     true,
		"GITLAB_TOKEN": true,
		"PATH":         true,
	}
	for _, vars := range roleTokenVars {
		for _, tv := range vars {
			names[tv.Name] = true
		}
	}
	return names
}()

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

// loadRunnerSecrets reads the runner secrets bundle from the file named by
// FULLSEND_RUNNER_SECRETS_FILE or, for local runs, from
// FULLSEND_RUNNER_SECRETS. It masks and registers every value for
// redaction and returns the validated name → value map. With neither set,
// or with a blank bundle, it returns a nil map: the run behaves as it did
// before the channel existed.
//
// Both variables are removed from the process environment and the file is
// deleted before anything else, even when reading or parsing fails, so a
// malformed bundle still never reaches a child process. Values are masked
// before validation so a refused entry's value is hidden too. Errors never
// quote values.
func loadRunnerSecrets() (map[string]string, error) {
	path, hasFile := os.LookupEnv(runnerSecretsFileEnv)
	inline, hasInline := os.LookupEnv(runnerSecretsEnv)
	for _, name := range []string{runnerSecretsFileEnv, runnerSecretsEnv} {
		if err := os.Unsetenv(name); err != nil {
			return nil, fmt.Errorf("removing %s from the environment: %w", name, err)
		}
	}
	// The composite action always sets the path variable; it is empty when
	// the FULLSEND_RUNNER_SECRETS secret is unset.
	hasFile = hasFile && strings.TrimSpace(path) != ""

	source, raw := runnerSecretsEnv, inline
	if hasFile {
		source = runnerSecretsFileEnv
		data, readErr := os.ReadFile(path)
		removeErr := os.Remove(path)
		if readErr != nil {
			return nil, fmt.Errorf("reading the %s file: %w", runnerSecretsFileEnv, readErr)
		}
		if removeErr != nil {
			return nil, fmt.Errorf("deleting the %s file: %w", runnerSecretsFileEnv, removeErr)
		}
		if hasInline {
			return nil, fmt.Errorf("set %s or %s, not both", runnerSecretsFileEnv, runnerSecretsEnv)
		}
		raw = string(data)
	} else if !hasInline {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	entries, problems, err := parseRunnerSecretsBundle([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%s must hold a JSON object of name → string value", source)
	}
	secrets := make(map[string]string, len(entries))
	for _, e := range entries {
		maskRunnerSecretValue(e.value)
		redactable := registerRunnerSecretValue(e.value)
		switch {
		case e.duplicate:
			problems = append(problems, fmt.Sprintf("%s: duplicate key", e.name))
		case !validEnvKeyRe.MatchString(e.name):
			problems = append(problems, fmt.Sprintf("%q: not a valid environment variable name", e.name))
		case runnerSecretNameRefused(e.name):
			problems = append(problems, fmt.Sprintf("%s: name is reserved for the runner", e.name))
		case !redactable:
			problems = append(problems, fmt.Sprintf("%s: value is too short to redact from logs", e.name))
		default:
			secrets[e.name] = e.value
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%s has %d invalid entr(ies):\n    %s", source, len(problems), strings.Join(problems, "\n    "))
	}
	return secrets, nil
}

// runnerSecretEntry is one name → string value pair of the bundle.
// duplicate marks a repeat of a name seen earlier in the object; its value
// is still returned so the caller masks it.
type runnerSecretEntry struct {
	name      string
	value     string
	duplicate bool
}

// errRunnerSecretsShape reports a bundle that is not a single JSON object.
// It carries no detail from the decoder, which could quote the input.
var errRunnerSecretsShape = errors.New("not a single JSON object")

// parseRunnerSecretsBundle walks raw token by token so it can refuse what
// json.Unmarshal into a map would accept silently: a duplicate key (the
// last one would win) and a null value (it would decode as ""). It returns
// every string entry, the problems with non-string values, and
// errRunnerSecretsShape when raw is not a single JSON object. No problem
// quotes a value.
func parseRunnerSecretsBundle(raw []byte) ([]runnerSecretEntry, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, errRunnerSecretsShape
	}
	var entries []runnerSecretEntry
	var problems []string
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, errRunnerSecretsShape
		}
		name, _ := tok.(string)
		var rawValue json.RawMessage
		if err := dec.Decode(&rawValue); err != nil {
			return nil, nil, errRunnerSecretsShape
		}
		var value string
		switch {
		case string(rawValue) == "null":
			problems = append(problems, fmt.Sprintf("%s: value must be a string, not null", name))
		case json.Unmarshal(rawValue, &value) != nil:
			problems = append(problems, fmt.Sprintf("%s: value must be a string", name))
		default:
			entries = append(entries, runnerSecretEntry{name: name, value: value, duplicate: seen[name]})
		}
		seen[name] = true
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, nil, errRunnerSecretsShape
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errRunnerSecretsShape
	}
	return entries, problems, nil
}

// maskRunnerSecretValue masks a runner secret in the GitHub Actions log:
// the whole value, and each non-blank line of a multi-line value so a
// script that prints one line of it is masked too.
func maskRunnerSecretValue(value string) {
	maskActionsValue(value)
	if !strings.ContainsAny(value, "\r\n") {
		return
	}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			maskActionsValue(line)
		}
	}
}

// registerRunnerSecretValue registers a runner secret with the
// process-wide redactor and reports whether the whole value was long
// enough to register. It also registers each line of a multi-line value
// and the JSON-escaped form when that differs, so a script that prints one
// line or embeds the value in JSON output is still redacted. A line or
// escaped form below the redactor's minimum length is skipped.
func registerRunnerSecretValue(value string) bool {
	if !security.RegisterRuntimeSecret(value) {
		return false
	}
	if strings.ContainsAny(value, "\r\n") {
		for _, line := range strings.Split(value, "\n") {
			security.RegisterRuntimeSecret(strings.TrimRight(line, "\r"))
		}
	}
	if escaped := jsonEscapedString(value); escaped != value {
		security.RegisterRuntimeSecret(escaped)
	}
	return true
}

// jsonEscapedString returns value as it appears inside a JSON string
// literal, without the surrounding quotes.
func jsonEscapedString(value string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value) // encoding a string cannot fail
	quoted := strings.TrimSuffix(buf.String(), "\n")
	return quoted[1 : len(quoted)-1]
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

// referencedRunnerSecretNames returns, sorted and de-duplicated, the
// runner secret names the harness's env.runner values reference: the only
// names a host-side script can receive. Call it before env.runner is
// expanded. Names are not secret; the run log lists these and not the
// rest of the bundle.
func referencedRunnerSecretNames(h *harness.Harness, secrets map[string]string) []string {
	if len(secrets) == 0 || h.Env == nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, v := range h.Env.Runner {
		for _, n := range runnerSecretRefs(v, secrets) {
			seen[n] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
