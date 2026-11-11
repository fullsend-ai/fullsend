package e2eauth

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCheck_AuthorPermissions(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.PRAuthorLogin = "author"

	h.setRole("author", "write")
	h.writePR("author", nil, "")
	h.runCase(t, "write", in, true, "trusted_author", false)

	h.setRole("author", "maintain")
	h.runCase(t, "maintain", in, true, "trusted_author", false)

	h.setRole("author", "admin")
	h.runCase(t, "admin", in, true, "trusted_author", false)

	h.setRole("author", "triage")
	h.runCase(t, "triage denied", in, false, "unauthorized", false)

	h.setRole("author", "read")
	h.runCase(t, "read denied", in, false, "unauthorized", false)

	h.setRole("author", "custom:Developer:write")
	h.runCase(t, "custom write", in, true, "trusted_author", false)

	h.setRole("author", "custom:TriageHelper:read")
	h.runCase(t, "custom read denied", in, false, "unauthorized", false)

	h.setRole("author", "mismatch:write:read")
	h.runCase(t, "mismatch denied", in, false, "unauthorized", false)
}

func TestCheck_PermissionAPIErrors(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.PRAuthorLogin = "author"
	h.writePR("author", nil, "")

	h.fc.CollaboratorPermissionErrors = map[string]error{
		collabKey("author"): fmt.Errorf("simulated permission API failure"),
	}
	h.runCase(t, "author permission failure", in, false, "error", false)

	h.fc.CollaboratorPermissionErrors = nil
	h.fc.CollaboratorPermissionDetails = map[string]forge.GitHubCollaboratorPermissionDetail{
		collabKey("author"): {RoleName: "write", Permission: ""},
	}
	h.runCase(t, "missing permission field", in, false, "error", false)
}

func TestCheck_TrustedBots(t *testing.T) {
	h := newHarness()
	in := baseInput()

	in.PRAuthorLogin = "renovate-fullsend[bot]"
	h.writePR("renovate-fullsend[bot]", nil, "")
	h.runCase(t, "renovate", in, true, "trusted_bot", false)

	in.PRAuthorLogin = "fullsend-ai-coder[bot]"
	h.writePR("fullsend-ai-coder[bot]", nil, "")
	h.runCase(t, "coder bot", in, true, "trusted_bot", false)

	in.PRAuthorLogin = "some-other-bot[bot]"
	h.writePR("some-other-bot[bot]", nil, "")
	h.runCase(t, "unknown bot", in, false, "unauthorized", false)
}

func TestCheck_OkToTestLifecycle(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.PRAuthorLogin = "contributor"
	h.setRole("contributor", "none")
	h.setRole("labeler", "write")
	h.writePR("contributor", []string{OkToTestLabel}, "")

	in.EventAction = "synchronize"
	in.PRUpdatedAt = "2026-06-01T10:00:00Z"
	h.setEvents([]forge.IssueLabelEvent{
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
	})
	h.runCase(t, "fresh after push", in, false, "fresh_ok_to_test", false)

	in.PRUpdatedAt = "2026-06-01T12:00:00Z"
	h.runCase(t, "stale dry run", in, false, "stale_ok_to_test", true)

	in.DryRun = false
	h.runCase(t, "stale removes label", in, false, "stale_ok_to_test", true)
	assert.Contains(t, h.fc.RemovedIssueLabels, prKey+"/"+OkToTestLabel)
}

func TestCheck_LabeledOkToTest(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.EventAction = "labeled"
	in.LabelActorLogin = "labeler"
	h.setRole("labeler", "write")
	h.writePR("contributor", []string{OkToTestLabel}, "")
	h.setEvents([]forge.IssueLabelEvent{
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
	})

	in.PRAuthorLogin = "contributor"
	h.setRole("contributor", "none")
	h.runCase(t, "labeled ok", in, true, "ok_to_test", false)

	in.LabelActorLogin = ""
	h.runCase(t, "missing sender", in, false, "untrusted_labeler", false)

	in.LabelActorLogin = "triager"
	h.setRole("triager", "triage")
	h.setEvents([]forge.IssueLabelEvent{
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
	})
	in.DryRun = false
	h.runCase(t, "triage labeler", in, false, "untrusted_labeler", true)
}

func TestCheck_MissingAuthor(t *testing.T) {
	h := newHarness()
	in := baseInput()
	h.writePR("", nil, "2026-06-01T10:00:00Z")
	h.runCase(t, "missing author", in, false, "error", false)
}

func TestCheck_RemoveLabel404(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.DryRun = false
	in.PRAuthorLogin = "contributor"
	in.EventAction = "labeled"
	in.LabelActorLogin = "triager"
	h.setRole("contributor", "none")
	h.setRole("triager", "triage")
	h.writePR("contributor", []string{OkToTestLabel}, "")
	h.setEvents([]forge.IssueLabelEvent{
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
	})
	h.fc.Errors = map[string]error{"RemoveIssueLabel": forge.ErrNotFound}
	h.runCase(t, "delete 404", in, false, "untrusted_labeler", true)
}

func TestCheck_BotAuthorOkToTest(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.EventAction = "labeled"
	in.LabelActorLogin = "labeler"
	in.PRAuthorLogin = "app-bot"
	h.fc.CollaboratorPermissionDetails[collabKey("app-bot")] = forge.GitHubCollaboratorPermissionDetail{
		RoleName: "", Permission: "none",
	}
	h.setRole("labeler", "write")
	h.writePR("app-bot", []string{OkToTestLabel}, "")
	h.runCase(t, "bot ok-to-test", in, true, "ok_to_test", false)
}

func TestCheck_FrozenUntrustedKeepsNewerLabel(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.DryRun = true
	in.EventAction = "labeled"
	in.LabelActorLogin = "triager"
	in.PRAuthorLogin = "contributor"
	h.setRole("contributor", "none")
	h.setRole("triager", "triage")
	h.writePR("contributor", []string{OkToTestLabel}, "")
	h.setEvents([]forge.IssueLabelEvent{
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:05:00Z", ActorLogin: "labeler"},
	})
	h.runCase(t, "newer label kept", in, false, "untrusted_labeler", false)
}

func TestCheck_TrustedAuthorIgnoresEventsFailure(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.PRAuthorLogin = "author"
	h.setRole("author", "write")
	h.writePR("author", nil, "")
	h.fc.Errors = map[string]error{"ListIssueLabelEvents": fmt.Errorf("events down")}
	h.runCase(t, "write author", in, true, "trusted_author", false)
}
