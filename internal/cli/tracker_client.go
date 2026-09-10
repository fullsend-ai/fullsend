package cli

import (
	"fmt"
	"os"

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
