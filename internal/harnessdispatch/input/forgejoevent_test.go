package input_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const (
	fjServer = "https://code.example.com/forgejo"
	fjRepo   = "acme/widgets"
	fjSHA    = "0123456789abcdef0123456789abcdef01234567"
)

func fjOpts(eventName string) input.ForgejoEventOptions {
	return input.ForgejoEventOptions{
		EventName:  eventName,
		Repository: fjRepo,
		ServerURL:  fjServer,
		ResolveRole: func(_ context.Context, login string) (normevent.ActorRole, error) {
			if login == "alice" {
				return normevent.RoleWrite, nil
			}
			return normevent.RoleRead, nil
		},
	}
}

func fjUser(login string) map[string]any {
	return map[string]any{"id": float64(7), "login": login}
}

func fjLabels(names ...string) []any {
	out := []any{}
	for _, n := range names {
		out = append(out, map[string]any{"id": float64(1), "name": n})
	}
	return out
}

func fjIssue(labels ...string) map[string]any {
	return map[string]any{
		"id":        float64(9001),
		"number":    float64(12),
		"html_url":  fjServer + "/" + fjRepo + "/issues/12",
		"user":      fjUser("bob"),
		"labels":    fjLabels(labels...),
		"assignees": []any{fjUser("alice")},
	}
}

func fjPR(labels ...string) map[string]any {
	return map[string]any{
		"id":        float64(9100),
		"number":    float64(34),
		"html_url":  fjServer + "/" + fjRepo + "/pulls/34",
		"title":     "Add widget",
		"user":      fjUser("alice"),
		"labels":    fjLabels(labels...),
		"assignees": nil,
		"merged":    false,
		"draft":     false,
		"head": map[string]any{
			"ref":  "feature",
			"sha":  fjSHA,
			"repo": map[string]any{"full_name": fjRepo},
		},
		"base": map[string]any{
			"ref":  "main",
			"sha":  "fedcba9876543210fedcba9876543210fedcba98",
			"repo": map[string]any{"full_name": fjRepo},
		},
	}
}

func fjPayload(action string, extra map[string]any) map[string]any {
	p := map[string]any{
		"action":     action,
		"repository": map[string]any{"id": float64(3), "full_name": fjRepo},
		"sender":     fjUser("alice"),
	}
	maps.Copy(p, extra)
	return p
}

func fjMap(t *testing.T, opts input.ForgejoEventOptions, payload map[string]any) (*normevent.Event, error) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return input.MapForgejoEvent(context.Background(), opts, data)
}

