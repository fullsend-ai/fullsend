// Package cf implements the dispatch.Dispatcher interface using a
// Cloudflare Worker as the token mint. The Worker runs the mintcore
// WASM module compiled from cmd/mint-wasm, with a thin TypeScript
// adapter (workersrc/) handling I/O. Credentials are read from env
// vars (CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_API_TOKEN) — no secrets
// are passed as CLI flags.
package cf

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// DeployMode controls Worker deployment behavior.
type DeployMode int

const (
	// DeployDurable deploys a persistent, production Worker.
	DeployDurable DeployMode = iota
	// DeployPreview deploys an ephemeral preview Worker for testing.
	DeployPreview
)

const (
	defaultWorkerName   = "fullsend-mint"
	defaultOIDCAudience = "fullsend-mint"
)

// workerNamePattern validates Cloudflare Worker names.
// Worker names must be lowercase alphanumeric with hyphens, 2-63 chars.
var workerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// Compile-time check that Provisioner implements dispatch.Dispatcher.
var _ dispatch.Dispatcher = (*Provisioner)(nil)

// embeddedWorkerSource contains the TypeScript Worker adapter source
// files. These are extracted to a temp directory at deploy time so
// wrangler can build and deploy the Worker.
//
// The WASM binary (mintcore.wasm) and Go WASM support (wasm_exec.js)
// are NOT embedded here — they are build artifacts staged by
// `make wasm-stage`. The provisioner expects them to be present in
// the source directory at deploy time.
//
//go:embed workersrc/src/index.ts workersrc/src/version.ts workersrc/wrangler.toml workersrc/package.json workersrc/tsconfig.json workersrc/wasm.d.ts workersrc/wasm_exec.d.ts
var embeddedWorkerSource embed.FS

// embeddedWorkerFiles lists the embedded files for extraction.
// Maps embedded path (under workersrc/) to extraction path.
var embeddedWorkerFiles = []string{
	"workersrc/src/index.ts",
	"workersrc/src/version.ts",
	"workersrc/wrangler.toml",
	"workersrc/package.json",
	"workersrc/tsconfig.json",
	"workersrc/wasm.d.ts",
	"workersrc/wasm_exec.d.ts",
}

// Config holds the inputs for CF Worker mint provisioning.
type Config struct {
	// AccountID is the Cloudflare account ID. Read from
	// CLOUDFLARE_ACCOUNT_ID env var.
	AccountID string

	// WorkerName is the Worker script name (e.g. "fullsend-mint",
	// "fullsend-mint-test"). Defaults to "fullsend-mint".
	WorkerName string

	// DeployMode controls whether the Worker is deployed as a durable
	// production Worker or an ephemeral preview.
	DeployMode DeployMode

	// SourceDir overrides the embedded Worker source with a local
	// directory. When set, the provisioner uses this path directly
	// instead of extracting embedded files. The directory must
	// contain the workersrc tree including mintcore.wasm and
	// wasm_exec.js (staged by `make wasm-stage`).
	SourceDir string

	// PreviewAlias is the Wrangler preview alias for preview deploys.
	// When set (and DeployMode is DeployPreview), the provisioner uses
	// `wrangler versions upload --preview-alias=<alias>` instead of
	// `wrangler deploy`. The preview mint URL is deterministic:
	// https://<alias>-<worker-name>.workers.dev
	PreviewAlias string

	// EnvVars are non-secret environment variables to set on the Worker
	// (e.g. ROLE_APP_IDS, ALLOWED_ORGS, OIDC_AUDIENCE).
	EnvVars map[string]string

	// Secrets are secret values to bind to the Worker during deploy.
	// When non-empty, Deploy writes them to a temporary JSON file and
	// passes --secrets-file to wrangler versions upload. Use this for
	// preview deploys where wrangler secret put cannot scope secrets
	// to a preview version.
	Secrets map[string][]byte

	// Version is the fullsend semver stamped on the deployed Worker.
	Version string

	// Commit is the git SHA stamped on the deployed Worker.
	Commit string
}

