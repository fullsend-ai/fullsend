package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

const (
	// trackerGitHub is the --tracker value for GitHub.
	trackerGitHub = "github"
	// trackerGitLab is the --tracker value for GitLab.
	trackerGitLab = "gitlab"
	// trackerJira is the --tracker value for Jira.
	trackerJira = "jira"
)

// jiraScopedTestOptions are extra jira.Options applied by
// newJiraTrackerClient. Tests set them (for example WithHTTPClient for a
// TLS test server); production code leaves them nil.
var jiraScopedTestOptions []jira.Option

// newForgeTrackerClient creates a tracker.Client for GitHub or GitLab by
// wrapping the corresponding forge.Client via tracker.NewForgeClient. The
// token parameter overrides environment-variable resolution when
// non-empty (for the --token CLI flag).
func newForgeTrackerClient(trackerName, token string) (tracker.Client, error) {
	switch trackerName {
	case trackerGitHub:
		fc, err := newAuthenticatedGitHubClient(token, "")
		if err != nil {
			return nil, err
		}
		return tracker.NewForgeClient(fc), nil

	case trackerGitLab:
		glToken, err := resolveGitLabTrackerToken(token)
		if err != nil {
			return nil, err
		}
		fc, err := newForgeClient("gitlab", glToken, "")
		if err != nil {
			return nil, err
		}
		return tracker.NewForgeClient(fc), nil

	default:
		return nil, fmt.Errorf("unsupported tracker %q: use %q, %q, or %q", trackerName, trackerGitHub, trackerGitLab, trackerJira)
	}
}

// newConfiguredTrackerClient creates the tracker.Client for the fullsend
// issues commands. GitHub and GitLab clients come from
// newForgeTrackerClient. A Jira client is built only from the effective
// tracker.jira config under fullsendDir (or testConfigReader) and the
// FULLSEND_JIRA_TOKEN / FULLSEND_JIRA_EMAIL or FULLSEND_JIRA_CLIENT_ID /
// FULLSEND_JIRA_CLIENT_SECRET environment variables; --token is rejected
// for Jira so the credential never travels on the command line.
func newConfiguredTrackerClient(trackerName, token, fullsendDir string, testConfigReader config.PerRepoConfigReader) (tracker.Client, error) {
	if trackerName != trackerJira {
		return newForgeTrackerClient(trackerName, token)
	}
	if token != "" {
		return nil, fmt.Errorf("--token is not supported with --tracker jira: set the %s environment variable instead", jira.TokenEnvVar)
	}
	settings, err := loadJiraTrackingSettings(fullsendDir, testConfigReader)
	if err != nil {
		return nil, err
	}
	return newJiraTrackerClient(settings)
}

// newJiraTrackerClientFromConfig creates the tracker used for
// Jira-originated status notifications and reconciliation from the
// effective tracker config and the FULLSEND_JIRA_* credentials.
func newJiraTrackerClientFromConfig(tracking *config.TrackingConfig) (tracker.Client, error) {
	settings, err := jiraTrackingSettings(tracking)
	if err != nil {
		return nil, err
	}
	return newJiraTrackerClient(settings)
}

// newJiraTrackerClient is the single constructor for Jira tracker clients
// outside the poller. Connection settings come from the validated
// tracker.jira settings; the credentials come from FULLSEND_JIRA_TOKEN and
// the optional FULLSEND_JIRA_EMAIL, or from FULLSEND_JIRA_CLIENT_ID and
// FULLSEND_JIRA_CLIENT_SECRET, via jira.CredentialsFromEnv. The client is
// built with jira.NewScoped, so requests go through the configured API
// gateway with Basic auth when an email is set, and Bearer auth with the
// token or an OAuth 2.0 access token otherwise.
func newJiraTrackerClient(settings *config.JiraTrackingConfig) (*tracker.JiraClient, error) {
	creds, err := jira.CredentialsFromEnv(nil)
	if err != nil {
		if os.Getenv("JIRA_TOKEN") != "" && os.Getenv(jira.TokenEnvVar) == "" &&
			os.Getenv(jira.ClientIDEnvVar) == "" && os.Getenv(jira.ClientSecretEnvVar) == "" {
			return nil, fmt.Errorf("%w (JIRA_TOKEN is no longer read; store the same token as %s (and the email as %s), or use %s and %s; set the Jira site URL in tracker.jira.base_url instead of JIRA_BASE_URL)",
				err, jira.TokenEnvVar, jira.EmailEnvVar, jira.ClientIDEnvVar, jira.ClientSecretEnvVar)
		}
		return nil, err
	}
	maskActionsValue(creds.Token)
	maskActionsValue(creds.ClientID)
	maskActionsValue(creds.ClientSecret)
	jc, err := jira.NewScoped(jira.ScopedConfig{
		BaseURL: settings.BaseURL,
		APIURL:  settings.EffectiveAPIURL(),
	}, creds, jiraScopedTestOptions...)
	if err != nil {
		return nil, fmt.Errorf("creating Jira client: %w", err)
	}
	tc, err := tracker.NewJiraClient(jc, settings.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("creating Jira tracker client: %w", err)
	}
	return tc, nil
}

