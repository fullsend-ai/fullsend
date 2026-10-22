package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newReposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Manage per-repo installations across multiple orgs",
		Long: `Manage per-repo fullsend installations at scale via a declarative repos.yaml manifest.

The repos subcommand group provides bulk operations for platform administrators
managing fullsend across many repositories and organizations.`,
	}
	cmd.PersistentFlags().String("gitlab-token", "", "GitLab personal or project access token (overrides GITLAB_TOKEN env var)")
	cmd.AddCommand(newReposInstallCmd())
	cmd.AddCommand(newReposUninstallCmd())
	cmd.AddCommand(newReposStatusCmd())
	cmd.AddCommand(newReposSetCmd())
	return cmd
}

func newReposSetCmd() *cobra.Command {
	var manifest string
	var unset bool

	cmd := &cobra.Command{
		Use:   "set <path> <value>",
		Short: "Set or remove any field in the repos.yaml manifest",
		Long: `Set or remove a field anywhere in repos.yaml: defaults, the github and
gitlab sections, and individual repository entries, including nested config
fields and structured values. Paths are checked against the manifest schema,
and the whole resulting manifest is validated before it is written. A failed
edit leaves the file unchanged; a successful edit keeps unrelated values,
comments, and ordering. A missing manifest is created with version: 1.

Path syntax:
  Dot-separated YAML field names, for example defaults.config.runtime,
  github.mint_url, or gitlab.agent_runner_tags. Select a repository entry by
  its exact name with a double-quoted selector: github.repos["acme/api"].
  Glob entries are selected by their literal pattern (github.repos["acme/*"]).
  Quote the whole path in your shell.

  Setting a nested path under a repository entry requires the entry to exist.
  Setting the whole entry path creates or replaces the entry; the selector
  supplies its name.

Values:
  Strings are taken literally, so none and "" are stored as strings.
  Booleans must be true or false. Lists and objects take YAML or JSON, for
  example '[gpu, linux]' or '{"auth": "vertex-wif"}'. An empty argument
  never deletes a key; use --unset <path> to remove it.`,
		Example: `  # Set a fleet-wide default and a forge-wide config value
  fullsend repos set defaults.inference.auth vertex-wif
  fullsend repos set github.config.runtime claude

  # Set a list value
  fullsend repos set gitlab.agent_runner_tags '[fullsend-agent, gpu]'

  # Add (or replace) a repository entry
  fullsend repos set 'github.repos["acme/api"]' '{"app_set": "acme"}'

  # Change a field on an existing repository entry
  fullsend repos set 'github.repos["acme/api"].config.runtime' claude

  # Delete a key
  fullsend repos set --unset github.mint_url`,
		Args: func(cmd *cobra.Command, args []string) error {
			if unset {
				if len(args) != 1 {
					return fmt.Errorf("--unset takes exactly one argument (the path), received %d", len(args))
				}
				return nil
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if unset {
				changed, err := repos.UnsetManifestValue(manifest, args[0])
				if err != nil {
					return err
				}
				if changed {
					fmt.Fprintf(out, "Removed %s from %s\n", args[0], manifest)
				} else {
					fmt.Fprintf(out, "%s is not set in %s; nothing changed\n", args[0], manifest)
				}
				return nil
			}
			changed, err := repos.SetManifestValue(manifest, args[0], args[1])
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintf(out, "Set %s in %s\n", args[0], manifest)
			} else {
				fmt.Fprintf(out, "%s already has that value in %s; nothing changed\n", args[0], manifest)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&manifest, "manifest", "f", "repos.yaml", "path to repos.yaml")
	cmd.Flags().BoolVar(&unset, "unset", false, "remove the key at <path> instead of setting it")

	return cmd
}

// reposStatusConfig holds flags and test overrides for repos status.
type reposStatusConfig struct {
	manifest    string
	jsonOutput  bool
	repoFilter  []string
	concurrency int

	// Test overrides
	testClient forge.Client
}

func newReposStatusCmd() *cobra.Command {
	opts := &reposStatusConfig{}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare manifest against actual repo state",
		Long:  "Read-only comparison of the repos.yaml manifest against actual forge state. Reports installation status and configuration drift for each repo, including declared configuration-preset drift against .fullsend/config.base.yaml and managed configuration drift against .fullsend/config.yaml.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposStatus(cmd, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON output instead of table")
	cmd.Flags().StringArrayVar(&opts.repoFilter, "repo", nil, "filter to specific repos (repeatable)")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 8, "max parallel API calls")

	return cmd
}

