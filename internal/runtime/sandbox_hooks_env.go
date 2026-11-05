package runtime

import (
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// The sandbox hook scripts read their settings from the environment. Bootstrap
// writes the SandboxHookConfig-derived ones into the workspace .env
// (appendHookEnv), and the harness env writes FULLSEND_CANARY_TOKEN and
// FULLSEND_TOOL_ALLOWLIST there. That file is agent-writable, so iteration 1
// could widen the SSRF allowlist, clear TIRITH_REQUIRED or delete the canary
// token for iteration 2.
//
// Every runtime that installs the hooks therefore records the values at
// Bootstrap (bootstrapHookEnv), keeps them runner-side beside its other
// runner-held digests, and re-asserts them in the launch after the .env load
// (hookEnvExports). A managed name the runner has no value for is unset there,
// so a value only .env carries never reaches the hooks.

// hookEnvPair is one runner-owned hook environment assignment.
type hookEnvPair struct {
	Key   string
	Value string
}

// hookEnvManagedKeys are every hook setting the launch takes from the runner
// rather than from .env, in the order the launch renders them.
var hookEnvManagedKeys = []string{
	"TIRITH_FAIL_ON",
	"TIRITH_REQUIRED",
	"FULLSEND_EGRESS_ALLOWLIST",
	"FULLSEND_CANARY_TOKEN",
	"FULLSEND_TOOL_ALLOWLIST",
}

// harnessHookEnvKeys are the hook-configuration variables the harness
// supplies through the sandbox environment rather than through
// SandboxHookConfig, so the runner learns them by reading the file it just
// wrote rather than from its own state.
var harnessHookEnvKeys = []string{"FULLSEND_CANARY_TOKEN", "FULLSEND_TOOL_ALLOWLIST"}

// hookEnvReadSeparator delimits the values read back, chosen so a plausible
// token or allowlist cannot contain it.
const hookEnvReadSeparator = "|fullsend-env-sep|"

// configHookEnv returns the hook configuration values the runner derives from
// SandboxHookConfig — the same values appendHookEnv writes into .env — in a
// stable order.
//
// The harness-supplied pair (FULLSEND_CANARY_TOKEN and
// FULLSEND_TOOL_ALLOWLIST) has no typed runner-side copy, so Bootstrap reads
// it from the workspace .env instead (readHarnessHookEnv) while that file is
// still pristine; bootstrapHookEnv joins the two.
func configHookEnv(hooks security.SandboxHookConfig) []hookEnvPair {
	var env []hookEnvPair
	if failOn := hooks.TirithFailOn(); failOn != "" {
		env = append(env, hookEnvPair{"TIRITH_FAIL_ON", failOn})
	}
	if hooks.TirithRequired() {
		env = append(env, hookEnvPair{"TIRITH_REQUIRED", "1"})
	}
	allowlist := hooks.SSRFEgressAllowlist()
	if entry := hooks.ForgeEgressEntry(); entry != "" {
		if allowlist == "" {
			allowlist = entry
		} else {
			allowlist += "," + entry
		}
	}
	if allowlist != "" {
		env = append(env, hookEnvPair{"FULLSEND_EGRESS_ALLOWLIST", allowlist})
	}
	return env
}

// readHarnessHookEnvCmd reads envFile as data, as the launch does
// (envDataLoad, #8574), so no .env or .env.d line runs and nothing lands in
// front of the first value, and prints the harnessHookEnvKeys values joined
// by hookEnvReadSeparator. A .env (or a file it names) that exists but cannot
// be read prints nothing and exits non-zero, so it fails Bootstrap instead of
// pinning the values as unset; a line the loader does not take is skipped, as
// at launch. The loader reports a dropped line by file and line only, never
// by content, so its stderr is kept for the error message. The references are
// double-quoted, not shellQuote'd: single quotes would print the literal
// "${KEY:-}" text instead of expanding it.
func readHarnessHookEnvCmd(envFile string) string {
	var refs []string
	for _, key := range harnessHookEnvKeys {
		refs = append(refs, fmt.Sprintf("${%s:-}", key))
	}
	return fmt.Sprintf(`%s && printf '%%s' "%s"`,
		EnvDataLoad(envFile), strings.Join(refs, hookEnvReadSeparator))
}

// readHarnessHookEnv reads the harness-supplied hook variables from the
// workspace .env, which at Bootstrap time is still exactly what the runner
// wrote: the runner writes .env and then calls Bootstrap, and no agent
// iteration has run. Reading them later — in Run — would read whatever the
// previous iteration left behind. An unset value is recorded as empty, which
// hookEnvExports turns into an unset at launch.
func readHarnessHookEnv(sandboxName string) ([]hookEnvPair, error) {
	cmd := readHarnessHookEnvCmd(sandbox.SandboxWorkspace + "/.env")
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reading the harness hook environment: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("reading the harness hook environment: exited %d: %s",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}

	values := strings.Split(stdout, hookEnvReadSeparator)
	if len(values) != len(harnessHookEnvKeys) {
		return nil, fmt.Errorf("reading the harness hook environment: expected %d values, got %d",
			len(harnessHookEnvKeys), len(values))
	}
	var env []hookEnvPair
	for i, key := range harnessHookEnvKeys {
		env = append(env, hookEnvPair{key, strings.TrimSpace(values[i])})
	}
	return env, nil
}

// bootstrapHookEnv records the hook settings the runner knows at Bootstrap:
// what it derives from SandboxHookConfig, plus the harness-supplied pair read
// back from the workspace .env. Call it before the first iteration — and
// before appendHookEnv, so the read sees exactly what the runner wrote.
func bootstrapHookEnv(sandboxName string, hooks security.SandboxHookConfig) ([]hookEnvPair, error) {
	harnessEnv, err := readHarnessHookEnv(sandboxName)
	if err != nil {
		return nil, err
	}
	return append(configHookEnv(hooks), harnessEnv...), nil
}

// hookEnvExports renders the POSIX sh fragment the launch runs after the .env
// load: every hookEnvManagedKeys name the runner recorded a value for is
// exported with that value, and every other one is unset, so neither a .env
// that changes a value nor one that adds a name the harness left out reaches
// the hooks. `unset` and `export` are special builtins, which a function
// cannot shadow. An empty value counts as no value: every hook treats empty
// and unset alike.
func hookEnvExports(env []hookEnvPair) string {
	values := make(map[string]string, len(env))
	for _, pair := range env {
		values[pair.Key] = pair.Value
	}
	var unset, parts []string
	for _, key := range hookEnvManagedKeys {
		if v := values[key]; v != "" {
			parts = append(parts, "export "+key+"="+shellQuote(v))
		} else {
			unset = append(unset, key)
		}
	}
	if len(unset) > 0 {
		parts = append([]string{"unset " + strings.Join(unset, " ")}, parts...)
	}
	return strings.Join(parts, " && ")
}
