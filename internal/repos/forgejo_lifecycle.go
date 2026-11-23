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
	// GetFileAt reads path at ref (a commit SHA or tag).
	GetFileAt(ctx context.Context, owner, repo, path, ref string) (*ForgejoFile, error)
	// ChangeFiles applies changes to the default branch in one commit and
	// returns its SHA. Updates and deletes are conditional on each
	// change's SHA; any conflict rejects the whole commit.
	ChangeFiles(ctx context.Context, owner, repo, message string, changes []ForgejoFileChange) (string, error)
	// GetTag returns the commit SHA tag points at.
	GetTag(ctx context.Context, owner, repo, tag string) (string, error)
	// CreateTag tags target (a commit SHA, or the default branch when
	// empty) and returns the commit SHA the new tag points at.
	CreateTag(ctx context.Context, owner, repo, tag, target, message string) (string, error)
}

// Forgejo file change operations.
const (
	ForgejoFileCreate = "create"
	ForgejoFileUpdate = "update"
	ForgejoFileDelete = "delete"
)

// ForgejoFileChange is one file operation of a ChangeFiles commit.
type ForgejoFileChange struct {
	Operation string
	Path      string
	Content   []byte
	// SHA is the revision read before the change; required for update
	// and delete.
	SHA string
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
	// ForgejoStateUnmanaged is a managed path (.fullsend/config.yaml or a
	// workflow file) that Fullsend does not own; the lifecycle never
	// overwrites or deletes it.
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
	Role     string       `json:"role,omitempty"`
	State    ForgejoState `json:"state"`
	Detail   string       `json:"detail,omitempty"`
	Err      error        `json:"-"`
	// WorkflowsKept is set by uninstall when the repository's workflow
	// files (and so its shim's call to the host) were left in place. Callers
	// must keep such a repository declared in repos.yaml.
	WorkflowsKept bool `json:"-"`
}

// ForgejoLifecycle runs repos install, status, and uninstall for declared
// Forgejo repositories.
//
// Each instance's workflow host receives the versioned workflow bundle,
// tagged by version; each non-vendored repository receives a shim pinned
// to the tagged commit on its own instance; a vendored repository receives
// the bundle and a shim calling its local copy. Every non-host repository
// also receives the managed .fullsend/config.yaml.
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
	// Bundle overrides the workflow bundle embedded in this build.
	Bundle *ForgejoBundle
}

