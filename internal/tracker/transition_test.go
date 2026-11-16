package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type transitionJira struct {
	*FakeJiraClient
	transitions       []jira.Transition
	listErr, applyErr error
	calls             int
	key, id, comment  string
	fields            map[string]any
}

func (f *transitionJira) ListTransitions(context.Context, string) ([]jira.Transition, error) {
	return f.transitions, f.listErr
}
func (f *transitionJira) ApplyTransition(_ context.Context, key, id string, fields map[string]any, comment string) error {
	f.calls++
	f.key, f.id, f.fields, f.comment = key, id, fields, comment
	return f.applyErr
}

func TestJiraTransition(t *testing.T) {
	review := jira.Transition{ID: "21", Name: "Review", To: jira.Status{Name: "In Review"}}
	close := jira.Transition{ID: "31", Name: "Close", To: jira.Status{Name: "Done", StatusCategory: jira.StatusCategory{Key: "done"}}}
	for _, tt := range []struct {
		name              string
		opts              TransitionOptions
		transitions       []jira.Transition
		listErr, applyErr error
		wantErr           string
	}{
		{name: "named", opts: TransitionOptions{Name: "Review", Comment: "**Ready**"}, transitions: []jira.Transition{review, close}},
		{name: "status", opts: TransitionOptions{Status: "In Review"}, transitions: []jira.Transition{review}},
		{name: "semantic close", opts: TransitionOptions{State: "closed"}, transitions: []jira.Transition{review, close}},
		{name: "missing", opts: TransitionOptions{Name: "Review"}, wantErr: "no matching"},
		{name: "case sensitive", opts: TransitionOptions{Name: "review"}, transitions: []jira.Transition{review}, wantErr: "no matching"},
		{name: "ambiguous", opts: TransitionOptions{Name: "Review"}, transitions: []jira.Transition{review, review}, wantErr: "ambiguous"},
		{name: "ambiguous close", opts: TransitionOptions{State: "closed"}, transitions: []jira.Transition{close, close}, wantErr: "ambiguous"},
		{name: "no id", opts: TransitionOptions{Name: "Review"}, transitions: []jira.Transition{{Name: "Review"}}, wantErr: "no ID"},
		{name: "list error", opts: TransitionOptions{Name: "Review"}, listErr: forge.ErrNotFound, wantErr: "not found"},
		{name: "apply error", opts: TransitionOptions{Name: "Review"}, transitions: []jira.Transition{review}, applyErr: errors.New("permission denied"), wantErr: "permission denied"},
		{name: "required field", opts: TransitionOptions{Name: "Review"}, transitions: []jira.Transition{{ID: "21", Name: "Review", Fields: map[string]jira.TransitionField{"customfield_123": {Required: true}}}}},
		{name: "invalid selectors", opts: TransitionOptions{Name: "Review", State: "closed"}, wantErr: "exactly one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &transitionJira{FakeJiraClient: &FakeJiraClient{Issues: map[string]*jira.Issue{"PROJ-1": {}}}, transitions: tt.transitions, listErr: tt.listErr, applyErr: tt.applyErr}
			c, err := NewJiraClient(f, "https://example.atlassian.net")
			require.NoError(t, err)
			result, err := c.TransitionIssue(context.Background(), "PROJ", 1, tt.opts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				if tt.applyErr == nil {
					assert.Zero(t, f.calls)
				}
				return
			}
			require.NoError(t, err)
			assert.True(t, result.Changed)
			assert.Equal(t, 1, f.calls)
			assert.Equal(t, "PROJ-1", f.key)
			assert.Equal(t, string(tt.opts.Comment), f.comment)
		})
	}
}

func TestTransitionFields(t *testing.T) {
	resolution := jira.TransitionField{Required: true, AllowedValues: []json.RawMessage{json.RawMessage(`{"id":"1","name":"Fixed"}`), json.RawMessage(`{"id":"2","name":"Duplicate"}`)}}
	for _, tt := range []struct {
		name, res string
		fields    map[string]jira.TransitionField
		wantErr   string
	}{
		{"name", "Fixed", map[string]jira.TransitionField{"resolution": resolution}, ""},
		{"id", "2", map[string]jira.TransitionField{"resolution": resolution}, ""},
		{"required", "", map[string]jira.TransitionField{"resolution": resolution}, "requires a resolution"},
		{"invalid", "unknown", map[string]jira.TransitionField{"resolution": resolution}, "must match one"},
		{"not on screen", "Fixed", nil, "does not accept"},
		{"default", "", map[string]jira.TransitionField{"resolution": {Required: true, HasDefaultValue: true}}, ""},
		{"optional", "", map[string]jira.TransitionField{"summary": {}}, ""},
		{"ambiguous", "Fixed", map[string]jira.TransitionField{"resolution": {AllowedValues: []json.RawMessage{resolution.AllowedValues[0], resolution.AllowedValues[0]}}}, "must match one"},
		{"bad data", "Fixed", map[string]jira.TransitionField{"resolution": {AllowedValues: []json.RawMessage{json.RawMessage(`"bad"`), json.RawMessage(`{"name":"Fixed"}`)}}}, "must match one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fields, err := transitionFields(jira.Transition{Name: "Close", Fields: tt.fields}, tt.res, "")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				if tt.res != "" {
					assert.NotEmpty(t, fields["resolution"])
				}
			}
		})
	}
}

