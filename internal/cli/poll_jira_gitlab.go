package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/internal/urlutil"
)

// jiraPollGitLabFlags holds the raw flag values for the jira-poll driver
// with --target-forge gitlab.
type jiraPollGitLabFlags struct {
	jiraURL     string
	jiraProject string
	jqlOverride string
	targetRepo  string
	projectPath string
	gitlabURL   string
	outputPath  string
	fullsendDir string
}

// validGitLabProjectPath matches a full GitLab project path: a namespace
// with any number of nested groups, then the project. Each segment starts
// with a letter, digit, or underscore and holds only letters, digits,
// underscores, dots, and hyphens, as GitLab requires.
var validGitLabProjectPath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)+$`)

// validateJiraGitLabPollArgs resolves env-var fallbacks and validates the
// jira-poll arguments for a GitLab target (#8919). The project path comes
// from --project or CI_PROJECT_PATH and keeps every nested group; the
// GitLab URL must be an https URL and is returned without a trailing
// slash. --target-repo is rejected because it names a GitHub repository.
// --output is optional: GitLab dispatches are scheduled directly.
func validateJiraGitLabPollArgs(f jiraPollGitLabFlags, getenv func(string) string) (jiraPollArgs, string, error) {
	jiraURL := f.jiraURL
	if jiraURL == "" {
		jiraURL = getenv("JIRA_BASE_URL")
	}
	if jiraURL == "" {
		return jiraPollArgs{}, "", fmt.Errorf("--jira-url or JIRA_BASE_URL is required")
	}

	if f.targetRepo != "" {
		return jiraPollArgs{}, "", fmt.Errorf("--target-repo names a GitHub repository; use --project with --target-forge gitlab")
	}
	projectPath := f.projectPath
	if projectPath == "" {
		projectPath = getenv("CI_PROJECT_PATH")
	}
	if projectPath == "" {
		return jiraPollArgs{}, "", fmt.Errorf("--project or CI_PROJECT_PATH is required for --target-forge gitlab")
	}
	if !validGitLabProjectPath.MatchString(projectPath) || strings.Contains(projectPath, "..") {
		return jiraPollArgs{}, "", fmt.Errorf("--project %q must be a full GitLab project path such as group/subgroup/project", projectPath)
	}

	gitlabURL, err := normalizeJiraGitLabURL(f.gitlabURL)
	if err != nil {
		return jiraPollArgs{}, "", err
	}

	if f.jiraProject == "" && f.jqlOverride == "" {
		return jiraPollArgs{}, "", fmt.Errorf("--jira-project or --jql is required")
	}

	return jiraPollArgs{
		jiraURL:     jiraURL,
		jiraProject: f.jiraProject,
		jqlOverride: f.jqlOverride,
		targetRepo:  projectPath,
		outputPath:  f.outputPath,
		fullsendDir: f.fullsendDir,
	}, gitlabURL, nil
}

// normalizeJiraGitLabURL validates a GitLab instance URL for Jira dispatch:
// https, a host, no credentials, query, or fragment. A path prefix (a
// self-managed instance served under a sub-path) is kept. The trailing
// slash is removed so the URL is stable in the coordination namespace.
func normalizeJiraGitLabURL(raw string) (string, error) {
	if !urlutil.IsURL(raw) {
		return "", fmt.Errorf("--gitlab-url %q must be an https URL without credentials", raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("--gitlab-url %q must not carry a query or fragment", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// runJiraPollGitLab runs the jira-poll driver against a GitLab project
// (#8919). Matched Jira events are not handed to a GitHub workflow; each
// dispatch record starts the project's protected agent pipeline through the
// same route the GitLab poller uses (see poll.Poller.DispatchJiraEvent),
// authenticated with the Poller role credential, and an issue's Jira
// checkpoint advances only when all of its pipelines were created.
func runJiraPollGitLab(cmd *cobra.Command, f jiraPollGitLabFlags) error {
	args, gitlabURL, err := validateJiraGitLabPollArgs(f, os.Getenv)
	if err != nil {
		return err
	}

	sel, forgeToken, err := resolveGitLabPollerCredential(os.Getenv)
	if err != nil {
		return err
	}
	logGitLabRoleDiagnostics(sel, ui.New(cmd.ErrOrStderr()))

	pipelineRef := os.Getenv("CI_COMMIT_REF_NAME")
	if pipelineRef == "" {
		pipelineRef = os.Getenv("CI_DEFAULT_BRANCH")
	}
	if pipelineRef == "" {
		return fmt.Errorf("CI_COMMIT_REF_NAME or CI_DEFAULT_BRANCH is required for pipeline dispatch")
	}
	dispatchSecret := os.Getenv("FULLSEND_DISPATCH_SECRET")
	if dispatchSecret == "" {
		return fmt.Errorf("FULLSEND_DISPATCH_SECRET is required for --target-forge gitlab: the agent pipeline rejects unsigned dispatches")
	}
	childJobToken, err := gitlabChildDispatchJobToken(os.Getenv)
	if err != nil {
		return err
	}

	jiraClient, err := buildJiraClient(args.jiraURL)
	if err != nil {
		return fmt.Errorf("create Jira client: %w", err)
	}

	matcher, err := newCELMatcher(cmd.Context(), args.fullsendDir)
	if err != nil {
		return fmt.Errorf("build CEL matcher: %w", err)
	}

	glClient, err := gitlab.New(forgeToken, gitlab.WithBaseURL(gitlabURL))
	if err != nil {
		return fmt.Errorf("create GitLab client: %w", err)
	}
	pollClient := gitlab.NewPollClient(glClient)
	// Authenticate to GitLab before any Jira lock or checkpoint is touched,
	// so a bad Poller credential fails the cycle up front.
	if _, err := pollClient.GetAuthenticatedUserID(cmd.Context()); err != nil {
		return wrapGitLabAuthFailure(sel, fmt.Errorf("resolve GitLab poller identity: %w", err))
	}

	glPoller := poll.New(pollClient, nil, args.targetRepo, poll.Options{
		GitLabURL:             gitlabURL,
		PipelineRef:           pipelineRef,
		PollJobURL:            os.Getenv("CI_JOB_URL"),
		DispatchSecret:        dispatchSecret,
		ChildDispatchJobToken: childJobToken,
	})

	opts := jiraPollOptions(args, matcher)
	opts.TargetForge = jirapoll.TargetForgeGitLab
	opts.GitLabURL = gitlabURL
	opts.Dispatcher = gitlabJiraDispatcher{poller: glPoller}
	poller := jirapoll.New(jiraClient, matcher, opts)
	return wrapGitLabAuthFailure(sel, poller.Run(cmd.Context()))
}

// jiraEventPipelineDispatcher is the GitLab dispatch surface
// gitlabJiraDispatcher needs; poll.Poller implements it.
type jiraEventPipelineDispatcher interface {
	DispatchJiraEvent(ctx context.Context, stage string, event *normevent.Event) error
}

// gitlabJiraDispatcher is the jirapoll output driver for GitLab targets.
// It recovers the normalized event the CEL matcher embedded in the record's
// event_payload, validates it, and starts the agent pipeline for the
// record's agent.
type gitlabJiraDispatcher struct {
	poller jiraEventPipelineDispatcher
}

// Dispatch implements jirapoll.RecordDispatcher.
func (d gitlabJiraDispatcher) Dispatch(ctx context.Context, record jirapoll.DispatchRecord) error {
	var payload struct {
		NormalizedEvent json.RawMessage `json:"_normalized_event"`
	}
	if err := json.Unmarshal([]byte(record.EventPayload), &payload); err != nil {
		return fmt.Errorf("parse event payload for %s: %w", record.Agent, err)
	}
	if len(payload.NormalizedEvent) == 0 {
		return fmt.Errorf("event payload for %s has no _normalized_event", record.Agent)
	}
	event, err := normevent.ParseJSON(payload.NormalizedEvent)
	if err != nil {
		return fmt.Errorf("event payload for %s: %w", record.Agent, err)
	}
	return d.poller.DispatchJiraEvent(ctx, record.Agent, event)
}
