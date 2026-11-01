package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newPollCmd() *cobra.Command {
	var (
		forgeFlag   string
		inputDriver string
		projectPath string
		gitlabURL   string
		outputPath  string
		fullsendDir string
		jiraURL     string
		jiraProject string
		jqlOverride string
		targetRepo  string
		targetForge string
		modeFlag    string

		printDispatchRoutes bool
	)

	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Poll forge or external tracker APIs for new events and dispatch agent stages",
		RunE: func(cmd *cobra.Command, args []string) error {
			if printDispatchRoutes {
				// Capability probe for the GitLab job scripts (#8438): a CLI
				// that predates the FULLSEND_GITLAB_DISPATCH_ROUTE selector
				// rejects this flag as unknown, so a nonzero exit or a missing
				// route name means the route is unsupported.
				for _, route := range supportedGitLabDispatchRoutes {
					fmt.Fprintln(cmd.OutOrStdout(), route)
				}
				return nil
			}
			if inputDriver == "jira-poll" {
				if targetForge == jirapoll.TargetForgeGitLab {
					return runJiraPollGitLab(cmd, jiraPollGitLabFlags{
						jiraURL:     jiraURL,
						jiraProject: jiraProject,
						jqlOverride: jqlOverride,
						targetRepo:  targetRepo,
						projectPath: projectPath,
						gitlabURL:   gitlabURL,
						outputPath:  outputPath,
						fullsendDir: fullsendDir,
					})
				}
				if targetForge != jirapoll.TargetForgeGitHub {
					return fmt.Errorf("--target-forge must be %q or %q (got %q)", jirapoll.TargetForgeGitHub, jirapoll.TargetForgeGitLab, targetForge)
				}
				return runJiraPoll(cmd, jiraURL, jiraProject, jqlOverride, targetRepo, outputPath, fullsendDir)
			}

			if inputDriver == "gitlab-webhook" {
				return runGitLabWebhook(cmd, projectPath, gitlabURL, fullsendDir)
			}

			if forgeFlag != "gitlab" {
				return fmt.Errorf("poll command supports --forge gitlab, --input-driver jira-poll, or --input-driver gitlab-webhook (got forge=%q, input-driver=%q)", forgeFlag, inputDriver)
			}

			sel, forgeToken, err := resolveGitLabPollerCredential(os.Getenv)
			if err != nil {
				return err
			}
			logGitLabRoleDiagnostics(sel, ui.New(cmd.ErrOrStderr()))

			if projectPath == "" {
				projectPath = os.Getenv("CI_PROJECT_PATH")
			}
			if projectPath == "" {
				return fmt.Errorf("--project or CI_PROJECT_PATH is required")
			}

			// Resolve poll mode from flag or environment variable.
			mode := modeFlag
			if mode == "" {
				mode = os.Getenv(forge.VarPollMode)
			}
			if mode != "" && mode != "slash" && mode != "events" {
				return fmt.Errorf("invalid poll mode %q: must be \"slash\" or \"events\"", mode)
			}

			glClient, err := gitlab.New(forgeToken, gitlab.WithBaseURL(gitlabURL))
			if err != nil {
				return fmt.Errorf("create GitLab client: %w", err)
			}
			pollClient := gitlab.NewPollClient(glClient)

			botUserID, err := pollClient.GetAuthenticatedUserID(cmd.Context())
			if err != nil {
				return wrapGitLabAuthFailure(sel, fmt.Errorf("resolve bot user ID: %w", err))
			}
			analystUserID, err := gitlabAnalystUserID(os.Getenv)
			if err != nil {
				return err
			}

			// Build the event router from config + agents-repo known agents.
			router, err := buildRouter(fullsendDir)
			if err != nil {
				return fmt.Errorf("build event router: %w", err)
			}

			pipelineRef := os.Getenv("CI_COMMIT_REF_NAME")
			if pipelineRef == "" {
				pipelineRef = os.Getenv("CI_DEFAULT_BRANCH")
			}
			if pipelineRef == "" {
				return fmt.Errorf("CI_COMMIT_REF_NAME or CI_DEFAULT_BRANCH is required for pipeline dispatch")
			}

			childJobToken, err := gitlabChildDispatchJobToken(os.Getenv)
			if err != nil {
				return err
			}

			opts := poll.Options{
				BotUserID:             botUserID,
				AnalystUserID:         analystUserID,
				GitLabURL:             gitlabURL,
				PipelineRef:           pipelineRef,
				PollJobURL:            os.Getenv("CI_JOB_URL"),
				DispatchSecret:        os.Getenv("FULLSEND_DISPATCH_SECRET"),
				Mode:                  mode,
				ChildDispatchJobToken: childJobToken,
			}

			poller := poll.New(pollClient, router, projectPath, opts)
			return wrapGitLabAuthFailure(sel, poller.Run(cmd.Context()))
		},
	}

	cmd.Flags().BoolVar(&printDispatchRoutes, "print-dispatch-routes", false, "Print the GitLab agent dispatch routes this CLI supports (one per line) and exit; used by the job scripts to detect an older CLI before selecting a route")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", "Forge platform (gitlab)")
	cmd.Flags().StringVar(&inputDriver, "input-driver", "", "Poll input driver (jira-poll or gitlab-webhook)")
	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path (default: $CI_PROJECT_PATH for --forge gitlab and for --input-driver jira-poll --target-forge gitlab; required for --input-driver gitlab-webhook)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write dispatches JSON (jira-poll only; required for --target-forge github, optional for --target-forge gitlab; ignored by --forge gitlab)")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	cmd.Flags().StringVar(&jiraURL, "jira-url", "", "Jira instance base URL (default: $JIRA_BASE_URL)")
	cmd.Flags().StringVar(&jiraProject, "jira-project", "", "Jira project key for JQL scoping")
	cmd.Flags().StringVar(&jqlOverride, "jql", "", "Custom JQL override")
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "GitHub repo slug where agents run (default: $GITHUB_REPOSITORY; jira-poll with --target-forge github only)")
	cmd.Flags().StringVar(&targetForge, "target-forge", jirapoll.TargetForgeGitHub, "Forge the jira-poll driver dispatches agents to: 'github' (write --output for reusable-dispatch.yml) or 'gitlab' (start the project's protected agent pipelines; uses --project and --gitlab-url)")
	cmd.Flags().StringVar(&modeFlag, "mode", "", "Poll mode: 'slash' (slash commands only) or 'events' (labels, merges, non-slash notes)")
	cmd.MarkFlagsOneRequired("forge", "input-driver")

	cmd.Hidden = true
	return cmd
}

