package jirapoll

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"golang.org/x/sync/errgroup"
)

var (
	githubReservedFirstSegments = map[string]struct{}{
		"about": {}, "account": {}, "advisories": {}, "apps": {}, "codespaces": {},
		"collections": {}, "customer-stories": {}, "discussions": {}, "enterprise": {},
		"enterprises": {}, "events": {}, "explore": {}, "features": {}, "gist": {},
		"import": {}, "issues": {}, "join": {}, "login": {}, "marketplace": {},
		"new": {}, "notifications": {}, "organizations": {}, "orgs": {}, "pricing": {},
		"pulls": {}, "readme": {}, "search": {}, "security": {}, "sessions": {},
		"settings": {}, "site": {}, "sponsors": {}, "team": {}, "topics": {},
		"trending": {}, "user-attachments": {}, "users": {},
	}

	bitbucketReservedFirstSegments = map[string]struct{}{
		"account": {}, "dashboard": {}, "product": {}, "profile": {},
		"repo": {}, "snippets": {}, "socialauth": {}, "workspaces": {},
	}

	gitlabReservedFirstSegments = map[string]struct{}{
		"-": {}, "abuse_reports": {}, "admin": {}, "api": {}, "assets": {},
		"dashboard": {}, "explore": {}, "groups": {}, "help": {}, "import": {},
		"jwt": {}, "oauth": {}, "profile": {}, "public": {}, "search": {},
		"snippets": {}, "uploads": {}, "users": {},
	}

	gitlabLegacyUIRoutes = map[string]struct{}{
		"badges": {}, "blame": {}, "blob": {}, "branches": {}, "commit": {},
		"commits": {}, "compare": {}, "container_registry": {}, "edit": {},
		"forks": {}, "graphs": {}, "issues": {}, "jobs": {}, "labels": {},
		"merge_requests": {}, "milestones": {}, "network": {}, "packages": {},
		"pipelines": {}, "raw": {}, "releases": {}, "snippets": {}, "tags": {},
		"tree": {}, "wikis": {},
	}
)

func hostFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func splitNonEmptyPathSegments(path string) ([]string, bool) {
	parts := strings.Split(path, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return nil, false
		}
	}
	return parts, true
}

// extractRepoSlug extracts and normalizes the "owner/repo" or
// "group/subgroup/repo" slug from a git repository URL.
func extractRepoSlug(rawURL string) string {
	return extractRepoSlugWithAllowedHost(rawURL, "")
}

// extractRepoSlugWithAllowedHost extracts and normalizes the repository slug
// from a git repository URL, additionally treating allowedGitLabHost (for
// example, Options.GitLabURL) as a known GitLab instance. Returns an empty
// string if the URL does not identify a repository link.
func extractRepoSlugWithAllowedHost(rawURL, allowedGitLabHost string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}

	var host, path string
	var isSSH bool

	if strings.HasPrefix(rawURL, "git@") {
		parts := strings.SplitN(rawURL, ":", 2)
		if len(parts) != 2 {
			return ""
		}
		host = strings.TrimPrefix(parts[0], "git@")
		path = parts[1]
		isSSH = true
	} else {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return ""
		}
		switch parsed.Scheme {
		case "http", "https":
		case "ssh":
			isSSH = true
		default:
			return ""
		}
		host = parsed.Host
		path = parsed.Path
	}

	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || path == "" {
		return ""
	}

	allowedHost := strings.ToLower(strings.TrimSpace(allowedGitLabHost))
	isGitHub := host == "github.com"
	isBitbucket := host == "bitbucket.org"
	isGitLab := host == "gitlab.com" ||
		strings.HasPrefix(host, "gitlab.") ||
		strings.HasSuffix(host, ".gitlab.com") ||
		(allowedHost != "" && host == allowedHost)

	if !isGitHub && !isBitbucket && !isGitLab {
		// Generic / self-hosted git hosts: only accept SSH URLs or git.* hosts
		// whose path explicitly ends with .git so arbitrary site links like
		// docs.example.com/download/tool.git are ignored.
		if !isSSH && !(strings.HasPrefix(host, "git.") && strings.HasSuffix(path, ".git")) {
			return ""
		}
	}

	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}

	if isGitHub || isBitbucket {
		path = strings.TrimSuffix(path, ".git")
		path = strings.Trim(path, "/")
		parts, ok := splitNonEmptyPathSegments(path)
		if !ok || len(parts) < 2 {
			return ""
		}
		first := strings.ToLower(parts[0])
		if isGitHub {
			if _, reserved := githubReservedFirstSegments[first]; reserved {
				return ""
			}
		} else {
			if _, reserved := bitbucketReservedFirstSegments[first]; reserved {
				return ""
			}
		}
		return strings.ToLower(parts[0] + "/" + parts[1])
	}

	if isGitLab {
		if idx := strings.Index(path, "/-/"); idx != -1 {
			path = path[:idx]
		} else {
			// Legacy GitLab UI URLs omit "/-/": only truncate at a UI route word
			// once past owner/repo (segment index >= 2) so nested subgroups named
			// "blob" or "issues" remain intact.
			path = strings.TrimSuffix(path, ".git")
			path = strings.Trim(path, "/")
			segs, ok := splitNonEmptyPathSegments(path)
			if !ok {
				return ""
			}
			for i := 2; i < len(segs); i++ {
				if _, isUI := gitlabLegacyUIRoutes[strings.ToLower(segs[i])]; isUI {
					segs = segs[:i]
					break
				}
			}
			path = strings.Join(segs, "/")
		}
		path = strings.TrimSuffix(path, ".git")
		path = strings.Trim(path, "/")
		parts, ok := splitNonEmptyPathSegments(path)
		if !ok || len(parts) < 2 {
			return ""
		}
		if _, reserved := gitlabReservedFirstSegments[strings.ToLower(parts[0])]; reserved {
			return ""
		}
		return strings.ToLower(strings.Join(parts, "/"))
	}

	// Generic git host: strip "/-/" UI marker if present and preserve full group path.
	if idx := strings.Index(path, "/-/"); idx != -1 {
		path = path[:idx]
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	parts, ok := splitNonEmptyPathSegments(path)
	if !ok || len(parts) < 2 {
		return ""
	}
	return strings.ToLower(strings.Join(parts, "/"))
}

