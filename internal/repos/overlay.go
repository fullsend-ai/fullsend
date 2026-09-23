package repos

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
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

// RenderManagedOverlay returns the canonical sparse .fullsend/config.yaml
// bytes for an overlay-managed repository. ok is false when the repository
// is not overlay-managed; data is then nil.
func (m *Manifest) RenderManagedOverlay(entry RepoEntry) (data []byte, ok bool, err error) {
	if !overlayManaged(m.Defaults.Config, entry.Config) {
		return nil, false, nil
	}
	overlay := m.managedOverlay(entry)
	if err := overlay.Validate(); err != nil {
		return nil, true, fmt.Errorf("managed config overlay: %w", err)
	}
	body, err := overlay.Marshal()
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
	}{{"github", m.GitHub}, {"gitlab", m.GitLab}} {
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
			if err := overlay.Validate(); err != nil {
				return fmt.Errorf("%s: merged overlay: %w", field, err)
			}
		}
	}
	return nil
}

func validateOverlayBlock(field string, o config.OverlayConfig) error {
	if !o.IsSet() {
		return nil
	}
	if err := o.Writer().Validate(); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}
