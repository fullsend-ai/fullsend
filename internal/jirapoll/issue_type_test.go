package jirapoll

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/celtrigger"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/stretchr/testify/require"
)

func TestIssueType_DiscoveryAndCEL(t *testing.T) {
	for _, issueType := range []*jira.IssueType{
		{ID: "10001", Name: "Bug", Subtask: false},
		{ID: "10002", Name: "Custom subtask", Subtask: true},
		nil,
	} {
		name := "missing"
		if issueType != nil {
			name = issueType.Name
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now().Add(-time.Minute).Format(time.RFC3339)
			actor := jira.User{AccountID: "alice", AccountType: "atlassian"}
			issue := jira.Issue{ID: "10042", Key: "PROJ-123", Fields: jira.IssueFields{
				Created: now, Reporter: actor, Labels: []string{}, IssueType: issueType,
			}}
			client := newMockClient()
			client.comments[issue.Key] = []jira.Comment{{ID: "1", Body: "/fs-triage", Created: now, Author: actor}}
			client.changelog[issue.Key] = []jira.ChangelogEntry{{Created: now, Author: actor, Items: []jira.ChangeItem{
				{Field: "labels", ToString: "ready-to-code"},
				{Field: "summary", FromString: "old", ToString: "new"},
				{Field: "status", From: "open", To: "done"},
				{Field: "status", From: "done", To: "open"},
			}}}
			client.statuses["open"] = jira.Status{StatusCategory: jira.StatusCategory{Key: "new"}}
			client.statuses["done"] = jira.Status{StatusCategory: jira.StatusCategory{Key: "done"}}
			p := New(client, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://example.atlassian.net", JiraProject: "PROJ"})
			result, err := p.detectChanges(context.Background(), issue, time.Time{})
			require.NoError(t, err)
			require.Len(t, result.events, 6)
			for _, raw := range result.events {
				event := p.toNormalizedEvent(raw)
				require.NoError(t, event.Validate())
				if issueType == nil {
					require.Nil(t, event.State.IssueType)
				} else {
					require.Equal(t, &normevent.IssueType{ID: issueType.ID, Name: issueType.Name, Subtask: issueType.Subtask}, event.State.IssueType)
				}
				data, err := json.Marshal(event)
				require.NoError(t, err)
				restored, err := normevent.ParseJSON(data)
				require.NoError(t, err)
				require.Equal(t, event.State.IssueType, restored.State.IssueType)
				eventMap, err := restored.ToMap()
				require.NoError(t, err)
				matched, err := celtrigger.Evaluate(`event.source.system == 'jira' && has(event.state.issue_type) && event.state.issue_type.id == '10001' && event.state.issue_type.name == 'Bug' && !event.state.issue_type.subtask`, eventMap)
				require.NoError(t, err)
				require.Equal(t, issueType != nil && issueType.ID == "10001", matched)
			}
		})
	}
}

func TestIssueType_MalformedMetadataOmitted(t *testing.T) {
	for _, tc := range []struct {
		name        string
		issueType   jira.IssueType
		wantPresent bool
	}{
		{name: "empty ID", issueType: jira.IssueType{Name: "Bug"}},
		{name: "empty name", issueType: jira.IssueType{ID: "10001"}},
		{name: "empty fields", issueType: jira.IssueType{}},
		{name: "oversized ID", issueType: jira.IssueType{ID: strings.Repeat("1", 256), Name: "Bug"}},
		{name: "oversized name", issueType: jira.IssueType{ID: "10001", Name: strings.Repeat("界", 256)}},
		{name: "Unicode boundary", issueType: jira.IssueType{ID: strings.Repeat("1", 255), Name: strings.Repeat("界", 255)}, wantPresent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(newMockClient(), nil, Options{TargetRepo: "acme/platform"})
			event := p.toNormalizedEvent(JiraEvent{
				Type: "opened", IssueID: "10042", IssueKey: "PROJ-123",
				IssueURL: "https://example.atlassian.net/browse/PROJ-123",
				Reporter: jira.User{AccountID: "alice"}, IssueType: &tc.issueType,
			})
			require.NoError(t, event.Validate())
			data, err := json.Marshal(event)
			require.NoError(t, err)
			restored, err := normevent.ParseJSON(data)
			require.NoError(t, err)
			eventMap, err := restored.ToMap()
			require.NoError(t, err)
			present, err := celtrigger.Evaluate(`has(event.state.issue_type)`, eventMap)
			require.NoError(t, err)
			require.Equal(t, tc.wantPresent, present)
			if tc.wantPresent {
				require.Equal(t, tc.issueType.Name, restored.State.IssueType.Name)
			} else {
				require.Nil(t, restored.State.IssueType)
			}
		})
	}
}
