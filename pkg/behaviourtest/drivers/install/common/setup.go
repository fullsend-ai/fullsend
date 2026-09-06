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

// RunReposInstall runs `fullsend repos install` for the given target with
// ref-pinned resolution, vendored binary, and mint URL. Each call uses a
// unique temp manifest path so concurrent scenarios don't collide; repos
// install creates the file automatically.
//
// When vendorBinary is non-empty, it is passed as --fullsend-binary to
// skip per-repo cross-compilation.
func RunReposInstall(
	binary, token, target, mintURL, fullsendRef, gcpProjectID, wifProvider, vendorBinary string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	f, err := os.CreateTemp("", "repos-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp manifest: %w", err)
	}
	manifest := f.Name()
	f.Close()
	// Remove the empty file so repos install bootstraps a v1 manifest;
	// an existing empty file parses as version 0 and fails validation.
	os.Remove(manifest)
	defer os.Remove(manifest)

	args := []string{
		"repos", "install", target,
		"--vendor",
		"--runtime", "dummy",
		"--direct",
		"--forge", "github",
		"-f", manifest,
	}
	if fullsendRef != "" {
		args = append(args, "--fullsend-ref", fullsendRef)
	}
	if mintURL != "" {
		args = append(args, "--mint-url", mintURL)
	}
	if project := strings.TrimSpace(gcpProjectID); project != "" {
		args = append(args, "--inference-project", project)
	}
	if wif := strings.TrimSpace(wifProvider); wif != "" {
		args = append(args, "--inference-wif-provider", wif)
	}
	if vendorBinary != "" {
		args = append(args, "--fullsend-binary", vendorBinary)
	}

	logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := runCLI(binary, token, args...); err != nil {
		return fmt.Errorf("repos install %s: %w", target, err)
	}
	return nil
}

// EnvFullsendRef resolves the fullsend ref for behaviour tests.
// Resolution chain: BEHAVIOUR_FULLSEND_REF → PR head SHA from event
// payload → GITHUB_HEAD_REF → GITHUB_REF_NAME → "" (defaults to v0).
func EnvFullsendRef() string {
	if v := os.Getenv("BEHAVIOUR_FULLSEND_REF"); v != "" {
		return v
	}
	if sha := prHeadSHAFromEvent(); sha != "" {
		return sha
	}
	if v := os.Getenv("GITHUB_HEAD_REF"); v != "" {
		return v
	}
	if v := os.Getenv("GITHUB_REF_NAME"); v != "" {
		return v
	}
	return ""
}

// prHeadSHAFromEvent extracts the head SHA from a GitHub event payload.
func prHeadSHAFromEvent() string {
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	if eventPath == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Clean(eventPath))
	if err != nil {
		return ""
	}
	var event struct {
		PullRequest struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return ""
	}
	return event.PullRequest.Head.SHA
}

// ResolveOrgWIFProvider looks up the org-level WIF provider via
// `fullsend inference status <org>` and returns the full provider path.
func ResolveOrgWIFProvider(
	binary, token, org, gcpProjectID string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
	statusArgs := []string{"inference", "status", org, "--project", gcpProjectID, "--format", "json"}
	logf("[install] running fullsend %s", strings.Join(statusArgs, " "))
	out, err := runCLI(binary, token, statusArgs...)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", org, err)
	}
	wifProvider, err := ParseInferenceStatusWIFProvider(out)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", org, err)
	}
	logf("[install] org-scoped inference WIF provider: %s", wifProvider)
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
