package authorization

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

var agentCommandPattern = regexp.MustCompile(`(?:^|\s)/fs-[a-z][a-z0-9-]*`)

// CheckStale reports whether an allowed label was invalidated by a subsequent
// non-collaborator agent-influencing comment. triggerCommentID is exempt.
func CheckStale(ctx context.Context, client forge.Client, owner, repo string, number int, gate Gate, allowedAt time.Time, triggerCommentID int) (bool, error) {
	comments, err := client.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		return false, err
	}

	for _, c := range comments {
		if triggerCommentID > 0 && c.ID == triggerCommentID {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			continue
		}
		if !createdAt.After(allowedAt) {
			continue
		}
		if !IsAgentInfluencingComment(c.Body) {
			continue
		}
		assoc, err := client.GetCommentAuthorAssociation(ctx, owner, repo, number, c.ID)
		if err != nil {
			return false, err
		}
		if IsNonCollaboratorAssociation(assoc) {
			return true, nil
		}
	}
	return false, nil
}

// IsAgentInfluencingComment reports whether a comment could re-dispatch an agent.
func IsAgentInfluencingComment(body string) bool {
	return agentCommandPattern.MatchString(body)
}

// IsNonCollaboratorAssociation reports whether assoc indicates the author lacks
// write access on the repository.
func IsNonCollaboratorAssociation(assoc string) bool {
	switch strings.ToUpper(strings.TrimSpace(assoc)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return false
	default:
		return true
	}
}
