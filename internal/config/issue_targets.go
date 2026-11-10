package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Issue-tracker provider names accepted as keys under tracker:.
const (
	TrackingProviderGitHub = "github"
	TrackingProviderGitLab = "gitlab"
	TrackingProviderJira   = "jira"
)

// errLegacyCreateIssues reports a leftover top-level create_issues block,
// which was replaced by the provider-specific tracker mapping (#9074).
var errLegacyCreateIssues = errors.New("create_issues is no longer supported: move create_issues.allow_targets to tracker.<provider>.allowed_targets " +
	"(tracker.github.allowed_targets.orgs/repos, tracker.gitlab.allowed_targets.groups/repos, or tracker.jira.allowed_targets.projects)")

// errLegacyScalarTracker reports a scalar tracker value (tracker: jira),
// which the provider-discriminated tracker mapping replaced (#9074).
func errLegacyScalarTracker(value string) error {
	provider := value
	if !slices.Contains([]string{TrackingProviderGitHub, TrackingProviderGitLab, TrackingProviderJira}, provider) {
		provider = "<provider>"
	}
	return fmt.Errorf("tracker: the scalar form (tracker: %s) is no longer supported: tracker is now a provider mapping, "+
		"so replace it with \"tracker:\" containing a \"%s: {}\" block (add allowed_targets under it as needed, "+
		"for example tracker.github.allowed_targets.orgs/repos, tracker.gitlab.allowed_targets.groups/repos, or tracker.jira.allowed_targets.projects)",
		value, provider)
}

// TrackingConfig is the value of the tracker key: a provider-discriminated
// mapping that selects the issue-tracker provider and holds its settings.
// At most one provider mapping may be set; the one that is set is the
// selected provider. A nil TrackingConfig, or one with no provider
// mapping, selects no provider and allows no issue destinations beyond the
// source repository.
type TrackingConfig struct {
	GitHub *GitHubTrackingConfig `yaml:"github,omitempty"`
	GitLab *GitLabTrackingConfig `yaml:"gitlab,omitempty"`
	Jira   *JiraTrackingConfig   `yaml:"jira,omitempty"`
}

// trackingPlain has TrackingConfig's fields but not its UnmarshalYAML, so
// it can be decoded strictly (rejecting unknown keys) where the custom
// unmarshaler would drop the strict-fields option.
type trackingPlain TrackingConfig

// UnmarshalYAML implements yaml.Unmarshaler. The legacy scalar form
// (tracker: jira) is rejected with a migration message instead of a
// generic type error; a mapping decodes as usual.
func (t *TrackingConfig) UnmarshalYAML(n *yaml.Node) error {
	if r := resolveAlias(n); r.Kind == yaml.ScalarNode {
		return errLegacyScalarTracker(r.Value)
	}
	return n.Decode((*trackingPlain)(t))
}

// GitHubTrackingConfig holds GitHub issue-tracking settings.
type GitHubTrackingConfig struct {
	AllowedTargets GitHubAllowedTargets `yaml:"allowed_targets"`
}

// GitHubAllowedTargets lists the GitHub destinations agents may create
// issues in beyond the source repository. The scopes are additive: a
// destination is allowed if its owner is listed in Orgs or the repository
// is listed in Repos.
type GitHubAllowedTargets struct {
	Orgs  []string `yaml:"orgs,omitempty"`
	Repos []string `yaml:"repos,omitempty"`
}

// GitLabTrackingConfig holds GitLab issue-tracking settings.
type GitLabTrackingConfig struct {
	AllowedTargets GitLabAllowedTargets `yaml:"allowed_targets"`
}

// GitLabAllowedTargets lists the GitLab destinations agents may create
// issues in beyond the source project. The scopes are additive: a
// destination is allowed if it is inside a group listed in Groups or its
// full project path is listed in Repos.
type GitLabAllowedTargets struct {
	Groups []string `yaml:"groups,omitempty"`
	Repos  []string `yaml:"repos,omitempty"`
}

