package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// ClaudeEntrypointHelper returns the sandbox helper used by script-led
// harnesses to launch Claude Code with Fullsend's selected model, hooks, and
// validated Claude plugin directories.
func ClaudeEntrypointHelper(model, effort, hooksSettings string, pluginDirs []string, aliases map[string]string, integrityGuard string, securityEnv map[string]string) []byte {
	// Build the final argument vector without eval or word splitting.
	flags := []string{"--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	if model != "" {
		flags = append(flags, "--model", remapModel(model, aliases))
	}
	if effort != "" {
		flags = append(flags, "--effort", effort)
	}
	if hooksSettings != "" {
		flags = append(flags, "--settings", hooksSettings)
	}
	for _, dir := range pluginDirs {
		flags = append(flags, "--plugin-dir", dir)
	}
	quoted := make([]string, len(flags))
	for i, flag := range flags {
		quoted[i] = shellQuote(flag)
	}
	lines := []string{"#!/bin/sh", "set -eu"}
	if integrityGuard != "" {
		lines = append(lines, integrityGuard)
	}
	securityKeys := make([]string, 0, len(securityEnv))
	for key := range securityEnv {
		securityKeys = append(securityKeys, key)
	}
	sort.Strings(securityKeys)
	for _, key := range securityKeys {
		lines = append(lines, "export "+key+"="+shellQuote(securityEnv[key]))
	}
	lines = append(lines, "exec claude "+strings.Join(quoted, " ")+" \"$@\"")
	return []byte(strings.Join(lines, "\n") + "\n")
}

// RunEntrypoint runs a harness command directly inside the sandbox. argv is
// preserved as individual arguments; only the selected Claude stream format
// is parsed for runtime metrics.
func RunEntrypoint(ctx context.Context, sandboxName, repoDir string, argv []string, format string, timeout time.Duration, outputPath string, printer *ui.Printer, onEvent func(AgentEvent), metrics *RunMetrics) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return -1, fmt.Errorf("entrypoint command is empty")
	}
	if format != "" && format != "none" && format != "claude" {
		return -1, fmt.Errorf("unsupported entrypoint stream format %q", format)
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	cmd := fmt.Sprintf("cd %s && . %s && exec %s", shellQuote(repoDir), shellQuote(sandbox.SandboxWorkspace+"/.env"), strings.Join(quoted, " "))
	stdout, proc, cancel, err := sandbox.ExecStreamReader(ctx, sandboxName, cmd, timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()
	var out io.Writer = io.Discard
	var file *os.File
	if outputPath != "" {
		file, err = os.Create(outputPath)
		if err != nil {
			return -1, fmt.Errorf("creating entrypoint output: %w", err)
		}
		defer file.Close()
		out = file
	}
	if format == "claude" {
		handler := onEvent
		if handler == nil {
			handler = NewEventRenderer(printer).Handle
		}
		if parseErr := parseClaudeStream(io.TeeReader(stdout, out), func(evt AgentEvent) { recordClaudeMetrics(metrics, evt); handler(evt) }); parseErr != nil {
			fmt.Fprintf(os.Stderr, "  entrypoint progress parser: %v\n", sanitizeOutput(parseErr.Error()))
			cancel()
			_, _ = io.Copy(io.Discard, stdout)
		}
	} else if _, err = io.Copy(io.MultiWriter(out, os.Stdout), stdout); err != nil {
		cancel()
		return -1, fmt.Errorf("reading entrypoint output: %w", err)
	}
	waitErr := proc.Wait()
	exitCode := -1
	if proc.ProcessState != nil {
		exitCode = proc.ProcessState.ExitCode()
	}
	if waitErr != nil && proc.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}
	return exitCode, nil
}
