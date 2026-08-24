package repos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"gopkg.in/yaml.v3"
)

// manifestConfig holds configuration used by buildManifest and discovery helpers.
type manifestConfig struct {
	Forge       string
	ForgeURL    string
	MintURL     string
	FullsendRef string
	CLIVersion  string
}

// DiscoveredRepo holds the result of discovering a single repo's
// fullsend installation status.
type DiscoveredRepo struct {
	Owner           string
	Repo            string
	Source          string // "per-repo", "per-org", or "new"
	MintURL         string
	InferenceRegion string
	FullsendRef     string
}

type discoveryResult struct {
	repos  []DiscoveredRepo
	errors []string
}

// discoverRepo checks the installation status of a single repository.
func discoverRepo(ctx context.Context, client forge.Client,
	owner, repo string, orgCfg config.OrgConfigReader, forgeName string, progress ProgressFunc) (DiscoveredRepo, error) {

	fullName := owner + "/" + repo
	progress(fullName, "discover", "reading variables")

	fc := ForgeConfigFor(forgeName)
	state, err := ProbeRepoState(ctx, client, owner, repo, forgeName, fc)
	if err != nil && !state.Installed {
		return DiscoveredRepo{}, err
	}
	if err != nil {
		progress(fullName, "discover", fmt.Sprintf("warning: %v", err))
	}

	if state.Installed {
		progress(fullName, "discover", "per-repo installation detected")
		if state.FullsendRef != "" {
			progress(fullName, "discover", fmt.Sprintf("ref: %s", state.FullsendRef))
		}
		return DiscoveredRepo{
			Owner:           owner,
			Repo:            repo,
			Source:          "per-repo",
			MintURL:         state.MintURL,
			InferenceRegion: state.InferenceRegion,
			FullsendRef:     state.FullsendRef,
		}, nil
	}

	// Check for per-org enrollment.
	if orgCfg != nil {
		if repoConfig, exists := orgCfg.RepoMap()[repo]; exists && repoConfig.Enabled {
			progress(fullName, "discover", "per-org enrollment detected")
			ref, err := readWorkflowRef(ctx, client, owner, repo, fc)
			if err != nil {
				return DiscoveredRepo{}, err
			}
			if ref != "" {
				progress(fullName, "discover", fmt.Sprintf("ref: %s", ref))
			}
			d := DiscoveredRepo{
				Owner:  owner,
				Repo:   repo,
				Source: "per-org",
			}
			if mintURL := orgCfg.DispatchSettings().MintURL; mintURL != "" {
				d.MintURL = mintURL
			}
			if d.MintURL == "" {
				v, exists, err := client.GetOrgVariable(ctx, owner, "FULLSEND_MINT_URL")
				if err != nil {
					progress(fullName, "discover", fmt.Sprintf("warning: could not read org variable FULLSEND_MINT_URL: %v", err))
				}
				if err == nil && exists {
					d.MintURL = v
				}
			}
			// Read GCP region from org variables so migrate receives it
			// instead of falling back to the wrong default.
			{
				v, exists, err := client.GetOrgVariable(ctx, owner, "FULLSEND_GCP_REGION")
				if err != nil {
					progress(fullName, "discover", fmt.Sprintf("warning: could not read org variable FULLSEND_GCP_REGION: %v", err))
				}
				if err == nil && exists {
					d.InferenceRegion = v
				}
			}
			d.FullsendRef = ref
			return d, nil
		}
	}

	// Not installed.
	progress(fullName, "discover", "not installed")
	return DiscoveredRepo{
		Owner:  owner,
		Repo:   repo,
		Source: "new",
	}, nil
}

