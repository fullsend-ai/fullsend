package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CLIRunnerFunc is the signature for running a fullsend CLI command.
type CLIRunnerFunc = func(binary, token string, args ...string) (string, error)

// RunGitHubSetup runs fullsend github setup for the given target with the
// provided mint URL. If gcpProjectID is non-empty, inference provisioning
// is performed first and the resulting WIF provider is threaded to setup.
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

// RunReposInstall runs fullsend repos install with --fullsend-ref for
// ref-pinned installs. This replaces github setup --vendor for ephemeral
// repos that resolve the CLI binary at runtime via action.yml.
func RunReposInstall(
	binary, token, target, fullsendRef, mintURL, gcpProjectID string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	if fullsendRef == "" {
		return fmt.Errorf("repos install %s: --fullsend-ref is required", target)
	}
	// Each install uses an isolated temp manifest so concurrent
	// allocations don't race on a shared repos.yaml.
	tmpManifest, err := os.CreateTemp("", "bt-manifest-*.yaml")
	if err != nil {
		return fmt.Errorf("repos install %s: creating temp manifest: %w", target, err)
	}
	if _, err := tmpManifest.WriteString("version: 1\n"); err != nil {
		tmpManifest.Close()
		return fmt.Errorf("repos install %s: writing temp manifest: %w", target, err)
	}
	manifestPath := tmpManifest.Name()
	tmpManifest.Close()
	defer os.Remove(manifestPath)

	args := []string{
		"repos", "install", target,
		"--fullsend-ref", fullsendRef,
		"--runtime", "dummy",
		"--direct",
		"--forge", "github",
		"-f", filepath.Clean(manifestPath),
	}
	if project := strings.TrimSpace(gcpProjectID); project != "" {
		// Provision the WIF provider before repos install runs — repos install
		// auto-derives the WIF provider from --inference-project internally,
		// so only --inference-project is passed (not --inference-wif-provider,
		// which repos install does not accept).
		if _, err := ProvisionInference(binary, token, target, project, runCLI, logf); err != nil {
			return err
		}
		args = append(args, "--inference-project", project)
	}
	if mintURL != "" {
		args = append(args, "--mint-url", mintURL)
	}
	logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := runCLI(binary, token, args...); err != nil {
		return fmt.Errorf("repos install %s: %w", target, err)
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
