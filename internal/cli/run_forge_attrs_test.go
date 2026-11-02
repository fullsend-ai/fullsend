package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

const (
	testHeadSHA  = "0123456789abcdef0123456789abcdef01234567"
	testMergeSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

func eventMapFor(t *testing.T, ev normevent.Event) map[string]any {
	t.Helper()
	m, err := ev.ToMap()
	require.NoError(t, err)
	return m
}

func envFrom(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func attrValues(attrs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}

func githubPREvent() normevent.Event {
	return normevent.Event{
		Repo: "fullsend-ai/fullsend",
		Entity: normevent.Entity{
			Kind: normevent.EntityChangeProposal,
			ID:   8594,
			URL:  "https://github.com/fullsend-ai/fullsend/pull/8594",
		},
		State: normevent.State{ChangeProposal: &normevent.ChangeProposal{
			ID:       8594,
			HeadRepo: "someone/fullsend",
			BaseRepo: "fullsend-ai/fullsend",
			HeadRef:  "fix/thing",
			BaseRef:  "main",
			HeadSHA:  testHeadSHA,
		}},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "pull_request"},
	}
}

func TestForgePointerAttrs_GitHubPullRequestOnActions(t *testing.T) {
	env := envFrom(map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_RUN_ID":     "12246334316",
		"GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "fullsend-ai/.fullsend",
		// A native pull_request run checks out the merge commit; it must
		// not be recorded as the head revision.
		"GITHUB_SHA": testMergeSHA,
	})
	got := attrValues(forgePointerAttrs(eventMapFor(t, githubPREvent()), "github", "", env))
	assert.Equal(t, map[string]string{
		"vcs.provider.name":          "github",
		"vcs.repository.url.full":    "https://github.com/fullsend-ai/fullsend",
		"vcs.change.id":              "8594",
		"vcs.ref.head.name":          "fix/thing",
		"vcs.ref.base.name":          "main",
		"vcs.ref.head.revision":      testHeadSHA,
		"cicd.pipeline.run.id":       "12246334316",
		"cicd.pipeline.run.url.full": "https://github.com/fullsend-ai/.fullsend/actions/runs/12246334316",
	}, got)
}

func TestForgePointerAttrs_GitHubIssueOmitsChangeAndRefs(t *testing.T) {
	ev := normevent.Event{
		Repo: "fullsend-ai/fullsend",
		Entity: normevent.Entity{
			Kind: normevent.EntityWorkItem,
			ID:   6842,
			URL:  "https://github.com/fullsend-ai/fullsend/issues/6842",
		},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "issues"},
	}
	env := envFrom(map[string]string{"GITHUB_SHA": testMergeSHA})
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", env))
	assert.Equal(t, map[string]string{
		"vcs.provider.name":       "github",
		"vcs.repository.url.full": "https://github.com/fullsend-ai/fullsend",
	}, got)
}

func TestForgePointerAttrs_LinkedChangeProposalUsesStateID(t *testing.T) {
	ev := normevent.Event{
		Repo: "fullsend-ai/fullsend",
		Entity: normevent.Entity{
			Kind: normevent.EntityWorkItem,
			ID:   10,
			URL:  "https://github.com/fullsend-ai/fullsend/issues/10",
		},
		State:  normevent.State{ChangeProposal: &normevent.ChangeProposal{ID: 11, HeadRef: "b", BaseRef: "main", HeadSHA: testHeadSHA}},
		Source: normevent.Source{System: normevent.SystemGitHub},
	}
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", envFrom(nil)))
	assert.Equal(t, "11", got["vcs.change.id"])
	assert.Equal(t, testHeadSHA, got["vcs.ref.head.revision"])
}

