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

// appendEnvVar appends a single "echo 'export K=V'" fragment to buf,
// escaping single quotes in the value.
func appendEnvVar(buf *strings.Builder, key, value string) {
	escaped := strings.ReplaceAll(value, "'", "'\\''")
	if buf.Len() > 0 {
		buf.WriteString(" && ")
	}
	fmt.Fprintf(buf, "echo 'export %s=%s' >> %s/.env", key, escaped, sandbox.SandboxWorkspace)
}

// appendHookEnv appends the hook-related environment (TIRITH_FAIL_ON,
// TIRITH_REQUIRED, FULLSEND_EGRESS_ALLOWLIST) to the sandbox workspace
// .env so the scripts see it regardless of which runtime invokes them.
// All env vars are written in a single sandbox exec call.
func appendHookEnv(sandboxName string, hooks security.SandboxHookConfig) error {
	var buf strings.Builder
	if failOn := hooks.TirithFailOn(); failOn != "" {
		appendEnvVar(&buf, "TIRITH_FAIL_ON", failOn)
	}
	if hooks.TirithRequired() {
		appendEnvVar(&buf, "TIRITH_REQUIRED", "1")
	}
	if allowlist := hooks.SSRFEgressAllowlist(); allowlist != "" {
		appendEnvVar(&buf, "FULLSEND_EGRESS_ALLOWLIST", allowlist)
	}
	if buf.Len() == 0 {
		return nil
	}
	if _, _, _, err := sandbox.Exec(sandboxName, buf.String(), 10*time.Second); err != nil {
		return fmt.Errorf("appending hook env: %w", err)
	}
	return nil
}