// WranglerRunner abstracts wrangler CLI operations for testing.
type WranglerRunner interface {
	// Deploy deploys a Worker from sourceDir. Returns the Worker URL.
	// When previewAlias is non-empty, the runner uses
	// `wrangler versions upload --preview-alias=<alias>` instead of
	// the production `wrangler deploy`. An empty previewAlias triggers
	// a durable production deploy.
	//
	// When secrets is non-empty, the runner writes them to a temporary
	// JSON file and passes --secrets-file to wrangler. This is required
	// for preview deploys because wrangler secret put does not support
	// --preview-alias.
	Deploy(ctx context.Context, sourceDir, workerName string, previewAlias string, envVars map[string]string, secrets map[string][]byte) (url string, err error)

	// PutSecret stores a secret value on the durable Worker via
	// wrangler secret put. This command does not support --preview-alias;
	// for preview deploys, pass secrets through Deploy instead.
	PutSecret(ctx context.Context, workerName, secretName string, value []byte) error

	// Delete removes a Worker deployment.
	Delete(ctx context.Context, workerName string) error
}

// Provisioner creates Cloudflare Worker infrastructure for token minting.
type Provisioner struct {
	cfg      Config
	wrangler WranglerRunner
}

// NewProvisioner creates a new CF Provisioner with defaults applied.
func NewProvisioner(cfg Config, wrangler WranglerRunner) *Provisioner {
	if cfg.WorkerName == "" {
		cfg.WorkerName = defaultWorkerName
	}
	if cfg.EnvVars == nil {
		cfg.EnvVars = make(map[string]string)
	}
	if cfg.EnvVars["OIDC_AUDIENCE"] == "" {
		cfg.EnvVars["OIDC_AUDIENCE"] = defaultOIDCAudience
	}
	return &Provisioner{cfg: cfg, wrangler: wrangler}
}

// Name returns the dispatcher identifier.
func (p *Provisioner) Name() string {
	return "cf"
}

// OrgSecretNames returns nil — PEM secrets are stored as Worker secrets.
func (p *Provisioner) OrgSecretNames() []string {
	return nil
}

// OrgVariableNames returns the org variables this dispatcher manages.
func (p *Provisioner) OrgVariableNames() []string {
	return []string{"FULLSEND_MINT_URL"}
}

// Provision deploys the Cloudflare Worker mint and returns the Worker
// URL as FULLSEND_MINT_URL.
func (p *Provisioner) Provision(ctx context.Context) (map[string]string, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	sourceDir, cleanup, err := p.resolveSourceDir()
	if err != nil {
		return nil, fmt.Errorf("resolving worker source: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Stamp version metadata into the Worker source at deploy time so
	// the WASM module can report them via /health and /status. This
	// mirrors the GCF approach (writeVersionGoToZip) — version data is
	// compiled into the deployed bundle and cannot diverge via admin
	// action on environment variables.
	if err := writeVersionTS(sourceDir, p.cfg.Version, p.cfg.Commit); err != nil {
		return nil, fmt.Errorf("writing version.ts: %w", err)
	}

	url, err := p.wrangler.Deploy(ctx, sourceDir, p.cfg.WorkerName, p.cfg.PreviewAlias, p.cfg.EnvVars, p.cfg.Secrets)
	if err != nil {
		return nil, fmt.Errorf("deploying worker: %w", err)
	}

	return map[string]string{
		"FULLSEND_MINT_URL": url,
	}, nil
}

// StoreAgentPEM stores a role's PEM key as a Cloudflare Worker secret
// via wrangler secret put. Secret names follow the convention
// <ROLE>_APP_PEM (e.g. CODER_APP_PEM).
//
// This method is intended for durable (non-preview) deploys. For preview
// deploys, pass secrets via Config.Secrets so they are included in the
// wrangler versions upload --secrets-file call, because wrangler secret
// put does not support --preview-alias.
func (p *Provisioner) StoreAgentPEM(ctx context.Context, role string, pemData []byte) error {
	if err := mintcore.ValidateRoleName(role); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role, err)
	}
	secretName := pemSecretName(role)
	if err := p.wrangler.PutSecret(ctx, p.cfg.WorkerName, secretName, pemData); err != nil {
		return fmt.Errorf("storing PEM secret %s: %w", secretName, err)
	}
	return nil
}

// Teardown cleans up a preview Worker deployment. Only valid when
// DeployMode is DeployPreview.
//
// Preview-alias deploys use `wrangler versions upload`, which creates
// a version routed via the alias. The durable Worker script is shared
// with production, so teardown abandons the preview version without
// deleting the Worker script. The alias is simply left unrouted — it
// will be overwritten on the next preview deploy or can be cleaned up
// manually via `wrangler versions list`.
//
// Note: validate() enforces that DeployPreview always has a non-empty
// PreviewAlias, so the bare-preview (delete Worker) path is no longer
// reachable through normal Provisioner lifecycle.
func (p *Provisioner) Teardown(ctx context.Context) error {
	if p.cfg.DeployMode != DeployPreview {
		return fmt.Errorf("teardown is only supported for preview Workers")
	}
	// Preview-alias teardown: abandon the alias without deleting the
	// durable Worker script, which is shared with production.
	return nil
}