func TestForgePointerAttrs_SelfHostedGitLabMergeRequest(t *testing.T) {
	ev := normevent.Event{
		Repo: "group/sub/project",
		Entity: normevent.Entity{
			Kind: normevent.EntityChangeProposal,
			ID:   42,
			URL:  "https://gitlab.example.com/group/sub/project/-/merge_requests/42",
		},
		State: normevent.State{ChangeProposal: &normevent.ChangeProposal{
			ID: 42, HeadRef: "feature", BaseRef: "main", HeadSHA: testHeadSHA,
		}},
		Source: normevent.Source{System: normevent.SystemGitLab},
	}
	env := envFrom(map[string]string{
		"GITLAB_CI":       "true",
		"CI_PIPELINE_ID":  "998877",
		"CI_PIPELINE_URL": "https://gitlab.example.com/group/sub/project/-/pipelines/998877",
		"CI_COMMIT_SHA":   testMergeSHA,
	})
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "gitlab", "", env))
	assert.Equal(t, map[string]string{
		"vcs.provider.name":          "gitlab",
		"vcs.repository.url.full":    "https://gitlab.example.com/group/sub/project",
		"vcs.change.id":              "42",
		"vcs.ref.head.name":          "feature",
		"vcs.ref.base.name":          "main",
		"vcs.ref.head.revision":      testHeadSHA,
		"cicd.pipeline.run.id":       "998877",
		"cicd.pipeline.run.url.full": "https://gitlab.example.com/group/sub/project/-/pipelines/998877",
	}, got)
}

func TestForgePointerAttrs_PRCommentKeepsLinkedChangeID(t *testing.T) {
	// The pull request lookup failed, so only the linked id survives.
	ev := normevent.Event{
		Repo: "fullsend-ai/fullsend",
		Entity: normevent.Entity{
			Kind: normevent.EntityWorkItem,
			ID:   9011,
			URL:  "https://github.com/fullsend-ai/fullsend/pull/9011",
			LinkedChangeProposal: &normevent.LinkedChangeProposal{
				ID:  9011,
				URL: "https://github.com/fullsend-ai/fullsend/pull/9011",
			},
		},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "issue_comment"},
	}
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", envFrom(nil)))
	assert.Equal(t, "9011", got["vcs.change.id"])
	_, ok := got["vcs.ref.head.name"]
	assert.False(t, ok)
}

func TestForgePointerAttrs_JiraEventUsesForgePlatformAndCIRun(t *testing.T) {
	ev := normevent.Event{
		// Jira events carry the target repository with a Jira issue URL;
		// joining them would name a repository on the Jira host.
		Repo:   "org/target",
		Entity: normevent.Entity{Kind: normevent.EntityWorkItem, Key: "PROJ-123", URL: "https://example.atlassian.net/browse/PROJ-123"},
		Source: normevent.Source{System: normevent.SystemJira},
	}
	env := envFrom(map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_RUN_ID":     "5",
		"GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "org/.fullsend",
	})
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", env))
	assert.Equal(t, map[string]string{
		"vcs.provider.name":          "github",
		"cicd.pipeline.run.id":       "5",
		"cicd.pipeline.run.url.full": "https://github.com/org/.fullsend/actions/runs/5",
	}, got)
}

