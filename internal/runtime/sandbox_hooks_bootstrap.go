package runtime

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// SandboxHooksBootstrap is an optional BootstrapInput extension carrying the
// runtime-neutral sandbox tool hook configuration (Tirith, SSRF, canary,
// secret redaction, ...). Runtimes type-assert for it in Bootstrap and, when
// present, install the hook scripts from security.HookFiles and wire them
// through their native mechanism (Claude Code: settings.json; other runtimes:
// their own plugin/extension) following security.HookPlan. Runtimes that do
// not implement it install no sandbox tool hooks — see docs/runtimes.md.
type SandboxHooksBootstrap interface {
	SandboxHookConfig() security.SandboxHookConfig
}

// installHookScripts creates hooksDir inside the sandbox, uploads every
// enabled hook script into it and marks each executable. It is
// runtime-neutral: callers pass the directory their runtime will invoke the
// scripts from.
func installHookScripts(sandboxName, hooksDir string, hooks security.SandboxHookConfig) error {
	mkdirCmd := "mkdir -p " + shellQuote(hooksDir)
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating hooks dir %s: %w", hooksDir, err)
	}
	for name, content := range security.HookFiles(hooks) {
		tmpFile, err := os.CreateTemp("", "fullsend-hook-*")
		if err != nil {
			return fmt.Errorf("creating temp file for hook %s: %w", name, err)
		}
		if _, err := tmpFile.Write(content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return fmt.Errorf("writing hook %s: %w", name, err)
		}
		tmpFile.Close()

		remotePath := hooksDir + "/" + name
		if err := sandbox.Upload(sandboxName, tmpFile.Name(), remotePath); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("copying hook %s to sandbox: %w", name, err)
		}
		os.Remove(tmpFile.Name())

		chmodCmd := "chmod +x " + shellQuote(remotePath)
		if _, _, _, err := sandbox.Exec(sandboxName, chmodCmd, 10*time.Second); err != nil {
			return fmt.Errorf("chmod hook %s: %w", name, err)
		}
	}
	return nil
}

// appendHookEnv appends the hook-related environment (TIRITH_FAIL_ON,
// TIRITH_REQUIRED) to the sandbox workspace .env so the scripts see it
// regardless of which runtime invokes them.
func appendHookEnv(sandboxName string, hooks security.SandboxHookConfig) error {
	if failOn := hooks.TirithFailOn(); failOn != "" {
		escapedFailOn := strings.ReplaceAll(failOn, "'", "'\\''")
		envCmd := fmt.Sprintf("echo 'export TIRITH_FAIL_ON=%s' >> %s/.env",
			escapedFailOn, sandbox.SandboxWorkspace)
		if _, _, _, err := sandbox.Exec(sandboxName, envCmd, 10*time.Second); err != nil {
			return fmt.Errorf("setting TIRITH_FAIL_ON: %w", err)
		}
	}
	if hooks.TirithRequired() {
		envCmd := fmt.Sprintf("echo 'export TIRITH_REQUIRED=1' >> %s/.env", sandbox.SandboxWorkspace)
		if _, _, _, err := sandbox.Exec(sandboxName, envCmd, 10*time.Second); err != nil {
			return fmt.Errorf("setting TIRITH_REQUIRED: %w", err)
		}
	}
	return nil
}
