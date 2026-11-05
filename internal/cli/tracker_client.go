package cli

import (
	"fmt"
	"os"

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

// newTrackerClient creates a tracker.Client for the given tracker type.
//
// For GitHub and GitLab, it wraps the corresponding forge.Client via
// tracker.NewForgeClient. For Jira, it creates a jira.LiveClient and
// wraps it via tracker.NewJiraClient. Token resolution mirrors the
// existing forge client factory patterns.
//
// The token parameter overrides environment-variable resolution when
// non-empty (for the --token CLI flag). jiraBaseURL and jiraEmail are
// Jira-specific parameters sourced from --jira-url/--jira-email flags
// or JIRA_BASE_URL/JIRA_USER_EMAIL environment variables.
func newTrackerClient(trackerName, token, jiraBaseURL, jiraEmail string) (tracker.Client, error) {
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

	case trackerJira:
		jiraToken, err := resolveJiraTrackerToken(token)
		if err != nil {
			return nil, err
		}
		baseURL := jiraBaseURL
		if baseURL == "" {
			baseURL = os.Getenv("JIRA_BASE_URL")
		}
		if baseURL == "" {
			return nil, fmt.Errorf("--jira-url or JIRA_BASE_URL required for Jira tracker")
		}
		email := jiraEmail
		if email == "" {
			email = os.Getenv("JIRA_USER_EMAIL")
		}
		if email == "" {
			// Mirrors buildJiraClient's rationale in poll.go: this Jira
			// client targets Cloud only, Cloud rejects a bare Bearer
			// token, and omitting the email would silently send a
			// scheme Cloud rejects, surfacing as a generic 401 rather
			// than a clear configuration error.
			return nil, fmt.Errorf("--jira-email or JIRA_USER_EMAIL required for Jira tracker (Jira Cloud auth is email+token, not a bare token)")
		}
		jc, err := jira.New(jiraToken, jira.WithBaseURL(baseURL), jira.WithEmail(email))
		if err != nil {
			return nil, fmt.Errorf("creating Jira client: %w", err)
		}
		tc, err := tracker.NewJiraClient(jc, baseURL)
		if err != nil {
			return nil, fmt.Errorf("creating Jira tracker client: %w", err)
		}
		return tc, nil

	default:
		return nil, fmt.Errorf("unsupported tracker %q: use %q, %q, or %q", trackerName, trackerGitHub, trackerGitLab, trackerJira)
	}
}

// newConfiguredTrackerClient is newTrackerClient for the fullsend issues
// commands: for Jira it first resolves the instance URL from
// tracker.jira.base_url in the config.yaml under fullsendDir (see
// resolveConfiguredJiraURL).
func newConfiguredTrackerClient(trackerName, token, jiraURLFlag, jiraEmail, fullsendDir string, testConfigReader config.PerRepoConfigReader) (tracker.Client, error) {
	jiraURL := jiraURLFlag
	if trackerName == trackerJira {
		var err error
		jiraURL, err = resolveConfiguredJiraURL(jiraURLFlag, fullsendDir, testConfigReader)
		if err != nil {
			return nil, err
		}
	}
	return newTrackerClient(trackerName, token, jiraURL, jiraEmail)
}

// resolveConfiguredJiraURL returns the Jira instance URL for the issues
// commands (#9035). When the effective config sets the tracker.jira
// connection settings, base_url is the only source: --jira-url is
// rejected instead of acting as an override, and JIRA_BASE_URL is not
// consulted. Otherwise the flag is returned unchanged, and
// newTrackerClient falls back to JIRA_BASE_URL as before.
func resolveConfiguredJiraURL(jiraURLFlag, fullsendDir string, testConfigReader config.PerRepoConfigReader) (string, error) {
	var tracking *config.TrackingConfig
	switch {
	case testConfigReader != nil:
		tracking = testConfigReader.IssueTracking()
	case fullsendDir != "":
		reader, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
		if err != nil {
			return "", fmt.Errorf("loading config for tracker.jira: %w", err)
		}
		tracking = reader.IssueTracking()
	}
	if tracking.Provider() != config.TrackingProviderJira || !tracking.Jira.HasConnectionSettings() {
		return jiraURLFlag, nil
	}
	if err := tracking.Jira.ValidateConnection(); err != nil {
		return "", err
	}
	if jiraURLFlag != "" {
		return "", fmt.Errorf("--jira-url cannot be used when tracker.jira is configured in config.yaml: set tracker.jira.base_url there instead")
	}
	return tracking.Jira.BaseURL, nil
}

// resolveGitLabTrackerToken returns a GitLab token from the explicit
// override or GITLAB_TOKEN environment variable.
func resolveGitLabTrackerToken(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return resolveGitLabToken()
}

// resolveJiraTrackerToken returns a Jira API token from the explicit
// override or JIRA_TOKEN environment variable.
func resolveJiraTrackerToken(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if token := os.Getenv("JIRA_TOKEN"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("--token or JIRA_TOKEN required for Jira tracker")
}
