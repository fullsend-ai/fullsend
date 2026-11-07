package repos

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// jiraPollComponent is the ComponentAction component reported for the
// forge-specific managed Jira poll resources (#9076).
const jiraPollComponent = "jira-poll"

// jiraPollRequest is the input to a forge's jiraPollReconciler.
type jiraPollRequest struct {
	Client forge.Client
	Owner  string
	Repo   string
	// Jira is the repository's effective tracker.jira mapping. It is
	// non-nil and validated when polling is enabled (Ensure), and may be
	// nil when it is disabled (Remove).
	Jira *config.JiraTrackingConfig
	// EmailConfigured reports that FULLSEND_JIRA_EMAIL exists or is
	// written by this run, so the poll consumer passes the account email.
	// Only meaningful for Ensure.
	EmailConfigured bool
	// DryRun asks the reconciler to report the actions it would take
	// without any forge write.
	DryRun bool
	// Render pins the reusable workflows of a file-based poll resource
	// (the GitHub caller) to the ref the run's scaffold uses. Only
	// meaningful for Ensure.
	Render scaffold.RenderOptions
	// StageFile queues a repository file write or deletion for the run's
	// scaffold commit, so a file-based poll resource is delivered the same
	// way as the rest of the scaffold (direct push or pull request). It is
	// nil on a dry run.
	StageFile func(forge.TreeFile)
}

// jiraPollDelivery is how a run delivers file-based poll resources: the
// render options of its scaffold, and the function that queues a file for
// its scaffold commit (nil on a dry run).
type jiraPollDelivery struct {
	render scaffold.RenderOptions
	stage  func(forge.TreeFile)
}

// jiraPollReconciler manages the Fullsend-owned Jira poll resources of one
// forge: the scheduled caller workflow on GitHub (#9109) and the poll job
// and pipeline schedule on GitLab (#9108). The shared install behavior
// (credential secrets, the enable/disable decision, dry run) lives in
// convergeJiraPoll; a reconciler only converges its own resources and
// must touch nothing it does not own. Both methods must be idempotent.
type jiraPollReconciler interface {
	// Ensure makes the poll resources present and current.
	Ensure(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error)
	// Remove deletes the poll resources if present. It never deletes the
	// Jira credential secrets.
	Remove(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error)
}

// jiraPollReconcilers maps a forge name (ForgeGitHub, ForgeGitLab) to its
// reconciler. A forge without an entry has no managed poll resources yet:
// install still converges the credentials and reports that the resources
// are not managed.
var jiraPollReconcilers = map[string]jiraPollReconciler{
	ForgeGitHub: githubJiraPollReconciler{},
	ForgeGitLab: gitlabJiraPollReconciler{},
}

// jiraPollState is a repository's Jira polling state, read during
// discovery before any write.
type jiraPollState struct {
	// jira is the effective tracker.jira mapping when the selected
	// tracker provider is Jira, else nil.
	jira *config.JiraTrackingConfig
	// enabled reports that the effective tracker.jira.poll.enabled is
	// true: the poll resources must be present.
	enabled bool
	// tokenExists and emailExists report that FULLSEND_JIRA_TOKEN and
	// FULLSEND_JIRA_EMAIL existed before this run. Probed only when
	// enabled.
	tokenExists bool
	emailExists bool
	// tokenDefect names why an existing GitLab FULLSEND_JIRA_TOKEN
	// cannot be reused (see openAIKeyDefect); empty when it can.
	tokenDefect string
}

// effectiveJiraTracking returns the repository's effective tracker.jira
// mapping, the managed configuration layered over the declared preset (the
// same layers committed as the .fullsend files), or nil when the selected
// tracker provider is not Jira. When polling is enabled the connection
// settings are validated, so an incomplete mapping fails before any write.
// URL, project and query never come from flags or the environment.
func effectiveJiraTracking(resolved ResolvedConfig, preset []byte) (*config.JiraTrackingConfig, error) {
	layered, err := config.LayerOnBase(resolved.Managed, preset)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: reading the effective configuration: %w", resolved.Owner, resolved.Repo, err)
	}
	tracking := layered.IssueTracking()
	if tracking.Provider() != config.TrackingProviderJira {
		return nil, nil
	}
	if tracking.Jira.PollEnabled() {
		if err := tracking.Jira.ValidateConnection(); err != nil {
			return nil, fmt.Errorf("%s/%s: tracker.jira.poll.enabled is true: %w", resolved.Owner, resolved.Repo, err)
		}
	}
	return tracking.Jira, nil
}

// probeJiraPollSecrets reads whether the Jira credential secrets exist. It
// reads nothing when polling is disabled: the credentials are then left
// alone.
func probeJiraPollSecrets(ctx context.Context, client forge.Client, owner, repo, forgeName string, st jiraPollState) (jiraPollState, error) {
	if !st.enabled {
		return st, nil
	}
	prot, err := client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretJiraToken)
	if err != nil {
		return st, fmt.Errorf("checking secret %s: %w", forge.SecretJiraToken, err)
	}
	st.tokenExists = prot.Exists
	if forgeName == ForgeGitLab {
		st.tokenDefect = openAIKeyDefect(prot)
	}
	st.emailExists, err = client.RepoSecretExists(ctx, owner, repo, forge.SecretJiraEmail)
	if err != nil {
		return st, fmt.Errorf("checking secret %s: %w", forge.SecretJiraEmail, err)
	}
	return st, nil
}

