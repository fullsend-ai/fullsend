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
	if err := commitRelativeResources(ctx, w, hostOwner, hostRepo, name, doc); err != nil {
		return fmt.Errorf("committing relative resources to hosting repo: %w", err)
	}

	// Verify the committed file is accessible via the Contents API.
	// GitHub's auto_init and CDN propagation can cause transient 404s
	// after a commit; retry briefly rather than letting the scenario
	// hang for the full 30m job timeout.
	if err := waitForFileAccessible(ctx, w, hostOwner, hostRepo, harnessPath); err != nil {
		return fmt.Errorf("harness file not accessible after commit (raw URL will fail): %w", err)
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
	// repo is not truly public or CDN hasn't propagated, this catches
	// the mismatch early instead of hanging for 12+ minutes.
	if err := verifyRawURLAccessible(rawURL); err != nil {
		return fmt.Errorf("raw URL not accessible (repo may not be public or CDN not propagated): %w", err)
	}

	// Log the constructed URL for diagnostics if the scenario fails later.
	if w.Logf != nil {
		w.Logf("URL-sourced harness %q: rawURL=%s defaultBranch=%s", name, rawURL, defaultBranch)
	}

	// Build the URL prefix for the allowlist.
	urlPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", hostOwner, hostRepo)

	// Update config.yaml on the enrolled test repo: register agent with URL
	// source and update allowlist.
	cfgOwner := w.Install.ConfigOwner()
	cfgRepo := w.Install.ConfigRepo()
	cfgPath := path.Join(".fullsend", "config.yaml")
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

// minimalAgentContent is a stub agent definition committed to the hosting
// repo so that URL-sourced harness resource resolution succeeds at runtime.
// The behaviour tests override the agent with a dummy script, so the content
// only needs to be fetchable — not a complete agent specification.
const minimalAgentContent = "# URL Test Agent\n\nMinimal agent fixture for URL-sourced harness behaviour tests.\n"

// commitRelativeResources parses the harness YAML doc and commits any
// relative resource files (agent, policy) to the hosting repo. This is
// required by ADR-0045: when SourceURL is set, resolveBaseResources
// fetches relative paths from the hosting repo URL directory.
func commitRelativeResources(ctx context.Context, w *world.World, owner, repo, harnessName, doc string) error {
	// Parse just the resource fields we need from the harness YAML.
	var h struct {
		Agent  string `yaml:"agent"`
		Policy string `yaml:"policy"`
	}
	if err := yaml.Unmarshal([]byte(doc), &h); err != nil {
		return fmt.Errorf("parsing harness YAML for resource paths: %w", err)
	}

	// Commit relative agent file if specified.
	if h.Agent != "" && !strings.HasPrefix(h.Agent, "/") && !strings.HasPrefix(h.Agent, "https://") {
		if err := w.SCM.CommitFile(ctx, owner, repo, h.Agent,
			fmt.Sprintf("behaviour: add agent resource for %s", harnessName),
			[]byte(minimalAgentContent)); err != nil {
			return fmt.Errorf("committing agent resource %s: %w", h.Agent, err)
		}
	}

	// Commit relative policy file if specified.
	if h.Policy != "" && !strings.HasPrefix(h.Policy, "/") && !strings.HasPrefix(h.Policy, "https://") {
		minimalPolicy := fmt.Sprintf("# Minimal policy for %s\n", harnessName)
		if err := w.SCM.CommitFile(ctx, owner, repo, h.Policy,
			fmt.Sprintf("behaviour: add policy resource for %s", harnessName),
			[]byte(minimalPolicy)); err != nil {
			return fmt.Errorf("committing policy resource %s: %w", h.Policy, err)
		}
	}

	return nil
}

// waitForFileAccessible polls the Contents API until the file is readable,
// retrying briefly for CDN / commit propagation delays. This prevents the
// scenario from hanging silently when the raw URL returns 404 due to
// eventual consistency.
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
// verification. It can be overridden in tests to avoid real HTTP calls.
var rawHTTPClient = http.DefaultClient

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
