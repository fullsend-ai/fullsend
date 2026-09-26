package repos

import (
	"fmt"
	"net/url"

	"github.com/fullsend-ai/fullsend/internal/config"
	"gopkg.in/yaml.v3"
)

// overlayManaged reports whether defaults.config or the repository config
// block opts this repository into a managed .fullsend/config.yaml overlay.
func overlayManaged(defaults, entry config.OverlayConfig) bool {
	return defaults.IsSet() || entry.IsSet()
}

// managedOverlay flattens defaults.config, the repository config, and
// authoritative runtime / allowed_remote_resources shorthands into a sparse
// overlay. It does not bake in code defaults or config.base.yaml.
func (m *Manifest) managedOverlay(entry RepoEntry) config.PerRepoConfigWriter {
	merged := config.MergeOverlays(m.Defaults.Config.Writer(), entry.Config.Writer())
	if merged == nil {
		merged = config.NewEmptyPerRepoOverlay()
	}
	config.ApplyOverlayShorthands(merged, resolveField(entry.Runtime, m.Defaults.Runtime, ""), overlayAllowlist(entry, m.Defaults))
	return merged
}

func overlayAllowlist(entry RepoEntry, defaults DefaultsConfig) []string {
	if entry.AllowedRemoteResources != nil {
		return entry.AllowedRemoteResources
	}
	return defaults.AllowedRemoteResources
}

// RenderManagedOverlay returns the canonical sparse overlay YAML body for
// an overlay-managed repository — the explicitly supplied manifest values,
// with no code defaults or config.base.yaml baked in. ok is false when the
// repository is not overlay-managed; data is then nil.
//
// The returned bytes deliberately carry no file header. perRepoConfig's
// Marshal() prepends perRepoConfigHeader, the unmanaged per-repo-install
// header — not the ADR 0122 ownership marker
// ("# This file is managed by 'fullsend repos': ...") that every managed
// .fullsend/config.yaml must begin with. Whether to write that marker (on
// first creation), or require an adoption acknowledgement first (existing
// unmarked config.yaml), depends on adoption-detection state this function
// does not have. That decision, and prefixing the marker, belongs to the
// install path (#7632); this only renders the config body.
func (m *Manifest) RenderManagedOverlay(entry RepoEntry) (data []byte, ok bool, err error) {
	if !overlayManaged(m.Defaults.Config, entry.Config) {
		return nil, false, nil
	}
	overlay := m.managedOverlay(entry)
	if err := config.ValidateMergedOverlay(overlay); err != nil {
		return nil, true, fmt.Errorf("managed config overlay: %w", err)
	}
	if err := validateOverlayMintAndWIF(overlay); err != nil {
		return nil, true, fmt.Errorf("managed config overlay: %w", err)
	}
	body, err := yaml.Marshal(overlay)
	if err != nil {
		return nil, true, fmt.Errorf("marshaling managed config overlay: %w", err)
	}
	return body, true, nil
}

// LayeredConfig returns the effective per-repo configuration for entry:
// managed overlay → config.base.yaml (baseYAML) → code defaults.
// Unmanaged repositories skip the overlay layer. baseYAML may be empty.
func (m *Manifest) LayeredConfig(entry RepoEntry, baseYAML []byte) (config.PerRepoConfigReader, error) {
	var overlay config.PerRepoConfigWriter
	if overlayManaged(m.Defaults.Config, entry.Config) {
		overlay = m.managedOverlay(entry)
	}
	return config.LayerOnBase(overlay, baseYAML)
}

func validateManifestOverlays(m *Manifest) error {
	if err := validateOverlayBlock("defaults.config", m.Defaults.Config); err != nil {
		return err
	}
	for _, p := range []struct {
		name string
		cfg  *PlatformConfig
	}{{ForgeGitHub, m.GitHub}, {ForgeGitLab, m.GitLab}} {
		if p.cfg == nil {
			continue
		}
		for i, entry := range p.cfg.Repos {
			field := fmt.Sprintf("%s.repos[%d].config", p.name, i)
			if entry.Name != "" {
				field = fmt.Sprintf("%s.repos[%s].config", p.name, entry.Name)
			}
			if err := validateOverlayBlock(field, entry.Config); err != nil {
				return err
			}
			if !overlayManaged(m.Defaults.Config, entry.Config) {
				continue
			}
			overlay := m.managedOverlay(entry)
			if err := config.ValidateMergedOverlay(overlay); err != nil {
				return fmt.Errorf("%s: merged overlay: %w", field, err)
			}
			if err := validateOverlayMintAndWIF(overlay); err != nil {
				return fmt.Errorf("%s: merged overlay: %w", field, err)
			}
		}
	}
	return nil
}

// validateOverlayBlock validates a single overlay layer (defaults.config
// or one repository's raw config block) in isolation, before
// defaults.config and the repository config are merged and before
// repos.yaml shorthands are applied. It intentionally does not run the
// checks that need that merge (agent allowlist, override-only custom
// agents) — see config.ValidateOverlayLayer — those run again, correctly,
// against the merged overlay below.
func validateOverlayBlock(field string, o config.OverlayConfig) error {
	if !o.IsSet() {
		return nil
	}
	if err := config.ValidateOverlayLayer(o.Writer()); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if err := validateOverlayMintAndWIF(o.Writer()); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

// validateOverlayMintAndWIF runs the same mint_url HTTPS/userinfo gate
// used for RepoEntry.MintURL and the CLI's validateMintURLHTTPS, plus the
// WIF provider resource-name pattern check, on an overlay layer or the
// shorthand-merged overlay. Neither field is touched by
// ApplyOverlayShorthands, so the same check is correct whether r is an
// isolated layer or the merged overlay; an unset value resolves through
// the parent chain to the code default (always a valid HTTPS mint URL),
// so this only ever flags a value the overlay itself set. Overlay
// install/converge (which would actually use these values to drive a
// mint install) is follow-on work (#7632, #7633); this closes the format
// gate before that lands. Unlike github.url/gitlab.url (validated via
// RejectExtraneousURLParts), mint_url is allowed to carry a path — e.g. a
// Cloud Functions mint endpoint — matching every other mint_url gate in
// this codebase.
func validateOverlayMintAndWIF(r config.PerRepoConfigReader) error {
	if mintURL := r.ConfigMintURL(); mintURL != "" {
		mu, err := url.Parse(mintURL)
		if err != nil || mu.Scheme != "https" || mu.Host == "" {
			return fmt.Errorf("mint_url must be a valid HTTPS URL, got %q", mintURL)
		}
		if mu.User != nil {
			return fmt.Errorf("mint_url must not contain userinfo, got %q", mintURL)
		}
	}
	if wif := r.ConfigInferenceWIFProvider(); wif != "" && !WIFProviderPattern.MatchString(wif) {
		return fmt.Errorf("inference.wif_provider %q does not match the required resource name pattern", wif)
	}
	return nil
}
