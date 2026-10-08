package harnessdispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const authorizationDeniedMarkerPrefix = "<!-- fullsend:authorization-denied:"

// AuthorizationDeniedComment posts one explanatory comment for an
// unauthorized human dispatch attempt. The marker includes the actor so a
// separate user can still receive their own explanation on the same issue.
// Listing before creating mirrors the existing sticky-comment contract and
// keeps repeated webhook deliveries quiet.
func AuthorizationDeniedComment(ctx context.Context, client forge.Client, event *normevent.Event) error {
	if client == nil || event == nil || event.Source.System != normevent.SystemGitHub ||
		event.Actor.Kind != normevent.ActorHuman || event.Actor.ID == "" || event.Entity.ID < 1 {
		return nil
	}
	if !validGitHubLogin(event.Actor.ID) {
		return nil
	}

	parts := strings.SplitN(event.Repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repository %q", event.Repo)
	}

	marker := authorizationDeniedMarker(event.Actor.ID)
	comments, err := client.ListIssueComments(ctx, parts[0], parts[1], event.Entity.ID)
	if err != nil {
		return fmt.Errorf("list authorization-denial comments: %w", err)
	}
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			return nil
		}
	}

	body := marker + "\n\n@" + event.Actor.ID +
		", this agent launch was blocked because you are not authorized to launch Fullsend agents on this work item. " +
		"Ask a repository maintainer for access."
	if _, err := client.CreateIssueComment(ctx, parts[0], parts[1], event.Entity.ID, body); err != nil {
		return fmt.Errorf("create authorization-denial comment: %w", err)
	}
	return nil
}

func authorizationDeniedMarker(actor string) string {
	return authorizationDeniedMarkerPrefix + actor + " -->"
}

func validGitHubLogin(login string) bool {
	for _, r := range login {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return login != ""
}