func runJiraPoll(cmd *cobra.Command, jiraURL, jiraProject, jqlOverride, targetRepo, outputPath, fullsendDir string) error {
	args, err := validateJiraPollArgs(jiraURL, jiraProject, jqlOverride, targetRepo, outputPath, fullsendDir)
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

	poller := jirapoll.New(jiraClient, matcher, jiraPollOptions(args, matcher))
	return poller.Run(cmd.Context())
}

// runGitLabWebhook is the gitlab-webhook input driver (ADR 0125): it
// reads the native webhook body from GitLab's file-type TRIGGER_PAYLOAD
// variable and dispatches the validated, routed events through the
// poller's dispatch spine with the poller-role credential, deduplicated
// against the poller's per-mode state (see poll.Poller.RunWebhook).
//
// The project path must be passed explicitly with --project: unlike the
// cron poll path there is no CI_PROJECT_PATH fallback, because a trigger
// caller can override that variable and the dispatcher job pins the
// project from its CI_JOB_TOKEN job record instead.
func runGitLabWebhook(cmd *cobra.Command, projectPath, gitlabURL, fullsendDir string) error {
	sel, forgeToken, err := resolveGitLabPollerCredential(os.Getenv)
	if err != nil {
		return err
	}
	logGitLabRoleDiagnostics(sel, ui.New(cmd.ErrOrStderr()))

	if projectPath == "" {
		return fmt.Errorf("--project is required for --input-driver gitlab-webhook")
	}

	pipelineRef := os.Getenv("CI_COMMIT_REF_NAME")
	if pipelineRef == "" {
		pipelineRef = os.Getenv("CI_DEFAULT_BRANCH")
	}
	if pipelineRef == "" {
		return fmt.Errorf("CI_COMMIT_REF_NAME or CI_DEFAULT_BRANCH is required for pipeline dispatch")
	}

	payloadPath := os.Getenv("TRIGGER_PAYLOAD")
	if payloadPath == "" {
		return fmt.Errorf("TRIGGER_PAYLOAD is required for --input-driver gitlab-webhook")
	}
	raw, err := poll.ReadWebhookPayloadFile(payloadPath)
	if err != nil {
		return err
	}

	glClient, err := gitlab.New(forgeToken, gitlab.WithBaseURL(gitlabURL))
	if err != nil {
		return fmt.Errorf("create GitLab client: %w", err)
	}
	pollClient := gitlab.NewPollClient(glClient)

	botUserID, err := pollClient.GetAuthenticatedUserID(cmd.Context())
	if err != nil {
		return wrapGitLabAuthFailure(sel, fmt.Errorf("resolve bot user ID: %w", err))
	}
	analystUserID, err := gitlabAnalystUserID(os.Getenv)
	if err != nil {
		return err
	}

	router, err := buildRouter(fullsendDir)
	if err != nil {
		return fmt.Errorf("build event router: %w", err)
	}

	childJobToken, err := gitlabChildDispatchJobToken(os.Getenv)
	if err != nil {
		return err
	}

	opts := poll.Options{
		BotUserID:             botUserID,
		AnalystUserID:         analystUserID,
		GitLabURL:             gitlabURL,
		PipelineRef:           pipelineRef,
		PollJobURL:            os.Getenv("CI_JOB_URL"),
		DispatchSecret:        os.Getenv("FULLSEND_DISPATCH_SECRET"),
		ChildDispatchJobToken: childJobToken,
	}

	poller := poll.New(pollClient, router, projectPath, opts)
	return wrapGitLabAuthFailure(sel, poller.RunWebhook(cmd.Context(), raw))
}

