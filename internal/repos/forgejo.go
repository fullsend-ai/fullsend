package repos

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// ForgeForgejo names Forgejo repositories in selectors and results.
const ForgeForgejo = "forgejo"

// ForgejoSelectorPrefix starts an instance-qualified Forgejo selector:
// forgejo:<alias>/<owner>/<repo>.
const ForgejoSelectorPrefix = ForgeForgejo + ":"

// ForgejoConfig is the forgejo section of repos.yaml. It declares named
// Forgejo instances; repository identity is the pair of instance alias and
// owner/repo path, so the same path may be declared on several instances.
//
//	forgejo:
//	  instances:
//	    - alias: codeberg
//	      url: https://codeberg.org
//	      repos:
//	        - name: acme/app
//
// API credentials are never stored here: each alias reads its token from
// the environment variable ForgejoTokenEnvVar returns.
type ForgejoConfig struct {
	Instances []ForgejoInstance `yaml:"instances"`
}

// ForgejoInstance is one named Forgejo instance.
type ForgejoInstance struct {
	// Alias names the instance in qualified selectors and in the
	// credential environment variable.
	Alias string `yaml:"alias"`
	// URL is the canonical base URL of the instance. It may include a path
	// prefix (https://example.com/forgejo), which every API request keeps.
	URL string `yaml:"url"`
	// Repos lists the managed repositories as owner/repo paths relative
	// to URL.
	Repos []ForgejoRepoEntry `yaml:"repos"`
}

// ForgejoRepoEntry is one repository declared on a Forgejo instance.
type ForgejoRepoEntry struct {
	Name string `yaml:"name"`
}

// ForgejoTarget is a declared Forgejo repository selected for an operation.
type ForgejoTarget struct {
	Alias   string
	BaseURL string
	Owner   string
	Repo    string
}

// Path returns owner/repo.
func (t ForgejoTarget) Path() string { return t.Owner + "/" + t.Repo }

// Selector returns the qualified selector forgejo:<alias>/<owner>/<repo>.
func (t ForgejoTarget) Selector() string { return ForgejoSelectorPrefix + t.Alias + "/" + t.Path() }

