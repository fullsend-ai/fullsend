package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const configFilePath = "config.yaml"

// ConfigRepoLayer manages the .fullsend configuration repository.
// This is the foundational layer — it must be installed before any
// other layers that depend on the config repo existing.
type ConfigRepoLayer struct {
	org        string
	client     forge.Client
	config     *config.OrgConfig
	ui         *ui.Printer
	hasPrivate bool // whether org supports private repos
}

// Compile-time check that ConfigRepoLayer implements Layer.
var _ Layer = (*ConfigRepoLayer)(nil)

// NewConfigRepoLayer creates a new ConfigRepoLayer.
// Set hasPrivate to true if the org has any private repos — the config repo
// will be created as private to match the org's existing pattern. For orgs
// with only public repos (e.g., open source orgs), it is created as public
// to avoid surprises. This matters because the .fullsend repo may contain
// workflow files referenced by public repos.
func NewConfigRepoLayer(org string, client forge.Client, cfg *config.OrgConfig, printer *ui.Printer, hasPrivate bool) *ConfigRepoLayer {
	return &ConfigRepoLayer{
		org:        org,
		client:     client,
		config:     cfg,
		ui:         printer,
		hasPrivate: hasPrivate,
	}
}

func (l *ConfigRepoLayer) Name() string {
	return "config-repo"
}

// Install creates the .fullsend config repo (if it doesn't exist) and
// writes config.yaml into it.
//
// Timing note: after CreateRepo with auto_init, the default branch may not
// be fully materialized yet. The Contents API call to write config.yaml can
// get transient 404s. The GitHub client's retry-with-backoff in do() handles
// this, but callers should be aware that the first file write to a newly
// created repo may take several seconds to succeed.
func (l *ConfigRepoLayer) Install(ctx context.Context) error {
	exists, err := l.repoExists(ctx)
	if err != nil {
		return fmt.Errorf("checking for config repo: %w", err)
	}

	if !exists {
		l.ui.StepStart("Creating " + forge.ConfigRepoName + " repository")
		desc := fmt.Sprintf("fullsend configuration for %s", l.org)
		_, err := l.client.CreateRepo(ctx, l.org, forge.ConfigRepoName, desc, l.hasPrivate)
		if err != nil {
			l.ui.StepFail("Failed to create " + forge.ConfigRepoName + " repository")
			return fmt.Errorf("creating config repo: %w", err)
		}
		l.ui.StepDone("Created " + forge.ConfigRepoName + " repository")
	} else {
		l.ui.StepInfo(forge.ConfigRepoName + " repository already exists")
	}

	l.ui.StepStart("Writing " + configFilePath)
	data, err := l.config.Marshal()
	if err != nil {
		l.ui.StepFail("Failed to marshal config")
		return fmt.Errorf("marshaling config: %w", err)
	}

	err = l.client.CreateOrUpdateFile(ctx, l.org, forge.ConfigRepoName, configFilePath, "chore: update fullsend configuration", data)
	if err != nil {
		l.ui.StepFail("Failed to write " + configFilePath)
		return fmt.Errorf("writing config file: %w", err)
	}
	l.ui.StepDone("Wrote " + configFilePath)

	return nil
}

// Uninstall deletes the .fullsend config repo.
func (l *ConfigRepoLayer) Uninstall(ctx context.Context) error {
	l.ui.StepStart("Deleting " + forge.ConfigRepoName + " repository")
	if err := l.client.DeleteRepo(ctx, l.org, forge.ConfigRepoName); err != nil {
		l.ui.StepFail("Failed to delete " + forge.ConfigRepoName + " repository")
		return fmt.Errorf("deleting config repo: %w", err)
	}
	l.ui.StepDone("Deleted " + forge.ConfigRepoName + " repository")
	return nil
}

// Analyze checks whether the .fullsend repo and config.yaml exist and are valid.
func (l *ConfigRepoLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{
		Name: l.Name(),
	}

	exists, err := l.repoExists(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking for config repo: %w", err)
	}

	if !exists {
		report.Status = StatusNotInstalled
		report.WouldInstall = []string{
			"create " + forge.ConfigRepoName + " repository",
			"write " + configFilePath,
		}
		return report, nil
	}

	// Repo exists — check for config.yaml
	content, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, configFilePath)
	if err != nil {
		// File missing or unreadable
		if forge.IsNotFound(err) {
			report.Status = StatusDegraded
			report.Details = []string{"repo exists but " + configFilePath + " is missing"}
			report.WouldFix = []string{"write " + configFilePath}
			return report, nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// File exists — validate it
	parsed, parseErr := config.ParseOrgConfig(content)
	if parseErr != nil {
		report.Status = StatusDegraded
		report.Details = []string{configFilePath + " exists but is invalid: " + parseErr.Error()}
		report.WouldFix = []string{"rewrite " + configFilePath}
		return report, nil
	}

	if validateErr := parsed.Validate(); validateErr != nil {
		report.Status = StatusDegraded
		report.Details = []string{configFilePath + " exists but is invalid: " + validateErr.Error()}
		report.WouldFix = []string{"rewrite " + configFilePath}
		return report, nil
	}

	report.Status = StatusInstalled
	report.Details = []string{configFilePath + " exists and is valid"}
	return report, nil
}

// repoExists checks whether the .fullsend repo exists in the org.
func (l *ConfigRepoLayer) repoExists(ctx context.Context) (bool, error) {
	_, err := l.client.GetRepo(ctx, l.org, forge.ConfigRepoName)
	if err == nil {
		return true, nil
	}
	if forge.IsNotFound(err) {
		return false, nil
	}
	return false, err
}
