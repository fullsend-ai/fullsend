package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// preflightHostEnvNames lists the host environment variables a preflight
// check command may see (#8721). Preflight checks run on the bare host via
// sh -c before sandbox creation, so the command's child environment is a
// minimal allowlist rather than the full process environment: unset
// allowlisted names are simply absent, and every other name must reach the
// child through an env.runner/runner_env declaration.
var preflightHostEnvNames = []string{"PATH", "HOME", "TMPDIR"}

// preflightBraceRef matches ${VAR} references with shell-portable names.
// Bare $VAR references are deliberately not matched by the rejection scan:
// they are expanded inside the child shell, where an unset variable is
// empty (design D3 of the preflight-check-credential-safety change).
var preflightBraceRef = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// preflightCredentialRefusedNames is the minted-credential inventory: the
// per-role agent token names plus every role token declared in roleTokenVars
// (entries whose value is the minted token, not a fixed marker value), so
// the refusal set cannot drift when a new role token is minted. Deriving it
// from the same inventories the mint code owns keeps the preflight list in
// lockstep with them (#8748).
var preflightCredentialRefusedNames = func() map[string]bool {
	names := map[string]bool{
		"GITLAB_TOKEN":         true,
		forge.SecretForgeToken: true,
		"FULLSEND_FETCH_TOKEN": true,
	}
	for _, n := range agentTokenEnvVars {
		names[n] = true
	}
	for _, vars := range roleTokenVars {
		for _, tv := range vars {
			if tv.Value == "" {
				names[tv.Name] = true
			}
		}
	}
	return names
}()

// preflightCredentialRefused reports whether a runner-env key is refused
// from the preflight environment. Forge, mint, and provider credential
// names never reach a preflight child, regardless of whether they arrive
// from the process environment or a harness declaration under their own
// names: the minted-token inventory above, the harness expansion deny set
// (OIDC request variables, OPENAI_API_KEY, GH_WORKFLOW_TOKEN, trigger and
// runner-secret names), and the GitLab registered-role routing prefix.
func preflightCredentialRefused(key string) bool {
	if preflightCredentialRefusedNames[key] {
		return true
	}
	return harnessExpansionDenied(key) || strings.HasPrefix(key, gitlabRoleRoutingKeyPrefix)
}

// preflightEnv builds the variable set a preflight command runs with: the
// allowlisted host names (when set), the runner-forced FULLSEND_DIR and
// PYTHONDONTWRITEBYTECODE, and the effective runner env minus refused
// credential names. On collision the harness declaration wins for
// PATH/HOME/TMPDIR; FULLSEND_DIR and PYTHONDONTWRITEBYTECODE are pinned to
// the runner-forced values and a harness declaration of either is dropped,
// mirroring the pinned-key precedent in childScriptEnv (#7137).
func preflightEnv(runnerEnv map[string]string, fullsendDir string) map[string]string {
	env := make(map[string]string)
	for _, name := range preflightHostEnvNames {
		if v, ok := os.LookupEnv(name); ok {
			env[name] = v
		}
	}
	env["FULLSEND_DIR"] = fullsendDir
	env["PYTHONDONTWRITEBYTECODE"] = "1"
	for k, v := range runnerEnv {
		if preflightCredentialRefused(k) {
			continue
		}
		switch k {
		case "FULLSEND_DIR", "PYTHONDONTWRITEBYTECODE":
			continue
		}
		env[k] = v
	}
	return env
}

// buildPreflightEnv renders preflightEnv as a sorted KEY=VALUE slice. It
// never starts from os.Environ: beyond the allowlist, the host process
// environment is not a source for preflight children.
func buildPreflightEnv(runnerEnv map[string]string, fullsendDir string) []string {
	return envToList(preflightEnv(runnerEnv, fullsendDir))
}

