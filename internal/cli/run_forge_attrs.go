package cli

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// forgePointerAttrs returns the forge and CI pointers recorded on the root
// run span (#6842): the repository, change and head commit the run was
// against, and the pipeline run that executed it. Names follow the
// OpenTelemetry VCS and CICD semantic conventions, so backends that know
// those conventions read them without a Fullsend-specific mapping.
//
// Repository, change and refs come from the normalized event, so no forge
// needs its own code here: GitHub and GitLab dispatch both deliver that
// event to run (GitLab since #9014). A Jira event records no repository URL,
// since its entity URL is on the Jira host. The head revision is the change's
// head commit, never GITHUB_SHA or CI_COMMIT_SHA, which name a merge or base
// commit on pull request and merged-results pipelines. On GitLab it is
// usually absent: the poller sets head_sha only on synchronized MR events,
// and the pipelines it creates are not merge request pipelines, so
// CI_MERGE_REQUEST_SOURCE_BRANCH_SHA is unset there. The pipeline run
// comes from the environment of the CI system running fullsend, which can
// differ from the forge the run targets (a Jira-triggered run on GitHub
// Actions, for one). An attribute that does not apply is omitted rather than
// emitted empty.
func forgePointerAttrs(eventMap map[string]any, forgePlatform, runURL string, getenv func(string) string) []attribute.KeyValue {
	var ev normevent.Event
	if eventMap != nil {
		// The map came from normevent.Event.ToMap; decoding it back gives
		// typed fields (entity ids are float64 in the map).
		if data, err := json.Marshal(eventMap); err == nil {
			_ = json.Unmarshal(data, &ev)
		}
	}

	var attrs []attribute.KeyValue
	add := func(key, val string) {
		if val = strings.TrimSpace(val); val != "" {
			attrs = append(attrs, boundedStringAttr(key, val))
		}
	}

	provider := string(ev.Source.System)
	forgeEvent := provider == string(normevent.SystemGitHub) || provider == string(normevent.SystemGitLab)
	if !forgeEvent {
		provider = forgePlatform
	}
	if provider == "github" || provider == "gitlab" {
		add("vcs.provider.name", provider)
	}
	// Only a forge event's entity URL is on the repository's host; a Jira
	// event pairs its issue URL with the target repository path.
	if forgeEvent {
		add("vcs.repository.url.full", repositoryURL(ev.Entity.URL, ev.Repo))
	}

	cp := ev.State.ChangeProposal
	switch {
	case ev.Entity.Kind == normevent.EntityChangeProposal && ev.Entity.ID > 0:
		add("vcs.change.id", strconv.Itoa(ev.Entity.ID))
	case cp != nil && cp.ID > 0:
		add("vcs.change.id", strconv.Itoa(cp.ID))
	case ev.Entity.LinkedChangeProposal != nil && ev.Entity.LinkedChangeProposal.ID > 0:
		// A comment on a pull request keeps the PR number here even when
		// the pull request lookup that fills State.ChangeProposal failed.
		add("vcs.change.id", strconv.Itoa(ev.Entity.LinkedChangeProposal.ID))
	}

	headSHA := ""
	if cp != nil {
		add("vcs.ref.head.name", cp.HeadRef)
		add("vcs.ref.base.name", cp.BaseRef)
		headSHA = cp.HeadSHA
	}
	if headSHA == "" {
		headSHA = changeHeadSHAFromEnv(provider, getenv)
	}
	if hexSHARe.MatchString(headSHA) {
		add("vcs.ref.head.revision", headSHA)
	}

	runID, pipelineURL := pipelineRunFromEnv(getenv)
	add("cicd.pipeline.run.id", runID)
	if u := httpURLWithoutCredentials(runURL); u != "" {
		pipelineURL = u
	}
	add("cicd.pipeline.run.url.full", pipelineURL)
	return attrs
}

// repositoryURL returns the repository's web URL: the entity URL cut right
// after the event's repository path, which keeps the path prefix of a GitLab
// install served under a relative URL (https://example.com/gitlab/group/project).
// When the entity URL does not contain the repository path, it falls back to
// the URL's origin plus the repository path. It returns "" when either part
// is missing.
func repositoryURL(entityURL, repo string) string {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	u, err := url.Parse(strings.TrimSpace(entityURL))
	if repo == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	// u.Host carries no userinfo, so credentials in the entity URL are dropped.
	origin := u.Scheme + "://" + u.Host
	if i := strings.Index(u.Path+"/", "/"+repo+"/"); i >= 0 {
		return origin + u.Path[:i] + "/" + repo
	}
	return origin + "/" + repo
}

// changeHeadSHAFromEnv is the head-commit fallback for runs without a
// change proposal in the normalized event. It reads only variables that name
// the change's head: PR_HEAD_SHA or the native pull_request event on GitHub,
// CI_MERGE_REQUEST_SOURCE_BRANCH_SHA on GitLab.
func changeHeadSHAFromEnv(provider string, getenv func(string) string) string {
	switch provider {
	case "github":
		if sha := strings.TrimSpace(getenv("PR_HEAD_SHA")); sha != "" {
			return sha
		}
		return prHeadSHAFromEventPath(getenv("GITHUB_EVENT_PATH"))
	case "gitlab":
		return strings.TrimSpace(getenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"))
	}
	return ""
}

// pipelineRunFromEnv returns the id and URL of the CI pipeline run executing
// fullsend, or empty strings outside GitHub Actions and GitLab CI.
func pipelineRunFromEnv(getenv func(string) string) (id, runURL string) {
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		id = strings.TrimSpace(getenv("GITHUB_RUN_ID"))
		server := httpURLWithoutCredentials(getenv("GITHUB_SERVER_URL"))
		repo := strings.Trim(strings.TrimSpace(getenv("GITHUB_REPOSITORY")), "/")
		if id != "" && server != "" && repo != "" {
			runURL = strings.TrimSuffix(server, "/") + "/" + repo + "/actions/runs/" + id
		}
	case getenv("GITLAB_CI") == "true":
		id = strings.TrimSpace(getenv("CI_PIPELINE_ID"))
		runURL = httpURLWithoutCredentials(getenv("CI_PIPELINE_URL"))
	}
	return id, runURL
}

// httpURLWithoutCredentials returns raw as an http(s) URL with any userinfo,
// query and fragment removed, or "" when raw is not an absolute http(s) URL.
func httpURLWithoutCredentials(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
