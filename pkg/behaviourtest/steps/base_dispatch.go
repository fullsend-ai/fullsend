package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerBaseDispatchSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a custom harness "([^"]+)" with base "([^"]+)" and:$`, func(ctx context.Context, name, baseName, doc string) (context.Context, error) {
		return ctx, givenCustomHarnessWithLocalBase(world.FromContext(ctx), name, baseName, doc)
	})
	sc.Step(`^a URL-sourced base harness "([^"]+)" with:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenURLSourcedBaseHarness(world.FromContext(ctx), name, doc)
	})
	sc.Step(`^a custom harness "([^"]+)" with URL base "([^"]+)" and:$`, func(ctx context.Context, name, baseName, doc string) (context.Context, error) {
		return ctx, givenCustomHarnessWithURLBase(world.FromContext(ctx), name, baseName, doc)
	})
	sc.Step(`^a URL-sourced custom harness "([^"]+)" with URL base "([^"]+)" and:$`, func(ctx context.Context, name, baseName, doc string) (context.Context, error) {
		return ctx, givenURLSourcedCustomHarnessWithURLBase(world.FromContext(ctx), name, baseName, doc)
	})
}

// givenCustomHarnessWithLocalBase creates a local child harness that
// references a local base harness via the base: field. The base must
// already exist at .fullsend/harness/<baseName>.yaml (typically created
// by the "a custom harness" step). The child YAML is prepended with
// "base: <baseName>.yaml" before committing.
func givenCustomHarnessWithLocalBase(w *world.World, name, baseName, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before harness operations")
	}
	name = strings.TrimSpace(name)
	baseName = strings.TrimSpace(baseName)
	doc = strings.TrimSpace(doc)
	if name == "" || baseName == "" || doc == "" {
		return fmt.Errorf("harness name, base name, and contents are required")
	}
	w.DispatchAgent = name

	// Prepend the base: field to the child YAML. Both harnesses are in
	// the same directory (.fullsend/harness/), so a bare filename suffices.
	childDoc := fmt.Sprintf("base: %s.yaml\n%s", baseName, doc)

	harnessPath := path.Join(".fullsend", "harness", name+".yaml")
	ctx := context.Background()
	if err := w.SCM.CommitFile(ctx, w.Org, w.RepoName, harnessPath,
		fmt.Sprintf("behaviour: add base-composed harness %s", name), []byte(childDoc)); err != nil {
		return fmt.Errorf("committing harness: %w", err)
	}

	if err := commitLocalHarnessResources(ctx, w, name, doc); err != nil {
		return err
	}

	return registerLocalAgentConfig(ctx, w, name,
		fmt.Sprintf("behaviour: register base-composed harness %s", name))
}

// givenURLSourcedBaseHarness commits a base harness to the hosting repo
// without registering it as an agent in config.yaml. The harness URL is
// stored in w.URLBaseHarnesses so child steps can reference it. The
// hosting repo URL prefix is added to allowed_remote_resources so that
// LoadWithBase can fetch the base at dispatch time.
func givenURLSourcedBaseHarness(w *world.World, name, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before base-harness operations")
	}
	name = strings.TrimSpace(name)
	doc = strings.TrimSpace(doc)
	if name == "" || doc == "" {
		return fmt.Errorf("base harness name and contents are required")
	}
	if w.URLHarnessRepoOwner == "" || w.URLHarnessRepoName == "" {
		return fmt.Errorf("harness-hosting repo must be created first: use 'Given a harness-hosting repository'")
	}

	hostOwner := w.URLHarnessRepoOwner
	hostRepo := w.URLHarnessRepoName

	// Commit the base harness YAML to the hosting repo.
	harnessPath := path.Join("harness", name+".yaml")
	content := []byte(doc)
	ctx := context.Background()
	if err := w.SCM.CommitFile(ctx, hostOwner, hostRepo, harnessPath,
		fmt.Sprintf("behaviour: add URL base harness %s", name), content); err != nil {
		return fmt.Errorf("committing base harness to hosting repo: %w", err)
	}

	// Commit relative resources (agent, policy) to the hosting repo.
	relativePaths, err := commitRelativeResources(ctx, w, hostOwner, hostRepo, name, doc)
	if err != nil {
		return fmt.Errorf("committing base harness resources: %w", err)
	}

	// Verify the committed file is accessible.
	if err := waitForFileAccessible(ctx, w, hostOwner, hostRepo, harnessPath); err != nil {
		return fmt.Errorf("base harness file not accessible after commit: %w", err)
	}

	// Verify relative resources are accessible.
	for _, rp := range relativePaths {
		if err := waitForFileAccessible(ctx, w, hostOwner, hostRepo, rp); err != nil {
			return fmt.Errorf("base harness resource %s not accessible after commit: %w", rp, err)
		}
	}

	// Get the default branch for building the raw URL.
	defaultBranch, err := w.SCM.GetDefaultBranch(ctx, hostOwner, hostRepo)
	if err != nil {
		return fmt.Errorf("getting default branch for %s/%s: %w", hostOwner, hostRepo, err)
	}

	// Compute SHA256 integrity hash.
	hash := fmt.Sprintf("%x", sha256.Sum256(content))

	// Build the raw URL with integrity hash.
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s#sha256=%s",
		hostOwner, hostRepo, defaultBranch, harnessPath, hash)

	// Verify the raw URL is publicly accessible.
	if err := verifyRawURLAccessible(rawURL); err != nil {
		return fmt.Errorf("base harness raw URL not accessible: %w", err)
	}

	if w.Logf != nil {
		w.Logf("URL-sourced base harness %q: rawURL=%s defaultBranch=%s", name, rawURL, defaultBranch)
	}

	// Store the URL for child steps.
	if w.URLBaseHarnesses == nil {
		w.URLBaseHarnesses = make(map[string]string)
	}
	w.URLBaseHarnesses[name] = rawURL

	// Add hosting repo URL prefix to allowed_remote_resources so
	// LoadWithBase can fetch the base at dispatch time.
	urlPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", hostOwner, hostRepo)
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(ctx, w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	allowed := cfg.AllowedResources()
	if !slices.Contains(allowed, urlPrefix) {
		allowed = append(allowed, urlPrefix)
		cfg.SetAllowedRemoteResources(allowed)
		merged, err := cfg.Marshal()
		if err != nil {
			return err
		}
		if err := w.SCM.CommitFile(ctx, w.Org, w.RepoName, cfgPath,
			fmt.Sprintf("behaviour: add base harness %s URL to allowlist", name), merged); err != nil {
			return fmt.Errorf("updating config: %w", err)
		}
	}

	return nil
}