func TestJiraTransitionAlreadyClosed(t *testing.T) {
	f := &transitionJira{FakeJiraClient: &FakeJiraClient{Issues: map[string]*jira.Issue{"P-1": {Fields: jira.IssueFields{Status: jira.Status{Name: "Done", StatusCategory: jira.StatusCategory{Key: "done"}}}}}}, transitions: []jira.Transition{{ID: "1", Name: "Close"}}}
	c, err := NewJiraClient(f, "https://example.atlassian.net")
	require.NoError(t, err)
	result, err := c.TransitionIssue(context.Background(), "P", 1, TransitionOptions{State: "closed"})
	require.NoError(t, err)
	assert.False(t, result.Changed)
	assert.Zero(t, f.calls)
	// Explicit named actions still execute even on a done issue (self-transitions).
	_, err = c.TransitionIssue(context.Background(), "P", 1, TransitionOptions{Name: "Close"})
	require.NoError(t, err)
	assert.Equal(t, 1, f.calls)
	_, err = c.TransitionIssue(context.Background(), "P", 2, TransitionOptions{State: "closed"})
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
	plain, err := NewJiraClient(&FakeJiraClient{}, "https://example.atlassian.net")
	require.NoError(t, err)
	_, err = plain.TransitionIssue(context.Background(), "P", 1, TransitionOptions{Name: "Close"})
	assert.ErrorIs(t, err, ErrNotSupported)
}

type transitionForge struct {
	forge.Client
	state                        string
	isPullRequest                bool
	getErr, closeErr, commentErr error
	closes, comments             int
	project                      string
	number                       int
}

func (f *transitionForge) GetIssue(_ context.Context, owner, repo string, number int) (*forge.Issue, error) {
	f.project = owner + "/" + repo
	f.number = number
	return &forge.Issue{State: f.state, IsPullRequest: f.isPullRequest}, f.getErr
}
func (f *transitionForge) CloseIssue(context.Context, string, string, int) error {
	f.closes++
	return f.closeErr
}
func (f *transitionForge) CreateIssueComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	f.comments++
	return &forge.IssueComment{}, f.commentErr
}
func TestForgeTransition(t *testing.T) {
	for _, tt := range []struct {
		name, state                  string
		opts                         TransitionOptions
		getErr, closeErr, commentErr error
		wantErr                      string
		closes, comments             int
	}{
		{name: "close with comment", opts: TransitionOptions{State: "closed", Comment: "done"}, closes: 1, comments: 1},
		{name: "already closed comment", state: "closed", opts: TransitionOptions{State: "closed", Comment: "done"}, comments: 1},
		{name: "already closed", state: "closed", opts: TransitionOptions{State: "closed"}},
		{name: "unsupported", opts: TransitionOptions{Name: "Review"}, wantErr: "not supported"},
		{name: "resolution", opts: TransitionOptions{State: "closed", Resolution: "Fixed"}, wantErr: "not supported"},
		{name: "invalid", opts: TransitionOptions{State: "open"}, wantErr: "unsupported lifecycle"},
		{name: "get fails", opts: TransitionOptions{State: "closed"}, getErr: forge.ErrNotFound, wantErr: "not found"},
		{name: "close fails", opts: TransitionOptions{State: "closed", Comment: "done"}, closeErr: errors.New("close denied"), wantErr: "close denied", closes: 1},
		{name: "comment fails", opts: TransitionOptions{State: "closed", Comment: "done"}, commentErr: errors.New("comment denied"), wantErr: "issue is closed, but", closes: 1, comments: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &transitionForge{state: tt.state, getErr: tt.getErr, closeErr: tt.closeErr, commentErr: tt.commentErr}
			c := NewForgeClient(f)
			result, err := c.TransitionIssue(context.Background(), "group/sub/repo", 7, tt.opts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.state != "closed", result.Changed)
				assert.Equal(t, "group/sub/repo", f.project)
				assert.Equal(t, 7, f.number)
			}
			assert.Equal(t, tt.closes, f.closes)
			assert.Equal(t, tt.comments, f.comments)
		})
	}
	_, err := NewForgeClient(&transitionForge{}).TransitionIssue(context.Background(), "invalid", 1, TransitionOptions{State: "closed"})
	require.Error(t, err)
}

func TestTransitionRequiredComment(t *testing.T) {
	tr := jira.Transition{ID: "21", Name: "Review", Fields: map[string]jira.TransitionField{"comment": {Required: true}}}
	f := &transitionJira{FakeJiraClient: &FakeJiraClient{}, transitions: []jira.Transition{tr}}
	c, err := NewJiraClient(f, "https://example.atlassian.net")
	require.NoError(t, err)
	_, err = c.TransitionIssue(context.Background(), "P", 1, TransitionOptions{Name: "Review"})
	require.ErrorContains(t, err, "requires a comment")
	assert.Zero(t, f.calls)
	_, err = c.TransitionIssue(context.Background(), "P", 1, TransitionOptions{Name: "Review", Comment: "Ready for review"})
	require.NoError(t, err)
	assert.Equal(t, "Ready for review", f.comment)
	assert.Equal(t, 1, f.calls)
}