func runReposStatus(cmd *cobra.Command, opts *reposStatusConfig) error {
	ctx := cmd.Context()

	m, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return err
	}
	// inference.auth values are validated per repository by
	// repos.Status, so an invalid value on one entry is reported as that
	// repository's error instead of aborting the whole command.
	if err := m.ValidateStructure(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	var clients repos.ForgeClientFactory
	if opts.testClient != nil {
		clients = newSingleClientFactory(opts.testClient)
	} else {
		clients = newForgeClientFactory(getGitLabToken(cmd), m)
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()
	result, err := repos.Status(ctx, m, clients, opts.concurrency, opts.repoFilter, repos.DriftConfig{UpstreamRef: upstreamRef, UpstreamTag: upstreamTag})
	if err != nil {
		return err
	}
	annotateGitLabRoleLifecycle(ctx, clients, result)

	return renderStatusResult(cmd, result, opts.jsonOutput)
}

func renderStatusResult(cmd *cobra.Command, result *repos.StatusResult, jsonOutput bool) error {
	if jsonOutput {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling JSON: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
	} else {
		printStatusTable(cmd, result)
	}

	if result.Summary.Drifted > 0 || result.Summary.NotInstalled > 0 || result.Summary.Errored > 0 {
		cmd.SilenceUsage = true
		return fmt.Errorf("%d installed, %d drifted, %d not installed, %d errored",
			result.Summary.Installed, result.Summary.Drifted, result.Summary.NotInstalled, result.Summary.Errored)
	}
	return nil
}

// formatRef formats a ref for display in the status table. SHA-like refs
// (40 hex characters) are truncated to 7 characters. When the expected ref
// is set and differs from the current ref, it is appended in parentheses
// (also truncated if it is a SHA).
//
// SHA detection reuses commitSHAPattern (defined in agent.go) which matches
// exactly 40 lowercase hex characters. This intentionally differs from
// isSHARef in internal/repos/ref_ops.go, which accepts 7–40 hex chars
// case-insensitively; the stricter check here is appropriate because Git
// stores full SHAs as lowercase and status output always receives full refs.
func formatRef(currentRef, expectedRef string) string {
	if currentRef == "" {
		return "—"
	}

	if !commitSHAPattern.MatchString(currentRef) {
		return currentRef
	}

	display := currentRef[:7]
	if expectedRef != "" && expectedRef != currentRef {
		if commitSHAPattern.MatchString(expectedRef) {
			display += " (" + expectedRef[:7] + ")"
		} else {
			display += " (" + expectedRef + ")"
		}
	}

	return display
}

func showGitLabRoleStatus(s repos.RepoStatus) bool {
	// Surface role-credential diagnostics and parse/read/registry errors.
	return len(s.GitLabRoleDiagnostics) > 0
}

func printStatusTable(cmd *cobra.Command, result *repos.StatusResult) {
	out := cmd.OutOrStdout()

	maxRepo := len("REPO")
	maxRef := len("REF")
	for _, s := range result.Repos {
		name := s.Owner + "/" + s.Repo
		if len(name) > maxRepo {
			maxRepo = len(name)
		}
		ref := formatRef(s.CurrentRef, s.ExpectedRef)
		if len(ref) > maxRef {
			maxRef = len(ref)
		}
	}

	fmt.Fprintf(out, "%-*s  %-*s  %-14s  %s\n", maxRepo, "REPO", maxRef, "REF", "STATUS", "DRIFT")
	for _, s := range result.Repos {
		name := s.Owner + "/" + s.Repo
		ref := formatRef(s.CurrentRef, s.ExpectedRef)

		var status string
		switch {
		case s.Error != "":
			status = "error"
		case !s.Installed:
			status = "not installed"
		default:
			status = "installed"
		}

		var drift string
		switch {
		case s.Error != "":
			drift = s.Error
		case len(s.Drifts) == 0:
			drift = "none"
		default:
			fields := make([]string, len(s.Drifts))
			for i, d := range s.Drifts {
				fields[i] = d.Field + " differs"
			}
			drift = strings.Join(fields, ", ")
		}

		fmt.Fprintf(out, "%-*s  %-*s  %-14s  %s\n", maxRepo, name, maxRef, ref, status, drift)
	}

	fmt.Fprintf(out, "\n%d installed, %d drifted, %d not installed",
		result.Summary.Installed, result.Summary.Drifted, result.Summary.NotInstalled)
	if result.Summary.Errored > 0 {
		fmt.Fprintf(out, ", %d errored", result.Summary.Errored)
	}
	fmt.Fprintln(out)

	for _, s := range result.Repos {
		for _, d := range s.Drifts {
			if d.Detail == "" {
				continue
			}
			fmt.Fprintf(out, "\n%s %s:\n", s.Owner+"/"+s.Repo, d.Field)
			for _, line := range strings.Split(d.Detail, "\n") {
				fmt.Fprintf(out, "  %s\n", line)
			}
		}
	}

	for _, s := range result.Repos {
		if !showGitLabRoleStatus(s) {
			continue
		}
		fmt.Fprintf(out, "\n%s GitLab roles:\n", s.Owner+"/"+s.Repo)
		for _, d := range s.GitLabRoleDiagnostics {
			fmt.Fprintf(out, "  %s\n", d)
		}
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(out, "WARNING: %s\n", w)
	}
}

// reposInstallConfig holds flags and test overrides for repos install.
// Persistent, non-secret repository settings and repository membership
// live in repos.yaml; only credentials, local artifacts and one-run
// controls are command-line inputs (#8219).
type reposInstallConfig struct {
	// Core flags
	manifest            string
	dryRun              bool
	repoFilter          []string
	concurrency         int
	direct              bool
	force               bool
	reactivateSchedules bool
	gitlabToken         string

	// GCP credentials (install-time only). The region is not an input:
	// FULLSEND_GCP_REGION comes from the effective configuration (#8220).
	inferenceProject       string
	inferenceWIFProvider   string
	inferenceProjectNumber string // derived from the GCP project; not a CLI flag

	// openAIAPIKey is written as FULLSEND_OPENAI_API_KEY to selected
	// repos whose inference.auth is openai-api-key. Command-line only:
	// never persisted to the manifest and never logged.
	openAIAPIKey string

	// GitLab-specific
	gitlabRoleRegistry string
	gitlabRoleTokens   []string

	gitlabRoleRegistryJSON string
	gitlabRoleProvided     map[gitlabroles.Role]string
	rotateGitLabRoles      bool
	rotateGitLabRoleNames  []string
	rotateGitLabRoleFilter []gitlabroles.Role
	// rotateGitLabTriggerToken force-rotates the webhook fast-path
	// pipeline trigger token (FULLSEND_TRIGGER_TOKEN, ADR 0125).
	rotateGitLabTriggerToken bool
	// rotateGitLabTriggerAccount replaces the trigger-only service account's
	// deploy key, branch grant, and trigger token (#8437).
	rotateGitLabTriggerAccount bool

	// Local vendoring artifacts, used for repositories whose manifest
	// settings resolve vendor: true. They are never persisted.
	fullsendBinary string
	fullsendSource string

	// Test overrides
	testClient               forge.Client
	testFactory              repos.ForgeClientFactory
	testGitLabTokenInventory repos.ProjectAccessTokenClient
	testGitLabTriggerOwner   repos.GitLabTriggerOwner
	testProjectNumberFn      func(ctx context.Context, projectID string) (string, error)
	// testContainTriggerAccount replaces the live GitLab trigger-account
	// containment step, which only runs against the live client.
	testContainTriggerAccount func(ctx context.Context, client forge.Client, owner, repo string) error
	// testConvergeTriggerAccount replaces the live GitLab trigger-account
	// convergence step, which only runs against the live client.
	testConvergeTriggerAccount func(ctx context.Context, client forge.Client, owner, repo string, rotate bool) error
	// testRepairTriggerSchedules replaces the live repair of drifted
	// trigger-account-owned polling schedules, which only runs against the
	// live client.
	testRepairTriggerSchedules func(ctx context.Context, client forge.Client, owner, repo string) error
}

func newReposInstallCmd() *cobra.Command {
	opts := &reposInstallConfig{}

	cmd := &cobra.Command{
		Use:   "install [repos...]",
		Short: "Converge repos to the desired state defined in a manifest",
		Long: `Idempotent convergence operator for repos.yaml manifest entries.

repos.yaml is the source of every persistent, non-secret repository setting
and of repository membership. Declare repositories and settings there, by
editing the file or with 'fullsend repos set', before running install; the
command-line flags supply only credentials, local vendoring artifacts and
one-run controls, and none of their values are written to repos.yaml.

For repos whose shim workflow is not yet on the default branch, scaffolds
workflow files and writes variables/secrets onto the initialization branch,
including re-runs while an initialization PR/MR is still open. For repos
whose workflow is already on the default branch, reconciles variable drift,
disabled GitLab pipeline schedules (reported as drift; reactivated only when
--reactivate-schedules is passed), declared configuration-preset drift
against .fullsend/config.base.yaml, managed .fullsend/config.yaml drift,
and upgrades scaffold refs to match the manifest.

Positional arguments select repositories already declared in repos.yaml:
exact entry names, repositories covered by a glob entry (e.g. "acme/api"
under "acme/*"), or glob patterns (e.g. "acme/*"). They never add or copy
manifest entries. Every selector must match a declared repository; one that
matches none fails the whole command before anything is written, even when
another selector matches (for example "other/*" next to a declared "acme/*",
alongside "acme/api"). Declare a missing repository first. Dry run and
status select the same way. For example:
  fullsend repos set 'github.repos["acme/api"]' '{}'
When no repos are specified, all manifest repos are converged. Credentials
are required only for the forges of the selected repos.

Every selected repo must resolve an inference authentication method
(inference.auth: vertex-wif, openai-api-key or openai-wif) from its manifest
entry, its forge section, or defaults; there is no implicit default.

Infrastructure values come from each repository's effective configuration
(the managed config from repos.yaml layered over its config_base preset), so
install, --dry-run and 'repos status' agree with the committed .fullsend
files: FULLSEND_MINT_URL is the declared config mint_url, else the manifest
mint_url/mint_mode; FULLSEND_GCP_REGION is the declared inference.region,
else global. A manifest mint_url that contradicts a declared config mint_url
fails before any write.

Each repo's inference.auth selects the inference credentials provisioned on
it: vertex-wif writes FULLSEND_GCP_PROJECT_ID and FULLSEND_GCP_WIF_PROVIDER
from --vertex-project and --vertex-wif-provider, else from the
inference.project and inference.wif_provider the effective configuration
declares (a flag that contradicts a declared value fails before any write;
without a WIF provider one is derived from the project number);
openai-api-key writes FULLSEND_OPENAI_API_KEY (from --openai-api-key, GitHub
and GitLab). Existing secrets are reused when no values are supplied;
supplied values replace them. A repo missing its credentials with no values
supplied fails before any write. After switching methods, the other
method's Fullsend-managed secrets are removed once the new ones are written.

openai-wif (OpenAI Workload Identity Federation, GitHub only) needs no
secret. The repo must have a complete set of OpenAI WIF identifiers: the
FULLSEND_OPENAI_AUDIENCE, FULLSEND_OPENAI_IDENTITY_PROVIDER_ID and
FULLSEND_OPENAI_SERVICE_ACCOUNT_ID repository variables (which win when any
is set), else inference.openai from the managed config, existing
.fullsend/config.yaml, or config base/preset. A missing or partial set fails
before any write. The GCP secrets stay optional (written when a GCP project
is supplied) for Vertex sub-agents; FULLSEND_OPENAI_API_KEY is removed only
once the identifiers are live on the default branch. openai-wif on a GitLab
repo is rejected before any write.

The persistent-setting flags --roles, --fullsend-ref, --mint-url, --app-set,
--inference-auth, --vendor, --signoff, --gitlab-url, --vertex-region and
--forge were removed; using one fails with the repos.yaml key to set instead.

GCP infrastructure (WIF, mint) must be provisioned separately via
'inference provision' and 'mint enroll' before running this command.`,
		Example: `  # Declare a repository and its settings, then install it
  fullsend repos set defaults.inference.auth vertex-wif
  fullsend repos set defaults.config.inference.region us-east5
  fullsend repos set 'github.repos["acme/api"]' '{}'
  fullsend repos install acme/api --vertex-project my-gcp-project --dry-run
  fullsend repos install acme/api --vertex-project my-gcp-project

  # Converge every declared repository
  fullsend repos install`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.repoFilter = args
			opts.gitlabToken = getGitLabToken(cmd)
			return runReposInstall(cmd.Context(), opts)
		},
	}
	cmd.SetFlagErrorFunc(removedReposInstallFlagError)

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would change without applying")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push scaffold directly to default branch (skip PR)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "allow scaffold ref downgrades")
	cmd.Flags().BoolVar(&opts.reactivateSchedules, "reactivate-schedules", false, "reactivate required GitLab pipeline schedules that exist but are disabled (leave disabled by default so off-system polling setups are not silently reverted)")
	cmd.Flags().StringVar(&opts.inferenceProject, "vertex-project", "", "GCP project ID written as FULLSEND_GCP_PROJECT_ID for Vertex AI inference; must match config.inference.project when repos.yaml or the preset declares one; never written to repos.yaml")
	cmd.Flags().StringVar(&opts.inferenceWIFProvider, "vertex-wif-provider", "", "full WIF provider resource name (projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}) written as FULLSEND_GCP_WIF_PROVIDER instead of deriving per-repo providers; must match config.inference.wif_provider when declared; never written to repos.yaml")
	cmd.Flags().StringVar(&opts.openAIAPIKey, "openai-api-key", "", "OpenAI API key written as FULLSEND_OPENAI_API_KEY to selected repos whose inference.auth is openai-api-key; command-line only, never written to repos.yaml and never logged")
	cmd.Flags().StringVar(&opts.gitlabRoleRegistry, "gitlab-role-registry", "", "path to administrator GitLab role registry JSON (custom roles; never secret values)")
	cmd.Flags().StringArrayVar(&opts.gitlabRoleTokens, "gitlab-role-token", nil, "administrator-provided GitLab role PAT (repeatable, role=token); values are never logged")
	cmd.Flags().BoolVar(&opts.rotateGitLabRoles, "rotate-gitlab-roles", false, "force-rotate GitLab role credentials even if they are not near expiry")
	cmd.Flags().StringArrayVar(&opts.rotateGitLabRoleNames, "rotate-gitlab-role", nil, "rotate a specific GitLab role (repeatable); default is all own-credential roles that are due")
	cmd.Flags().BoolVar(&opts.rotateGitLabTriggerToken, "rotate-gitlab-trigger-token", false, "force-rotate the GitLab webhook fast-path pipeline trigger token (FULLSEND_TRIGGER_TOKEN); the previous token is revoked after the webhook is updated. A webhook on the trigger-account route uses FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN instead; rotate it with --rotate-gitlab-trigger-account")
	cmd.Flags().BoolVar(&opts.rotateGitLabTriggerAccount, "rotate-gitlab-trigger-account", false, "replace the GitLab trigger-only service account's deploy key, protected-branch push entry, and trigger token (FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN); requires a ready trigger account, otherwise install finishes it instead")
	cmd.Flags().StringVar(&opts.fullsendBinary, "fullsend-binary", "", "path to a Linux fullsend binary to upload for repos whose repos.yaml settings resolve vendor: true (default: auto-resolve)")
	cmd.Flags().StringVar(&opts.fullsendSource, "fullsend-source", "", "fullsend source checkout for vendored content and cross-compile, for repos whose repos.yaml settings resolve vendor: true (default: auto-detect or GitHub fetch)")

	return cmd
}

