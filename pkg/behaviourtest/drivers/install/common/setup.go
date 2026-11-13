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

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
)

// CLIRunnerFunc is the signature for running a fullsend CLI command.
type CLIRunnerFunc = func(binary, token string, args ...string) (string, error)

// InstallOpts configures how RunReposInstallWithOpts provisions a test
// repository with manifest-driven `fullsend repos install`. Persistent
// settings are written to a generated repos.yaml entry; only credential
// inputs and one-run controls are passed as flags. Use
// DefaultInstallOpts() for vendored-mode defaults (zero value is
// non-vendored).
type InstallOpts struct {
	// Vendor is the entry's vendor setting. When true, repos install
	// vendors the binary it resolves from the CLI's working directory
	// (the current checkout). When false, FullsendRef must be set so the
	// shim references a remote ref instead of a vendored binary.
	Vendor bool

	// FullsendRef is the entry's fullsend_ref when non-empty. Used in
	// non-vendored mode to pin the reusable workflow ref (e.g. "main").
	FullsendRef string

	// ConfigPreset is the entry's config_base.source when non-empty (a
	// local path or HTTPS URL to a config base layer preset). A local
	// preset is copied next to the generated manifest, because repos
	// install only reads local presets from inside the manifest
	// directory. When set and Runtime is empty, config.runtime is omitted
	// so the preset's runtime is inherited rather than pinned in the
	// managed configuration.
	ConfigPreset string

	// PresetBaseDir is the directory a relative local ConfigPreset is
	// resolved against. Empty uses the module root, the working directory
	// e2etest.TryRunCLI runs the CLI in.
	PresetBaseDir string

	// Runtime is the entry's config.runtime. When empty, the "dummy"
	// runtime is used unless a preset is supplied. Playback suites set
	// this to "dummy-playback" while retaining the standard repo-pool
	// lifecycle (allocation, install, validation, teardown).
	Runtime string

	// AppSet is the entry's app_set: the GitHub App identities used by the
	// configured mint.
	AppSet string

	// ManifestDir is the parent directory for the temporary directory
	// holding each install's generated repos.yaml. Empty uses the
	// system temporary directory.
	ManifestDir string

	// ResolveWIFProvider, when set, returns the inference WIF provider
	// for target in project. Callers use it to cache or serialise the
	// lookup. When nil, ResolveInferenceWIFProvider is used.
	ResolveWIFProvider func(target, project string) (string, error)
}

// DefaultInstallOpts returns vendored-mode defaults.
func DefaultInstallOpts() InstallOpts {
	return InstallOpts{Vendor: true}
}

// inferenceAuthVertexWIF is the inference.auth method declared for every
// test repository. Behaviour suites resolve a repo-scoped Vertex WIF
// provider (see ResolveInferenceWIFProvider) and pass it with the project
// as credential inputs. Duplicated from internal/repos so this package
// does not import it (internal/repos pulls the nested mintcore module).
const inferenceAuthVertexWIF = "vertex-wif"

// manifestFileName is the file name of the generated manifest inside the
// per-install temporary directory.
const manifestFileName = "repos.yaml"

// presetFileName is the name a local ConfigPreset is copied to, next to
// the generated manifest.
const presetFileName = "config-base.yaml"

// installManifest mirrors the subset of the repos.yaml schema the
// behaviour driver declares. It is kept local so this package does not
// import internal/repos.
type installManifest struct {
	Version int                  `yaml:"version"`
	GitHub  installManifestForge `yaml:"github"`
}

type installManifestForge struct {
	Repos []installManifestEntry `yaml:"repos"`
}

type installManifestEntry struct {
	Name        string                     `yaml:"name"`
	FullsendRef string                     `yaml:"fullsend_ref,omitempty"`
	AppSet      string                     `yaml:"app_set,omitempty"`
	Inference   installManifestInference   `yaml:"inference"`
	Vendor      bool                       `yaml:"vendor"`
	ConfigBase  *installManifestConfigBase `yaml:"config_base,omitempty"`
	Config      installManifestConfig      `yaml:"config"`
}

type installManifestInference struct {
	Auth string `yaml:"auth"`
}

type installManifestConfigBase struct {
	Source string `yaml:"source"`
}

// installManifestConfig is the managed .fullsend/config.yaml layer for
// the entry. mint_url lives here rather than in the entry's mint_url so
// it overrides a preset's mint_url field by field, as github setup's
// --mint-url did, instead of conflicting with it.
type installManifestConfig struct {
	Runtime string `yaml:"runtime,omitempty"`
	MintURL string `yaml:"mint_url,omitempty"`
}