// DefaultJiraAPIURL is the Jira API gateway URL used when
// tracker.jira.api_url is unset.
const DefaultJiraAPIURL = "https://api.atlassian.com"

// DefaultJiraPollQuery is the JQL predicate the Jira poller uses when
// tracker.jira.poll.query is unset. It is always scoped to the configured
// project (see JiraTrackingConfig.PollJQL).
const DefaultJiraPollQuery = "statusCategory != Done ORDER BY updated DESC"

// JiraTrackingConfig holds Jira issue-tracking settings: the Jira site and
// project this repository is connected to, the cross-project issue
// destinations, and the poller settings. Credentials (the Jira token and
// account email) are never part of this mapping; they are supplied as
// repository secrets.
type JiraTrackingConfig struct {
	// BaseURL is the Jira site URL (for example
	// https://example.atlassian.net). Required when any connection or
	// poll setting is present.
	BaseURL string `yaml:"base_url,omitempty"`
	// APIURL is the Jira API gateway URL; empty means DefaultJiraAPIURL.
	APIURL string `yaml:"api_url,omitempty"`
	// RunLabel is added at the start of Jira-sourced runs. Empty disables it.
	RunLabel string `yaml:"run_label,omitempty"`
	// Project is the key of the single Jira project configured for this
	// repository. Required when any connection or poll setting is present.
	Project        string             `yaml:"project,omitempty"`
	AllowedTargets JiraAllowedTargets `yaml:"allowed_targets"`
	Poll           *JiraPollConfig    `yaml:"poll,omitempty"`
}

// JiraPollConfig holds the Jira poller settings.
type JiraPollConfig struct {
	// Enabled installs (true) or removes (false) the managed poll
	// workflow. Nil means the workflow is not installed.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Query is the JQL the poller runs, before project scoping; empty
	// means DefaultJiraPollQuery.
	Query string `yaml:"query,omitempty"`
}

// jiraTrackingKeys, jiraPollKeys, and jiraAllowedTargetsKeys are the keys
// accepted in tracker.jira, tracker.jira.poll, and
// tracker.jira.allowed_targets.
var (
	jiraTrackingKeys       = map[string]bool{"base_url": true, "api_url": true, "run_label": true, "project": true, "allowed_targets": true, "poll": true}
	jiraPollKeys           = map[string]bool{"enabled": true, "query": true}
	jiraAllowedTargetsKeys = map[string]bool{"projects": true}
)

// jiraCredentialKeys are keys rejected in tracker.jira with a pointer to
// the repository secrets: credentials never belong in config files.
var jiraCredentialKeys = map[string]bool{
	"token": true, "api_token": true, "jira_token": true, "email": true, "user_email": true,
	"jira_email": true, "password": true, "secret": true, "credentials": true, "username": true,
	"client_id": true, "client_secret": true, "oauth_client_id": true, "oauth_client_secret": true,
}

// jiraTrackingPlain has JiraTrackingConfig's fields but not its
// UnmarshalYAML, so it can be decoded without recursion.
type jiraTrackingPlain JiraTrackingConfig

