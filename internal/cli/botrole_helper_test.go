package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeCommentActors answers CommentActor from a fixed map, recording each
// comment asked about and the context each lookup ran under.
type fakeCommentActors struct {
	actors   map[string]commentActor
	err      error
	asked    []string
	contexts []context.Context
}

func (f *fakeCommentActors) CommentActor(ctx context.Context, commentID string) (commentActor, error) {
	f.asked = append(f.asked, commentID)
	f.contexts = append(f.contexts, ctx)
	if f.err != nil {
		return commentActor{}, f.err
	}
	a, ok := f.actors[commentID]
	if !ok {
		return commentActor{}, errors.New("no such comment")
	}
	return a, nil
}

// askHelper sends one script request body to h and returns the status and
// decoded response.
func askHelper(t *testing.T, h *botRoleHelper, body string) (int, botidentity.MintBotRoleResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot-role", strings.NewReader(body))
	h.ServeHTTP(rec, req)
	var resp botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	return rec.Code, resp
}

func commentRequest(entity, comment string) string {
	b, _ := json.Marshal(botRoleScriptRequest{Entity: entity, Comment: comment})
	return string(b)
}

const otherEntityID = "fullsend:entity:v1:github:github.com:work_item:I_kwDOOther1"

// newCommentHelper is a helper for testEntityID whose comments are actors.
func newCommentHelper(resolver *fakeBotRoleResolver, actors map[string]commentActor) (*botRoleHelper, *fakeCommentActors) {
	comments := &fakeCommentActors{actors: actors}
	return &botRoleHelper{resolver: resolver, target: "fullsend-ai/agents", entity: testEntityID, comments: comments}, comments
}

// A comment whose current-revision actor is a bot answers that bot's
// registered role, resolved for the run's target (#9602 ac_1).
func TestBotRoleHelper_BotCommentRecognized(t *testing.T) {
	resolver := &fakeBotRoleResolver{role: "triage"}
	h, comments := newCommentHelper(resolver, map[string]commentActor{
		testCommentID: {Entity: testEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"},
	})

	code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, botidentity.MintOutcomeRecognized, resp.Outcome)
	assert.Equal(t, "triage", resp.Role)

	assert.Equal(t, []string{testCommentID}, comments.asked)
	require.Len(t, resolver.queries, 1)
	assert.Equal(t, botidentity.BotRoleQuery{Forge: "github", Target: "fullsend-ai/agents", Login: "fullsend-ai-triage[bot]"}, resolver.queries[0],
		"the comment's actor must be resolved for the run's target, bound by the helper")
}

func TestBotRoleHelper_UnregisteredBotIsNoMatch(t *testing.T) {
	resolver := &fakeBotRoleResolver{}
	h, _ := newCommentHelper(resolver, map[string]commentActor{
		testCommentID: {Entity: testEntityID, Bot: true, Login: "someone-else[bot]"},
	})

	code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, botidentity.MintOutcomeNoMatch, resp.Outcome)
	assert.Empty(t, resp.Role)
}

// A comment whose current-revision actor is not a bot — a human author, or
// a human who last edited a bot's comment — is a completed no-match, and
// never reaches the mint (#9602 ac_2).
func TestBotRoleHelper_HumanActorIsNoMatchNotFailure(t *testing.T) {
	resolver := &fakeBotRoleResolver{role: "triage"}
	h, _ := newCommentHelper(resolver, map[string]commentActor{
		testCommentID: {Entity: testEntityID, Bot: false, Login: "octocat"},
	})

	code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
	require.Equal(t, http.StatusOK, code, "a human actor must answer a completed lookup, not a failure")
	assert.Equal(t, botidentity.MintOutcomeNoMatch, resp.Outcome)
	assert.Empty(t, resolver.queries, "a human actor must not reach the mint")
}