// removedReposInstallFlags maps each persistent-setting flag removed from
// repos install (#8219) to the repos.yaml setting that replaces it and an
// example 'fullsend repos set' command.
var removedReposInstallFlags = map[string]struct{ key, example string }{
	"roles":                  {"config.roles (under defaults, a forge section, or a repository entry)", `fullsend repos set defaults.config.roles '[triage, coder, review]'`},
	"fullsend-ref":           {"fullsend_ref (in the github or gitlab section, or on a repository entry)", `fullsend repos set github.fullsend_ref <ref>`},
	"mint-url":               {"mint_url (in the github section or on a GitHub repository entry) or config.mint_url", `fullsend repos set github.mint_url https://mint.example.com`},
	"app-set":                {"app_set (in the github section or on a GitHub repository entry)", `fullsend repos set github.app_set <app-set>`},
	"inference-auth":         {"inference.auth (under defaults, a forge section, or a repository entry)", `fullsend repos set defaults.inference.auth vertex-wif`},
	"vendor":                 {"vendor (under defaults or on a repository entry)", `fullsend repos set defaults.vendor true`},
	"vendor-fullsend-binary": {"vendor (under defaults or on a repository entry)", `fullsend repos set defaults.vendor true`},
	"signoff":                {"signoff (under defaults, a forge section, or a repository entry)", `fullsend repos set defaults.signoff true`},
	"gitlab-url":             {"gitlab.url", `fullsend repos set gitlab.url https://gitlab.example.com`},
	"vertex-region":          {"config.inference.region (under defaults, a forge section, or a repository entry; or inference.region in the config_base preset)", `fullsend repos set defaults.config.inference.region us-east5`},
	"forge":                  {"a repository entry in the github or gitlab section (repository membership is declared in repos.yaml)", `fullsend repos set 'github.repos["owner/repo"]' '{}'`},
}

// removedReposInstallFlagError turns cobra's "unknown flag" error for a
// removed persistent-setting flag into an actionable message naming the
// repos.yaml setting to declare instead. Other flag errors pass through.
func removedReposInstallFlagError(_ *cobra.Command, err error) error {
	name, ok := strings.CutPrefix(err.Error(), "unknown flag: --")
	if !ok {
		return err
	}
	removed, ok := removedReposInstallFlags[name]
	if !ok {
		return err
	}
	return fmt.Errorf("--%s was removed from repos install: repos.yaml is the source of persistent repository settings; declare %s instead, for example:\n  %s\nthen re-run repos install", name, removed.key, removed.example)
}

