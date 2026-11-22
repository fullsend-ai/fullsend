package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// forgejoManagedConfigPath is the Fullsend-managed file the Forgejo
// lifecycle converges in each declared repository.
const forgejoManagedConfigPath = ".fullsend/config.yaml"

// ForgejoFileClient is the subset of forge.Client the Forgejo repository
// lifecycle uses. Its methods have the forge.Client signatures.
type ForgejoFileClient interface {
	GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error)
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error
	DeleteFile(ctx context.Context, owner, repo, path, message string) error
}

// ForgejoClientFunc returns the API client for an instance using token.
type ForgejoClientFunc func(baseURL, token string) (ForgejoFileClient, error)

// ResolveForgejoCredentials returns the API token of every instance the
// targets use, keyed by alias. Only selected instances are looked up, each
// from its own ForgejoTokenEnvVar variable; one alias's token is never
// used for another. Every missing token is reported together, before any
// forge call, so a missing credential cannot leave a partial install.
func ResolveForgejoCredentials(targets []ForgejoTarget, lookup func(string) (string, bool)) (map[string]string, error) {
	tokens := make(map[string]string)
	var missing []string
	for _, t := range targets {
		if _, done := tokens[t.Alias]; done {
			continue
		}
		env := ForgejoTokenEnvVar(t.Alias)
		tok, ok := lookup(env)
		tok = strings.TrimSpace(tok)
		if !ok || tok == "" {
			tokens[t.Alias] = ""
			missing = append(missing, fmt.Sprintf("%s (set %s)", t.Alias, env))
			continue
		}
		tokens[t.Alias] = tok
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing Forgejo API credentials for instance %s; nothing was changed. "+
			"Export a Forgejo access token for each selected instance; tokens are never read from repos.yaml",
			strings.Join(missing, ", "))
	}
	return tokens, nil
}

// ForgejoState is the state of a declared repository's managed resources.
type ForgejoState string

// Forgejo repository states reported by status and returned by install and
// uninstall.
const (
	ForgejoStateNotInstalled ForgejoState = "not-installed"
	ForgejoStateUpToDate     ForgejoState = "up-to-date"
	ForgejoStateDrift        ForgejoState = "drift"
	// ForgejoStateUnmanaged is a .fullsend/config.yaml without the
	// Fullsend ownership marker; the lifecycle never overwrites or
	// deletes it.
	ForgejoStateUnmanaged ForgejoState = "unmanaged"
	ForgejoStateInstalled ForgejoState = "installed"
	ForgejoStateUpgraded  ForgejoState = "upgraded"
	ForgejoStateRemoved   ForgejoState = "removed"
	ForgejoStateError     ForgejoState = "error"
)

// ForgejoRepoResult is the outcome for one Forgejo repository.
type ForgejoRepoResult struct {
	Selector string       `json:"selector"`
	Alias    string       `json:"alias"`
	Repo     string       `json:"repo"`
	State    ForgejoState `json:"state"`
	Detail   string       `json:"detail,omitempty"`
	Err      error        `json:"-"`
}

// ForgejoLifecycle runs repos install, status, and uninstall for declared
// Forgejo repositories.
type ForgejoLifecycle struct {
	Manifest  *Manifest
	NewClient ForgejoClientFunc
	// Lookup reads an environment variable (os.LookupEnv in production).
	Lookup func(string) (string, bool)
	DryRun bool
}

// desiredForgejoConfig renders the managed .fullsend/config.yaml for a
// Forgejo repository: the ownership marker and defaults.config.
func (l *ForgejoLifecycle) desiredForgejoConfig() ([]byte, error) {
	data, _, err := desiredManagedConfig(ResolvedConfig{
		ConfigManaged:  true,
		Managed:        l.Manifest.mergeManagedConfig(nil, RepoEntry{}),
		ManifestSource: l.Manifest.sourceURL,
	})
	if err != nil {
		return nil, fmt.Errorf("rendering managed config: %w", err)
	}
	return data, nil
}

// clients resolves credentials for the targets' instances, then builds one
// client per instance. No forge call happens here.
func (l *ForgejoLifecycle) clients(targets []ForgejoTarget) (map[string]ForgejoFileClient, error) {
	tokens, err := ResolveForgejoCredentials(targets, l.Lookup)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ForgejoFileClient, len(tokens))
	for _, t := range targets {
		if _, ok := out[t.Alias]; ok {
			continue
		}
		c, err := l.NewClient(t.BaseURL, tokens[t.Alias])
		if err != nil {
			return nil, fmt.Errorf("forgejo instance %q: %w", t.Alias, err)
		}
		out[t.Alias] = c
	}
	return out, nil
}