// UnmarshalYAML implements yaml.Unmarshaler. A custom unmarshaler drops
// yaml.v3's strict-field option for everything below it, so the keys of
// tracker.jira (and of its poll and allowed_targets mappings) are checked
// here. Credential-like keys get a message pointing at the repository
// secrets instead of the generic unknown-field error.
func (j *JiraTrackingConfig) UnmarshalYAML(n *yaml.Node) error {
	if r := resolveAlias(n); r.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(r.Content); i += 2 {
			key := r.Content[i].Value
			if jiraCredentialKeys[strings.ToLower(key)] {
				return managedFieldError(r.Content[i], fmt.Sprintf("tracker.jira: %q must not be set in config: "+
					"supply the Jira token and account email as the FULLSEND_JIRA_TOKEN and FULLSEND_JIRA_EMAIL repository secrets, "+
					"or OAuth 2.0 client credentials as FULLSEND_JIRA_CLIENT_ID and FULLSEND_JIRA_CLIENT_SECRET", key))
			}
		}
		if err := checkMappingKeys(r, jiraTrackingKeys, "tracker.jira"); err != nil {
			return err
		}
		for _, sub := range []struct {
			key     string
			allowed map[string]bool
		}{{"poll", jiraPollKeys}, {"allowed_targets", jiraAllowedTargetsKeys}} {
			if v := mappingNode(r, sub.key); v != nil && v.Kind == yaml.MappingNode {
				if err := checkMappingKeys(v, sub.allowed, "tracker.jira."+sub.key); err != nil {
					return err
				}
			}
		}
	}
	return n.Decode((*jiraTrackingPlain)(j))
}

// EffectiveAPIURL returns api_url, or DefaultJiraAPIURL when it is unset.
func (j *JiraTrackingConfig) EffectiveAPIURL() string {
	if j == nil || j.APIURL == "" {
		return DefaultJiraAPIURL
	}
	return j.APIURL
}

// PollEnabled reports whether poll.enabled is explicitly true. An omitted
// poll mapping or poll.enabled means the poll workflow is not installed.
func (j *JiraTrackingConfig) PollEnabled() bool {
	return j != nil && j.Poll != nil && j.Poll.Enabled != nil && *j.Poll.Enabled
}

// PollQuery returns poll.query, or DefaultJiraPollQuery when it is unset.
func (j *JiraTrackingConfig) PollQuery() string {
	if j == nil || j.Poll == nil || strings.TrimSpace(j.Poll.Query) == "" {
		return DefaultJiraPollQuery
	}
	return j.Poll.Query
}

// PollJQL returns the poller's JQL: the configured (or default) query
// scoped to the repository's project (see ScopeJQLToProject).
func (j *JiraTrackingConfig) PollJQL() string {
	return ScopeJQLToProject(j.Project, j.PollQuery())
}

// HasConnectionSettings reports whether the mapping sets any setting
// beyond allowed_targets (base_url, api_url, project, or poll).
func (j *JiraTrackingConfig) HasConnectionSettings() bool {
	return j != nil && (j.BaseURL != "" || j.APIURL != "" || j.Project != "" || j.RunLabel != "" || j.Poll != nil)
}

// jqlOrderByRe finds a trailing ORDER BY clause, case-insensitively.
var jqlOrderByRe = regexp.MustCompile(`(?i)\border\s+by\b`)

// ScopeJQLToProject scopes query to project as
// `project = "<project>" AND (<predicate>)`, keeping a trailing ORDER BY
// clause after the closing parenthesis. The ORDER BY is located outside
// quoted strings, so a predicate that mentions "order by" inside a quoted
// value is not split. An empty predicate (a query that is only an
// ORDER BY clause) yields `project = "<project>"` plus the ORDER BY.
func ScopeJQLToProject(project, query string) string {
	predicate, orderBy := splitJQLOrderBy(query)
	predicate = strings.TrimSpace(predicate)
	// Quote the key so a reserved-word key (AND, OR, IN) stays a value.
	scoped := fmt.Sprintf("project = %q", project)
	if predicate != "" {
		scoped += " AND (" + predicate + ")"
	}
	if orderBy != "" {
		scoped += " " + orderBy
	}
	return scoped
}

// splitJQLOrderBy splits query at its last ORDER BY that is outside a
// quoted string, returning the predicate and the ORDER BY clause.
func splitJQLOrderBy(query string) (string, string) {
	query = strings.TrimSpace(query)
	quoted := quotedRanges(query)
	matches := jqlOrderByRe.FindAllStringIndex(query, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		start := matches[i][0]
		if inRanges(start, quoted) {
			continue
		}
		return query[:start], strings.TrimSpace(query[start:])
	}
	return query, ""
}

