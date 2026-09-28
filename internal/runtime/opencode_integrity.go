package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

const openCodeEnvReadSeparator = "|fullsend-opencode-env-sep|"

type openCodeTrustedEnv struct {
	ConfigContent   string
	CredentialsPath string
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

func openCodeTrustedEnvReadCommand(envFile string) string {
	return ". " + shellQuote(envFile) + ` 2>/dev/null && command -p printf '%s' "${OPENCODE_CONFIG_CONTENT-}" ` +
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
	if !ok || validateOpenCodePermissionPolicy(permission) != nil {
		return fmt.Errorf("reading the trusted OpenCode environment: OPENCODE_CONFIG_CONTENT has no permission policy")
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