// The helper answers only for the entity the run handles: a request naming
// another entity, or a comment that is on another entity, is refused as a
// failure, never answered (#9602 ac_2).
func TestBotRoleHelper_OtherEntityRefused(t *testing.T) {
	t.Run("request names another entity", func(t *testing.T) {
		resolver := &fakeBotRoleResolver{role: "triage"}
		h, comments := newCommentHelper(resolver, map[string]commentActor{
			testCommentID: {Entity: otherEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"},
		})
		code, resp := askHelper(t, h, commentRequest(otherEntityID, testCommentID))
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Empty(t, resp.Outcome)
		assert.Empty(t, comments.asked, "a request for another entity must not reach the forge")
		assert.Empty(t, resolver.queries)
	})

	t.Run("comment is on another entity", func(t *testing.T) {
		resolver := &fakeBotRoleResolver{role: "triage"}
		h, _ := newCommentHelper(resolver, map[string]commentActor{
			testCommentID: {Entity: otherEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"},
		})
		code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Empty(t, resp.Outcome)
		assert.Empty(t, resolver.queries, "a comment on another entity must not be answered")
	})

	t.Run("request names a legacy-format entity ID", func(t *testing.T) {
		resolver := &fakeBotRoleResolver{role: "triage"}
		h, comments := newCommentHelper(resolver, map[string]commentActor{
			testCommentID: {Entity: testEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"},
		})
		// A REST or webhook node_id producer still emits the legacy format
		// for older objects; entity IDs are compared as the recorded
		// strings, so the mismatch must be refused, never answered.
		legacy := "fullsend:entity:v1:github:github.com:work_item:MDExOlB1bGxSZXF1ZXN0MTA5OTU5NDE="
		code, resp := askHelper(t, h, commentRequest(legacy, testCommentID))
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Empty(t, resp.Outcome)
		assert.Empty(t, comments.asked, "a legacy-format entity ID must not reach the forge")
		assert.Empty(t, resolver.queries)
	})
}

// Each GitHub comment lookup runs under githubGraphQLTimeout, like the
// run-start entity call, so a slow lookup cannot outlive the bounds the
// WriteTimeout and botRoleCmdTimeout comments count.
func TestBotRoleHelper_GitHubLookupBounded(t *testing.T) {
	t.Run("lookup context carries the GitHub timeout", func(t *testing.T) {
		resolver := &fakeBotRoleResolver{role: "triage"}
		h, comments := newCommentHelper(resolver, map[string]commentActor{
			testCommentID: {Entity: testEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"},
		})
		code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, botidentity.MintOutcomeRecognized, resp.Outcome)
		require.Len(t, comments.contexts, 1)
		deadline, ok := comments.contexts[0].Deadline()
		require.True(t, ok, "the comment lookup must run under a deadline")
		assert.WithinDuration(t, time.Now().Add(githubGraphQLTimeout), deadline, 5*time.Second,
			"the comment lookup deadline must be githubGraphQLTimeout out")
	})

	t.Run("deadline exceeded answers the fixed failure", func(t *testing.T) {
		comments := &fakeCommentActors{err: context.DeadlineExceeded}
		h := &botRoleHelper{resolver: &fakeBotRoleResolver{role: "triage"}, target: "fullsend-ai/agents", entity: testEntityID, comments: comments}
		code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
		assert.Equal(t, http.StatusBadGateway, code)
		assert.Empty(t, resp.Outcome)
		assert.NotContains(t, resp.Error, "deadline", "context detail must stay runner-side")
	})
}

// A lookup that cannot be completed — the forge or the mint fails, or the
// run has no trusted lookup (GitLab, Jira, or an entity the helper could
// not resolve) — answers an error status, never no_match, with a fixed
// message (#9602 ac_3).
func TestBotRoleHelper_LookupFailureDistinctFromNoMatch(t *testing.T) {
	t.Run("forge lookup fails", func(t *testing.T) {
		comments := &fakeCommentActors{err: errors.New("graphql: connection refused to api.github.com")}
		h := &botRoleHelper{resolver: &fakeBotRoleResolver{}, target: "fullsend-ai/agents", entity: testEntityID, comments: comments}
		code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
		assert.Equal(t, http.StatusBadGateway, code)
		assert.Empty(t, resp.Outcome)
		assert.NotContains(t, resp.Error, "api.github.com", "forge error detail must stay runner-side")
	})

	t.Run("mint lookup fails", func(t *testing.T) {
		resolver := &fakeBotRoleResolver{err: errors.Join(botidentity.ErrLookup, errors.New("mint unreachable"))}
		h, _ := newCommentHelper(resolver, map[string]commentActor{
			testCommentID: {Entity: testEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"},
		})
		code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
		assert.Equal(t, http.StatusBadGateway, code)
		assert.Empty(t, resp.Outcome)
		assert.NotContains(t, resp.Error, "mint unreachable", "mint error detail must stay runner-side")
	})

	t.Run("no trusted lookup for this run", func(t *testing.T) {
		h := &botRoleHelper{resolver: &fakeBotRoleResolver{role: "triage"}, target: "group/project"}
		code, resp := askHelper(t, h, commentRequest(testEntityID, testCommentID))
		assert.Equal(t, http.StatusNotImplemented, code)
		assert.Empty(t, resp.Outcome)
	})
}

func TestBotRoleHelper_BadRequests(t *testing.T) {
	h, _ := newCommentHelper(&fakeBotRoleResolver{}, nil)

	for name, body := range map[string]string{
		"empty entity":   `{"entity":"","comment":"IC_1"}`,
		"empty comment":  `{"entity":"` + testEntityID + `","comment":""}`,
		"missing fields": `{}`,
		"a login":        `{"login":"fullsend-ai-triage[bot]"}`,
		"invalid json":   `{"entity":`,
	} {
		t.Run(name, func(t *testing.T) {
			code, resp := askHelper(t, h, body)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Empty(t, resp.Outcome)
		})
	}
}

func TestBotRoleHelper_MethodNotAllowed(t *testing.T) {
	h, _ := newCommentHelper(&fakeBotRoleResolver{}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bot-role", nil)
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestBotRoleHelperEnv_Exports(t *testing.T) {
	assert.Nil(t, botRoleHelperEnv{}.exports(), "no helper running means no exports")

	env := botRoleHelperEnv{url: "http://127.0.0.1:1/bot-role", token: "tok"}
	assert.Equal(t, []string{"FULLSEND_BOT_ROLE_URL=http://127.0.0.1:1/bot-role", "FULLSEND_BOT_ROLE_TOKEN=tok"}, env.exports(),
		"a helper with no resolved entity exports no entity ID")

	env.entity = testEntityID
	assert.Equal(t, []string{"FULLSEND_BOT_ROLE_URL=http://127.0.0.1:1/bot-role", "FULLSEND_BOT_ROLE_TOKEN=tok", "FULLSEND_ENTITY_ID=" + testEntityID}, env.exports())
}

// The started service must be loopback, bearer-authenticated, and answer
// end to end through the same wire the `fullsend bot-role` subcommand
// reads.
func TestStartBotRoleHelperService(t *testing.T) {
	resolver := &fakeBotRoleResolver{role: "review"}
	comments := &fakeCommentActors{actors: map[string]commentActor{
		testCommentID: {Entity: testEntityID, Bot: true, Login: "fullsend-ai-review[bot]"},
	}}
	env, shutdown, err := startBotRoleHelperService(resolver, "fullsend-ai/agents", testEntityID, comments)
	require.NoError(t, err)
	t.Cleanup(shutdown)

	require.True(t, strings.HasPrefix(env.url, "http://127.0.0.1:"), "the helper must listen on loopback, got %s", env.url)
	require.NotEmpty(t, env.token)
	assert.Equal(t, testEntityID, env.entity)

	// healthz answers without a token.
	healthResp, err := http.Get(strings.TrimSuffix(env.url, "/bot-role") + "/healthz")
	require.NoError(t, err)
	defer healthResp.Body.Close()
	assert.Equal(t, http.StatusOK, healthResp.StatusCode)

	// A lookup without the bearer token is refused.
	unauthed, err := http.Post(env.url, "application/json", strings.NewReader(commentRequest(testEntityID, testCommentID)))
	require.NoError(t, err)
	unauthed.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, unauthed.StatusCode)

	// With the token, the answer is the mint's response shape.
	req, err := http.NewRequest(http.MethodPost, env.url, strings.NewReader(commentRequest(testEntityID, testCommentID)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, botidentity.MintOutcomeRecognized, body.Outcome)
	assert.Equal(t, "review", body.Role)
	require.Len(t, resolver.queries, 1)
	assert.Equal(t, "fullsend-ai/agents", resolver.queries[0].Target)
}

// Starting the helper asks GitHub Actions to mask its bearer token in the
// job log, the same treatment every other runner-held credential gets: a
// pre-script that uses shell tracing or dumps its environment cannot print
// FULLSEND_BOT_ROLE_TOKEN there.
func TestStartBotRoleHelperService_MasksTokenInActionsLog(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	var env botRoleHelperEnv
	stderr := captureStderr(t, func() {
		var shutdown func()
		var err error
		env, shutdown, err = startBotRoleHelperService(&fakeBotRoleResolver{}, "fullsend-ai/agents", testEntityID, nil)
		require.NoError(t, err)
		t.Cleanup(shutdown)
	})
	require.NotEmpty(t, env.token)
	assert.Contains(t, stderr, "::add-mask::"+env.token+"\n")
}

// githubCommentActors reads a comment's current-revision actor and the
// entity it is on from GitHub GraphQL: the last editor when the comment
// was edited, its author otherwise; a Bot actor in its REST login form.
func TestGitHubCommentActors_CommentActor(t *testing.T) {
	useFakeGitHubGraphQL(t, "I_kwDOAgents9602", map[string]fakeGitHubComment{
		"IC_bot":             {authorType: "Bot", authorLogin: "fullsend-ai-triage", issueID: "I_kwDOAgents9602"},
		"IC_human":           {authorType: "User", authorLogin: "octocat", issueID: "I_kwDOAgents9602"},
		"IC_human_edited":    {authorType: "Bot", authorLogin: "fullsend-ai-triage", editorType: "User", editorLogin: "octocat", issueID: "I_kwDOAgents9602"},
		"IC_bot_edited":      {authorType: "Bot", authorLogin: "fullsend-ai-triage", editorType: "Bot", editorLogin: "fullsend-ai-triage", issueID: "I_kwDOAgents9602"},
		"IC_on_pr":           {authorType: "Bot", authorLogin: "fullsend-ai-review", issueID: "I_kwDOPRIssue", pullRequestID: "PR_kwDOAgents1"},
		"PRRC_review_thread": {authorType: "Bot", authorLogin: "fullsend-ai-review", pullRequestID: "PR_kwDOAgents1", reviewThread: true},
		"IC_other_entity":    {authorType: "Bot", authorLogin: "fullsend-ai-triage", issueID: "I_kwDOOther1"},
	})
	g := newGitHubCommentActors()

	for name, tc := range map[string]struct {
		id   string
		want commentActor
	}{
		"bot author":               {"IC_bot", commentActor{Entity: testEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"}},
		"human author":             {"IC_human", commentActor{Entity: testEntityID, Bot: false, Login: "octocat"}},
		"bot comment human-edited": {"IC_human_edited", commentActor{Entity: testEntityID, Bot: false, Login: "octocat"}},
		"bot comment bot-edited":   {"IC_bot_edited", commentActor{Entity: testEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"}},
		"comment on a pull request": {"IC_on_pr", commentActor{
			Entity: "fullsend:entity:v1:github:github.com:change_proposal:PR_kwDOAgents1", Bot: true, Login: "fullsend-ai-review[bot]"}},
		"review-thread comment on a pull request": {"PRRC_review_thread", commentActor{
			Entity: "fullsend:entity:v1:github:github.com:change_proposal:PR_kwDOAgents1", Bot: true, Login: "fullsend-ai-review[bot]"}},
		"comment on another issue": {"IC_other_entity", commentActor{Entity: otherEntityID, Bot: true, Login: "fullsend-ai-triage[bot]"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := g.CommentActor(context.Background(), tc.id)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("unknown comment is a failure", func(t *testing.T) {
		_, err := g.CommentActor(context.Background(), "IC_missing")
		require.Error(t, err)
	})
}

func TestGitHubCommentActors_EntityID(t *testing.T) {
	t.Run("issue", func(t *testing.T) {
		useFakeGitHubGraphQL(t, "I_kwDOAgents9602", nil)
		id, err := newGitHubCommentActors().EntityID(context.Background(), "fullsend-ai/agents", "9602")
		require.NoError(t, err)
		assert.Equal(t, testEntityID, id)
	})

	t.Run("pull request", func(t *testing.T) {
		useFakeGitHubGraphQL(t, "PR_kwDOAgents1", nil)
		id, err := newGitHubCommentActors().EntityID(context.Background(), "fullsend-ai/agents", "1")
		require.NoError(t, err)
		assert.Equal(t, "fullsend:entity:v1:github:github.com:change_proposal:PR_kwDOAgents1", id)
	})

	t.Run("bad inputs fail", func(t *testing.T) {
		useFakeGitHubGraphQL(t, "I_kwDOAgents9602", nil)
		for _, in := range [][2]string{{"", "1"}, {"fullsend-ai/agents", ""}, {"fullsend-ai/agents", "x"}, {"no-slash", "1"}} {
			_, err := newGitHubCommentActors().EntityID(context.Background(), in[0], in[1])
			assert.Error(t, err, "repo %q number %q", in[0], in[1])
		}
	})
}

// botRoleCommentLookup gives the helper a comment lookup only on a GitHub
// run handling a GitHub issue or pull request it can resolve; every other
// run gets none, so the helper refuses every lookup (#9602 ac_3).
func TestBotRoleCommentLookup_OnlyGitHubRunsWithAResolvedEntity(t *testing.T) {
	printer := ui.New(io.Discard)

	t.Run("github issue", func(t *testing.T) {
		useFakeGitHubGraphQL(t, "I_kwDOAgents9602", nil)
		t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
		t.Setenv("ISSUE_NUMBER", "9602")
		t.Setenv("PR_NUMBER", "")
		entity, comments := botRoleCommentLookup(context.Background(), "github", "github", printer)
		assert.Equal(t, testEntityID, entity)
		assert.NotNil(t, comments)
	})

	t.Run("github pull request number", func(t *testing.T) {
		useFakeGitHubGraphQL(t, "PR_kwDOAgents1", nil)
		t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
		t.Setenv("ISSUE_NUMBER", "")
		t.Setenv("PR_NUMBER", "1")
		entity, comments := botRoleCommentLookup(context.Background(), "github", "", printer)
		assert.Equal(t, "fullsend:entity:v1:github:github.com:change_proposal:PR_kwDOAgents1", entity)
		assert.NotNil(t, comments)
	})

	for name, tc := range map[string]struct {
		forge, tracker, repo, issue string
		unreachable                 bool
	}{
		"gitlab":                {forge: "gitlab", repo: "group/project", issue: "1"},
		"jira-tracked":          {forge: "github", tracker: "jira", repo: "fullsend-ai/agents", issue: "1"},
		"no issue or PR number": {forge: "github", repo: "fullsend-ai/agents"},
		"no repository":         {forge: "github", issue: "1"},
		"entity lookup fails":   {forge: "github", repo: "fullsend-ai/agents", issue: "1", unreachable: true},
	} {
		t.Run(name, func(t *testing.T) {
			useFakeGitHubGraphQL(t, "I_kwDOAgents9602", nil)
			if tc.unreachable {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadGateway)
				}))
				t.Cleanup(srv.Close)
				t.Setenv("GITHUB_API_URL", srv.URL)
			}
			t.Setenv("REPO_FULL_NAME", tc.repo)
			t.Setenv("ISSUE_NUMBER", tc.issue)
			t.Setenv("PR_NUMBER", "")
			entity, comments := botRoleCommentLookup(context.Background(), tc.forge, tc.tracker, printer)
			assert.Empty(t, entity)
			assert.Nil(t, comments, "a run with no trusted lookup must give the helper none, so it refuses every lookup")
		})
	}
}