// quotedRanges returns the [start, end) byte ranges of single- or
// double-quoted strings in s, honoring backslash escapes.
func quotedRanges(s string) [][2]int {
	var out [][2]int
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0 && c == quote:
			out = append(out, [2]int{start, i + 1})
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
			start = i
		}
	}
	if quote != 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

func inRanges(pos int, ranges [][2]int) bool {
	for _, r := range ranges {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}

// JiraAllowedTargets lists the Jira projects (by key) agents may create
// issues in beyond the source project.
type JiraAllowedTargets struct {
	Projects []string `yaml:"projects,omitempty"`
}

// Provider returns the selected provider name (TrackingProviderGitHub,
// TrackingProviderGitLab, or TrackingProviderJira), or "" when no
// provider mapping is set. It also returns "" when more than one mapping
// is set: runtime loading does not run validateTracking, so an ambiguous
// mapping fails closed (selects no provider and grants nothing) instead of
// silently picking one.
func (t *TrackingConfig) Provider() string {
	providers := t.providers()
	if len(providers) != 1 {
		return ""
	}
	return providers[0]
}

func (t *TrackingConfig) providers() []string {
	if t == nil {
		return nil
	}
	var out []string
	if t.GitHub != nil {
		out = append(out, TrackingProviderGitHub)
	}
	if t.GitLab != nil {
		out = append(out, TrackingProviderGitLab)
	}
	if t.Jira != nil {
		out = append(out, TrackingProviderJira)
	}
	return out
}

// targetGrants is the provider-neutral view of an allowed_targets mapping:
// scopes (GitHub orgs, GitLab groups) admit every destination under them,
// and targets (repositories, project paths, Jira project keys) admit
// exactly themselves.
type targetGrants struct {
	provider string
	scopes   []string
	targets  []string
}

func (t *TrackingConfig) grants() targetGrants {
	switch t.Provider() {
	case TrackingProviderGitHub:
		return targetGrants{provider: TrackingProviderGitHub, scopes: t.GitHub.AllowedTargets.Orgs, targets: t.GitHub.AllowedTargets.Repos}
	case TrackingProviderGitLab:
		return targetGrants{provider: TrackingProviderGitLab, scopes: t.GitLab.AllowedTargets.Groups, targets: t.GitLab.AllowedTargets.Repos}
	case TrackingProviderJira:
		return targetGrants{provider: TrackingProviderJira, targets: t.Jira.AllowedTargets.Projects}
	}
	return targetGrants{}
}

func (g targetGrants) empty() bool {
	return len(g.scopes) == 0 && len(g.targets) == 0
}

// coveredByScope reports whether destination (a scope or a target) is admitted by
// one of g's scopes: equal to it, or nested under it.
func (g targetGrants) coveredByScope(destination string) bool {
	for _, scope := range g.scopes {
		if destination == scope || strings.HasPrefix(destination, scope+"/") {
			return true
		}
	}
	return false
}

// trackingWidened reports whether candidate grants issue-creation access
// current does not. Scopes and targets are compared by what they grant,
// not list by list: a candidate repository inside a current org or group
// admits nothing new, so replacing an org with one of its repositories is
// a narrowing. Switching to a different provider widens whenever the
// candidate lists any destination.
func trackingWidened(current, candidate *TrackingConfig) bool {
	cur := current.grants()
	cand := candidate.grants()
	if cand.empty() {
		return false
	}
	if cur.provider != cand.provider {
		return true
	}
	for _, scope := range cand.scopes {
		if !cur.coveredByScope(scope) {
			return true
		}
	}
	for _, target := range cand.targets {
		if slices.Contains(cur.targets, target) || cur.coveredByScope(target) {
			continue
		}
		return true
	}
	return false
}

func formatTrackingTargets(t *TrackingConfig) string {
	switch t.Provider() {
	case TrackingProviderGitHub:
		return "github: orgs=" + formatStringList(t.GitHub.AllowedTargets.Orgs) + " repos=" + formatStringList(t.GitHub.AllowedTargets.Repos)
	case TrackingProviderGitLab:
		return "gitlab: groups=" + formatStringList(t.GitLab.AllowedTargets.Groups) + " repos=" + formatStringList(t.GitLab.AllowedTargets.Repos)
	case TrackingProviderJira:
		return "jira: projects=" + formatStringList(t.Jira.AllowedTargets.Projects)
	}
	return "unset"
}

// trackingSafetyKey names the allowed_targets key of the provider that
// candidate selects (falling back to current's) in safety reports.
func trackingSafetyKey(current, candidate *TrackingConfig) string {
	provider := candidate.Provider()
	if provider == "" {
		provider = current.Provider()
	}
	if provider == "" {
		return "tracker"
	}
	return "tracker." + provider + ".allowed_targets"
}

func cloneTracking(src *TrackingConfig) *TrackingConfig {
	if src == nil {
		return nil
	}
	out := &TrackingConfig{}
	if src.GitHub != nil {
		out.GitHub = &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{
			Orgs:  cloneStringSlice(src.GitHub.AllowedTargets.Orgs),
			Repos: cloneStringSlice(src.GitHub.AllowedTargets.Repos),
		}}
	}
	if src.GitLab != nil {
		out.GitLab = &GitLabTrackingConfig{AllowedTargets: GitLabAllowedTargets{
			Groups: cloneStringSlice(src.GitLab.AllowedTargets.Groups),
			Repos:  cloneStringSlice(src.GitLab.AllowedTargets.Repos),
		}}
	}
	if src.Jira != nil {
		out.Jira = &JiraTrackingConfig{
			BaseURL:  src.Jira.BaseURL,
			APIURL:   src.Jira.APIURL,
			RunLabel: src.Jira.RunLabel,
			Project:  src.Jira.Project,
			AllowedTargets: JiraAllowedTargets{
				Projects: cloneStringSlice(src.Jira.AllowedTargets.Projects),
			},
		}
		if p := src.Jira.Poll; p != nil {
			poll := &JiraPollConfig{Query: p.Query}
			if p.Enabled != nil {
				enabled := *p.Enabled
				poll.Enabled = &enabled
			}
			out.Jira.Poll = poll
		}
	}
	return out
}