// supportedGitLabDispatchRoutes are the VarGitLabDispatchRoute values this CLI
// honors, in the order `fullsend poll --print-dispatch-routes` prints them.
var supportedGitLabDispatchRoutes = []string{
	forge.GitLabDispatchRouteAPI,
	forge.GitLabDispatchRouteChildPipeline,
}

// gitlabAnalystUserID reads the registered Analyst identity's numeric GitLab
// user ID that the poll and dispatcher job scripts export (#8807). The poller
// retains a changes-requested marker note only when the Analyst authored it,
// because in distinct-role installations the Analyst is not the Poller whose
// credential this command holds. Unset returns 0, which matches no author, so
// an older job script that does not export the ID keeps the marker filtered.
// A malformed value is an error rather than a silent 0, since the scripts
// only export a validated positive integer.
func gitlabAnalystUserID(getenv func(string) string) (int, error) {
	raw := getenv(forge.VarGitLabAnalystUserID)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s=%q is not a positive GitLab user ID", forge.VarGitLabAnalystUserID, raw)
	}
	return id, nil
}

// gitlabChildDispatchJobToken resolves the agent dispatch route the GitLab
// poll and dispatcher job scripts selected (#8438). It returns the
// CI_JOB_TOKEN to start source=pipeline agent pipelines with when the
// route is forge.GitLabDispatchRouteChildPipeline, or "" for the Poller
// API route (unset or forge.GitLabDispatchRouteAPI). The child route
// without a job token, or an unrecognized route, is an error rather than
// a silent fallback: the scripts select the child route only when the
// pipeline is owned by the trigger-only account, and an API pipeline
// created by the Poller would then fail the agent's creator check.
func gitlabChildDispatchJobToken(getenv func(string) string) (string, error) {
	switch route := getenv(forge.VarGitLabDispatchRoute); route {
	case "", forge.GitLabDispatchRouteAPI:
		return "", nil
	case forge.GitLabDispatchRouteChildPipeline:
		token := getenv("CI_JOB_TOKEN")
		if token == "" {
			return "", fmt.Errorf("%s=%s requires CI_JOB_TOKEN", forge.VarGitLabDispatchRoute, forge.GitLabDispatchRouteChildPipeline)
		}
		return token, nil
	default:
		return "", fmt.Errorf("%s=%q is not a recognized dispatch route (expected unset, %q, or %q)", forge.VarGitLabDispatchRoute, route, forge.GitLabDispatchRouteAPI, forge.GitLabDispatchRouteChildPipeline)
	}
}

// jiraPollArgs holds resolved arguments for runJiraPoll after env-var fallbacks.
type jiraPollArgs struct {
	jiraURL     string
	jiraProject string
	jqlOverride string
	targetRepo  string
	outputPath  string
	fullsendDir string
}