func runReposInstall(ctx context.Context, opts *reposInstallConfig) error {
	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}
	if opts.inferenceProject != "" && !repos.IsValidGCPProjectID(opts.inferenceProject) {
		return fmt.Errorf("--vertex-project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter, no trailing hyphen)", opts.inferenceProject)
	}
	if opts.inferenceWIFProvider != "" {
		if err := validateWIFProviderFlag("--vertex-wif-provider", opts.inferenceWIFProvider); err != nil {
			return err
		}
	}
	if err := prepareGitLabRoleFlags(opts); err != nil {
		return err
	}

	printer := ui.New(os.Stdout)

	// When no WIF provider is supplied, converge derives the project
	// number of each repository's effective GCP project (--vertex-project
	// or the declared inference.project) via the GCP Resource Manager API
	// so that per-repo WIF provider paths can be constructed. The lookup
	// is lazy: it runs only when a selected Vertex-using repo needs a
	// derived provider, so OpenAI-only runs never call GCP.
	var resolveProjectNumber func(ctx context.Context, projectID string) (string, error)
	if opts.inferenceWIFProvider == "" {
		resolveProjectNumber = opts.testProjectNumberFn
		if resolveProjectNumber == nil {
			resolveProjectNumber = func(ctx context.Context, projectID string) (string, error) {
				return gcf.NewLiveGCFClient(projectID).GetProjectNumber(ctx, projectID)
			}
		}
	}

	printer.StepStart("Loading manifest")
	manifest, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("loading manifest: %w; repos install does not create a manifest: declare repositories first, for example:\n  fullsend repos set -f %s 'github.repos[\"owner/repo\"]' '{}'", err, opts.manifest)
		}
		return fmt.Errorf("loading manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", manifest.TotalRepoCount()))

	// Positional repositories only select entries already declared in
	// repos.yaml (#8219): an undeclared selection fails here, before any
	// manifest or forge write.
	if err := requireDeclaredSelections(manifest, opts.repoFilter); err != nil {
		return err
	}
	if opts.fullsendBinary != "" || opts.fullsendSource != "" {
		vendored, vendorErr := manifest.VendorSelected(opts.repoFilter)
		if vendorErr != nil {
			return fmt.Errorf("resolving vendor settings: %w", vendorErr)
		}
		if !vendored {
			flag := "--fullsend-binary"
			if opts.fullsendBinary == "" {
				flag = "--fullsend-source"
			}
			return fmt.Errorf("%s is only used for repositories whose repos.yaml settings resolve vendor: true, and no selected repository does; enable vendoring in repos.yaml, for example:\n  fullsend repos set defaults.vendor true", flag)
		}
	}

	var clients repos.ForgeClientFactory
	switch {
	case opts.testFactory != nil:
		clients = opts.testFactory
	case opts.testClient != nil:
		clients = newSingleClientFactory(opts.testClient)
	default:
		clients = newForgeClientFactory(opts.gitlabToken, manifest)
	}

	// Every selector must also match a declared repository once glob
	// entries resolve: a glob selector no declared repository matches
	// (other/* next to a declared acme/*) fails here even when another
	// selector matches. This only reads from the forge, so dry run and live
	// install fail identically before any manifest or forge write.
	//
	// The error is held, not returned, until the containment-only recovery
	// below has run for the repositories that did resolve: an unrelated glob
	// that cannot be listed must not leave an explicit repository's interrupted
	// bootstrap uncontained. Normal installation stages stay blocked.
	selectionMatchErr := repos.RequireDeclaredSelections(ctx, manifest, clients, opts.repoFilter)

	// Containment-only recovery of an interrupted GitLab trigger-account
	// bootstrap (#8437). It runs for every targeted GitLab repository as soon as
	// the manifest and forge clients exist, before the configuration preflights,
	// convergence, the Poller inventory and every later stage, none of which
	// can then fail or skip it. It creates nothing and leaves the webhook fast
	// path alone. A repo whose containment failed is reported as failed and
	// skips the later stages.
	containFailed := make(map[string]error)
	var containFailedRepos []repos.ConvergeResult
	// A selection error (for example a glob that cannot be expanded) does not
	// stop containment of the repos that did resolve: SelectedGitLabRepos
	// returns every repo it could resolve alongside the error.
	gitlabRepos, selErr := repos.SelectedGitLabRepos(ctx, manifest, clients, opts.repoFilter)
	if selErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not select every GitLab repo for trigger account recovery: %v", selErr))
	}
	for _, gr := range gitlabRepos {
		key := gr.Owner + "/" + gr.Repo
		fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
		if fcErr != nil {
			printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client for trigger account recovery: %v", key, fcErr))
			continue
		}
		contain := func() error {
			if opts.testContainTriggerAccount != nil {
				return opts.testContainTriggerAccount(ctx, fc.Client, gr.Owner, gr.Repo)
			}
			return containGitLabTriggerAccount(ctx, opts, fc.Client, printer, gr.Owner, gr.Repo)
		}
		if _, dup := containFailed[key]; dup {
			continue
		}
		if cErr := contain(); cErr != nil {
			printer.StepWarn(fmt.Sprintf("[%s] GitLab trigger account recovery failed: %v", key, cErr))
			containFailed[key] = cErr
			containFailedRepos = append(containFailedRepos, repos.ConvergeResult{
				Owner: gr.Owner, Repo: gr.Repo,
				Error: fmt.Errorf("GitLab trigger account recovery: %w", cErr),
			})
			continue
		}
	}
	if selectionMatchErr != nil {
		errs := []error{fmt.Errorf("matching repository selection: %w; repos install only selects repositories the manifest declares; nothing was changed. Declare a missing repository first, for example:\n  fullsend repos set 'github.repos[\"owner/repo\"]' '{}'", selectionMatchErr)}
		for _, r := range containFailedRepos {
			errs = append(errs, r.Error)
		}
		return errors.Join(errs...)
	}

	// The configuration preflights below only read the forge, and the
	// manifest is written only after all of them, so they share one read
	// cache: equivalent layer, component and glob reads are made once
	// (#8501). It is discarded before the manifest write; convergence
	// reads current state through clients.
	preflightClients := repos.NewPreflightReadCache(clients)

	// Preflight the existing configuration layers of manifest-tracked repos
	// before any manifest or forge write: a markerless overlay needs
	// adoption and an undeclared base needs resolution (#8218). A repo that
	// only needs adoption is skipped and reported as failed, so the other
	// repos still converge (#8511); every other problem stops the run.
	adoptionRequired, err := repos.PreflightManagedConfig(ctx, manifest, preflightClients, opts.repoFilter)
	if err != nil {
		return fmt.Errorf("configuration preflight: %w", err)
	}
	adoptionFailedRepos := make([]repos.ConvergeResult, 0, len(adoptionRequired))
	adoptionSkip := make([]string, 0, len(adoptionRequired))
	for _, a := range adoptionRequired {
		printer.StepFail(fmt.Sprintf("[%s] skipped: %s", a.Name(), a.Guidance))
		adoptionSkip = append(adoptionSkip, a.Name())
		if _, failed := containFailed[a.Name()]; failed {
			// Already excluded and counted as failed by trigger-account
			// recovery.
			continue
		}
		adoptionFailedRepos = append(adoptionFailedRepos, repos.ConvergeResult{
			Owner: a.Owner, Repo: a.Repo,
			Error: fmt.Errorf("adoption required: %s exists without the managed-configuration ownership marker (ADR-0122); see the guidance above; nothing was written", preset.OverlayPath),
		})
	}

	// The only manifest edit install still plans is the initial
	// create_issues writeback below. It is planned in memory (DryRun) with
	// its progress output held in deferredOutput; the layered safety
	// comparison then runs against the proposed manifest and presets, and
	// only then is the manifest persisted, so a safety rejection leaves
	// repos.yaml byte-identical and writes nothing to the forge (#8218).
	var deferredOutput []func()
	deferOutput := func(f func()) { deferredOutput = append(deferredOutput, f) }
	manifestChanged := false

	// Persist the install-time choice the generated managed overlay must
	// reproduce (the upstream create_issues default for a no-preset first
	// install) in each repo's manifest entry before the overlay is rendered
	// (#8218). A manifest that cannot be written fails here, before any
	// forge write, with the exact edit.
	writtenBack, wbErr := repos.EnsureManagedConfigDefaults(ctx, repos.ManagedConfigWritebackConfig{
		Manifest:     manifest,
		ManifestPath: opts.manifest,
		DryRun:       true,
		Deferred:     !opts.dryRun,
		RepoFilter:   opts.repoFilter,
		ExcludeRepos: adoptionSkip,
	}, preflightClients, func(repo, _, msg string) {
		deferOutput(func() { printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg)) })
	})
	if wbErr != nil {
		return fmt.Errorf("recording install defaults in manifest: %w", wbErr)
	}
	if len(writtenBack) > 0 {
		manifestChanged = true
	}

	// Compare the proposed layered configuration (manifest edits and declared
	// presets) with each repository's installed layers before anything is
	// persisted or written to the forge.
	if err := repos.PreflightProposedManagedSafety(ctx, manifest, preflightClients, opts.repoFilter, adoptionSkip); err != nil {
		return fmt.Errorf("configuration safety preflight: %w", err)
	}
	// Resolve the effective infrastructure and reconcile the Vertex inputs
	// of every selected repository before the manifest is persisted, so a
	// mint URL conflict or a flag that contradicts the declared
	// configuration leaves repos.yaml byte-identical.
	if err := repos.PreflightEffectiveInfrastructure(ctx, manifest, preflightClients, opts.repoFilter, adoptionSkip, opts.inferenceProject, opts.inferenceWIFProvider); err != nil {
		return fmt.Errorf("effective infrastructure preflight: %w", err)
	}
	if manifestChanged && !opts.dryRun && opts.manifest != "" {
		if err := repos.WriteManifest(opts.manifest, manifest); err != nil {
			return fmt.Errorf("writing manifest: %w", err)
		}
	}
	for _, flush := range deferredOutput {
		flush()
	}

	targetedForges, err := manifest.DistinctForgesFor(opts.repoFilter)
	if err != nil {
		return fmt.Errorf("determining targeted forges: %w", err)
	}
	if err := checkAllForgeScopes(ctx, clients, printer, targetedForges); err != nil {
		return err
	}

	// Signoff comes from the manifest settings. Trailers are resolved once
	// per forge before convergence (including dry runs) so a missing
	// identity fails before any scaffold commit is made.
	signOffTrailers, err := resolveRepoSignOffTrailers(ctx, clients, printer, manifest,
		opts.repoFilter, opts.dryRun)
	if err != nil {
		return err
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, installed bool) error {
		rc, ok := manifest.ResolveConfigWithGlobs(owner, repo)
		if !ok {
			return fmt.Errorf("repo %s/%s not found in manifest", owner, repo)
		}
		fc, fcErr := clients.ConfigFor(rc.Forge)
		if fcErr != nil {
			return fcErr
		}

		// When the manifest enables vendor for this repo, append vendored
		// binary and content files to the scaffold commit so they are
		// delivered atomically.
		if rc.Vendor {
			var vendorErr error
			var vendorCleanup func()
			files, _, vendorCleanup, vendorErr = appendVendorTreeFiles(ctx, fc.Client, printer, owner, repo, files, true, opts.fullsendBinary, opts.fullsendSource)
			if vendorCleanup != nil {
				defer vendorCleanup()
			}
			if vendorErr != nil {
				return fmt.Errorf("collecting vendored assets: %w", vendorErr)
			}
		}

		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.BuildScaffoldPRMetadata(ctx, fc.Client, owner, repo, upstreamTag,
			repos.ScaffoldMetadataOpts{GuardInstalled: &installed})
		if rc.Forge == repos.ForgeGitLab {
			meta.CommitMsg += " [skip ci]"
			meta.PRTitle += " [skip ci]"
		}
		// The trailer goes after [skip ci] so it stays the last paragraph
		// of the commit message, where git trailer parsing expects it.
		if rc.Signoff {
			trailer, ok := signOffTrailers[rc.Forge]
			if !ok {
				return fmt.Errorf("signoff is enabled for %s/%s but no %s user identity was resolved", owner, repo, rc.Forge)
			}
			meta.CommitMsg += "\n\n" + trailer
		}
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, nil)
		return commitErr
	}

	// Resolve the review app client ID for provenance validation.
	// Best-effort: a missing client ID does not block installation.
	// Skip the GitHub lookup when this run does not target any GitHub repo.
	//
	// reviewAppSet is the app set this ID was resolved for; it seeds the
	// converge cache so repos on that app set skip a second lookup, and
	// repos whose effective app set differs resolve their own.
	reviewAppSet := appsetup.DefaultAppSet
	if manifest.GitHub != nil && manifest.GitHub.AppSet != "" && manifest.GitHub.AppSet != repos.NoneSentinel {
		reviewAppSet = manifest.GitHub.AppSet
	}
	var reviewAppClientID string
	if forgeListIncludesGitHub(targetedForges) {
		if fc, fcErr := clients.ConfigFor(repos.ForgeGitHub); fcErr == nil {
			// Resolve the review app by its {app_set}-review slug. Prefer
			// the manifest's GitHub app_set so a custom app set resolves
			// its own review app rather than the built-in default.
			reviewAppClientID = resolveReviewAppClientID(ctx, fc.Client, reviewAppSet)
		}
	}

	readVendoredWorkflow, vendoredWorkflowCleanup := newVendoredWorkflowReader(opts.fullsendSource)
	defer vendoredWorkflowCleanup()

	convergeCfg := repos.ConvergeConfig{
		Manifest:                manifest,
		DryRun:                  opts.dryRun,
		RepoFilter:              opts.repoFilter,
		ExcludeRepos:            append(containFailedKeys(containFailedRepos), adoptionSkip...),
		MaxConcurrency:          opts.concurrency,
		UpstreamRef:             upstreamRef,
		UpstreamTag:             upstreamTag,
		Direct:                  opts.direct,
		Force:                   opts.force,
		ReactivateSchedules:     opts.reactivateSchedules,
		InferenceProject:        opts.inferenceProject,
		InferenceProjectNumber:  opts.inferenceProjectNumber,
		WIFProvider:             opts.inferenceWIFProvider,
		ResolveProjectNumber:    resolveProjectNumber,
		OpenAIAPIKey:            opts.openAIAPIKey,
		ReviewAppClientID:       reviewAppClientID,
		ReviewAppClientIDAppSet: reviewAppSet,
		ReadVendoredWorkflow:    readVendoredWorkflow,
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	// A managed schedule the trigger account owns that drifted to carry
	// pipeline variables goes back to the installer before convergence: the
	// pipeline-variable migration preflight convergence reaches rejects it and
	// cannot edit it as the installer (#8508). It runs only here, after the
	// repository selection and every configuration preflight succeeded, so an
	// install that fails those checks leaves live schedules untouched. A repo
	// whose containment failed is skipped. A failure here is only reported; a
	// schedule left carrying variables fails the convergence or override-role
	// check that rejects it.
	for _, gr := range gitlabRepos {
		key := gr.Owner + "/" + gr.Repo
		if _, failed := containFailed[key]; failed {
			continue
		}
		fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
		if fcErr != nil {
			printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client for trigger account schedule repair: %v", key, fcErr))
			continue
		}
		if rErr := repairGitLabTriggerSchedules(ctx, opts, fc.Client, printer, gr.Owner, gr.Repo); rErr != nil {
			printer.StepWarn(fmt.Sprintf("[%s] GitLab trigger account schedule repair failed: %v", key, rErr))
		}
	}

	printer.Blank()
	if opts.dryRun {
		printer.StepStart("Dry-run: previewing convergence")
	} else {
		printer.StepStart("Converging repos to desired state")
	}

	result, err := repos.Converge(ctx, convergeCfg, clients, scaffoldCommitFn, progressFn)
	if err != nil {
		return err
	}

	installed := result.Installed()
	converged := result.Converged()
	alreadyCurrent := result.AlreadyCurrent()
	failed := result.Failed()

	// Writeback: when platform-level fullsend_ref was empty and repos
	// were installed, write the binary's version back to repos.yaml
	// so future installs and status checks have a baseline.
	// Exception to ADR-0057 manual-maintenance: authorized by #6190.
	if !opts.dryRun && len(installed) > 0 {
		writebackRef := upstreamTag
		if writebackRef == "" {
			writebackRef = upstreamRef
		}
		if writebackRef != "" {
			ghEmpty := manifest.GitHub == nil || manifest.GitHub.FullsendRef == ""
			glEmpty := manifest.GitLab == nil || manifest.GitLab.FullsendRef == ""
			if ghEmpty || glEmpty {
				var hasGH, hasGL bool
				for _, r := range installed {
					rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
					if !ok {
						continue
					}
					if rc.Forge == repos.ForgeGitHub {
						hasGH = true
					} else if rc.Forge == repos.ForgeGitLab {
						hasGL = true
					}
				}
				if hasGH && ghEmpty {
					if _, wbErr := repos.SetManifestValue(opts.manifest, "github.fullsend_ref", writebackRef); wbErr == nil {
						manifest.EnsurePlatform(repos.ForgeGitHub).FullsendRef = writebackRef
						printer.StepDone(fmt.Sprintf("Wrote fullsend_ref=%s to manifest (GitHub)", writebackRef))
					} else {
						printer.StepWarn(fmt.Sprintf("failed to write fullsend_ref to manifest (GitHub): %v", wbErr))
					}
				}
				if hasGL && glEmpty {
					if _, wbErr := repos.SetManifestValue(opts.manifest, "gitlab.fullsend_ref", writebackRef); wbErr == nil {
						manifest.EnsurePlatform(repos.ForgeGitLab).FullsendRef = writebackRef
						printer.StepDone(fmt.Sprintf("Wrote fullsend_ref=%s to manifest (GitLab)", writebackRef))
					} else {
						printer.StepWarn(fmt.Sprintf("failed to write fullsend_ref to manifest (GitLab): %v", wbErr))
					}
				}
			}
		}
	}

	// GitLab post-install: set up pipeline schedules for repos whose
	// convergence result says they are missing. Entry into the loop body
	// is gated on NeedsGitLabPostInstall (true when schedules are missing).
	// Schedule setup itself is gated on NeedsGitLabPipelineSchedules so a
	// repo re-run while its initialization MR is still open (#7417) does
	// not delete and recreate live schedules. Shared-token bot-PAT setup
	// was removed: role credentials are the only GitLab runtime path.
	// failedRepoKeys tracks owner/repo pairs that have already failed in an
	// earlier stage (post-install setup, poll-state provisioning) so the
	// role-provisioning pass below can skip them instead of double-counting
	// them as both a stage failure and a role failure.
	failedRepoKeys := make(map[string]bool)

	var installedPostFail int
	var postInstallFailedRepos []repos.ConvergeResult
	// A repo whose trigger-account containment failed was excluded from
	// Converge above, so it is in none of the result lists. It is counted once
	// as failed here, takes no later stage (failedRepoKeys), and is
	// deliberately not fed to the webhook safety reconciliation, so the
	// existing fast path is preserved.
	for _, r := range containFailedRepos {
		failedRepoKeys[r.Owner+"/"+r.Repo] = true
	}
	// A repo skipped because its overlay needs adoption was excluded the
	// same way and is likewise counted once and takes no later stage.
	for _, r := range adoptionFailedRepos {
		failedRepoKeys[r.Owner+"/"+r.Repo] = true
	}
	postInstallRepos := make([]repos.ConvergeResult, 0, len(installed)+len(converged)+len(alreadyCurrent))
	postInstallRepos = append(postInstallRepos, installed...)
	postInstallRepos = append(postInstallRepos, converged...)
	postInstallRepos = append(postInstallRepos, alreadyCurrent...)
	if !opts.dryRun && len(postInstallRepos) > 0 {
		for _, r := range postInstallRepos {
			if !r.NeedsGitLabPostInstall {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			repoFullName := r.Owner + "/" + r.Repo
			if failedRepoKeys[repoFullName] {
				// Trigger-account containment failed for this repo: it is
				// already counted once and takes no further mutating stage.
				continue
			}
			printer.Blank()
			printer.StepStart(fmt.Sprintf("[%s] GitLab post-install setup", repoFullName))

			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client: %v", repoFullName, fcErr))
				r.Error = fcErr
				postInstallFailedRepos = append(postInstallFailedRepos, r)
				if r.Installed {
					installedPostFail++
				}
				failedRepoKeys[repoFullName] = true
				continue
			}
			glClient, ok := fc.Client.(*gl.LiveClient)
			if !ok {
				err := fmt.Errorf("GitLab client type assertion failed — post-install setup skipped")
				printer.StepWarn(fmt.Sprintf("[%s] %v", repoFullName, err))
				r.Error = err
				postInstallFailedRepos = append(postInstallFailedRepos, r)
				if r.Installed {
					installedPostFail++
				}
				failedRepoKeys[repoFullName] = true
				continue
			}

			if r.Installed && r.NeedsGitLabPipelineSchedules {
				targetRepo, repoErr := fc.Client.GetRepo(ctx, r.Owner, r.Repo)
				if repoErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s] Could not get repo info for schedule setup: %v", repoFullName, repoErr))
					r.Error = repoErr
					postInstallFailedRepos = append(postInstallFailedRepos, r)
					if r.Installed {
						installedPostFail++
					}
					failedRepoKeys[repoFullName] = true
					continue
				}

				schedErr := setupGitLabPipelineSchedules(ctx, fc.Client, printer, r.Owner, r.Repo, targetRepo.DefaultBranch, r.GitLabTypedDispatch)
				if schedErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s] Pipeline schedule setup failed: %v", repoFullName, schedErr))
					r.Error = schedErr
					postInstallFailedRepos = append(postInstallFailedRepos, r)
					if r.Installed {
						installedPostFail++
					}
					failedRepoKeys[repoFullName] = true
				}
			}

			healGitLabResourceGroups(ctx, glClient, printer, r.Owner, r.Repo)
		}
	}

	// GitLab poll-state provisioning for already-enrolled repos.
	// Fresh installs are handled above via repos.Install (Step 5b);
	// converged and already-current repos still need
	// FULLSEND_DISPATCH_SECRET and the two poll-state branches, with
	// any legacy CI/CD variables migrated into signed documents. This
	// runs with the operator's Maintainer-level client and never
	// touches the bot PAT, so it is safe on live repos.
	var pollStateFail int
	var pollStateFailedRepos []repos.ConvergeResult
	if !opts.dryRun {
		existing := make([]repos.ConvergeResult, 0, len(converged)+len(alreadyCurrent))
		existing = append(existing, converged...)
		existing = append(existing, alreadyCurrent...)
		for _, r := range existing {
			if failedRepoKeys[r.Owner+"/"+r.Repo] {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] Could not get GitLab client for poll-state provisioning: %v", r.Owner, r.Repo, fcErr))
				pollStateFail++
				r.Error = fcErr
				pollStateFailedRepos = append(pollStateFailedRepos, r)
				failedRepoKeys[r.Owner+"/"+r.Repo] = true
				continue
			}
			if err := provisionGitLabPollState(ctx, fc.Client, printer, r.Owner, r.Repo); err != nil {
				pollStateFail++
				r.Error = err
				pollStateFailedRepos = append(pollStateFailedRepos, r)
				failedRepoKeys[r.Owner+"/"+r.Repo] = true
			}
		}
	}

	webhookSafetyDone := make(map[string]bool)

	var roleFail int
	var roleFailedRepos []repos.ConvergeResult
	var roleFailInstalledCount int
	{
		type tagged struct {
			r     repos.ConvergeResult
			fresh bool
		}
		all := make([]tagged, 0, len(installed)+len(converged)+len(alreadyCurrent))
		for _, r := range installed {
			all = append(all, tagged{r: r, fresh: true})
		}
		for _, r := range converged {
			all = append(all, tagged{r: r, fresh: false})
		}
		for _, r := range alreadyCurrent {
			all = append(all, tagged{r: r, fresh: false})
		}
		for _, item := range all {
			// Skip repos that already failed at an earlier stage (post-install
			// setup, poll-state provisioning) so they are not double-counted
			// as both a stage failure and a role-provisioning failure.
			if item.r.Error != nil || failedRepoKeys[item.r.Owner+"/"+item.r.Repo] {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(item.r.Owner, item.r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] Could not get GitLab client for role provisioning: %v", item.r.Owner, item.r.Repo, fcErr))
				roleFail++
				item.r.Error = fcErr
				roleFailedRepos = append(roleFailedRepos, item.r)
				if item.fresh {
					roleFailInstalledCount++
				}
				continue
			}
			// A Poller left elevated by an interrupted rotation is restored,
			// or its credential contained, on every install, before any role
			// lifecycle operation and whether or not webhook work is pending.
			if err := reconcileGitLabPollerElevation(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab Poller membership reconciliation failed: %v", item.r.Owner, item.r.Repo, err))
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			// The trigger account does not use the Poller credential or its
			// protected-ref grant, so a Poller credential failure does not
			// stop its preparation (GitLab.com Free refuses the grant while a
			// replacement Poller is verified, which surfaces here as an opaque
			// provisioning failure). The failure stays the repo's recorded
			// error and the dispatch-route stages below remain skipped, so
			// it is counted once. The callback still refreshes an existing
			// webhook after the lifecycle: ordinary repair can replace the
			// account token even without a requested rotation.
			convergeTriggerAfterPollerFailure := func(pollerErr error) error {
				// Do not force rotation on a failed Poller stage, but do repair
				// the webhook if normal account convergence changed its token.
				tErr := maybeConvergeGitLabTriggerAccount(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo, false, true, manifest)
				if tErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab trigger account failed: %v", item.r.Owner, item.r.Repo, tErr))
				}
				wErr := refreshGitLabWebhookAfterPollerFailure(ctx, fc.Client, printer, item.r.Owner, item.r.Repo, opts.dryRun)
				if wErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab webhook route refresh failed: %v", item.r.Owner, item.r.Repo, wErr))
				}
				var containErr error
				if repos.GitLabWebhookTriggerOwnerUnsafe(wErr) {
					containErr = containGitLabTriggerAccountAfterWebhookFailure(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo)
				}
				var retireErr error
				if tErr == nil && wErr == nil {
					retireErr = finishGitLabTriggerAccountRetirement(ctx, opts, fc.Client, item.r.Owner, item.r.Repo)
				}
				return errors.Join(pollerErr, tErr, wErr, containErr, retireErr)
			}
			if err := maybeProvisionGitLabRoles(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab role provisioning failed: %v", item.r.Owner, item.r.Repo, err))
				err = convergeTriggerAfterPollerFailure(err)
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			if err := maybeRotateGitLabRoles(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab role rotation failed: %v", item.r.Owner, item.r.Repo, err))
				err = convergeTriggerAfterPollerFailure(err)
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			if item.r.Error != nil {
				continue
			}
			if err := ensureGitLabPollerPipelineAccess(ctx, fc.Client, gitLabTokenInventory(opts, fc.Client), printer, item.r.Owner, item.r.Repo, opts.dryRun); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab poller protected-ref pipeline access failed: %v", item.r.Owner, item.r.Repo, err))
				// GitLab.com Free refuses the legacy Poller per-user merge
				// grant. The trigger account does not use that grant, so its
				// resource preparation still runs. The Poller failure stays the
				// repo's recorded error. The full dispatch-route stages below
				// remain skipped; the callback only refreshes an existing webhook
				// route after account repair. The failure is counted once.
				if pollerMergeGrantRejected(err) {
					err = convergeTriggerAfterPollerFailure(err)
				}
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			if err := ensureGitLabPipelineVariableOverrideRole(ctx, fc.Client, printer, item.r.Owner, item.r.Repo, opts.dryRun); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab pipeline-variable override role failed: %v", item.r.Owner, item.r.Repo, err))
				// The restriction is weakened or could not be verified. A
				// published trigger-account credential was already removed and
				// revoked by the early containment pass, which checks the live
				// role itself; the safety reconciliation after this loop
				// (roleFailedRepos) revokes any live webhook trigger credential
				// and keeps this role failure as the repo's recorded error.
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			// Webhook fast-path (ADR 0125) and trigger-only service account
			// (#8437), ordered so the webhook follows the dispatch route the
			// account lifecycle settles (#8509).
			if err := convergeGitLabDispatchRoute(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo, item.r.NeedsGitLabWebhook, manifest); err != nil {
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
			}
		}
	}

	// Safety reconciliation for repos that failed before the webhook step
	// (convergence, post-install setup, poll-state provisioning, or any
	// role-stage step: provisioning, rotation, protected-ref access, or the
	// override-role check). A
	// weakened restriction can make convergence fail, which skips the
	// success-only loop above, so an existing managed trigger credential
	// would otherwise stay live. This only revokes; it never provisions.
	// Cleanup errors are reported alongside the original failure, which is
	// preserved.
	reconcileFailedGitLabWebhookSafety(ctx, opts, clients, manifest, printer, webhookSafetyDone,
		failed, postInstallFailedRepos, pollStateFailedRepos, roleFailedRepos)

	printer.Blank()
	installedCount := len(installed) - installedPostFail - roleFailInstalledCount
	failedCount := len(failed) + len(postInstallFailedRepos) + len(containFailedRepos) + len(adoptionFailedRepos) + pollStateFail + roleFail

	for _, r := range failed {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range pollStateFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range postInstallFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range containFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range adoptionFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range roleFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}

	printer.StepDone(fmt.Sprintf("Install complete: %d installed, %d converged, %d already current, %d failed",
		installedCount, len(converged), len(alreadyCurrent), failedCount))

	if failedCount > 0 {
		return fmt.Errorf("%d repos failed", failedCount)
	}
	return nil
}

