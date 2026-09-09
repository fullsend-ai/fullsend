package cli

import (
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

// newJiraTrackerClientFromEnv creates the tracker used for Jira-originated
// status notifications and reconciliation. Jira Cloud requires Basic auth
// with both the account email and API token; accepting a bare token here
// would defer a configuration error until the first API request.
func newJiraTrackerClientFromEnv() (tracker.Client, error) {
	baseURL := os.Getenv("JIRA_BASE_URL")
	if baseURL == "" {
		return nil, fmt.Errorf("JIRA_BASE_URL required for Jira status notifications")
	}
	token := os.Getenv("JIRA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("JIRA_TOKEN required for Jira status notifications")
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", token)
	}
	email := os.Getenv("JIRA_USER_EMAIL")
	if email == "" {
		return nil, fmt.Errorf("JIRA_USER_EMAIL required for Jira Cloud authentication")
	}

	jc, err := jira.New(token, jira.WithBaseURL(baseURL), jira.WithEmail(email))
	if err != nil {
		return nil, fmt.Errorf("creating Jira client: %w", err)
	}
	return tracker.NewJiraClient(jc, baseURL)
}