// loadIssueTracking returns the effective tracker config from
// testConfigReader when non-nil, otherwise from the config.yaml under
// fullsendDir. It returns nil when neither is available.
func loadIssueTracking(fullsendDir string, testConfigReader config.PerRepoConfigReader) (*config.TrackingConfig, error) {
	switch {
	case testConfigReader != nil:
		return testConfigReader.IssueTracking(), nil
	case fullsendDir != "":
		reader, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
		if err != nil {
			return nil, fmt.Errorf("loading config for tracker.jira: %w", err)
		}
		return reader.IssueTracking(), nil
	}
	return nil, nil
}

// loadJiraTrackingSettings loads the effective tracker config (see
// loadIssueTracking) and returns its validated tracker.jira settings.
func loadJiraTrackingSettings(fullsendDir string, testConfigReader config.PerRepoConfigReader) (*config.JiraTrackingConfig, error) {
	tracking, err := loadIssueTracking(fullsendDir, testConfigReader)
	if err != nil {
		return nil, err
	}
	return jiraTrackingSettings(tracking)
}

// jiraTrackingSettings returns tracking's tracker.jira settings after
// checking that Jira is the selected provider and that the connection
// settings (base_url and project) are present and valid. There are no
// command-line or environment fallbacks for these settings.
func jiraTrackingSettings(tracking *config.TrackingConfig) (*config.JiraTrackingConfig, error) {
	if tracking.Provider() != config.TrackingProviderJira || tracking.Jira.BaseURL == "" {
		return nil, fmt.Errorf("Jira requires tracker.jira.base_url and tracker.jira.project in config.yaml (pass --fullsend-dir pointing at the directory containing it)")
	}
	if err := tracking.Jira.ValidateConnection(); err != nil {
		return nil, err
	}
	return tracking.Jira, nil
}

// resolveIssueProject returns the --project value for the issues
// commands. For Jira an omitted --project defaults to
// tracker.jira.project, the repository's primary project; every other
// tracker requires it.
func resolveIssueProject(trackerName, project, fullsendDir string, testConfigReader config.PerRepoConfigReader) (string, error) {
	if strings.TrimSpace(project) != "" {
		return project, nil
	}
	if trackerName != trackerJira {
		return "", fmt.Errorf("--project must not be empty for --tracker %s", trackerName)
	}
	settings, err := loadJiraTrackingSettings(fullsendDir, testConfigReader)
	if err != nil {
		return "", fmt.Errorf("--project was omitted and no default Jira project is configured: %w", err)
	}
	return settings.Project, nil
}

// checkJiraTargetProject refuses a Jira project that is neither the
// repository's primary project (tracker.jira.project) nor listed in
// tracker.jira.allowed_targets.projects. It guards cross-project issue
// creation only. Jira project keys are case-insensitive, so the
// comparison is too.
func checkJiraTargetProject(settings *config.JiraTrackingConfig, project string) error {
	if strings.EqualFold(project, settings.Project) {
		return nil
	}
	for _, allowed := range settings.AllowedTargets.Projects {
		if strings.EqualFold(project, allowed) {
			return nil
		}
	}
	return fmt.Errorf("Jira project %q is not allowed: use tracker.jira.project (%s) or add the project to tracker.jira.allowed_targets.projects", project, settings.Project)
}

// resolveGitLabTrackerToken returns a GitLab token from the explicit
// override or GITLAB_TOKEN environment variable.
func resolveGitLabTrackerToken(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return resolveGitLabToken()
}
