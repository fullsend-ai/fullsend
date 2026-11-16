package jirapoll

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
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

	gitlabReservedWildcardRoutes = map[string]struct{}{
		"badges": {}, "blame": {}, "blob": {}, "commits": {},
		"edit": {}, "files": {}, "new": {}, "raw": {},
		"refs": {}, "tree": {}, "wikis": {},
	}

	gitlabLegacyEntityRoutes = map[string]struct{}{
		"commit": {}, "compare": {}, "container_registry": {},
		"forks": {}, "issues": {}, "jobs": {}, "labels": {},
		"merge_requests": {}, "milestones": {}, "packages": {},
		"pipelines": {}, "releases": {}, "snippets": {}, "tags": {},
	}
)

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHexCommitRef(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

func looksLikeVersionTag(s string) bool {
	if len(s) >= 2 && (s[0] == 'v' || s[0] == 'V') && s[1] >= '0' && s[1] <= '9' {
		return true
	}
	return len(s) >= 1 && s[0] >= '0' && s[0] <= '9' && (strings.ContainsRune(s, '.') || isDigitsOnly(s))
}

func isLegacyGitLabEntitySubpath(route string, tail []string) bool {
	if len(tail) == 0 {
		return false
	}
	switch route {
	case "issues", "merge_requests", "milestones", "pipelines", "snippets":
		return isDigitsOnly(tail[0]) || (len(tail) == 1 && strings.EqualFold(tail[0], "new"))
	case "jobs", "packages", "container_registry":
		return isDigitsOnly(tail[0])
	case "forks", "labels":
		return len(tail) == 1 && strings.EqualFold(tail[0], "new")
	case "commit":
		return isHexCommitRef(tail[0])
	case "compare":
		return strings.Contains(strings.Join(tail, "/"), "..")
	default: // "releases", "tags"
		return looksLikeVersionTag(tail[0]) || (route == "releases" && len(tail) == 1 && strings.EqualFold(tail[0], "new"))
	}
}

func parseGitLabInstance(rawURL string) (host, pathPrefix string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", ""
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	return strings.ToLower(parsed.Hostname()), strings.Trim(parsed.Path, "/")
}

func hostFromURL(rawURL string) string {
	host, _ := parseGitLabInstance(rawURL)
	return host
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
// from a git repository URL, additionally treating allowedGitLabURL (for
// example, Options.GitLabURL) as a known GitLab instance. Returns an empty
// string if the URL does not identify a repository link.
func extractRepoSlugWithAllowedHost(rawURL, allowedGitLabURL string) string {
	_, slug := extractRepoHostAndSlug(rawURL, allowedGitLabURL)
	return slug
}

// trimGitSuffix removes a trailing ".git" suffix (case-insensitively), ignoring
// any trailing slashes.
func trimGitSuffix(s string) string {
	trimmed := strings.TrimRight(s, "/")
	if strings.HasSuffix(strings.ToLower(trimmed), ".git") {
		return trimmed[:len(trimmed)-4]
	}
	return s
}

// extractRepoHostAndSlug extracts the normalized host and repository slug
// from a git repository URL, additionally treating allowedGitLabURL (for
// example, Options.GitLabURL, including any sub-path prefix) as a known GitLab
// instance. Returns ("", "") if the URL does not identify a repository link.
func extractRepoHostAndSlug(rawURL, allowedGitLabURL string) (string, string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", ""
	}

	var host, path string
	var isSSH bool

	if strings.HasPrefix(rawURL, "git@") {
		parts := strings.SplitN(rawURL, ":", 2)
		if len(parts) != 2 {
			return "", ""
		}
		host = strings.TrimPrefix(parts[0], "git@")
		path = parts[1]
		isSSH = true
	} else {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return "", ""
		}
		switch parsed.Scheme {
		case "http", "https":
		case "ssh":
			isSSH = true
		default:
			return "", ""
		}
		host = parsed.Host
		path = parsed.Path
	}

	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || path == "" {
		return "", ""
	}

	path = strings.Trim(path, "/")
	if path == "" {
		return "", ""
	}

	allowedHost, allowedPrefix := parseGitLabInstance(allowedGitLabURL)
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
		if !isSSH && !(strings.HasPrefix(host, "git.") && strings.HasSuffix(strings.ToLower(path), ".git")) {
			return "", ""
		}
	}

	if allowedHost != "" && host == allowedHost && allowedPrefix != "" && !isSSH {
		if strings.EqualFold(path, allowedPrefix) {
			return "", ""
		}
		if len(path) > len(allowedPrefix) && strings.EqualFold(path[:len(allowedPrefix)], allowedPrefix) && path[len(allowedPrefix)] == '/' {
			path = path[len(allowedPrefix)+1:]
		} else {
			return "", ""
		}
	}

	if isGitHub || isBitbucket {
		path = strings.Trim(trimGitSuffix(path), "/")
		parts, ok := splitNonEmptyPathSegments(path)
		if !ok || len(parts) < 2 {
			return "", ""
		}
		first := strings.ToLower(parts[0])
		if isGitHub {
			if _, reserved := githubReservedFirstSegments[first]; reserved {
				return "", ""
			}
		} else {
			if _, reserved := bitbucketReservedFirstSegments[first]; reserved {
				return "", ""
			}
		}
		return host, strings.ToLower(parts[0] + "/" + parts[1])
	}

	if isGitLab {
		if idx := strings.Index(path, "/-/"); idx != -1 {
			path = path[:idx]
		} else {
			// Legacy GitLab UI URLs omit "/-/": never split before segment index 2
			// (keeping 2-segment subgroups such as "group/blob/project" intact); at
			// index >= 2, reserved wildcard UI routes truncate immediately whereas
			// entity routes (issues, packages, releases, etc.) truncate only when
			// followed by a recognized entity ID or ref subpath.
			path = strings.Trim(trimGitSuffix(path), "/")
			segs, ok := splitNonEmptyPathSegments(path)
			if !ok {
				return "", ""
			}
			for i := 2; i < len(segs); i++ {
				seg := strings.ToLower(segs[i])
				if _, isWildcard := gitlabReservedWildcardRoutes[seg]; isWildcard {
					segs = segs[:i]
					break
				}
				if _, isEntity := gitlabLegacyEntityRoutes[seg]; isEntity && isLegacyGitLabEntitySubpath(seg, segs[i+1:]) {
					segs = segs[:i]
					break
				}
			}
			path = strings.Join(segs, "/")
		}
		path = strings.Trim(trimGitSuffix(path), "/")
		parts, ok := splitNonEmptyPathSegments(path)
		if !ok || len(parts) < 2 {
			return "", ""
		}
		if _, reserved := gitlabReservedFirstSegments[strings.ToLower(parts[0])]; reserved {
			return "", ""
		}
		return host, strings.ToLower(strings.Join(parts, "/"))
	}

	// Generic git host: strip "/-/" UI marker if present and preserve full group path.
	if idx := strings.Index(path, "/-/"); idx != -1 {
		path = path[:idx]
	}
	path = strings.Trim(trimGitSuffix(path), "/")
	parts, ok := splitNonEmptyPathSegments(path)
	if !ok || len(parts) < 2 {
		return "", ""
	}
	return host, strings.ToLower(strings.Join(parts, "/"))
}