// jiraProjectKeyRe matches a Jira project key: an uppercase letter
// followed by uppercase letters, digits, or underscores.
var jiraProjectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// validPathSegments reports whether p is a slash-separated path with at
// least minSegments segments and no empty segment.
func validPathSegments(p string, minSegments int) bool {
	segments := strings.Split(p, "/")
	if len(segments) < minSegments {
		return false
	}
	for _, s := range segments {
		if s == "" {
			return false
		}
	}
	return true
}

func validateTracking(cfg *TrackingConfig) error {
	if cfg == nil {
		return nil
	}
	if providers := cfg.providers(); len(providers) > 1 {
		return fmt.Errorf("tracker: at most one provider mapping may be set, found %s", strings.Join(providers, ", "))
	}
	if gh := cfg.GitHub; gh != nil {
		for _, org := range gh.AllowedTargets.Orgs {
			if !validPathSegments(org, 1) || strings.Contains(org, "/") {
				return fmt.Errorf("tracker.github: org %q in allowed_targets.orgs must be a non-empty organization name", org)
			}
		}
		for _, repo := range gh.AllowedTargets.Repos {
			if !validPathSegments(repo, 2) || strings.Count(repo, "/") != 1 {
				return fmt.Errorf("tracker.github: repo %q in allowed_targets.repos must be owner/repo", repo)
			}
		}
	}
	if gl := cfg.GitLab; gl != nil {
		for _, group := range gl.AllowedTargets.Groups {
			if !validPathSegments(group, 1) {
				return fmt.Errorf("tracker.gitlab: group %q in allowed_targets.groups must be a group path", group)
			}
		}
		for _, repo := range gl.AllowedTargets.Repos {
			if !validPathSegments(repo, 2) {
				return fmt.Errorf("tracker.gitlab: repo %q in allowed_targets.repos must be a full project path (group/project)", repo)
			}
		}
	}
	if jira := cfg.Jira; jira != nil {
		if err := jira.ValidateConnection(); err != nil {
			return err
		}
		for _, project := range jira.AllowedTargets.Projects {
			if !jiraProjectKeyRe.MatchString(project) {
				return fmt.Errorf("tracker.jira: project %q in allowed_targets.projects must be a Jira project key (for example PROJ)", project)
			}
		}
	}
	return nil
}

