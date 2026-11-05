package jira

import (
	"fmt"
	"os"
	"strings"
)

// Environment variables carrying the credentials for Fullsend-managed Jira
// API calls. They are repository secrets and never part of config files.
const (
	// TokenEnvVar holds the scoped Jira API token. Required.
	TokenEnvVar = "FULLSEND_JIRA_TOKEN"
	// EmailEnvVar holds the Atlassian account email of a bot user. Optional:
	// when set, the token is a scoped user token sent with Basic auth; when
	// unset, the token is a scoped service-account token sent as a Bearer
	// token. The token string itself does not reveal its type.
	EmailEnvVar = "FULLSEND_JIRA_EMAIL"
)

// DefaultAPIURL is the Atlassian API gateway used when no API URL is
// configured. It matches config.DefaultJiraAPIURL, the default of
// tracker.jira.api_url.
const DefaultAPIURL = "https://api.atlassian.com"

// Credentials are the secrets used by NewScoped.
type Credentials struct {
	// Token is a scoped API token (user or service account).
	Token string
	// Email selects bot-user Basic auth when non-empty; otherwise the
	// token is sent as a Bearer token (service account).
	Email string
}

// CredentialsFromEnv reads FULLSEND_JIRA_TOKEN (required) and
// FULLSEND_JIRA_EMAIL (optional) through getenv, or os.Getenv when getenv
// is nil. Surrounding whitespace, such as a trailing newline in a stored
// secret, is trimmed. Errors name the variables but never their values.
func CredentialsFromEnv(getenv func(string) string) (Credentials, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	creds := Credentials{
		Token: strings.TrimSpace(getenv(TokenEnvVar)),
		Email: strings.TrimSpace(getenv(EmailEnvVar)),
	}
	if creds.Token == "" {
		return Credentials{}, fmt.Errorf("jira: %s is required", TokenEnvVar)
	}
	return creds, nil
}

// ScopedConfig holds the non-secret connection settings for NewScoped,
// taken from the effective tracker.jira config.
type ScopedConfig struct {
	// BaseURL is the Jira site URL (tracker.jira.base_url). It is used to
	// discover the cloud ID and for issue browse links.
	BaseURL string
	// APIURL is the API gateway URL (tracker.jira.api_url); empty means
	// DefaultAPIURL.
	APIURL string
}

// NewScoped is the shared constructor for Fullsend-managed Jira API calls
// with scoped API tokens. Requests go through the API gateway at
// {APIURL}/ex/jira/{cloudId}/rest/api/3/..., with the cloud ID discovered
// from BaseURL (see WithAPIGateway). Auth is Basic email:token when
// creds.Email is set (bot user) and Bearer token otherwise (service
// account). opts are applied after the connection settings, for example
// WithHTTPClient.
func NewScoped(cfg ScopedConfig, creds Credentials, opts ...Option) (*LiveClient, error) {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	all := append([]Option{
		WithBaseURL(cfg.BaseURL),
		WithAPIGateway(apiURL),
		WithEmail(creds.Email),
	}, opts...)
	return New(creds.Token, all...)
}
