package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
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

// gcpWIFQuotaSignatures are the lowercase substrings that identify a GCP
// quota or rate-limit rejection as it surfaces in errors from the
// `inference provision` and `inference status` CLI calls. The CLI runner
// embeds the command's combined output — cobra's "Error:" line carrying
// GCP's message — in the error string, so signatures are matched against
// the whole lowercased error message.
//
// The known texts, per the GCP client producers in internal/dispatch/gcf:
//
//   - "rate limit" — doWIFRequestWithRetry's IAM-write exhaustion message
//     ("rate limited (HTTP 429) after N attempts") and GCP rate-limit
//     bodies ("Rate limit exceeded ...").
//   - "quota exceeded" — GCP quota bodies ("Quota exceeded for quota
//     project ... of service 'iam.googleapis.com' ...").
//   - "status 429" / "returned 429" — IAM calls without the retry wrapper
//     surface the status directly ("unexpected status 429 deleting WIF
//     pool: ...", "getting WIF provider returned 429: ...").
//
// String matching is acceptable here because it is bounded to the
// inference provision/status call sites — RunInferenceProvision,
// ResolveInferenceWIFProvider and WaitInferenceHealthy (design D3 of the
// behaviour-infra-retry change): a drifted GCP message stops matching and
// degrades to scenario — no retry — which is the safe direction.
var gcpWIFQuotaSignatures = []string{
	"rate limit",
	"quota exceeded",
	"status 429",
	"returned 429",
}

// isGCPWIFQuotaError reports whether err's message carries one of the
// known GCP quota/rate-limit signatures from the WIF provisioning path.
func isGCPWIFQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range gcpWIFQuotaSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// markGCPWIFQuota marks err as an infrastructure failure ("setup: gcp wif
// quota") when its message carries a known GCP WIF quota signature, and
// returns every other error — including nil — unchanged. An error that
// already carries a behaviourfail mark passes through unwrapped so marking
// is never doubled: RunInferenceProvision's and WaitInferenceHealthy's
// marked errors reach ResolveInferenceWIFProvider again through
// ProvisionInference. A cancelled or expired context also passes through
// unmarked: the caller stopped the wait, and an earlier status error
// quoted in its message is not the cause.
func markGCPWIFQuota(err error) error {
	if err == nil || behaviourfail.Is(err) || !isGCPWIFQuotaError(err) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return behaviourfail.Mark(err, "setup: gcp wif quota")
}

// ResolveInferenceWIFProvider returns the healthy WIF provider for target.
// It reads "inference status" first and runs "inference provision" only
// when the provider is missing or not healthy, so an enrolled repo costs
// no IAM writes. A returned error whose message carries a GCP
// quota/rate-limit signature is marked as an infrastructure failure
// (markGCPWIFQuota), so the suite retries the run instead of failing the
// scenario.
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
	wifProvider, err = ProvisionInference(binary, token, target, project, runCLI, logf)
	return wifProvider, markGCPWIFQuota(err)
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

// postProvisionStatusDelays is the backoff between "inference status"
// reads after "inference provision" succeeds. A just-provisioned repo can
// briefly still report not_provisioned (read-after-write lag), so the
// status is re-read until healthy. The delays sum to 60s; the status is
// read len(postProvisionStatusDelays)+1 times before giving up.
var postProvisionStatusDelays = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

// PostProvisionSleep waits d between post-provision "inference status"
// reads, returning early with ctx's error when ctx is done. Tests replace
// it to skip the real backoff.
var PostProvisionSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ProvisionInference runs inference provision, then waits for the provider
// to become healthy (WaitInferenceHealthy) and returns the WIF provider
// resource name. A failed provision is returned without reading status.
func ProvisionInference(
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
	if err := RunInferenceProvision(binary, token, target, project, runCLI, logf); err != nil {
		return "", err
	}
	return WaitInferenceHealthy(context.Background(), binary, token, target, project, runCLI, logf)
}

// RunInferenceProvision runs "inference provision" for target. Callers
// that serialise provisions (the ensurer's provisionGate) hold their gate
// only around this call, not around WaitInferenceHealthy. A returned
// error whose message carries a GCP quota/rate-limit signature is marked
// as an infrastructure failure (markGCPWIFQuota), so a quota-rejected
// provision classifies as infrastructure on every path that runs it: the
// ensurer calls this directly from its provision gate, bypassing
// ResolveInferenceWIFProvider. The mark lives here — the single producer
// of provision-phase CLI errors — rather than at that call site, keeping
// the signature predicate bounded to this file's inference call sites
// (design D3) and avoiding an exported marker from common (design D1);
// it is never doubled, because ResolveInferenceWIFProvider passes marked
// errors through unwrapped.
func RunInferenceProvision(
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	provisionArgs := []string{"inference", "provision", target, "--project", project}
	logf("[install] running fullsend %s", strings.Join(provisionArgs, " "))
	if _, err := runCLI(binary, token, provisionArgs...); err != nil {
		return markGCPWIFQuota(fmt.Errorf("inference provision %s: %w", target, err))
	}
	return nil
}

