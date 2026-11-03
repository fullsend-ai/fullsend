package cli

import (
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/tracker"
)

// newJiraTrackerClientFromEnv creates the tracker used for Jira-originated
// status notifications and reconciliation. Jira Cloud requires Basic auth
// with both the account email and API token; accepting a bare token here
// would defer a configuration error until the first API request. API calls
// go to the URL resolveJiraAPIURL returns (see newJiraClient).
func newJiraTrackerClientFromEnv() (tracker.Client, error) {
	baseURL := os.Getenv("JIRA_BASE_URL")
	if baseURL == "" {
		return nil, fmt.Errorf("JIRA_BASE_URL required for Jira status notifications")
	}
	token := os.Getenv("JIRA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("JIRA_TOKEN required for Jira status notifications")
	}
	maskActionsValue(token)
	email := os.Getenv("JIRA_USER_EMAIL")
	if email == "" {
		return nil, fmt.Errorf("JIRA_USER_EMAIL required for Jira Cloud authentication")
	}

	jc, err := newJiraClient(baseURL, email, token)
	if err != nil {
		return nil, fmt.Errorf("creating Jira client: %w", err)
	}
	return tracker.NewJiraClient(jc, baseURL)
}