func TestJiraAlreadyClosedResolutionDoesNotWrite(t *testing.T) {
	for _, comment := range []Body{"", "reason"} {
		f := &transitionJira{FakeJiraClient: &FakeJiraClient{Issues: map[string]*jira.Issue{"P-1": {Fields: jira.IssueFields{Status: jira.Status{StatusCategory: jira.StatusCategory{Key: "done"}}}}}}}
		c, err := NewJiraClient(f, "https://example.atlassian.net")
		require.NoError(t, err)
		_, err = c.TransitionIssue(context.Background(), "P", 1, TransitionOptions{State: "closed", Resolution: "Fixed", Comment: comment})
		require.ErrorContains(t, err, "already closed")
		assert.Zero(t, f.calls)
		assert.Empty(t, f.Comments)
	}
}

func TestTransitionAutomaticResolution(t *testing.T) {
	for _, tt := range []struct {
		name                string
		values              []json.RawMessage
		optional, defaulted bool
		wantID              string
		wantErr             bool
	}{
		{name: "single", values: []json.RawMessage{json.RawMessage(`{"id":"1","name":"Fixed"}`)}, wantID: "1"},
		{name: "none", wantErr: true},
		{name: "multiple", values: []json.RawMessage{json.RawMessage(`{"id":"1"}`), json.RawMessage(`{"id":"2"}`)}, wantErr: true},
		{name: "malformed alternative", values: []json.RawMessage{json.RawMessage(`{"id":"1"}`), json.RawMessage(`null`)}, wantErr: true},
		{name: "malformed", values: []json.RawMessage{json.RawMessage(`"bad"`)}, wantErr: true},
		{name: "missing ID", values: []json.RawMessage{json.RawMessage(`{"name":"Fixed"}`)}, wantErr: true},
		{name: "empty ID", values: []json.RawMessage{json.RawMessage(`{"id":""}`)}, wantErr: true},
		{name: "whitespace ID", values: []json.RawMessage{json.RawMessage(`{"id":" "}`)}, wantErr: true},
		{name: "numeric ID", values: []json.RawMessage{json.RawMessage(`{"id":1}`)}, wantErr: true},
		{name: "optional", values: []json.RawMessage{json.RawMessage(`{"id":"1"}`)}, optional: true},
		{name: "default", values: []json.RawMessage{json.RawMessage(`{"id":"1"}`)}, defaulted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tr := jira.Transition{Name: "Close", Fields: map[string]jira.TransitionField{"resolution": {Required: !tt.optional, HasDefaultValue: tt.defaulted, AllowedValues: tt.values}}}
			fields, err := transitionFields(tr, "", "")
			if tt.wantErr {
				require.ErrorContains(t, err, "requires a resolution")
				return
			}
			require.NoError(t, err)
			if tt.wantID != "" {
				assert.Equal(t, map[string]string{"id": tt.wantID}, fields["resolution"])
			} else {
				assert.Empty(t, fields)
			}
		})
	}
}

func TestJiraRequiredFieldsValidatedByServer(t *testing.T) {
	for _, field := range []string{"summary", "assignee", "customfield_123"} {
		for _, serverErr := range []error{nil, errors.New("Jira API: 400: required field is empty")} {
			t.Run(field+"/"+fmt.Sprint(serverErr), func(t *testing.T) {
				f := &transitionJira{FakeJiraClient: &FakeJiraClient{}, transitions: []jira.Transition{{ID: "21", Name: "Review", Fields: map[string]jira.TransitionField{field: {Required: true}}}}, applyErr: serverErr}
				c, err := NewJiraClient(f, "https://example.atlassian.net")
				require.NoError(t, err)
				_, err = c.TransitionIssue(context.Background(), "P", 1, TransitionOptions{Name: "Review", Comment: "Ready"})
				if serverErr != nil {
					require.ErrorIs(t, err, serverErr)
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, 1, f.calls)
				assert.Empty(t, f.fields)
				assert.Equal(t, "Ready", f.comment)
				assert.Empty(t, f.Comments)
			})
		}
	}
}

func TestForgeTransitionRejectsPullRequests(t *testing.T) {
	for _, state := range []string{"open", "closed"} {
		for _, comment := range []Body{"", "Completed"} {
			t.Run(state+"/"+string(comment), func(t *testing.T) {
				f := &transitionForge{state: state, isPullRequest: true}
				result, err := NewForgeClient(f).TransitionIssue(context.Background(), "owner/repo", 1, TransitionOptions{State: "closed", Comment: comment})
				require.ErrorContains(t, err, "pull request, not an issue")
				assert.Nil(t, result)
				assert.Zero(t, f.closes)
				assert.Zero(t, f.comments)
			})
		}
	}
}
