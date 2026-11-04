package repos

import (
	"context"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// PreflightEffectiveInfrastructure resolves the effective infrastructure of
// every manifest repository selected by filter and reconciles the Vertex
// command-line inputs with the declared values, exactly as convergence does
// per repository. `repos install` calls it before persisting planned
// manifest edits, so a mint URL conflict, an invalid declared value or a
// flag that contradicts the declared configuration fails before any write
// and leaves repos.yaml untouched. Every offending repository is reported.
// A repository whose preset cannot be loaded is skipped: convergence
// reports that failure for the repository alone. Repositories named in
// exclude (owner/repo) are not checked: install skips them, as it does
// those that need adoption (#8511).
func PreflightEffectiveInfrastructure(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter, exclude []string, inferenceProject, wifProvider string) error {
	selected, err := selectManifestRepos(ctx, m, clients, filter)
	if err != nil {
		return err
	}
	selected = withoutRepos(selected, exclude)
	var errs []error
	store := newPresetCache()
	cfg := ConvergeConfig{InferenceProject: inferenceProject, WIFProvider: wifProvider}
	for _, rr := range selected {
		resolved := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		// Convergence reports a repository with no inference
		// authentication before anything else; nothing to resolve here.
		if resolved.RequireInferenceAuth() != nil {
			continue
		}
		if resolved.Config != "" {
			data, loadErr := store.Load(ctx, resolved.Config, resolved.ConfigHash)
			if loadErr != nil {
				// A preset that cannot be loaded is a failure of this
				// repository only: convergence loads it again before any
				// forge write and reports the error in this repository's
				// result, so one preset cannot stop the fleet (#8516).
				continue
			}
			resolved.ProposedBase = data
		}
		if infraErr := resolved.applyEffectiveInfrastructure(); infraErr != nil {
			errs = append(errs, infraErr)
			continue
		}
		if _, _, inputErr := vertexCredentialInputs(cfg, resolved); inputErr != nil {
			errs = append(errs, inputErr)
		}
	}
	return errors.Join(errs...)
}

// applyEffectiveInfrastructure resolves the repository's installation
// infrastructure values from its effective configuration — the managed
// configuration (Managed) layered over the declared preset (ProposedBase) —
// so install, dry run and status derive FULLSEND_MINT_URL,
// FULLSEND_GCP_REGION and the Vertex credential inputs from the same source
// as the committed .fullsend files (#8220). Callers load the declared preset
// into ProposedBase first; a repository without a preset passes none.
//
//   - MintURL (GitHub): a mint_url declared by the effective configuration
//     replaces the manifest mint_url/mint_mode resolution. A manifest
//     mint_url that contradicts it is a configuration error: the repository
//     variable and the committed configuration would disagree.
//   - InferenceRegion (Vertex-using inference.auth only): the declared
//     inference.region, else the code default. Only a declared region is
//     enforced on an existing variable (see expectedInferenceRegion).
//   - DeclaredInferenceProject / DeclaredWIFProvider: the declared Vertex
//     credential values, consumed by vertexCredentialInputs.
//   - DeclaredWIFAudience / DeclaredServiceAccount: the GitLab vertex-wif
//     options for an existing WIF provider, cleared where they do not apply
//     (see applyExistingWIFOptions).
func (c *ResolvedConfig) applyEffectiveInfrastructure() error {
	declared, err := config.DeclaredInfrastructureValues(c.Managed, c.ProposedBase)
	if err != nil {
		return fmt.Errorf("%s/%s: reading the effective configuration: %w", c.Owner, c.Repo, err)
	}
	if c.Forge == ForgeGitHub && declared.MintURL != "" {
		// A mint_url from a config_base preset has not passed the managed
		// layer's validation, so apply the same HTTPS/userinfo gate before
		// it becomes FULLSEND_MINT_URL.
		if err := validateDeclaredMintURL(declared.MintURL); err != nil {
			return fmt.Errorf("%s/%s: effective configuration: %w", c.Owner, c.Repo, err)
		}
		if c.MintURLExplicit && c.MintURL != declared.MintURL {
			return fmt.Errorf("%s/%s: mint URL conflict: the repos.yaml mint_url resolves to %q but the effective configuration (config.mint_url or the config_base preset) declares %q, so FULLSEND_MINT_URL and .fullsend/config.yaml would disagree; make the values match or remove one (fullsend repos set --unset <path>)",
				c.Owner, c.Repo, c.MintURL, declared.MintURL)
		}
		// A mint_url that only an unpinned remote preset supplies lets the
		// preset's owner redirect the mint endpoint (and the OIDC tokens
		// later workflows send to it) without any repos.yaml change.
		// Require a pinned preset (config_base.sha256) or an explicit
		// mint_url in repos.yaml or the managed config before it replaces
		// the manifest-resolved value.
		if declared.MintURL != c.MintURL && c.Config != "" && preset.IsRemote(c.Config) && c.ConfigHash == "" {
			managedOnly, managedErr := config.DeclaredInfrastructureValues(c.Managed, nil)
			if managedErr != nil {
				return fmt.Errorf("%s/%s: reading the managed configuration: %w", c.Owner, c.Repo, managedErr)
			}
			if managedOnly.MintURL == "" {
				return fmt.Errorf("%s/%s: the config_base preset %q declares mint_url %q but is not pinned; pin it with config_base.sha256 or declare mint_url in repos.yaml so the mint endpoint cannot change without a repos.yaml change",
					c.Owner, c.Repo, c.Config, declared.MintURL)
			}
		}
		c.MintURL = declared.MintURL
	}
	// Private mode has no built-in mint URL. Manifest validation accepts a
	// repository whose URL may come from config.mint_url or a preset it
	// could not load, so the requirement is enforced here once the
	// effective configuration is known.
	if c.Forge == ForgeGitHub && c.MintMode == MintModePrivate && c.MintURL == "" {
		return fmt.Errorf("%s/%s: mint_url is required when mint_mode is %q: neither repos.yaml (mint_url, config.mint_url) nor the config_base preset declares one",
			c.Owner, c.Repo, MintModePrivate)
	}
	if declared.InferenceRegion != "" && !IsValidGCPRegion(declared.InferenceRegion) {
		return fmt.Errorf("%s/%s: inference.region %q in the effective configuration is not a valid GCP region (lowercase letters, digits and hyphens, starting with a letter)",
			c.Owner, c.Repo, declared.InferenceRegion)
	}
	c.InferenceRegion = ""
	c.InferenceRegionDeclared = false
	if c.InferenceAuth != InferenceAuthOpenAIAPIKey {
		c.InferenceRegion = firstNonEmpty(declared.InferenceRegion, config.DefaultPerRepoInferenceRegion)
		c.InferenceRegionDeclared = declared.InferenceRegion != ""
	}
	c.DeclaredInferenceProject = declared.InferenceProject
	c.DeclaredWIFProvider = declared.InferenceWIFProvider
	c.DeclaredWIFAudience = declared.InferenceWIFAudience
	c.DeclaredServiceAccount = declared.InferenceServiceAccount
	return c.applyExistingWIFOptions()
}

// expectedInferenceRegion is the FULLSEND_GCP_REGION value install, dry run
// and status check an existing variable against: the region the effective
// configuration declares. With no declared region it is empty (presence
// only), so an existing value — for example one set before repos.yaml
// carried the region — is preserved rather than reset to the code default,
// mirroring how an undeclared app_set preserves FULLSEND_APP_SET. The code
// default is written only where the variable is missing (InferenceRegion).
func (c ResolvedConfig) expectedInferenceRegion() string {
	if c.InferenceRegionDeclared {
		return c.InferenceRegion
	}
	return ""
}

// vertexCredentialInputs returns the GCP project and explicit WIF provider
// used to write a Vertex-using repository's FULLSEND_GCP_PROJECT_ID and
// FULLSEND_GCP_WIF_PROVIDER secrets: the --vertex-project and
// --vertex-wif-provider inputs, else the values the effective configuration
// declares. A command-line input that disagrees with a declared value is a
// configuration error, so the written secrets never silently differ from the
// committed configuration. Empty results mean no input supplies the value;
// a WIF provider without a project is rejected, and a declared WIF provider
// additionally requires a declared project (--vertex-project alone does not
// satisfy it) so status and install agree. openai-api-key repositories
// use no Vertex inputs.
func vertexCredentialInputs(cfg ConvergeConfig, resolved ResolvedConfig) (project, wifProvider string, err error) {
	if resolved.InferenceAuth == InferenceAuthOpenAIAPIKey {
		return "", "", nil
	}
	repo := resolved.Owner + "/" + resolved.Repo
	project, err = reconcileVertexInput(repo, "--vertex-project", cfg.InferenceProject, "inference.project", resolved.DeclaredInferenceProject)
	if err != nil {
		return "", "", err
	}
	wifProvider, err = reconcileVertexInput(repo, "--vertex-wif-provider", cfg.WIFProvider, "inference.wif_provider", resolved.DeclaredWIFProvider)
	if err != nil {
		return "", "", err
	}
	if project != "" && !IsValidGCPProjectID(project) {
		return "", "", fmt.Errorf("%s: GCP project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter)", repo, project)
	}
	if wifProvider != "" && !WIFProviderPattern.MatchString(wifProvider) {
		return "", "", fmt.Errorf("%s: WIF provider %q is not a valid WIF provider (expected projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id})", repo, wifProvider)
	}
	if wifProvider != "" && project == "" {
		return "", "", fmt.Errorf("%s: a WIF provider (--vertex-wif-provider or inference.wif_provider) requires a GCP project: declare config.inference.project in repos.yaml or pass --vertex-project", repo)
	}
	// A declared WIF provider needs a declared project too, so status, which
	// cannot see command-line inputs, judges the configuration exactly as
	// install does.
	if resolved.DeclaredWIFProvider != "" && resolved.DeclaredInferenceProject == "" {
		return "", "", fmt.Errorf("%s: inference.wif_provider requires a GCP project declared as config.inference.project in repos.yaml or the config_base preset; --vertex-project alone does not satisfy it", repo)
	}
	// The audience and impersonation options describe an existing provider
	// the installer does not own. The derived per-project gitlab-oidc
	// provider accepts only aud "fullsend" and is used by direct federation,
	// so the provider must be named explicitly.
	if (resolved.DeclaredWIFAudience != "" || resolved.DeclaredServiceAccount != "") && wifProvider == "" {
		return "", "", fmt.Errorf("%s: inference.wif_audience and inference.service_account require an explicitly supplied WIF provider (declare config.inference.wif_provider in repos.yaml or pass --vertex-wif-provider); they cannot be combined with the derived per-project gitlab-oidc provider", repo)
	}
	return project, wifProvider, nil
}

// reconcileVertexInput returns the command-line value when set, else the
// declared one, and rejects a command-line value that contradicts a
// declared value.
func reconcileVertexInput(repo, flag, flagValue, key, declared string) (string, error) {
	if flagValue != "" && declared != "" && flagValue != declared {
		return "", fmt.Errorf("%s: %s %q disagrees with %s %q declared in the effective configuration (repos.yaml config or the config_base preset); the written secret would differ from .fullsend/config.yaml, so make them match or omit %s",
			repo, flag, flagValue, key, declared, flag)
	}
	return firstNonEmpty(flagValue, declared), nil
}
