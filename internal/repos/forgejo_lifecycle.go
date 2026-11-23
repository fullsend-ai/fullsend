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

// ForgejoFile is a file's content and the SHA of the revision that holds it.
type ForgejoFile struct {
	Content []byte
	SHA     string
}

// ForgejoFileClient is the file API the Forgejo repository lifecycle uses.
// UpdateFile and DeleteFile take the SHA of the revision whose ownership
// marker was checked, so a file replaced after that read is not overwritten
// or deleted.
type ForgejoFileClient interface {
	GetFile(ctx context.Context, owner, repo, path string) (*ForgejoFile, error)
	CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error
	UpdateFile(ctx context.Context, owner, repo, path, message string, content []byte, sha string) error
	DeleteFile(ctx context.Context, owner, repo, path, message, sha string) error
}

// ForgejoClientFunc returns the API client for an instance using token.
type ForgejoClientFunc func(baseURL, token string) (ForgejoFileClient, error)

// forgejoOAuthScopeWarning states, in credential errors, the authority an
// OAuth login grants. The login consent prompt explains it separately.
const forgejoOAuthScopeWarning = "Forgejo does not implement OAuth scopes, so an OAuth token can act with your full Forgejo user permissions on that instance, not only on the selected repositories"

// ForgejoOAuthRequest identifies the instance and public OAuth client an
// OAuth token is for.
type ForgejoOAuthRequest struct {
	Alias    string
	BaseURL  string
	ClientID string
	// TokenEnvVar is the instance's ForgejoTokenEnvVar, named in prompts
	// as the scoped alternative to OAuth.
	TokenEnvVar string
}

// ForgejoOAuth is the OAuth fallback for instances without an environment
// token. Implementations never print or log token values.
type ForgejoOAuth interface {
	// CachedToken returns a usable cached token, refreshing it when it has
	// expired. When no cached token is usable it returns "" and a short
	// reason, and removes a cached token the instance rejected. An error
	// means the cache could not be checked (for example, the instance is
	// unreachable).
	CachedToken(ctx context.Context, req ForgejoOAuthRequest) (token, reason string, err error)
	// Login explains the OAuth authority and asks for consent, runs the
	// browser authorization, caches the token, and returns it. It is
	// called only for interactive commands.
	Login(ctx context.Context, req ForgejoOAuthRequest) (string, error)
}

// ForgejoCredentials resolves per-instance Forgejo API credentials.
type ForgejoCredentials struct {
	// Lookup reads an environment variable (os.LookupEnv in production).
	Lookup func(string) (string, bool)
	// OAuth is the OAuth fallback; nil disables it.
	OAuth ForgejoOAuth
	// Interactive allows a browser login when no token is available.
	Interactive bool
}