// validate checks that the Config has all required fields.
func (p *Provisioner) validate() error {
	if p.cfg.AccountID == "" {
		return fmt.Errorf("CLOUDFLARE_ACCOUNT_ID is required (set via environment variable)")
	}
	if !ValidateWorkerName(p.cfg.WorkerName) {
		return fmt.Errorf("invalid Worker name %q: must be 2-63 lowercase alphanumeric characters or hyphens", p.cfg.WorkerName)
	}
	// Guard against DeployPreview with an empty alias: Provision routes
	// on PreviewAlias (empty → durable deploy) while Teardown routes on
	// DeployMode (DeployPreview → delete). This mismatch would cause a
	// durable deploy followed by a destructive teardown.
	if p.cfg.DeployMode == DeployPreview && p.cfg.PreviewAlias == "" {
		return fmt.Errorf("DeployPreview requires a non-empty PreviewAlias")
	}
	// Guard against the inverse: DeployDurable with a non-empty alias.
	// Provision routes on PreviewAlias (non-empty → preview deploy) while
	// Teardown routes on DeployMode (DeployDurable → rejected). This
	// mismatch would cause a preview deploy that cannot be torn down.
	if p.cfg.DeployMode != DeployPreview && p.cfg.PreviewAlias != "" {
		return fmt.Errorf("PreviewAlias %q requires DeployMode=DeployPreview", p.cfg.PreviewAlias)
	}
	return nil
}

// resolveSourceDir returns the path to the Worker source directory,
// either from Config.SourceDir or by extracting embedded files to
// a temp directory. Returns a cleanup function for temp dirs.
func (p *Provisioner) resolveSourceDir() (string, func(), error) {
	if p.cfg.SourceDir != "" {
		if err := validateSourceDir(p.cfg.SourceDir); err != nil {
			return "", nil, err
		}
		return p.cfg.SourceDir, nil, nil
	}

	// Extract embedded source to temp directory.
	tmpDir, err := os.MkdirTemp("", "fullsend-cf-worker-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	if err := extractEmbeddedSource(tmpDir); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("extracting embedded source: %w", err)
	}

	return tmpDir, cleanup, nil
}

// extractEmbeddedSource writes the embedded Worker source files to dir.
func extractEmbeddedSource(dir string) error {
	for _, path := range embeddedWorkerFiles {
		data, err := embeddedWorkerSource.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}

		// Strip the "workersrc/" prefix for the extraction path.
		relPath := strings.TrimPrefix(path, "workersrc/")
		destPath := filepath.Join(dir, relPath)

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", relPath, err)
		}
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", relPath, err)
		}
	}
	return nil
}

// writeVersionTS writes a generated version.ts into the Worker source
// directory with the provided version and commit values. This stamps
// the version identity directly into the deployed source code —
// mirroring how writeVersionGoToZip works for GCF deploys — so it
// cannot drift from the running code via admin changes to env vars.
func writeVersionTS(dir, version, commit string) error {
	src := fmt.Sprintf(
		"// Generated at deploy time by the CF provisioner. Do not edit.\n"+
			"export const FULLSEND_VERSION = %q;\n"+
			"export const FULLSEND_COMMIT = %q;\n",
		version, commit)
	destPath := filepath.Join(dir, "src", "version.ts")
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating directory for version.ts: %w", err)
	}
	return os.WriteFile(destPath, []byte(src), 0o644)
}

// validateSourceDir checks that a source directory contains the
// required Worker files.
func validateSourceDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("source-dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source-dir %q is not a directory", dir)
	}

	required := []string{
		"src/index.ts",
		"wrangler.toml",
		"package.json",
	}
	for _, name := range required {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("source-dir missing required file %s: %w", name, err)
		}
	}
	return nil
}

// pemSecretName returns the Cloudflare Worker secret name for a role's
// PEM key. Follows the convention <ROLE>_APP_PEM with hyphens mapped
// to underscores (CF secret names must be valid JS identifiers).
func pemSecretName(role string) string {
	mapped := mintcore.PemSecretRole(role)
	return strings.ToUpper(strings.ReplaceAll(mapped, "-", "_")) + "_APP_PEM"
}

