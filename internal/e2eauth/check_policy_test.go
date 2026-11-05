package e2eauth

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCheck_PolicyTable(t *testing.T) {
	cases := []struct {
		name             string
		setup            func(h *harness)
		in               func() Input
		wantAuth         bool
		wantReason       string
		wantLabelRemoved bool
	}{
		{
			name: "write author ignores stale ok-to-test label",
			setup: func(h *harness) {
				h.setRole("author", "write")
				h.writePR("author", []string{OkToTestLabel}, "")
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "author"
				return in
			},
			wantAuth: true, wantReason: "trusted_author", wantLabelRemoved: false,
		},
		{
			name: "reopened fresh ok-to-test",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
				})
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "reopened"
				in.PRUpdatedAt = "2026-06-01T10:00:00Z"
				return in
			},
			wantAuth: false, wantReason: "fresh_ok_to_test", wantLabelRemoved: false,
		},
		{
			name: "reopened stale ok-to-test",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
				})
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "reopened"
				in.PRUpdatedAt = "2026-06-01T12:00:00Z"
				return in
			},
			wantAuth: false, wantReason: "stale_ok_to_test", wantLabelRemoved: true,
		},
		{
			name: "frozen trusted sender no events needed",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.setRole("labeler", "write")
				h.writePR("contributor", []string{OkToTestLabel}, "")
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "labeled"
				in.LabelActorLogin = "labeler"
				return in
			},
			wantAuth: true, wantReason: "ok_to_test", wantLabelRemoved: false,
		},
		{
			name: "collaborator maruiz93 admin via permissions map",
			setup: func(h *harness) {
				h.fc.CollaboratorPermissions[collabKey("maruiz93")] = "admin"
				h.writePR("maruiz93", nil, "")
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "maruiz93"
				return in
			},
			wantAuth: true, wantReason: "trusted_author", wantLabelRemoved: false,
		},
		{
			name: "events API failure on stale path",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.fc.Errors = map[string]error{"ListIssueLabelEvents": fmt.Errorf("events down")}
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "reopened"
				in.PRUpdatedAt = "2026-06-01T10:00:00Z"
				return in
			},
			wantAuth: false, wantReason: "error", wantLabelRemoved: false,
		},
		{
			name: "ok-to-test at same time as push is stale",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T12:00:00Z", ActorLogin: "labeler"},
				})
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "synchronize"
				in.PRUpdatedAt = "2026-06-01T12:00:00Z"
				return in
			},
			wantAuth: false, wantReason: "stale_ok_to_test", wantLabelRemoved: true,
		},
		{
			name: "falls back to pull updated_at when PR_UPDATED_AT unset",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", []string{OkToTestLabel}, "2026-06-01T10:00:00Z")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
				})
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "reopened"
				in.PRUpdatedAt = ""
				return in
			},
			wantAuth: false, wantReason: "fresh_ok_to_test", wantLabelRemoved: false,
		},
		{
			name: "untrusted author without ok-to-test label",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", nil, "")
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				return in
			},
			wantAuth: false, wantReason: "unauthorized", wantLabelRemoved: false,
		},
		{
			name: "labeled ok-to-test ignores later non-ok-to-test labels in history",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.setRole("labeler", "write")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
					{LabelName: "component/cli", CreatedAt: "2026-06-01T11:30:00Z", ActorLogin: "triager"},
				})
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "labeled"
				in.LabelActorLogin = "labeler"
				return in
			},
			wantAuth: true, wantReason: "ok_to_test", wantLabelRemoved: false,
		},
		{
			name: "PR context fetch failure",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.fc.Errors = map[string]error{
					"GetPullRequestAuthorizationContext": fmt.Errorf("pull fetch failed"),
				}
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				return in
			},
			wantAuth: false, wantReason: "error", wantLabelRemoved: false,
		},
		{
			name: "label removal failure returns error",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.setRole("triager", "triage")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
				})
				h.fc.Errors = map[string]error{"RemoveIssueLabel": fmt.Errorf("delete failed")}
			},
			in: func() Input {
				in := baseInput()
				in.DryRun = false
				in.PRAuthorLogin = "contributor"
				in.EventAction = "labeled"
				in.LabelActorLogin = "triager"
				return in
			},
			wantAuth: false, wantReason: "error", wantLabelRemoved: false,
		},
		{
			name: "labeler permission API failure on labeled event",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.fc.CollaboratorPermissionErrors = map[string]error{
					collabKey("labeler"): fmt.Errorf("permission API down"),
				}
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "labeled"
				in.LabelActorLogin = "labeler"
				return in
			},
			wantAuth: false, wantReason: "error", wantLabelRemoved: false,
		},
		{
			name: "events API failure during untrusted labeler cleanup",
			setup: func(h *harness) {
				h.setRole("contributor", "none")
				h.setRole("triager", "triage")
				h.writePR("contributor", []string{OkToTestLabel}, "")
				h.setEvents([]forge.IssueLabelEvent{
					{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
				})
				h.fc.Errors = map[string]error{"ListIssueLabelEvents": fmt.Errorf("events down")}
			},
			in: func() Input {
				in := baseInput()
				in.PRAuthorLogin = "contributor"
				in.EventAction = "labeled"
				in.LabelActorLogin = "triager"
				return in
			},
			wantAuth: false, wantReason: "error", wantLabelRemoved: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			tc.setup(h)
			got := Check(context.Background(), h.gate, tc.in())
			assert.Equal(t, tc.wantAuth, got.Authorized, "authorized")
			assert.Equal(t, tc.wantReason, got.Reason, "reason")
			assert.Equal(t, tc.wantLabelRemoved, got.LabelRemoved, "label_removed")
			if tc.wantReason == "error" {
				assert.Error(t, got.Err)
			}
		})
	}
}