// givenCustomHarnessWithURLBase creates a local child harness whose
// base: field points to a URL-sourced base harness in the hosting repo.
// The base must have been created by "a URL-sourced base harness" step.
func givenCustomHarnessWithURLBase(w *world.World, name, baseName, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before harness operations")
	}
	name = strings.TrimSpace(name)
	baseName = strings.TrimSpace(baseName)
	doc = strings.TrimSpace(doc)
	if name == "" || baseName == "" || doc == "" {
		return fmt.Errorf("harness name, base name, and contents are required")
	}

	baseURL, ok := w.URLBaseHarnesses[baseName]
	if !ok {
		return fmt.Errorf("URL base harness %q not found; create it first with 'a URL-sourced base harness'", baseName)
	}

	w.DispatchAgent = name

	// Prepend the base: field with the full URL.
	childDoc := fmt.Sprintf("base: %s\n%s", baseURL, doc)

	harnessPath := path.Join(".fullsend", "harness", name+".yaml")
	ctx := context.Background()
	if err := w.SCM.CommitFile(ctx, w.Org, w.RepoName, harnessPath,
		fmt.Sprintf("behaviour: add URL-base-composed harness %s", name), []byte(childDoc)); err != nil {
		return fmt.Errorf("committing harness: %w", err)
	}

	if err := commitLocalHarnessResources(ctx, w, name, doc); err != nil {
		return err
	}

	return registerLocalAgentConfig(ctx, w, name,
		fmt.Sprintf("behaviour: register URL-base-composed harness %s", name))
}

// registerLocalAgentConfig registers a local harness as an agent in the
// repo's config.yaml. It reads the current config, upserts the agent
// entry with source "harness/<name>.yaml", and commits the updated
// config with the given commit message. This deduplicates the config
// update boilerplate shared by givenCustomHarnessWithLocalBase and
// givenCustomHarnessWithURLBase.
func registerLocalAgentConfig(ctx context.Context, w *world.World, name, commitMsg string) error {
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(ctx, w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfgW, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	entry := config.AgentEntry{Name: name, Source: "harness/" + name + ".yaml"}
	agents := cfgW.AgentEntries()
	found := false
	for i, a := range agents {
		if strings.EqualFold(a.DerivedName(), name) {
			agents[i] = entry
			found = true
			break
		}
	}
	if !found {
		agents = append(agents, entry)
	}
	cfgW.SetAgents(agents)
	merged, err := cfgW.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(ctx, w.Org, w.RepoName, cfgPath, commitMsg, merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// givenURLSourcedCustomHarnessWithURLBase creates a URL-sourced child
// harness that references a URL-sourced base harness. Both are committed
// to the hosting repo. The child's base: field uses the full URL of the
// base (not a relative path) because LoadWithBase resolves relative base
// paths against the child's local cache directory, where the base would
// not exist.
func givenURLSourcedCustomHarnessWithURLBase(w *world.World, name, baseName, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before harness operations")
	}
	name = strings.TrimSpace(name)
	baseName = strings.TrimSpace(baseName)
	doc = strings.TrimSpace(doc)
	if name == "" || baseName == "" || doc == "" {
		return fmt.Errorf("harness name, base name, and contents are required")
	}
	if w.URLHarnessRepoOwner == "" || w.URLHarnessRepoName == "" {
		return fmt.Errorf("harness-hosting repo must be created first: use 'Given a harness-hosting repository'")
	}

	baseURL, ok := w.URLBaseHarnesses[baseName]
	if !ok {
		return fmt.Errorf("URL base harness %q not found; create it first with 'a URL-sourced base harness'", baseName)
	}

	w.DispatchAgent = name

	// Prepend the base: field with the full URL.
	childDoc := fmt.Sprintf("base: %s\n%s", baseURL, doc)

	// Use the existing URL-sourced harness registration with the
	// modified doc that includes the base: field.
	return givenURLSourcedCustomHarness(w, name, childDoc, urlHarnessOpts{})
}