// ValidateWorkerName checks if a string is a valid CF Worker name.
func ValidateWorkerName(name string) bool {
	return workerNamePattern.MatchString(name)
}

// previewAliasPattern validates Cloudflare preview alias names.
// Aliases must be lowercase alphanumeric with hyphens, 2-63 chars —
// same constraints as Worker names since the alias appears in the URL.
var previewAliasPattern = workerNamePattern

// ValidatePreviewAlias checks if a string is a valid CF preview alias.
func ValidatePreviewAlias(alias string) bool {
	return previewAliasPattern.MatchString(alias)
}

// DefaultWorkerSourceDir returns the default path to the Worker source
// directory. This assumes the CLI is run from the repository root.
func DefaultWorkerSourceDir() string {
	return filepath.Join("internal", "dispatch", "cf", "workersrc")
}

// ValidateCloudflareEnv checks that required Cloudflare environment
// variables are set. Returns an error listing all missing variables.
//
// Deprecated: Use ResolveCloudflareAuth which also accepts Wrangler OAuth
// sessions as an alternative to CLOUDFLARE_API_TOKEN.
func ValidateCloudflareEnv() error {
	var missing []string
	if os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "" {
		missing = append(missing, "CLOUDFLARE_ACCOUNT_ID")
	}
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" {
		missing = append(missing, "CLOUDFLARE_API_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required Cloudflare environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

// WranglerWhoamiFn is the function used to run `wrangler whoami`.
// Override in tests to avoid needing a real wrangler installation.
var WranglerWhoamiFn = runWranglerWhoami

// runWranglerWhoami executes `npx wrangler whoami` and returns the
// combined stdout+stderr output.
func runWranglerWhoami(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "whoami")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ResolveCloudflareAuth resolves Cloudflare authentication and returns
// the account ID. It prefers explicit environment variables when set, but
// falls back to a Wrangler OAuth session (from 'wrangler login') when
// CLOUDFLARE_API_TOKEN is absent.
//
// Resolution order:
//  1. CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID env vars → use both
//  2. CLOUDFLARE_API_TOKEN set, CLOUDFLARE_ACCOUNT_ID unset → error
//  3. CLOUDFLARE_API_TOKEN unset → check for Wrangler session via whoami
//     a. If CLOUDFLARE_ACCOUNT_ID is set → use it
//     b. If whoami output contains exactly one account → use its ID
//     c. Otherwise → error with guidance
func ResolveCloudflareAuth(ctx context.Context) (accountID string, err error) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	envAccountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")

	if token != "" {
		// Explicit API token — require account ID too.
		if envAccountID == "" {
			return "", fmt.Errorf("CLOUDFLARE_API_TOKEN is set but CLOUDFLARE_ACCOUNT_ID is missing; set both for API-token auth")
		}
		return envAccountID, nil
	}

	// No API token — check for Wrangler OAuth session.
	whoamiOut, whoamiErr := WranglerWhoamiFn(ctx)
	if whoamiErr != nil {
		if envAccountID != "" {
			return "", fmt.Errorf("CLOUDFLARE_API_TOKEN is not set and 'wrangler whoami' failed: %w\nSet CLOUDFLARE_API_TOKEN or run 'wrangler login' first", whoamiErr)
		}
		return "", fmt.Errorf("no Cloudflare credentials: CLOUDFLARE_API_TOKEN is not set and 'wrangler whoami' failed: %w\nEither set CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID, or run 'wrangler login'", whoamiErr)
	}

	// Wrangler session is valid. Resolve account ID.
	if envAccountID != "" {
		return envAccountID, nil
	}

	// Try to parse account ID from whoami output.
	// `wrangler whoami` typically prints lines like:
	//   │ Account Name    │ Account ID                       │
	//   │ My Account      │ abc123def456                     │
	parsed := parseWranglerWhoamiAccountID(whoamiOut)
	if parsed == "" {
		return "", fmt.Errorf("wrangler login session is active but CLOUDFLARE_ACCOUNT_ID is not set and could not be auto-detected from 'wrangler whoami' output; set CLOUDFLARE_ACCOUNT_ID explicitly")
	}
	return parsed, nil
}

// parseWranglerWhoamiAccountID extracts the account ID from wrangler
// whoami output. Returns the account ID if exactly one is found, or
// empty string if zero or multiple accounts are present (the user must
// set CLOUDFLARE_ACCOUNT_ID explicitly in that case).
func parseWranglerWhoamiAccountID(output string) string {
	// wrangler whoami prints a table like:
	//   ┌──────────────────┬──────────────────────────────────┐
	//   │ Account Name     │ Account ID                       │
	//   ├──────────────────┼──────────────────────────────────┤
	//   │ My Account       │ abc123def456789...               │
	//   └──────────────────┴──────────────────────────────────┘
	//
	// We look for lines with exactly two pipe-delimited cells where
	// the second cell looks like a 32-char hex account ID.
	var accountIDs []string
	for line := range strings.SplitSeq(output, "\n") {
		parts := strings.Split(line, "│")
		if len(parts) < 3 {
			continue
		}
		// The second column (parts[2]) is the Account ID.
		candidate := strings.TrimSpace(parts[2])
		if len(candidate) == 32 && isHex(candidate) {
			accountIDs = append(accountIDs, candidate)
		}
	}
	if len(accountIDs) == 1 {
		return accountIDs[0]
	}
	return ""
}

// isHex returns true if s consists entirely of hexadecimal characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// --- LiveWranglerRunner ---

// LiveWranglerRunner executes wrangler commands via the CLI.
type LiveWranglerRunner struct {
	// AccountID is passed to wrangler via CLOUDFLARE_ACCOUNT_ID.
	AccountID string
}

// NewLiveWranglerRunner creates a runner that uses the real wrangler CLI.
func NewLiveWranglerRunner(accountID string) *LiveWranglerRunner {
	return &LiveWranglerRunner{AccountID: accountID}
}

// Deploy deploys a Worker from sourceDir using wrangler.
//
// When previewAlias is non-empty, uses `wrangler versions upload` with
// `--preview-alias=<alias>` for a preview deploy. The preview URL is
// deterministic: https://<alias>-<workerName>.workers.dev
//
// When previewAlias is empty, uses `wrangler deploy` for a durable
// production deploy.
//
// When secrets is non-empty, writes them to a temporary JSON file and
// passes --secrets-file. This is the only way to attach secrets to a
// preview version, since wrangler secret put does not support
// --preview-alias.
func (r *LiveWranglerRunner) Deploy(ctx context.Context, sourceDir, workerName string, previewAlias string, envVars map[string]string, secrets map[string][]byte) (string, error) {
	if previewAlias != "" {
		return r.deployPreview(ctx, sourceDir, workerName, previewAlias, envVars, secrets)
	}
	return r.deployDurable(ctx, sourceDir, workerName, envVars, secrets)
}

// deployDurable performs a production deploy via `wrangler deploy`.
// Secrets passed here are stored via separate PutSecret calls by the
// caller (Provisioner or CLI) after deploy completes — wrangler deploy
// does not support --secrets-file. The secrets parameter is accepted
// for interface consistency but not used in the deploy command.
func (r *LiveWranglerRunner) deployDurable(ctx context.Context, sourceDir, workerName string, envVars map[string]string, _ map[string][]byte) (string, error) {
	args := []string{"wrangler", "deploy", "--name", workerName}
	// Always pass --keep-vars to preserve existing Worker secrets
	// (e.g. PEM keys stored via StoreAgentPEM). Without this flag,
	// wrangler overwrites all bindings on each deploy, wiping secrets.
	args = append(args, "--keep-vars")

	// Pass env vars to wrangler via --var flags.
	for k, v := range envVars {
		args = append(args, "--var", fmt.Sprintf("%s:%s", k, v))
	}

	cmd := exec.CommandContext(ctx, "npx", args...)
	cmd.Dir = sourceDir
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wrangler deploy failed: %s\n%s", err, string(output))
	}

	// Parse the Worker URL from wrangler output.
	url := parseWorkerURL(string(output), workerName)
	if url == "" {
		url = fmt.Sprintf("https://%s.workers.dev", workerName)
	}
	return url, nil
}

