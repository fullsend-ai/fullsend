package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/jiramock"
)

// This file holds the cross-repository vertical scenario for Jira work items
// moving through the built-in PR/MR lifecycle (#8921), for a GitHub and a
// GitLab code host. Focused unit tests for each piece live with the pieces.
//
// What runs here, all in-process and deterministic:
//   - the real jira-poll driver (jirapoll.Poller) talking HTTP to the
//     jiramock Jira server through jira.LiveClient;
//   - the production CEL matcher (newCELMatcher) over harness triggers;
//   - the GitHub output (the dispatches.json the poll workflow reads) or the
//     GitLab output driver (gitlabJiraDispatcher), stopped at the
//     DispatchJiraEvent boundary that poll.Poller tests cover;
//   - the work-item identity that `fullsend run` derives for the code run,
//     from the dispatch payload (GitHub) or --event-file (GitLab);
//   - Jira run-status updates through tracker.Client and statuscomment;
//   - native PR/MR routing for review, fix, and retro.
//
// Boundary: the built-in triage and code post-scripts that write the triage
// result, the PR/MR (with the Jira key and URL), and the code-stage update to
// Jira live in fullsend-ai/agents (agents#1843) and are not executed here.
// The scenario checks the inputs they read (FULLSEND_WORK_ITEM_KEY and
// FULLSEND_WORK_ITEM_URL, an empty ISSUE_NUMBER) and that comments posted by
// the Jira account they write as do not dispatch agents again. The default
// triage, review, and retro triggers are the pinned copies in testdata/; the
// code trigger is the documented Jira setup trigger (jira-integration.md,
// step 6), since the default code harness ships none.

const (
	lifecycleJiraBaseURL = "https://acme.atlassian.net"
	lifecycleIssueKey    = "PROJ-1"
	lifecycleIssueURL    = lifecycleJiraBaseURL + "/browse/" + lifecycleIssueKey
	lifecycleGitHubRepo  = "acme/platform"
	lifecycleGitLabRepo  = "group/sub/project"
	lifecycleGitLabURL   = "https://gitlab.example.com"

	// jiramock account IDs: commenter-001 and changer-001 hold the Jira
	// "Developers" role (actor.role write); bot-001 is the account /myself
	// returns, i.e. the account fullsend writes to Jira as. newJiraLifecycle
	// also puts bot-001 in a group-backed "Administrators" role so its
	// comments pass authorization and only bot classification can stop them.
	lifecycleDeveloper = "commenter-001"
	lifecycleOutsider  = "outsider-001"
	lifecycleJiraBot   = "bot-001"
	// lifecycleJiraBotName is a plain service-account name that the
	// poller's automation display-name pattern does not match, so only the
	// /myself account match classifies its comments as bot.
	lifecycleJiraBotName = "Fullsend Service"
)

// lifecycleCodeTrigger is the code trigger from the Jira setup guide: a
// /fs-code comment or a ready-to-code label on a Jira work item.
const lifecycleCodeTrigger = `event.source.system == "jira"
&& event.entity.kind == "work_item"
&& ((event.transition.kind == "comment_added"
     && has(event.transition.comment.command)
     && event.transition.comment.command == "/fs-code"
     && event.actor.kind == "human")
    || (event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-to-code"
        && event.transition.label.action == "added"))`