type repoLink struct {
	host string
	slug string
}

func (r repoLink) String() string {
	return r.host + "/" + r.slug
}

const routingBatchConcurrency = 8

func isRoutingAbortError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var apiErr *jira.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized ||
			apiErr.StatusCode == http.StatusForbidden ||
			apiErr.StatusCode == http.StatusTooManyRequests ||
			apiErr.StatusCode >= 500
	}
	return false
}

// repoSlugRegex matches valid repository slugs with at least two slash-separated
// segments (e.g. "owner/repo" or "group/subgroup/project"). Bare project names
// without a slash are rejected to prevent taxonomy and fork collisions.
var repoSlugRegex = regexp.MustCompile(`^[^/\s]+(/[^/\s]+)+$`)

// extractRepoLabels extracts and normalizes repository slugs from issue labels
// matching prefix (case-insensitive, default DefaultRepoLabelPrefix = "repo:").
// It supports bare slugs (e.g. "owner/repo" or "group/subgroup/project")
// and host-qualified git repository URLs. Returns deduplicated lowercase slugs.
func (p *Poller) extractRepoLabels(labels []string) []string {
	if p.opts.DisableRepoLabels || len(labels) == 0 {
		return nil
	}
	prefix := p.opts.RepoLabelPrefix
	if prefix == "" {
		prefix = DefaultRepoLabelPrefix
	}

	var slugs []string
	seen := make(map[string]bool)

	for _, label := range labels {
		trimmed := strings.TrimSpace(label)
		if len(trimmed) < len(prefix) {
			continue
		}
		if !strings.EqualFold(trimmed[:len(prefix)], prefix) {
			continue
		}
		val := strings.TrimSpace(trimmed[len(prefix):])
		val = strings.Trim(val, "/")
		if val == "" {
			continue
		}

		var slug string
		// Check for host-qualified URL or SSH format first
		if strings.Contains(val, "://") || strings.HasPrefix(val, "git@") {
			_, s := extractRepoHostAndSlug(val, p.opts.GitLabURL)
			slug = s
		} else {
			// Try as URL with https:// if it looks like host/path
			if strings.Contains(val, "/") {
				if _, s := extractRepoHostAndSlug("https://"+val, p.opts.GitLabURL); s != "" {
					slug = s
				}
			}
			if slug == "" {
				// Bare slug check: strip .git suffix and slashes
				cleanVal := strings.Trim(val, "/")
				cleanVal = trimGitSuffix(cleanVal)
				cleanVal = strings.Trim(cleanVal, "/")
				cleanVal = strings.ToLower(cleanVal)
				if repoSlugRegex.MatchString(cleanVal) {
					slug = cleanVal
				}
			}
		}

		if slug != "" {
			slug = strings.ToLower(strings.Trim(slug, "/"))
			if !seen[slug] {
				seen[slug] = true
				slugs = append(slugs, slug)
			}
		}
	}

	return slugs
}

