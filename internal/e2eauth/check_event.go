package e2eauth

import (
	"fmt"
	"strings"
)

// authorLabelEventNames are github.event_name values that use the PR
// author / ok-to-test label policy. An empty name is allowed for callers
// that omit EVENT_NAME (same as check-e2e-authorization.sh).
var authorLabelEventNames = map[string]struct{}{
	"":                    {},
	"pull_request":        {},
	"pull_request_target": {},
	"labeled":             {},
}

func resolveEventMode(in Input) (string, error) {
	name := strings.TrimSpace(in.EventName)
	if name == "issue_comment" {
		return "issue_comment", nil
	}
	if in.CommentID > 0 {
		return "", fmt.Errorf("COMMENT_ID is set but EVENT_NAME is %q, expected issue_comment", in.EventName)
	}
	if _, ok := authorLabelEventNames[name]; ok {
		return "author", nil
	}
	return "", fmt.Errorf("unsupported EVENT_NAME %q", name)
}