// expandPreflightCommand expands ${VAR} references in a raw preflight
// command against env. Every brace reference must resolve in env before any
// substitution happens: rejection occurs before execution, so no part of
// the command runs, and the error names the variable, never a value or the
// raw command (a literal secret beside a rejected reference must not reach
// diagnostics, design D3). Bare $VAR references are left to the child
// shell, where unset means empty; because os.Expand resolves them against
// env, they can only ever splice allowlisted or declared values (design D3).
func expandPreflightCommand(raw string, env map[string]string) (string, error) {
	if !strings.Contains(raw, "${") {
		return raw, nil
	}
	for _, ref := range preflightBraceRef.FindAllString(raw, -1) {
		name := ref[2 : len(ref)-1]
		if _, ok := env[name]; !ok {
			return "", fmt.Errorf("references %s, which is not in the preflight environment (allowlisted host variables: PATH, HOME, TMPDIR; runner-forced: FULLSEND_DIR, PYTHONDONTWRITEBYTECODE; declare other variables via env.runner or runner_env)", ref)
		}
	}
	return os.Expand(raw, func(key string) string { return env[key] }), nil
}

// runPreflightCheck runs one preflight dependency check: it expands the raw
// command against the preflight environment (rejecting out-of-allowlist
// brace references before execution), runs it via sh -c with that
// environment, and reports failure with redacted diagnostics. source labels
// the harness field the command came from (e.g.
// "validation_loop.preflight_check") so error messages keep their
// field-qualified shape.
func runPreflightCheck(ctx context.Context, printer *ui.Printer, source, rawCmd string, runnerEnv map[string]string, fullsendDir string) error {
	printer.StepStart("Preflight: checking " + source + " dependencies")
	env := preflightEnv(runnerEnv, fullsendDir)
	expanded, err := expandPreflightCommand(rawCmd, env)
	if err != nil {
		printer.StepFail("Preflight dependency check failed")
		return fmt.Errorf("%s %w", source, err)
	}
	preflightCtx, preflightCancel := context.WithTimeout(ctx, preflightCheckTimeout)
	defer preflightCancel()
	cmd := exec.CommandContext(preflightCtx, "sh", "-c", expanded)
	cmd.Env = buildPreflightEnv(runnerEnv, fullsendDir)
	// Run the check in its own process group and kill the whole group on
	// deadline (#8875). Killing only sh would orphan non-exec children
	// (e.g. the sleep in "sleep 300; true"), which would keep running on the
	// host with the preflight environment. Where signalling a group is not
	// permitted (some sandboxes refuse it), fall back to killing sh alone so
	// the deadline still holds; WaitDelay then bounds the orphan's pipe.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	// Without WaitDelay, a descendant that inherits the output pipe and
	// escapes the process group (e.g. via setsid) keeps CombinedOutput
	// blocked past the deadline even after the group is killed.
	cmd.WaitDelay = preflightWaitDelay
	out, execErr := cmd.CombinedOutput()
	if errors.Is(execErr, exec.ErrWaitDelay) && preflightCtx.Err() == nil {
		// sh exited successfully, but a background child still held the
		// output pipe when WaitDelay elapsed. The check itself passed, so
		// do not report it as a missing dependency (#8875).
		printer.StepWarn("Preflight dependency check passed, but " + source + " left a background process holding its output")
		return nil
	}
	if execErr != nil && preflightCtx.Err() != nil {
		// If sh exited before the deadline, os/exec stopped watching the
		// context and never called Cancel, but a background child can still
		// have held the output pipe past it. That surfaces as exec.ErrWaitDelay
		// after a zero exit, but as sh's *exec.ExitError after a nonzero one.
		// Kill the group whatever the error type so the timeout does not leave
		// that child running (#8875). Best effort: the group may already be
		// gone, or signalling it may not be permitted.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if execErr != nil {
		printer.StepFail("Preflight dependency check failed")
		// Redact before the message reaches the CLI error, the trace and
		// the completion comment: the command runs with env.runner.
		redactedCmd := redactFeedback(expanded, runnerEnv)
		if preflightCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s timed out after %s: %s", source, preflightCheckTimeout, redactedCmd)
		}
		// Redact the raw capture first, because validationFailMessage
		// trims it and a padded secret would then miss the literal match.
		redactedOut := []byte(redactFeedback(string(out), runnerEnv))
		return fmt.Errorf("%s failed: %s\n%s\nInstall the missing dependency before running this agent", source, redactedCmd, redactFeedback(validationFailMessage(redactedOut, execErr), runnerEnv))
	}
	printer.StepDone("Preflight dependency check passed")
	return nil
}
