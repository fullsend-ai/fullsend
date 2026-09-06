package common

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CLIRunnerFunc is the signature for running a fullsend CLI command.
type CLIRunnerFunc = func(binary, token string, args ...string) (string, error)

// GitHubSetupOpts configures how RunGitHubSetupWithOpts invokes
// `fullsend github setup`. Use DefaultGitHubSetupOpts() for
// vendored-mode defaults (zero value is non-vendored).
type GitHubSetupOpts struct {
	// Vendor controls whether --vendor is passed. When false,
	// FullsendRef must be set so the shim references a remote ref
	// instead of a vendored binary.
	Vendor bool

	// FullsendRef is passed as --fullsend-ref when non-empty. Used in
	// non-vendored mode to pin the reusable workflow ref (e.g. "main").
	FullsendRef string
}

// DefaultGitHubSetupOpts returns vendored-mode defaults.
func DefaultGitHubSetupOpts() GitHubSetupOpts {
	return GitHubSetupOpts{Vendor: true}
}

// RunGitHubSetup runs fullsend github setup for the given target with the
// provided mint URL. If gcpProjectID is non-empty, inference provisioning
// is performed first and the resulting WIF provider is threaded to setup.
func RunGitHubSetup(
	binary, token, target, mintURL, gcpProjectID string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	return RunGitHubSetupWithOpts(binary, token, target, mintURL, gcpProjectID, DefaultGitHubSetupOpts(), runCLI, logf)
}

// RunGitHubSetupWithOpts is like RunGitHubSetup but accepts GitHubSetupOpts
// to control vendoring and fullsend-ref behaviour.
func RunGitHubSetupWithOpts(
	binary, token, target, mintURL, gcpProjectID string,
	opts GitHubSetupOpts,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	if !opts.Vendor && opts.FullsendRef == "" {
		return fmt.Errorf("github setup %s: non-vendored mode requires FullsendRef to be set", target)
	}
	if opts.Vendor && opts.FullsendRef != "" {
		return fmt.Errorf("github setup %s: vendored mode conflicts with FullsendRef %q — use one or the other", target, opts.FullsendRef)
	}
	args := []string{
		"github", "setup", target,
		"--direct",
		"--skip-app-setup",
		"--mint-url", mintURL,
		"--runtime", "dummy",
	}
	if opts.Vendor {
		args = append(args, "--vendor")
	}
	if opts.FullsendRef != "" {
		args = append(args, "--fullsend-ref", opts.FullsendRef)
	}
	if project := strings.TrimSpace(gcpProjectID); project != "" {
		wifProvider, err := ProvisionInference(binary, token, target, project, runCLI, logf)
		if err != nil {
			return err
		}
		args = append(args, "--inference-project", project, "--inference-wif-provider", wifProvider)
	}

	logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := runCLI(binary, token, args...); err != nil {
		return fmt.Errorf("github setup %s: %w", target, err)
	}
	return nil
}

// ProvisionInference runs inference provision and returns the WIF provider
// resource name. Mirrors the per-repo driver's provisionPerRepoInference.
func ProvisionInference(
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
	provisionArgs := []string{"inference", "provision", target, "--project", project}
	logf("[install] running fullsend %s", strings.Join(provisionArgs, " "))
	if _, err := runCLI(binary, token, provisionArgs...); err != nil {
		return "", fmt.Errorf("inference provision %s: %w", target, err)
	}

	statusArgs := []string{"inference", "status", target, "--project", project, "--format", "json"}
	logf("[install] running fullsend %s", strings.Join(statusArgs, " "))
	out, err := runCLI(binary, token, statusArgs...)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", target, err)
	}

	wifProvider, err := ParseInferenceStatusWIFProvider(out)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", target, err)
	}
	logf("[install] repo-scoped inference WIF provider: %s", wifProvider)
	return wifProvider, nil
}

// ParseInferenceStatusWIFProvider extracts the WIF provider from fullsend
// inference status JSON output.
func ParseInferenceStatusWIFProvider(output string) (string, error) {
	statusKey := `"status":`
	keyIdx := strings.Index(output, statusKey)
	if keyIdx < 0 {
		return "", fmt.Errorf("no JSON status object in output")
	}
	start := strings.LastIndex(output[:keyIdx], "{")
	if start < 0 {
		return "", fmt.Errorf("no JSON status object in output")
	}
	var status struct {
		Status      string `json:"status"`
		WIFProvider string `json:"FULLSEND_GCP_WIF_PROVIDER"`
	}
	if err := json.NewDecoder(strings.NewReader(output[start:])).Decode(&status); err != nil {
		return "", fmt.Errorf("parse JSON: %w", err)
	}
	if status.WIFProvider == "" {
		return "", fmt.Errorf("missing FULLSEND_GCP_WIF_PROVIDER (status=%q)", status.Status)
	}
	if status.Status != "healthy" {
		return "", fmt.Errorf("expected healthy status, got %q", status.Status)
	}
	return status.WIFProvider, nil
}