// filterRoutingPage evaluates repository routing for one page of candidates in
// bounded batches of up to routingBatchConcurrency, stopping early once needed
// (>0) routed candidates are collected, ctx expires, or a fatal/rate-limit/5xx
// lookup error occurs. It returns the routed issues, number of candidates
// attempted, count of candidates dropped due to lookup errors, and whether
// further candidate scanning in this cycle should stop.
func (p *Poller) filterRoutingPage(ctx context.Context, candidates []jira.Issue, needed int) ([]jira.Issue, int, int, bool) {
	if len(candidates) == 0 {
		return candidates, 0, 0, false
	}

	targetRepo := strings.ToLower(strings.TrimSpace(p.opts.TargetRepo))
	targetRepo = strings.Trim(trimGitSuffix(targetRepo), "/")
	targetHost := "github.com"
	if p.opts.TargetForge == TargetForgeGitLab {
		targetHost, _ = parseGitLabInstance(p.opts.GitLabURL)
		if targetHost == "" {
			targetHost = "gitlab.com"
		}
	}

	type linkResult struct {
		links []jira.RemoteLink
		err   error
	}

	var routed []jira.Issue
	var attempted, linkErrors int
	var stopScan bool

	for start := 0; start < len(candidates); {
		if ctx.Err() != nil {
			stopScan = true
			break
		}
		if needed > 0 && len(routed) >= needed {
			break
		}

		// Priority 1: Check in-memory repo labels (0 HTTP calls).
		repoLabels := p.extractRepoLabels(candidates[start].Fields.Labels)
		if len(repoLabels) > 0 {
			attempted++
			matched := false
			for _, slug := range repoLabels {
				if slug == targetRepo {
					matched = true
					break
				}
			}

			if matched {
				log.Printf("routing %s to %s via repo label", candidates[start].Key, p.opts.TargetRepo)
				routed = append(routed, candidates[start])
			} else {
				log.Printf("skipping %s: repo label(s) %v do not match target repo %q", candidates[start].Key, repoLabels, p.opts.TargetRepo)
			}
			start++
			continue
		}

		// Candidates without repo labels require remote links (Priority 2 & 3).
		// Gather a batch of up to routingBatchConcurrency candidates that lack repo labels.
		maxBatch := routingBatchConcurrency
		if needed > 0 && needed-len(routed) < maxBatch {
			maxBatch = needed - len(routed)
		}

		batchEnd := start + 1
		for batchEnd < len(candidates) && (batchEnd-start) < maxBatch {
			if labels := p.extractRepoLabels(candidates[batchEnd].Fields.Labels); len(labels) > 0 {
				break
			}
			batchEnd++
		}

		batch := candidates[start:batchEnd]
		start = batchEnd

		batchCtx, cancelBatch := context.WithCancel(ctx)
		results := make([]linkResult, len(batch))
		g, gCtx := errgroup.WithContext(batchCtx)
		g.SetLimit(routingBatchConcurrency)

		for i, issue := range batch {
			i, issueKey := i, issue.Key
			g.Go(func() error {
				links, err := p.client.ListRemoteLinks(gCtx, issueKey)
				results[i] = linkResult{links: links, err: err}
				if err != nil && isRoutingAbortError(err) {
					cancelBatch()
				}
				return nil
			})
		}
		_ = g.Wait()
		cancelBatch()
		attempted += len(batch)

		for i, issue := range batch {
			res := results[i]
			if res.err != nil {
				// Fail-safe: if remote links lookup failed (transient error, auth failure, etc.),
				// do NOT fall back to permissive claiming. Skip this issue for this cycle
				// to avoid claiming an issue that might belong to another repository.
				log.Printf("WARNING: listing remote links for %s: %v (skipping candidate to avoid misrouting)", issue.Key, res.err)
				linkErrors++
				if isRoutingAbortError(res.err) {
					stopScan = true
				}
				continue
			}

			var attachedRepos []repoLink
			for _, link := range res.links {
				if host, slug := extractRepoHostAndSlug(link.Object.URL, p.opts.GitLabURL); slug != "" {
					attachedRepos = append(attachedRepos, repoLink{host: host, slug: slug})
				}
			}

			// Priority 2: Attached Git repository links
			if len(attachedRepos) > 0 {
				matched := false
				for _, r := range attachedRepos {
					if r.host == targetHost && r.slug == targetRepo {
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

			// Priority 3: Candidates with neither label nor remote link
			if p.opts.StrictRepoRouting {
				log.Printf("skipping %s: no repo label or attached repository link and strict routing is enabled", issue.Key)
				continue
			}

			log.Printf("routing %s to %s via unrouted fallback (strict routing disabled)", issue.Key, p.opts.TargetRepo)
			routed = append(routed, issue)
		}

		if (needed > 0 && len(routed) >= needed) || stopScan {
			break
		}
	}

	if needed > 0 && len(routed) > needed {
		routed = routed[:needed]
	}

	return routed, attempted, linkErrors, stopScan
}
