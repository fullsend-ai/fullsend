package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerURLDispatchSteps(ctx *godog.ScenarioContext, w *world.World) {
	ctx.Step(`^a URL-sourced custom harness "([^"]+)" with:$`, func(name, doc string) error {
		return givenURLSourcedCustomHarness(w, name, doc, urlHarnessOpts{})
	})
	ctx.Step(`^a URL-sourced custom harness "([^"]+)" with bad integrity hash:$`, func(name, doc string) error {
		return givenURLSourcedCustomHarness(w, name, doc, urlHarnessOpts{badHash: true})
	})
	ctx.Step(`^a URL-sourced custom harness "([^"]+)" not in allowlist with:$`, func(name, doc string) error {
		return givenURLSourcedCustomHarness(w, name, doc, urlHarnessOpts{skipAllowlist: true})
	})
}

type urlHarnessOpts struct {
	badHash       bool
	skipAllowlist bool
}

// givenURLSourcedCustomHarness commits a harness YAML to the config repo,
// then registers it as a URL-sourced agent in config.yaml. The URL points
// to the file via raw.githubusercontent.com on the default branch.
func givenURLSourcedCustomHarness(w *world.World, name, doc string, opts urlHarnessOpts) error {
	name = strings.TrimSpace(name)
	doc = strings.TrimSpace(doc)
	if name == "" || doc == "" {
		return fmt.Errorf("harness name and contents are required")
	}
	w.DispatchAgent = name

	owner := w.Install.ConfigOwner()
	repo := w.Install.ConfigRepo()

	// Commit the harness YAML to the config repo at a known path.
	harnessPath := filepath.Join(".fullsend", "harness", name+".yaml")
	content := []byte(doc)
	if err := w.SCM.CommitFile(context.Background(), owner, repo, harnessPath, fmt.Sprintf("behaviour: add URL harness %s", name), content); err != nil {
		return fmt.Errorf("committing harness: %w", err)
	}

	// Compute the SHA256 of the content for the integrity hash.
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	if opts.badHash {
		// Use a deliberately wrong hash to trigger integrity failure.
		hash = "0000000000000000000000000000000000000000000000000000000000000000"
	}

	// Build the raw.githubusercontent.com URL with integrity hash.
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s#sha256=%s", owner, repo, harnessPath, hash)

	// Build the URL prefix for the allowlist.
	urlPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", owner, repo)

	// Update config.yaml: register agent with URL source and update allowlist.
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), owner, repo, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfig(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	// Register agent with URL source.
	entry := config.AgentEntry{Name: name, Source: rawURL}
	found := false
	for i, a := range cfg.Agents {
		if strings.EqualFold(a.DerivedName(), name) {
			cfg.Agents[i] = entry
			found = true
			break
		}
	}
	if !found {
		cfg.Agents = append(cfg.Agents, entry)
	}

	// Add URL prefix to allowed_remote_resources unless testing allowlist failure.
	if !opts.skipAllowlist {
		if !containsPrefix(cfg.AllowedRemoteResources, urlPrefix) {
			cfg.AllowedRemoteResources = append(cfg.AllowedRemoteResources, urlPrefix)
		}
	}

	merged, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), owner, repo, cfgPath, fmt.Sprintf("behaviour: register URL harness %s", name), merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// containsPrefix reports whether the list already contains the given prefix.
func containsPrefix(list []string, prefix string) bool {
	return slices.Contains(list, prefix)
}
