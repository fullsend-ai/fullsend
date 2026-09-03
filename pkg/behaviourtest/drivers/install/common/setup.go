package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
// ref-pinned resolution and mint URL. Unlike RunGitHubSetup, it does not
// vendor a binary — the ref-pinned action.yml resolves the binary at
// runtime. Each call uses a unique temp manifest path so concurrent
// scenarios don't collide; repos install creates the file automatically.
func RunReposInstall(
	binary, token, target, mintURL, fullsendRef, gcpProjectID, wifProvider string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	manifest := filepath.Join(os.TempDir(), fmt.Sprintf("repos-%d.yaml", time.Now().UnixNano()))
	defer os.Remove(manifest)

	args := []string{
		"repos", "install", target,
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
