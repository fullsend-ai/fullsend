package common

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CLIRunnerFunc is the signature for running a fullsend CLI command.
type CLIRunnerFunc = func(binary, token string, args ...string) (string, error)

// RunGitHubSetup runs fullsend github setup for the given target with the
// provided mint URL. If gcpProjectID is non-empty, the existing WIF
// provider is looked up first via "inference status". Provisioning is
// only performed when no healthy provider exists, avoiding redundant
// create/undelete/update/enable IAM writes on every run.
func RunGitHubSetup(
	binary, token, target, mintURL, gcpProjectID string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	args := []string{
		"github", "setup", target,
		"--vendor", "--direct",
		"--skip-app-setup",
		"--mint-url", mintURL,
		"--runtime", "dummy",
	}
	if project := strings.TrimSpace(gcpProjectID); project != "" {
		// Read-before-write: reuse the existing WIF provider when it
		// is already healthy to avoid redundant IAM write operations.
		wifProvider, err := GetExistingInferenceWIFProvider(binary, token, target, project, runCLI, logf)
		if err != nil {
			// Provider missing or unhealthy — fall through to provision.
			logf("[install] existing WIF provider not found for %s, provisioning: %v", target, err)
			wifProvider, err = ProvisionInference(binary, token, target, project, runCLI, logf)
			if err != nil {
				return err
			}
		} else {
			logf("[install] reusing existing WIF provider for %s: %s", target, wifProvider)
		}
		args = append(args, "--inference-project", project, "--inference-wif-provider", wifProvider)
	}

	logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := runCLI(binary, token, args...); err != nil {
		return fmt.Errorf("github setup %s: %w", target, err)
	}
	return nil
}

// GetExistingInferenceWIFProvider checks whether a healthy WIF provider
// already exists for the given target by running "inference status". It
// returns the provider resource name when one is found, or an error when
// the provider is missing, unhealthy, or the status command fails.
func GetExistingInferenceWIFProvider(
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
	statusArgs := []string{"inference", "status", target, "--project", project, "--format", "json"}
	logf("[install] checking existing inference WIF provider: fullsend %s", strings.Join(statusArgs, " "))
	out, err := runCLI(binary, token, statusArgs...)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", target, err)
	}

	return ParseInferenceStatusWIFProvider(out)
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
