package harnessdispatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestAuthorizationDeniedCommentCreatesOncePerActor(t *testing.T) {
	client := forge.NewFakeClient()
	event := &normevent.Event{
		Repo:   "acme/widgets",
		Entity: normevent.Entity{Kind: normevent.EntityWorkItem, ID: 42, URL: "https://github.com/acme/widgets/issues/42"},
		Actor:  normevent.Actor{ID: "external-user", Kind: normevent.ActorHuman, Role: normevent.RoleNone},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "issue_comment"},
	}

	require.NoError(t, AuthorizationDeniedComment(context.Background(), client, event))
	require.NoError(t, AuthorizationDeniedComment(context.Background(), client, event))

	comments := client.IssueComments["acme/widgets/42"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "<!-- fullsend:authorization-denied:external-user -->")
	assert.Contains(t, comments[0].Body, "@external-user")
}

func TestAuthorizationDeniedCommentSeparatesActors(t *testing.T) {
	client := forge.NewFakeClient()
	event := &normevent.Event{
		Repo:   "acme/widgets",
		Entity: normevent.Entity{Kind: normevent.EntityChangeProposal, ID: 7, URL: "https://github.com/acme/widgets/pull/7"},
		Actor:  normevent.Actor{ID: "first-user", Kind: normevent.ActorHuman, Role: normevent.RoleNone},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "pull_request_target"},
	}

	require.NoError(t, AuthorizationDeniedComment(context.Background(), client, event))
	event.Actor.ID = "second-user"
	require.NoError(t, AuthorizationDeniedComment(context.Background(), client, event))

	comments := client.IssueComments["acme/widgets/7"]
	require.Len(t, comments, 2)
	assert.Contains(t, comments[0].Body, "first-user")
	assert.Contains(t, comments[1].Body, "second-user")
}

func TestAuthorizationDeniedCommentIgnoresNonHumanOrNonGitHub(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   normevent.ActorKind
		system normevent.SourceSystem
	}{
		{name: "bot", kind: normevent.ActorBot, system: normevent.SystemGitHub},
		{name: "gitlab", kind: normevent.ActorHuman, system: normevent.SystemGitLab},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := forge.NewFakeClient()
			event := &normevent.Event{
				Repo:   "acme/widgets",
				Entity: normevent.Entity{Kind: normevent.EntityWorkItem, ID: 42, URL: "https://example.test/42"},
				Actor:  normevent.Actor{ID: "actor", Kind: tc.kind, Role: normevent.RoleNone},
				Source: normevent.Source{System: tc.system, RawType: "issue"},
			}
			require.NoError(t, AuthorizationDeniedComment(context.Background(), client, event))
			assert.Empty(t, client.IssueComments)
		})
	}
}

func TestAuthorizationDeniedCommentIgnoresInvalidLogin(t *testing.T) {
	client := forge.NewFakeClient()
	event := &normevent.Event{
		Repo:   "acme/widgets",
		Entity: normevent.Entity{Kind: normevent.EntityWorkItem, ID: 42, URL: "https://github.com/acme/widgets/issues/42"},
		Actor:  normevent.Actor{ID: "untrusted\nlogin", Kind: normevent.ActorHuman, Role: normevent.RoleNone},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "issue_comment"},
	}
	require.NoError(t, AuthorizationDeniedComment(context.Background(), client, event))
	assert.Empty(t, client.IssueComments)
}