// validTargetRepo matches "owner/repo" slugs (subgroup segments allowed,
// matching splitOwnerRepo). Validated up front because a slash-less value
// would otherwise flow into a silently malformed Jira entity-property lock
// namespace and into NormalizedEvent.Repo.
var validTargetRepo = regexp.MustCompile(`^[^/\s]+(/[^/\s]+)+$`)

// validateJiraPollArgs resolves env-var fallbacks and validates required
// arguments for the jira-poll input driver. It returns the resolved args
// or a validation error.
func validateJiraPollArgs(jiraURL, jiraProject, jqlOverride, targetRepo, outputPath, fullsendDir string) (jiraPollArgs, error) {
	if jiraURL == "" {
		jiraURL = os.Getenv("JIRA_BASE_URL")
	}
	if jiraURL == "" {
		return jiraPollArgs{}, fmt.Errorf("--jira-url or JIRA_BASE_URL is required")
	}

	if targetRepo == "" {
		targetRepo = os.Getenv("GITHUB_REPOSITORY")
	}
	if targetRepo == "" {
		return jiraPollArgs{}, fmt.Errorf("--target-repo or GITHUB_REPOSITORY is required")
	}
	if !validTargetRepo.MatchString(targetRepo) {
		return jiraPollArgs{}, fmt.Errorf("--target-repo %q must be an owner/repo slug", targetRepo)
	}

	if jiraProject == "" && jqlOverride == "" {
		return jiraPollArgs{}, fmt.Errorf("--jira-project or --jql is required")
	}

	if outputPath == "" {
		return jiraPollArgs{}, fmt.Errorf("--output is required: without it, a full poll cycle runs and checkpoints advance in Jira, but every dispatch is silently discarded")
	}

	return jiraPollArgs{
		jiraURL:     jiraURL,
		jiraProject: jiraProject,
		jqlOverride: jqlOverride,
		targetRepo:  targetRepo,
		outputPath:  outputPath,
		fullsendDir: fullsendDir,
	}, nil
}

// buildJiraClient creates a Jira client using Basic (email+token) auth.
// JIRA_USER_EMAIL is required: this driver only targets Jira Cloud, and
// Cloud does not accept a bare API token via Bearer auth the way Data
// Center/Server PATs do — omitting the email would silently send a scheme
// Cloud rejects, surfacing as a generic 401 rather than a clear
// configuration error.
func buildJiraClient(jiraURL string) (*jira.LiveClient, error) {
	jiraToken := os.Getenv("JIRA_TOKEN")
	if jiraToken == "" {
		return nil, fmt.Errorf("JIRA_TOKEN environment variable is required")
	}
	email := os.Getenv("JIRA_USER_EMAIL")
	if email == "" {
		return nil, fmt.Errorf("JIRA_USER_EMAIL environment variable is required (Jira Cloud auth is email+token, not a bare token)")
	}
	return jira.New(jiraToken, jira.WithBaseURL(jiraURL), jira.WithEmail(email))
}

// buildRouter constructs a HarnessRouter from config-registered agents
// and the known first-party agents available via agents-repo fallback.
// Used by the GitLab poll path which still uses hardcoded routing.
func buildRouter(fullsendDir string) (*dispatch.HarnessRouter, error) {
	cfg, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	seen := make(map[string]bool)
	var names []string

	entries := cfg.AgentEntries()
	for i := len(entries) - 1; i >= 0; i-- {
		name := entries[i].DerivedName()
		lower := strings.ToLower(name)
		if !seen[lower] {
			seen[lower] = true
			if entries[i].IsEnabled() {
				names = append(names, name)
			}
		}
	}

	for name := range defaultAgentsRepoKnownAgents {
		if !seen[name] && !config.IsAgentExplicitlyDisabled(entries, name) {
			seen[name] = true
			names = append(names, name)
		}
	}

	return dispatch.NewHarnessRouter(names), nil
}

// celMatcher evaluates events against harness CEL triggers, replacing
// the hardcoded HarnessRouter for the Jira poll path. Candidates are
// loaded once at construction and reused for every event in the cycle.
type celMatcher struct {
	candidates []harnessdispatch.TriggeredHarness
	// jiraAuth is the jira authorization provider from config.yaml, or
	// nil when none is listed. It configures the poller's actor role
	// resolution and the gate's Jira label rule.
	jiraAuth *config.AuthorizationProvider
}

