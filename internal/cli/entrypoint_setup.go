package cli

import (
	"fmt"
	"os"
	"time"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

const remoteEntrypointPath = sandbox.SandboxWorkspace + "/.fullsend-entrypoint/run"
const remoteClaudeHelperPath = sandbox.SandboxWorkspace + "/bin/fullsend-claude"

type claudeEntrypointArtifacts struct {
	HooksSettings string
	Helper        []byte
	Integrity     *agentruntime.EntrypointIntegrity
}

type sandboxFileUploader func(sandboxName, localPath, remotePath string) error
type sandboxCommand func(sandboxName, command string, timeout time.Duration) (string, string, int, error)

func installEntrypointFile(sandboxName, remotePath string, content []byte, upload sandboxFileUploader, exec sandboxCommand) error {
	tmp, err := os.CreateTemp("", "fullsend-entrypoint-file-*")
	if err != nil {
		return fmt.Errorf("creating staged file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing staged file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing staged file: %w", err)
	}
	if err := upload(sandboxName, tmp.Name(), remotePath); err != nil {
		return fmt.Errorf("uploading %q: %w", remotePath, err)
	}
	stdout, stderr, exitCode, err := exec(sandboxName, "chmod 755 "+remotePath, 10*time.Second)
	if err != nil {
		return fmt.Errorf("making %q executable: %w", remotePath, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("making %q executable: exit %d: %s%s", remotePath, exitCode, stdout, stderr)
	}
	return nil
}

func configureEntrypointEvent(sandboxName, eventFile string, upload sandboxFileUploader, exec sandboxCommand) error {
	const remoteEvent = sandbox.SandboxWorkspace + "/event.json"
	if err := upload(sandboxName, eventFile, remoteEvent); err != nil {
		return fmt.Errorf("uploading normalized event for entrypoint: %w", err)
	}
	command := "printf '\\nexport FULLSEND_EVENT_FILE=%s\\n' " + shellQuote(remoteEvent) + " >> " + sandbox.SandboxWorkspace + "/.env"
	stdout, stderr, exitCode, err := exec(sandboxName, command, 10*time.Second)
	if err != nil {
		return fmt.Errorf("configuring entrypoint event input: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("configuring entrypoint event input: exit %d: %s%s", exitCode, stdout, stderr)
	}
	return nil
}

// buildClaudeEntrypointArtifacts derives helper bytes and integrity
// expectations only from runner-owned content. It is pure so all supported
// hook combinations can be validated without a live sandbox.
func buildClaudeEntrypointArtifacts(entrypointBytes []byte, model, effort string, pluginDirs []string, aliases, securityEnv map[string]string, hookConfig security.SandboxHookConfig, hooksEnabled bool) (claudeEntrypointArtifacts, error) {
	if len(entrypointBytes) == 0 {
		return claudeEntrypointArtifacts{}, fmt.Errorf("entrypoint executable is empty")
	}

	baseFiles := map[string][]byte{remoteEntrypointPath: append([]byte(nil), entrypointBytes...)}
	exactDirs := map[string][]string{sandbox.SandboxWorkspace + "/.fullsend-entrypoint": {"run"}}
	hooksSettings := ""
	if hooksEnabled {
		hooksSettings = security.SandboxHooksSettings
		for name, content := range security.HookFiles(hookConfig) {
			baseFiles[security.SandboxHooksDir+"/"+name] = content
			exactDirs[security.SandboxHooksDir] = append(exactDirs[security.SandboxHooksDir], name)
		}
		hooksJSON, err := security.GenerateHooksConfig(hookConfig)
		if err != nil {
			return claudeEntrypointArtifacts{}, fmt.Errorf("generating trusted Claude hooks settings: %w", err)
		}
		baseFiles[security.SandboxHooksSettings] = hooksJSON
		exactDirs[security.SandboxHooksDir] = append(exactDirs[security.SandboxHooksDir], "hooks.json")
	}

	baseIntegrity := agentruntime.NewEntrypointIntegrity(baseFiles, exactDirs, remoteClaudeHelperPath)
	helper := agentruntime.ClaudeEntrypointHelper(model, effort, hooksSettings, pluginDirs, aliases, baseIntegrity.GuardCommand(false), securityEnv)
	integrityFiles := make(map[string][]byte, len(baseFiles)+1)
	for path, content := range baseFiles {
		integrityFiles[path] = content
	}
	integrityFiles[remoteClaudeHelperPath] = helper
	return claudeEntrypointArtifacts{
		HooksSettings: hooksSettings,
		Helper:        helper,
		Integrity:     agentruntime.NewEntrypointIntegrity(integrityFiles, exactDirs, remoteClaudeHelperPath),
	}, nil
}