// filterRouting filters candidate issues based on attached repository links
// and configured component. Returns an error if all candidates fail remote-link
// lookup.
func (p *Poller) filterRouting(ctx context.Context, candidates []jira.Issue) ([]jira.Issue, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	routed, linkErrors := p.filterRoutingPage(ctx, candidates)
	if linkErrors == len(candidates) {
		return nil, fmt.Errorf("all %d candidates dropped by remote-link lookup errors", len(candidates))
	}
	return routed, nil
}

// filterRoutingPage evaluates repository routing for one page of candidates,
// returning routed issues and the count of candidates dropped due to
// remote-link API errors.
func (p *Poller) filterRoutingPage(ctx context.Context, candidates []jira.Issue) ([]jira.Issue, int) {
	if len(candidates) == 0 {
		return candidates, 0
	}

	targetRepo := strings.ToLower(strings.TrimSpace(p.opts.TargetRepo))
	allowedGitLabHost := hostFromURL(p.opts.GitLabURL)

	// Fetch remote links concurrently with a bounded worker pool (max 8 concurrent)
	// while preserving candidate order.
	type linkResult struct {
		links []jira.RemoteLink
		err   error
	}

	results := make([]linkResult, len(candidates))
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(8)

	for i, issue := range candidates {
		i, issueKey := i, issue.Key
		g.Go(func() error {
			links, err := p.client.ListRemoteLinks(gCtx, issueKey)
			results[i] = linkResult{links: links, err: err}
			return nil
		})
	}
	_ = g.Wait()

	var routed []jira.Issue
	var linkErrors int

	for i, issue := range candidates {
		res := results[i]
		if res.err != nil {
			// Fail-safe: if remote links lookup failed (transient error, auth failure, etc.),
			// do NOT fall back to component or permissive claiming. Skip this issue for this cycle
			// to avoid claiming an issue that might belong to another repository.
			log.Printf("WARNING: listing remote links for %s: %v (skipping candidate to avoid misrouting)", issue.Key, res.err)
			linkErrors++
			continue
		}

		var attachedRepos []string
		for _, link := range res.links {
			if slug := extractRepoSlugWithAllowedHost(link.Object.URL, allowedGitLabHost); slug != "" {
				attachedRepos = append(attachedRepos, slug)
			}
		}

		// Priority 1: Attached Git repository links
		if len(attachedRepos) > 0 {
			matched := false
			for _, r := range attachedRepos {
				if r == targetRepo {
					matched = true
					break
				}
			}

			if matched {
				log.Printf("routing %s to %s via attached repository link", issue.Key, p.opts.TargetRepo)
				routed = append(routed, issue)
			} else {
				log.Printf("skipping %s: attached repository link(s) %v do not match target repo %q", issue.Key, attachedRepos, p.opts.TargetRepo)
			}
			continue
		}

		// Priority 2: Jira Component fallback
		if p.opts.JiraComponent != "" {
			componentMatched := false
			for _, comp := range issue.Fields.Components {
				if strings.EqualFold(comp.Name, p.opts.JiraComponent) {
					componentMatched = true
					break
				}
			}

			if componentMatched {
				log.Printf("routing %s to %s via component %q", issue.Key, p.opts.TargetRepo, p.opts.JiraComponent)
				routed = append(routed, issue)
			} else {
				log.Printf("skipping %s: no attached repo and components do not match %q", issue.Key, p.opts.JiraComponent)
			}
			continue
		}

		// Priority 3: No attached repo links and no component configured -> keep candidate
		routed = append(routed, issue)
	}

	return routed, linkErrors
}
