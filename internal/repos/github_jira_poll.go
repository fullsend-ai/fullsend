package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	reusableJiraPollWorkflowPath = ".github/workflows/reusable-poll-jira.yml"
	reusableDispatchWorkflowPath = ".github/workflows/reusable-dispatch.yml"
)

// githubJiraPollReconciler manages the GitHub Jira poll resource (#9109):
// the scheduled caller workflow at scaffold.JiraPollCallerPath, which calls
// Fullsend's reusable Jira poll workflow and hands its dispatch matrix to
// reusable-dispatch.yml. It writes and deletes the caller only through the
// run's scaffold commit (jiraPollRequest.StageFile), and only when the file
// is absent or carries scaffold.JiraPollCallerMarker: a user-owned workflow
// at the same path is never touched.
type githubJiraPollReconciler struct{}

// checkGitHubJiraPollWorkflow verifies that the upstream ref the managed
// caller will use contains its reusable poll workflow. It runs before
// credential or scaffold writes. A user-owned caller is left to its owner
// and does not depend on this workflow.
func checkGitHubJiraPollWorkflow(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, refResolver *RefResolver) error {
	if !d.jira.enabled || d.resolved.Forge != ForgeGitHub {
		return nil
	}

	resolved := d.resolved
	caller, exists, err := readJiraPollCaller(ctx, jiraPollRequest{
		Client: resolved.ForgeConfig.Client,
		Owner:  resolved.Owner,
		Repo:   resolved.Repo,
	})
	if err != nil {
		return fmt.Errorf("%s/%s: checking the managed Jira poll caller before install: %w",
			resolved.Owner, resolved.Repo, err)
	}
	if exists && !scaffold.IsManagedJiraPollCaller(caller) {
		// The user-owned caller is never rewritten, so an OAuth install
		// would strand it on the token credentials it is about to remove.
		if !jiraUsesOAuth(d.jira, cfg) {
			return nil
		}
		uses, forwards := userJiraCallerOAuthUses(caller)
		if !forwards {
			return fmt.Errorf("%s/%s: %s is not managed by fullsend and does not forward the %s secrets required for Jira OAuth credentials; migrate it to pass them (or remove it to let repos install manage it) before switching to OAuth (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, scaffold.JiraPollCallerPath, strings.Join(jiraOAuthSecrets, "/"))
		}
		// Forwarding the secrets does not prove the reusable workflows the
		// caller actually pins declare them; check each before credentials
		// are replaced.
		return requireUserJiraCallerWorkflowsOAuth(ctx, resolved, refResolver, uses)
	}

	target := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
	ref, source := target.ref, "release-default ref"
	if target.manifestRef != "" {
		source = "pinned fullsend_ref"
	}
	if ref == "" {
		ref = config.DefaultUpstreamRef
	}
	if refResolver == nil {
		if target.manifestRef != "" {
			return fmt.Errorf("%s/%s: cannot verify pinned fullsend_ref %q contains %s without an upstream GitHub client; use a ref that includes the Jira poll workflow",
				resolved.Owner, resolved.Repo, target.manifestRef, reusableJiraPollWorkflowPath)
		}
		return nil
	}

	pollWorkflow, err := refResolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, reusableJiraPollWorkflowPath, ref)
	if err != nil {
		if forge.IsNotFound(err) {
			return fmt.Errorf("%s/%s: %s %s does not include %s required by Jira polling; upgrade fullsend_ref to a ref that includes it (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, source, ref, reusableJiraPollWorkflowPath)
		}
		return fmt.Errorf("%s/%s: checking %s at %s %s before install: %w",
			resolved.Owner, resolved.Repo, reusableJiraPollWorkflowPath, source, ref, err)
	}
	if !jiraUsesOAuth(d.jira, cfg) {
		return nil
	}

	// OAuth installs write the client credentials and the managed caller
	// forwards them by name. A workflow that does not declare them rejects
	// the caller, so verify both reusable workflows before any write.
	if err := requireJiraOAuthSecrets(resolved, source, ref, reusableJiraPollWorkflowPath, pollWorkflow); err != nil {
		return err
	}
	dispatchWorkflow, err := refResolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, reusableDispatchWorkflowPath, ref)
	if err != nil {
		if forge.IsNotFound(err) {
			return fmt.Errorf("%s/%s: %s %s does not include %s; upgrade fullsend_ref to a ref that supports Jira OAuth credentials (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, source, ref, reusableDispatchWorkflowPath)
		}
		return fmt.Errorf("%s/%s: checking %s at %s %s before install: %w",
			resolved.Owner, resolved.Repo, reusableDispatchWorkflowPath, source, ref, err)
	}
	return requireJiraOAuthSecrets(resolved, source, ref, reusableDispatchWorkflowPath, dispatchWorkflow)
}

// userJiraCallerOAuthUses returns the sorted, de-duplicated `uses` references
// of a user-owned Jira poll caller's jobs that call Fullsend's reusable poll or
// dispatch workflow, and reports whether every such job hands the OAuth client
// credentials to it: the job inherits all secrets or names both. A caller that
// does not parse, or has no such job, cannot be verified and reports false.
func userJiraCallerOAuthUses(caller []byte) ([]string, bool) {
	var wf struct {
		Jobs map[string]struct {
			Uses    string    `yaml:"uses"`
			Secrets yaml.Node `yaml:"secrets"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(caller, &wf); err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	var uses []string
	for _, job := range wf.Jobs {
		if !strings.Contains(job.Uses, "reusable-poll-jira.yml") && !strings.Contains(job.Uses, "reusable-dispatch.yml") {
			continue
		}
		if !jiraJobForwardsOAuth(job.Secrets) {
			return nil, false
		}
		if !seen[job.Uses] {
			seen[job.Uses] = true
			uses = append(uses, job.Uses)
		}
	}
	sort.Strings(uses)
	return uses, len(uses) > 0
}

// requireUserJiraCallerWorkflowsOAuth verifies that each reusable workflow a
// user-owned caller invokes declares the Jira OAuth client secrets, reading it
// at the ref the caller pins (or from the caller's own repository for a local
// `./` reference). A reference whose workflow cannot be read or located is
// rejected, since its OAuth support cannot be established.
func requireUserJiraCallerWorkflowsOAuth(ctx context.Context, resolved ResolvedConfig, refResolver *RefResolver, uses []string) error {
	for _, u := range uses {
		content, located, err := readUserJiraCallerWorkflow(ctx, resolved, refResolver, u)
		if !located {
			return fmt.Errorf("%s/%s: %s calls %q, whose Jira OAuth support cannot be verified; pin it to owner/repo/path@ref of a Fullsend version that supports OAuth credentials (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, scaffold.JiraPollCallerPath, u)
		}
		if err != nil {
			return fmt.Errorf("%s/%s: %s calls %q, whose Jira OAuth support could not be verified: %w (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, scaffold.JiraPollCallerPath, u, err)
		}
		if name := missingJiraOAuthSecret(content); name != "" {
			return fmt.Errorf("%s/%s: %s calls %q, which does not declare the %s secret required for Jira OAuth credentials; update the reference to a Fullsend version that supports them (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, scaffold.JiraPollCallerPath, u, name)
		}
	}
	return nil
}

// readUserJiraCallerWorkflow reads the reusable workflow a user-owned caller's
// `uses` names: at the ref it pins, or from the caller's own repository for a
// local `./` reference. located is false when the reference has no
// owner/repo/path@ref form or no upstream client can read it.
func readUserJiraCallerWorkflow(ctx context.Context, resolved ResolvedConfig, refResolver *RefResolver, uses string) (content []byte, located bool, err error) {
	if localPath, local := strings.CutPrefix(uses, "./"); local {
		content, err = resolved.ForgeConfig.Client.GetFileContent(ctx, resolved.Owner, resolved.Repo, localPath)
		return content, true, err
	}
	target, ref, hasRef := strings.Cut(uses, "@")
	parts := strings.SplitN(target, "/", 3)
	if !hasRef || len(parts) != 3 || refResolver == nil {
		return nil, false, nil
	}
	content, err = refResolver.client.GetFileContentAtRef(ctx, parts[0], parts[1], parts[2], ref)
	return content, true, err
}

// checkUserJiraCallerAuthorizationEnv fails when a user-owned Jira poll caller
// calls a reusable-poll-jira.yml that predates groups_from and accounts_from.
// That workflow installs the CLI at its own ref, and an older CLI cannot load
// the config, so every poll would fail. A vendored install runs its own
// binary there, so it is not checked. Neither is a reference that cannot be
// located.
func checkUserJiraCallerAuthorizationEnv(ctx context.Context, d convergeDiscovery, refResolver *RefResolver) error {
	resolved := d.resolved
	if resolved.Forge != ForgeGitHub || !d.jira.enabled || resolved.Vendor {
		return nil
	}
	caller, exists, err := readJiraPollCaller(ctx, jiraPollRequest{
		Client: resolved.ForgeConfig.Client,
		Owner:  resolved.Owner,
		Repo:   resolved.Repo,
	})
	if err != nil {
		return fmt.Errorf("%s/%s: checking %s for groups_from/accounts_from support: %w",
			resolved.Owner, resolved.Repo, scaffold.JiraPollCallerPath, err)
	}
	if !exists || scaffold.IsManagedJiraPollCaller(caller) {
		return nil
	}
	for _, u := range userJiraCallerPollUses(caller) {
		content, located, err := readUserJiraCallerWorkflow(ctx, resolved, refResolver, u)
		if !located {
			continue
		}
		if err != nil && !forge.IsNotFound(err) {
			return fmt.Errorf("%s/%s: checking %q, called by %s, for groups_from/accounts_from support: %w",
				resolved.Owner, resolved.Repo, u, scaffold.JiraPollCallerPath, err)
		}
		if err != nil || !bytes.Contains(content, []byte(jiraAuthorizationEnvMarker)) {
			return fmt.Errorf("%s/%s: config.yaml reads %s with groups_from/accounts_from, which %q, called by %s, does not support; update the reference to a release that includes them",
				resolved.Owner, resolved.Repo, strings.Join(d.jira.authEnv, ", "), u, scaffold.JiraPollCallerPath)
		}
	}
	return nil
}

// userJiraCallerPollUses returns the sorted, de-duplicated `uses` references
// of a user-owned caller's jobs that call the reusable Jira poll workflow. A
// caller that does not parse returns none.
func userJiraCallerPollUses(caller []byte) []string {
	var wf struct {
		Jobs map[string]struct {
			Uses string `yaml:"uses"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(caller, &wf); err != nil {
		return nil
	}
	var uses []string
	for _, job := range wf.Jobs {
		if strings.Contains(job.Uses, "reusable-poll-jira.yml") && !slices.Contains(uses, job.Uses) {
			uses = append(uses, job.Uses)
		}
	}
	sort.Strings(uses)
	return uses
}

// missingJiraOAuthSecret returns the first Jira OAuth client secret a workflow
// does not declare, or "" when it declares them all.
func missingJiraOAuthSecret(content []byte) string {
	for _, name := range jiraOAuthSecrets {
		if !bytes.Contains(content, []byte(name)) {
			return name
		}
	}
	return ""
}

// splitJiraAuthorizationEnv splits the variables the jira authorization
// lists read (#9132) into those the managed caller forwards to the poll
// job, in scaffold.JiraAuthorizationSecrets order, and the others, which
// it cannot pass.
func splitJiraAuthorizationEnv(names []string) (forwarded, unforwarded []string) {
	for _, name := range scaffold.JiraAuthorizationSecrets {
		if slices.Contains(names, name) {
			forwarded = append(forwarded, name)
		}
	}
	for _, name := range names {
		if !slices.Contains(scaffold.JiraAuthorizationSecrets, name) {
			unforwarded = append(unforwarded, name)
		}
	}
	return forwarded, unforwarded
}

// jiraJobForwardsOAuth reports whether a job's secrets node is `inherit` or a
// mapping that names every Jira OAuth client secret.
func jiraJobForwardsOAuth(secrets yaml.Node) bool {
	return len(jiraJobUnforwarded(secrets, jiraOAuthSecrets)) == 0
}

// jiraJobUnforwarded returns the names a job's secrets node does not pass to
// the workflow it calls: none for `inherit`, and for a mapping the names it
// leaves out. Any other node passes nothing.
func jiraJobUnforwarded(secrets yaml.Node, names []string) []string {
	named := map[string]bool{}
	switch secrets.Kind {
	case yaml.ScalarNode:
		if secrets.Value == "inherit" {
			return nil
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(secrets.Content); i += 2 {
			named[secrets.Content[i].Value] = true
		}
	}
	var missing []string
	for _, name := range names {
		if !named[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// userJiraCallerUnforwarded returns the names that a user-owned caller's
// jobs calling the reusable Jira poll workflow do not pass to it, and
// reports whether it has such a job. A caller that runs the poller itself,
// or does not parse, returns none: only its owner knows what its
// environment sets.
func userJiraCallerUnforwarded(caller []byte, names []string) (missing []string, calls bool) {
	var wf struct {
		Jobs map[string]struct {
			Uses    string    `yaml:"uses"`
			Secrets yaml.Node `yaml:"secrets"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(caller, &wf); err != nil {
		return nil, false
	}
	for _, job := range wf.Jobs {
		if !strings.Contains(job.Uses, "reusable-poll-jira.yml") {
			continue
		}
		calls = true
		for _, name := range jiraJobUnforwarded(job.Secrets, names) {
			if !slices.Contains(missing, name) {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(missing)
	return missing, calls
}

// requireJiraOAuthSecrets rejects a reusable workflow that does not declare
// the Jira OAuth client credential secrets.
func requireJiraOAuthSecrets(resolved ResolvedConfig, source, ref, workflowPath string, content []byte) error {
	if name := missingJiraOAuthSecret(content); name != "" {
		return fmt.Errorf("%s/%s: %s %s: %s does not declare the %s secret required for Jira OAuth credentials; upgrade fullsend_ref to a ref that supports them (existing Jira credentials were left unchanged)",
			resolved.Owner, resolved.Repo, source, ref, workflowPath, name)
	}
	return nil
}

// Ensure makes the managed caller present and current.
func (githubJiraPollReconciler) Ensure(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	forwarded, unforwarded := splitJiraAuthorizationEnv(req.AuthorizationEnv)
	want, err := scaffold.RenderJiraPollCaller(req.Render, req.EmailConfigured, req.OAuthConfigured, forwarded)
	if err != nil {
		return nil, err
	}
	have, exists, err := readJiraPollCaller(ctx, req)
	if err != nil {
		return nil, err
	}
	if exists && !scaffold.IsManagedJiraPollCaller(have) {
		// A reusable workflow gets only the secrets its caller passes, and
		// the poll workflow accepts only the fixed names.
		missing, calls := userJiraCallerUnforwarded(have, forwarded)
		if len(missing) > 0 && req.Warn != nil {
			req.Warn(fmt.Sprintf("authorization: jira groups_from/accounts_from reads %s, which %s does not pass to reusable-poll-jira.yml; add it to that job's secrets, or that list grants no one",
				strings.Join(missing, ", "), scaffold.JiraPollCallerPath))
		}
		if calls && len(unforwarded) > 0 && req.Warn != nil {
			req.Warn(fmt.Sprintf("authorization: jira groups_from/accounts_from reads %s, which reusable-poll-jira.yml does not pass to the poller; rename it to one of %s, or run the poller in your own job",
				strings.Join(unforwarded, ", "), strings.Join(scaffold.JiraAuthorizationSecrets, ", ")))
		}
		return []ComponentAction{{
			Component: jiraPollComponent,
			Action:    "none",
			Detail: fmt.Sprintf("%s exists but is not managed by fullsend; left it untouched (remove it to let repos install manage the Jira poll workflow)",
				scaffold.JiraPollCallerPath),
		}}, nil
	}
	// The managed caller passes only the fixed names. The poller finds any
	// other variable unset, so that list grants no one unless the runner
	// itself sets it.
	if len(unforwarded) > 0 && req.Warn != nil {
		req.Warn(fmt.Sprintf("authorization: jira groups_from/accounts_from reads %s, which the managed %s does not pass to the poller; rename it to one of %s, or set it in the runner's environment",
			strings.Join(unforwarded, ", "), scaffold.JiraPollCallerPath, strings.Join(scaffold.JiraAuthorizationSecrets, ", ")))
	}
	if exists && bytes.Equal(have, want) {
		return []ComponentAction{{
			Component: jiraPollComponent,
			Action:    "none",
			Detail:    scaffold.JiraPollCallerPath + " is current",
		}}, nil
	}
	action := "add"
	if exists {
		action = "update"
	}
	if req.DryRun {
		return []ComponentAction{{
			Component: jiraPollComponent,
			Action:    action,
			Detail:    fmt.Sprintf("would %s %s", action, scaffold.JiraPollCallerPath),
		}}, nil
	}
	if req.StageFile == nil {
		return nil, errors.New("internal error: no scaffold commit to deliver " + scaffold.JiraPollCallerPath)
	}
	req.StageFile(forge.TreeFile{Path: scaffold.JiraPollCallerPath, Content: want, Mode: "100644"})
	return []ComponentAction{{
		Component: jiraPollComponent,
		Action:    action,
		Detail:    fmt.Sprintf("%s %s with the scaffold", action, scaffold.JiraPollCallerPath),
	}}, nil
}

// Remove deletes the managed caller if present. An absent or user-owned
// file reports nothing, so repositories that never enabled Jira polling see
// no action.
func (githubJiraPollReconciler) Remove(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	have, exists, err := readJiraPollCaller(ctx, req)
	if err != nil {
		return nil, err
	}
	if !exists || !scaffold.IsManagedJiraPollCaller(have) {
		return nil, nil
	}
	if req.DryRun {
		return []ComponentAction{{
			Component: jiraPollComponent,
			Action:    "delete",
			Detail:    "would delete " + scaffold.JiraPollCallerPath,
		}}, nil
	}
	if req.StageFile == nil {
		return nil, errors.New("internal error: no scaffold commit to delete " + scaffold.JiraPollCallerPath)
	}
	req.StageFile(forge.TreeFile{Path: scaffold.JiraPollCallerPath, Delete: true})
	return []ComponentAction{{
		Component: jiraPollComponent,
		Action:    "delete",
		Detail:    "delete " + scaffold.JiraPollCallerPath + " with the scaffold",
	}}, nil
}

// readJiraPollCaller reads the caller from the default branch. A missing
// file is not an error.
func readJiraPollCaller(ctx context.Context, req jiraPollRequest) ([]byte, bool, error) {
	content, err := req.Client.GetFileContent(ctx, req.Owner, req.Repo, scaffold.JiraPollCallerPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading %s: %w", scaffold.JiraPollCallerPath, err)
	}
	return content, true, nil
}