var (
	forgejoAliasPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?$`)
	// forgejoNamePattern matches a Forgejo owner or repository name.
	forgejoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	nonAlnumRun        = regexp.MustCompile(`[^A-Z0-9]+`)
)

// ForgejoTokenEnvVar returns the environment variable that supplies the
// API token for alias: FULLSEND_FORGEJO_<NORMALIZED_ALIAS>_TOKEN, where the
// alias is uppercased and every run of non-alphanumeric characters becomes
// one underscore.
func ForgejoTokenEnvVar(alias string) string {
	norm := strings.Trim(nonAlnumRun.ReplaceAllString(strings.ToUpper(alias), "_"), "_")
	return "FULLSEND_FORGEJO_" + norm + "_TOKEN"
}

// validate checks the forgejo section. A nil section is valid.
func (c *ForgejoConfig) validate() error {
	if c == nil {
		return nil
	}
	aliases := make(map[string]string)
	envVars := make(map[string]string)
	for i, inst := range c.Instances {
		field := fmt.Sprintf("forgejo.instances[%d]", i)
		if !forgejoAliasPattern.MatchString(inst.Alias) {
			return fmt.Errorf("%s.alias %q must be letters, digits, '.', '_' or '-', starting and ending with a letter or digit", field, inst.Alias)
		}
		key := strings.ToLower(inst.Alias)
		if prev, ok := aliases[key]; ok {
			return fmt.Errorf("%s.alias %q duplicates alias %q", field, inst.Alias, prev)
		}
		aliases[key] = inst.Alias
		env := ForgejoTokenEnvVar(inst.Alias)
		if prev, ok := envVars[env]; ok {
			return fmt.Errorf("%s.alias %q and alias %q share the credential variable %q; rename one", field, inst.Alias, prev, env)
		}
		envVars[env] = inst.Alias
		if err := validateForgejoURL(field+".url", inst.URL); err != nil {
			return err
		}
		seen := make(map[string]bool)
		for j, r := range inst.Repos {
			rfield := fmt.Sprintf("%s.repos[%d].name", field, j)
			owner, repo, ok := splitForgejoPath(r.Name)
			if !ok {
				return fmt.Errorf("%s %q must be an owner/repo path relative to the instance URL", rfield, r.Name)
			}
			k := strings.ToLower(owner + "/" + repo)
			if seen[k] {
				return fmt.Errorf("%s %q is declared more than once on instance %q", rfield, r.Name, inst.Alias)
			}
			seen[k] = true
		}
	}
	return nil
}

// validateForgejoURL requires an absolute https URL with an optional path
// prefix and no credentials, query, or fragment. Plain http is accepted
// only for loopback hosts, such as the local development stack.
func validateForgejoURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s %q must be an absolute https URL", field, raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("%s %q must use https (http is accepted only for loopback hosts)", field, raw)
		}
	default:
		return fmt.Errorf("%s %q must be an absolute https URL", field, raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return fmt.Errorf("%s %q must not include credentials, a query, or a fragment", field, raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// splitForgejoPath splits an owner/repo path.
func splitForgejoPath(p string) (owner, repo string, ok bool) {
	owner, repo, found := strings.Cut(p, "/")
	if !found || !forgejoNamePattern.MatchString(owner) || !forgejoNamePattern.MatchString(repo) {
		return "", "", false
	}
	if owner == "." || owner == ".." || repo == "." || repo == ".." {
		return "", "", false
	}
	return owner, repo, true
}

// HasForgejo reports whether the manifest declares any Forgejo repository.
func (m *Manifest) HasForgejo() bool {
	if m.Forgejo == nil {
		return false
	}
	for _, inst := range m.Forgejo.Instances {
		if len(inst.Repos) > 0 {
			return true
		}
	}
	return false
}

// hasNonForgejoRepos reports whether the github or gitlab section declares
// any repository.
func (m *Manifest) hasNonForgejoRepos() bool {
	return m.TotalRepoCount() > 0
}

// forgejoTargets returns every declared Forgejo repository in manifest
// order.
func (m *Manifest) forgejoTargets() []ForgejoTarget {
	if m.Forgejo == nil {
		return nil
	}
	var out []ForgejoTarget
	for _, inst := range m.Forgejo.Instances {
		for _, r := range inst.Repos {
			owner, repo, ok := splitForgejoPath(r.Name)
			if !ok {
				continue
			}
			out = append(out, ForgejoTarget{Alias: inst.Alias, BaseURL: inst.URL, Owner: owner, Repo: repo})
		}
	}
	return out
}

// ForgejoSelection is the result of SplitForgejoSelectors.
type ForgejoSelection struct {
	// Targets are the selected Forgejo repositories, deduplicated, in
	// manifest order.
	Targets []ForgejoTarget
	// Rest holds the selectors left for the github and gitlab sections.
	Rest []string
	// RunOthers reports whether the github/gitlab lifecycle should run:
	// with no selectors, when those sections declare repositories; with
	// selectors, when some selector was left for them.
	RunOthers bool
}

// SplitForgejoSelectors resolves the Forgejo part of a repository
// selection before any credential lookup or forge call.
//
// A qualified selector forgejo:<alias>/<owner>/<repo> (the owner/repo part
// may be a glob) selects only from that instance; an unknown alias or an
// undeclared repository is an error. A bare owner/repo selector or glob
// selects a Forgejo repository only when every path it matches is declared
// on exactly one instance; otherwise it fails and lists the qualified
// choices. A bare selector that matches no Forgejo repository is left for
// the github and gitlab sections, and fails as undeclared when the manifest
// has none. A bare selector that matches both a Forgejo repository and a
// github/gitlab declaration is ambiguous. With no selectors every declared
// Forgejo repository is selected.
func (m *Manifest) SplitForgejoSelectors(filter []string) (ForgejoSelection, error) {
	if m.Forgejo == nil {
		// Without a forgejo section every selector belongs to the
		// github/gitlab lifecycle, which keeps its own behavior.
		for _, s := range filter {
			if strings.HasPrefix(s, ForgejoSelectorPrefix) {
				return ForgejoSelection{}, fmt.Errorf("selector %q names a Forgejo instance, but repos.yaml declares none (declared aliases: (none))", s)
			}
		}
		return ForgejoSelection{Rest: filter, RunOthers: true}, nil
	}
	all := m.forgejoTargets()
	if len(filter) == 0 {
		return ForgejoSelection{Targets: all, RunOthers: m.hasNonForgejoRepos() || len(all) == 0}, nil
	}
	var sel ForgejoSelection
	picked := make(map[string]bool)
	add := func(t ForgejoTarget) {
		k := strings.ToLower(t.Selector())
		if !picked[k] {
			picked[k] = true
			sel.Targets = append(sel.Targets, t)
		}
	}
	for _, s := range filter {
		if strings.HasPrefix(s, ForgejoSelectorPrefix) {
			matches, err := m.matchQualifiedForgejo(all, s)
			if err != nil {
				return ForgejoSelection{}, err
			}
			for _, t := range matches {
				add(t)
			}
			continue
		}
		matches, err := matchBareForgejo(all, s)
		if err != nil {
			return ForgejoSelection{}, err
		}
		if len(matches) == 0 {
			sel.Rest = append(sel.Rest, s)
			continue
		}
		if err := forgejoAmbiguity(s, matches); err != nil {
			return ForgejoSelection{}, err
		}
		overlaps, err := m.overlapsNonForgejo(s, matches)
		if err != nil {
			return ForgejoSelection{}, err
		}
		if overlaps {
			return ForgejoSelection{}, fmt.Errorf("selector %q is ambiguous: it matches a github/gitlab declaration and %s; qualify the Forgejo repository as forgejo:<alias>/<owner>/<repo>",
				s, joinSelectors(matches))
		}
		for _, t := range matches {
			add(t)
		}
	}
	if len(sel.Rest) > 0 && !m.hasNonForgejoRepos() {
		return ForgejoSelection{}, fmt.Errorf("not declared in repos.yaml: %s; declare each repository under forgejo.instances[].repos first, or select a declared one as forgejo:<alias>/<owner>/<repo>",
			strings.Join(sel.Rest, ", "))
	}
	sel.RunOthers = len(sel.Rest) > 0
	return sel, nil
}

// overlapsNonForgejo reports whether a bare selector that matched the given
// Forgejo repositories also selects a github/gitlab declaration: the
// selector matches a declared entry name, or a declared glob entry covers
// one of the Forgejo paths. A glob selector that matches only Forgejo
// repositories (fj/* next to an unrelated gh/app) does not overlap.
func (m *Manifest) overlapsNonForgejo(selector string, matches []ForgejoTarget) (bool, error) {
	for _, platform := range []*PlatformConfig{m.GitHub, m.GitLab} {
		if platform == nil {
			continue
		}
		for _, e := range platform.Repos {
			if !isGlob(e.Name) {
				// A glob entry's text is a pattern, not a name, so only a
				// literal entry is matched against the selector by name.
				ok, err := matchesPattern(selector, e.Name)
				if err != nil {
					return false, fmt.Errorf("invalid glob pattern %q: %w", selector, err)
				}
				if ok {
					return true, nil
				}
				continue
			}
			// Two globs can select a repository neither pattern names
			// (org/*z and org/b* both select org/bz), so a glob selector
			// against a glob entry is ambiguous whenever the patterns
			// could intersect, even if the entry matches no Forgejo path.
			if isGlob(selector) && globsMayIntersect(selector, e.Name) {
				return true, nil
			}
			for _, t := range matches {
				ok, err := matchesPattern(e.Name, t.Path())
				if err != nil {
					return false, fmt.Errorf("matching manifest entry %q against %q: %w", e.Name, t.Path(), err)
				}
				if ok {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// matchQualifiedForgejo resolves forgejo:<alias>/<owner>/<repo>.
func (m *Manifest) matchQualifiedForgejo(all []ForgejoTarget, selector string) ([]ForgejoTarget, error) {
	rest := strings.TrimPrefix(selector, ForgejoSelectorPrefix)
	alias, pattern, ok := strings.Cut(rest, "/")
	if !ok || alias == "" || !strings.Contains(pattern, "/") {
		return nil, fmt.Errorf("selector %q must have the form forgejo:<alias>/<owner>/<repo>", selector)
	}
	known := false
	if m.Forgejo != nil {
		for _, inst := range m.Forgejo.Instances {
			if strings.EqualFold(inst.Alias, alias) {
				known = true
				break
			}
		}
	}
	if !known {
		return nil, fmt.Errorf("selector %q names unknown Forgejo instance %q; declared aliases: %s", selector, alias, m.forgejoAliasList())
	}
	var out []ForgejoTarget
	for _, t := range all {
		if !strings.EqualFold(t.Alias, alias) {
			continue
		}
		ok, err := matchesPattern(pattern, t.Path())
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", selector, err)
		}
		if ok {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("not declared in repos.yaml: %q; declare it under the %q instance's repos first", selector, alias)
	}
	return out, nil
}

func (m *Manifest) forgejoAliasList() string {
	if m.Forgejo == nil || len(m.Forgejo.Instances) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(m.Forgejo.Instances))
	for _, inst := range m.Forgejo.Instances {
		names = append(names, inst.Alias)
	}
	return strings.Join(names, ", ")
}

// matchBareForgejo returns every declared Forgejo repository a bare
// owner/repo selector or glob matches.
func matchBareForgejo(all []ForgejoTarget, selector string) ([]ForgejoTarget, error) {
	var out []ForgejoTarget
	for _, t := range all {
		ok, err := matchesPattern(selector, t.Path())
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", selector, err)
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// forgejoAmbiguity fails when any path a bare selector matched is declared
// on more than one instance.
func forgejoAmbiguity(selector string, matches []ForgejoTarget) error {
	byPath := make(map[string][]ForgejoTarget)
	for _, t := range matches {
		k := strings.ToLower(t.Path())
		byPath[k] = append(byPath[k], t)
	}
	var ambiguous []ForgejoTarget
	for _, ts := range byPath {
		if len(ts) > 1 {
			ambiguous = append(ambiguous, ts...)
		}
	}
	if len(ambiguous) == 0 {
		return nil
	}
	return fmt.Errorf("selector %q is ambiguous: it matches a repository declared on more than one Forgejo instance; nothing was changed. Use a qualified selector: %s",
		selector, joinSelectors(ambiguous))
}

func joinSelectors(ts []ForgejoTarget) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Selector())
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// RemoveForgejoRepos removes the targets' entries from the manifest and
// returns the qualified selectors that were removed.
func (m *Manifest) RemoveForgejoRepos(targets []ForgejoTarget) []string {
	if m.Forgejo == nil {
		return nil
	}
	drop := make(map[string]bool, len(targets))
	for _, t := range targets {
		drop[strings.ToLower(t.Selector())] = true
	}
	var removed []string
	for i := range m.Forgejo.Instances {
		inst := &m.Forgejo.Instances[i]
		kept := inst.Repos[:0:0]
		for _, r := range inst.Repos {
			sel := ForgejoSelectorPrefix + inst.Alias + "/" + r.Name
			if drop[strings.ToLower(sel)] {
				removed = append(removed, sel)
				continue
			}
			kept = append(kept, r)
		}
		inst.Repos = kept
	}
	return removed
}