func TestMapForgejoEvent_SupportedMappings(t *testing.T) {
	merged := fjPR()
	merged["merged"] = true

	tests := []struct {
		name      string
		eventName string
		payload   map[string]any
		entity    normevent.EntityKind
		kind      normevent.TransitionKind
	}{
		{"issue opened", "issues", fjPayload("opened", map[string]any{"issue": fjIssue()}), normevent.EntityWorkItem, normevent.TransitionOpened},
		{"issue reopened", "issues", fjPayload("reopened", map[string]any{"issue": fjIssue()}), normevent.EntityWorkItem, normevent.TransitionReopened},
		{"issue edited", "issues", fjPayload("edited", map[string]any{"issue": fjIssue()}), normevent.EntityWorkItem, normevent.TransitionEdited},
		{"issue closed", "issues", fjPayload("closed", map[string]any{"issue": fjIssue()}), normevent.EntityWorkItem, normevent.TransitionClosed},
		{"issue assigned", "issues", fjPayload("assigned", map[string]any{"issue": fjIssue()}), normevent.EntityWorkItem, normevent.TransitionUpdated},
		{"pr opened", "pull_request", fjPayload("opened", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionOpened},
		{"pr reopened", "pull_request", fjPayload("reopened", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionReopened},
		{"pr synchronized", "pull_request", fjPayload("synchronized", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionSynchronized},
		{"pr ready for review", "pull_request", fjPayload("ready_for_review", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionMarkedReady},
		{"pr edited", "pull_request", fjPayload("edited", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionEdited},
		{"pr closed unmerged", "pull_request", fjPayload("closed", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionClosed},
		{"pr closed merged", "pull_request", fjPayload("closed", map[string]any{"pull_request": merged}), normevent.EntityChangeProposal, normevent.TransitionMerged},
		{"pr review requested", "pull_request", fjPayload("review_requested", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionUpdated},
		{"pr target opened", "pull_request_target", fjPayload("opened", map[string]any{"pull_request": fjPR()}), normevent.EntityChangeProposal, normevent.TransitionOpened},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := fjMap(t, fjOpts(tc.eventName), tc.payload)
			require.NoError(t, err)
			require.NoError(t, ev.Validate())
			assert.Equal(t, tc.entity, ev.Entity.Kind)
			assert.Equal(t, tc.kind, ev.Transition.Kind)
			assert.Equal(t, fjRepo, ev.Repo)
			assert.Equal(t, "alice", ev.Actor.ID)
			assert.Equal(t, normevent.ActorHuman, ev.Actor.Kind)
			assert.Equal(t, normevent.RoleWrite, ev.Actor.Role)
			assert.Equal(t, normevent.SystemForgejo, ev.Source.System)
			assert.Equal(t, tc.eventName, ev.Source.RawType)
			assert.Equal(t, tc.payload["action"], ev.Source.RawAction)
			if tc.entity == normevent.EntityChangeProposal {
				require.NotNil(t, ev.State.ChangeProposal)
				assert.Equal(t, 34, ev.Entity.ID)
				assert.Equal(t, fjSHA, ev.State.ChangeProposal.HeadSHA)
			} else {
				assert.Equal(t, 12, ev.Entity.ID)
				assert.Nil(t, ev.State.ChangeProposal)
			}
		})
	}
}

func TestMapForgejoEvent_IssueActorFacts(t *testing.T) {
	ev, err := fjMap(t, fjOpts("issues"), fjPayload("opened", map[string]any{"issue": fjIssue("bug")}))
	require.NoError(t, err)
	assert.Equal(t, fjServer+"/"+fjRepo+"/issues/12", ev.Entity.URL)
	assert.Equal(t, []string{"bug"}, ev.State.Labels)
	assert.False(t, ev.Actor.IsEntityAuthor)
	assert.True(t, ev.Actor.IsEntityAssignee)
}

func TestMapForgejoEvent_PRChangeProposal(t *testing.T) {
	pr := fjPR("needs-review")
	pr["head"].(map[string]any)["repo"] = map[string]any{"full_name": "mallory/widgets"}
	pr["assignee"] = fjUser("alice")
	ev, err := fjMap(t, fjOpts("pull_request_target"), fjPayload("opened", map[string]any{"pull_request": pr}))
	require.NoError(t, err)
	assert.Equal(t, &normevent.ChangeProposal{
		ID:       34,
		HeadRepo: "mallory/widgets",
		BaseRepo: fjRepo,
		HeadRef:  "feature",
		BaseRef:  "main",
		HeadSHA:  fjSHA,
		AuthorID: "alice",
		IsFork:   true,
	}, ev.State.ChangeProposal)
	assert.Equal(t, []string{"needs-review"}, ev.State.Labels)
	assert.True(t, ev.Actor.IsEntityAuthor)
	assert.True(t, ev.Actor.IsEntityAssignee, "legacy assignee is used when assignees is absent")
}

func TestMapForgejoEvent_PRMissingHeadRepoIsFork(t *testing.T) {
	pr := fjPR()
	delete(pr["head"].(map[string]any), "repo")
	ev, err := fjMap(t, fjOpts("pull_request"), fjPayload("opened", map[string]any{"pull_request": pr}))
	require.NoError(t, err)
	assert.True(t, ev.State.ChangeProposal.IsFork)
}

func TestMapForgejoEvent_ReadyByRemovingWIPPrefix(t *testing.T) {
	changes := map[string]any{"title": map[string]any{"from": "WIP: Add widget"}}

	ev, err := fjMap(t, fjOpts("pull_request"), fjPayload("edited", map[string]any{"pull_request": fjPR(), "changes": changes}))
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionMarkedReady, ev.Transition.Kind)

	bracket := map[string]any{"title": map[string]any{"from": "[wip] Add widget"}}
	ev, err = fjMap(t, fjOpts("pull_request"), fjPayload("edited", map[string]any{"pull_request": fjPR(), "changes": bracket}))
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionMarkedReady, ev.Transition.Kind)

	draft := fjPR()
	draft["draft"] = true
	ev, err = fjMap(t, fjOpts("pull_request"), fjPayload("edited", map[string]any{"pull_request": draft, "changes": changes}))
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionEdited, ev.Transition.Kind, "still a draft")

	noDraft := fjPR()
	delete(noDraft, "draft")
	ev, err = fjMap(t, fjOpts("pull_request"), fjPayload("edited", map[string]any{"pull_request": noDraft, "changes": changes}))
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionEdited, ev.Transition.Kind, "draft state unknown")

	stillWIP := fjPR()
	stillWIP["title"] = "[WIP] Add widget"
	ev, err = fjMap(t, fjOpts("pull_request"), fjPayload("edited", map[string]any{"pull_request": stillWIP, "changes": changes}))
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionEdited, ev.Transition.Kind, "title still has a WIP prefix")
}