// bundle returns the workflow bundle to publish.
func (l *ForgejoLifecycle) bundle() (ForgejoBundle, error) {
	if l.Bundle != nil {
		return *l.Bundle, nil
	}
	return LoadForgejoBundle()
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

// requireForgejoHosts fails, before any credential lookup or forge call,
// when a selected non-vendored repository's instance declares no workflow
// host: a shim must never reference a workflow on another instance.
func (l *ForgejoLifecycle) requireForgejoHosts(targets []ForgejoTarget) error {
	var missing []string
	seen := make(map[string]bool)
	for _, t := range targets {
		if l.Manifest.forgejoRoleOf(t) != forgejoRoleConsumer {
			continue
		}
		if _, ok := l.Manifest.forgejoHost(t.Alias); ok || seen[strings.ToLower(t.Alias)] {
			continue
		}
		seen[strings.ToLower(t.Alias)] = true
		missing = append(missing, fmt.Sprintf("%q (needed by %s)", t.Alias, t.Selector()))
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("no same-instance workflow host is configured for Forgejo instance %s; nothing was changed. "+
		"Declare the instance's workflow-host repository with workflow_host: true, or set vendor: true on each repository that should carry its own copy of the workflows",
		strings.Join(missing, ", "))
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

func (l *ForgejoLifecycle) newResult(t ForgejoTarget) ForgejoRepoResult {
	r := ForgejoRepoResult{Selector: t.Selector(), Alias: t.Alias, Repo: t.Path()}
	if role := l.Manifest.forgejoRoleOf(t); role != forgejoRoleConsumer {
		r.Role = string(role)
	}
	return r
}

func (r *ForgejoRepoResult) fail(err error) {
	r.State = ForgejoStateError
	r.Err = err
	r.Detail = err.Error()
}

// pinCache looks up each host's published bundle commit once per command.
type pinCache struct {
	clients map[string]ForgejoFileClient
	bundle  ForgejoBundle
	pins    map[string]forgejoPin
}

func (p *pinCache) get(ctx context.Context, host ForgejoTarget) forgejoPin {
	k := strings.ToLower(host.Selector())
	if pin, ok := p.pins[k]; ok {
		return pin
	}
	pin := lookupForgejoPin(ctx, p.clients[host.Alias], host, p.bundle)
	p.pins[k] = pin
	return pin
}

func (p *pinCache) set(host ForgejoTarget, pin forgejoPin) {
	p.pins[strings.ToLower(host.Selector())] = pin
}

// Status reports drift for each target without writing to Forgejo.
func (l *ForgejoLifecycle) Status(ctx context.Context, targets []ForgejoTarget) ([]ForgejoRepoResult, error) {
	desired, err := l.desiredForgejoConfig()
	if err != nil {
		return nil, err
	}
	bundle, err := l.bundle()
	if err != nil {
		return nil, err
	}
	clients, err := l.clients(ctx, targets)
	if err != nil {
		return nil, err
	}
	pins := &pinCache{clients: clients, bundle: bundle, pins: map[string]forgejoPin{}}
	results := make([]ForgejoRepoResult, 0, len(targets))
	for _, t := range targets {
		r := l.newResult(t)
		if l.Manifest.forgejoRoleOf(t) == forgejoRoleHost {
			l.hostStatus(ctx, clients[t.Alias], t, bundle, pins, &r)
		} else {
			l.repoStatus(ctx, clients[t.Alias], t, desired, bundle, pins, &r)
		}
		results = append(results, r)
	}
	return results, nil
}

// hostStatus classifies a workflow host's bundle files and version tag.
func (l *ForgejoLifecycle) hostStatus(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, bundle ForgejoBundle, pins *pinCache, r *ForgejoRepoResult) {
	want, err := desiredForgejoWorkflows(forgejoRoleHost, bundle, t, "")
	if err != nil {
		r.fail(err)
		return
	}
	plan, err := planForgejoWorkflows(ctx, c, t, want)
	if err != nil {
		r.fail(err)
		return
	}
	if plan.State == ForgejoStateUnmanaged {
		r.State, r.Detail = ForgejoStateUnmanaged, forgejoConflictError(plan.Conflict).Error()
		return
	}
	pin := pins.get(ctx, t)
	switch {
	case pin.Err != nil:
		r.fail(pin.Err)
	case plan.State == ForgejoStateUpToDate && pin.Published:
		r.State, r.Detail = ForgejoStateUpToDate, fmt.Sprintf("bundle %s at %s", bundle.Tag(), pin.SHA)
	case plan.State == ForgejoStateNotInstalled && !pin.Published:
		r.State, r.Detail = ForgejoStateNotInstalled, fmt.Sprintf("bundle %s is not published; run repos install", bundle.Tag())
	default:
		var parts []string
		if plan.State != ForgejoStateUpToDate {
			parts = append(parts, "workflow files differ from bundle "+bundle.Tag())
		}
		if !pin.Published {
			parts = append(parts, "bundle "+bundle.Tag()+" is not tagged")
		}
		r.State, r.Detail = ForgejoStateDrift, joinDetails(parts...)+"; run repos install"
	}
}

// repoStatus classifies a consumer's or vendored repository's managed
// config and workflow files.
func (l *ForgejoLifecycle) repoStatus(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, desired []byte, bundle ForgejoBundle, pins *pinCache, r *ForgejoRepoResult) {
	cfgState, _, err := inspectForgejo(ctx, c, t, desired)
	if err != nil {
		r.fail(err)
		return
	}
	cfgDetail := forgejoStatusDetail(cfgState)
	wfState, wfDetail, err := l.workflowStatus(ctx, c, t, bundle, pins)
	if err != nil {
		r.fail(err)
		return
	}
	r.State = combineForgejoStatus(cfgState, wfState)
	r.Detail = joinDetails(cfgDetail, wfDetail)
}

// workflowStatus classifies a non-host repository's workflow files.
func (l *ForgejoLifecycle) workflowStatus(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, bundle ForgejoBundle, pins *pinCache) (ForgejoState, string, error) {
	role := l.Manifest.forgejoRoleOf(t)
	var host ForgejoTarget
	pinSHA := ""
	if role == forgejoRoleConsumer {
		var ok bool
		host, ok = l.Manifest.forgejoHost(t.Alias)
		if !ok {
			return "", "", fmt.Errorf("no same-instance workflow host is configured for Forgejo instance %q; declare one with workflow_host: true, or set vendor: true", t.Alias)
		}
		pin := pins.get(ctx, host)
		if pin.Err != nil {
			return "", "", pin.Err
		}
		if !pin.Published {
			_, ours, _, err := readForgejoIndex(ctx, c, t)
			if err != nil {
				return "", "", err
			}
			state := ForgejoStateDrift
			if !ours {
				state = ForgejoStateNotInstalled
			}
			return state, fmt.Sprintf("bundle %s is not published on %s; run repos install %s", bundle.Tag(), host.Selector(), host.Selector()), nil
		}
		pinSHA = pin.SHA
	}
	want, err := desiredForgejoWorkflows(role, bundle, host, pinSHA)
	if err != nil {
		return "", "", err
	}
	plan, err := planForgejoWorkflows(ctx, c, t, want)
	if err != nil {
		return "", "", err
	}
	switch plan.State {
	case ForgejoStateUnmanaged:
		return plan.State, forgejoConflictError(plan.Conflict).Error(), nil
	case ForgejoStateNotInstalled:
		return plan.State, "workflow files are missing; run repos install", nil
	case ForgejoStateDrift:
		return plan.State, "workflow files differ from bundle " + bundle.Tag() + "; run repos install", nil
	}
	return plan.State, "", nil
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

// Install converges each target: it publishes each selected workflow host's
// bundle, then creates missing managed files and upgrades drifted ones in
// the other repositories, and leaves up-to-date files unchanged. Running it
// twice makes no change the second time. A non-vendored repository whose
// instance has no workflow host fails the whole install before any write.
func (l *ForgejoLifecycle) Install(ctx context.Context, targets []ForgejoTarget) ([]ForgejoRepoResult, error) {
	desired, err := l.desiredForgejoConfig()
	if err != nil {
		return nil, err
	}
	bundle, err := l.bundle()
	if err != nil {
		return nil, err
	}
	if err := l.requireForgejoHosts(targets); err != nil {
		return nil, err
	}
	clients, err := l.clients(ctx, targets)
	if err != nil {
		return nil, err
	}
	pins := &pinCache{clients: clients, bundle: bundle, pins: map[string]forgejoPin{}}
	results := make([]ForgejoRepoResult, len(targets))
	// Hosts publish first so consumers in the same run pin the new bundle.
	for i, t := range targets {
		if l.Manifest.forgejoRoleOf(t) == forgejoRoleHost {
			results[i] = l.installHost(ctx, clients[t.Alias], t, bundle, pins)
		}
	}
	for i, t := range targets {
		if l.Manifest.forgejoRoleOf(t) != forgejoRoleHost {
			results[i] = l.installRepo(ctx, clients[t.Alias], t, desired, bundle, pins)
		}
	}
	return results, joinForgejoErrors(results)
}

// installHost publishes the bundle to a workflow host in one commit, tags
// the commit with the bundle version, and records the tagged commit for
// the consumers that follow.
func (l *ForgejoLifecycle) installHost(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, bundle ForgejoBundle, pins *pinCache) ForgejoRepoResult {
	r := l.newResult(t)
	fail := func(err error) ForgejoRepoResult {
		r.fail(err)
		pins.set(t, forgejoPin{Err: fmt.Errorf("workflow host %s failed: %w", t.Selector(), err)})
		return r
	}
	want, err := desiredForgejoWorkflows(forgejoRoleHost, bundle, t, "")
	if err != nil {
		return fail(err)
	}
	plan, err := planForgejoWorkflows(ctx, c, t, want)
	if err != nil {
		return fail(err)
	}
	if plan.State == ForgejoStateUnmanaged {
		return fail(forgejoConflictError(plan.Conflict))
	}
	pin := lookupForgejoPin(ctx, c, t, bundle)
	if pin.Err != nil {
		return fail(pin.Err)
	}
	r.State = installOutcome(plan.State)
	if !pin.Published && r.State == ForgejoStateUpToDate {
		// The files are current but the version tag is missing.
		r.State = ForgejoStateUpgraded
	}
	if l.DryRun {
		if !pin.Published {
			pin.Pending = true
		}
		pins.set(t, pin)
		r.Detail = "would publish bundle " + bundle.Tag()
		if pin.Published && len(plan.Changes) == 0 {
			r.Detail = fmt.Sprintf("bundle %s at %s", bundle.Tag(), pin.SHA)
		}
		return r
	}
	commit := ""
	if len(plan.Changes) > 0 {
		commit, err = c.ChangeFiles(ctx, t.Owner, t.Repo, "chore: publish fullsend workflow bundle "+bundle.Tag(), plan.Changes)
		if err != nil {
			return fail(fmt.Errorf("publishing bundle %s: %w", bundle.Tag(), err))
		}
	}
	if !pin.Published {
		// An empty target tags the default branch, which already holds
		// the current files when nothing had to be committed.
		sha, err := c.CreateTag(ctx, t.Owner, t.Repo, bundle.Tag(), commit, "fullsend workflow bundle "+bundle.Version)
		if err != nil {
			return fail(fmt.Errorf("tagging bundle %s: %w", bundle.Tag(), err))
		}
		// Verify the whole bundle at the new tag: a concurrent commit to
		// a file outside plan.Changes must not be published as this version.
		if err := verifyForgejoBundleAt(ctx, c, t, sha, bundle); err != nil {
			return fail(err)
		}
		pin = forgejoPin{SHA: sha, Published: true}
	}
	pins.set(t, pin)
	r.Detail = fmt.Sprintf("bundle %s at %s", bundle.Tag(), pin.SHA)
	return r
}

// installRepo converges a consumer's or vendored repository's managed
// config and workflow files. Every read and ownership check happens before
// the first write, so a conflict leaves the repository unchanged.
func (l *ForgejoLifecycle) installRepo(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, desired []byte, bundle ForgejoBundle, pins *pinCache) ForgejoRepoResult {
	r := l.newResult(t)
	role := l.Manifest.forgejoRoleOf(t)
	var host ForgejoTarget
	pinSHA := ""
	if role == forgejoRoleConsumer {
		host, _ = l.Manifest.forgejoHost(t.Alias)
		pin := pins.get(ctx, host)
		switch {
		case pin.Err != nil:
			r.fail(pin.Err)
			return r
		case pin.Pending:
			pinSHA = forgejoPendingPinPrefix + bundle.Tag() + ">"
		case !pin.Published:
			r.fail(fmt.Errorf("bundle %s is not published on workflow host %s; run repos install %s first",
				bundle.Tag(), host.Selector(), host.Selector()))
			return r
		default:
			pinSHA = pin.SHA
		}
	}
	cfgState, cfgSHA, err := inspectForgejo(ctx, c, t, desired)
	if err != nil {
		r.fail(err)
		return r
	}
	if cfgState == ForgejoStateUnmanaged {
		r.fail(fmt.Errorf("%s exists without the Fullsend ownership marker; move or delete it, then rerun repos install", forgejoManagedConfigPath))
		return r
	}
	want, err := desiredForgejoWorkflows(role, bundle, host, pinSHA)
	if err != nil {
		r.fail(err)
		return r
	}
	plan, err := planForgejoWorkflows(ctx, c, t, want)
	if err != nil {
		r.fail(err)
		return r
	}
	if plan.State == ForgejoStateUnmanaged {
		r.fail(forgejoConflictError(plan.Conflict))
		return r
	}
	if !l.DryRun {
		switch cfgState {
		case ForgejoStateDrift:
			err = c.UpdateFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, "chore: update fullsend managed config", desired, cfgSHA)
		case ForgejoStateNotInstalled:
			err = c.CreateFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, "chore: install fullsend managed config", desired)
		}
		if err != nil {
			r.fail(fmt.Errorf("writing %s: %w", forgejoManagedConfigPath, err))
			return r
		}
		if len(plan.Changes) > 0 {
			if _, err := c.ChangeFiles(ctx, t.Owner, t.Repo, "chore: converge fullsend workflows to bundle "+bundle.Tag(), plan.Changes); err != nil {
				r.fail(fmt.Errorf("writing workflow files: %w", err))
				return r
			}
		}
	}
	r.State = combineForgejoInstall(installOutcome(cfgState), installOutcome(plan.State))
	if role == forgejoRoleConsumer {
		r.Detail = "pinned to " + host.Selector() + "@" + pinSHA
	}
	return r
}

// Uninstall removes each target's Fullsend-managed files. A file Fullsend
// does not own is never deleted. A workflow host is removed only after
// every repository repos.yaml declares as its consumer has been removed in
// this run; otherwise it is refused and the remaining consumers are named.
func (l *ForgejoLifecycle) Uninstall(ctx context.Context, targets []ForgejoTarget) ([]ForgejoRepoResult, error) {
	clients, err := l.clients(ctx, targets)
	if err != nil {
		return nil, err
	}
	results := make([]ForgejoRepoResult, len(targets))
	removed := make(map[string]bool)
	for i, t := range targets {
		if l.Manifest.forgejoRoleOf(t) == forgejoRoleHost {
			continue
		}
		results[i] = l.uninstallRepo(ctx, clients[t.Alias], t)
		if results[i].Err == nil && !results[i].WorkflowsKept {
			removed[strings.ToLower(t.Selector())] = true
		}
	}
	for i, t := range targets {
		if l.Manifest.forgejoRoleOf(t) == forgejoRoleHost {
			results[i] = l.uninstallHost(ctx, clients[t.Alias], t, removed)
		}
	}
	return results, joinForgejoErrors(results)
}

// uninstallRepo removes a non-host repository's managed config and
// workflow files.
func (l *ForgejoLifecycle) uninstallRepo(ctx context.Context, c ForgejoFileClient, t ForgejoTarget) ForgejoRepoResult {
	r := l.newResult(t)
	existing, err := c.GetFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath)
	cfgState := ForgejoStateRemoved
	switch {
	case forge.IsNotFound(err):
		cfgState = ForgejoStateNotInstalled
	case err != nil:
		r.fail(err)
		return r
	case !hasManagedConfigMarker(existing.Content):
		cfgState = ForgejoStateUnmanaged
	}
	plan, err := planForgejoWorkflowRemoval(ctx, c, t)
	if err != nil {
		r.fail(err)
		return r
	}
	if !l.DryRun {
		if cfgState == ForgejoStateRemoved {
			if err := c.DeleteFile(ctx, t.Owner, t.Repo, forgejoManagedConfigPath, "chore: remove fullsend managed config", existing.SHA); err != nil && !forge.IsNotFound(err) {
				r.fail(fmt.Errorf("removing %s: %w", forgejoManagedConfigPath, err))
				return r
			}
		}
		if plan.State == ForgejoStateRemoved {
			if _, err := c.ChangeFiles(ctx, t.Owner, t.Repo, "chore: remove fullsend workflows", plan.Changes); err != nil {
				r.fail(fmt.Errorf("removing workflow files: %w", err))
				return r
			}
		}
	}
	var details []string
	if cfgState == ForgejoStateUnmanaged {
		details = append(details, forgejoStatusDetail(ForgejoStateUnmanaged))
	}
	if plan.State == ForgejoStateUnmanaged {
		r.WorkflowsKept = true
		details = append(details, plan.unmanagedDetail())
	}
	if plan.State == ForgejoStateRemoved && l.Manifest.forgejoRoleOf(t) == forgejoRoleConsumer && !plansDeleteOf(plan, forgejoShimPath) {
		// The index does not list the shim, so removal leaves it in place;
		// a consumer's shim still calls the host.
		if _, err := c.GetFile(ctx, t.Owner, t.Repo, forgejoShimPath); err == nil {
			r.WorkflowsKept = true
			details = append(details, forgejoShimPath+" is not listed in "+forgejoWorkflowIndexPath+" but still calls the workflow host; the shim is left unchanged")
		} else if !forge.IsNotFound(err) {
			r.fail(err)
			return r
		}
	}
	r.Detail = joinDetails(details...)
	switch {
	case cfgState == ForgejoStateRemoved || plan.State == ForgejoStateRemoved:
		r.State = ForgejoStateRemoved
	case cfgState == ForgejoStateUnmanaged || plan.State == ForgejoStateUnmanaged:
		r.State = ForgejoStateUnmanaged
	default:
		r.State = ForgejoStateNotInstalled
	}
	return r
}

// uninstallHost removes a workflow host's bundle files unless a declared
// consumer still depends on them. Version tags are kept, so a pinned
// commit stays reachable for any consumer repos.yaml does not declare.
func (l *ForgejoLifecycle) uninstallHost(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, removed map[string]bool) ForgejoRepoResult {
	r := l.newResult(t)
	var remaining []ForgejoTarget
	for _, d := range l.Manifest.forgejoDependents(t) {
		if !removed[strings.ToLower(d.Selector())] {
			remaining = append(remaining, d)
		}
	}
	if len(remaining) > 0 {
		r.fail(fmt.Errorf("workflow host %s still serves %d consumer(s) declared in repos.yaml: %s; uninstall them first. "+
			"Only consumers declared in repos.yaml can be checked", t.Selector(), len(remaining), joinSelectors(remaining)))
		return r
	}
	plan, err := planForgejoWorkflowRemoval(ctx, c, t)
	if err != nil {
		r.fail(err)
		return r
	}
	switch plan.State {
	case ForgejoStateNotInstalled:
		r.State = ForgejoStateNotInstalled
		return r
	case ForgejoStateUnmanaged:
		r.State, r.Detail = ForgejoStateUnmanaged, plan.unmanagedDetail()
		return r
	}
	if !l.DryRun {
		if _, err := c.ChangeFiles(ctx, t.Owner, t.Repo, "chore: remove fullsend workflow bundle", plan.Changes); err != nil {
			r.fail(fmt.Errorf("removing workflow bundle: %w", err))
			return r
		}
	}
	r.State = ForgejoStateRemoved
	r.Detail = "bundle files removed; version tags are kept"
	return r
}

// plansDeleteOf reports whether plan deletes path.
func plansDeleteOf(plan forgejoWorkflowPlan, path string) bool {
	for _, ch := range plan.Changes {
		if ch.Operation == ForgejoFileDelete && ch.Path == path {
			return true
		}
	}
	return false
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