// newCELMatcher loads harness files with CEL triggers from the config
// directory and returns a matcher that evaluates events against them.
func newCELMatcher(ctx context.Context, fullsendDir string) (*celMatcher, error) {
	cfg, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := config.ValidateRuntimeAuthorization(cfg); err != nil {
		return nil, fmt.Errorf("invalid authorization config: %w", err)
	}
	var jiraAuth *config.AuthorizationProvider
	if pr, ok := cfg.(config.PerRepoConfigReader); ok {
		jiraAuth = pr.JiraAuthorization()
	}
	if cfg.IsKillSwitchActive() {
		// Kill switch active: return a matcher with no candidates so all
		// events are silently dropped (no dispatches).
		return &celMatcher{jiraAuth: jiraAuth}, nil
	}
	candidates, err := harnessdispatch.ListTriggeredHarnesses(ctx, fullsendDir, cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("list triggered harnesses: %w", err)
	}
	return &celMatcher{candidates: candidates, jiraAuth: jiraAuth}, nil
}

// authOptions returns the dispatch gate policy for the configured jira
// provider: with no jira entry, Jira label-added events take the
// per-harness role threshold as before; with an entry, they are authorized
// by Jira's Edit Issues permission under labels: jira_editors (the default)
// and take the role threshold under allowlist or project_roles, where the
// poller resolves the label author's role from that source.
func (m *celMatcher) authOptions() harnessdispatch.AuthOptions {
	return harnessdispatch.AuthOptions{
		JiraLabelsByEditPermission: m.jiraAuth != nil &&
			m.jiraAuth.EffectiveJiraLabels() == config.JiraLabelsEditors,
	}
}

// jiraPollOptions builds the poller options every jira-poll target forge
// shares: the Jira connection, the query, the output path and the jira
// authorization provider the matcher loaded. Forge-specific callers set
// their own TargetForge, GitLabURL and Dispatcher on the result, so the
// authorization lists reach the poller on every target.
func jiraPollOptions(args jiraPollArgs, matcher *celMatcher) jirapoll.Options {
	return jirapoll.Options{
		TargetRepo:  args.targetRepo,
		JiraBaseURL: args.jiraURL,
		JiraProject: args.jiraProject,
		JQL:         args.jqlOverride,
		OutputPath:  args.outputPath,

		Authorization: jiraPollAuthorization(matcher.jiraAuth),
	}
}

// jiraPollAuthorization converts the jira authorization provider to the
// poller's option type. A nil provider is the zero value.
func jiraPollAuthorization(p *config.AuthorizationProvider) jirapoll.Authorization {
	if p == nil {
		return jirapoll.Authorization{}
	}
	list := func(l *config.JiraActorList) jirapoll.ActorList {
		if l == nil {
			return jirapoll.ActorList{}
		}
		return jirapoll.ActorList{Groups: l.Groups, Accounts: l.Accounts}
	}
	return jirapoll.Authorization{
		Configured:      true,
		Write:           list(p.Write),
		Triage:          list(p.Triage),
		ListsConfigured: p.Write != nil || p.Triage != nil,
		Labels:          p.EffectiveJiraLabels(),
	}
}

// Match evaluates the event against all CEL trigger candidates and returns
// dispatch records for each matching harness.
func (m *celMatcher) Match(_ context.Context, event *normevent.Event) ([]jirapoll.DispatchRecord, error) {
	opts := m.authOptions()
	if !harnessdispatch.IsEventAuthorizedWith(event, opts) {
		return nil, nil
	}
	candidates := harnessdispatch.FilterAuthorizedHarnessesWith(m.candidates, event, opts)
	matched, err := harnessdispatch.MatchHarnesses(candidates, event)
	if err != nil {
		return nil, err
	}
	var records []jirapoll.DispatchRecord
	for _, h := range matched {
		ref, err := harnessdispatch.ProjectExecutionRef(h.Name, h.Harness.Role, event)
		if err != nil {
			return nil, fmt.Errorf("build execution ref for %s: %w", h.Name, err)
		}
		records = append(records, jirapoll.DispatchRecord{
			Agent:         ref.Agent,
			Role:          ref.Role,
			SourceRepo:    ref.SourceRepo,
			EventType:     ref.EventType,
			EventPayload:  ref.EventPayload,
			TriggerSource: ref.TriggerSource,
			StatusRepo:    ref.StatusRepo,
			StatusNumber:  ref.StatusNumber,
		})
	}
	return records, nil
}