// deployPreview performs a preview deploy via `wrangler versions upload`.
// When secrets are provided, they are written to a temporary JSON file
// and passed via --secrets-file. This is the only way to attach secrets
// to a preview version because wrangler secret put does not support
// --preview-alias.
func (r *LiveWranglerRunner) deployPreview(ctx context.Context, sourceDir, workerName, previewAlias string, envVars map[string]string, secrets map[string][]byte) (string, error) {
	args := []string{"wrangler", "versions", "upload", "--name", workerName}
	args = append(args, fmt.Sprintf("--preview-alias=%s", previewAlias))
	// Pass --keep-vars to preserve existing Worker secrets (PEM keys
	// stored via StoreAgentPEM on the durable Worker). Preview-alias
	// deploys target the same Worker script as production, so omitting
	// this could wipe secrets.
	args = append(args, "--keep-vars")

	// Pass env vars to wrangler via --var flags.
	for k, v := range envVars {
		args = append(args, "--var", fmt.Sprintf("%s:%s", k, v))
	}

	// Pass secrets via --secrets-file when present.
	if len(secrets) > 0 {
		secretsPath, cleanup, err := writeSecretsFile(secrets)
		if err != nil {
			return "", fmt.Errorf("preparing secrets file: %w", err)
		}
		defer cleanup()
		args = append(args, fmt.Sprintf("--secrets-file=%s", secretsPath))
	}

	cmd := exec.CommandContext(ctx, "npx", args...)
	cmd.Dir = sourceDir
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wrangler versions upload failed: %s\n%s", err, string(output))
	}

	// Preview URL is deterministic from the alias and worker name.
	// We don't use parseWorkerURL here because wrangler output may
	// contain the production Worker URL, which parseWorkerURL would
	// match as a false positive. The deterministic pattern is reliable.
	url := fmt.Sprintf("https://%s-%s.workers.dev", previewAlias, workerName)
	return url, nil
}