// ValidateConnection checks the Jira site, API gateway, project, and poll
// settings. base_url and project have no defaults, so both are required
// once any of these settings is present; a mapping that only selects Jira
// (jira: {}), such as one migrated from the legacy scalar tracker form,
// stays valid. Runtime loading does not run Validate, so commands that
// consume these settings call it directly.
func (j *JiraTrackingConfig) ValidateConnection() error {
	if j != nil && strings.ContainsFunc(j.RunLabel, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("tracker.jira.run_label must not contain whitespace or control characters")
	}
	if !j.HasConnectionSettings() {
		return nil
	}
	if j.BaseURL == "" {
		return fmt.Errorf("tracker.jira: base_url is required (the Jira site URL, for example https://example.atlassian.net)")
	}
	if err := validateJiraURL("base_url", j.BaseURL); err != nil {
		return err
	}
	if j.APIURL != "" {
		if err := validateJiraURL("api_url", j.APIURL); err != nil {
			return err
		}
	}
	if j.Project == "" {
		return fmt.Errorf("tracker.jira: project is required (the Jira project key for this repository, for example PROJ)")
	}
	if !jiraProjectKeyRe.MatchString(j.Project) {
		return fmt.Errorf("tracker.jira: project %q must be a Jira project key (for example PROJ)", j.Project)
	}
	if j.Poll != nil && !jqlParensBalanced(j.Poll.Query) {
		return fmt.Errorf("tracker.jira: poll.query has unbalanced parentheses: the query is wrapped in parentheses to scope it to the project")
	}
	return nil
}

// jqlParensBalanced reports whether the parentheses of query, ignoring
// those inside quoted strings, are balanced and never close before they
// open.
func jqlParensBalanced(query string) bool {
	quoted := quotedRanges(query)
	depth := 0
	for i := 0; i < len(query); i++ {
		if inRanges(i, quoted) {
			continue
		}
		switch query[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// validateJiraURL requires an https URL with a host and no credentials,
// query, or fragment.
func validateJiraURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		// The raw value is left out of the error: it may embed credentials.
		return fmt.Errorf("tracker.jira: %s must be an https URL without credentials, query, or fragment", field)
	}
	return nil
}

// DefaultIssueCreationRepos returns the cross-repository issue destinations
// granted by a fresh GitHub installation without a preset or explicit
// tracker setting. The source repository is implicitly allowed by the
// post-scripts. Each call returns its own slice so callers can customize it
// independently.
func DefaultIssueCreationRepos() []string {
	return []string{DefaultUpstreamRepo, DefaultAgentsRepo}
}

// DefaultIssueTracking returns the tracker configuration a fresh
// installation without a preset records for the given forge: the
// upstream repositories on GitHub, and an explicit empty allowed_targets
// on GitLab, where GitHub upstream names are not destinations. Any other
// forge gets nil.
func DefaultIssueTracking(forge string) *TrackingConfig {
	switch forge {
	case TrackingProviderGitHub:
		return &TrackingConfig{GitHub: &GitHubTrackingConfig{
			AllowedTargets: GitHubAllowedTargets{Repos: DefaultIssueCreationRepos()},
		}}
	case TrackingProviderGitLab:
		return &TrackingConfig{GitLab: &GitLabTrackingConfig{}}
	}
	return nil
}
