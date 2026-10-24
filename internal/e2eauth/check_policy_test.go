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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			tc.setup(h)
			got := Check(context.Background(), h.gate, tc.in())
			assert.Equal(t, tc.wantAuth, got.Authorized, "authorized")
			assert.Equal(t, tc.wantReason, got.Reason, "reason")
			assert.Equal(t, tc.wantLabelRemoved, got.LabelRemoved, "label_removed")
		})
	}
}