// moduleRootFn returns the module root directory. Override in tests to
// return a fixed directory instead of shelling out to `go list`.
var moduleRootFn = func() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("finding module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RunReposInstallWithOpts installs fullsend on target with `fullsend
// repos install`. It writes a repos.yaml that declares target with the
// settings from opts and mintURL, then runs install against that
// manifest with --direct. If gcpProjectID is non-empty, the inference
// WIF provider is resolved first (see ResolveInferenceWIFProvider) and
// passed with the project as --vertex-project / --vertex-wif-provider.
//
// The fullsend binary must support `repos install --manifest` together
// with --vertex-project and --vertex-wif-provider. A relative local
// ConfigPreset is resolved against InstallOpts.PresetBaseDir, which
// defaults to the module root found with `go list -m`; that default
// needs `go` on PATH and a working directory inside a module.
//
// The manifest lives in a temporary directory that is removed on return,
// so every call (including a retry) installs from a fresh manifest.
// repos install never creates a manifest; when this one cannot be
// written, the error is returned and the CLI is not run.
func RunReposInstallWithOpts(
	binary, token, target, mintURL, gcpProjectID string,
	opts InstallOpts,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) error {
	if !opts.Vendor && opts.FullsendRef == "" {
		return fmt.Errorf("repos install %s: non-vendored mode requires FullsendRef to be set", target)
	}
	if opts.Vendor && opts.FullsendRef != "" {
		return fmt.Errorf("repos install %s: vendored mode conflicts with FullsendRef %q — use one or the other", target, opts.FullsendRef)
	}

	var credentialArgs []string
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
		credentialArgs = []string{"--vertex-project", project, "--vertex-wif-provider", wifProvider}
	}

	dir, err := os.MkdirTemp(opts.ManifestDir, "fullsend-bt-manifest-")
	if err != nil {
		return fmt.Errorf("repos install %s: creating manifest directory: %w", target, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	manifest, err := writeInstallManifest(dir, target, mintURL, opts)
	if err != nil {
		return fmt.Errorf("repos install %s: %w", target, err)
	}
	manifestPath := filepath.Join(dir, manifestFileName)

	args := []string{
		"repos", "install", target,
		"--manifest", manifestPath,
		"--direct",
	}
	args = append(args, credentialArgs...)

	logf("[install] generated %s for %s:\n%s", manifestPath, target, manifest)
	logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := runCLI(binary, token, args...); err != nil {
		return fmt.Errorf("repos install %s: %w", target, err)
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

// writeInstallManifest writes dir/repos.yaml declaring target with the
// settings from opts and mintURL, and returns the manifest content. A
// local ConfigPreset is copied into dir and referenced by its relative
// name. dir must already exist.
func writeInstallManifest(dir, target, mintURL string, opts InstallOpts) ([]byte, error) {
	entry := installManifestEntry{
		Name:        target,
		FullsendRef: opts.FullsendRef,
		AppSet:      opts.AppSet,
		Inference:   installManifestInference{Auth: inferenceAuthVertexWIF},
		Vendor:      opts.Vendor,
		Config: installManifestConfig{
			Runtime: opts.Runtime,
			MintURL: strings.TrimSpace(mintURL),
		},
	}
	// Explicit Runtime wins over a preset's runtime: doEnsure's
	// post-install validation checks against opts.Runtime, so the
	// installed runtime must match it. Without a preset, an empty
	// Runtime selects the normal "dummy" runtime.
	if preset := strings.TrimSpace(opts.ConfigPreset); preset != "" {
		source, err := stagePreset(dir, preset, opts.PresetBaseDir)
		if err != nil {
			return nil, err
		}
		entry.ConfigBase = &installManifestConfigBase{Source: source}
	} else if entry.Config.Runtime == "" {
		entry.Config.Runtime = "dummy"
	}

	data, err := yaml.Marshal(installManifest{
		Version: 1,
		GitHub:  installManifestForge{Repos: []installManifestEntry{entry}},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), data, 0o600); err != nil {
		return nil, fmt.Errorf("writing manifest: %w", err)
	}
	return data, nil
}

// stagePreset returns the config_base.source for preset. An HTTPS URL is
// used as is. A local path (resolved against baseDir, or the module root
// when baseDir is empty) is copied into dir, because repos install
// rejects local presets outside the manifest directory.
func stagePreset(dir, preset, baseDir string) (string, error) {
	if strings.HasPrefix(strings.ToLower(preset), "https://") {
		return preset, nil
	}
	if strings.Contains(preset, "://") {
		return "", fmt.Errorf("config preset %q: only local paths and https:// URLs are supported", preset)
	}
	path := preset
	if !filepath.IsAbs(path) {
		if baseDir == "" {
			root, err := moduleRootFn()
			if err != nil {
				return "", fmt.Errorf("resolving config preset %q: %w", preset, err)
			}
			baseDir = root
		}
		path = filepath.Join(baseDir, path)
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
