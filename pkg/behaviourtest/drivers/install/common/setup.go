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

	// ConfigPreset is passed as --config when non-empty (a local path
	// or HTTPS URL to a config base layer preset). When set, --runtime
	// dummy is omitted so the preset's runtime: dummy is inherited
	// rather than pinned in the overlay.
	ConfigPreset string

	// Runtime overrides the runtime passed to github setup. When empty,
	// github setup uses the normal "dummy" runtime. Playback suites set
	// this to "dummy-playback" while retaining the standard repo-pool
	// lifecycle (allocation, install, validation, teardown).
	Runtime string

	// AppSet selects the GitHub App identities used by the configured mint.
	AppSet string

	// ResolveWIFProvider, when set, returns the inference WIF provider
	// for target in project. Callers use it to cache or serialise the
	// lookup. When nil, ResolveInferenceWIFProvider is used.
	ResolveWIFProvider func(target, project string) (string, error)
}

// DefaultGitHubSetupOpts returns vendored-mode defaults.
func DefaultGitHubSetupOpts() GitHubSetupOpts {
	return GitHubSetupOpts{Vendor: true}
}

// RunGitHubSetup runs fullsend github setup for the given target with the
// provided mint URL. If gcpProjectID is non-empty, the inference WIF
// provider is resolved first (see ResolveInferenceWIFProvider) and
// threaded to setup.
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
		"--mint-url", mintURL,
	}
	if opts.AppSet != "" {
		args = append(args, "--app-set", opts.AppSet)
	}
	// Omit the implicit default runtime when a preset is supplied so the
	// preset's runtime is inherited rather than pinned in the overlay.
	// When the caller explicitly set Runtime (e.g. "dummy-playback"), it
	// must still win over the preset's embedded runtime — otherwise the
	// install runs the preset's runtime while doEnsure's post-install
	// validation checks against opts.Runtime, a mismatch.
	if preset := strings.TrimSpace(opts.ConfigPreset); preset != "" {
		args = append(args, "--config", preset)
		if opts.Runtime != "" {
			args = append(args, "--runtime", opts.Runtime)
		}
	} else {
		runtime := opts.Runtime
		if runtime == "" {
			runtime = "dummy"
		}
		args = append(args, "--runtime", runtime)
	}
	if opts.Vendor {
		args = append(args, "--vendor")
	}
	if opts.FullsendRef != "" {
		args = append(args, "--fullsend-ref", opts.FullsendRef)
	}
	if project := strings.TrimSpace(gcpProjectID); project != "" {
		resolve := opts.ResolveWIFProvider
		if resolve == nil {
			resolve = func(target, project string) (string, error) {
				return ResolveInferenceWIFProvider(binary, token, target, project, runCLI, logf)
			}
		}
		wifProvider, err := resolve(target, project)
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

// ResolveInferenceWIFProvider returns the healthy WIF provider for target.
// It reads "inference status" first and runs "inference provision" only
// when the provider is missing or not healthy, so an enrolled repo costs
// no IAM writes.
func ResolveInferenceWIFProvider(
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
	wifProvider, err := InferenceStatusWIFProvider(binary, token, target, project, runCLI, logf)
	if err == nil {
		return wifProvider, nil
	}
	logf("[install] no healthy WIF provider for %s, provisioning: %v", target, err)
	return ProvisionInference(binary, token, target, project, runCLI, logf)
}

// InferenceStatusWIFProvider runs "inference status" and returns the WIF
// provider when the status is healthy. Any other status is an error.
func InferenceStatusWIFProvider(
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
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

// ProvisionInference runs inference provision, then inference status, and
// returns the WIF provider resource name. The provider must be healthy.
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
	return InferenceStatusWIFProvider(binary, token, target, project, runCLI, logf)
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
