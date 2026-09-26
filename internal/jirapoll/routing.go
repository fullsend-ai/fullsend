package jirapoll

import (
	"context"
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"golang.org/x/sync/errgroup"
)

// extractRepoSlug extracts and normalizes the "owner/repo" or "group/subgroup/repo" slug
// from a git repository URL (HTTPS, HTTP, SSH, or git@ format).
// Returns an empty string if the URL does not look like a git repository link.
func extractRepoSlug(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}

	var host, path string
	if strings.HasPrefix(rawURL, "git@") {
		// Handle git@host:owner/repo(.git)
		parts := strings.SplitN(rawURL, ":", 2)
		if len(parts) == 2 {
			host = strings.TrimPrefix(parts[0], "git@")
			path = parts[1]
		}
	} else {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return ""
		}
		host = parsed.Host
		path = parsed.Path
	}

	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || path == "" {
		return ""
	}

	// Strip port if present
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Classify forge host with exact or dot-boundary checks
	isGitHub := host == "github.com"
	isBitbucket := host == "bitbucket.org"
	isGitLab := host == "gitlab.com" || strings.HasPrefix(host, "gitlab.") || strings.HasSuffix(host, ".gitlab.com")

	if !isGitHub && !isBitbucket && !isGitLab {
		// Reject lookalikes and unrelated hosts unless URL explicitly ends with .git or uses ssh://
		if !strings.HasSuffix(path, ".git") && !strings.HasPrefix(rawURL, "ssh://") {
			return ""
		}
	}

	// Clean path
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}

	if isGitHub || isBitbucket {
		// GitHub and Bitbucket repository slugs are strictly 2 segments: owner/repo.
		// Any UI subpaths (/pull/123, /commits/main, /pull-requests/45, /src/master, /issues/1)
		// are automatically ignored by taking the first 2 segments.
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return ""
		}
		return strings.ToLower(parts[0] + "/" + parts[1])
	}

	if isGitLab {
		// GitLab supports subgroups (group/subgroup/project).
		// In GitLab, UI routes follow the "/-/" separator (e.g., /-/merge_requests/1, /-/tree/main).
		// We only strip UI subpaths after "/-/" so subgroup names like "tree", "blob", "issues" remain intact.
		if idx := strings.Index(path, "/-/"); idx != -1 {
			path = path[:idx]
		}
		path = strings.Trim(path, "/")
		if !strings.Contains(path, "/") {
			return ""
		}
		return strings.ToLower(path)
	}

	// Generic / self-hosted git with .git suffix or ssh
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + parts[1])
}

// filterRouting filters candidate issues based on attached repository links
// and configured component.
//
// Evaluation order:
//  1. Attached Git repository links (Priority):
//     - If any attached link matches targetRepo -> issue is routed to this repo.
//     - If attached repo links exist but none match targetRepo -> issue belongs to another repo; skip.
//  2. Jira Component (Fallback):
//     - If no attached repo links exist and JiraComponent is configured:
//     check if any issue component matches (case-insensitive).
//     - If no attached repo links and no JiraComponent configured: keep issue.
func (p *Poller) filterRouting(ctx context.Context, candidates []jira.Issue) ([]jira.Issue, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}

	targetRepo := strings.ToLower(strings.TrimSpace(p.opts.TargetRepo))

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

	for i, issue := range candidates {
		res := results[i]
		if res.err != nil {
			// Fail-safe: if remote links lookup failed (transient error, auth failure, etc.),
			// do NOT fall back to component or permissive claiming. Skip this issue for this cycle
			// to avoid claiming an issue that might belong to another repository.
			log.Printf("WARNING: listing remote links for %s: %v (skipping candidate to avoid misrouting)", issue.Key, res.err)
			continue
		}

		var attachedRepos []string
		for _, link := range res.links {
			if slug := extractRepoSlug(link.Object.URL); slug != "" {
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

	return routed, nil
}