func TestMapForgejoEvent_LabelTransitions(t *testing.T) {
	tests := []struct {
		name      string
		eventName string
		action    string
		labels    []string
		want      string
	}{
		{"issue label_updated added", "issues", "label_updated", []string{"ready-to-code"}, "added"},
		{"issue label_updated removed", "issues", "label_updated", []string{"other"}, "removed"},
		{"issue labeled", "issues", "labeled", []string{"ready-to-code"}, "added"},
		{"issue unlabeled", "issues", "unlabeled", []string{}, "removed"},
		{"issue label_cleared", "issues", "label_cleared", []string{}, "removed"},
		{"pr label_updated added", "pull_request", "label_updated", []string{"ready-to-code"}, "added"},
		{"pr unlabeled", "pull_request", "unlabeled", []string{}, "removed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"label": map[string]any{"name": "ready-to-code"}}
			if tc.eventName == "issues" {
				extra["issue"] = fjIssue(tc.labels...)
			} else {
				extra["pull_request"] = fjPR(tc.labels...)
			}
			ev, err := fjMap(t, fjOpts(tc.eventName), fjPayload(tc.action, extra))
			require.NoError(t, err)
			assert.Equal(t, normevent.TransitionLabelChanged, ev.Transition.Kind)
			assert.Equal(t, &normevent.LabelChange{Name: "ready-to-code", Action: tc.want}, ev.Transition.Label)
			assert.Equal(t, tc.labels, ev.State.Labels)
		})
	}
}