// WaitInferenceHealthy polls "inference status" after a successful
// provision with a bounded backoff (postProvisionStatusDelays) until the
// provider is healthy, and returns the WIF provider resource name. It fails
// with the last status error once the budget is spent, or with ctx's error
// when ctx is done during a wait. A returned error whose message carries a
// GCP quota/rate-limit signature is marked as an infrastructure failure
// (markGCPWIFQuota), so the suite retries the run instead of failing the
// scenario.
func WaitInferenceHealthy(
	ctx context.Context,
	binary, token, target, project string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (string, error) {
	attempts := len(postProvisionStatusDelays) + 1
	var budget time.Duration
	for _, d := range postProvisionStatusDelays {
		budget += d
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		logf("[install] post-provision inference status for %s (attempt %d/%d)", target, attempt, attempts)
		wifProvider, err := InferenceStatusWIFProvider(binary, token, target, project, runCLI, logf)
		if err == nil {
			return wifProvider, nil
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		delay := postProvisionStatusDelays[attempt-1]
		logf("[install] inference for %s not healthy yet, retrying in %s: %v", target, delay, err)
		if err := PostProvisionSleep(ctx, delay); err != nil {
			waitErr := fmt.Errorf("waiting for inference for %s to become healthy: %w (last status: %v)", target, err, lastErr)
			return "", markGCPWIFQuota(waitErr)
		}
	}
	return "", markGCPWIFQuota(fmt.Errorf("inference for %s not healthy after provision (%d status reads, %s of backoff): %w",
		target, attempts, budget, lastErr))
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

// ReposInstallOpts configures a single behaviour-test `repos install`. Binary,
// Token and Target are required; the remaining fields are optional and written
// into the manifest only when set. Bundling them in a struct keeps the many
// string parameters from being silently transposable at the call site.
//
// AppSet must match the app set the suite's mint was deployed with: it is
// written as the repo's FULLSEND_APP_SET, which the dispatch shim uses to
// recognize the registered role bots (e.g. {app_set}-review[bot]). Omitting it
// defaults the repo to the production app set and breaks bot-identity
// authorization such as the review-bot fix path.
type ReposInstallOpts struct {
	Binary       string // path to the fullsend binary
	Token        string // forge token for the install
	Target       string // org/repo to install into
	MintURL      string // token mint URL (entry config.mint_url)
	FullsendRef  string // github.fullsend_ref
	AppSet       string // github.app_set (FULLSEND_APP_SET)
	ConfigPreset string // BEHAVIOUR_CONFIG_PRESET path or URL (config_base)
	GCPProjectID string // --vertex-project
	WIFProvider  string // --vertex-wif-provider
}

// RunReposInstall installs fullsend into an ephemeral behaviour-test repo
// via `repos install <target> --fullsend-ref`, the no-vendoring path used by
// the ephemeral-repo model. A fresh temp manifest path is used per call for
// concurrency safety; the empty file is removed first so `repos install`
// bootstraps a v1 manifest rather than failing on a version-0 empty file. The
// optional ReposInstallOpts fields are written only when set.
func RunReposInstall(
	opts ReposInstallOpts,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	binary, token, target := opts.Binary, opts.Token, opts.Target
	mintURL, fullsendRef, appSet := opts.MintURL, opts.FullsendRef, opts.AppSet
	configPreset, gcpProjectID, wifProvider := opts.ConfigPreset, opts.GCPProjectID, opts.WIFProvider
	// The manifest lives in a fresh temp dir so a local config preset can be
	// staged next to it: `repos install` only reads local presets from inside
	// the manifest directory. The dir (manifest + any staged preset) is
	// removed on return, so every call installs from a fresh manifest.
	dir, err := os.MkdirTemp("", "fullsend-bt-manifest-")
	if err != nil {
		return fmt.Errorf("creating manifest dir: %w", err)
	}
	defer os.RemoveAll(dir)
	manifest := filepath.Join(dir, "repos.yaml")
	os.Remove(manifest) // `repos set` bootstraps a v1 manifest; an empty file parses as version 0.

	// repos install is manifest-driven (#8219): vendor, runtime, mint_url,
	// fullsend_ref, app_set, inference.auth, config_base and repository
	// membership live in repos.yaml, not on flags. Build the manifest with
	// `repos set`, then install. Only credentials and one-run controls stay
	// on the flags.
	set := func(keyVal ...string) error {
		args := append([]string{"repos", "set"}, keyVal...)
		args = append(args, "-f", manifest)
		if _, err := runCLI(binary, token, args...); err != nil {
			return fmt.Errorf("repos set %s: %w", keyVal[0], err)
		}
		return nil
	}
	entry := func(suffix string) string { return fmt.Sprintf("github.repos[%q]%s", target, suffix) }

	steps := [][]string{
		{"defaults.vendor", "true"},
		{"defaults.inference.auth", "vertex-wif"},
	}
	if fullsendRef != "" {
		steps = append(steps, []string{"github.fullsend_ref", fullsendRef})
	}
	if appSet != "" {
		steps = append(steps, []string{"github.app_set", appSet})
	}
	steps = append(steps, []string{entry(""), "{}"})
	// mint_url goes in the entry's managed config (not the entry's mint_url)
	// so it overrides a config preset's mint_url field by field — the way
	// `github setup --mint-url` did — rather than tripping the manifest
	// mint_url conflict check (#8483).
	if mintURL != "" {
		steps = append(steps, []string{entry(".config.mint_url"), mintURL})
	}
	// Config preset (BEHAVIOUR_CONFIG_PRESET): declare it as config_base.
	// A local preset is staged next to the manifest and referenced by its
	// relative name. With a preset and no explicit runtime, config.runtime
	// is left unset so the preset's runtime is inherited; otherwise the
	// behaviour suite's "dummy" runtime is pinned.
	if preset := strings.TrimSpace(configPreset); preset != "" {
		source, err := stageConfigPreset(dir, preset)
		if err != nil {
			return err
		}
		steps = append(steps, []string{entry(".config_base"), fmt.Sprintf("{source: %s}", source)})
	} else {
		steps = append(steps, []string{entry(".config.runtime"), "dummy"})
	}
	for _, s := range steps {
		if err := set(s...); err != nil {
			return err
		}
	}

	args := []string{"repos", "install", target, "-f", manifest, "--direct"}
	if project := strings.TrimSpace(gcpProjectID); project != "" {
		args = append(args, "--vertex-project", project)
	}
	if wif := strings.TrimSpace(wifProvider); wif != "" {
		args = append(args, "--vertex-wif-provider", wif)
	}

	logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := runCLI(binary, token, args...); err != nil {
		return fmt.Errorf("repos install %s: %w", target, err)
	}
	return nil
}

// stageConfigPreset returns the config_base.source for a behaviour-test
// config preset. An HTTPS URL is used as is. A local path (relative paths
// resolve against the module root) is copied into dir as config-base.yaml,
// because `repos install` rejects local presets outside the manifest
// directory, and its relative name is returned. (Ported from #8483.)
func stageConfigPreset(dir, preset string) (string, error) {
	const presetFileName = "config-base.yaml"
	if strings.HasPrefix(strings.ToLower(preset), "https://") {
		return preset, nil
	}
	if strings.Contains(preset, "://") {
		return "", fmt.Errorf("config preset %q: only local paths and https:// URLs are supported", preset)
	}
	path := preset
	if !filepath.IsAbs(path) {
		root, err := moduleRootFn()
		if err != nil {
			return "", fmt.Errorf("resolving config preset %q: %w", preset, err)
		}
		path = filepath.Join(root, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading config preset: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, presetFileName), data, 0o600); err != nil {
		return "", fmt.Errorf("copying config preset: %w", err)
	}
	return presetFileName, nil
}

// moduleRootFn returns the module root directory. Overridable in tests.
var moduleRootFn = func() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("finding module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// EnvFullsendRef resolves the fullsend ref for behaviour tests.
// Resolution chain: BEHAVIOUR_FULLSEND_REF → PR head SHA from the event
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

// prHeadSHAFromEvent extracts the PR head SHA from a GitHub event payload.
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

// ResolveOrgWIFProvider looks up the org-level Workload Identity Federation
// provider via `fullsend inference status <org>` and returns the full
// provider path. The ephemeral-repo model resolves this once per run and
// reuses it for every repo (ADR 0163), rather than provisioning a per-repo
// provider.
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