// buildManifest generates a Manifest from discovered repos and config.
func buildManifest(repos []DiscoveredRepo, cfg manifestConfig) (*Manifest, []string) {
	var todos []string

	forgeName := cfg.Forge

	manifest := &Manifest{
		Version: 1,
	}

	// Populate platform section based on the target forge.
	if forgeName == ForgeGitHub {
		// Compute mint URL: CLI flag > discovery > TODO.
		mintURL := cfg.MintURL
		mintFromFlag := mintURL != ""
		if mintURL == "" {
			mintURL = computeMode(repos, func(d DiscoveredRepo) string { return d.MintURL })
		}
		if mintURL == "" {
			mintURL = "# TODO: set mint URL"
			todos = append(todos, "github.mint_url: set the Cloud Run endpoint URL")
		} else if !mintFromFlag && countDistinct(repos, func(d DiscoveredRepo) string { return d.MintURL }) > 1 {
			todos = append(todos, "github.mint_url: multiple mint URLs discovered; using most common — verify correctness")
		}

		// Compute fullsend ref: CLI flag > discovery > CLI version > DefaultUpstreamRef.
		fullsendRef := cfg.FullsendRef
		if fullsendRef == "" {
			fullsendRef = computeMode(repos, func(d DiscoveredRepo) string { return d.FullsendRef })
		}
		if fullsendRef == "" {
			if cfg.CLIVersion != "" && cfg.CLIVersion != "dev" {
				fullsendRef = "v" + strings.TrimPrefix(cfg.CLIVersion, "v")
			} else {
				fullsendRef = config.DefaultUpstreamRef
			}
		}

		manifest.GitHub = &PlatformConfig{
			URL:         cfg.ForgeURL,
			MintURL:     mintURL,
			FullsendRef: fullsendRef,
		}
	}
	if forgeName == ForgeGitLab {
		gitlabURL := cfg.ForgeURL

		fullsendRef := cfg.FullsendRef
		if fullsendRef == "" {
			fullsendRef = computeMode(repos, func(d DiscoveredRepo) string { return d.FullsendRef })
		}
		if fullsendRef == "" {
			if cfg.CLIVersion != "" && cfg.CLIVersion != "dev" {
				fullsendRef = "v" + strings.TrimPrefix(cfg.CLIVersion, "v")
			} else {
				fullsendRef = config.DefaultUpstreamRef
			}
		}

		manifest.GitLab = &PlatformConfig{
			URL:         gitlabURL,
			FullsendRef: fullsendRef,
		}
	}

	// Build repo entries with per-repo overrides where discovered
	// values differ from the platform-level defaults.
	platform := manifest.PlatformFor(forgeName)
	if platform != nil {
		for _, d := range repos {
			entry := RepoEntry{Name: d.Owner + "/" + d.Repo}

			if forgeName == ForgeGitHub {
				if d.MintURL != "" && d.MintURL != platform.MintURL {
					entry.MintURL = d.MintURL
				}
				if d.FullsendRef != "" && d.FullsendRef != platform.FullsendRef {
					entry.FullsendRef = d.FullsendRef
				}
			}
			if forgeName == ForgeGitLab {
				if d.FullsendRef != "" && d.FullsendRef != platform.FullsendRef {
					entry.FullsendRef = d.FullsendRef
				}
			}
			platform.Repos = append(platform.Repos, entry)
		}
	}

	return manifest, todos
}

// computeMode returns the most common non-empty value across repos.
func computeMode(repos []DiscoveredRepo, extract func(DiscoveredRepo) string) string {
	counts := make(map[string]int)
	for _, r := range repos {
		v := extract(r)
		if v != "" {
			counts[v]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	var best string
	var bestCount int
	for v, c := range counts {
		if c > bestCount || (c == bestCount && v < best) {
			best = v
			bestCount = c
		}
	}
	return best
}

// countDistinct returns the number of distinct non-empty values.
func countDistinct(repos []DiscoveredRepo, extract func(DiscoveredRepo) string) int {
	seen := make(map[string]bool)
	for _, r := range repos {
		v := extract(r)
		if v != "" {
			seen[v] = true
		}
	}
	return len(seen)
}

// MarshalWithHeader serializes the manifest with a descriptive header comment.
func MarshalWithHeader(m *Manifest) ([]byte, error) {
	data, err := yaml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshalling manifest: %w", err)
	}

	header := fmt.Sprintf("# Generated by fullsend repos migrate on %s.\n# Review and adjust before running fullsend repos install.\n",
		time.Now().UTC().Format("2006-01-02"))

	return append([]byte(header), data...), nil
}