// inspect reads the managed file and classifies it against desired.
func inspectForgejo(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, desired []byte) (ForgejoState, error) {
	existing, err := c.GetFileContent(ctx, t.Owner, t.Repo, forgejoManagedConfigPath)
	switch {
	case forge.IsNotFound(err):
		return ForgejoStateNotInstalled, nil
	case err != nil:
		return ForgejoStateError, err
	case !hasManagedConfigMarker(existing):
		return ForgejoStateUnmanaged, nil
	case managedConfigEquivalent(existing, desired):
		return ForgejoStateUpToDate, nil
	default:
		return ForgejoStateDrift, nil
	}
}

func newForgejoResult(t ForgejoTarget) ForgejoRepoResult {
	return ForgejoRepoResult{Selector: t.Selector(), Alias: t.Alias, Repo: t.Path()}
}

func (r *ForgejoRepoResult) fail(err error) {
	r.State = ForgejoStateError
	r.Err = err
	r.Detail = err.Error()
}

// Status reports drift for each target without writing to Forgejo.
func (l *ForgejoLifecycle) Status(ctx context.Context, targets []ForgejoTarget) ([]ForgejoRepoResult, error) {
	desired, err := l.desiredForgejoConfig()
	if err != nil {
		return nil, err
	}
	clients, err := l.clients(targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := newForgejoResult(t)
		state, err := inspectForgejo(ctx, clients[t.Alias], t, desired)
		if err != nil {
			r.fail(err)
		} else {
			r.State = state
			r.Detail = forgejoStatusDetail(state)
		}
		results = append(results, r)
	}
	return results, nil
}

func forgejoStatusDetail(s ForgejoState) string {
	switch s {
	case ForgejoStateNotInstalled:
		return forgejoManagedConfigPath + " is missing; run repos install"
	case ForgejoStateDrift:
		return forgejoManagedConfigPath + " differs from the manifest; run repos install"
	case ForgejoStateUnmanaged:
		return forgejoManagedConfigPath + " exists without the Fullsend ownership marker; it is left unchanged"
	default:
		return ""
	}
}

// Install converges each target: it creates a missing managed file and
// upgrades a drifted one, and leaves an up-to-date or unmanaged file
// unchanged. Running it twice makes no change the second time.
func (l *ForgejoLifecycle) Install(ctx context.Context, targets []ForgejoTarget) ([]ForgejoRepoResult, error) {
	desired, err := l.desiredForgejoConfig()
	if err != nil {
		return nil, err
	}
	clients, err := l.clients(targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := newForgejoResult(t)
		c := clients[t.Alias]
		state, err := inspectForgejo(ctx, c, t, desired)
		switch {
		case err != nil:
			r.fail(err)
		case state == ForgejoStateUpToDate:
			r.State = ForgejoStateUpToDate
		case state == ForgejoStateUnmanaged:
			r.fail(fmt.Errorf("%s exists without the Fullsend ownership marker; move or delete it, then rerun repos install", forgejoManagedConfigPath))
		default:
			next, msg := ForgejoStateInstalled, "chore: install fullsend managed config"
			if state == ForgejoStateDrift {
				next, msg = ForgejoStateUpgraded, "chore: update fullsend managed config"
			}
			if !l.DryRun {
				if err := c.CreateOrUpdateFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, msg, desired); err != nil {
					r.fail(fmt.Errorf("writing %s: %w", forgejoManagedConfigPath, err))
					break
				}
			}
			r.State = next
		}
		results = append(results, r)
	}
	return results, joinForgejoErrors(results)
}

// Uninstall removes each target's Fullsend-managed file. A file without
// the ownership marker is never deleted.
func (l *ForgejoLifecycle) Uninstall(ctx context.Context, targets []ForgejoTarget) ([]ForgejoRepoResult, error) {
	clients, err := l.clients(targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := newForgejoResult(t)
		c := clients[t.Alias]
		existing, err := c.GetFileContent(ctx, t.Owner, t.Repo, forgejoManagedConfigPath)
		switch {
		case forge.IsNotFound(err):
			r.State = ForgejoStateNotInstalled
		case err != nil:
			r.fail(err)
		case !hasManagedConfigMarker(existing):
			r.State = ForgejoStateUnmanaged
			r.Detail = forgejoStatusDetail(ForgejoStateUnmanaged)
		default:
			if !l.DryRun {
				if err := c.DeleteFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, "chore: remove fullsend managed config"); err != nil && !forge.IsNotFound(err) {
					r.fail(fmt.Errorf("removing %s: %w", forgejoManagedConfigPath, err))
					break
				}
			}
			r.State = ForgejoStateRemoved
		}
		results = append(results, r)
	}
	return results, joinForgejoErrors(results)
}

func joinForgejoErrors(results []ForgejoRepoResult) error {
	var errs []error
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Selector, r.Err))
		}
	}
	return errors.Join(errs...)
}
