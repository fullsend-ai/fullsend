package steps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerURLDispatchSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a harness-hosting repository "([^"]+)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenHarnessHostingRepo(world.FromContext(ctx), name)
	})
	sc.Step(`^a URL-sourced custom harness "([^"]+)" with:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenURLSourcedCustomHarness(world.FromContext(ctx), name, doc, urlHarnessOpts{})
	})
	sc.Step(`^a URL-sourced custom harness "([^"]+)" with bad integrity hash:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenURLSourcedCustomHarness(world.FromContext(ctx), name, doc, urlHarnessOpts{badHash: true})
	})
	sc.Step(`^a URL-sourced custom harness "([^"]+)" not in allowlist with:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenURLSourcedCustomHarness(world.FromContext(ctx), name, doc, urlHarnessOpts{skipAllowlist: true})
	})
}

type urlHarnessOpts struct {
	badHash       bool
	skipAllowlist bool
}

// snapshotAllowedResources captures the current allowed_remote_resources
// from config.yaml into w.AllowedResourcesOriginal — but only on the
// first call per scenario, so that multiple URL-harness steps in the same
// scenario do not overwrite the original value with an already-modified
// copy.
//
// Called by givenURLSourcedCustomHarness (url_dispatch.go) and
// givenURLSourcedBaseHarness (base_dispatch.go) before any allowlist
// modification. CleanupScenario uses the snapshot to restore the
// original allowlist when the scenario is done.
func snapshotAllowedResources(w *world.World) error {
	if w.AllowedResourcesOverridden {
		return nil // already snapshotted this scenario
	}
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config for allowlist snapshot: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config for allowlist snapshot: %w", err)
	}
	orig := cfg.AllowedResources()
	// Store a copy so later mutations do not alias the snapshot.
	w.AllowedResourcesOriginal = make([]string, len(orig))
	copy(w.AllowedResourcesOriginal, orig)
	w.AllowedResourcesOverridden = true
	return nil
}

// snapshotAgents captures the current agents list from config.yaml into
// w.AgentsOriginal — but only on the first call per scenario, so that
// multiple harness steps in the same scenario do not overwrite the
// original value with an already-modified copy.
//
// Called before any cfg.SetAgents() call (dispatch.go, url_dispatch.go,
// base_dispatch.go). CleanupScenario uses the snapshot to restore the
// original agents when the scenario is done.
func snapshotAgents(w *world.World) error {
	if w.AgentsOverridden {
		return nil // already snapshotted this scenario
	}
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config for agents snapshot: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config for agents snapshot: %w", err)
	}
	orig := cfg.AgentEntries()
	// Store a copy so later mutations do not alias the snapshot.
	w.AgentsOriginal = make([]config.AgentEntry, len(orig))
	copy(w.AgentsOriginal, orig)
	w.AgentsOverridden = true
	return nil
}

// RestoreAllowedResources sets allowed_remote_resources back to the
// pre-scenario value captured by snapshotAllowedResources. Exported so
// CleanupScenario can call it during scenario teardown.
func RestoreAllowedResources(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before allowlist operations")
	}
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetAllowedRemoteResources(w.AllowedResourcesOriginal)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: restore allowed_remote_resources", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// RestoreAgents sets the agents list back to the pre-scenario value
// captured by snapshotAgents. Exported so CleanupScenario can call it
// during scenario teardown.
func RestoreAgents(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before agent operations")
	}
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetAgents(w.AgentsOriginal)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: restore agents", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// givenHarnessHostingRepo creates a public repository to host URL-sourced
// harness YAML files. The repo is created in the same org as the test
// repository. It is idempotent — if the repo already exists, it returns
// without error.
//
// The hosting repo is ephemeral: created per-scenario and deleted by
// CleanupScenario (same lifecycle as fork repos). When a leased repo is
// in use, the logical name is remapped via resolveHostRepoName so each
// parallel scenario gets its own isolated hosting repo.
func givenHarnessHostingRepo(w *world.World, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("harness-hosting repository name is required")
	}

	org := w.Org
	if org == "" {
		return fmt.Errorf("org must be set before creating harness-hosting repo")
	}

	resolved := resolveHostRepoName(w, name)

	ctx := context.Background()
	if err := w.SCM.CreateRepo(ctx, org, resolved, "behaviour test: URL harness host"); err != nil {
		return fmt.Errorf("creating harness-hosting repo: %w", err)
	}

	// Set world fields immediately after CreateRepo so that cleanup
	// can reference the repo if subsequent steps fail.
	w.URLHarnessRepoOwner = org
	w.URLHarnessRepoName = resolved

	// The repo must be public so raw.githubusercontent.com URLs are accessible
	// without authentication. Orgs may force repos private despite the
	// CreateRepo(private=false) request; detect and fix that immediately rather
	// than letting the scenario hang later when the URL fetch fails silently.
	if err := w.SCM.EnsureRepoPublic(ctx, org, resolved); err != nil {
		return fmt.Errorf("harness-hosting repo %s/%s must be public for URL-sourced dispatch: %w", org, resolved, err)
	}

	return nil
}

// resolveHostRepoName maps a logical harness-hosting repo name from a
// Gherkin feature file to the actual GitHub repository name. When a
// leased repo is in use (w.LeasedRepoName is set), the logical name
// is prefixed with the leased repo name so each parallel scenario gets
// its own isolated hosting repository.
//
// This mirrors resolveForkName in fork.go — both use the leased repo
// name to namespace ephemeral repos created per-scenario.
//
// Examples:
//
//	"url-harness-host" + leased "test-repo-07" → "test-repo-07-url-harness-host"
//	"url-harness-host" + no lease              → "url-harness-host" (unchanged)
func resolveHostRepoName(w *world.World, logicalName string) string {
	if w.LeasedRepoName == "" {
		return logicalName
	}
	return w.RepoName + "-" + logicalName
}

// givenURLSourcedCustomHarness commits a harness YAML to the harness-hosting
// repository, then registers it as a URL-sourced agent in config.yaml on the
// enrolled test repository. The URL points to the file via
// raw.githubusercontent.com on the default branch of the hosting repo.
func givenURLSourcedCustomHarness(w *world.World, name, doc string, opts urlHarnessOpts) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before URL-harness operations")
	}
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
	harnessPath := path.Join("harness", name+".yaml")
	content := []byte(doc)
	ctx := context.Background()
	if err := w.SCM.CommitFile(ctx, hostOwner, hostRepo, harnessPath, fmt.Sprintf("behaviour: add URL harness %s", name), content); err != nil {
		return fmt.Errorf("committing harness to hosting repo: %w", err)
	}

	// ADR-0045: when the runtime loads a URL-sourced harness, it resolves
	// relative resource paths (agent, policy, skills) against the hosting
	// repo URL directory. Commit any relative resources so the runtime can
	// fetch them. Without this, LoadWithBase fails because the agent file
	// does not exist at the resolved URL.
	relativePaths, err := commitRelativeResources(ctx, w, hostOwner, hostRepo, name, doc)
	if err != nil {
		return fmt.Errorf("committing relative resources to hosting repo: %w", err)
	}

	// Verify the committed harness file is accessible via the Contents API.
	// Edge-cache propagation on raw.githubusercontent.com can cause
	// transient 404s after a commit; retry briefly rather than letting
	// the scenario hang for the full 30m job timeout.
	if err := waitForFileAccessible(ctx, w, hostOwner, hostRepo, harnessPath); err != nil {
		return fmt.Errorf("harness file not accessible after commit (raw URL will fail): %w", err)
	}

	// Also verify relative resource files are accessible via the Contents
	// API, matching the same verification applied to the harness YAML itself.
	for _, rp := range relativePaths {
		if err := waitForFileAccessible(ctx, w, hostOwner, hostRepo, rp); err != nil {
			return fmt.Errorf("relative resource %s not accessible after commit: %w", rp, err)
		}
	}

	// Use the actual default branch instead of hardcoding "main".
	// Orgs may use "master" or custom defaults; Contents API succeeds
	// on any default branch, but the raw URL must match exactly.
	defaultBranch, err := w.SCM.GetDefaultBranch(ctx, hostOwner, hostRepo)
	if err != nil {
		return fmt.Errorf("getting default branch for %s/%s: %w", hostOwner, hostRepo, err)
	}

	// Compute the SHA256 of the content for the integrity hash.
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	if opts.badHash {
		// Use a deliberately wrong hash to trigger integrity failure.
		hash = "0000000000000000000000000000000000000000000000000000000000000000"
	}

	// Build the raw.githubusercontent.com URL with integrity hash.
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s#sha256=%s", hostOwner, hostRepo, defaultBranch, harnessPath, hash)

	// Verify the raw URL is accessible without authentication.
	// The Contents API uses an authenticated token, but production
	// FetchAgentHarness fetches the raw URL unauthenticated. If the
	// repo is not truly public or the edge cache hasn't propagated,
	// this catches the mismatch early instead of hanging for 12+ minutes.
	if err := verifyRawURLAccessible(rawURL); err != nil {
		return fmt.Errorf("raw URL not accessible (repo may not be public or edge cache not propagated): %w", err)
	}

	// Log the constructed URL for diagnostics if the scenario fails later.
	if w.Logf != nil {
		w.Logf("URL-sourced harness %q: rawURL=%s defaultBranch=%s", name, rawURL, defaultBranch)
	}

	// Build the URL prefix for the allowlist.
	urlPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", hostOwner, hostRepo)

	// Snapshot the current allowed_remote_resources and agents before any
	// modification so CleanupScenario can restore them when the slot is
	// reused.
	if err := snapshotAllowedResources(w); err != nil {
		return fmt.Errorf("snapshotting allowed_remote_resources: %w", err)
	}
	if err := snapshotAgents(w); err != nil {
		return fmt.Errorf("snapshotting agents: %w", err)
	}

	// Update config.yaml on the enrolled test repo: register agent with URL
	// source and update allowlist.
	cfgOwner := w.Org
	cfgRepo := w.RepoName
	cfgPath := path.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(ctx, cfgOwner, cfgRepo, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	// Register agent with URL source.
	entry := config.AgentEntry{Name: name, Source: rawURL}
	agents := cfg.AgentEntries()
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
	cfg.SetAgents(agents)

	// Add URL prefix to allowed_remote_resources unless testing allowlist failure.
	if !opts.skipAllowlist {
		allowed := cfg.AllowedResources()
		if !slices.Contains(allowed, urlPrefix) {
			allowed = append(allowed, urlPrefix)
		}
		cfg.SetAllowedRemoteResources(allowed)
	}

	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(ctx, cfgOwner, cfgRepo, cfgPath, fmt.Sprintf("behaviour: register URL harness %s", name), merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// minimalAgentContent is a stub agent definition committed to the hosting
// repo so that URL-sourced harness resource resolution succeeds at runtime.
// The behaviour tests override the agent with a dummy script, so the content
// only needs to be fetchable — not a complete agent specification.
const minimalAgentContent = "# URL Test Agent\n\nMinimal agent fixture for URL-sourced harness behaviour tests.\n"

// commitRelativeResources parses the harness YAML doc and commits any
// relative resource files (agent, policy) to the hosting repo. This is
// required by ADR-0045: when SourceURL is set, resolveBaseResources
// fetches relative paths from the hosting repo URL directory.
// Returns the list of committed relative paths for subsequent verification.
func commitRelativeResources(ctx context.Context, w *world.World, owner, repo, harnessName, doc string) ([]string, error) {
	// Parse just the resource fields we need from the harness YAML.
	var h struct {
		Agent  string `yaml:"agent"`
		Policy string `yaml:"policy"`
	}
	if err := yaml.Unmarshal([]byte(doc), &h); err != nil {
		return nil, fmt.Errorf("parsing harness YAML for resource paths: %w", err)
	}

	var committed []string

	// Commit relative agent file if specified.
	if h.Agent != "" && !strings.HasPrefix(h.Agent, "/") && !strings.HasPrefix(h.Agent, "https://") {
		if err := w.SCM.CommitFile(ctx, owner, repo, h.Agent,
			fmt.Sprintf("behaviour: add agent resource for %s", harnessName),
			[]byte(minimalAgentContent)); err != nil {
			return nil, fmt.Errorf("committing agent resource %s: %w", h.Agent, err)
		}
		committed = append(committed, h.Agent)
	}

	// Commit relative policy file if specified.
	if h.Policy != "" && !strings.HasPrefix(h.Policy, "/") && !strings.HasPrefix(h.Policy, "https://") {
		minimalPolicy := fmt.Sprintf("# Minimal policy for %s\n", harnessName)
		if err := w.SCM.CommitFile(ctx, owner, repo, h.Policy,
			fmt.Sprintf("behaviour: add policy resource for %s", harnessName),
			[]byte(minimalPolicy)); err != nil {
			return nil, fmt.Errorf("committing policy resource %s: %w", h.Policy, err)
		}
		committed = append(committed, h.Policy)
	}

	return committed, nil
}

// waitForFileAccessible polls the Contents API until the file is readable,
// retrying briefly for edge-cache propagation delays on
// raw.githubusercontent.com. This prevents the scenario from hanging
// silently when the raw URL returns 404 due to eventual consistency.
//
// The retry budget (5 attempts, 2s apart = 10s max) is calibrated for
// GitHub's typical CDN propagation latency of 1-5s. Production harness
// dispatch has its own timeout via the job-level 30m limit; this retry
// exists to fail fast with a clear error rather than proceeding with a
// URL that will 404.
func waitForFileAccessible(ctx context.Context, w *world.World, owner, repo, path string) error {
	const maxAttempts = 5
	var lastErr error
	for i := range maxAttempts {
		_, err := w.SCM.GetFileContent(ctx, owner, repo, path)
		if err == nil {
			return nil
		}
		lastErr = err
		if i < maxAttempts-1 {
			time.Sleep(fileAccessRetryDelay)
		}
	}
	return fmt.Errorf("file %s in %s/%s not accessible after %d attempts: %w",
		path, owner, repo, maxAttempts, lastErr)
}

// rawHTTPClient is the HTTP client used for unauthenticated raw URL
// verification. It uses an explicit timeout to prevent the retry loop
// from hanging indefinitely on slow or unresponsive endpoints.
// It can be overridden in tests to avoid real HTTP calls.
var rawHTTPClient = &http.Client{Timeout: 30 * time.Second}

// rawURLRetryDelay is the delay between retries for raw URL verification.
// Overridden in tests to avoid slow retry loops.
var rawURLRetryDelay = 2 * time.Second

// fileAccessRetryDelay is the delay between retries for Contents API checks.
// Overridden in tests to avoid slow retry loops.
var fileAccessRetryDelay = 2 * time.Second

// verifyRawURLAccessible performs an unauthenticated HTTP GET of the raw
// URL (stripping the fragment) to verify the file is publicly accessible.
// This catches mismatches between the authenticated Contents API (which
// succeeds with a token even on private repos) and the unauthenticated
// raw.githubusercontent.com URL that production FetchAgentHarness uses.
//
// The retry budget (5 attempts, 2s apart = 10s max) matches
// waitForFileAccessible and targets GitHub's CDN edge-cache propagation.
func verifyRawURLAccessible(rawURL string) error {
	// Strip the #sha256=... fragment — HTTP clients ignore fragments,
	// but be explicit.
	fetchURL := rawURL
	if idx := strings.Index(fetchURL, "#"); idx >= 0 {
		fetchURL = fetchURL[:idx]
	}

	const maxAttempts = 5

	var lastErr error
	for i := range maxAttempts {
		resp, err := rawHTTPClient.Get(fetchURL) //nolint:gosec // URL is constructed, not user input
		if err != nil {
			lastErr = fmt.Errorf("HTTP GET failed: %w", err)
			if i < maxAttempts-1 {
				time.Sleep(rawURLRetryDelay)
			}
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		lastErr = fmt.Errorf("HTTP GET %s returned status %d", fetchURL, resp.StatusCode)
		if i < maxAttempts-1 {
			time.Sleep(rawURLRetryDelay)
		}
	}
	return fmt.Errorf("raw URL %s not accessible after %d attempts: %w",
		fetchURL, maxAttempts, lastErr)
}