// lifecycleHarness renders a harness file with a block-scalar trigger.
func lifecycleHarness(agent, role, trigger string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent: agents/%s.md\nrole: %s\nslug: fullsend-ai-%s\n", agent, role, agent)
	b.WriteString("model: opus\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\ntrigger: |\n")
	for line := range strings.SplitSeq(strings.TrimRight(trigger, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return b.String()
}

// writeLifecycleConfig writes a .fullsend directory registering triage,
// code, review, and retro with local harnesses. Review and retro also set
// agents[].trigger, the GitHub opt-in that moves their routing to CEL.
func writeLifecycleConfig(t *testing.T) string {
	t.Helper()
	triage := routeTrigger(t, "triage")
	retro := routeTrigger(t, "retro")
	review := reviewTrigger(t)
	files := map[string]string{
		"harness/triage.yaml": lifecycleHarness("triage", "triage", triage),
		"harness/code.yaml":   lifecycleHarness("code", "coder", lifecycleCodeTrigger),
		"harness/review.yaml": lifecycleHarness("review", "review", review),
		"harness/retro.yaml":  lifecycleHarness("retro", "retro", retro),
	}
	for _, agent := range []string{"triage", "code", "review", "retro"} {
		files["agents/"+agent+".md"] = "# " + agent + "\n"
	}
	return writeRouteFixture(t, routeFixture{
		agents: []config.AgentEntry{
			{Name: "triage", Source: "harness/triage.yaml"},
			{Name: "code", Source: "harness/code.yaml"},
			{Name: "review", Source: "harness/review.yaml", Trigger: review},
			{Name: "retro", Source: "harness/retro.yaml", Trigger: retro},
		},
		files: files,
	})
}

// countingTransport counts every HTTP request made through the Jira client.
type countingTransport struct {
	n    *atomic.Int64
	base http.RoundTripper
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}

// lifecycleDispatch is one agent dispatch observed at the code-host
// boundary, normalized across the GitHub and GitLab outputs.
type lifecycleDispatch struct {
	agent string
	event *normevent.Event
	// payload is the GitHub dispatch record's event_payload; empty for
	// GitLab, whose agent job receives the event as --event-file.
	payload string
	// statusNumber is the GitHub dispatch record's status_number.
	statusNumber string
}

// pipelineCall is one DispatchJiraEvent call seen by the GitLab fake.
type pipelineCall struct {
	stage string
	event *normevent.Event
}

// recordingPipelineDispatcher stands in for poll.Poller.DispatchJiraEvent:
// it records every pipeline it is asked to start and rejects the next
// failNext calls for stage failStage, as a transient pipeline failure.
type recordingPipelineDispatcher struct {
	created   []pipelineCall
	failStage string
	failNext  int
	rejected  int
}

var errPipelineRejected = errors.New("pipeline creation rejected")

func (r *recordingPipelineDispatcher) DispatchJiraEvent(_ context.Context, stage string, event *normevent.Event) error {
	if stage == r.failStage && r.failNext > 0 {
		r.failNext--
		r.rejected++
		return errPipelineRejected
	}
	r.created = append(r.created, pipelineCall{stage: stage, event: event})
	return nil
}

// jiraLifecycle drives one code host through the scenario.
type jiraLifecycle struct {
	host      string
	repo      string
	configDir string
	state     *jiramock.State
	client    *jira.LiveClient
	requests  *atomic.Int64
	matcher   *celMatcher
	pipelines *recordingPipelineDispatcher
	output    string
	// base anchors activity timestamps. Every comment and label change is
	// placed after base and well before the poller's change-detection
	// safety margin, so each cycle's checkpoint covers it.
	base time.Time
	step int
}

func newJiraLifecycle(t *testing.T, host string) *jiraLifecycle {
	t.Helper()
	srv, state := jiramock.NewServer()
	t.Cleanup(srv.Close)
	requests := &atomic.Int64{}
	client, err := jira.New("test-token",
		jira.WithBaseURL(srv.URL),
		jira.WithHTTPClient(&http.Client{Transport: countingTransport{n: requests, base: http.DefaultTransport}}),
	)
	require.NoError(t, err)

	configDir := writeLifecycleConfig(t)
	matcher, err := newCELMatcher(context.Background(), configDir)
	require.NoError(t, err)

	l := &jiraLifecycle{
		host:      host,
		configDir: configDir,
		state:     state,
		client:    client,
		requests:  requests,
		matcher:   matcher,
		pipelines: &recordingPipelineDispatcher{},
		output:    filepath.Join(t.TempDir(), "dispatches.json"),
		base:      time.Now().Add(-30 * time.Minute),
	}
	l.repo = lifecycleGitHubRepo
	if host == jirapoll.TargetForgeGitLab {
		l.repo = lifecycleGitLabRepo
	}
	// Give the bot account a write-or-higher project role. Without one its
	// comments would be rejected as external, masking a regression in the
	// poller's self-authored-comment (bot) classification.
	state.SetRoleGroup("Administrators", "fullsend-bot-group")
	state.SetUserGroups(lifecycleJiraBot, "fullsend-bot-group")
	state.AddIssue(lifecycleIssueKey, []string{"bug"})
	return l
}

// at returns the timestamp for the next piece of Jira activity.
func (l *jiraLifecycle) at() time.Time {
	l.step++
	return l.base.Add(time.Duration(l.step) * time.Minute)
}

func (l *jiraLifecycle) comment(accountID, displayName, body string) {
	l.state.AddCommentFromActorAt(lifecycleIssueKey, body, accountID, displayName, l.at())
}

// options returns the jira-poll options the CLI builds for this host:
// runJiraPoll for GitHub and runJiraPollGitLab for GitLab.
func (l *jiraLifecycle) options() jirapoll.Options {
	opts := jirapoll.Options{
		TargetRepo:  l.repo,
		JiraBaseURL: lifecycleJiraBaseURL,
		JiraProject: "PROJ",
	}
	if l.host == jirapoll.TargetForgeGitLab {
		opts.TargetForge = jirapoll.TargetForgeGitLab
		opts.GitLabURL = lifecycleGitLabURL
		opts.Dispatcher = gitlabJiraDispatcher{poller: l.pipelines}
		return opts
	}
	opts.OutputPath = l.output
	return opts
}

// poll runs one poll cycle and returns the dispatches it produced at the
// code-host boundary, with the cycle's error.
func (l *jiraLifecycle) poll(t *testing.T) ([]lifecycleDispatch, error) {
	t.Helper()
	before := len(l.pipelines.created)
	err := jirapoll.New(l.client, l.matcher, l.options()).Run(context.Background())

	if l.host == jirapoll.TargetForgeGitLab {
		var out []lifecycleDispatch
		for _, c := range l.pipelines.created[before:] {
			out = append(out, lifecycleDispatch{agent: c.stage, event: c.event})
		}
		return out, err
	}
	if err != nil {
		return nil, err
	}
	data, readErr := os.ReadFile(l.output)
	require.NoError(t, readErr)
	var records []jirapoll.DispatchRecord
	require.NoError(t, json.Unmarshal(data, &records))
	var out []lifecycleDispatch
	for _, r := range records {
		var payload struct {
			NormalizedEvent json.RawMessage `json:"_normalized_event"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.EventPayload), &payload))
		ev, parseErr := normevent.ParseJSON(payload.NormalizedEvent)
		require.NoError(t, parseErr)
		assert.Equal(t, l.repo, r.SourceRepo)
		assert.Equal(t, l.repo, r.StatusRepo)
		out = append(out, lifecycleDispatch{agent: r.Agent, event: ev, payload: r.EventPayload, statusNumber: r.StatusNumber})
	}
	return out, nil
}

// mustPoll runs a cycle that must succeed.
func (l *jiraLifecycle) mustPoll(t *testing.T) []lifecycleDispatch {
	t.Helper()
	got, err := l.poll(t)
	require.NoError(t, err)
	return got
}

// requireSingleDispatch asserts exactly one dispatch of agent for the
// lifecycle issue, carrying its Jira identity, and returns it.
func (l *jiraLifecycle) requireSingleDispatch(t *testing.T, got []lifecycleDispatch, agent string) lifecycleDispatch {
	t.Helper()
	require.Len(t, got, 1, "dispatches: %+v", got)
	d := got[0]
	assert.Equal(t, agent, d.agent)
	assert.Equal(t, l.repo, d.event.Repo, "the event targets the configured code-host project")
	assert.Equal(t, normevent.SystemJira, d.event.Source.System)
	assert.Equal(t, normevent.EntityWorkItem, d.event.Entity.Kind)
	assert.Equal(t, lifecycleIssueKey, d.event.Entity.Key)
	assert.Equal(t, lifecycleIssueURL, d.event.Entity.URL)
	assert.Equal(t, normevent.ActorHuman, d.event.Actor.Kind)
	assert.Equal(t, normevent.RoleWrite, d.event.Actor.Role)
	return d
}

// codeRunIdentity derives what `fullsend run` exports for a dispatched run,
// using run's own helpers in run's order: the normalized event comes from
// .fullsend/dispatch/event-payload.json on GitHub and from --event-file on
// GitLab (run-agent-job.sh).
func (l *jiraLifecycle) codeRunIdentity(t *testing.T, d lifecycleDispatch) (env map[string]string, sOpts statusOpts) {
	t.Helper()
	var eventMap map[string]any
	if l.host == jirapoll.TargetForgeGitLab {
		m, err := d.event.ToMap()
		require.NoError(t, err)
		eventMap = m
	} else {
		dir := t.TempDir()
		writeDispatchPayload(t, dir, d.payload)
		eventMap = extractNormalizedEventFromDispatch(dir)
		require.NotNil(t, eventMap, "run must recover the normalized event from the dispatch payload")
		assert.False(t, dispatchJiraClaimUnresolved("", string(normevent.SystemJira), dir))
	}

	sOpts.trackerSource = extractMapString(eventMap, "source", "system")
	require.Equal(t, string(normevent.SystemJira), sOpts.trackerSource)
	proj, num, ok := parseJiraKey(extractMapString(eventMap, "entity", "key"))
	require.True(t, ok)
	sOpts.trackerProject, sOpts.statusNum = proj, num

	env, err := resolveWorkItemIdentity(eventMap, sOpts.trackerSource, l.host, noEnv)
	require.NoError(t, err)
	return env, sOpts
}

// postRunStatus posts the Jira run-status start and completion comments
// for a run the way setupStatusNotifierJira wires them, then mirrors the
// stored comments onto the mock Jira issue as authored by the account
// fullsend writes as (bot-001, a regular Atlassian account).
func (l *jiraLifecycle) postRunStatus(t *testing.T, sOpts statusOpts, runID string) {
	t.Helper()
	ctx := context.Background()
	tc, fake, err := tracker.NewFakeJiraClientWithFake(lifecycleJiraBaseURL)
	require.NoError(t, err)
	fake.Myself = jira.User{AccountID: lifecycleJiraBot, DisplayName: lifecycleJiraBotName, AccountType: "atlassian", Active: true}

	cfg := config.StatusNotificationConfig{Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"}}
	n := statuscomment.New(tc, cfg, sOpts.trackerProject, sOpts.statusNum, "https://ci.example.com/run/"+runID, "", runID)
	require.NoError(t, n.PostStart(ctx, "Code"))
	require.NoError(t, n.PostCompletion(ctx, "Code", "success"))

	stored := fake.Comments[lifecycleIssueKey]
	require.Len(t, stored, 1, "start and completion share one Jira status comment")
	assert.Equal(t, lifecycleJiraBot, stored[0].Author.AccountID, "status updates are written as the poller's Jira account")

	marker := tracker.StatusMarkerOpener + runID + " -->"
	found, terminal, err := tc.FindStatusComment(ctx, sOpts.trackerProject, sOpts.statusNum, marker)
	require.NoError(t, err)
	require.NotNil(t, found, "a retry finds the run's status comment by its marker instead of posting another")
	assert.Equal(t, stored[0].ID, found.ID)
	assert.True(t, terminal)

	comments, err := tc.ListComments(ctx, sOpts.trackerProject, sOpts.statusNum)
	require.NoError(t, err)
	for _, c := range comments {
		l.comment(lifecycleJiraBot, lifecycleJiraBotName, string(c.Body))
	}
}

// TestJiraWorkItemVerticalLifecycle drives a Jira work item through triage
// and code dispatch on each code host, then the native PR/MR stages, and
// checks the loop, authorization, retry, and identity guarantees of #8921.
func TestJiraWorkItemVerticalLifecycle(t *testing.T) {
	// run reads GITHUB_EVENT_PATH as a second dispatch-payload channel;
	// clear it so a CI runner's own event cannot leak into the scenario.
	t.Setenv("GITHUB_EVENT_PATH", "")

	for _, host := range []string{jirapoll.TargetForgeGitHub, jirapoll.TargetForgeGitLab} {
		t.Run(host, func(t *testing.T) {
			l := newJiraLifecycle(t, host)

			// 1. A human /fs-triage comment dispatches triage once. The
			// issue's own "opened" event is also seen on this first poll,
			// but its reporter holds no project role, so it dispatches
			// nothing.
			l.comment(lifecycleDeveloper, "Test Commenter", "/fs-triage check acceptance criteria")
			triage := l.requireSingleDispatch(t, l.mustPoll(t), "triage")
			require.NotNil(t, triage.event.Transition.Comment)
			assert.Equal(t, "/fs-triage", triage.event.Transition.Comment.Command)
			assert.Equal(t, "check acceptance criteria", triage.event.Transition.Comment.Instruction)
			triageEnv, triageStatus := l.codeRunIdentity(t, triage)
			assert.Equal(t, lifecycleIssueKey, triageEnv[envWorkItemKey])

			// 2. Triage writes back to Jira (run status, plus a result
			// comment quoting /fs-code) as the poller's account. In the
			// same cycle, a user without a project role asks for /fs-code
			// and a developer asks for /fs-review, which Jira issues cannot
			// start. Nothing dispatches, and the triage comment from step 1
			// is not replayed.
			l.postRunStatus(t, triageStatus, "1001")
			l.comment(lifecycleJiraBot, lifecycleJiraBotName, "/fs-code\nTriage result: ready to implement")
			l.comment(lifecycleOutsider, "Outside User", "/fs-code please")
			l.comment(lifecycleDeveloper, "Test Commenter", "/fs-review")
			assert.Empty(t, l.mustPoll(t), "bot write-back, unauthorized, and non-Jira commands must not dispatch")

			// 3. /fs-code from a developer dispatches code. On GitLab the
			// first pipeline creation fails: the cycle fails, the issue's
			// checkpoint stays put, and the next cycle retries it.
			l.comment(lifecycleDeveloper, "Test Commenter", "/fs-code keep the change small")
			if host == jirapoll.TargetForgeGitLab {
				l.pipelines.failStage, l.pipelines.failNext = "code", 1
				failed, err := l.poll(t)
				require.ErrorIs(t, err, errPipelineRejected)
				assert.Empty(t, failed)
				assert.Equal(t, 1, l.pipelines.rejected)
			}
			code := l.requireSingleDispatch(t, l.mustPoll(t), "code")
			require.NotNil(t, code.event.Transition.Comment)
			assert.Equal(t, "/fs-code", code.event.Transition.Comment.Command)

			// The code run gets the Jira key and URL its post-script puts in
			// the PR/MR body and writes the code-stage update to. No Jira ID
			// or key reaches a numeric forge issue field: ISSUE_NUMBER is
			// exported empty, and status routing goes to the Jira project
			// with the key's number, not the numeric Jira entity ID.
			env, sOpts := l.codeRunIdentity(t, code)
			assert.Equal(t, map[string]string{
				envWorkItemSource: string(normevent.SystemJira),
				envWorkItemKey:    lifecycleIssueKey,
				envWorkItemURL:    lifecycleIssueURL,
			}, env)
			issueNumber, export := issueNumberFlagEnv(sOpts)
			assert.True(t, export, "ISSUE_NUMBER must be exported to clear an inherited value")
			assert.Empty(t, issueNumber)
			assert.Equal(t, "PROJ", sOpts.trackerProject)
			assert.Equal(t, 1, sOpts.statusNum)
			assert.NotEqual(t, code.event.Entity.ID, sOpts.statusNum, "the numeric Jira entity ID is not used as a status number")
			if host == jirapoll.TargetForgeGitHub {
				assert.Equal(t, strconv.Itoa(code.event.Entity.ID), code.statusNumber,
					"the GitHub record keeps the Jira entity ID; run replaces it with the key's number")
			}

			// 4. A developer adds ready-to-code: code dispatches once more,
			// and the /fs-code comment from step 3 is not dispatched again.
			l.state.AddLabelChangeAt(lifecycleIssueKey, "ready-to-code", l.at())
			label := l.requireSingleDispatch(t, l.mustPoll(t), "code")
			require.NotNil(t, label.event.Transition.Label)
			assert.Equal(t, "ready-to-code", label.event.Transition.Label.Name)
			assert.Equal(t, "added", label.event.Transition.Label.Action)

			// 5. The PR/MR the code agent opened continues through review,
			// fix, and retro on the code host, without any Jira API call.
			jiraCalls := l.requests.Load()
			if host == jirapoll.TargetForgeGitLab {
				assertGitLabNativeStages(t, l.configDir)
			} else {
				assertGitHubNativeStages(t, l.configDir)
			}
			assert.Equal(t, jiraCalls, l.requests.Load(), "PR/MR stages must not call Jira")
		})
	}
}

// loadExampleEvent reads a normative normalized-event example.
func loadExampleEvent(t *testing.T, name string) *normevent.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", name))
	require.NoError(t, err)
	ev, err := normevent.ParseJSON(raw)
	require.NoError(t, err)
	return ev
}

// assertForgeRunIdentity checks a PR/MR run keeps forge identity: no Jira
// work-item key, and ISSUE_NUMBER set from the forge number.
func assertForgeRunIdentity(t *testing.T, ev *normevent.Event, forgePlatform string) {
	t.Helper()
	m, err := ev.ToMap()
	require.NoError(t, err)
	source := string(ev.Source.System)
	env, err := resolveWorkItemIdentity(m, source, forgePlatform, noEnv)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{envWorkItemSource: forgePlatform}, env)
	issueNumber, export := issueNumberFlagEnv(statusOpts{trackerSource: source, statusNum: ev.Entity.ID})
	assert.True(t, export)
	assert.Equal(t, strconv.Itoa(ev.Entity.ID), issueNumber)
}

// assertGitHubNativeStages routes the GitHub pull request fixtures through
// `fullsend dispatch --route` (routeBuiltinAgent) with the review and retro
// CEL routes enabled. GitHub fix routing stays in the bash router of
// reusable-dispatch.yml, which this package cannot execute.
func assertGitHubNativeStages(t *testing.T, configDir string) {
	t.Helper()
	for _, tc := range []struct{ agent, fixture string }{
		{"review", "pr-opened.json"},
		{"review", "pr-synchronized.json"},
		{"retro", "pr-closed.json"},
	} {
		ev := loadExampleEvent(t, tc.fixture)
		got, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent:       tc.agent,
			configDir:   configDir,
			event:       ev,
			forgeClient: forge.NewFakeClient(),
			printer:     ui.New(io.Discard),
		})
		require.NoError(t, err)
		assert.Equal(t, routeRun, got.Decision, "%s on %s: %s", tc.agent, tc.fixture, got.Reason)
		assertForgeRunIdentity(t, ev, string(normevent.SystemGitHub))
	}
}

// assertGitLabNativeStages routes the GitLab merge request fixtures through
// the router the GitLab poller uses (buildRouter).
func assertGitLabNativeStages(t *testing.T, configDir string) {
	t.Helper()
	router, err := buildRouter(configDir)
	require.NoError(t, err)

	opened := loadExampleEvent(t, "gitlab-mr-opened.json")
	merged := loadExampleEvent(t, "gitlab-mr-merged.json")
	// The review agent's changes-requested note on the MR starts fix.
	changes := loadExampleEvent(t, "gitlab-mr-opened.json")
	changes.Transition = normevent.Transition{
		Kind:    normevent.TransitionCommentAdded,
		Comment: &normevent.Comment{Body: "Changes requested.\n" + forge.ChangesRequestedMarker},
	}
	changes.Actor = normevent.Actor{ID: "fullsend-review-bot", Kind: normevent.ActorBot, Role: normevent.RoleWrite}
	changes.Source.RawType, changes.Source.RawAction = "note", "created"

	for _, tc := range []struct {
		name  string
		ev    *normevent.Event
		stage string
	}{
		{"MR opened", opened, "review"},
		{"changes requested", changes, "fix"},
		{"MR merged", merged, "retro"},
	} {
		raw, err := json.Marshal(tc.ev)
		require.NoError(t, err)
		var de dispatch.NormalizedEvent
		require.NoError(t, json.Unmarshal(raw, &de))
		stages, err := router.Route(&de)
		require.NoError(t, err)
		assert.Equal(t, []string{tc.stage}, stages, tc.name)
		assertForgeRunIdentity(t, tc.ev, string(normevent.SystemGitLab))
	}
}
