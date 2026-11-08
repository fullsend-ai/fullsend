package scaffold

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// JiraPollCallerPath is the repository path of the Fullsend-managed GitHub
// Jira poll caller (#9109). `repos install` writes it while the effective
// tracker.jira.poll.enabled is true and removes it otherwise.
const JiraPollCallerPath = ".github/workflows/fullsend-poll-jira.yml"

// jiraPollCallerTemplate is the embedded template rendered to
// JiraPollCallerPath.
const jiraPollCallerTemplate = "templates/jira-poll-per-repo.yaml"

// JiraPollCallerMarker is the line that marks a workflow at
// JiraPollCallerPath as Fullsend-managed. A file there without it is
// user-owned (for example one copied from the Jira integration guide), and
// `repos install` never rewrites or deletes it.
const JiraPollCallerMarker = "# fullsend-component: jira-poll"

// jiraPollEmailSecretLine forwards FULLSEND_JIRA_EMAIL to the poll job. The
// same secret is forwarded separately to reusable-dispatch for Jira status
// writes; only the poll-job copy is omitted when there is no email.
const jiraPollEmailSecretLine = "      FULLSEND_JIRA_EMAIL: ${{ secrets.FULLSEND_JIRA_EMAIL }}\n"

// jiraPollOAuthSecretLines forward the Jira OAuth 2.0 client credentials to
// the poll and dispatch jobs. A reusable workflow pinned to a ref from before
// OAuth support does not declare these secrets, and GitHub rejects a caller
// that passes an undeclared secret, so they are rendered only when the
// repository uses OAuth.
var jiraPollOAuthSecretLines = []string{
	"      FULLSEND_JIRA_CLIENT_ID: ${{ secrets.FULLSEND_JIRA_CLIENT_ID }}\n",
	"      FULLSEND_JIRA_CLIENT_SECRET: ${{ secrets.FULLSEND_JIRA_CLIENT_SECRET }}\n",
}

// RenderJiraPollCaller renders the managed GitHub Jira poll caller. Its
// reusable workflows are pinned to the upstream ref in opts; the caller
// always references the upstream repository, so it does not depend on a
// vendored copy of reusable-poll-jira.yml. emailConfigured controls whether
// FULLSEND_JIRA_EMAIL reaches the poll job. The dispatch job forwards Jira
// credentials to Jira-originated Fullsend status updates only. oauthConfigured
// controls whether the OAuth client credentials are forwarded; a token-only
// caller omits them so it stays valid against reusable workflows pinned to a
// ref that predates OAuth support.
func RenderJiraPollCaller(opts RenderOptions, emailConfigured, oauthConfigured bool) ([]byte, error) {
	raw, err := FullsendRepoFile(jiraPollCallerTemplate)
	if err != nil {
		return nil, fmt.Errorf("loading Jira poll caller template: %w", err)
	}
	out := string(raw)
	if strings.Count(out, jiraPollEmailSecretLine) != 2 {
		return nil, fmt.Errorf("the Jira poll caller template must forward FULLSEND_JIRA_EMAIL to the poll and dispatch jobs")
	}
	if !emailConfigured {
		out = strings.Replace(out, jiraPollEmailSecretLine, "", 1)
	}
	for _, line := range jiraPollOAuthSecretLines {
		if strings.Count(out, line) != 2 {
			return nil, fmt.Errorf("the Jira poll caller template must forward the Jira OAuth client credentials to the poll and dispatch jobs")
		}
		if !oauthConfigured {
			out = strings.ReplaceAll(out, line, "")
		}
	}
	upstream := opts
	upstream.Vendored = false
	out = strings.ReplaceAll(out, "__REUSABLE_WORKFLOW__", upstreamReusableWorkflowUses("reusable-poll-jira.yml", upstream))
	out = strings.ReplaceAll(out, "__REUSABLE_DISPATCH__", reusableDispatchUses(upstream))
	out = strings.ReplaceAll(out, "__GH_RUNNER__", resolvedRunner(upstream))
	return PrependManagedHeader(jiraPollCallerTemplate, []byte(out)), nil
}

// IsManagedJiraPollCaller reports whether content is a Fullsend-managed Jira
// poll caller: one of its lines is JiraPollCallerMarker.
func IsManagedJiraPollCaller(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == JiraPollCallerMarker {
			return true
		}
	}
	return false
}

// upstreamReusableWorkflowUses returns the uses: value of the named reusable
// workflow in the upstream repository at the ref in opts.
func upstreamReusableWorkflowUses(name string, opts RenderOptions) string {
	ref := resolvedRef(opts)
	uses := config.DefaultUpstreamRepo + "/.github/workflows/" + name + "@" + ref
	if opts.UpstreamTag != "" && opts.UpstreamTag != ref {
		uses += "  # " + opts.UpstreamTag
	}
	return uses
}
