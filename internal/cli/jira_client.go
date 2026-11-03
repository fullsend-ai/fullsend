package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// jiraGatewayURL is the Atlassian Platform API Gateway that fullsend's Jira
// API calls default to (#9007). Overridden in tests to point at an httptest
// server.
var jiraGatewayURL = jira.DefaultGatewayURL

// resolveJiraAPIURL returns the base URL fullsend's Jira API calls go to for
// the site at baseURL. JIRA_API_URL wins when set: the site's gateway
// address (https://api.atlassian.com/ex/jira/<cloudId>), another Atlassian
// cloud variant's gateway, or the site URL itself to call it directly as
// before #9007. JIRA_API_URL describes the site in JIRA_BASE_URL, so it is
// honoured only when baseURL is that site (or JIRA_BASE_URL is unset); a
// caller that names another site with --jira-url gets discovery for that
// site instead of the first site's gateway. Otherwise the result is the
// gateway address for the site, with the cloud ID discovered from the
// site's unauthenticated tenant_info endpoint.
// The gateway accepts both a bot account's classic token and an
// Atlassian-managed service account's scoped token with Basic auth, so
// fullsend never needs to know which kind of account it holds. A discovery
// failure is an error rather than a fallback to the site URL: the site URL
// rejects a service account's token, and a silent fallback would turn that
// into an unexplained 401.
func resolveJiraAPIURL(ctx context.Context, baseURL string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("JIRA_API_URL")); v != "" && sameJiraSite(baseURL, os.Getenv("JIRA_BASE_URL")) {
		if err := jira.ValidateBaseURL(v); err != nil {
			return "", fmt.Errorf("JIRA_API_URL: %w", err)
		}
		return strings.TrimRight(v, "/"), nil
	}
	apiURL, err := jira.DiscoverAPIURL(ctx, nil, baseURL, jiraGatewayURL)
	if err != nil {
		return "", fmt.Errorf("JIRA_API_URL is unset and cloud ID discovery for %s failed: %w (set JIRA_API_URL to the site's API gateway URL, https://api.atlassian.com/ex/jira/<cloudId>, or to the site URL to call it directly)", baseURL, err)
	}
	return apiURL, nil
}

// sameJiraSite reports whether the site a caller asked about is the site
// JIRA_BASE_URL names, ignoring surrounding whitespace, a trailing slash and
// case. An unset JIRA_BASE_URL matches any site: there is then only one
// site in play, the one the caller named.
func sameJiraSite(baseURL, envBaseURL string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	e := norm(envBaseURL)
	return e == "" || e == norm(baseURL)
}

// newJiraClient builds the Jira client every fullsend command uses: Basic
// auth with the account email and API token, API calls to the URL
// resolveJiraAPIURL returns, and baseURL kept as the site URL for browse
// links shown to humans. The API URL is resolved on the first request, so
// building a client makes no network call. Callers check the email first:
// this client targets Jira Cloud only, and neither the site URL nor the
// gateway accepts a bare classic token via Bearer auth the way Data
// Center/Server PATs do, so omitting the email would silently send a scheme
// Cloud rejects, surfacing as a generic 401 rather than a clear
// configuration error.
func newJiraClient(baseURL, email, token string) (*jira.LiveClient, error) {
	return jira.New(token,
		jira.WithBaseURL(baseURL),
		jira.WithEmail(email),
		jira.WithAPIURLResolver(func(ctx context.Context) (string, error) {
			return resolveJiraAPIURL(ctx, baseURL)
		}),
	)
}

// jiraAPIURLExportTimeout bounds the cloud ID discovery exportJiraAPIURL
// makes at the start of a run.
const jiraAPIURLExportTimeout = 15 * time.Second

// exportJiraAPIURL resolves the Jira API URL once and sets JIRA_API_URL in
// the process environment, so host-side scripts (inheritedScriptEnv) and
// fullsend's own later Jira clients use the resolved value instead of
// repeating discovery. fullsend run calls it only for runs whose work item
// came from Jira; the reusable workflow sets JIRA_BASE_URL on every run, and
// a review or retro run has no reason to contact the Jira site. It is best
// effort: with no JIRA_BASE_URL there is nothing to resolve, and a discovery
// failure is reported through warn and left for the first Jira call to
// fail on.
func exportJiraAPIURL(ctx context.Context, warn func(string)) {
	if strings.TrimSpace(os.Getenv("JIRA_API_URL")) != "" {
		return
	}
	baseURL := strings.TrimSpace(os.Getenv("JIRA_BASE_URL"))
	if baseURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, jiraAPIURLExportTimeout)
	defer cancel()
	apiURL, err := resolveJiraAPIURL(ctx, baseURL)
	if err != nil {
		warn(fmt.Sprintf("Jira API URL not resolved; Jira calls will retry discovery: %v", err))
		return
	}
	if err := os.Setenv("JIRA_API_URL", apiURL); err != nil {
		warn(fmt.Sprintf("set JIRA_API_URL: %v", err))
	}
}