// reconcileFailedGitLabWebhookSafety runs the revocation-only webhook safety
// reconciliation for every GitLab repo in groups, once per repo (done tracks
// repos already reconciled). Each group holds failed results — convergence,
// post-install, poll-state, and role-stage failures — whose entries are
// updated in place: a cleanup error is joined onto the original failure,
// which is preserved.
func reconcileFailedGitLabWebhookSafety(ctx context.Context, opts *reposInstallConfig, clients repos.ForgeClientFactory, manifest *repos.Manifest, printer *ui.Printer, done map[string]bool, groups ...[]repos.ConvergeResult) {
	for _, group := range groups {
		for i := range group {
			r := &group[i]
			key := r.Owner + "/" + r.Repo
			if done[key] {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			done[key] = true
			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client for webhook safety reconciliation: %v", key, fcErr))
				continue
			}
			if err := reconcileGitLabWebhookSafety(ctx, opts, fc.Client, printer, r.Owner, r.Repo); err != nil {
				r.Error = errors.Join(r.Error, fmt.Errorf("webhook safety reconciliation: %w", err))
			}
		}
	}
}

type reposUninstallConfig struct {
	manifest      string
	dryRun        bool
	yes           bool
	direct        bool
	concurrency   int
	manifestOnly  bool
	uninstallOnly bool
	gitlabToken   string

	testClient       forge.Client
	testFactory      repos.ForgeClientFactory
	testGitLabTokens repos.ProjectAccessTokenClient
}