func TestMapForgejoEvent_LabelFailures(t *testing.T) {
	tests := []struct {
		name   string
		action string
		labels []string
		label  any
		want   string
	}{
		{"label_updated without label", "label_updated", []string{"ready-to-code"}, nil, "does not name the changed label"},
		{"label_cleared without label", "label_cleared", []string{}, nil, "does not name the changed label"},
		{"empty label name", "labeled", []string{"ready-to-code"}, map[string]any{"name": " "}, "does not name the changed label"},
		{"labeled but absent", "labeled", []string{}, map[string]any{"name": "ready-to-code"}, "not in the label snapshot"},
		{"unlabeled but present", "unlabeled", []string{"ready-to-code"}, map[string]any{"name": "ready-to-code"}, "still in the label snapshot"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"issue": fjIssue(tc.labels...)}
			if tc.label != nil {
				extra["label"] = tc.label
			}
			ev, err := fjMap(t, fjOpts("issues"), fjPayload(tc.action, extra))
			require.Error(t, err)
			assert.Nil(t, ev)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestMapForgejoEvent_IssueComment(t *testing.T) {
	comment := map[string]any{"id": float64(555), "body": "/fs-code please fix it\nmore context", "user": fjUser("alice")}
	ev, err := fjMap(t, fjOpts("issue_comment"), fjPayload("created", map[string]any{"issue": fjIssue("bug"), "comment": comment}))
	require.NoError(t, err)
	assert.Equal(t, normevent.EntityWorkItem, ev.Entity.Kind)
	assert.Equal(t, normevent.TransitionCommentAdded, ev.Transition.Kind)
	assert.Equal(t, &normevent.Comment{
		ID:          "555",
		Command:     "/fs-code",
		Instruction: "please fix it",
		Body:        "/fs-code please fix it\nmore context",
	}, ev.Transition.Comment)
	assert.Nil(t, ev.Entity.LinkedChangeProposal)
	assert.Equal(t, []string{"bug"}, ev.State.Labels)

	plain := map[string]any{"id": float64(556), "body": "Thanks!"}
	ev, err = fjMap(t, fjOpts("issue_comment"), fjPayload("created", map[string]any{"issue": fjIssue(), "comment": plain}))
	require.NoError(t, err)
	assert.Equal(t, "Thanks", ev.Transition.Comment.Command, "first word is extracted like gha-event")
	assert.Equal(t, "556", ev.Transition.Comment.ID)
}

func TestMapForgejoEvent_CommentEditsDoNotParseCommands(t *testing.T) {
	comment := map[string]any{"id": float64(555), "body": "/fs-code again"}
	for action, kind := range map[string]normevent.TransitionKind{
		"edited":  normevent.TransitionCommentEdited,
		"deleted": normevent.TransitionCommentDeleted,
	} {
		ev, err := fjMap(t, fjOpts("issue_comment"), fjPayload(action, map[string]any{"issue": fjIssue(), "comment": comment}))
		require.NoError(t, err, action)
		assert.Equal(t, kind, ev.Transition.Kind, action)
		assert.Empty(t, ev.Transition.Comment.Command, action)
		assert.Empty(t, ev.Transition.Comment.Instruction, action)
		assert.Equal(t, "555", ev.Transition.Comment.ID, action)
	}
}

func TestMapForgejoEvent_PullRequestComment(t *testing.T) {
	issue := fjIssue()
	issue["number"] = float64(34)
	issue["html_url"] = fjServer + "/" + fjRepo + "/issues/34"
	issue["pull_request"] = map[string]any{"merged": false, "html_url": fjServer + "/" + fjRepo + "/pulls/34"}
	comment := map[string]any{"id": float64(777), "body": "/fs-review"}

	ev, err := fjMap(t, fjOpts("issue_comment"), fjPayload("created", map[string]any{"issue": issue, "comment": comment, "is_pull": true}))
	require.NoError(t, err)
	require.NotNil(t, ev.Entity.LinkedChangeProposal)
	assert.Equal(t, 34, ev.Entity.LinkedChangeProposal.ID)
	assert.Equal(t, fjServer+"/"+fjRepo+"/pulls/34", ev.Entity.LinkedChangeProposal.URL)
	assert.Nil(t, ev.State.ChangeProposal, "no pull_request object in the payload")
	assert.Equal(t, "/fs-review", ev.Transition.Comment.Command)

	// A payload that carries the pull request also carries its head.
	ev, err = fjMap(t, fjOpts("issue_comment"), fjPayload("created", map[string]any{"issue": issue, "comment": comment, "is_pull": true, "pull_request": fjPR()}))
	require.NoError(t, err)
	require.NotNil(t, ev.State.ChangeProposal)
	assert.Equal(t, fjSHA, ev.State.ChangeProposal.HeadSHA)

	// is_pull alone links the derived /pulls/ URL.
	bare := fjIssue()
	ev, err = fjMap(t, fjOpts("issue_comment"), fjPayload("created", map[string]any{"issue": bare, "comment": comment, "is_pull": true}))
	require.NoError(t, err)
	assert.Equal(t, fjServer+"/"+fjRepo+"/pulls/12", ev.Entity.LinkedChangeProposal.URL)

	// A pull_request object for another number is rejected.
	other := fjPR()
	other["number"] = float64(99)
	_, err = fjMap(t, fjOpts("issue_comment"), fjPayload("created", map[string]any{"issue": issue, "comment": comment, "is_pull": true, "pull_request": other}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match issue.number")
}

func TestMapForgejoEvent_ReviewSubmitted(t *testing.T) {
	tests := map[string]string{
		"pull_request_review_approved": "approved",
		"pull_request_review_rejected": "changes_requested",
		"pull_request_review_comment":  "commented",
	}
	for reviewType, want := range tests {
		t.Run(reviewType, func(t *testing.T) {
			payload := fjPayload("reviewed", map[string]any{
				"pull_request": fjPR(),
				"review":       map[string]any{"type": reviewType, "content": "looks good"},
				"sender":       fjUser("carol"),
			})
			ev, err := fjMap(t, fjOpts("pull_request_review"), payload)
			require.NoError(t, err)
			assert.Equal(t, normevent.TransitionReviewSubmitted, ev.Transition.Kind)
			assert.Equal(t, &normevent.Review{State: want, ReviewerID: "carol"}, ev.Transition.Review)
			assert.Equal(t, "carol", ev.Actor.ID)
			assert.Equal(t, normevent.RoleRead, ev.Actor.Role)
			assert.False(t, ev.Actor.IsEntityAuthor)
			assert.Equal(t, fjSHA, ev.State.ChangeProposal.HeadSHA)
		})
	}

	ev, err := fjMap(t, fjOpts("pull_request_review"), fjPayload("submitted", map[string]any{
		"pull_request": fjPR(),
		"review":       map[string]any{"state": "REQUEST_CHANGES"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "changes_requested", ev.Transition.Review.State)
}

func TestMapForgejoEvent_ReviewFailures(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		payload map[string]any
		want    string
	}{
		{"missing review", "reviewed", map[string]any{"pull_request": fjPR()}, "missing review"},
		{"missing review type", "reviewed", map[string]any{"pull_request": fjPR(), "review": map[string]any{}}, "review type is missing"},
		{"unknown review type", "reviewed", map[string]any{"pull_request": fjPR(), "review": map[string]any{"type": "pending"}}, "unsupported review type"},
		{"unknown action", "dismissed", map[string]any{"pull_request": fjPR(), "review": map[string]any{"type": "pull_request_review_approved"}}, "unsupported pull_request_review action"},
		{"missing pull request", "reviewed", map[string]any{"review": map[string]any{"type": "pull_request_review_approved"}}, "missing pull_request"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := fjMap(t, fjOpts("pull_request_review"), fjPayload(tc.action, tc.payload))
			require.Error(t, err)
			assert.Nil(t, ev)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestMapForgejoEvent_ActorKindAndRole(t *testing.T) {
	payload := fjPayload("synchronized", map[string]any{
		"pull_request": fjPR(),
		"sender":       map[string]any{"id": float64(-2), "login": "forgejo-actions"},
	})
	ev, err := fjMap(t, fjOpts("pull_request"), payload)
	require.NoError(t, err)
	assert.Equal(t, normevent.ActorBot, ev.Actor.Kind)
	assert.Equal(t, normevent.TransitionSynchronized, ev.Transition.Kind)

	opts := fjOpts("issues")
	opts.ResolveRole = nil
	ev, err = fjMap(t, opts, fjPayload("opened", map[string]any{"issue": fjIssue()}))
	require.NoError(t, err)
	assert.Equal(t, normevent.RoleNone, ev.Actor.Role, "no resolver fails closed")

	opts.ResolveRole = func(context.Context, string) (normevent.ActorRole, error) {
		return normevent.RoleAdmin, errors.New("lookup failed")
	}
	ev, err = fjMap(t, opts, fjPayload("opened", map[string]any{"issue": fjIssue()}))
	require.NoError(t, err)
	assert.Equal(t, normevent.RoleNone, ev.Actor.Role, "failed lookup fails closed")

	opts.ResolveRole = func(context.Context, string) (normevent.ActorRole, error) { return "", nil }
	ev, err = fjMap(t, opts, fjPayload("opened", map[string]any{"issue": fjIssue()}))
	require.NoError(t, err)
	assert.Equal(t, normevent.RoleNone, ev.Actor.Role, "empty role fails closed")
}

func TestMapForgejoEvent_DeterministicIdentity(t *testing.T) {
	payload := fjPayload("created", map[string]any{"issue": fjIssue(), "comment": map[string]any{"id": float64(42), "body": "/fs-triage"}})
	first, err := fjMap(t, fjOpts("issue_comment"), payload)
	require.NoError(t, err)
	second, err := fjMap(t, fjOpts("issue_comment"), payload)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestMapForgejoEvent_MalformedOrUnsupported(t *testing.T) {
	githubIssue := map[string]any{
		"action": "labeled",
		"issue": map[string]any{
			"number":   float64(12),
			"html_url": "https://github.com/acme/widgets/issues/12",
			"labels":   fjLabels("ready-to-code"),
		},
		"label":      map[string]any{"name": "ready-to-code"},
		"repository": map[string]any{"full_name": fjRepo},
		"sender":     map[string]any{"login": "alice", "type": "User"},
	}
	otherInstance := fjIssue()
	otherInstance["html_url"] = "https://other.example.com/forgejo/" + fjRepo + "/issues/12"
	otherRepoURL := fjIssue()
	otherRepoURL["html_url"] = fjServer + "/acme/gadgets/issues/12"
	prAsIssue := fjIssue()
	prAsIssue["pull_request"] = map[string]any{"html_url": fjServer + "/" + fjRepo + "/pulls/12"}
	noNumber := fjIssue()
	delete(noNumber, "number")
	noURL := fjIssue()
	delete(noURL, "html_url")
	noHead := fjPR()
	noHead["head"].(map[string]any)["sha"] = ""
	noBase := fjPR()
	delete(noBase, "base")
	foreignBase := fjPR()
	foreignBase["base"].(map[string]any)["repo"] = map[string]any{"full_name": "acme/gadgets"}
	badPRURL := fjPR()
	badPRURL["html_url"] = "https://github.com/acme/widgets/pull/34"

	tests := []struct {
		name      string
		eventName string
		raw       string
		payload   map[string]any
		want      string
	}{
		{name: "empty payload", eventName: "issues", raw: " ", want: "parsing issues payload"},
		{name: "invalid JSON", eventName: "issues", raw: "{not json", want: "parsing issues payload"},
		{name: "trailing data", eventName: "issues", raw: `{"action":"opened"} {}`, want: "trailing data"},
		{name: "wrong field type", eventName: "issues", raw: `{"action":"opened","issue":{"number":"twelve"}}`, want: "parsing issues payload"},
		{name: "missing repository", eventName: "issues", payload: map[string]any{"action": "opened", "sender": fjUser("alice"), "issue": fjIssue()}, want: "missing repository.full_name"},
		{name: "other repository", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": fjIssue(), "repository": map[string]any{"full_name": "acme/gadgets"}}), want: "does not match configured repository"},
		{name: "missing sender", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": fjIssue(), "sender": nil}), want: "missing sender.login"},
		{name: "missing action", eventName: "issues", payload: fjPayload("", map[string]any{"issue": fjIssue()}), want: "missing action"},
		{name: "unknown event name", eventName: "push", payload: fjPayload("opened", nil), want: `unsupported event name "push"`},
		{name: "unknown issues action", eventName: "issues", payload: fjPayload("transferred", map[string]any{"issue": fjIssue()}), want: "unsupported issues action"},
		{name: "unknown pr action", eventName: "pull_request", payload: fjPayload("converted_to_draft", map[string]any{"pull_request": fjPR()}), want: "unsupported pull_request action"},
		{name: "unknown comment action", eventName: "issue_comment", payload: fjPayload("pinned", map[string]any{"issue": fjIssue(), "comment": map[string]any{"id": float64(1)}}), want: "unsupported issue_comment action"},
		{name: "missing issue", eventName: "issues", payload: fjPayload("opened", nil), want: "missing issue"},
		{name: "comment missing issue", eventName: "issue_comment", payload: fjPayload("created", map[string]any{"comment": map[string]any{"id": float64(1)}}), want: "missing issue"},
		{name: "missing comment", eventName: "issue_comment", payload: fjPayload("created", map[string]any{"issue": fjIssue()}), want: "missing comment"},
		{name: "comment without id", eventName: "issue_comment", payload: fjPayload("created", map[string]any{"issue": fjIssue(), "comment": map[string]any{"body": "hi"}}), want: "comment.id"},
		{name: "missing pull request", eventName: "pull_request", payload: fjPayload("opened", nil), want: "missing pull_request"},
		{name: "issue missing number", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": noNumber}), want: "issue.number"},
		{name: "issue missing url", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": noURL}), want: "missing html_url"},
		{name: "issues event for a pull request", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": prAsIssue}), want: "issue is a pull request"},
		{name: "other instance", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": otherInstance}), want: "configured Forgejo instance"},
		{name: "other repository URL", eventName: "issues", payload: fjPayload("opened", map[string]any{"issue": otherRepoURL}), want: "configured Forgejo instance"},
		{name: "github-shaped payload", eventName: "issues", payload: githubIssue, want: "configured Forgejo instance"},
		{name: "pr without head sha", eventName: "pull_request", payload: fjPayload("opened", map[string]any{"pull_request": noHead}), want: "head.sha is required"},
		{name: "pr without base", eventName: "pull_request", payload: fjPayload("opened", map[string]any{"pull_request": noBase}), want: "base is required"},
		{name: "pr with foreign base", eventName: "pull_request", payload: fjPayload("opened", map[string]any{"pull_request": foreignBase}), want: "base.repo"},
		{name: "pr with github url", eventName: "pull_request", payload: fjPayload("opened", map[string]any{"pull_request": badPRURL}), want: "configured Forgejo instance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.raw)
			if tc.payload != nil {
				var err error
				data, err = json.Marshal(tc.payload)
				require.NoError(t, err)
			}
			ev, err := input.MapForgejoEvent(context.Background(), fjOpts(tc.eventName), data)
			require.Error(t, err)
			assert.Nil(t, ev)
			assert.Contains(t, err.Error(), tc.want)
			assert.True(t, strings.HasPrefix(err.Error(), "forgejo event:"), err.Error())
		})
	}
}

func TestMapForgejoEvent_OptionValidation(t *testing.T) {
	data, err := json.Marshal(fjPayload("opened", map[string]any{"issue": fjIssue()}))
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*input.ForgejoEventOptions)
		want   string
	}{
		{"no event name", func(o *input.ForgejoEventOptions) { o.EventName = "" }, "event name is not set"},
		{"no repository", func(o *input.ForgejoEventOptions) { o.Repository = "" }, "FORGEJO_REPOSITORY is not set"},
		{"bad repository", func(o *input.ForgejoEventOptions) { o.Repository = "acme/widgets/extra" }, "must be owner/repo"},
		{"traversal repository", func(o *input.ForgejoEventOptions) { o.Repository = "acme/.." }, "must be owner/repo"},
		{"no server", func(o *input.ForgejoEventOptions) { o.ServerURL = "" }, "FORGEJO_SERVER_URL is not set"},
		{"bad server scheme", func(o *input.ForgejoEventOptions) { o.ServerURL = "ftp://code.example.com" }, "server URL must be"},
		{"server with credentials", func(o *input.ForgejoEventOptions) { o.ServerURL = "https://u:p@code.example.com" }, "server URL must be"},
		{"unparsable server", func(o *input.ForgejoEventOptions) { o.ServerURL = "https://code.example.com/%zz" }, "parsing server URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := fjOpts("issues")
			tc.mutate(&opts)
			ev, err := input.MapForgejoEvent(context.Background(), opts, data)
			require.Error(t, err)
			assert.Nil(t, ev)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// A trailing slash on the server URL is accepted.
	opts := fjOpts("issues")
	opts.ServerURL = fjServer + "/"
	_, err = input.MapForgejoEvent(context.Background(), opts, data)
	require.NoError(t, err)

	oversized := make([]byte, 8<<20+1)
	_, err = input.MapForgejoEvent(context.Background(), fjOpts("issues"), oversized)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload exceeds")
}

func TestLoadForgejoEvent_FromEnvironment(t *testing.T) {
	data, err := json.Marshal(fjPayload("opened", map[string]any{"issue": fjIssue()}))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	t.Setenv("FORGEJO_EVENT_PATH", path)
	t.Setenv("FORGEJO_EVENT_NAME", "issues")
	t.Setenv("FORGEJO_REPOSITORY", fjRepo)
	t.Setenv("FORGEJO_SERVER_URL", fjServer)
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	t.Setenv("GITHUB_REPOSITORY", "acme/gadgets")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")

	ev, err := input.LoadForgejoEvent(context.Background(), input.ForgejoEventOptions{})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionOpened, ev.Transition.Kind)
	assert.Equal(t, normevent.RoleNone, ev.Actor.Role)
	assert.Equal(t, "issues", ev.Source.RawType)
}

func TestLoadForgejoEvent_GitHubEnvironmentFallback(t *testing.T) {
	data, err := json.Marshal(fjPayload("opened", map[string]any{"issue": fjIssue()}))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	for _, k := range []string{"FORGEJO_EVENT_PATH", "FORGEJO_EVENT_NAME", "FORGEJO_REPOSITORY", "FORGEJO_SERVER_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("GITHUB_EVENT_PATH", path)
	t.Setenv("GITHUB_EVENT_NAME", "issues")
	t.Setenv("GITHUB_REPOSITORY", fjRepo)
	t.Setenv("GITHUB_SERVER_URL", fjServer)

	ev, err := input.LoadForgejoEvent(context.Background(), input.ForgejoEventOptions{})
	require.NoError(t, err)
	assert.Equal(t, 12, ev.Entity.ID)
}

func TestLoadForgejoEvent_Errors(t *testing.T) {
	for _, k := range []string{"FORGEJO_EVENT_PATH", "GITHUB_EVENT_PATH"} {
		t.Setenv(k, "")
	}
	_, err := input.LoadForgejoEvent(context.Background(), input.ForgejoEventOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FORGEJO_EVENT_PATH is not set")

	_, err = input.LoadForgejoEvent(context.Background(), input.ForgejoEventOptions{
		EventPath: filepath.Join(t.TempDir(), "missing.json"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading event file")
}