// PutSecret stores a secret value on the durable Worker via wrangler
// secret put. This command does not support --preview-alias; for preview
// deploys, pass secrets through Deploy's secrets parameter instead.
func (r *LiveWranglerRunner) PutSecret(ctx context.Context, workerName, secretName string, value []byte) error {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "secret", "put", secretName, "--name", workerName)
	cmd.Stdin = strings.NewReader(string(value))
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wrangler secret put failed: %s\n%s", err, string(output))
	}
	return nil
}

// Delete removes a Worker deployment via wrangler delete.
func (r *LiveWranglerRunner) Delete(ctx context.Context, workerName string) error {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "delete", "--name", workerName, "--force")
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wrangler delete failed: %s\n%s", err, string(output))
	}
	return nil
}

// PEMSecretsFromRoles converts a role-keyed PEM map (e.g. "coder" → PEM data)
// into a Cloudflare secret-name-keyed map (e.g. "CODER_APP_PEM" → PEM data)
// suitable for passing as Config.Secrets during deploy.
func PEMSecretsFromRoles(agentPEMs map[string][]byte) map[string][]byte {
	secrets := make(map[string][]byte, len(agentPEMs))
	for role, pem := range agentPEMs {
		secrets[pemSecretName(role)] = pem
	}
	return secrets
}

// writeSecretsFile writes secrets to a temporary JSON file suitable for
// wrangler's --secrets-file parameter. Returns the file path and a cleanup
// function that removes the file. The file is created with restrictive
// permissions (0600) since it may contain sensitive values like PEM keys.
func writeSecretsFile(secrets map[string][]byte) (string, func(), error) {
	// Convert []byte values to strings for JSON encoding.
	jsonMap := make(map[string]string, len(secrets))
	for k, v := range secrets {
		jsonMap[k] = string(v)
	}
	data, err := json.Marshal(jsonMap)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling secrets: %w", err)
	}
	f, err := os.CreateTemp("", "wrangler-secrets-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("writing secrets: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", nil, fmt.Errorf("closing secrets file: %w", err)
	}
	cleanup := func() { os.Remove(path) }
	return path, cleanup, nil
}

// parseWorkerURL extracts the deployed Worker URL from wrangler output.
func parseWorkerURL(output, _ string) string {
	// Wrangler prints the URL in various formats. Look for common patterns.
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "workers.dev") && strings.Contains(line, "https://") {
			// Extract URL from the line.
			start := strings.Index(line, "https://")
			if start >= 0 {
				url := line[start:]
				// Trim trailing whitespace and punctuation.
				url = strings.TrimRight(url, " \t\n\r.,;")
				return url
			}
		}
	}
	return ""
}

// --- Test support ---

// EmbeddedWorkerSource returns the embedded Worker source filesystem
// for testing embed integrity.
func EmbeddedWorkerSource() fs.FS {
	return embeddedWorkerSource
}