func newReposUninstallCmd() *cobra.Command {
	opts := &reposUninstallConfig{}

	cmd := &cobra.Command{
		Use:   "uninstall <repos...>",
		Short: "Tear down fullsend from repos and remove from manifest",
		Long: `Tear down fullsend from the specified repos and remove them from the manifest.

By default, tears down (removes workflow files via a pull request, deletes
variables and secrets immediately) and then removes the repo entry from
repos.yaml. The manifest entry is only removed if teardown succeeds.

File deletions (workflow YAML, .fullsend/config.yaml, and GitLab
.gitlab-ci.yml unmerge) are delivered as a PR unless --direct is set.
Variable and secret deletions are API operations and always happen
immediately. Use --direct to push file deletions to the default branch.

Use --manifest-only to remove from the manifest without tearing down (e.g.
when a repo is already deleted). Use --uninstall-only to tear down without
modifying the manifest (e.g. for temporary teardown with intent to reinstall).

GCP infrastructure (WIF) must be cleaned up separately via
'inference deprovision'.

Glob patterns (e.g. "acme/*") are matched against manifest entries and
prompt for confirmation unless --yes is set.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.gitlabToken = getGitLabToken(cmd)
			return runReposUninstall(cmd.Context(), opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be uninstalled without making changes")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip confirmation prompt when multiple repos are targeted")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push file deletions to default branch instead of PR")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().BoolVar(&opts.manifestOnly, "manifest-only", false, "remove from manifest without tearing down")
	cmd.Flags().BoolVar(&opts.uninstallOnly, "uninstall-only", false, "tear down without removing from manifest")
	cmd.MarkFlagsMutuallyExclusive("manifest-only", "uninstall-only")

	return cmd
}

func runReposUninstall(ctx context.Context, opts *reposUninstallConfig, repoArgs []string) error {
	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}

	printer := ui.New(os.Stdout)

	printer.StepStart("Loading manifest")
	manifest, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	// Uninstall removes every Fullsend-managed inference credential, so an
	// invalid inference.auth selection must not block cleanup.
	if err := manifest.ValidateForUninstall(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", manifest.TotalRepoCount()))

	matched, matchErr := repos.MatchManifestRepos(manifest, repoArgs)
	if matchErr != nil {
		return matchErr
	}
	if len(matched) == 0 {
		printer.StepInfo("No manifest entries matched the given patterns")
		return nil
	}

	var concreteRepos []string
	for _, r := range matched {
		if strings.ContainsAny(r, "*?[") {
			printer.StepInfo(fmt.Sprintf("[%s] Skipping glob manifest entry (use concrete repo names to uninstall)", r))
			continue
		}
		concreteRepos = append(concreteRepos, r)
	}
	if len(concreteRepos) == 0 {
		printer.StepInfo("All matched entries are glob patterns — no concrete repos to uninstall")
		return nil
	}

	action := "uninstall and remove from manifest"
	if opts.manifestOnly {
		action = "remove from manifest"
	} else if opts.uninstallOnly {
		action = "uninstall"
	}
	if !opts.yes && !opts.dryRun {
		if err := confirmBulkAction(printer, action, repoArgs, manifest, os.Stdin); err != nil {
			return err
		}
	}

	var clients repos.ForgeClientFactory
	switch {
	case opts.testFactory != nil:
		clients = opts.testFactory
	case opts.testClient != nil:
		clients = newSingleClientFactory(opts.testClient)
	default:
		clients = newForgeClientFactory(opts.gitlabToken, manifest)
	}

	progressFn := func(repo, phase, msg string) {
		switch {
		case phase == "done" || phase == "manifest":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		case strings.HasPrefix(msg, "Warning:"):
			printer.StepWarn(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, _ bool) error {
		forgeName := ""
		if rc, ok := manifest.ResolveConfigWithGlobs(owner, repo); ok {
			forgeName = rc.Forge
		}
		fc, fcErr := clients.ConfigFor(forgeName)
		if fcErr != nil {
			return fcErr
		}
		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.UninstallPRMetadata()
		if forgeName == repos.ForgeGitLab {
			meta.CommitMsg += " [skip ci]"
			meta.PRTitle += " [skip ci]"
		}
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, nil)
		return commitErr
	}

	// Teardown phase (skipped when --manifest-only).
	var succeededRepos []string
	var teardownFailed int
	if !opts.manifestOnly {
		targetedForges, err := manifest.DistinctForgesFor(concreteRepos)
		if err != nil {
			return fmt.Errorf("determining targeted forges: %w", err)
		}
		if err := checkAllForgeScopes(ctx, clients, printer, targetedForges); err != nil {
			return err
		}

		teardownCfg := repos.UninstallConfig{
			Manifest:       manifest,
			Repos:          concreteRepos,
			DryRun:         opts.dryRun,
			Direct:         opts.direct,
			MaxConcurrency: opts.concurrency,
			GitLabTokens:   gitLabUninstallTokens(opts, clients, printer, manifest, concreteRepos),
		}

		printer.Blank()
		if opts.dryRun {
			printer.StepStart("Dry-run: previewing uninstall")
		} else {
			printer.StepStart("Uninstalling fullsend from repos")
		}

		results, teardownErr := repos.Uninstall(ctx, teardownCfg, clients, scaffoldCommitFn, progressFn)
		if teardownErr != nil {
			return teardownErr
		}

		for _, r := range results {
			if r.Success {
				succeededRepos = append(succeededRepos, r.Owner+"/"+r.Repo)
			} else {
				teardownFailed++
				printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
			}
		}

		// GitLab post-uninstall: clean up pipeline schedules. Role and
		// shared-bot project access tokens are revoked inside Uninstall
		// when GitLabTokens is set, and a revocation failure fails the
		// uninstall so the manifest entry remains for retry.
		if !opts.dryRun {
			for _, r := range results {
				if !r.Success {
					continue
				}
				rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
				if !ok || rc.Forge != repos.ForgeGitLab {
					continue
				}
				repoFullName := r.Owner + "/" + r.Repo
				printer.Blank()
				printer.StepStart(fmt.Sprintf("[%s] GitLab cleanup", repoFullName))

				fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
				if fcErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client: %v", repoFullName, fcErr))
					continue
				}
				_ = cleanupGitLabPipelineSchedules(ctx, fc.Client, printer, r.Owner, r.Repo)
			}
		}
	} else {
		succeededRepos = concreteRepos
	}

	// Manifest removal phase (skipped when --uninstall-only).
	if !opts.uninstallOnly && len(succeededRepos) > 0 {
		removeResult, _, removeErr := repos.RemoveFromManifest(repos.ManifestEditConfig{
			Manifest:     manifest,
			ManifestPath: opts.manifest,
			DryRun:       opts.dryRun,
		}, succeededRepos, progressFn)
		if removeErr != nil {
			return removeErr
		}

		printer.Blank()
		printer.StepDone(fmt.Sprintf("Removed %d entries from manifest", len(removeResult.Removed)))
	}

	if opts.manifestOnly {
		return nil
	}

	printer.Blank()
	uninstalled := len(succeededRepos)
	printer.StepDone(fmt.Sprintf("Uninstall complete: %d uninstalled, %d failed", uninstalled, teardownFailed))

	if teardownFailed > 0 {
		return fmt.Errorf("%d repos failed to uninstall", teardownFailed)
	}
	return nil
}

// confirmBulkAction prompts for confirmation when a destructive action targets
// multiple repos — either through glob expansion or an explicit bulk list.
func confirmBulkAction(printer *ui.Printer, action string, patterns []string, manifest *repos.Manifest, stdin *os.File) error {
	matched, err := repos.MatchManifestRepos(manifest, patterns)
	if err != nil {
		return err
	}
	if len(matched) <= 1 {
		return nil
	}

	if !term.IsTerminal(int(stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal; use --yes to skip confirmation")
	}

	printer.StepWarn(fmt.Sprintf("This will %s %d repos:", action, len(matched)))
	for _, r := range matched {
		printer.StepInfo("  " + r)
	}
	printer.StepInfo("Continue? [y/N]")

	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		return fmt.Errorf("aborted")
	}
	return nil
}

func forgeListIncludesGitHub(forges []string) bool {
	for _, forgeName := range forges {
		if forgeName == "" || forgeName == repos.ForgeGitHub {
			return true
		}
	}
	return false
}

// checkAllForgeScopes validates GitHub token permissions for the given
// forges. Only GitHub forges are checked because scope introspection is
// not supported by other forge providers. Callers must pass the forges
// actually targeted by the operation, not every forge in the manifest.
func checkAllForgeScopes(ctx context.Context, clients repos.ForgeClientFactory, printer *ui.Printer, forges []string) error {
	for _, forgeName := range forges {
		if forgeName != "" && forgeName != repos.ForgeGitHub {
			continue
		}
		fc, err := clients.ConfigFor(forgeName)
		if err != nil {
			return err
		}
		if err := checkPerRepoScopes(ctx, fc.Client, printer); err != nil {
			return err
		}
	}
	return nil
}

// resolveRepoSignOffTrailers returns the Signed-off-by trailer to use for
// scaffold commits on each forge that needs one, keyed by forge name. A
// forge needs a trailer when any selected manifest entry resolves signoff
// to true (entry, then forge section, then defaults). The authenticated
// user's identity is resolved once per forge and a missing or incomplete
// identity is an error. In a dry run the trailer that would be added is
// reported for each forge.
func resolveRepoSignOffTrailers(ctx context.Context, clients repos.ForgeClientFactory, printer *ui.Printer,
	manifest *repos.Manifest, filter []string, dryRun bool) (map[string]string, error) {
	forges, err := manifest.SignoffForgesFor(filter)
	if err != nil {
		return nil, fmt.Errorf("determining signoff settings: %w", err)
	}

	trailers := make(map[string]string, len(forges))
	for _, forgeName := range forges {
		fc, err := clients.ConfigFor(forgeName)
		if err != nil {
			return nil, err
		}
		label := "GitHub"
		if forgeName == repos.ForgeGitLab {
			label = "GitLab"
		}
		trailer, err := resolveSignOffTrailer(ctx, fc.Client, "signoff", label)
		if err != nil {
			return nil, err
		}
		trailers[forgeName] = trailer
		if dryRun {
			printer.StepDone(fmt.Sprintf("Would add trailer to %s scaffold commits: %s", label, trailer))
		}
	}
	return trailers, nil
}

// requireDeclaredSelections rejects positional repository selections that
// match no repos.yaml entry. Repository membership is declared in the
// manifest (#8219): install never adds or copies entries, so an undeclared
// selection fails before any manifest or forge write with the command that
// declares it.
func requireDeclaredSelections(manifest *repos.Manifest, filter []string) error {
	undeclared, err := manifest.UndeclaredSelectors(filter)
	if err != nil {
		return fmt.Errorf("matching repository selection: %w", err)
	}
	if len(undeclared) == 0 {
		return nil
	}
	example := undeclared[0]
	if strings.ContainsAny(example, "*?[") || !strings.Contains(example, "/") {
		example = "owner/repo"
	}
	section := repos.ForgeGitHub
	if manifest.GitLab != nil && manifest.GitHub == nil {
		section = repos.ForgeGitLab
	}
	return fmt.Errorf("not declared in repos.yaml: %s; repos install only selects repositories the manifest declares (an entry or a covering glob entry) and never adds them; nothing was changed. Declare each repository first, for example:\n  fullsend repos set '%s.repos[\"%s\"]' '{}'",
		strings.Join(undeclared, ", "), section, example)
}
