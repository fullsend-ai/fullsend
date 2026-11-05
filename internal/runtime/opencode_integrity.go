package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

const openCodeEnvReadSeparator = "|fullsend-opencode-env-sep|"

type openCodeTrustedEnv struct {
	ConfigContent   string
	CredentialsPath string
	// AgentPath and AgentDefinition are the translated agent file Bootstrap
	// uploaded. Run clears agent overrides, re-uploads it and re-verifies it
	// before every iteration (restoreOpenCodeAgent).
	AgentPath       string
	AgentDefinition string
	// AgentModel is the agent definition's frontmatter model. A harness or
	// CLI model wins over it (EffectiveModel), as on Claude Code and Codex.
	AgentModel string
}

// openCodeRunnerEnv holds values Bootstrap reads before the first agent
// iteration. Bootstrap and Run execute in the same one-shot CLI process, and a
// sandbox name is unique per run. If fullsend becomes a long-lived service,
// entries should be dropped when each run finishes.
var openCodeRunnerEnv sync.Map

func recordOpenCodeTrustedEnv(sandboxName string, env openCodeTrustedEnv) {
	openCodeRunnerEnv.Store(sandboxName, env)
}

func lookupOpenCodeTrustedEnv(sandboxName string) (openCodeTrustedEnv, bool) {
	value, ok := openCodeRunnerEnv.Load(sandboxName)
	if !ok {
		return openCodeTrustedEnv{}, false
	}
	env, ok := value.(openCodeTrustedEnv)
	return env, ok
}

func forgetOpenCodeTrustedEnv(sandboxName string) {
	openCodeRunnerEnv.Delete(sandboxName)
}

func openCodeReadTrustedEnv(sandboxName string) (openCodeTrustedEnv, error) {
	envFile := sandbox.SandboxWorkspace + "/.env"
	cmd := openCodeTrustedEnvReadCommand(envFile)
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return openCodeTrustedEnv{}, fmt.Errorf("reading the trusted OpenCode environment: %w", err)
	}
	if exitCode != 0 {
		return openCodeTrustedEnv{}, fmt.Errorf("reading the trusted OpenCode environment: exited %d: %s",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	return parseOpenCodeTrustedEnv(stdout)
}

// openCodeTrustedEnvReadCommand reads .env as data (envDataLoad, #8574), as
// the launch does, and prints the two trusted values. The loader reports a
// dropped line by file and line only, so its stderr can be shown on failure.
func openCodeTrustedEnvReadCommand(envFile string) string {
	return openCodeUtilityPin() + " && " + envDataTrustedPin() + " && " + envDataLoad(envFile, openCodeEnvDataDrop...) + ` && "$` + openCodePrintfVar + `" '%s' "${OPENCODE_CONFIG_CONTENT-}" ` +
		shellQuote(openCodeEnvReadSeparator) + ` "${GOOGLE_APPLICATION_CREDENTIALS-}"`
}

func parseOpenCodeTrustedEnv(output string) (openCodeTrustedEnv, error) {
	configContent, credentialsPath, ok := strings.Cut(output, openCodeEnvReadSeparator)
	if !ok {
		return openCodeTrustedEnv{}, fmt.Errorf("reading the trusted OpenCode environment: malformed response")
	}
	env := openCodeTrustedEnv{ConfigContent: configContent, CredentialsPath: credentialsPath}
	if err := validateOpenCodeTrustedEnv(env); err != nil {
		return openCodeTrustedEnv{}, err
	}
	return env, nil
}

func validateOpenCodeTrustedEnv(env openCodeTrustedEnv) error {
	if strings.TrimSpace(env.ConfigContent) == "" {
		return fmt.Errorf("reading the trusted OpenCode environment: OPENCODE_CONFIG_CONTENT is empty")
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.ConfigContent), &config); err != nil {
		return fmt.Errorf("reading the trusted OpenCode environment: OPENCODE_CONFIG_CONTENT is invalid JSON: %w", err)
	}
	permission, ok := config["permission"]
	if !ok {
		return fmt.Errorf("reading the trusted OpenCode environment: OPENCODE_CONFIG_CONTENT has no permission policy")
	}
	if err := validateOpenCodePermissionPolicy(permission); err != nil {
		return fmt.Errorf("reading the trusted OpenCode environment: OPENCODE_CONFIG_CONTENT has invalid permission policy: %w", err)
	}
	if strings.TrimSpace(env.CredentialsPath) == "" {
		return fmt.Errorf("reading the trusted OpenCode environment: GOOGLE_APPLICATION_CREDENTIALS is empty")
	}
	return nil
}

func validateOpenCodePermissionPolicy(raw json.RawMessage) error {
	var policy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &policy); err != nil || len(policy) == 0 {
		return fmt.Errorf("permission policy must be a non-empty object")
	}
	// Go keeps a duplicate key's last value and position, JSON.parse its
	// last value at the first position, so the runner's reading of a policy
	// with duplicates could differ from OpenCode's.
	if key, ok := openCodeDuplicateKey(raw); ok {
		return fmt.Errorf("duplicate permission key %q", key)
	}
	for _, value := range policy {
		var action string
		if err := json.Unmarshal(value, &action); err == nil {
			if action != "allow" && action != "deny" && action != "ask" {
				return fmt.Errorf("invalid permission action %q", action)
			}
			continue
		}

		var patterns map[string]string
		if err := json.Unmarshal(value, &patterns); err != nil || len(patterns) == 0 {
			return fmt.Errorf("permission value must be an action or non-empty pattern map")
		}
		for _, patternAction := range patterns {
			if patternAction != "allow" && patternAction != "deny" && patternAction != "ask" {
				return fmt.Errorf("invalid permission action %q", patternAction)
			}
		}
	}
	return nil
}

// openCodeAgentFallbackMarker is the stderr text `opencode run --agent`
// prints (cli/cmd/run.ts) when the named agent is missing or is a subagent
// and it runs OpenCode's default agent instead.
const openCodeAgentFallbackMarker = "Falling back to default agent"

// openCodeFallbackWatcher passes stderr through and records whether OpenCode
// reported an agent fallback. It keeps a short tail so a marker split across
// writes is still seen.
type openCodeFallbackWatcher struct {
	w        io.Writer
	tail     []byte
	fellBack bool
}

func (f *openCodeFallbackWatcher) Write(p []byte) (int, error) {
	if !f.fellBack {
		buf := append(f.tail, p...)
		if bytes.Contains(buf, []byte(openCodeAgentFallbackMarker)) {
			f.fellBack = true
		}
		if keep := len(openCodeAgentFallbackMarker) - 1; len(buf) > keep {
			buf = buf[len(buf)-keep:]
		}
		f.tail = append(f.tail[:0], buf...)
	}
	return f.w.Write(p)
}

// openCodeDuplicateKey reports the first key that repeats within the
// permission object or within one of its pattern maps.
func openCodeDuplicateKey(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func() (string, bool)
	walk = func() (string, bool) {
		tok, err := dec.Token()
		if err != nil || tok != json.Delim('{') {
			return "", false
		}
		seen := map[string]bool{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return "", false
			}
			key, _ := tok.(string)
			if seen[key] {
				return key, true
			}
			seen[key] = true
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return "", false
			}
			if len(value) > 0 && value[0] == '{' {
				if k, dup := openCodeDuplicateKey(value); dup {
					return k, true
				}
			}
		}
		return "", false
	}
	return walk()
}