// validateJiraPollInputs checks, before any write, that a repository with
// polling enabled will have a usable Jira token: an existing one, or one
// supplied with --jira-token, which is required on the first Jira-enabled
// install. Values are never included in errors.
func validateJiraPollInputs(d convergeDiscovery, cfg ConvergeConfig) error {
	if !d.jira.enabled {
		return nil
	}
	repo := d.repo.Owner + "/" + d.repo.Repo
	if cfg.JiraToken == "" {
		if !d.jira.tokenExists {
			return fmt.Errorf("%s: tracker.jira.poll.enabled is true but %s is missing; supply --jira-token on the first Jira-enabled install",
				repo, forge.SecretJiraToken)
		}
		if d.jira.tokenDefect != "" {
			return fmt.Errorf("%s: the existing %s CI/CD variable cannot be reused (%s): it must be a masked, protected environment variable available to all environments; fix it, or supply a replacement with --jira-token",
				repo, forge.SecretJiraToken, d.jira.tokenDefect)
		}
		return nil
	}
	if d.resolved.Forge == ForgeGitLab && !gitlabMaskableRe.MatchString(cfg.JiraToken) {
		return fmt.Errorf("%s: the Jira token cannot be stored as a masked GitLab CI/CD variable (at least 8 characters from A-Z a-z 0-9 _ + = / @ : . ~ -, no whitespace); check the --jira-token value",
			repo)
	}
	return nil
}

// jiraSecretValues returns the Jira secrets this run writes: each supplied
// flag's value. An omitted flag preserves the existing secret. Values are
// secrets and must never be logged.
func jiraSecretValues(cfg ConvergeConfig) map[string]string {
	values := map[string]string{}
	if cfg.JiraToken != "" {
		values[forge.SecretJiraToken] = cfg.JiraToken
	}
	if cfg.JiraEmail != "" {
		values[forge.SecretJiraEmail] = cfg.JiraEmail
	}
	return values
}

// convergeJiraPoll converges the repository's Jira polling to its effective
// tracker.jira.poll.enabled. Enabled: the supplied credential secrets are
// written (an omitted flag keeps the existing secret), then the forge's
// poll resources are ensured. Disabled or omitted: the forge's poll
// resources are removed, and the credentials are left untouched because
// other Jira consumers may still need them. File-based poll resources are
// delivered through delivery. A dry run writes nothing.
func convergeJiraPoll(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, delivery jiraPollDelivery, progress ProgressFunc) []ComponentAction {
	resolved := d.resolved
	repoFullName := resolved.Owner + "/" + resolved.Repo
	client := resolved.ForgeConfig.Client
	reconciler := jiraPollReconcilers[resolved.Forge]
	req := jiraPollRequest{
		Client: client,
		Owner:  resolved.Owner,
		Repo:   resolved.Repo,
		Jira:   d.jira.jira,
		DryRun: cfg.DryRun,
		Render: delivery.render,
	}
	if !cfg.DryRun {
		req.StageFile = delivery.stage
	}

	if !d.jira.enabled {
		if reconciler == nil {
			return nil
		}
		actions, err := reconciler.Remove(ctx, req)
		if err != nil {
			actions = append(actions, ComponentAction{
				Component: jiraPollComponent,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to remove Jira poll resources: %v", err),
			})
		}
		return actions
	}

	var actions []ComponentAction
	values := jiraSecretValues(cfg)
	exists := map[string]bool{
		forge.SecretJiraToken: d.jira.tokenExists,
		forge.SecretJiraEmail: d.jira.emailExists,
	}
	for _, name := range []string{forge.SecretJiraToken, forge.SecretJiraEmail} {
		value, supplied := values[name]
		if !supplied {
			if exists[name] {
				actions = append(actions, ComponentAction{
					Component: "secret:" + name,
					Action:    "none",
					Detail:    fmt.Sprintf("%s exists", name),
				})
			}
			continue
		}
		action := "add"
		if exists[name] {
			action = "update"
		}
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    action,
				Detail:    fmt.Sprintf("would %s %s", action, name),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would %s secret %s", action, name))
			continue
		}
		if err := client.CreateRepoSecret(ctx, resolved.Owner, resolved.Repo, name, value); err != nil {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to set %s: %s", name, redactSecretValues(err.Error(), values)),
			})
			// Poll resources are not ensured without their credentials.
			return actions
		}
		actions = append(actions, ComponentAction{
			Component: "secret:" + name,
			Action:    action,
			Detail:    fmt.Sprintf("set %s", name),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Set secret %s", name))
	}

	if reconciler == nil {
		detail := fmt.Sprintf("tracker.jira.poll.enabled is true, but this release does not manage Jira poll resources on %s yet", resolved.Forge)
		progress(repoFullName, "jira", detail)
		return append(actions, ComponentAction{
			Component: jiraPollComponent,
			Action:    "none",
			Detail:    detail,
		})
	}
	_, emailSupplied := values[forge.SecretJiraEmail]
	req.EmailConfigured = d.jira.emailExists || emailSupplied
	ensured, err := reconciler.Ensure(ctx, req)
	actions = append(actions, ensured...)
	if err != nil {
		actions = append(actions, ComponentAction{
			Component: jiraPollComponent,
			Action:    "error",
			Detail:    fmt.Sprintf("failed to ensure Jira poll resources: %s", redactSecretValues(err.Error(), values)),
		})
	}
	return actions
}
