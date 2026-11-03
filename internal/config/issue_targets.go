package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

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

// JiraTrackingConfig holds Jira issue-tracking settings.
type JiraTrackingConfig struct {
	AllowedTargets JiraAllowedTargets `yaml:"allowed_targets"`
}

// JiraAllowedTargets lists the Jira projects (by key) agents may create
// issues in beyond the source project.
type JiraAllowedTargets struct {
	Projects []string `yaml:"projects,omitempty"`
}

// Provider returns the selected provider name (TrackingProviderGitHub,
// TrackingProviderGitLab, or TrackingProviderJira), or "" when no
// provider mapping is set.
func (t *TrackingConfig) Provider() string {
	providers := t.providers()
	if len(providers) == 0 {
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
		out.Jira = &JiraTrackingConfig{AllowedTargets: JiraAllowedTargets{
			Projects: cloneStringSlice(src.Jira.AllowedTargets.Projects),
		}}
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
		for _, project := range jira.AllowedTargets.Projects {
			if !jiraProjectKeyRe.MatchString(project) {
				return fmt.Errorf("tracker.jira: project %q in allowed_targets.projects must be a Jira project key (for example PROJ)", project)
			}
		}
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