func TestForgePointerAttrs_NoEventFallsBackToChangeHeadEnv(t *testing.T) {
	t.Run("github PR_HEAD_SHA", func(t *testing.T) {
		env := envFrom(map[string]string{"PR_HEAD_SHA": testHeadSHA, "GITHUB_SHA": testMergeSHA})
		got := attrValues(forgePointerAttrs(nil, "github", "", env))
		assert.Equal(t, map[string]string{"vcs.provider.name": "github", "vcs.ref.head.revision": testHeadSHA}, got)
	})
	t.Run("github native pull_request event", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "event.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"pull_request":{"head":{"sha":"`+testHeadSHA+`"}}}`), 0o644))
		env := envFrom(map[string]string{"GITHUB_EVENT_PATH": path, "GITHUB_SHA": testMergeSHA})
		got := attrValues(forgePointerAttrs(nil, "github", "", env))
		assert.Equal(t, testHeadSHA, got["vcs.ref.head.revision"])
	})
	t.Run("gitlab merged-results pipeline", func(t *testing.T) {
		env := envFrom(map[string]string{"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": testHeadSHA, "CI_COMMIT_SHA": testMergeSHA})
		got := attrValues(forgePointerAttrs(nil, "gitlab", "", env))
		assert.Equal(t, testHeadSHA, got["vcs.ref.head.revision"])
	})
	t.Run("commit sha alone is never the head revision", func(t *testing.T) {
		env := envFrom(map[string]string{"GITHUB_SHA": testMergeSHA, "CI_COMMIT_SHA": testMergeSHA})
		for _, platform := range []string{"github", "gitlab"} {
			_, ok := attrValues(forgePointerAttrs(nil, platform, "", env))["vcs.ref.head.revision"]
			assert.False(t, ok, platform)
		}
	})
}

func TestForgePointerAttrs_NothingKnownEmitsNothing(t *testing.T) {
	assert.Empty(t, forgePointerAttrs(nil, "", "", envFrom(nil)))
	// Unknown forge platforms are not reported as a VCS provider.
	assert.Empty(t, forgePointerAttrs(nil, "forgejo", "", envFrom(nil)))
}

func TestForgePointerAttrs_DropsMalformedHeadSHA(t *testing.T) {
	ev := githubPREvent()
	ev.State.ChangeProposal.HeadSHA = "not-a-sha"
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", envFrom(nil)))
	_, ok := got["vcs.ref.head.revision"]
	assert.False(t, ok)
	assert.Equal(t, "fix/thing", got["vcs.ref.head.name"])
}

func TestForgePointerAttrs_RunURLFlagWinsAndLosesCredentials(t *testing.T) {
	env := envFrom(map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_RUN_ID":     "7",
		"GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "org/.fullsend",
	})
	got := attrValues(forgePointerAttrs(nil, "github", "https://user:secret@ci.example.com/runs/7?token=x#frag", env))
	assert.Equal(t, "https://ci.example.com/runs/7", got["cicd.pipeline.run.url.full"])
	assert.Equal(t, "7", got["cicd.pipeline.run.id"])

	got = attrValues(forgePointerAttrs(nil, "github", "not a url", env))
	assert.Equal(t, "https://github.com/org/.fullsend/actions/runs/7", got["cicd.pipeline.run.url.full"])
}

func TestForgePointerAttrs_RepositoryURLDropsEntityCredentials(t *testing.T) {
	ev := githubPREvent()
	ev.Entity.URL = "https://user:secret@github.com/fullsend-ai/fullsend/pull/8594?x=1"
	got := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", envFrom(nil)))
	assert.Equal(t, "https://github.com/fullsend-ai/fullsend", got["vcs.repository.url.full"])
}

func TestForgePointerAttrs_RepositoryURLNeedsHTTPEntityURL(t *testing.T) {
	for _, entityURL := range []string{"", "not a url", "ssh://git@github.com/fullsend-ai/fullsend"} {
		ev := githubPREvent()
		ev.Entity.URL = entityURL
		_, ok := attrValues(forgePointerAttrs(eventMapFor(t, ev), "github", "", envFrom(nil)))["vcs.repository.url.full"]
		assert.False(t, ok, entityURL)
	}
}

func TestForgePointerAttrs_BoundsLongValues(t *testing.T) {
	ev := githubPREvent()
	ev.State.ChangeProposal.HeadRef = strings.Repeat("r", telemetry.MaxSpanAttrValueLen*2)
	for _, kv := range forgePointerAttrs(eventMapFor(t, ev), "github", "", envFrom(nil)) {
		assert.LessOrEqual(t, len(kv.Value.AsString()), telemetry.MaxSpanAttrValueLen, kv.Key)
	}
}
