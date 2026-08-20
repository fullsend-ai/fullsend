package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/poll"
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
		modeFlag    string
	)

	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Poll forge or external tracker APIs for new events and dispatch agent stages",
		RunE: func(cmd *cobra.Command, args []string) error {
			if inputDriver == "jira-poll" {
				return runJiraPoll(cmd, jiraURL, jiraProject, jqlOverride, targetRepo, outputPath, fullsendDir)
			}

			if forgeFlag != "gitlab" {
				return fmt.Errorf("poll command supports --forge gitlab or --input-driver jira-poll (got forge=%q, input-driver=%q)", forgeFlag, inputDriver)
			}

			forgeToken := os.Getenv("FULLSEND_FORGE_TOKEN")
			if forgeToken == "" {
				return fmt.Errorf("FULLSEND_FORGE_TOKEN is required")
			}

			if projectPath == "" {
				projectPath = os.Getenv("CI_PROJECT_PATH")
			}
			if projectPath == "" {
				return fmt.Errorf("--project or CI_PROJECT_PATH is required")
			}

			// Resolve poll mode from flag or environment variable.
			mode := modeFlag
			if mode == "" {
				mode = os.Getenv("FULLSEND_POLL_MODE")
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
				return fmt.Errorf("resolve bot user ID: %w", err)
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

			opts := poll.Options{
				BotUserID:      botUserID,
				GitLabURL:      gitlabURL,
				PipelineRef:    pipelineRef,
				PollJobURL:     os.Getenv("CI_JOB_URL"),
				DispatchSecret: os.Getenv("FULLSEND_DISPATCH_SECRET"),
				Mode:           mode,
			}

			poller := poll.New(pollClient, router, projectPath, opts)
			return poller.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&forgeFlag, "forge", "", "Forge platform (gitlab)")
	cmd.Flags().StringVar(&inputDriver, "input-driver", "", "Poll input driver (jira-poll)")
	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path (default: $CI_PROJECT_PATH)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write dispatches JSON (jira-poll only; ignored by --forge gitlab)")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	cmd.Flags().StringVar(&jiraURL, "jira-url", "", "Jira instance base URL (default: $JIRA_BASE_URL)")
	cmd.Flags().StringVar(&jiraProject, "jira-project", "", "Jira project key for JQL scoping")
	cmd.Flags().StringVar(&jqlOverride, "jql", "", "Custom JQL override")
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "GitHub repo slug where agents run (default: $GITHUB_REPOSITORY)")
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

	opts := jirapoll.Options{
		TargetRepo:  args.targetRepo,
		JiraBaseURL: args.jiraURL,
		JiraProject: args.jiraProject,
		JQL:         args.jqlOverride,
		OutputPath:  args.outputPath,
	}

	poller := jirapoll.New(jiraClient, matcher, opts)
	return poller.Run(cmd.Context())
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
}

// newCELMatcher loads harness files with CEL triggers from the config
// directory and returns a matcher that evaluates events against them.
func newCELMatcher(ctx context.Context, fullsendDir string) (*celMatcher, error) {
	cfg, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.IsKillSwitchActive() {
		// Kill switch active: return a matcher with no candidates so all
		// events are silently dropped (no dispatches).
		return &celMatcher{}, nil
	}
	candidates, err := harnessdispatch.ListTriggeredHarnesses(ctx, fullsendDir, cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("list triggered harnesses: %w", err)
	}
	return &celMatcher{candidates: candidates}, nil
}

// Match evaluates the event against all CEL trigger candidates and returns
// dispatch records for each matching harness.
func (m *celMatcher) Match(_ context.Context, event *normevent.Event) ([]jirapoll.DispatchRecord, error) {
	if !harnessdispatch.IsAuthorized(event) {
		return nil, nil
	}
	matched, err := harnessdispatch.MatchHarnesses(m.candidates, event)
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
