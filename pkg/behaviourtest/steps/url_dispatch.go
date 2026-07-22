package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerURLDispatchSteps(ctx *godog.ScenarioContext, w *world.World) {
	ctx.Step(`^a harness-hosting repository "([^"]+)"$`, func(name string) error {
		return givenHarnessHostingRepo(w, name)
	})
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

// givenHarnessHostingRepo creates a public repository to host URL-sourced
// harness YAML files. The repo is created in the same org as the test
// repository. It is idempotent — if the repo already exists, it returns
// without error.
func givenHarnessHostingRepo(w *world.World, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("harness-hosting repository name is required")
	}

	org := w.Org
	if org == "" {
		return fmt.Errorf("org must be set before creating harness-hosting repo")
	}

	ctx := context.Background()
	if err := w.SCM.CreateRepo(ctx, org, name, "behaviour test: URL harness host"); err != nil {
		return fmt.Errorf("creating harness-hosting repo: %w", err)
	}

	// The repo must be public so raw.githubusercontent.com URLs are accessible
	// without authentication. Orgs may force repos private despite the
	// CreateRepo(private=false) request; detect and fix that immediately rather
	// than letting the scenario hang later when the URL fetch fails silently.
	if err := w.SCM.EnsureRepoPublic(ctx, org, name); err != nil {
		return fmt.Errorf("harness-hosting repo %s/%s must be public for URL-sourced dispatch: %w", org, name, err)
	}

	w.URLHarnessRepoOwner = org
	w.URLHarnessRepoName = name
	return nil
}

// givenURLSourcedCustomHarness commits a harness YAML to the harness-hosting
// repository, then registers it as a URL-sourced agent in config.yaml on the
// enrolled test repository. The URL points to the file via
// raw.githubusercontent.com on the default branch of the hosting repo.
func givenURLSourcedCustomHarness(w *world.World, name, doc string, opts urlHarnessOpts) error {
	name = strings.TrimSpace(name)
	doc = strings.TrimSpace(doc)
	if name == "" || doc == "" {
		return fmt.Errorf("harness name and contents are required")
	}
	if w.URLHarnessRepoOwner == "" || w.URLHarnessRepoName == "" {
		return fmt.Errorf("harness-hosting repo must be created first: use 'Given a harness-hosting repository'")
	}
	w.DispatchAgent = name

	hostOwner := w.URLHarnessRepoOwner
	hostRepo := w.URLHarnessRepoName

	// Commit the harness YAML to the hosting repo at a known path.
	harnessPath := filepath.Join("harness", name+".yaml")
	content := []byte(doc)
	ctx := context.Background()
	if err := w.SCM.CommitFile(ctx, hostOwner, hostRepo, harnessPath, fmt.Sprintf("behaviour: add URL harness %s", name), content); err != nil {
		return fmt.Errorf("committing harness to hosting repo: %w", err)
	}

	// Verify the committed file is accessible via the Contents API.
	// GitHub's auto_init and CDN propagation can cause transient 404s
	// after a commit; retry briefly rather than letting the scenario
	// hang for the full 30m job timeout.
	if err := waitForFileAccessible(ctx, w, hostOwner, hostRepo, harnessPath); err != nil {
		return fmt.Errorf("harness file not accessible after commit (raw URL will fail): %w", err)
	}

	// Compute the SHA256 of the content for the integrity hash.
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	if opts.badHash {
		// Use a deliberately wrong hash to trigger integrity failure.
		hash = "0000000000000000000000000000000000000000000000000000000000000000"
	}

	// Build the raw.githubusercontent.com URL with integrity hash.
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s#sha256=%s", hostOwner, hostRepo, harnessPath, hash)

	// Build the URL prefix for the allowlist.
	urlPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", hostOwner, hostRepo)

	// Update config.yaml on the enrolled test repo: register agent with URL
	// source and update allowlist.
	cfgOwner := w.Install.ConfigOwner()
	cfgRepo := w.Install.ConfigRepo()
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(ctx, cfgOwner, cfgRepo, cfgPath)
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
		if !slices.Contains(cfg.AllowedRemoteResources, urlPrefix) {
			cfg.AllowedRemoteResources = append(cfg.AllowedRemoteResources, urlPrefix)
		}
	}

	merged, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(ctx, cfgOwner, cfgRepo, cfgPath, fmt.Sprintf("behaviour: register URL harness %s", name), merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// waitForFileAccessible polls the Contents API until the file is readable,
// retrying briefly for CDN / commit propagation delays. This prevents the
// scenario from hanging silently when the raw URL returns 404 due to
// eventual consistency.
func waitForFileAccessible(ctx context.Context, w *world.World, owner, repo, path string) error {
	const (
		maxAttempts = 5
		retryDelay  = 2 * time.Second
	)
	var lastErr error
	for i := range maxAttempts {
		_, err := w.SCM.GetFileContent(ctx, owner, repo, path)
		if err == nil {
			return nil
		}
		lastErr = err
		if i < maxAttempts-1 {
			time.Sleep(retryDelay)
		}
	}
	return fmt.Errorf("file %s in %s/%s not accessible after %d attempts: %w",
		path, owner, repo, maxAttempts, lastErr)
}
