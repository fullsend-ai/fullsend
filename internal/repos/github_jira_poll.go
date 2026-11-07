package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	reusableJiraPollWorkflowPath = ".github/workflows/reusable-poll-jira.yml"
	reusableDispatchWorkflowPath = ".github/workflows/reusable-dispatch.yml"
)

// jiraCallerInheritsSecretsRe matches a `secrets: inherit` line.
var jiraCallerInheritsSecretsRe = regexp.MustCompile(`(?m)^\s*secrets:\s*inherit\s*(#.*)?$`)

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
		if jiraUsesOAuth(d.jira, cfg) && !userJiraCallerForwardsOAuth(caller) {
			return fmt.Errorf("%s/%s: %s is not managed by fullsend and does not forward the %s secrets required for Jira OAuth credentials; migrate it to pass them (or remove it to let repos install manage it) before switching to OAuth (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, scaffold.JiraPollCallerPath, strings.Join(jiraOAuthSecrets, "/"))
		}
		return nil
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

// userJiraCallerForwardsOAuth reports whether a user-owned Jira poll caller
// hands the OAuth client credentials to the reusable workflow: it names both
// secrets or inherits all of them.
func userJiraCallerForwardsOAuth(caller []byte) bool {
	if jiraCallerInheritsSecretsRe.Match(caller) {
		return true
	}
	for _, name := range jiraOAuthSecrets {
		if !bytes.Contains(caller, []byte(name)) {
			return false
		}
	}
	return true
}

// requireJiraOAuthSecrets rejects a reusable workflow that does not declare
// the Jira OAuth client credential secrets.
func requireJiraOAuthSecrets(resolved ResolvedConfig, source, ref, workflowPath string, content []byte) error {
	for _, name := range jiraOAuthSecrets {
		if !bytes.Contains(content, []byte(name)) {
			return fmt.Errorf("%s/%s: %s %s: %s does not declare the %s secret required for Jira OAuth credentials; upgrade fullsend_ref to a ref that supports them (existing Jira credentials were left unchanged)",
				resolved.Owner, resolved.Repo, source, ref, workflowPath, name)
		}
	}
	return nil
}

// Ensure makes the managed caller present and current.
func (githubJiraPollReconciler) Ensure(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	want, err := scaffold.RenderJiraPollCaller(req.Render, req.EmailConfigured, req.OAuthConfigured)
	if err != nil {
		return nil, err
	}
	have, exists, err := readJiraPollCaller(ctx, req)
	if err != nil {
		return nil, err
	}
	if exists && !scaffold.IsManagedJiraPollCaller(have) {
		return []ComponentAction{{
			Component: jiraPollComponent,
			Action:    "none",
			Detail: fmt.Sprintf("%s exists but is not managed by fullsend; left it untouched (remove it to let repos install manage the Jira poll workflow)",
				scaffold.JiraPollCallerPath),
		}}, nil
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