// Resolve returns the API token of every instance the targets use, keyed
// by alias. Only selected instances are resolved, each in this order: its
// own ForgejoTokenEnvVar variable; a cached OAuth token, when the instance
// configures an OAuth client ID; then, for interactive commands only, a
// browser login. One alias's token is never used for another.
//
// Every instance that cannot get a token without a login is reported
// together, before any login or forge write, so a missing credential cannot
// leave a partial install. Logins run only after every other selected
// instance has a token.
func (c ForgejoCredentials) Resolve(ctx context.Context, targets []ForgejoTarget) (map[string]string, error) {
	tokens := make(map[string]string)
	var missing []string
	var logins []ForgejoOAuthRequest
	oauthHint := false
	for _, t := range targets {
		if _, done := tokens[t.Alias]; done {
			continue
		}
		tokens[t.Alias] = ""
		env := ForgejoTokenEnvVar(t.Alias)
		if tok, ok := c.Lookup(env); ok && strings.TrimSpace(tok) != "" {
			tokens[t.Alias] = strings.TrimSpace(tok)
			continue
		}
		if t.ClientID == "" || c.OAuth == nil {
			missing = append(missing, fmt.Sprintf("%s (set %s)", t.Alias, env))
			oauthHint = oauthHint || t.ClientID == ""
			continue
		}
		req := ForgejoOAuthRequest{Alias: t.Alias, BaseURL: t.BaseURL, ClientID: t.ClientID, TokenEnvVar: env}
		tok, reason, err := c.OAuth.CachedToken(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("forgejo instance %q: checking the cached OAuth token: %w; nothing was changed", t.Alias, err)
		}
		if tok != "" {
			tokens[t.Alias] = tok
			continue
		}
		if !c.Interactive {
			missing = append(missing, fmt.Sprintf("%s (%s; set %s, or rerun in a terminal to log in with OAuth)", t.Alias, reason, env))
			continue
		}
		logins = append(logins, req)
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf("missing Forgejo API credentials for instance %s; nothing was changed. "+
			"Export a Forgejo access token for each selected instance; tokens are never read from repos.yaml",
			strings.Join(missing, ", "))
		if oauthHint {
			msg += ". To log in through a browser instead, set forgejo.instances[].oauth.client_id and run the command in a terminal; " +
				forgejoOAuthScopeWarning
		}
		return nil, errors.New(msg)
	}
	for _, req := range logins {
		tok, err := c.OAuth.Login(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("forgejo instance %q: OAuth login failed: %w; nothing was changed. Set %s to use an access token instead", req.Alias, err, req.TokenEnvVar)
		}
		tokens[req.Alias] = tok
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
	// OAuth is the OAuth credential fallback; nil disables it.
	OAuth ForgejoOAuth
	// Interactive allows a browser login for a selected instance that has
	// no usable token.
	Interactive bool
	DryRun      bool
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
// client per instance. No forge write happens here; checking a cached OAuth
// token may read from the instance.
func (l *ForgejoLifecycle) clients(ctx context.Context, targets []ForgejoTarget) (map[string]ForgejoFileClient, error) {
	tokens, err := ForgejoCredentials{Lookup: l.Lookup, OAuth: l.OAuth, Interactive: l.Interactive}.Resolve(ctx, targets)
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

// inspectForgejo reads the managed file and classifies it against desired.
// It also returns the SHA of the revision it classified, for the conditional
// write that follows.
func inspectForgejo(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, desired []byte) (ForgejoState, string, error) {
	existing, err := c.GetFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath)
	switch {
	case forge.IsNotFound(err):
		return ForgejoStateNotInstalled, "", nil
	case err != nil:
		return ForgejoStateError, "", err
	case !hasManagedConfigMarker(existing.Content):
		return ForgejoStateUnmanaged, existing.SHA, nil
	case managedConfigEquivalent(existing.Content, desired):
		return ForgejoStateUpToDate, existing.SHA, nil
	default:
		return ForgejoStateDrift, existing.SHA, nil
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
	clients, err := l.clients(ctx, targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := newForgejoResult(t)
		state, _, err := inspectForgejo(ctx, clients[t.Alias], t, desired)
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
	clients, err := l.clients(ctx, targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := newForgejoResult(t)
		c := clients[t.Alias]
		state, sha, err := inspectForgejo(ctx, c, t, desired)
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
				var werr error
				if state == ForgejoStateDrift {
					werr = c.UpdateFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, msg, desired, sha)
				} else {
					werr = c.CreateFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, msg, desired)
				}
				if werr != nil {
					r.fail(fmt.Errorf("writing %s: %w", forgejoManagedConfigPath, werr))
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
	clients, err := l.clients(ctx, targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := newForgejoResult(t)
		c := clients[t.Alias]
		existing, err := c.GetFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath)
		switch {
		case forge.IsNotFound(err):
			r.State = ForgejoStateNotInstalled
		case err != nil:
			r.fail(err)
		case !hasManagedConfigMarker(existing.Content):
			r.State = ForgejoStateUnmanaged
			r.Detail = forgejoStatusDetail(ForgejoStateUnmanaged)
		default:
			if !l.DryRun {
				if err := c.DeleteFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, "chore: remove fullsend managed config", existing.SHA); err != nil && !forge.IsNotFound(err) {
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
