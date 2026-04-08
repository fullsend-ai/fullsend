package entrypoint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner executes external commands. The real implementation wraps
// exec.CommandContext; the fake records calls for testing.
type Runner interface {
	// Run executes a command and returns its exit code. A non-zero exit
	// code is returned as (code, nil) — only infrastructure failures
	// (e.g., binary not found) produce a non-nil error.
	Run(ctx context.Context, name string, args []string, dir string, env []string) (exitCode int, err error)
}

// ExecRunner executes commands via os/exec.
type ExecRunner struct{}

func (r *ExecRunner) Run(ctx context.Context, name string, args []string, dir string, env []string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("exec %s: %w", name, err)
	}
	return 0, nil
}

// SanitizeEnv returns a copy of the given environment with sensitive
// variables removed. Variables matching any of the prefixes or exact
// names in the deny list are excluded.
func SanitizeEnv(env []string) []string {
	denyPrefixes := []string{
		"FULLSEND_",
		"GITHUB_TOKEN",
		"ACTIONS_",
	}
	denyExact := map[string]bool{
		"GH_TOKEN":             true,
		"GITHUB_TOKEN":         true,
		"GITHUB_ENV":           true,
		"GITHUB_OUTPUT":        true,
		"GITHUB_PATH":          true,
		"GITHUB_STEP_SUMMARY":  true,
		"GITHUB_STATE":         true,
	}

	var result []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if denyExact[key] {
			continue
		}
		denied := false
		for _, prefix := range denyPrefixes {
			if strings.HasPrefix(key, prefix) {
				denied = true
				break
			}
		}
		if !denied {
			result = append(result, e)
		}
	}
	return result
}

// RunRecord captures a single invocation of FakeRunner.
type RunRecord struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

// FakeRunner records calls and returns pre-configured exit codes.
type FakeRunner struct {
	// Calls records each invocation in order.
	Calls []RunRecord
	// ExitCodes maps command name to exit code. If not set, returns 0.
	ExitCodes map[string]int
	// Errors maps command name to error. If set, returned instead of exit code.
	Errors map[string]error
	// RunFunc, if set, determines the result for each invocation and takes
	// precedence over ExitCodes/Errors. It receives the 0-based call index,
	// command name, and args.
	RunFunc func(call int, name string, args []string) (int, error)
}

func (r *FakeRunner) Run(_ context.Context, name string, args []string, dir string, env []string) (int, error) {
	callIndex := len(r.Calls)
	r.Calls = append(r.Calls, RunRecord{
		Name: name,
		Args: append([]string{}, args...), // copy to avoid aliasing
		Dir:  dir,
		Env:  append([]string{}, env...), // copy
	})
	if r.RunFunc != nil {
		return r.RunFunc(callIndex, name, args)
	}
	if r.Errors != nil {
		if err, ok := r.Errors[name]; ok {
			return -1, err
		}
	}
	if r.ExitCodes != nil {
		if code, ok := r.ExitCodes[name]; ok {
			return code, nil
		}
	}
	return 0, nil
}
